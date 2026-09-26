package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Inspect the offered tool, not just the prompt constant. Its schema and the
// separate small-UI description must stay unchanged.
func assertNeededReadTool(t *testing.T, value any, normal bool) {
	t.Helper()
	data, _ := json.Marshal(value)
	var tools []functionTool
	if err := json.Unmarshal(data, &tools); err != nil {
		t.Fatal(err)
	}
	want := repositoryTools(false)[0]
	for _, tool := range tools {
		if tool.Name != "read_files" {
			continue
		}
		if normal {
			for _, text := range []string{"concrete uncertainty", "Reuse source already supplied", "needed unread or changed files"} {
				if !strings.Contains(tool.Description, text) {
					t.Errorf("normal read description lacks %q: %s", text, tool.Description)
				}
			}
			tool.Description = want.Description
		} else if tool.Description != want.Description {
			t.Error("normal read guidance leaked into small UI")
		}
		gotJSON, _ := json.Marshal(tool)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("read tool schema changed: %s", gotJSON)
		}
		return
	}
	t.Fatal("read_files was not offered")
}

func TestNeededReadsAllowsMissingContextThenChecksCompletedEdit(t *testing.T) {
	repository := folder(t, map[string]string{
		"value.ts":     "export const label = 'old';\n",
		"rules.txt":    "The required label is 'new'.\n",
		"package.json": `{"scripts":{"test":"node -e \"const fs=require('fs');process.exitCode=fs.readFileSync('value.ts','utf8').includes('new')?0:1;fs.writeFileSync('checked','yes')\""}}`,
	})
	server, seen := scripted(t, reads("needed", "rules.txt"),
		editsAndFinishes("edit", "@value.ts\n-export const label = 'old';\n+export const label = 'new';\n", "Updated the label."))
	defer server.Close()
	runner := NewRunner(&Client{baseURL: server.URL, endpoint: server.URL + "/v1/responses", model: "primary", http: server.Client()}, repository)
	mapping, err := repository.Map()
	if err != nil {
		t.Fatal(err)
	}
	session := runner.newChangeSession("Use the label required by the project rules.", mapping, nil, nil)
	if !runner.completePrefetch(session, []string{"value.ts"}, mappedPaths(mapping)) {
		t.Fatal("initial read did not complete")
	}
	outcome, err := session.work(context.Background(), &recordingProgress{})
	content, _ := repository.ReadFile("value.ts")
	checked, _ := os.ReadFile(filepath.Join(repository.Root, "checked"))
	if err != nil || !outcome.Applied || outcome.Attempts != 1 || len(*seen) != 2 ||
		content != "export const label = 'new';\n" || string(checked) != "yes" || session.used["read_files"] != 2 {
		t.Fatalf("outcome=%+v err=%v requests=%d content=%q checked=%q reads=%d", outcome, err, len(*seen), content, checked, session.used["read_files"])
	}
	for _, request := range *seen {
		assertNeededReadTool(t, request["tools"], true)
		instructions, _ := request["instructions"].(string)
		if !strings.Contains(instructions, "appearing in the map or search results alone") ||
			strings.Contains(instructions, "read all four") || strings.Contains(instructions, "every file you want") {
			t.Error("outgoing normal instructions retained broad batching guidance")
		}
	}
	initial, _ := json.Marshal((*seen)[0]["input"])
	second, _ := json.Marshal((*seen)[1]["input"])
	if !strings.Contains(string(initial), "export const label = 'old'") ||
		!strings.Contains(string(initial), "function_call_output") ||
		!strings.Contains(string(second), "The required label is 'new'.") {
		t.Fatal("supplied context or the additional needed read was lost")
	}
}
