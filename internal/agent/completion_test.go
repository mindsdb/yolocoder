package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Check the schema actually sent to the provider, including the old small-UI
// tool. Calling tools() for another route must not mutate an earlier schema.
func assertCompletionSchema(t *testing.T, value any, normal bool) {
	t.Helper()
	data, _ := json.Marshal(value)
	var tools []functionTool
	if err := json.Unmarshal(data, &tools); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name != "apply_diff" {
			continue
		}
		if !normal {
			want, _ := json.Marshal(repositoryTools(false)[2])
			got, _ := json.Marshal(tool)
			if string(got) != string(want) {
				t.Errorf("small-UI apply_diff schema changed: %s", got)
			}
			return
		}
		properties := tool.Parameters["properties"].(map[string]any)
		if !reflect.DeepEqual(properties["task_complete"], map[string]any{"type": "boolean"}) ||
			!reflect.DeepEqual(tool.Parameters["required"], []any{"patch", "response_comment_for_user", "task_complete"}) ||
			tool.Parameters["additionalProperties"] != false || !tool.Strict {
			t.Errorf("invalid normal completion schema: %s", data)
		}
		if !strings.Contains(tool.Description, "entire request") || !strings.Contains(tool.Description, "final tool call") {
			t.Errorf("schema lacks completion bounds: %s", tool.Description)
		}
		return
	}
	t.Fatal("missing apply_diff tool")
}

func TestNormalCompletionAcceptsEmptyNoteAndDisclosesSkippedCheck(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "old\n"})
	server, seen := scripted(t, editsAndFinishes("edit", "@a.ts\n-old\n+new\n", ""))
	defer server.Close()
	outcome, progress, err := run(t, repository, server, "change it")
	if err != nil || !outcome.Applied || outcome.Attempts != 1 || len(*seen) != 1 {
		t.Fatalf("outcome=%+v err=%v requests=%d", outcome, err, len(*seen))
	}
	if outcome.Reply != "Updated a.ts.\n\nNo project check was detected." ||
		strings.Contains(strings.Join(progress.logs, "\n"), "check passed") {
		t.Fatalf("skipped check misreported: %+v %v", outcome, progress.logs)
	}
	assertCompletionSchema(t, (*seen)[0]["tools"], true)
	instructions := (*seen)[0]["instructions"].(string)
	for _, required := range []string{"task_complete=true", "task_complete=false", "REQUEST CONTRACT", "typecheck does not establish", "honest account of any incomplete work"} {
		if !strings.Contains(instructions, required) {
			t.Errorf("outgoing instructions missing %q", required)
		}
	}
	if strings.Contains(instructions, "Finish by replying in plain markdown") {
		t.Error("conflicting unconditional finish rule remains")
	}
	// Fresh schema maps prevent normal-route additions leaking into small UI.
	small := NewRunner(&Client{}, repository).newChangeSession("edit", "", nil, nil)
	small.prefetched = true
	assertCompletionSchema(t, small.tools(), false)
}

func TestNormalIncompleteEditIgnoresClosingNote(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"patch": "@a.ts\n-old\n+new\n", "task_complete": false, "response_comment_for_user": "All done!"})
	server, seen := scripted(t, calls("apply_diff", "edit", string(args)), finishes("Changed one part; the rest remains incomplete."))
	defer server.Close()
	repository := folder(t, map[string]string{"a.ts": "old\n"})
	outcome, _, err := run(t, repository, server, "change two parts")
	if err != nil || len(*seen) != 2 || outcome.Reply != "Changed one part; the rest remains incomplete." || !outcome.Applied {
		t.Fatalf("outcome=%+v err=%v requests=%d", outcome, err, len(*seen))
	}
}

