package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

var pastTurns = []Recollection{
	{Number: 1, Message: "make it multilingual", Summary: "Added i18n", Files: []string{"translations.js"}},
	{Number: 2, Message: "hi", Summary: "Hello!"},
	{Number: 3, Message: "fit the side cards too", Summary: "Compacted the cards", Files: []string{"base_index.html"}},
}

// answerOnce replies to every call with a Change carrying only an answer,
// which ends the turn without a patch or a test run.
func answerOnce(writer http.ResponseWriter, answer string) {
	payload, _ := json.Marshal(Change{Answer: answer})
	fmt.Fprintf(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":%s}]}]}`, strconv.Quote(string(payload)))
}

// firstInput is the opening message of the first request a server saw.
func firstInput(t *testing.T, bodies []map[string]any) string {
	t.Helper()
	parts, ok := bodies[0]["input"].([]any)
	if !ok || len(parts) == 0 {
		t.Fatalf("input = %#v, want the opening message", bodies[0]["input"])
	}
	message, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("input part = %#v, want a message object", parts[0])
	}
	text, _ := message["content"].(string)
	return text
}

func recordingServer(t *testing.T, handle func(writer http.ResponseWriter, round int)) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		bodies = append(bodies, body)
		writer.Header().Set("Content-Type", "application/json")
		handle(writer, len(bodies))
	}))
	return server, &bodies
}

