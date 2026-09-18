package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

var pastTurns = []Recollection{
	{Number: 1, Message: "make it multilingual", Summary: "Added i18n", Files: []string{"translations.js"}},
	{Number: 2, Message: "hi", Summary: "Hello!"},
	{Number: 3, Message: "fit the side cards too", Summary: "Compacted the cards", Files: []string{"base_index.html"}},
}

// answerOnce ends the turn with a plain reply and no tool call, which is
// how every turn finishes now.
func answerOnce(writer http.ResponseWriter, answer string) {
	fmt.Fprint(writer, finishes(answer))
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

// manyTurns is longer than the inline window, so some of it only exists
// behind the recall tool.
var manyTurns = []Recollection{
	{Number: 1, Message: "make it multilingual", Summary: "Added i18n", Files: []string{"translations.js"}},
	{Number: 2, Message: "hi", Summary: "Hello!"},
	{Number: 3, Message: "use a green palette", Summary: "Recoloured to Game Boy green"},
	{Number: 4, Message: "fit the side cards too", Summary: "Compacted the cards", Files: []string{"base_index.html"}},
	{Number: 5, Message: "add a room code", Summary: "Added room codes"},
	{Number: 6, Message: "shrink the header", Summary: "Header is smaller"},
}

func runOnce(t *testing.T, history []Recollection, recall bool, task string) ([]map[string]any, Outcome) {
	t.Helper()
	server, bodies := recordingServer(t, func(writer http.ResponseWriter, round int) {
		answerOnce(writer, "ok")
	})
	defer server.Close()
	runner := NewRunner(&Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()},
		&repo.Repository{Root: t.TempDir()})
	runner.UseRecall(recall)
	outcome, err := runner.Run(context.Background(), task, nil, history, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	return *bodies, outcome
}

func TestTheLastFewTurnsRideAlongUnasked(t *testing.T) {
	// Cheap enough not to be worth a round trip: the recent turns are
	// what a follow-up like "keep going" actually leans on.
	bodies, _ := runOnce(t, manyTurns, false, "keep going")
	opening := firstInput(t, bodies)
	for _, want := range []string{"fit the side cards too", "add a room code", "shrink the header"} {
		if !strings.Contains(opening, want) {
			t.Fatalf("the last turns should ride along; %q is missing:\n%s", want, opening)
		}
	}
	// And only the last few: the rest is what recall exists for.
	if strings.Contains(opening, "make it multilingual") {
		t.Fatalf("only the inline window belongs in the opening:\n%s", opening)
	}
}

func TestRecallIsNotOfferedByDefault(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range repositoryTools(false) {
		names[tool.Name] = true
	}
	if names["recall"] {
		t.Fatal("recall should be off unless it was turned on")
	}
	if !names["read_files"] || !names["search"] {
		t.Fatalf("the ordinary tools should still be there: %v", names)
	}
	if len(repositoryTools(true)) != len(repositoryTools(false))+1 {
		t.Fatal("turning recall on should add exactly the one tool")
	}

	// With it off, nothing should invite the model to reach further back.
	bodies, _ := runOnce(t, manyTurns, false, "keep going")
	if opening := firstInput(t, bodies); strings.Contains(opening, "recall") {
		t.Fatalf("the opening should not mention a tool that is not offered:\n%s", opening)
	}
}

func TestRecallOfferIsAnnouncedWhenItIsOn(t *testing.T) {
	// The model cannot know there is anything behind what it was shown
	// unless it is told, and the tool would sit unused.
	bodies, _ := runOnce(t, manyTurns, true, "keep going")
	opening := firstInput(t, bodies)
	if !strings.Contains(opening, "3 further turns") || !strings.Contains(opening, "recall") {
		t.Fatalf("the opening should offer what it is holding back:\n%s", opening)
	}
}

func TestRecallServesOnlyWhatIsOlderThanTheInlineTurns(t *testing.T) {
	runner := NewRunner(&Client{}, &repo.Repository{Root: t.TempDir()})
	runner.earlier = manyTurns

	served := runner.recallEarlier()
	for _, want := range []string{"make it multilingual", "hi", "use a green palette"} {
		if !strings.Contains(served, want) {
			t.Fatalf("recall dropped %q; there is no selection any more:\n%s", want, served)
		}
	}
	// The inline turns are already in the opening; sending them twice
	// would just be another copy in a transcript that is resent.
	if strings.Contains(served, "shrink the header") {
		t.Fatalf("recall resent a turn that was already carried inline:\n%s", served)
	}
	if runner.profile.Recall.Served != 3 {
		t.Fatalf("Recall.Served = %d, want 3", runner.profile.Recall.Served)
	}

	if again := runner.recallEarlier(); strings.Contains(again, "make it multilingual") {
		t.Fatalf("recall resent history it had already served:\n%s", again)
	}
}

func TestRecallWithNothingBehindTheInlineTurnsSaysSo(t *testing.T) {
	runner := NewRunner(&Client{}, &repo.Repository{Root: t.TempDir()})
	runner.earlier = pastTurns // exactly the inline window, nothing behind it
	if got := runner.recallEarlier(); !strings.Contains(got, "Nothing came before") {
		t.Fatalf("recall with nothing older = %q", got)
	}
}

func TestRunServesOlderTurnsWhenTheModelAsksForThem(t *testing.T) {
	server, bodies := recordingServer(t, func(writer http.ResponseWriter, round int) {
		if round == 1 {
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"function_call","name":"recall","call_id":"c1","arguments":"{\"reason\":\"the message reaches back past what is shown\"}"}]}`)
			return
		}
		answerOnce(writer, "picked up where we left off")
	})
	defer server.Close()

	runner := NewRunner(&Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()},
		&repo.Repository{Root: t.TempDir()})
	runner.UseRecall(true)
	progress := &recordingProgress{}
	outcome, err := runner.Run(context.Background(), "go back to the original palette", nil, manyTurns, progress)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal((*bodies)[1]["input"])
	if !strings.Contains(string(encoded), "make it multilingual") {
		t.Fatalf("the older turns never reached the model:\n%s", encoded)
	}
	if outcome.Profile.Recall.Available != 3 || outcome.Profile.Recall.Served != 3 {
		t.Fatalf("Recall = %+v, want 3 available and 3 served", outcome.Profile.Recall)
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "reaches back past") {
		t.Fatalf("the trail should say why it reached back:\n%s", trail)
	}
}

func TestATurnThatNeverRecallsRecordsThatItDidNot(t *testing.T) {
	// The measurement: older turns were there to ask for, and this turn
	// did not need them. Served staying zero is the answer, not a gap.
	_, outcome := runOnce(t, manyTurns, true, "what is 2 + 2")
	if outcome.Profile.Recall.Available != 3 {
		t.Fatalf("Recall.Available = %d, want 3", outcome.Profile.Recall.Available)
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

// longHistory is more turns than either window will take.
func longHistory(count int) []Recollection {
	var turns []Recollection
	for index := 1; index <= count; index++ {
		turns = append(turns, Recollection{
			Number:  index,
			Message: fmt.Sprintf("ask number %d", index),
			Summary: fmt.Sprintf("did number %d", index),
			Files:   []string{"App.tsx"},
		})
	}
	return turns
}

func TestRecallServesAWindowRatherThanEverything(t *testing.T) {
	runner := NewRunner(&Client{}, &repo.Repository{Root: t.TempDir()})
	runner.earlier = longHistory(20)

	served := runner.recallEarlier()
	// Turns 18-20 are carried inline, so recall covers the ten below
	// them: 8 through 17.
	for _, want := range []int{8, 17} {
		if !strings.Contains(served, fmt.Sprintf("ask number %d", want)) {
			t.Fatalf("turn %d should be within reach:\n%s", want, served)
		}
	}
	if strings.Contains(served, "ask number 7") {
		t.Fatalf("recall reached further back than its window:\n%s", served)
	}
	if strings.Contains(served, "ask number 18") {
		t.Fatalf("recall resent a turn already carried inline:\n%s", served)
	}
	if runner.profile.Recall.Served != recallTurns {
		t.Fatalf("Recall.Served = %d, want %d", runner.profile.Recall.Served, recallTurns)
	}
}

func TestAvailableCountsWhatRecallWouldActuallyGive(t *testing.T) {
	// Not every turn on record: the offer in the opening message and the
	// number in the session log both have to mean the same thing.
	_, outcome := runOnce(t, longHistory(20), true, "keep going")
	if outcome.Profile.Recall.Available != recallTurns {
		t.Fatalf("Recall.Available = %d, want the %d recall would serve", outcome.Profile.Recall.Available, recallTurns)
	}
}

func TestHistoryIsJustWhatWasAskedAndWhatCameOfIt(t *testing.T) {
	rendered := renderHistory([]Recollection{
		{Number: 4, Message: "add portuguese", Summary: "Added pt", Files: []string{"App.tsx", "index.css"}},
	})
	if !strings.Contains(rendered, "add portuguese") || !strings.Contains(rendered, "Added pt") {
		t.Fatalf("a turn is the request and the answer:\n%s", rendered)
	}
	// The files a turn touched are a fact about the repository, and the
	// map and the files themselves say it better than a stale note would.
	if strings.Contains(rendered, "App.tsx") {
		t.Fatalf("history should carry no file list:\n%s", rendered)
	}
}
