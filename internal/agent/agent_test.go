package agent

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

func TestInputMessageMarshalsAsPlainStringWithoutImages(t *testing.T) {
	payload, err := json.Marshal(inputMessage{Role: "user", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(payload); got != `{"role":"user","content":"hello"}` {
		t.Fatalf("payload = %s, want the plain {role, content} shape", got)
	}
}

func TestInputMessageMarshalsContentPartsWithImages(t *testing.T) {
	payload, err := json.Marshal(inputMessage{Role: "user", Content: "look at this", Images: []string{"data:image/png;base64,AAAA"}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload %s did not decode as content parts: %v", payload, err)
	}
	if len(decoded.Content) != 2 {
		t.Fatalf("content parts = %d, want 2 (text + image): %s", len(decoded.Content), payload)
	}
	if decoded.Content[0].Type != "input_text" || decoded.Content[0].Text != "look at this" {
		t.Fatalf("first part = %+v, want the text", decoded.Content[0])
	}
	if decoded.Content[1].Type != "input_image" || decoded.Content[1].ImageURL != "data:image/png;base64,AAAA" {
		t.Fatalf("second part = %+v, want the image", decoded.Content[1])
	}
}

func TestChatMessageForBuildsMultimodalPartsForImages(t *testing.T) {
	messages, err := chatMessageFor(inputMessage{Role: "user", Content: "look", Images: []string{"data:image/png;base64,AAAA", "data:image/png;base64,BBBB"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	parts, ok := messages[0].Content.([]map[string]any)
	if !ok {
		t.Fatalf("content = %#v, want the content-parts shape", messages[0].Content)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3 (text + 2 images): %#v", len(parts), parts)
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "look" {
		t.Fatalf("first part = %#v, want the text", parts[0])
	}
	if parts[1]["type"] != "image_url" {
		t.Fatalf("second part = %#v, want an image_url part", parts[1])
	}
}

// schemaName is the structured-output schema a request asked for, which
// is what distinguishes the routing, change and rewrite calls.
func schemaName(body map[string]any) string {
	text, ok := body["text"].(map[string]any)
	if !ok {
		return ""
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := format["name"].(string)
	return name
}

func writeOutputText(writer http.ResponseWriter, text string) {
	response := responseEnvelope{Output: []responseItem{{Type: "message", Content: []contentItem{{Type: "output_text", Text: text}}}}}
	_ = json.NewEncoder(writer).Encode(response)
}

// recordingProgress captures progress output so tests can assert on the
// trail the user would see.
type recordingProgress struct {
	statuses []string
	logs     []string
}

func (progress *recordingProgress) Status(message string) {
	progress.statuses = append(progress.statuses, message)
}

func (progress *recordingProgress) Log(message string) {
	progress.logs = append(progress.logs, message)
}

func TestDecodeJSONHandlesProseAndFences(t *testing.T) {
	type payload struct {
		Value string `json:"value"`
	}
	tests := []string{
		`{"value":"ok"}`,
		"I reviewed the repository. Here is the plan:\n\n" + `{"value":"ok"}`,
		"```json\n" + `{"value":"ok"}` + "\n```",
	}
	for _, text := range tests {
		var got payload
		if err := decodeJSON(text, &got); err != nil {
			t.Fatalf("decodeJSON(%q) error: %v", text, err)
		}
		if got.Value != "ok" {
			t.Fatalf("decodeJSON(%q) = %+v", text, got)
		}
	}
}

func TestDecodeJSONToleratesAStrayClosingBrace(t *testing.T) {
	// Verbatim shape from a real reply: the object is correct but closed
	// one brace too many. Taking the last "}" swallowed the stray one and
	// failed to parse an otherwise perfect answer, so the whole change was
	// thrown away.
	reply := `{
  "summary": "Celebrate a win",
  "content": "if(a){ c() }\n"}
}`
	var rewrite Rewrite
	if err := decodeJSON(reply, &rewrite); err != nil {
		t.Fatalf("decodeJSON() = %v", err)
	}
	if rewrite.Summary != "Celebrate a win" {
		t.Fatalf("Summary = %q", rewrite.Summary)
	}
	// The braces inside the content string must not confuse the scan.
	if !strings.Contains(rewrite.Content, "if(a){ c() }") {
		t.Fatalf("Content = %q", rewrite.Content)
	}
}

func TestDecodeJSONIgnoresTrailingProse(t *testing.T) {
	var rewrite Rewrite
	if err := decodeJSON(`{"summary":"s","content":"c"}

Let me know if you would like anything else!`, &rewrite); err != nil {
		t.Fatalf("decodeJSON() = %v", err)
	}
	if rewrite.Summary != "s" {
		t.Fatalf("Summary = %q", rewrite.Summary)
	}
}

func TestDecodeJSONRejectsNonJSON(t *testing.T) {
	var got map[string]any
	if err := decodeJSON("I can't help with that.", &got); err == nil {
		t.Fatal("expected an error for text with no JSON")
	}
}

func TestDecodeJSONDistinguishesShapeFromInvalidJSON(t *testing.T) {
	// Calling a well-formed object "no valid JSON" sends the reader
	// looking in entirely the wrong place.
	var rewrite Rewrite
	err := decodeJSON(`{"summary":{"nested":"object"},"content":""}`, &rewrite)
	if err == nil || !strings.Contains(err.Error(), "did not match the expected shape") {
		t.Fatalf("err = %v, want a shape mismatch", err)
	}
	err = decodeJSON("I can't help with that.", &rewrite)
	if err == nil || !strings.Contains(err.Error(), "no valid JSON") {
		t.Fatalf("err = %v, want a not-JSON error", err)
	}
}

func TestRewriteTargetsFallsBackToTheFilesRead(t *testing.T) {
	named := rewriteTargets([]string{"a.go", "a.go", ""}, []string{"b.go"}, []string{"c.go"})
	if len(named) != 1 || named[0] != "a.go" {
		t.Fatalf("targets = %v, want just a.go deduplicated", named)
	}
	// A weak model often names no files; the ones it opened are then the
	// best evidence of what it meant.
	fallback := rewriteTargets(nil, []string{"index.html", "index.html"}, []string{"other.html"})
	if len(fallback) != 1 || fallback[0] != "index.html" {
		t.Fatalf("targets = %v, want the file that was read", fallback)
	}
	// With nothing named or read, a single-file folder is unambiguous.
	if only := rewriteTargets(nil, nil, []string{"index.html"}); len(only) != 1 || only[0] != "index.html" {
		t.Fatalf("targets = %v, want the only file in the folder", only)
	}
	// Several files with no other signal stays ambiguous, so no guess.
	if targets := rewriteTargets(nil, nil, []string{"a.go", "b.go"}); len(targets) != 0 {
		t.Fatalf("targets = %v, want none", targets)
	}
	if targets := rewriteTargets(nil, nil, nil); len(targets) != 0 {
		t.Fatalf("targets = %v, want none", targets)
	}
}

func integrationRepository(t *testing.T) *repo.Repository {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("git", "add", "hello.txt")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	return &repo.Repository{Root: root}
}

func TestReadFilesDoesNotResendAnUnchangedFile(t *testing.T) {
	// A model will ask for the same file several times over. Every copy
	// lands in a transcript resent in full on every later turn, so the
	// second answer must be a note rather than the file again.
	root := t.TempDir()
	body := strings.Repeat("x", 5000)
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(nil, &repo.Repository{Root: root})

	first, err := runner.readFiles([]string{"index.html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, body) {
		t.Fatal("the first read must carry the contents")
	}

	second, err := runner.readFiles([]string{"index.html"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(second, body) {
		t.Fatalf("the file was sent again: %d bytes", len(second))
	}
	if !strings.Contains(second, "unchanged") {
		t.Fatalf("second read = %q, want it to say so", second)
	}

	// Once the file actually changes, it has to be sent again.
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(body+"changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := runner.readFiles([]string{"index.html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(third, "changed") {
		t.Fatal("a changed file must be sent again")
	}
}

func TestReadFilesStillReportsMissingFiles(t *testing.T) {
	runner := NewRunner(nil, &repo.Repository{Root: t.TempDir()})
	if _, err := runner.readFiles([]string{"nope.txt"}); err == nil {
		t.Fatal("expected a missing file to be reported")
	}
}

func TestReadFilesMixesFreshAndAlreadySeen(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("body of "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(nil, &repo.Repository{Root: root})
	if _, err := runner.readFiles([]string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	both, err := runner.readFiles([]string{"a.txt", "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(both, "body of a.txt") {
		t.Fatalf("a.txt was resent:\n%s", both)
	}
	if !strings.Contains(both, "body of b.txt") {
		t.Fatalf("b.txt was not sent:\n%s", both)
	}
}

func TestDescribeCallSaysWhenAReadCostsNothing(t *testing.T) {
	// The log prints when the model asks, so a deduplicated re-read used
	// to look identical to fetching the file all over again.
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("body of "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(nil, &repo.Repository{Root: root})
	read := func(paths string) responseItem {
		return responseItem{Name: "read_files", Arguments: `{"paths":[` + paths + `]}`}
	}

	if got := runner.describeCall(read(`"a.txt"`)); got != "read a.txt" {
		t.Fatalf("first read described as %q", got)
	}
	if _, err := runner.readFiles([]string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := runner.describeCall(read(`"a.txt"`)); got != "read a.txt (already shown, not resent)" {
		t.Fatalf("repeat read described as %q", got)
	}
	if got := runner.describeCall(read(`"a.txt","b.txt"`)); got != "read b.txt (a.txt already shown)" {
		t.Fatalf("mixed read described as %q", got)
	}

	// Once the file changes it is genuinely fetched again, and says so.
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("different"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := runner.describeCall(read(`"a.txt"`)); got != "read a.txt" {
		t.Fatalf("changed file described as %q", got)
	}
}
