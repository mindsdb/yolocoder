package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/repo"
)

// Start through the public constructors without assigning router/writer fields
// or passing linker flags. This is the same path used by CLI and web tasks.
func TestMindsHubDefaultsRunCompletePackage(t *testing.T) {
	for _, tc := range []struct {
		name, route, selected, wantWriter string
	}{
		{"small edit", "small_ui", "", "muse-spark-1-3"},
		{"normal task", "normal", "", "muse-spark-1-3"},
		{"selected primary", "normal", "chosen-model", "chosen-model"},
		{"small edit with selected primary", "small_ui", "chosen-model", "muse-spark-1-3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := folder(t, map[string]string{
				"page.html":    "<h1>Hello</h1>",
				"package.json": `{"scripts":{"test":"node -e \"const fs=require('fs');if(fs.readFileSync('page.html','utf8')!=='<h1>Atlas</h1>')process.exit(1);fs.writeFileSync('checked','yes')\""}}`,
			})
			client, err := NewClient(config.LLM{Provider: "environment", BaseURL: "https://api.mindshub.ai/v1", APIKey: "same-key", Model: tc.selected})
			if err != nil {
				t.Fatal(err)
			}
			routes, coding := 0, 0
			client.http = &http.Client{Transport: wholeContextTransport(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get("Authorization") != "Bearer same-key" || request.URL.Host != "api.mindshub.ai" {
					t.Fatal("model call changed provider or credentials")
				}
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					return nil, err
				}
				reply := ""
				switch request.URL.Path {
				case "/v1/decisions":
					routes++
					if body["model"] != "jev-1.13.0" || body["questions"].(map[string]any)["route"] == nil {
						t.Fatal("standard Jev router was not used")
					}
					reply = strings.ReplaceAll(routerReply(tc.route, .99, 1), "jev-test", "jev-1.13.0")
				case "/v1/responses":
					coding++
					if body["model"] != tc.wantWriter {
						t.Fatalf("writer = %v, want %s", body["model"], tc.wantWriter)
					}
					input := fmt.Sprint(body["input"])
					if !strings.Contains(input, "<h1>Hello</h1>") || !strings.Contains(input, "function_call_output") {
						t.Fatal("first coding request did not receive prepared context")
					}
					assertCompletionSchema(t, body["tools"], tc.route == "normal")
					if tc.route == "small_ui" {
						if body["instructions"] != smallEditInstructions || !strings.Contains(fmt.Sprint(body["tools"]), "replace_text") {
							t.Fatal("bounded small-edit path was not enabled")
						}
						call := replacementCall([]repo.Replacement{{Path: "page.html", Old: "Hello", New: "Atlas"}})
						reply = calls(call.Name, call.CallID, call.Arguments)
					} else {
						if !strings.Contains(input, "whole_context_1") || body["instructions"] != changeInstructions {
							t.Fatal("normal task lost whole context or completion instructions")
						}
						reply = editsAndFinishes("edit", "@page.html\n-<h1>Hello</h1>\n+<h1>Atlas</h1>\n", "Updated.")
					}
				default:
					return nil, fmt.Errorf("unexpected request: %s", request.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(reply))}, nil
			})}
			runner := NewRunner(client, repository)
			runner.UsePreselect(true) // A saved legacy preference cannot double-route.
			outcome, err := runner.Run(context.Background(), "Change the heading to Atlas", nil, nil, &recordingProgress{})
			if err != nil || !outcome.Applied || routes != 1 || coding != 1 {
				t.Fatalf("outcome=%+v error=%v routes=%d coding=%d", outcome, err, routes, coding)
			}
			if checked, err := os.ReadFile(filepath.Join(repository.Root, "checked")); err != nil || string(checked) != "yes" {
				t.Fatal("project check did not verify the edited file")
			}
		})
	}
}

func TestOtherProviderUsesConfiguredModelWithoutMindsHubRequests(t *testing.T) {
	repository := folder(t, map[string]string{"page.html": "<h1>Hello</h1>"})
	server, seen := scripted(t, reads("read", "page.html"), editsAndFinishes("edit", "@page.html\n-<h1>Hello</h1>\n+<h1>Atlas</h1>\n", "Updated."))
	defer server.Close()
	// A stale provider label must not turn a third-party endpoint into MindsHub.
	client, err := NewClient(config.LLM{Provider: "mindshub", BaseURL: server.URL, APIKey: "other-key", Model: "other-model", API: config.APIResponses})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(client, repository)
	outcome, err := runner.Run(context.Background(), "Change the heading to Atlas", nil, nil, &recordingProgress{})
	if err != nil || !outcome.Applied || len(*seen) != 2 {
		t.Fatalf("outcome=%+v error=%v calls=%d", outcome, err, len(*seen))
	}
	for _, request := range *seen {
		if request["model"] != "other-model" || request["questions"] != nil {
			t.Fatalf("unexpected model or decision request: %v", request["model"])
		}
		assertCompletionSchema(t, request["tools"], true)
	}
}
