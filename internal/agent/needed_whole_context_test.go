package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercise the composed route through real outgoing requests and a real
// detected check. The oversized case must retain ranked context and permit
// a needed read; neither context strategy may accept a failed final edit.
func TestNeededWholeContextCompletionRequiresSuccessfulRepair(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("ranked_fallback_%t", fallback), func(t *testing.T) {
			files := map[string]string{
				"value.ts":          "export const value = 'old';\n",
				"style.css":         ".value { color: navy; }\n",
				"project-rules.txt": "The value must be 'new'.\n",
				"package.json":      `{"scripts":{"test":"node -e \"const fs=require('fs');const value=fs.readFileSync('value.ts','utf8');fs.appendFileSync('checked',value);if(!value.includes('new')){console.error('needs required value');process.exitCode=1}\""}}`,
			}
			if fallback {
				files["oversized.md"] = strings.Repeat("x", prefetchMaxFileBytes+1)
			}
			repository := folder(t, files)
			responses := []string{
				editsAndFinishes("first", "@value.ts\n-export const value = 'old';\n+export const value = 'wrong';\n", "Premature completion."),
				editsAndFinishes("repair", "@value.ts\n-export const value = 'wrong';\n+export const value = 'new';\n", "Updated the value."),
			}
			if fallback {
				responses = append([]string{reads("needed", "project-rules.txt")}, responses...)
			}
			var routes, coding int
			var seen []responseRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/decisions" {
					routes++
					fmt.Fprint(w, routerReply("normal", .99, 2))
					return
				}
				var body responseRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				seen = append(seen, body)
				coding++
				assertCompletionSchema(t, body.Tools, true)
				assertNeededReadTool(t, body.Tools, true)
				if body.Model != "primary" || body.Instructions != changeInstructions ||
					!strings.Contains(body.Instructions, "Reuse source already supplied by read_files") ||
					strings.Contains(body.Instructions, "read all four") {
					t.Error("composed normal request lost writer or needed-read guidance")
				}
				for _, tool := range body.Tools {
					if tool.Name == "replace_text" {
						t.Error("normal whole context enabled small-UI tool")
					}
				}
				if coding > len(responses) {
					t.Error("unexpected extra coding call")
					fmt.Fprint(w, finishes("Unexpected request."))
					return
				}
				fmt.Fprint(w, responses[coding-1])
			}))
			defer server.Close()
			runner := NewRunner(&Client{baseURL: server.URL, endpoint: server.URL + "/v1/responses", model: "primary", http: server.Client()}, repository)
			runner.editRouterModel, runner.smallEditModel = "jev-test", "small"
			mapping, err := repository.Map()
			if err != nil {
				t.Fatal(err)
			}
			session := runner.newChangeSession("Use the value required by the project rules and preserve its styling.", mapping, nil, nil)
			progress := &recordingProgress{}
			runner.prefetchEdit(context.Background(), session, mappedPaths(mapping), progress)
			if session.prefetched || session.used["read_files"] != 1 || session.used["apply_diff"] != 0 {
				t.Fatalf("initial context changed route/quota: small=%v used=%v", session.prefetched, session.used)
			}
			_, rulesShown := runner.served["project-rules.txt"]
			if rulesShown == fallback {
				t.Fatalf("wrong context strategy: fallback=%v served=%v", fallback, runner.served)
			}
			outcome, err := session.work(context.Background(), progress)
			if err != nil || !outcome.Applied || outcome.Reply != "Updated the value." || outcome.Attempts != 2 || routes != 1 || coding != len(responses) {
				t.Fatalf("outcome=%+v err=%v routes=%d coding=%d", outcome, err, routes, coding)
			}
			wantReads := 1
			if fallback {
				wantReads++
			}
			if session.used["read_files"] != wantReads || session.used["apply_diff"] != 2 {
				t.Fatalf("wrong original quota accounting: %v", session.used)
			}
			checked, _ := repository.ReadFile("checked")
			if checked != "export const value = 'wrong';\nexport const value = 'new';\n" {
				t.Fatalf("both actual check executions were not reached: %q", checked)
			}
			initial, _ := json.Marshal(seen[0].Input)
			for _, path := range []string{"value.ts", "style.css"} {
				quoted, _ := json.Marshal("--- " + path + " ---\n" + files[path] + "\n")
				// A read result can contain several framed files; compare their
				// escaped bytes without requiring it to be a standalone item.
				if !strings.Contains(string(initial), string(quoted[1:len(quoted)-1])) {
					t.Errorf("initial request lost exact %s source", path)
				}
			}
			repair, _ := json.Marshal(seen[len(seen)-1].Input)
			if !strings.Contains(string(repair), "project's check failed") || !strings.Contains(string(repair), "needs required value") ||
				!strings.Contains(string(repair), "The value must be 'new'.") {
				t.Fatal("repair request lost actual failure or needed project rules")
			}
			logs := strings.Join(progress.logs, "\n")
			if strings.Count(logs, "  check failed, back to it") != 1 || strings.Count(logs, "  check passed") != 1 ||
				strings.Contains(logs, "reason=whole_context") == fallback {
				t.Fatalf("unexpected mechanism/check log: %s", logs)
			}
		})
	}
}
