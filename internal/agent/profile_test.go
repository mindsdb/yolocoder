package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mindsdb/yolocoder/internal/repo"
)

func TestProfileRecordFoldsRepeatsKeepingFirstOrder(t *testing.T) {
	var profile Profile
	profile.record(StepThink, 2*time.Second)
	profile.record(StepTools, 10*time.Millisecond)
	profile.record(StepThink, 3*time.Second)
	profile.record(StepTools, 5*time.Millisecond)

	spans := profile.Spans()
	if len(spans) != 2 {
		t.Fatalf("Spans() = %d entries, want 2 (repeats should fold)", len(spans))
	}
	if spans[0].Step != StepThink || spans[1].Step != StepTools {
		t.Fatalf("Spans() = %v, want think first (the order they ran)", spans)
	}
	if spans[0].Spent != 5*time.Second || spans[0].Calls != 2 {
		t.Fatalf("think span = %+v, want 5s over 2 calls", spans[0])
	}
	if got, want := profile.Total(), 5*time.Second+15*time.Millisecond; got != want {
		t.Fatalf("Total() = %v, want %v", got, want)
	}
}

func TestProfileEmpty(t *testing.T) {
	if !(Profile{}).Empty() {
		t.Fatal("a zero Profile should be Empty")
	}
	if (Profile{}).Summary() != "" {
		t.Fatal("Summary() of an empty Profile should be empty, so a caller can skip the line")
	}
	var profile Profile
	profile.record(StepMap, time.Millisecond)
	if profile.Empty() {
		t.Fatal("a Profile with a recorded step should not be Empty")
	}
}

func TestProfileSummary(t *testing.T) {
	var profile Profile
	profile.record(StepRecall, 1050*time.Millisecond)
	profile.record(StepMap, 11*time.Millisecond)
	profile.record(StepThink, 2*time.Second)
	profile.record(StepThink, 2700*time.Millisecond)

	// Only the total: the steps each streamed their own time onto the
	// trail already, so the footer is for the one comparable number.
	if got, want := profile.Summary(), "5.8s total"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
	// The breakdown is still there for anyone who wants it.
	if got := len(profile.Spans()); got != 3 {
		t.Fatalf("Spans() = %d, want 3 kept alongside the total", got)
	}
}

func TestRecallDetailLine(t *testing.T) {
	detail := RecallDetail{Available: 45, Served: 45, Bytes: 7987, Spent: 3 * time.Millisecond}
	if got, want := detail.Line(), "recalled 45 earlier turns · 7.8 KB"; got != want {
		t.Fatalf("Line() = %q, want %q", got, want)
	}
	if got, want := (RecallDetail{Served: 1, Bytes: 312}).Line(), "recalled 1 earlier turn · 312 B"; got != want {
		t.Fatalf("Line() singular = %q, want %q", got, want)
	}
}

func TestFormatDuration(t *testing.T) {
	for _, testCase := range []struct {
		spent time.Duration
		want  string
	}{
		{0, "0ms"},
		{500 * time.Microsecond, "<1ms"},
		{time.Millisecond, "1ms"},
		{999 * time.Millisecond, "999ms"},
		{time.Second, "1.0s"},
		{90 * time.Second, "90.0s"},
	} {
		if got := formatDuration(testCase.spent); got != testCase.want {
			t.Errorf("formatDuration(%v) = %q, want %q", testCase.spent, got, testCase.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	if got, want := formatBytes(512), "512 B"; got != want {
		t.Errorf("formatBytes(512) = %q, want %q", got, want)
	}
	if got, want := formatBytes(7987), "7.8 KB"; got != want {
		t.Errorf("formatBytes(7987) = %q, want %q", got, want)
	}
}

// TestRunProfilesTheTurn checks the wiring end to end: that a real Run
// fills the profile as it goes and hands it back on the Outcome, rather
// than the type merely working in isolation.
func TestRunProfilesTheTurn(t *testing.T) {
	var round int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.Header().Set("Content-Type", "application/json")
		round++
		switch round {
		case 1: // asks to read a file
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"function_call","name":"read_files","call_id":"c1","arguments":"{\"paths\":[\"a.txt\"]}"}]}`)
		default: // answers without a diff
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"summary\":\"\",\"files_to_modify\":[],\"diff\":\"\",\"answer\":\"it says hello\"}"}]}]}`)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	outcome, err := NewRunner(client, &repo.Repository{Root: root}).
		Run(context.Background(), "what does a.txt say", nil, pastTurns, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}

	profile := outcome.Profile
	if profile.Empty() {
		t.Fatal("Run should hand back a profile of the turn")
	}
	steps := map[Step]Span{}
	for _, span := range profile.Spans() {
		steps[span.Step] = span
	}
	for _, step := range []Step{StepMap, StepThink, StepTools} {
		if _, measured := steps[step]; !measured {
			t.Errorf("%s was not measured; profile = %q", step, profile.Summary())
		}
	}
	// Two calls: the one that asked to read, and the one that answered.
	// Folding them into a single counted span is the point.
	if got := steps[StepThink].Calls; got != 2 {
		t.Errorf("think calls = %d, want 2", got)
	}
	// A question answered from the files never patches or tests, so those
	// steps should be absent rather than present at zero.
	if _, measured := steps[StepPatch]; measured {
		t.Error("a turn that changed nothing should record no patch step")
	}

	// pastTurns is exactly the inline window, so nothing was left behind
	// it for recall to offer and nothing was served.
	if profile.Recall.Available != 0 || profile.Recall.Served != 0 {
		t.Errorf("Recall = %+v, want nothing beyond the inline turns", profile.Recall)
	}
}