func TestNormalCompletionRejectsInvalidBooleanBeforeWriting(t *testing.T) {
	for name, value := range map[string]string{"missing": "", "null": ",\"task_complete\":null", "string": ",\"task_complete\":\"true\"", "number": ",\"task_complete\":1", "array": ",\"task_complete\":[]"} {
		t.Run(name, func(t *testing.T) {
			args := `{"patch":"@a.ts\n-old\n+new\n","response_comment_for_user":"Done"` + value + `}`
			server, seen := scripted(t, calls("apply_diff", "edit", args), finishes("The edit was rejected; nothing changed."))
			defer server.Close()
			repository := folder(t, map[string]string{"a.ts": "old\n"})
			outcome, _, err := run(t, repository, server, "change it")
			content, _ := repository.ReadFile("a.ts")
			if err != nil || outcome.Applied || outcome.Attempts != 1 || content != "old\n" || len(*seen) != 2 {
				t.Fatalf("outcome=%+v err=%v content=%q requests=%d", outcome, err, content, len(*seen))
			}
			transcript, _ := json.Marshal((*seen)[1]["input"])
			if !strings.Contains(string(transcript), "ERROR:") {
				t.Fatal("rejection was not returned to the model")
			}
		})
	}
}

func batchReplies(t *testing.T, replies ...string) string {
	t.Helper()
	var output []json.RawMessage
	for _, reply := range replies {
		var response struct {
			Output []json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal([]byte(reply), &response); err != nil {
			t.Fatal(err)
		}
		output = append(output, response.Output...)
	}
	data, _ := json.Marshal(map[string]any{"id": "batch", "output": output})
	return string(data)
}

func TestNormalCompletionRequiresFinalAcceptedTool(t *testing.T) {
	for _, last := range []string{"complete", "incomplete", "read", "rejected", "malformed", "unknown", "quota"} {
		t.Run(last, func(t *testing.T) {
			repository := folder(t, map[string]string{"a.ts": "old\n", "b.ts": "old\n"})
			replies := []string{editsAndFinishes("first", "@a.ts\n-old\n+new\n", "First complete.")}
			wantB, attempts := "old\n", 1
			switch last {
			case "complete":
				replies = append(replies, editsAndFinishes("last", "@b.ts\n-old\n+new\n", "Both complete."))
				wantB, attempts = "new\n", 2
			case "incomplete":
				replies = append(replies, edits("last", "@b.ts\n-old\n+new\n"))
				wantB, attempts = "new\n", 2
			case "read":
				replies = append(replies, reads("last", "b.ts"))
			case "rejected":
				replies = append(replies, editsAndFinishes("last", "@b.ts\n-absent\n+new\n", "Wrong."))
				attempts = 2
			case "malformed":
				replies = append(replies, calls("apply_diff", "last", "{"))
				attempts = 2
			case "unknown":
				replies = append(replies, calls("unknown", "last", "{}"))
			case "quota":
				replies = append(replies, edits("second", "@a.ts\n-new\n+newer\n"), edits("third", "@a.ts\n-newer\n+latest\n"), editsAndFinishes("fourth", "@a.ts\n-latest\n+final\n", "Fourth complete."), editsAndFinishes("last", "@b.ts\n-old\n+new\n", "Refused."))
				attempts = 4
			}
			script := []string{batchReplies(t, replies...)}
			if last != "complete" {
				script = append(script, finishes("More work remains."))
			}
			server, seen := scripted(t, script...)
			defer server.Close()
			outcome, _, err := run(t, repository, server, "change both files")
			content, _ := repository.ReadFile("b.ts")
			if err != nil || !outcome.Applied || outcome.Attempts != attempts || content != wantB || len(*seen) != len(script) {
				t.Fatalf("outcome=%+v err=%v b=%q requests=%d", outcome, err, content, len(*seen))
			}
			if last == "complete" {
				if !strings.HasPrefix(outcome.Reply, "Both complete.") || len(outcome.Files) != 2 {
					t.Fatalf("later accepted edit was skipped: %+v", outcome)
				}
			} else {
				if outcome.Reply != "More work remains." {
					t.Fatalf("earlier completion leaked: %+v", outcome)
				}
				transcript, _ := json.Marshal((*seen)[1]["input"])
				if !strings.Contains(string(transcript), `"call_id":"last"`) {
					t.Fatal("last tool was skipped")
				}
			}
		})
	}
}

func TestNormalCompletionReturnsEarlierBatchFailuresForReview(t *testing.T) {
	for name, first := range map[string]string{
		"rejected patch":  editsAndFinishes("first", "@a.ts\n-absent\n+new\n", "Done."),
		"malformed patch": calls("apply_diff", "first", "{"),
		"failed read":     reads("first", "absent.ts"),
		"unknown tool":    calls("unknown", "first", "{}"),
	} {
		t.Run(name, func(t *testing.T) {
			repository := folder(t, map[string]string{"a.ts": "old\n", "b.ts": "old\n"})
			server, seen := scripted(t,
				batchReplies(t, first, editsAndFinishes("last", "@b.ts\n-old\n+new\n", "Premature completion.")),
				finishes("Only one part changed; the first part is still incomplete."))
			defer server.Close()
			outcome, _, err := run(t, repository, server, "change both files")
			a, _ := repository.ReadFile("a.ts")
			b, _ := repository.ReadFile("b.ts")
			if err != nil || len(*seen) != 2 || !outcome.Applied || a != "old\n" || b != "new\n" ||
				outcome.Reply != "Only one part changed; the first part is still incomplete." {
				t.Fatalf("outcome=%+v err=%v requests=%d a=%q b=%q", outcome, err, len(*seen), a, b)
			}
			transcript, _ := json.Marshal((*seen)[1]["input"])
			for _, id := range []string{"first", "last"} {
				if !strings.Contains(string(transcript), `"call_id":"`+id+`"`) {
					t.Errorf("missing batch result for %s", id)
				}
			}
		})
	}
}

func TestCompletionFieldDoesNotChangeOtherToolParsing(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "old\n"})
	runner := NewRunner(&Client{}, repository)
	output, ok := runner.runTool(context.Background(), responseItem{Name: "read_files", Arguments: `{"paths":["a.ts"],"task_complete":"previously ignored"}`})
	if !ok || !strings.Contains(output, "old") {
		t.Fatalf("unknown completion field changed read parsing: %q", output)
	}
}