func TestHistoryIsOfferedRatherThanSent(t *testing.T) {
	// The turns themselves must not be in the opening message: the whole
	// point of the recall tool is that a turn pays for history only when
	// it says it needs it.
	server, bodies := recordingServer(t, func(writer http.ResponseWriter, round int) {
		answerOnce(writer, "no need")
	})
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	if _, err := NewRunner(client, &repo.Repository{Root: t.TempDir()}).
		Run(context.Background(), "hi", nil, pastTurns, &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	opening := firstInput(t, *bodies)
	if strings.Contains(opening, "make it multilingual") {
		t.Fatalf("history should not be sent unasked:\n%s", opening)
	}
	// But it must say the history is there, or the model cannot know
	// what it is missing and will never reach for it.
	if !strings.Contains(opening, "3 earlier turns") || !strings.Contains(opening, "recall") {
		t.Fatalf("the opening should offer the history it is holding back:\n%s", opening)
	}
}

func TestRecallServesEveryTurnUnfiltered(t *testing.T) {
	runner := NewRunner(&Client{}, &repo.Repository{Root: t.TempDir()})
	runner.earlier = pastTurns

	served := runner.recallEarlier()
	for _, want := range []string{"make it multilingual", "hi", "fit the side cards too", "Compacted the cards"} {
		if !strings.Contains(served, want) {
			t.Fatalf("recall dropped %q; there is no selection any more:\n%s", want, served)
		}
	}
	if runner.profile.Recall.Served != len(pastTurns) {
		t.Fatalf("Recall.Served = %d, want %d", runner.profile.Recall.Served, len(pastTurns))
	}
	if runner.profile.Recall.Bytes != len(renderHistory(pastTurns)) {
		t.Fatal("Recall.Bytes should size what was actually handed over")
	}

	// A second call costs a sentence, not another copy in a transcript
	// that is resent on every following round.
	if again := runner.recallEarlier(); strings.Contains(again, "make it multilingual") {
		t.Fatalf("recall resent history it had already served:\n%s", again)
	}
}

func TestRecallOnAFolderWithNoHistorySaysSo(t *testing.T) {
	runner := NewRunner(&Client{}, &repo.Repository{Root: t.TempDir()})
	if got := runner.recallEarlier(); !strings.Contains(got, "Nothing earlier") {
		t.Fatalf("recall on an empty folder = %q", got)
	}
}

func TestRunServesHistoryWhenTheModelAsksForIt(t *testing.T) {
	server, bodies := recordingServer(t, func(writer http.ResponseWriter, round int) {
		if round == 1 {
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"function_call","name":"recall","call_id":"c1","arguments":"{\"reason\":\"the message says keep going\"}"}]}`)
			return
		}
		answerOnce(writer, "picked up where we left off")
	})
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	progress := &recordingProgress{}
	outcome, err := NewRunner(client, &repo.Repository{Root: t.TempDir()}).
		Run(context.Background(), "keep going", nil, pastTurns, progress)
	if err != nil {
		t.Fatal(err)
	}
	// The turns reach the model as a tool result on the second request.
	encoded, _ := json.Marshal((*bodies)[1]["input"])
	if !strings.Contains(string(encoded), "fit the side cards too") {
		t.Fatalf("the recalled turns never reached the model:\n%s", encoded)
	}
	if outcome.Profile.Recall.Available != len(pastTurns) || outcome.Profile.Recall.Served != len(pastTurns) {
		t.Fatalf("Recall = %+v, want all turns available and served", outcome.Profile.Recall)
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "the message says keep going") {
		t.Fatalf("the trail should say why it reached back:\n%s", trail)
	}
}

func TestATurnThatNeverRecallsRecordsThatItDidNot(t *testing.T) {
	// The measurement the design rests on: history was there, and this
	// turn did not need it. Served staying zero is the answer, not a gap.
	server, _ := recordingServer(t, func(writer http.ResponseWriter, round int) {
		answerOnce(writer, "stands on its own")
	})
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	outcome, err := NewRunner(client, &repo.Repository{Root: t.TempDir()}).
		Run(context.Background(), "what is 2 + 2", nil, pastTurns, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Profile.Recall.Available != len(pastTurns) {
		t.Fatalf("Recall.Available = %d, want %d", outcome.Profile.Recall.Available, len(pastTurns))
	}
	if outcome.Profile.Recall.Served != 0 || outcome.Profile.Recall.Spent != 0 {
		t.Fatalf("Recall = %+v, want nothing served on a turn that never asked", outcome.Profile.Recall)
	}
}

func TestOpeningPutsTheStableBlocksFirst(t *testing.T) {
	// Prefix caching only reuses an unchanged prefix, so the map — the
	// big block that moves only when the folder does — has to come ahead
	// of the message, which is different every turn.
	server, bodies := recordingServer(t, func(writer http.ResponseWriter, round int) {
		answerOnce(writer, "ok")
	})
	defer server.Close()

	root := t.TempDir()
	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	notes := []Recollection{{Message: "this project ships weekly", Note: true}}
	if _, err := NewRunner(client, &repo.Repository{Root: root}).
		Run(context.Background(), "do the thing", nil, notes, &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	opening := firstInput(t, *bodies)
	notesAt := strings.Index(opening, "PROJECT CONTEXT")
	mapAt := strings.Index(opening, "REPOSITORY MAP")
	taskAt := strings.Index(opening, "TASK:")
	if notesAt == -1 || mapAt == -1 || taskAt == -1 {
		t.Fatalf("opening is missing a block:\n%s", opening)
	}
	if !(notesAt < mapAt && mapAt < taskAt) {
		t.Fatalf("blocks must run most-stable to least:\n%s", opening)
	}
}

func TestSuppliedNotesAreAlwaysCarried(t *testing.T) {
	// Notes come from --context, handed in on purpose, so they are never
	// something the model has to ask for.
	server, bodies := recordingServer(t, func(writer http.ResponseWriter, round int) {
		answerOnce(writer, "ok")
	})
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	supplied := []Recollection{{Message: "the API is frozen until March", Note: true}}
	if _, err := NewRunner(client, &repo.Repository{Root: t.TempDir()}).
		Run(context.Background(), "add a field", nil, supplied, &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	if opening := firstInput(t, *bodies); !strings.Contains(opening, "the API is frozen until March") {
		t.Fatalf("supplied notes must always be carried:\n%s", opening)
	}
}
