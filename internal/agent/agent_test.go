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
	"strconv"
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
			fmt.Fprint(writer, `{"id":"resp_1","output":[{"type":"function_call","name":"read_files","call_id":"call_1","arguments":"{\"paths\":[\"hello.txt\"]}"}]}`)
		case 2:
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
	outcome, err := NewRunner(client, repository).Run(context.Background(), "Change old to new", nil, nil, progress)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Reply != "Update greeting" {
		t.Fatalf("reply = %q", outcome.Reply)
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
	// One tool round, one answer. Planning and patching used to be
	// separate calls that shipped every file twice, and a routing call
	// used to sit ahead of both.
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestRunnerAccumulatesUsageAcrossCalls(t *testing.T) {
	// A turn can cost several calls (a tool round, then the change, then
	// possibly a rewrite); what's worth reporting is all of them summed,
	// not any one call's own count.
	repository := integrationRepository(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writer.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"function_call","name":"read_files","call_id":"call_1","arguments":"{\"paths\":[\"hello.txt\"]}"}],`+
				`"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110,"input_tokens_details":{"cached_tokens":20}}}`)
		case 2:
			diff := "diff --git a/hello.txt b/hello.txt\n--- a/hello.txt\n+++ b/hello.txt\n@@ -1 +1 @@\n-old\n+new\n"
			change, _ := json.Marshal(Change{Summary: "Update greeting", FilesToModify: []string{"hello.txt"}, Diff: diff})
			envelope := map[string]any{
				"id":     "c",
				"output": []map[string]any{{"type": "message", "content": []map[string]any{{"type": "output_text", "text": string(change)}}}},
				"usage": map[string]any{
					"input_tokens": 200, "output_tokens": 50, "total_tokens": 250,
					"input_tokens_details": map[string]any{"cached_tokens": 30},
				},
			}
			_ = json.NewEncoder(writer).Encode(envelope)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	outcome, err := NewRunner(client, repository).Run(context.Background(), "Change old to new", nil, nil, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{InputTokens: 300, CachedTokens: 50, OutputTokens: 60, TotalTokens: 360}
	if outcome.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", outcome.Usage, want)
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
	outcome, err := NewRunner(client, repository).Run(context.Background(), "retitle it", nil, nil, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if changes != 2 {
		t.Fatalf("change requests = %d, want 2 (the corrected retry should land)", changes)
	}
	if outcome.Attempts != 2 {
		t.Fatalf("Attempts = %d, want 2 (recorded so a session log can tell a repair was needed)", outcome.Attempts)
	}
	if outcome.Rewrote {
		t.Fatal("Rewrote should be false: this succeeded as a diff, not a whole-file fallback")
	}
	content, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil || !strings.Contains(string(content), "TICTACTRIS") {
		t.Fatalf("index.html = %q, %v", content, err)
	}
}

func TestRunnerAnswersAQuestionThatNeededFilesInsteadOfWritingADiff(t *testing.T) {
	// Traced from a real session: routing correctly sends a question like
	// "what's the theme's color scheme" into the coding path, since
	// routing has no tools and cannot read the CSS itself. Once this
	// session has read it, there's nothing to change — only an answer to
	// give back, which must not be treated as a failed change.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.css"), []byte("body { background: #6b8f47; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &repo.Repository{Root: root}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requests++
		writer.Header().Set("Content-Type", "application/json")
		switch schemaName(body) {
		case "code_change":
			if requests == 2 {
				// Reads the file before answering, the same as it would
				// before writing a diff.
				response := responseEnvelope{Output: []responseItem{{
					Type: "function_call", Name: "read_files", CallID: "call_1", Arguments: `{"paths":["index.css"]}`,
				}}}
				_ = json.NewEncoder(writer).Encode(response)
				return
			}
			payload, _ := json.Marshal(Change{Answer: "The background is olive green (#6b8f47)."})
			writeOutputText(writer, string(payload))
		default:
			t.Fatalf("unexpected schema %q", schemaName(body))
		}
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	outcome, err := NewRunner(client, repository).Run(context.Background(), "what color is the background", nil, nil, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Reply != "The background is olive green (#6b8f47)." {
		t.Fatalf("Reply = %q", outcome.Reply)
	}
	if outcome.Applied {
		t.Fatal("Applied should be false: nothing was changed")
	}
	if outcome.Coding {
		t.Fatal("Coding should be false: this ended as an answer, not a change")
	}
}

func TestRunnerStillErrorsWhenNeitherDiffNorAnswerIsGiven(t *testing.T) {
	// Regression guard: a genuinely empty reply (neither a change nor an
	// answer) must still fail loudly rather than silently succeeding with
	// nothing to show for it.
	root := t.TempDir()
	repository := &repo.Repository{Root: root}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch schemaName(body) {
		case "code_change":
			payload, _ := json.Marshal(Change{})
			writeOutputText(writer, string(payload))
		default:
			t.Fatalf("unexpected schema %q", schemaName(body))
		}
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	if _, err := NewRunner(client, repository).Run(context.Background(), "do something", nil, nil, &recordingProgress{}); err == nil {
		t.Fatal("expected an error when the model returns neither a diff nor an answer")
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
	outcome, err := NewRunner(client, repository).Run(context.Background(), "retitle it", nil, nil, progress)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Reply != "Retitled" {
		t.Fatalf("reply = %q", outcome.Reply)
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
	_, err := NewRunner(client, repository).Run(context.Background(), "retitle it", nil, nil, &recordingProgress{})
	if err == nil || !strings.Contains(err.Error(), "refusing to empty") {
		t.Fatalf("err = %v, want a refusal to empty the file", err)
	}
	content, readErr := os.ReadFile(filepath.Join(root, "index.html"))
	if readErr != nil || string(content) != original {
		t.Fatalf("index.html = %q, %v; the file must be left alone", content, readErr)
	}
}

func TestRunnerAnswersAConversationalMessageInOneCall(t *testing.T) {
	// There is no routing call ahead of the work any more, so "hi" costs
	// exactly one round trip: the same call everything else goes through,
	// ending in answer without reading a file or writing a diff.
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writer.Header().Set("Content-Type", "application/json")
		payload, _ := json.Marshal(Change{Answer: "Hi there!"})
		fmt.Fprintf(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":%s}]}]}`, strconv.Quote(string(payload)))
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	outcome, err := NewRunner(client, &repo.Repository{Root: t.TempDir()}).
		Run(context.Background(), "hi", nil, nil, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Reply != "Hi there!" {
		t.Fatalf("reply = %q", outcome.Reply)
	}
	if outcome.Coding || outcome.Applied {
		t.Fatalf("a conversational message changed nothing, so it is not a coding turn: %+v", outcome)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
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

func TestSalvageChangeAcceptsOtherSpellingsOfTheFileList(t *testing.T) {
	// Verbatim from a real reply: the model wrote "files_modified" where
	// the schema said "files_to_modify", so the list was dropped and the
	// trail lost its "will edit" line.
	reply := `{"summary":"Compact the cards","files_modified":["base_index.html"],"diff":"*** Begin Patch"}`
	var change Change
	if err := decodeJSON(reply, &change); err != nil {
		t.Fatal(err)
	}
	if len(change.FilesToModify) != 0 {
		t.Fatalf("precondition: the schema decode should come up empty, got %v", change.FilesToModify)
	}
	salvageChange(&change, reply)
	if len(change.FilesToModify) != 1 || change.FilesToModify[0] != "base_index.html" {
		t.Fatalf("FilesToModify = %v, want base_index.html", change.FilesToModify)
	}

	for _, key := range []string{"files_changed", "modified_files", "files"} {
		var other Change
		payload := `{"summary":"s","` + key + `":["a.go"],"diff":"d"}`
		if err := decodeJSON(payload, &other); err != nil {
			t.Fatal(err)
		}
		salvageChange(&other, payload)
		if len(other.FilesToModify) != 1 || other.FilesToModify[0] != "a.go" {
			t.Fatalf("%s: FilesToModify = %v", key, other.FilesToModify)
		}
	}
}

func TestRunnerRetriesWhenPromisedFilesAreNotCreated(t *testing.T) {
	// Reproduces a real failure: asked for a server, the model listed
	// server.js, package.json, install.sh and launch.sh as modified but
	// put only the HTML change in the diff. That patch applies cleanly,
	// so the run reported success while four promised files did not
	// exist.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>\n<body>old</body>\n</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &repo.Repository{Root: root}

	incomplete, _ := json.Marshal(Change{
		Summary:       "Add a server",
		FilesToModify: []string{"index.html", "server.js"},
		Diff:          "*** Begin Patch\n*** Update File: index.html\n@@\n-<body>old</body>\n+<body>new</body>\n</html>\n*** End Patch",
	})
	complete, _ := json.Marshal(Change{
		Summary:       "Add a server",
		FilesToModify: []string{"server.js"},
		Diff:          "*** Begin Patch\n*** Add File: server.js\n@@\n+const http = require('http');\n*** End Patch",
	})

	changes := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch schemaName(body) {
		case "code_change":
			changes++
			if changes == 1 {
				writeOutputText(writer, string(incomplete))
				return
			}
			// The retry must say which file was missing and how to make it.
			input, _ := json.Marshal(body["input"])
			for _, want := range []string{"server.js", "Add File"} {
				if !strings.Contains(string(input), want) {
					t.Fatalf("evidence missing %q:\n%s", want, input)
				}
			}
			writeOutputText(writer, string(complete))
		default:
			t.Fatalf("unexpected schema %q", schemaName(body))
		}
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	progress := &recordingProgress{}
	if _, err := NewRunner(client, repository).Run(context.Background(), "add a server", nil, nil, progress); err != nil {
		t.Fatal(err)
	}
	if changes != 2 {
		t.Fatalf("change requests = %d, want 2 (the omission should be caught)", changes)
	}
	if _, err := os.Stat(filepath.Join(root, "server.js")); err != nil {
		t.Fatalf("server.js was never created: %v", err)
	}
	trail := strings.Join(progress.logs, "\n")
	if !strings.Contains(trail, "was not created") {
		t.Fatalf("the trail should say a promised file was missing:\n%s", trail)
	}
}

func TestASummaryWithNoDiffIsRepairedRatherThanFatal(t *testing.T) {
	// Seen in practice: a long summary describing the change in prose,
	// with the diff field left empty. That used to end the turn and throw
	// away everything the conversation had already read.
	repository := integrationRepository(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writer.Header().Set("Content-Type", "application/json")
		var payload []byte
		if requests == 1 {
			payload, _ = json.Marshal(Change{Summary: "Restored the green theme across the CSS and the inline colors", FilesToModify: []string{"hello.txt"}})
		} else {
			diff := "diff --git a/hello.txt b/hello.txt\n--- a/hello.txt\n+++ b/hello.txt\n@@ -1 +1 @@\n-old\n+new\n"
			payload, _ = json.Marshal(Change{Summary: "Update greeting", FilesToModify: []string{"hello.txt"}, Diff: diff})
		}
		fmt.Fprintf(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":%s}]}]}`, strconv.Quote(string(payload)))
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	progress := &recordingProgress{}
	outcome, err := NewRunner(client, repository).Run(context.Background(), "restore the theme", nil, nil, progress)
	if err != nil {
		t.Fatalf("a missing diff should be repaired, not fatal: %v", err)
	}
	if !outcome.Applied || outcome.Attempts != 2 {
		t.Fatalf("outcome = %+v, want the second attempt to have landed", outcome)
	}
	content, readErr := os.ReadFile(filepath.Join(repository.Root, "hello.txt"))
	if readErr != nil || string(content) != "new\n" {
		t.Fatalf("hello.txt = %q, %v", content, readErr)
	}
	trail := strings.Join(progress.logs, "\n")
	if !strings.Contains(trail, "no diff came back, retrying") {
		t.Fatalf("the trail should say a diff was missing:\n%s", trail)
	}
	if !strings.Contains(trail, "Restored the green theme") {
		t.Fatalf("the trail should show what it claimed to have done:\n%s", trail)
	}
}

func TestAPromisedDiffThatNeverArrivesFallsBackToWholeFiles(t *testing.T) {
	// Three summaries and no diff: the intent is known and the file is
	// untouched, which is exactly what the whole-file fallback is for.
	repository := integrationRepository(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.Header().Set("Content-Type", "application/json")
		if schemaName(body) == "file_rewrite" {
			payload, _ := json.Marshal(Rewrite{Summary: "Rewrote it", Content: "new\n"})
			fmt.Fprintf(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":%s}]}]}`, strconv.Quote(string(payload)))
			return
		}
		payload, _ := json.Marshal(Change{Summary: "I changed it, honest", FilesToModify: []string{"hello.txt"}})
		fmt.Fprintf(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":%s}]}]}`, strconv.Quote(string(payload)))
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	outcome, err := NewRunner(client, repository).Run(context.Background(), "change it", nil, nil, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Rewrote || !outcome.Applied {
		t.Fatalf("outcome = %+v, want the whole-file fallback to have run", outcome)
	}
	content, _ := os.ReadFile(filepath.Join(repository.Root, "hello.txt"))
	if string(content) != "new\n" {
		t.Fatalf("hello.txt = %q", content)
	}
}
