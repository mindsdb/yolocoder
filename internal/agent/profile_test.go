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

	want := "5.8s total · recall 1.1s · map 11ms · think 4.7s ×2"
	if got := profile.Summary(); got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
}

func TestRecallDetailLine(t *testing.T) {
	// No history to select from: the call only routed, so saying "0 of 0
	// turns" would be reporting on work that never happened.
	detail := RecallDetail{Spent: 900 * time.Millisecond}
	if got, want := detail.Line(), "routed the message · 900ms"; got != want {
		t.Fatalf("Line() with no history = %q, want %q", got, want)
	}

	detail = RecallDetail{Offered: 45, Bytes: 7987, Chosen: 1, Spent: 1040 * time.Millisecond}
	want := "recalled 1 of 45 earlier turns · 7.8 KB offered · 1.0s"
	if got := detail.Line(); got != want {
		t.Fatalf("Line() = %q, want %q", got, want)
	}

	// The case worth noticing: a lot of history read to choose none of it.
	detail = RecallDetail{Offered: 20, Bytes: 8192, Chosen: 0, Spent: 2 * time.Second}
	want = "recalled 0 of 20 earlier turns · 8.0 KB offered · 2.0s"
	if got := detail.Line(); got != want {
		t.Fatalf("Line() choosing nothing = %q, want %q", got, want)
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
		case 1: // message_route
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"coding_task\":true,\"reply\":\"\",\"relevant\":[3],\"context\":\"carry on\"}"}]}]}`)
		case 2: // code_change, asking to read a file
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"function_call","name":"read_files","call_id":"c1","arguments":"{\"paths\":[\"a.txt\"]}"}]}`)
		default: // code_change, answering without a diff
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
	for _, step := range []Step{StepRecall, StepMap, StepThink, StepTools} {
		if _, measured := steps[step]; !measured {
			t.Errorf("%s was not measured; profile = %q", step, profile.Summary())
		}
	}
	// Two code_change calls: the one that asked to read, and the one that
	// answered. Folding them into a single counted span is the point.
	if got := steps[StepThink].Calls; got != 2 {
		t.Errorf("think calls = %d, want 2", got)
	}
	// A question answered from the files never patches or tests, so those
	// steps should be absent rather than present at zero.
	if _, measured := steps[StepPatch]; measured {
		t.Error("a turn that changed nothing should record no patch step")
	}

	// The selection detail is the part this exists for: it should say how
	// much history it was handed, not just how long it took.
	if profile.Recall.Offered != len(pastTurns) {
		t.Errorf("Recall.Offered = %d, want %d", profile.Recall.Offered, len(pastTurns))
	}
	if profile.Recall.Chosen != 1 {
		t.Errorf("Recall.Chosen = %d, want 1", profile.Recall.Chosen)
	}
	if profile.Recall.Bytes == 0 {
		t.Error("Recall.Bytes should size the history the selection had to read")
	}
}
