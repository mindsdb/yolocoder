package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

func feedbackCall(t *testing.T, reply string) responseItem {
	t.Helper()
	var response struct {
		Output []responseItem `json:"output"`
	}
	if err := json.Unmarshal([]byte(reply), &response); err != nil || len(response.Output) != 1 {
		t.Fatalf("invalid test reply: %s: %v", reply, err)
	}
	return response.Output[0]
}

func feedbackResult(t *testing.T, input any, id string) string {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var items []toolOutput
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Type == "function_call_output" && item.CallID == id {
			return item.Output
		}
	}
	t.Fatalf("missing tool result %s", id)
	return ""
}

func assertRemainingEdits(t *testing.T, output string, remaining int) {
	t.Helper()
	var got int
	_, tail, found := strings.Cut(output, "Remaining edit calls this turn: ")
	if !found {
		t.Fatalf("missing remaining budget: %s", output)
	}
	if _, err := fmt.Sscanf(tail, "%d", &got); err != nil || got != remaining {
		t.Fatalf("remaining=%d, want %d: %s", got, remaining, output)
	}
}

// Exercise feedback delivered on real outgoing requests, including a rejected
// multi-file patch whose placeable half must not overwrite an earlier success.
// A later accepted completion still has to survive the detected project check.
func TestEditFeedbackPreservesEarlierSuccessThroughRepairAndCheck(t *testing.T) {
	check := `node -e "const fs=require('fs');const state=[fs.readFileSync('store.txt','utf8').trim(),fs.readFileSync('view.txt','utf8').trim()];fs.appendFileSync('checked',JSON.stringify(state)+'\n');process.exitCode=state[0]==='saved'&&state[1]==='good'?0:1"`
	pkg, _ := json.Marshal(map[string]any{"scripts": map[string]string{"test": check}})
	repository := folder(t, map[string]string{"store.txt": "old\n", "view.txt": "old\n", "package.json": string(pkg)})
	server, seen := scripted(t,
		edits("save", "@store.txt\n-old\n+saved\n"),
		editsAndFinishes("reject", "@store.txt\n-saved\n+overwritten\n\n@view.txt\n-absent\n+good\n", "Premature completion."),
		editsAndFinishes("repair", "@view.txt\n-old\n+bad\n", "Check must reject this."),
		editsAndFinishes("finish", "@view.txt\n-bad\n+good\n", "Both parts complete."),
	)
	defer server.Close()
	runner := NewRunner(&Client{endpoint: server.URL, model: "m", http: server.Client()}, repository)
	session := runner.newChangeSession("Save the record and finish the view", "", nil, nil)
	progress := &recordingProgress{}
	outcome, err := session.work(context.Background(), progress)
	server.Close()
	checked, _ := os.ReadFile(filepath.Join(repository.Root, "checked"))
	wantChecked := "[\"saved\",\"bad\"]\n[\"saved\",\"good\"]\n"
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		wantChecked = "[\"saved\",\"old\"]\n" + wantChecked // Earlier diagnostic does not replace final repair.
	}
	if err != nil || !outcome.Applied || outcome.Attempts != 4 || outcome.Reply != "Both parts complete." ||
		len(*seen) != 4 || string(checked) != wantChecked {
		t.Fatalf("outcome=%+v err=%v requests=%d checks=%q", outcome, err, len(*seen), checked)
	}
	for i, id := range []string{"save", "reject", "repair"} {
		assertRemainingEdits(t, feedbackResult(t, (*seen)[i+1]["input"], id), 3-i)
		assertCompletionSchema(t, (*seen)[i+1]["tools"], true)
	}
	// The final result exists locally, but no extra provider request is needed
	// merely to transmit its remaining-zero feedback after checked completion.
	assertRemainingEdits(t, feedbackResult(t, session.transcript, "finish"), 0)
	rejected := feedbackResult(t, (*seen)[2]["input"], "reject")
	if !strings.Contains(rejected, "this call") || !strings.Contains(rejected, "earlier accepted edits") {
		t.Fatalf("rejection loses the scope of its rollback claim: %s", rejected)
	}
	logs := strings.Join(progress.logs, "\n")
	if strings.Count(logs, "diagnostic checkpoint:") != 1 || strings.Count(logs, "check failed, back to it") != 1 || strings.Count(logs, "check passed") != 1 || session.used["read_files"] != 0 {
		t.Fatalf("check/extra-read behavior changed: %s; reads=%d", logs, session.used["read_files"])
	}
}

