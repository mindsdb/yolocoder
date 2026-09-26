package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func routerReply(route string, probability float64, count int) string {
	answers := map[string]editAnswer{"route": {Type: "choice", Choice: route, Probabilities: map[string]float64{route: probability}}}
	for i := 0; i < count; i++ {
		answers[fmt.Sprintf("file_%d", i)] = editAnswer{Type: "choice", Choice: "read", Probabilities: map[string]float64{"read": .99}}
	}
	data, _ := json.Marshal(map[string]any{"model": "jev-test", "answers": answers, "usage": map[string]int{"input_tokens": 20, "output_tokens": 4, "total_tokens": 24}})
	return string(data)
}

func TestEditPrefetchKeepsPatchAndChecks(t *testing.T) {
	repository := folder(t, map[string]string{
		"app.tsx":      "export const title = 'Hello';\n",
		"package.json": `{"scripts":{"test":"node -e \"require('fs').writeFileSync('checked','yes')\""}}`,
	})
	var routes, coding int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Error("missing authentication")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/v1/decisions" {
			routes++
			if body["model"] != "jev-test" {
				t.Error("wrong decision model")
			}
			if !strings.Contains(body["state"].(string), "Change the heading") {
				t.Error("missing task")
			}
			fmt.Fprint(w, routerReply("small_ui", .99, 1))
			return
		}
		coding++
		input, _ := json.Marshal(body["input"])
		if !strings.Contains(string(input), "export const title") {
			t.Error("first coding call did not receive file")
		}
		if !strings.Contains(string(input), "function_call_output") || !strings.Contains(string(input), "package.json") {
			t.Error("first coding call lacks completed read or project context")
		}
		if body["instructions"] != smallEditInstructions {
			t.Error("small-UI instructions changed")
		}
		assertCompletionSchema(t, body["tools"], false)
		assertNeededReadTool(t, body["tools"], false)
		arguments, _ := json.Marshal(map[string]string{
			"task_complete":             "unknown fields stay ignored on small UI",
			"patch":                     "@app.tsx\n-export const title = 'Hello';\n+export const title = 'Welcome';\n",
			"response_comment_for_user": "Updated the heading.",
		})
		fmt.Fprint(w, calls("apply_diff", "edit", string(arguments)))
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, endpoint: server.URL + "/v1/responses", apiKey: "k", model: "m", http: server.Client()}
	runner := NewRunner(client, repository)
	runner.editRouterModel = "jev-test"
	outcome, err := runner.Run(context.Background(), "Change the heading to Welcome", nil, nil, &recordingProgress{})
	if err != nil || !outcome.Applied || routes != 1 || coding != 1 {
		t.Fatalf("outcome=%+v error=%v routes=%d coding=%d", outcome, err, routes, coding)
	}
	if outcome.Usage.TotalTokens != 24 {
		t.Error("router usage lost")
	}
	content, _ := repository.ReadFile("app.tsx")
	if content != "export const title = 'Welcome';\n" {
		t.Errorf("wrong edit: %s", content)
	}
	var tested bool
	for _, span := range outcome.Profile.Spans() {
		if span.Step == StepTest {
			tested = true
		}
	}
	if !tested {
		t.Error("normal project check was bypassed")
	}
	if data, err := os.ReadFile(filepath.Join(repository.Root, "checked")); err != nil || string(data) != "yes" {
		t.Error("project check did not execute")
	}
}

func TestCompletedPrefetchTracksExactReads(t *testing.T) {
	repository := folder(t, map[string]string{
		"ui/View.tsx":            "const heading = 'Hello';\n",
		"ui/theme.css":           "button { color: blue }\n",
		"ui/package.json":        `{"name":"ui"}`,
		"package.json":           `{"scripts":{"test":"tsc --noEmit"}}`,
		"tsconfig.json":          `{"compilerOptions":{"strict":true}}`,
		"unrelated/package.json": `{"name":"unrelated"}`,
	})
	runner := NewRunner(&Client{}, repository)
	mapping, _ := repository.Map()
	session := runner.newChangeSession("Rename the heading", mapping, nil, nil)
	if !runner.completePrefetch(session, []string{"ui/View.tsx", "ui/theme.css"}, mappedPaths(mapping)) {
		t.Fatal("prefetch rejected")
	}
	if len(session.transcript) != 3 || session.used["read_files"] != 1 || len(runner.served) != 5 {
		t.Fatal("incorrect read state")
	}
	if _, exists := runner.served["unrelated/package.json"]; exists {
		t.Fatal("read unrelated package")
	}
	call := session.transcript[1].(responseItem)
	output := session.transcript[2].(toolOutput)
	if call.Name != "read_files" || call.CallID != output.CallID {
		t.Fatal("unpaired tool transcript")
	}
	chat, err := chatMessages(session.transcript)
	if err != nil || len(chat) != 3 || chat[1].Role != "assistant" || chat[2].Role != "tool" {
		t.Fatalf("invalid chat dialect transcript: %v", err)
	}
	text, err := runner.readFiles([]string{"ui/View.tsx"})
	if err != nil || !strings.Contains(text, "unchanged since it was shown") {
		t.Fatal("prefetched file was not deduplicated")
	}
	if err := repository.Write("ui/View.tsx", "const heading = 'Updated externally';\n"); err != nil {
		t.Fatal(err)
	}
	text, err = runner.readFiles([]string{"ui/View.tsx"})
	if err != nil || !strings.Contains(text, "Updated externally") {
		t.Fatal("changed content was hidden by stale read state")
	}
}

