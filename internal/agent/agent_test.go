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

func TestDecodeJSONRejectsNonJSON(t *testing.T) {
	var got map[string]any
	if err := decodeJSON("I can't help with that.", &got); err == nil {
		t.Fatal("expected an error for text with no JSON")
	}
}

func TestReadPlanFilesAllowsAnEmptyPlanForNewFiles(t *testing.T) {
	repository := &repo.Repository{Root: t.TempDir()}
	runner := &Runner{repository: repository}
	text, err := runner.readPlanFiles(Plan{})
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Fatal("expected an explanatory message, got an empty string")
	}
}

func TestRunnerToolPlanPatchApply(t *testing.T) {
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
			fmt.Fprint(writer, `{"id":"resp_route","output":[{"type":"message","content":[{"type":"output_text","text":"{\"coding_task\":true,\"reply\":\"\"}"}]}]}`)
		case 2:
			fmt.Fprint(writer, `{"id":"resp_1","output":[{"type":"function_call","name":"read_files","call_id":"call_1","arguments":"{\"paths\":[\"hello.txt\"]}"}]}`)
		case 3:
			// The continuation must resend the full transcript itself
			// rather than lean on previous_response_id: not every
			// OpenAI-compatible provider persists server-side response
			// state, and one that doesn't rejects an orphaned
			// function_call_output.
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
			fmt.Fprint(writer, `{"id":"resp_2","output":[{"type":"message","content":[{"type":"output_text","text":"{\"summary\":\"Update greeting\",\"files_to_modify\":[\"hello.txt\"],\"context_files\":[],\"steps\":[\"Replace greeting\"]}"}]}]}`)
		case 4:
			diff := "diff --git a/hello.txt b/hello.txt\n--- a/hello.txt\n+++ b/hello.txt\n@@ -1 +1 @@\n-old\n+new\n"
			payload, _ := json.Marshal(Patch{Summary: "Update greeting", Diff: diff})
			response := responseEnvelope{ID: "resp_3", Output: []responseItem{{Type: "message", Content: []contentItem{{Type: "output_text", Text: string(payload)}}}}}
			_ = json.NewEncoder(writer).Encode(response)
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	runner := NewRunner(client, repository)
	progress := &recordingProgress{}
	reply, err := runner.Run(context.Background(), "Change old to new", progress)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Update greeting" {
		t.Fatalf("reply = %q", reply)
	}
	// The permanent log is what tells the user what actually happened.
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
	if requests != 4 {
		t.Fatalf("requests = %d, want 4", requests)
	}
}

func TestRunnerRewritesFilesWhenNoDiffApplies(t *testing.T) {
	// Reproduces a real failure: the file contains the HTML entity
	// "&amp;" but the model's diff writes "&" in the line it removes, so
	// the hunk can never anchor and no git apply flag can rescue it.
	// After the diff attempts fail, the whole-file rewrite must land.
	root := t.TempDir()
	original := "<html>\n<title>BLOCK &amp; BOARD</title>\n</html>\n"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &repo.Repository{Root: root}

	badDiff := "diff --git a/index.html b/index.html\n--- a/index.html\n+++ b/index.html\n@@ -1,3 +1,3 @@\n <html>\n-<title>BLOCK & BOARD</title>\n+<title>TICTACTRIS</title>\n </html>\n"
	rewritten := "<html>\n<title>TICTACTRIS</title>\n</html>\n"

	rewrites := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		name := ""
		if text, ok := body["text"].(map[string]any); ok {
			if format, ok := text["format"].(map[string]any); ok {
				name, _ = format["name"].(string)
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		switch name {
		case "message_route":
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"coding_task\":true,\"reply\":\"\"}"}]}]}`)
		case "implementation_plan":
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"summary\":\"Retitle\",\"files_to_modify\":[\"index.html\"],\"context_files\":[],\"steps\":[\"Rename\"]}"}]}]}`)
		case "unified_patch":
			payload, _ := json.Marshal(Patch{Summary: "Retitle", Diff: badDiff})
			writeOutputText(writer, string(payload))
		case "file_rewrite":
			rewrites++
			payload, _ := json.Marshal(Rewrite{Summary: "Retitled to TICTACTRIS", Files: []RewriteFile{{Path: "index.html", Content: rewritten}}})
			writeOutputText(writer, string(payload))
		default:
			t.Fatalf("unexpected schema %q", name)
		}
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "test", model: "test", http: server.Client()}
	progress := &recordingProgress{}
	reply, err := NewRunner(client, repository).Run(context.Background(), "retitle it", progress)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Retitled to TICTACTRIS" {
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

func writeOutputText(writer http.ResponseWriter, text string) {
	response := responseEnvelope{Output: []responseItem{{Type: "message", Content: []contentItem{{Type: "output_text", Text: text}}}}}
	_ = json.NewEncoder(writer).Encode(response)
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
	runner := NewRunner(client, repository)
	reply, err := runner.Run(context.Background(), "hi", &recordingProgress{})
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
