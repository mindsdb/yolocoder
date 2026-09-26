package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func timingNotes(t *testing.T, input any) []string {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var messages []struct{ Role, Content string }
	if err := json.Unmarshal(encoded, &messages); err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, message := range messages {
		if strings.HasPrefix(message.Content, "Runtime timing:") {
			if message.Role != "user" || len(message.Content) > 512 {
				t.Fatalf("unbounded or misplaced timing note: %+v", message)
			}
			notes = append(notes, message.Content)
		}
	}
	return notes
}

func timingNumber(t *testing.T, text, field string) float64 {
	t.Helper()
	match := regexp.MustCompile(field + ` ([0-9]+\.[0-9]) seconds`).FindStringSubmatch(text)
	if len(match) != 2 {
		t.Fatalf("missing numeric timing %q in %q", field, text)
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestElapsedFeedbackUsesActualClockAndParentDeadline(t *testing.T) {
	start := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), start.Add(70*time.Second))
	defer cancel()
	session := &changeSession{transcript: []any{inputMessage{Role: "user", Content: "Change both files."}}}
	before, _ := json.Marshal(session.transcript)
	for _, elapsed := range []time.Duration{0, 12300 * time.Millisecond, 80 * time.Second} {
		notes := timingNotes(t, session.timedInput(ctx, start, start.Add(elapsed)))
		if len(notes) != 1 {
			t.Fatal("timing notes accumulated", notes)
		}
		if got := timingNumber(t, notes[0], "elapsed"); got != elapsed.Seconds() {
			t.Fatalf("elapsed=%v want=%v", got, elapsed.Seconds())
		}
		if got := timingNumber(t, notes[0], "remaining:"); got != max(0, 70-elapsed.Seconds()) {
			t.Fatalf("remaining=%v", got)
		}
	}
	noDeadline := timingNotes(t, session.timedInput(context.Background(), start, start.Add(time.Second)))[0]
	if strings.Contains(noDeadline, "deadline") || strings.Contains(noDeadline, "180") {
		t.Fatal("invented an external deadline", noDeadline)
	}
	negative := timingNotes(t, session.timedInput(context.Background(), start, start.Add(-time.Second)))[0]
	if timingNumber(t, negative, "elapsed") != 0 {
		t.Fatal("negative timing leaked")
	}
	after, _ := json.Marshal(session.transcript)
	if string(before) != string(after) {
		t.Fatal("request metadata changed persistent transcript")
	}
}

func TestElapsedFeedbackDoesNotAliasTranscriptSpareCapacity(t *testing.T) {
	backing := []any{inputMessage{Role: "user", Content: "task"}, "sentinel", "spare"}
	session := &changeSession{transcript: backing[:1]}
	input := session.timedInput(context.Background(), time.Now(), time.Now())
	input[0] = "request-only change"
	if backing[0] == input[0] || backing[1] != "sentinel" || backing[2] != "spare" {
		t.Fatal("timing request reused or overwrote transcript backing storage")
	}
}

