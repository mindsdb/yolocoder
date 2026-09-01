package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

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

func routeAsCodingTask(writer http.ResponseWriter) {
	fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"coding_task\":true,\"reply\":\"\"}"}]}]}`)
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
  "files_to_modify": ["base_index.html"],
  "diff": "*** Begin Patch\n-  if(a){b()}\n+  if(a){ c() }\n*** End Patch"}
}`
	var change Change
	if err := decodeJSON(reply, &change); err != nil {
		t.Fatalf("decodeJSON() = %v", err)
	}
	if change.Summary != "Celebrate a win" {
		t.Fatalf("Summary = %q", change.Summary)
	}
	// The braces inside the diff string must not confuse the scan.
	if !strings.Contains(change.Diff, "if(a){ c() }") {
		t.Fatalf("Diff = %q", change.Diff)
	}
}

func TestDecodeJSONIgnoresTrailingProse(t *testing.T) {
	var change Change
	if err := decodeJSON(`{"summary":"s","files_to_modify":[],"diff":"d"}

Let me know if you would like anything else!`, &change); err != nil {
		t.Fatalf("decodeJSON() = %v", err)
	}
	if change.Summary != "s" {
		t.Fatalf("Summary = %q", change.Summary)
	}
}

func TestDecodeJSONRejectsNonJSON(t *testing.T) {
	var got map[string]any
	if err := decodeJSON("I can't help with that.", &got); err == nil {
		t.Fatal("expected an error for text with no JSON")
	}
}

func TestChangeAcceptsFilesAsObjects(t *testing.T) {
	// Verbatim from a real reply. This is valid JSON in a perfectly
	// sensible shape; only files_to_modify holding objects rather than
	// strings made the strict decode fail, and rejecting it threw away
	// an otherwise complete answer.
	reply := `{
  "summary": "Rename the page title and visible heading to \"TecTacTris\".",
  "files_to_modify": [
    {"path": "index.html", "changes": ["Change the <title> value."]}
  ],
  "diff": "--- a/index.html\n+++ b/index.html\n"
}`
	var change Change
	if err := decodeJSON(reply, &change); err != nil {
		t.Fatalf("decodeJSON() = %v", err)
	}
	if len(change.FilesToModify) != 1 || change.FilesToModify[0] != "index.html" {
		t.Fatalf("FilesToModify = %v, want index.html", change.FilesToModify)
	}
	if !strings.Contains(change.Summary, "TecTacTris") {
		t.Fatalf("Summary = %q", change.Summary)
	}
}

func TestChangeAcceptsPlainAndSingularFileLists(t *testing.T) {
	var plain Change
	if err := decodeJSON(`{"summary":"s","files_to_modify":["a.go","b.go"],"diff":"d"}`, &plain); err != nil {
		t.Fatal(err)
	}
	if strings.Join(plain.FilesToModify, ",") != "a.go,b.go" {
		t.Fatalf("FilesToModify = %v", plain.FilesToModify)
	}
	var single Change
	if err := decodeJSON(`{"summary":"s","files_to_modify":"only.go","diff":"d"}`, &single); err != nil {
		t.Fatal(err)
	}
	if len(single.FilesToModify) != 1 || single.FilesToModify[0] != "only.go" {
		t.Fatalf("FilesToModify = %v, want only.go", single.FilesToModify)
	}
}

func TestDecodeJSONDistinguishesShapeFromInvalidJSON(t *testing.T) {
	// Calling a well-formed object "no valid JSON" sends the reader
	// looking in entirely the wrong place.
	var change Change
	err := decodeJSON(`{"summary":{"nested":"object"},"files_to_modify":[],"diff":""}`, &change)
	if err == nil || !strings.Contains(err.Error(), "did not match the expected shape") {
		t.Fatalf("err = %v, want a shape mismatch", err)
	}
	err = decodeJSON("I can't help with that.", &change)
	if err == nil || !strings.Contains(err.Error(), "no valid JSON") {
		t.Fatalf("err = %v, want a not-JSON error", err)
	}
}

