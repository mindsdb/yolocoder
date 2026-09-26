package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the composition through real outgoing requests and a detected
// project check, rather than treating prefilled context as small-UI routing.
func TestCompletionWholeContextChecksAndRepairs(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(fmt.Sprintf("repair_%t", repair), func(t *testing.T) {
			check := `node -e "const fs=require('fs');const ok=fs.readFileSync('service.ts','utf8').includes('= true');fs.appendFileSync('checked',String(ok)+'\n');process.exitCode=ok?0:1"`
			pkg, _ := json.Marshal(map[string]any{"scripts": map[string]string{"test": check}})
			files := map[string]string{"service.ts": "export const enabled = false;\n", "package.json": string(pkg)}
			for i := range 12 {
				files[fmt.Sprintf("docs/note%02d.md", i)] = fmt.Sprintf("Project note %02d\n", i)
			}
			repository := folder(t, files)
			firstValue, firstNote := "true", ""
			if repair {
				firstValue, firstNote = "0", "Premature completion."
			}
			replies := []string{editsAndFinishes("first", "@service.ts\n-export const enabled = false;\n+export const enabled = "+firstValue+";\n", firstNote)}
			if repair {
				replies = append(replies, editsAndFinishes("repair", "@service.ts\n-export const enabled = 0;\n+export const enabled = true;\n", ""))
			}
			var requests []map[string]any
			routes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/decisions" {
					routes++
					fmt.Fprint(w, routerReply("normal", .99, 1))
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				requests = append(requests, body)
				if len(requests) > len(replies) {
					t.Error("unexpected coding request after checked completion")
					fmt.Fprint(w, finishes("Unexpected extra response."))
					return
				}
				fmt.Fprint(w, replies[len(requests)-1])
			}))
			defer server.Close()
			runner := NewRunner(&Client{baseURL: server.URL, endpoint: server.URL + "/v1/responses", model: "primary", http: server.Client()}, repository)
			runner.editRouterModel, runner.smallEditModel = "jev-test", "small"
			mapping, err := repository.Map()
			if err != nil {
				t.Fatal(err)
			}
			session := runner.newChangeSession("Enable the service", mapping, nil, nil)
			progress := &recordingProgress{}
			runner.prefetchEdit(context.Background(), session, mappedPaths(mapping), progress)
			if session.prefetched || session.used["read_files"] != 2 || len(session.readPaths) != len(files) {
				t.Fatalf("wrong prefill state: smallUI=%t reads=%d paths=%v", session.prefetched, session.used["read_files"], session.readPaths)
			}
			outcome, err := session.work(context.Background(), progress)
			server.Close() // All handler writes finish before inspecting captures.
			content, _ := repository.ReadFile("service.ts")
			checked, _ := os.ReadFile(filepath.Join(repository.Root, "checked"))
			wantChecks := "true\n"
			if repair {
				wantChecks = "false\ntrue\n"
			}
			if err != nil || !outcome.Applied || outcome.Reply != "Updated service.ts." || outcome.Attempts != len(replies) ||
				len(requests) != len(replies) || routes != 1 || content != "export const enabled = true;\n" || string(checked) != wantChecks || session.used["read_files"] != 2 {
				t.Fatalf("outcome=%+v err=%v requests=%d routes=%d content=%q checks=%q reads=%d", outcome, err, len(requests), routes, content, checked, session.used["read_files"])
			}
			for _, request := range requests {
				assertCompletionSchema(t, request["tools"], true)
				if request["model"] != "primary" || request["instructions"] != changeInstructions {
					t.Error("whole context changed the normal writer or instructions")
				}
			}
			initial, _ := json.Marshal(requests[0]["input"])
			for _, fragment := range []string{"whole_context_1", "whole_context_2", "export const enabled = false;", "Project note 11"} {
				if !strings.Contains(string(initial), fragment) {
					t.Errorf("initial coding request lacks prefilled context %q", fragment)
				}
			}
			logs := strings.Join(progress.logs, "\n")
			if !strings.Contains(logs, "reason=whole_context") || strings.Count(logs, "check passed") != 1 {
				t.Fatalf("missing activation or actual passing check: %s", logs)
			}
			if repair {
				second, _ := json.Marshal(requests[1]["input"])
				if !strings.Contains(string(second), "project's check failed") || !strings.Contains(string(second), "export const enabled = 0;") || strings.Count(logs, "check failed") != 1 {
					t.Fatal("failed check or landed edit was not returned for repair")
				}
			}
		})
	}
}
