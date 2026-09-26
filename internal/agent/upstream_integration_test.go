package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A saved /preselect preference must not add a second Jev round trip when
// the review build has already opted into the winning router.
func TestWinnerRouterSupersedesLegacyPreselection(t *testing.T) {
	repository := folder(t, map[string]string{"app.tsx": "export const title = 'Old';\n"})
	routes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/v1/decisions" {
			routes++
			questions := body["questions"].(map[string]any)
			if _, ok := questions["route"]; !ok {
				t.Error("legacy preselection ran alongside the winner router")
			}
			fmt.Fprint(w, routerReply("normal", .99, 1))
			return
		}
		for _, item := range body["input"].([]any) {
			call := item.(map[string]any)
			if call["type"] == "function_call" && call["name"] == "read_files" {
				var args map[string]any
				if err := json.Unmarshal([]byte(call["arguments"].(string)), &args); err != nil || args["matching"] != "" {
					t.Errorf("prefetched call does not match the current read schema: %v", args)
				}
			}
		}
		assertCompletionSchema(t, body["tools"], true)
		fmt.Fprint(w, editsAndFinishes("edit", "@app.tsx\n-export const title = 'Old';\n+export const title = 'New';\n", "Updated."))
	}))
	defer server.Close()
	runner := NewRunner(&Client{baseURL: server.URL, endpoint: server.URL + "/v1/responses", model: "m", http: server.Client()}, repository)
	runner.editRouterModel = "jev-test"
	runner.UsePreselect(true)
	outcome, err := runner.Run(context.Background(), "Update the app", nil, nil, &recordingProgress{})
	if err != nil || !outcome.Applied || routes != 1 {
		t.Fatalf("outcome=%+v error=%v router calls=%d", outcome, err, routes)
	}
}