func TestNormalPrefetchKeepsNormalWriterToolsAndChecks(t *testing.T) {
	repository := folder(t, map[string]string{
		"server.ts":    "export const enabled = false;\n",
		"package.json": `{"scripts":{"test":"node -e \"require('fs').writeFileSync('checked','yes')\""}}`,
	})
	var routes, coding int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/decisions" {
			routes++
			fmt.Fprint(w, routerReply("normal", .99, 1))
			return
		}
		coding++
		var body responseRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "primary" || body.Instructions != changeInstructions {
			t.Error("normal context selected small-edit writer or instructions")
		}
		for _, tool := range body.Tools {
			if tool.Name == "replace_text" {
				t.Error("normal context enabled small-edit tools")
			}
		}
		input, _ := json.Marshal(body.Input)
		if !strings.Contains(string(input), "export const enabled = false") || !strings.Contains(string(input), "package.json") {
			t.Error("first coding request lacks initial source and project context")
		}
		fmt.Fprint(w, editsAndFinishes("edit", "@server.ts\n-export const enabled = false;\n+export const enabled = true;\n", "Enabled the service."))
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, endpoint: server.URL + "/v1/responses", model: "primary", http: server.Client()}
	runner := NewRunner(client, repository)
	runner.editRouterModel = "jev-test"
	runner.smallEditModel = "small"
	outcome, err := runner.Run(context.Background(), "Enable the service", nil, nil, &recordingProgress{})
	if err != nil || !outcome.Applied || routes != 1 || coding != 1 || outcome.Attempts != 1 {
		t.Fatalf("outcome=%+v err=%v routes=%d coding=%d", outcome, err, routes, coding)
	}
	if data, err := os.ReadFile(filepath.Join(repository.Root, "checked")); err != nil || string(data) != "yes" {
		t.Error("normal prefetch bypassed the project check")
	}
}

func TestCompletedPrefetchBoundsAreAtomic(t *testing.T) {
	repository := folder(t, map[string]string{
		"one.tsx":      strings.Repeat("a", 13000),
		"two.css":      strings.Repeat("b", 13000),
		"package.json": strings.Repeat("c", 12000),
	})
	runner := NewRunner(&Client{}, repository)
	mapping, _ := repository.Map()
	session := runner.newChangeSession("Recolour", mapping, nil, nil)
	if runner.completePrefetch(session, []string{"one.tsx", "two.css"}, mappedPaths(mapping)) {
		t.Fatal("oversized required context accepted")
	}
	if len(runner.served) != 0 || len(session.transcript) != 1 || session.used["read_files"] != 0 {
		t.Fatal("failed read changed state")
	}
	if !runner.completePrefetch(session, []string{"one.tsx"}, mappedPaths(mapping)) {
		t.Fatal("optional oversized manifest blocked source")
	}
	if len(runner.served) != 1 {
		t.Fatal("manifest exceeded context budget")
	}
	for _, invalid := range []string{"../outside.tsx", "/outside.tsx"} {
		if runner.completePrefetch(session, []string{invalid}, []string{invalid}) {
			t.Fatal("outside path accepted")
		}
	}
}

func TestEditPrefetchFallbackDoesNotChangeConversation(t *testing.T) {
	for name, reply := range map[string]string{
		"uncertain_normal":    routerReply("normal", .79, 1),
		"unknown_route":       routerReply("other", .99, 1),
		"uncertain":           routerReply("small_ui", .79, 1),
		"invalid_probability": routerReply("small_ui", 2, 1),
		"bad_json":            "not JSON",
		"missing":             `{"model":"jev-test","answers":{}}`,
		"wrong_model":         strings.ReplaceAll(routerReply("small_ui", .99, 1), "jev-test", "wrong"),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, reply) }))
			defer server.Close()
			runner := NewRunner(&Client{baseURL: server.URL, http: server.Client()}, folder(t, map[string]string{"page.html": "<h1>Hello</h1>"}))
			runner.editRouterModel = "jev-test"
			session := runner.newChangeSession("Make it better", "page.html 14 B", nil, nil)
			runner.prefetchEdit(context.Background(), session, []string{"page.html"}, &recordingProgress{})
			if len(session.transcript) != 1 || len(session.readPaths) != 0 {
				t.Fatal("fallback changed conversation")
			}
		})
	}
}

func TestEditPrefetchBoundsAndSymlinks(t *testing.T) {
	repository := folder(t, map[string]string{"page.html": "<h1>Hello</h1>", "large.css": strings.Repeat("x", 16001)})
	outside := filepath.Join(t.TempDir(), "outside.css")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repository.Root, "linked.css")); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(&Client{}, repository)
	runner.editRouterModel = "jev-test"
	for _, paths := range [][]string{{"large.css"}, {"linked.css"}, nil} {
		session := runner.newChangeSession("Change colour", "", nil, nil)
		runner.prefetchEdit(context.Background(), session, paths, &recordingProgress{})
		if len(session.transcript) != 1 {
			t.Fatal("unbounded context added")
		}
	}
	// A selected set larger than four files must also fall back.
	paths := []string{"a.ts", "b.ts", "c.ts", "d.ts", "e.ts"}
	for _, p := range paths {
		if err := os.WriteFile(filepath.Join(repository.Root, p), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, routerReply("small_ui", .99, 5)) }))
	defer server.Close()
	runner.client = &Client{baseURL: server.URL, http: server.Client()}
	session := runner.newChangeSession("Change colour", "", nil, nil)
	runner.prefetchEdit(context.Background(), session, paths, &recordingProgress{})
	if len(session.transcript) != 1 {
		t.Fatal("too many files added")
	}
}

func TestEditRouterHonoursCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, http: server.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := client.selectEditFiles(ctx, "jev-test", "task", []string{"page.html"})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancellation not honoured: %v", err)
	}
}