type cancellingProgress struct {
	recordingProgress
	cancel context.CancelFunc
	when   string
}

func (p *cancellingProgress) Status(message string) {
	p.recordingProgress.Status(message)
	if p.when == "before edit" && message == activityFor("apply_diff") {
		p.cancel()
	}
}

func (p *cancellingProgress) Log(message string) {
	p.recordingProgress.Log(message)
	if p.when == "after edit" && strings.HasPrefix(message, "  edit ") {
		p.cancel()
	}
}

func TestNormalCompletionCancellationCannotReportSuccess(t *testing.T) {
	for _, when := range []string{"before edit", "after edit", "during check"} {
		t.Run(when, func(t *testing.T) {
			repository := folder(t, map[string]string{"a.ts": "old\n"})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			progress := &cancellingProgress{cancel: cancel, when: when}
			if when == "during check" {
				if runtime.GOOS == "windows" {
					t.Skip("this cancellation fixture requires a POSIX executable script")
				}
				// A real detected command marks its start, then waits to be
				// canceled. No provider or application process is involved.
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf started > checked\nexec sleep 30\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				if err := repository.Write("go.mod", "module demo\n"); err != nil {
					t.Fatal(err)
				}
				go func() {
					for ctx.Err() == nil {
						if _, err := os.Stat(filepath.Join(repository.Root, "checked")); err == nil {
							cancel()
							return
						}
						time.Sleep(5 * time.Millisecond)
					}
				}()
			}
			server, seen := scripted(t, editsAndFinishes("edit", "@a.ts\n-old\n+new\n", "Finished."))
			defer server.Close()
			client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
			outcome, err := NewRunner(client, repository).Run(ctx, "change it", nil, nil, progress)
			if !errors.Is(err, context.Canceled) || outcome.Reply != "" || len(*seen) != 1 {
				t.Fatalf("outcome=%+v err=%v requests=%d", outcome, err, len(*seen))
			}
			content, _ := repository.ReadFile("a.ts")
			if when == "before edit" && content != "old\n" {
				t.Fatal("canceled tool wrote a file")
			}
			if when != "before edit" && content != "new\n" {
				t.Fatal("test did not reach the accepted edit")
			}
			if when == "during check" {
				if _, err := os.Stat(filepath.Join(repository.Root, "checked")); err != nil {
					t.Fatal("test did not reach the check")
				}
			}
			if strings.Contains(strings.Join(progress.logs, "\n"), "check passed") {
				t.Fatal("canceled check reported success")
			}
		})
	}
}
