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
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

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
			if body["previous_response_id"] != "resp_1" {
				t.Fatalf("previous_response_id = %v", body["previous_response_id"])
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
	reply, err := runner.Run(context.Background(), "Change old to new", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Update greeting" {
		t.Fatalf("reply = %q", reply)
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
	reply, err := runner.Run(context.Background(), "hi", func(string) {})
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