func TestEditFeedbackChargesMalformedAndRejectedCallsBeforeLimit(t *testing.T) {
	repository := folder(t, map[string]string{"a.txt": "old\n"})
	session := NewRunner(&Client{}, repository).newChangeSession("change it", "", nil, nil)
	ctx := context.Background()
	output, _, ok := session.runTool(ctx, feedbackCall(t, edits("one", "@a.txt\n-old\n+first\n")))
	if !ok {
		t.Fatal(output)
	}
	assertRemainingEdits(t, output, 3)
	output, _, ok = session.runTool(ctx, responseItem{Name: "apply_diff", Arguments: "{"})
	if ok || session.used["apply_diff"] != 2 {
		t.Fatalf("malformed call was not charged: %s", output)
	}
	output, _, ok = session.runTool(ctx, feedbackCall(t, editsAndFinishes("three", "@a.txt\n-absent\n+wrong\n", "Done.")))
	if ok || session.complete || session.closing != "" {
		t.Fatalf("rejection kept completion: %s", output)
	}
	assertRemainingEdits(t, output, 1)
	output, _, ok = session.runTool(ctx, feedbackCall(t, editsAndFinishes("four", "@a.txt\n-first\n+final\n", "Done.")))
	if !ok || !session.complete {
		t.Fatal(output)
	}
	assertRemainingEdits(t, output, 0)
	output, _, ok = session.runTool(ctx, feedbackCall(t, editsAndFinishes("five", "@a.txt\n-final\n+forbidden\n", "Done.")))
	content, _ := repository.ReadFile("a.txt")
	if ok || session.used["apply_diff"] != 4 || content != "final\n" || session.complete || session.closing != "" || !strings.Contains(output, "which is the limit") {
		t.Fatalf("fifth call was not refused safely: %s; used=%d content=%q", output, session.used["apply_diff"], content)
	}
}

func TestEditFeedbackDoesNotClaimAtomicityForOtherErrors(t *testing.T) {
	for name, patch := range map[string]string{
		"read error":        "@directory\n-old\n+new\n",
		"wrapped git error": "--- a/a.txt\n+++ b/a.txt\n@@\n-absent\n+new\n",
	} {
		t.Run(name, func(t *testing.T) {
			repository := folder(t, map[string]string{"a.txt": "old\n", "directory/child": "file\n"})
			session := NewRunner(&Client{}, repository).newChangeSession("change it", "", nil, nil)
			output, _, ok := session.runTool(context.Background(), feedbackCall(t, edits("error", patch)))
			if ok || session.lastFailure == nil || !strings.HasPrefix(output, "The patch failed.") || strings.Contains(output, "Nothing was changed") || strings.Contains(output, "not rolled back") {
				t.Fatalf("unsafe failure claim: %s", output)
			}
			if name == "wrapped git error" {
				var placement *repo.PatchError
				if !errors.As(session.lastFailure, &placement) || session.lastFailure == placement {
					t.Fatalf("test did not reach a wrapped placement error: %v", session.lastFailure)
				}
			}
		})
	}
}

func TestEditFeedbackLeavesSmallUIResultsUnchanged(t *testing.T) {
	repository := folder(t, map[string]string{"a.txt": "old\n"})
	runner := NewRunner(&Client{}, repository)
	session := runner.newChangeSession("small edit", "", nil, nil)
	session.prefetched = true
	output, _, ok := session.runTool(context.Background(), feedbackCall(t, edits("one", "@a.txt\n-old\n+new\n")))
	want := "Applied. Changed: a.txt. Every edit in that patch was placed; the files contain them now. Do not read them again to check."
	if !ok || output != want {
		t.Fatalf("small-UI success changed: %q", output)
	}
	if _, err := runner.readFiles([]string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	output, _, ok = session.runTool(context.Background(), feedbackCall(t, edits("two", "@a.txt\n-absent\n+wrong\n")))
	if ok || session.lastFailure == nil {
		t.Fatal("test did not reach placement rejection")
	}
	want = "The patch did not apply and nothing was changed.\n\n" + session.lastFailure.Error() + session.staleContents(session.lastFailure)
	if output != want {
		t.Fatalf("small-UI rejection changed: %q", output)
	}
}