func TestElapsedFeedbackKeepsNormalCompletionCutoffAndFinalCheck(t *testing.T) {
	session, seen := retryScript(t,
		retryReply{status: 200, body: edits("first", "@a.txt\n-old\n+middle\n")},
		retryReply{status: 200, body: incompleteReply(t, finishes("Not finished"), "max_output_tokens")},
		retryReply{status: 200, body: editsAndFinishes("last", "@a.txt\n-middle\n+done\n", "Done.")},
	)
	cutoffCheck(t, session)
	beforeInstructions := session.instructions()
	progress := &recordingProgress{}
	outcome, err := session.work(context.Background(), progress)
	checked, _ := os.ReadFile(filepath.Join(session.runner.repository.Root, "checked"))
	wantChecked := "done\n"
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		wantChecked = "middle\ndone\n" // Incomplete-batch diagnostic, then mandatory final check.
	}
	if err != nil || outcome.Attempts != 2 || outcome.Reply != "Done." || len(*seen) != 3 || string(checked) != wantChecked {
		t.Fatalf("outcome=%+v err=%v calls=%d check=%q", outcome, err, len(*seen), checked)
	}
	lastElapsed := -1.0
	for i, call := range *seen {
		var request map[string]any
		if err := json.Unmarshal([]byte(call.body), &request); err != nil {
			t.Fatal(err)
		}
		notes := timingNotes(t, request["input"])
		if len(notes) != 1 || request["instructions"] != beforeInstructions || strings.Contains(notes[0], "deadline") {
			t.Fatalf("wrong request metadata/instructions: %+v", request)
		}
		if i == 1 {
			input, _ := json.Marshal(request["input"])
			if !strings.Contains(string(input), "Diagnostic checkpoint ") || !strings.Contains(string(input), "does not establish task completion") {
				t.Fatal("existing continuation lost diagnostic evidence alongside timing note")
			}
		}
		elapsed := timingNumber(t, notes[0], "elapsed")
		if elapsed < lastElapsed {
			t.Fatal("monotonic elapsed timing went backwards")
		}
		lastElapsed = elapsed
	}
	if len(timingNotes(t, session.transcript)) != 0 || strings.Count(strings.Join(progress.logs, "\n"), "check passed") != 1 {
		t.Fatal("metadata persisted or extra check ran")
	}
	if feedbackResult(t, session.transcript, "first") == "" || feedbackResult(t, session.transcript, "last") == "" {
		t.Fatal("tool results lost")
	}
}

func TestElapsedFeedbackLeavesSmallUIRequestUnchanged(t *testing.T) {
	session, seen := retryScript(t, retryReply{status: 200, body: finishes("Existing small edit answer.")})
	session.prefetched = true
	expected, _ := json.Marshal(session.transcript)
	instructions := session.instructions()
	outcome, err := session.work(context.Background(), &recordingProgress{})
	if err != nil || len(*seen) != 1 || outcome.Reply != "Existing small edit answer." {
		t.Fatalf("outcome=%+v err=%v calls=%d", outcome, err, len(*seen))
	}
	var request map[string]any
	_ = json.Unmarshal([]byte((*seen)[0].body), &request)
	var wanted any
	_ = json.Unmarshal(expected, &wanted)
	if !reflect.DeepEqual(wanted, request["input"]) || request["instructions"] != instructions {
		t.Fatal("small UI input or instructions changed")
	}
}

func TestElapsedFeedbackKeepsCancellationAndRoundLimit(t *testing.T) {
	t.Run("parent canceled after response", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		session, seen := retryScript(t, retryReply{status: 200,
			body: editsAndFinishes("blocked", "@a.txt\n-old\n+wrong\n", "Wrong."), onClose: cancel})
		_, err := session.work(ctx, &recordingProgress{})
		content, _ := session.runner.repository.ReadFile("a.txt")
		if !errors.Is(err, context.Canceled) || len(*seen) != 1 || session.used["apply_diff"] != 0 || content != "old\n" {
			t.Fatalf("err=%v calls=%d", err, len(*seen))
		}
	})
	t.Run("round limit", func(t *testing.T) {
		replies := make([]retryReply, maxRounds)
		for i := range replies {
			replies[i] = retryReply{status: 200, body: incompleteReply(t, finishes("Still partial"), "max_output_tokens")}
		}
		session, seen := retryScript(t, replies...)
		_, err := session.work(context.Background(), &recordingProgress{})
		if !errors.Is(err, errOutOfRounds) || len(*seen) != maxRounds || session.used["apply_diff"] != 0 {
			t.Fatalf("err=%v calls=%d used=%v", err, len(*seen), session.used)
		}
		for _, call := range *seen {
			var request map[string]any
			_ = json.Unmarshal([]byte(call.body), &request)
			if len(timingNotes(t, request["input"])) != 1 {
				t.Fatal("more than one current note on a request")
			}
		}
	})
}
