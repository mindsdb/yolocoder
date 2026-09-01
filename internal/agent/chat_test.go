package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/repo"
)

func TestChatEndpoint(t *testing.T) {
	tests := map[string]string{
		"https://api.cerebras.ai/v1": "https://api.cerebras.ai/v1/chat/completions",
		"https://api.example.com":    "https://api.example.com/v1/chat/completions",
		"https://api.example.com/":   "https://api.example.com/v1/chat/completions",
	}
	for input, expected := range tests {
		if actual := chatEndpoint(input); actual != expected {
			t.Fatalf("chatEndpoint(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestToChatConvertsATranscript(t *testing.T) {
	// The transcript is what a repair looks like: the opening message, the
	// tool call the model made, the output it got back, and the evidence.
	request := responseRequest{
		Model:        "m",
		Instructions: "be brief",
		Input: []any{
			inputMessage{Role: "user", Content: "TASK: retitle"},
			responseItem{Type: "function_call", Name: "read_files", CallID: "call_1", Arguments: `{"paths":["index.html"]}`},
			toolOutput{Type: "function_call_output", CallID: "call_1", Output: "<html>"},
			inputMessage{Role: "user", Content: "that diff did not apply"},
		},
		Tools:      repositoryTools(),
		ToolChoice: "auto",
		Text:       strictSchema("code_change", changeSchema()),
	}
	converted, err := toChat(request)
	if err != nil {
		t.Fatal(err)
	}
	if converted.Model != "m" {
		t.Fatalf("model = %q", converted.Model)
	}
	// Instructions become the system message chat completions expects.
	if len(converted.Messages) != 5 || converted.Messages[0].Role != "system" || converted.Messages[0].Content != "be brief" {
		t.Fatalf("messages = %+v", converted.Messages)
	}
	if converted.Messages[1].Role != "user" || converted.Messages[1].Content != "TASK: retitle" {
		t.Fatalf("messages[1] = %+v", converted.Messages[1])
	}
	// The tool call becomes an assistant turn carrying tool_calls...
	assistant := converted.Messages[2]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("messages[2] = %+v", assistant)
	}
	if assistant.ToolCalls[0].ID != "call_1" || assistant.ToolCalls[0].Function.Name != "read_files" {
		t.Fatalf("tool call = %+v", assistant.ToolCalls[0])
	}
	// ...and the output becomes a tool message whose id matches it, which
	// providers reject if it doesn't.
	answer := converted.Messages[3]
	if answer.Role != "tool" || answer.ToolCallID != "call_1" || answer.Content != "<html>" {
		t.Fatalf("messages[3] = %+v", answer)
	}
	if converted.Messages[4].Role != "user" {
		t.Fatalf("messages[4] = %+v", converted.Messages[4])
	}
	// Tools nest under "function" in this dialect.
	if len(converted.Tools) != 2 || converted.Tools[0].Type != "function" || converted.Tools[0].Function.Name != "read_files" {
		t.Fatalf("tools = %+v", converted.Tools)
	}
	// And the JSON schema moves from text.format to response_format.
	if converted.ResponseFormat == nil || converted.ResponseFormat.Type != "json_schema" {
		t.Fatalf("response format = %+v", converted.ResponseFormat)
	}
	if converted.ResponseFormat.JSONSchema.Name != "code_change" || !converted.ResponseFormat.JSONSchema.Strict {
		t.Fatalf("json schema = %+v", converted.ResponseFormat.JSONSchema)
	}
}

func TestToChatConvertsABareString(t *testing.T) {
	converted, err := toChat(responseRequest{Model: "m", Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(converted.Messages) != 1 || converted.Messages[0].Role != "user" || converted.Messages[0].Content != "hi" {
		t.Fatalf("messages = %+v", converted.Messages)
	}
}

func TestFromChatReadsTextAndToolCalls(t *testing.T) {
	// Text replies have to arrive where response.text() looks for them.
	text := fromChat(chatEnvelope{ID: "x", Choices: []chatChoice{{Message: chatMessage{Content: `{"ok":true}`}}}})
	got, err := text.text()
	if err != nil || got != `{"ok":true}` {
		t.Fatalf("text() = %q, %v", got, err)
	}
	if len(text.calls()) != 0 {
		t.Fatalf("calls = %+v", text.calls())
	}

	// Tool calls have to arrive where response.calls() looks for them.
	tools := fromChat(chatEnvelope{Choices: []chatChoice{{Message: chatMessage{ToolCalls: []chatToolCall{{
		ID: "call_9", Type: "function", Function: chatCallFunction{Name: "read_files", Arguments: `{"paths":["a"]}`},
	}}}}}})
	calls := tools.calls()
	if len(calls) != 1 || calls[0].CallID != "call_9" || calls[0].Name != "read_files" {
		t.Fatalf("calls = %+v", calls)
	}
	if paths := readFilePaths(calls[0]); len(paths) != 1 || paths[0] != "a" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestDetectAPI(t *testing.T) {
	// A Responses route that exists, even when it rejects the probe.
	responses := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer responses.Close()
	if dialect, err := DetectAPI(context.Background(), responses.URL, "k"); err != nil || dialect != config.APIResponses {
		t.Fatalf("DetectAPI() = %q, %v; want responses", dialect, err)
	}

	// A provider that only offers chat completions, like Cerebras: the
	// Responses route 404s.
	chat := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/responses") {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer chat.Close()
	if dialect, err := DetectAPI(context.Background(), chat.URL, "k"); err != nil || dialect != config.APIChat {
		t.Fatalf("DetectAPI() = %q, %v; want chat", dialect, err)
	}
}

// TestRunnerOverChatCompletions drives a whole task against a server that
// speaks only chat completions, which is what most OpenAI-compatible
// providers offer.
func TestRunnerOverChatCompletions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>\n<title>Old</title>\n</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &repo.Repository{Root: root}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/chat/completions") {
			t.Fatalf("wrong endpoint: %s", request.URL.Path)
		}
		var body chatRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requests++
		writer.Header().Set("Content-Type", "application/json")
		reply := func(message chatMessage) {
			_ = json.NewEncoder(writer).Encode(chatEnvelope{ID: "c", Choices: []chatChoice{{Message: message}}})
		}
		switch requests {
		case 1:
			reply(chatMessage{Role: "assistant", Content: `{"coding_task":true,"reply":""}`})
		case 2:
			// Ask to read the file, in this dialect's shape.
			reply(chatMessage{Role: "assistant", ToolCalls: []chatToolCall{{
				ID: "call_1", Type: "function",
				Function: chatCallFunction{Name: "read_files", Arguments: `{"paths":["index.html"]}`},
			}}})
		case 3:
			// The tool result must have come back as a tool message.
			last := body.Messages[len(body.Messages)-1]
			if last.Role != "tool" || last.ToolCallID != "call_1" {
				t.Fatalf("last message = %+v, want the tool result", last)
			}
			if !strings.Contains(last.Content, "<title>Old</title>") {
				t.Fatalf("tool result did not carry the file: %q", last.Content)
			}
			diff := "diff --git a/index.html b/index.html\n--- a/index.html\n+++ b/index.html\n@@ -1,3 +1,3 @@\n <html>\n-<title>Old</title>\n+<title>New</title>\n </html>\n"
			payload, _ := json.Marshal(Change{Summary: "Retitle", FilesToModify: []string{"index.html"}, Diff: diff})
			reply(chatMessage{Role: "assistant", Content: string(payload)})
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	client, err := NewClient(config.LLM{BaseURL: server.URL, APIKey: "k", Model: "gpt-oss-120b", API: config.APIChat})
	if err != nil {
		t.Fatal(err)
	}
	progress := &recordingProgress{}
	reply, err := NewRunner(client, repository).Run(context.Background(), "retitle it", progress)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Retitle" {
		t.Fatalf("reply = %q", reply)
	}
	content, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil || !strings.Contains(string(content), "<title>New</title>") {
		t.Fatalf("index.html = %q, %v", content, err)
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "read index.html") {
		t.Fatalf("progress log missing the tool call:\n%s", trail)
	}
}

func TestNewClientPicksTheEndpointForTheDialect(t *testing.T) {
	chat, err := NewClient(config.LLM{BaseURL: "https://api.cerebras.ai/v1", Model: "m", API: config.APIChat})
	if err != nil {
		t.Fatal(err)
	}
	if chat.endpoint != "https://api.cerebras.ai/v1/chat/completions" || !chat.chat {
		t.Fatalf("chat client = %+v", chat)
	}
	// An unset dialect stays on the Responses API, as saved configs from
	// before this existed expect.
	responses, err := NewClient(config.LLM{BaseURL: "https://api.openai.com/v1", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if responses.endpoint != "https://api.openai.com/v1/responses" || responses.chat {
		t.Fatalf("responses client = %+v", responses)
	}
}