func TestSalvageChangeRecoversAFilesListFromAnOffScheduleShape(t *testing.T) {
	reply := `{"plan":[{"file":"index.html","changes":["Change the title"]}],"notes":"No other files need modification."}`
	var change Change
	if err := decodeJSON(reply, &change); err != nil {
		t.Fatal(err)
	}
	if len(change.FilesToModify) != 0 {
		t.Fatalf("precondition: expected the schema decode to come up empty, got %v", change.FilesToModify)
	}
	salvageChange(&change, reply)
	if len(change.FilesToModify) != 1 || change.FilesToModify[0] != "index.html" {
		t.Fatalf("FilesToModify = %v, want index.html", change.FilesToModify)
	}
	if change.Summary != "No other files need modification." {
		t.Fatalf("Summary = %q", change.Summary)
	}

	// One that did follow the schema is left alone.
	good := Change{FilesToModify: []string{"a.go"}, Summary: "keep"}
	salvageChange(&good, reply)
	if len(good.FilesToModify) != 1 || good.FilesToModify[0] != "a.go" || good.Summary != "keep" {
		t.Fatalf("a valid change was altered: %+v", good)
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

func TestRunnerReadsThenChangesInOneConversation(t *testing.T) {
	repository := integrationRepository(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			routeAsCodingTask(writer)
		case 2:
			fmt.Fprint(writer, `{"id":"resp_1","output":[{"type":"function_call","name":"read_files","call_id":"call_1","arguments":"{\"paths\":[\"hello.txt\"]}"}]}`)
		case 3:
			// The continuation resends the transcript itself rather than
			// leaning on previous_response_id: not every OpenAI-compatible
			// provider persists server-side response state, and one that
			// doesn't rejects an orphaned function_call_output.
			if _, present := body["previous_response_id"]; present {
				t.Fatalf("previous_response_id must not be sent: %v", body["previous_response_id"])
			}
			input, ok := body["input"].([]any)
			if !ok || len(input) != 3 {
				t.Fatalf("input = %#v, want a 3-item transcript", body["input"])
			}
			call, ok := input[1].(map[string]any)
			if !ok || call["type"] != "function_call" || call["call_id"] != "call_1" {
				t.Fatalf("input[1] = %#v, want the echoed function_call", input[1])
			}
			output, ok := input[2].(map[string]any)
			if !ok || output["type"] != "function_call_output" || output["call_id"] != "call_1" {
				t.Fatalf("input[2] = %#v, want the matching function_call_output", input[2])
			}
			diff := "diff --git a/hello.txt b/hello.txt\n--- a/hello.txt\n+++ b/hello.txt\n@@ -1 +1 @@\n-old\n+new\n"
			payload, _ := json.Marshal(Change{Summary: "Update greeting", FilesToModify: []string{"hello.txt"}, Diff: diff})
			writeOutputText(writer, string(payload))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	progress := &recordingProgress{}
	reply, err := NewRunner(client, repository).Run(context.Background(), "Change old to new", progress)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Update greeting" {
		t.Fatalf("reply = %q", reply)
	}
	trail := strings.Join(progress.logs, "\n")
	for _, want := range []string{"read hello.txt", "plan: Update greeting", "will edit hello.txt", "applied the patch"} {
		if !strings.Contains(trail, want) {
			t.Fatalf("progress log missing %q:\n%s", want, trail)
		}
	}
	content, err := os.ReadFile(filepath.Join(repository.Root, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new\n" {
		t.Fatalf("hello.txt = %q", content)
	}
	// Route, one tool round, one answer. Planning and patching used to be
	// separate calls that shipped every file twice.
	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
}

func TestRunnerRepairsWithinTheSameConversation(t *testing.T) {
	// A retry is only useful if the model can see what it got wrong, and
	// it should cost only the evidence: the file contents are already in
	// the conversation from the tool call.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>\n<title>BLOCK &amp; BOARD</title>\n</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &repo.Repository{Root: root}

	badDiff := "diff --git a/index.html b/index.html\n--- a/index.html\n+++ b/index.html\n@@ -1,3 +1,3 @@\n <html>\n-<title>NOT WHAT IS THERE</title>\n+<title>TICTACTRIS</title>\n </html>\n"
	goodDiff := "diff --git a/index.html b/index.html\n--- a/index.html\n+++ b/index.html\n@@ -1,3 +1,3 @@\n <html>\n-<title>BLOCK &amp; BOARD</title>\n+<title>TICTACTRIS</title>\n </html>\n"

	changes := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch schemaName(body) {
		case "message_route":
			routeAsCodingTask(writer)
		case "code_change":
			changes++
			if changes == 2 {
				// The failed diff and git's own "while searching for"
				// text must reach the model, appended to the existing
				// conversation rather than rebuilt around it.
				input, ok := body["input"].([]any)
				if !ok {
					t.Fatalf("input = %#v, want the running transcript", body["input"])
				}
				last, _ := json.Marshal(input[len(input)-1])
				for _, want := range []string{"THE DIFF THAT FAILED", "while searching for"} {
					if !strings.Contains(string(last), want) {
						t.Fatalf("evidence missing %q:\n%s", want, last)
					}
				}
				payload, _ := json.Marshal(Change{Summary: "Retitle", FilesToModify: []string{"index.html"}, Diff: goodDiff})
				writeOutputText(writer, string(payload))
				return
			}
			payload, _ := json.Marshal(Change{Summary: "Retitle", FilesToModify: []string{"index.html"}, Diff: badDiff})
			writeOutputText(writer, string(payload))
		default:
			t.Fatalf("unexpected schema %q", schemaName(body))
		}
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	if _, err := NewRunner(client, repository).Run(context.Background(), "retitle it", &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	if changes != 2 {
		t.Fatalf("change requests = %d, want 2 (the corrected retry should land)", changes)
	}
	content, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil || !strings.Contains(string(content), "TICTACTRIS") {
		t.Fatalf("index.html = %q, %v", content, err)
	}
}

func TestRunnerRewritesTheFileItReadWhenNoneIsNamed(t *testing.T) {
	// Reproduces a real failure: no files named, every diff rejected, and
	// the rewrite had nothing to write. It must fall back to the file the
	// model actually opened.
	root := t.TempDir()
	original := "<html>\n<title>BLOCK &amp; BOARD</title>\n</html>\n"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &repo.Repository{Root: root}
	rewritten := "<html>\n<title>Tec-TAC-Tris</title>\n</html>\n"

	changes, rewrites := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch schemaName(body) {
		case "message_route":
			routeAsCodingTask(writer)
		case "code_change":
			changes++
			if changes == 1 {
				fmt.Fprint(writer, `{"id":"resp_1","output":[{"type":"function_call","name":"read_files","call_id":"c1","arguments":"{\"paths\":[\"index.html\"]}"}]}`)
				return
			}
			// A diff that can never apply, and no files named.
			payload, _ := json.Marshal(Change{Diff: "diff --git a/index.html b/index.html\n--- a/index.html\n+++ b/index.html\n@@ -1,3 +1,3 @@\n <html>\n-<title>SOMETHING ELSE</title>\n+<title>Tec-TAC-Tris</title>\n </html>\n"})
			writeOutputText(writer, string(payload))
		case "file_rewrite":
			rewrites++
			if input, _ := body["input"].(string); !strings.Contains(input, "index.html") {
				t.Fatalf("rewrite request does not name the file:\n%s", input)
			}
			payload, _ := json.Marshal(Rewrite{Summary: "Retitled", Content: rewritten})
			writeOutputText(writer, string(payload))
		default:
			t.Fatalf("unexpected schema %q", schemaName(body))
		}
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	progress := &recordingProgress{}
	reply, err := NewRunner(client, repository).Run(context.Background(), "retitle it", progress)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Retitled" {
		t.Fatalf("reply = %q", reply)
	}
	if rewrites != 1 {
		t.Fatalf("rewrite requests = %d, want 1", rewrites)
	}
	content, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil || string(content) != rewritten {
		t.Fatalf("index.html = %q, %v", content, err)
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "wrote index.html") {
		t.Fatalf("progress log missing the rewrite:\n%s", trail)
	}
}

func TestRunnerRefusesToEmptyAFileOnRewrite(t *testing.T) {
	// An empty rewrite of a file that has content is the model failing,
	// not an instruction to truncate the user's file.
	root := t.TempDir()
	original := "<html>keep me</html>\n"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &repo.Repository{Root: root}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch schemaName(body) {
		case "message_route":
			routeAsCodingTask(writer)
		case "code_change":
			payload, _ := json.Marshal(Change{FilesToModify: []string{"index.html"}, Diff: "diff --git a/index.html b/index.html\n--- a/index.html\n+++ b/index.html\n@@ -1 +1 @@\n-nope\n+new\n"})
			writeOutputText(writer, string(payload))
		case "file_rewrite":
			payload, _ := json.Marshal(Rewrite{Summary: "oops", Content: "   "})
			writeOutputText(writer, string(payload))
		}
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	_, err := NewRunner(client, repository).Run(context.Background(), "retitle it", &recordingProgress{})
	if err == nil || !strings.Contains(err.Error(), "refusing to empty") {
		t.Fatalf("err = %v, want a refusal to empty the file", err)
	}
	content, readErr := os.ReadFile(filepath.Join(root, "index.html"))
	if readErr != nil || string(content) != original {
		t.Fatalf("index.html = %q, %v; the file must be left alone", content, readErr)
	}
}

func TestRunnerRoutesNonCodingMessageWithoutTouchingRepository(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"id":"resp_route","output":[{"type":"message","content":[{"type":"output_text","text":"{\"coding_task\":false,\"reply\":\"Hi there!\"}"}]}]}`)
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	repository := &repo.Repository{Root: filepath.Join(t.TempDir(), "does-not-exist")}
	reply, err := NewRunner(client, repository).Run(context.Background(), "hi", &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Hi there!" {
		t.Fatalf("reply = %q", reply)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1 (repository must not be touched)", requests)
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
