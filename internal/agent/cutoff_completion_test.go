package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func incompleteReply(t *testing.T, body string, reason string) string {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatal(err)
	}
	value["status"] = "incomplete"
	value["incomplete_details"] = map[string]string{"reason": reason}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// A real detected project check records each execution and the state it saw.
func cutoffCheck(t *testing.T, session *changeSession) {
	t.Helper()
	check := `node -e "const fs=require('fs');fs.appendFileSync('checked',fs.readFileSync('a.txt','utf8'))"`
	pkg, _ := json.Marshal(map[string]any{"scripts": map[string]string{"test": check}})
	if err := session.runner.repository.Write("package.json", string(pkg)); err != nil {
		t.Fatal(err)
	}
}

func TestCutoffCompletionPlainTextContinuesBeforeChecking(t *testing.T) {
	for name, partial := range map[string]string{
		"nonempty": finishes("All done, except"),
		"empty":    finishes(""),
		"absent":   `{"output":[]}`,
		"nontext":  `{"output":[{"type":"message","content":[{"type":"refusal"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			session, seen := retryScript(t,
				retryReply{status: 200, body: edits("edit", "@a.txt\n-old\n+new\n")},
				retryReply{status: 200, body: incompleteReply(t, partial, "max_output_tokens")},
				retryReply{status: 200, body: finishes("Complete answer.")},
			)
			cutoffCheck(t, session)
			progress := &recordingProgress{}
			outcome, err := session.work(context.Background(), progress)
			checked, _ := os.ReadFile(filepath.Join(session.runner.repository.Root, "checked"))
			wantChecked := "new\n"
			if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
				wantChecked = "new\nnew\n" // Earlier incomplete edit diagnostic, then mandatory final check.
			}
			if err != nil || outcome.Reply != "Complete answer." || outcome.Attempts != 1 || len(*seen) != 3 || string(checked) != wantChecked {
				t.Fatalf("outcome=%+v err=%v requests=%d checks=%q", outcome, err, len(*seen), checked)
			}
			var request responseRequest
			if err := json.Unmarshal([]byte((*seen)[2].body), &request); err != nil {
				t.Fatal(err)
			}
			input, _ := json.Marshal(request.Input)
			if !strings.Contains(string(input), "cut off") || strings.Contains(string(input), "All done, except") {
				t.Fatalf("partial reply was accepted or continuation absent: %s", input)
			}
			logs := strings.Join(progress.logs, "\n")
			if strings.Count(logs, "check passed") != 1 || strings.Count(logs, "diagnostic checkpoint:") != 1 {
				t.Fatal("cutoff changed the earlier diagnostic or final check:", logs)
			}
		})
	}
}

func TestCutoffCompletionDrainsBatchOnceThenAcceptsCleanCompletion(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid batch", true: "malformed sibling"}[malformed], func(t *testing.T) {
			batch := []string{
				editsAndFinishes("first", "@a.txt\n-old\n+new\n", "Premature one."),
				editsAndFinishes("second", "@b.txt\n-old\n+new\n", "Premature two."),
			}
			if malformed {
				batch = append(batch, calls("apply_diff", "broken", "{"))
			}
			session, seen := retryScript(t,
				retryReply{status: 200, body: incompleteReply(t, batchReplies(t, batch...), "max_output_tokens")},
				retryReply{status: 200, body: editsAndFinishes("finish", "@a.txt\n-new\n+final\n", "Complete answer.")},
			)
			if err := session.runner.repository.Write("b.txt", "old\n"); err != nil {
				t.Fatal(err)
			}
			cutoffCheck(t, session)
			progress := &recordingProgress{}
			outcome, err := session.work(context.Background(), progress)
			checked, _ := os.ReadFile(filepath.Join(session.runner.repository.Root, "checked"))
			b, _ := session.runner.repository.ReadFile("b.txt")
			if err != nil || outcome.Reply != "Complete answer." || outcome.Attempts != len(batch)+1 || len(*seen) != 2 || b != "new\n" || string(checked) != "final\n" {
				t.Fatalf("outcome=%+v err=%v requests=%d b=%q checks=%q", outcome, err, len(*seen), b, checked)
			}
			var request map[string]any
			_ = json.Unmarshal([]byte((*seen)[1].body), &request)
			for _, id := range []string{"first", "second"} {
				output := feedbackResult(t, request["input"], id)
				if strings.HasPrefix(output, "ERROR") {
					t.Fatal("valid cutoff call did not apply:", output)
				}
			}
			if malformed && !strings.Contains(feedbackResult(t, request["input"], "broken"), "ERROR") {
				t.Fatal("malformed sibling result was hidden")
			}
			var outputs []toolOutput
			encoded, _ := json.Marshal(request["input"])
			_ = json.Unmarshal(encoded, &outputs)
			counts := map[string]int{}
			for _, output := range outputs {
				if output.Type == "function_call_output" {
					counts[output.CallID]++
				}
			}
			if counts["first"] != 1 || counts["second"] != 1 {
				t.Fatalf("cutoff tools were replayed: %v", counts)
			}
			if strings.Count(strings.Join(progress.logs, "\n"), "check passed") != 1 {
				t.Fatal("cutoff checked before continuation")
			}
		})
	}
}

func TestCutoffCompletionKeepsEditAndRoundLimits(t *testing.T) {
	t.Run("edit quota", func(t *testing.T) {
		batch := batchReplies(t,
			edits("one", "@a.txt\n-old\n+one\n"), edits("two", "@a.txt\n-one\n+two\n"),
			edits("three", "@a.txt\n-two\n+three\n"), editsAndFinishes("four", "@a.txt\n-three\n+four\n", "Partial."),
			editsAndFinishes("five", "@a.txt\n-four\n+five\n", "Wrong."))
		session, seen := retryScript(t,
			retryReply{status: 200, body: incompleteReply(t, batch, "max_output_tokens")},
			retryReply{status: 200, body: finishes("Only the allowed edits landed.")})
		cutoffCheck(t, session)
		outcome, err := session.work(context.Background(), &recordingProgress{})
		got, _ := session.runner.repository.ReadFile("a.txt")
		if err != nil || outcome.Attempts != 4 || got != "four\n" || len(*seen) != 2 {
			t.Fatalf("outcome=%+v err=%v a=%q requests=%d", outcome, err, got, len(*seen))
		}
		if output := feedbackResult(t, session.transcript, "five"); !strings.Contains(output, "limit") {
			t.Fatal("fifth call was not refused:", output)
		}
	})
	t.Run("round cap", func(t *testing.T) {
		replies := make([]retryReply, maxRounds)
		for i := range replies {
			replies[i] = retryReply{status: 200, body: incompleteReply(t, finishes("Unfinished"), "max_output_tokens")}
		}
		session, seen := retryScript(t, replies...)
		outcome, err := session.work(context.Background(), &recordingProgress{})
		if !errors.Is(err, errOutOfRounds) || len(*seen) != maxRounds || outcome.Reply != "" || session.used["apply_diff"] != 0 {
			t.Fatalf("outcome=%+v err=%v requests=%d", outcome, err, len(*seen))
		}
	})
}

func TestCutoffCompletionCancellationStopsDraining(t *testing.T) {
	session, seen := retryScript(t, retryReply{status: 200, body: incompleteReply(t, batchReplies(t,
		editsAndFinishes("one", "@a.txt\n-old\n+new\n", "Partial."),
		editsAndFinishes("two", "@a.txt\n-new\n+wrong\n", "Wrong.")), "max_output_tokens")})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	progress := &cancellingProgress{cancel: cancel, when: "after edit"}
	outcome, err := session.work(ctx, progress)
	got, _ := session.runner.repository.ReadFile("a.txt")
	if !errors.Is(err, context.Canceled) || outcome.Reply != "" || got != "new\n" || session.used["apply_diff"] != 1 || len(*seen) != 1 {
		t.Fatalf("outcome=%+v err=%v a=%q requests=%d", outcome, err, got, len(*seen))
	}
	if strings.Contains(strings.Join(progress.logs, "\n"), "check passed") {
		t.Fatal("canceled batch checked or completed")
	}
}

func TestCutoffCompletionPreservesSmallUIAndOtherReasons(t *testing.T) {
	for _, small := range []bool{false, true} {
		for _, tool := range []bool{false, true} {
			name := map[bool]string{false: "normal other reason", true: "small UI cutoff"}[small]
			t.Run(name+map[bool]string{false: " plain", true: " tool"}[tool], func(t *testing.T) {
				body := finishes("Existing finish.")
				if tool {
					body = editsAndFinishes("one", "@a.txt\n-old\n+new\n", "Existing finish.")
				}
				reason := "content_filter"
				if small {
					reason = "max_output_tokens"
				}
				session, seen := retryScript(t, retryReply{status: 200, body: incompleteReply(t, body, reason)})
				session.prefetched = small
				outcome, err := session.work(context.Background(), &recordingProgress{})
				if err != nil || !strings.HasPrefix(outcome.Reply, "Existing finish.") || len(*seen) != 1 {
					t.Fatalf("outcome=%+v err=%v requests=%d", outcome, err, len(*seen))
				}
			})
		}
	}
	t.Run("missing terminal text without cutoff", func(t *testing.T) {
		session, seen := retryScript(t, retryReply{status: 200, body: `{"output":[]}`})
		outcome, err := session.work(context.Background(), &recordingProgress{})
		if err == nil || outcome.Reply != "" || len(*seen) != 1 {
			t.Fatalf("outcome=%+v err=%v requests=%d", outcome, err, len(*seen))
		}
	})
}
