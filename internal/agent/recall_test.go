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

// content reads one input message's text back out of a decoded request.
func content(t *testing.T, part any) string {
	t.Helper()
	message, ok := part.(map[string]any)
	if !ok {
		t.Fatalf("input part = %#v, want a message object", part)
	}
	text, ok := message["content"].(string)
	if !ok {
		t.Fatalf("message = %#v, want string content", message)
	}
	return text
}

var pastTurns = []Recollection{
	{Number: 1, Message: "make it multilingual", Summary: "Added i18n", Files: []string{"translations.js"}},
	{Number: 2, Message: "hi", Summary: "Hello!"},
	{Number: 3, Message: "fit the side cards too", Summary: "Compacted the cards", Files: []string{"base_index.html"}},
}

func TestRouteAsksForRelevanceOnlyWhenThereIsHistory(t *testing.T) {
	var schemas []string
	var inputs []any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		schemas = append(schemas, schemaName(body))
		inputs = append(inputs, body["input"])
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"coding_task\":false,\"reply\":\"ok\",\"relevant\":[],\"context\":\"\"}"}]}]}`)
	}))
	defer server.Close()
	runner := NewRunner(&Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}, &repo.Repository{Root: t.TempDir()})

	// With no history the call stays exactly as cheap as it was: the
	// message goes as a bare string and nothing asks for selection.
	if _, err := runner.route(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if text, ok := inputs[0].(string); !ok || text != "hi" {
		t.Fatalf("input = %#v, want the bare message", inputs[0])
	}

	// With history it asks for the selection too.
	if _, err := runner.route(context.Background(), "and now?", pastTurns); err != nil {
		t.Fatal(err)
	}
	parts, ok := inputs[1].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("input = %#v, want history then message", inputs[1])
	}
}

func TestRouteputsHistoryBeforeTheMessageForTheCache(t *testing.T) {
	// Prefix caching only reuses an unchanged prefix, so the part that
	// grows must come before the part that changes every turn. If the new
	// message came first, no two turns would ever share a prefix.
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		encoded, _ := json.Marshal(body["input"])
		bodies = append(bodies, string(encoded))
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"coding_task\":false,\"reply\":\"ok\",\"relevant\":[],\"context\":\"\"}"}]}]}`)
	}))
	defer server.Close()
	runner := NewRunner(&Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}, &repo.Repository{Root: t.TempDir()})

	if _, err := runner.route(context.Background(), "first", pastTurns[:2]); err != nil {
		t.Fatal(err)
	}
	// One more turn recorded, and a different message.
	if _, err := runner.route(context.Background(), "second", pastTurns); err != nil {
		t.Fatal(err)
	}

	shared := 0
	for shared < len(bodies[0]) && shared < len(bodies[1]) && bodies[0][shared] == bodies[1][shared] {
		shared++
	}
	// The two requests must agree well past the first turn's text; if the
	// message were first they would diverge almost immediately.
	if !strings.Contains(bodies[0][:shared], "make it multilingual") {
		t.Fatalf("only %d bytes shared, too early to have kept the history prefix:\n%s", shared, bodies[0][:shared])
	}
	if strings.Contains(bodies[0][:shared], "first") {
		t.Fatal("the volatile message must not be inside the shared prefix")
	}
}

func TestRecallKeepsTheRecordedTextForChosenTurns(t *testing.T) {
	kept := recall(pastTurns, []int{3, 1})
	if len(kept) != 2 {
		t.Fatalf("kept = %+v", kept)
	}
	// Returned in recorded order, with the real text, whatever order the
	// model listed them in.
	if kept[0].Number != 1 || kept[1].Number != 3 {
		t.Fatalf("kept = %+v, want turns 1 and 3 in order", kept)
	}
	if kept[1].Summary != "Compacted the cards" {
		t.Fatalf("kept the wrong text: %+v", kept[1])
	}
	// A number that was never recorded cannot conjure a turn.
	if invented := recall(pastTurns, []int{99}); len(invented) != 0 {
		t.Fatalf("recall invented %+v", invented)
	}
	if none := recall(pastTurns, nil); len(none) != 0 {
		t.Fatalf("recall(nil) = %+v", none)
	}
}

func TestBackgroundCarriesOnlyWhatWasChosen(t *testing.T) {
	text := background("Continuing the layout work.", recall(pastTurns, []int{3}))
	if !strings.Contains(text, "Continuing the layout work.") {
		t.Fatalf("background lost the brief:\n%s", text)
	}
	if !strings.Contains(text, "fit the side cards too") {
		t.Fatalf("background lost the chosen turn:\n%s", text)
	}
	if strings.Contains(text, "make it multilingual") {
		t.Fatalf("background carried a turn that was not chosen:\n%s", text)
	}
	if background("", nil) != "" {
		t.Fatal("nothing chosen should carry nothing")
	}
}

func TestRunGivesTheWorkOnlyTheChosenHistory(t *testing.T) {
	root := t.TempDir()
	repository := &repo.Repository{Root: root}
	var changeInput string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.Header().Set("Content-Type", "application/json")
		switch schemaName(body) {
		case "message_route":
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"coding_task\":true,\"reply\":\"\",\"relevant\":[3],\"context\":\"Continuing the layout work.\"}"}]}]}`)
		case "code_change":
			encoded, _ := json.Marshal(body["input"])
			changeInput = string(encoded)
			payload, _ := json.Marshal(Change{Summary: "done", Diff: "*** Begin Patch\n*** Add File: note.txt\n+hi\n*** End Patch"})
			writeOutputText(writer, string(payload))
		}
	}))
	defer server.Close()

	progress := &recordingProgress{}
	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	if _, err := NewRunner(client, repository).Run(context.Background(), "keep going", pastTurns, progress); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(changeInput, "Continuing the layout work.") {
		t.Fatalf("the work was not told the brief:\n%s", changeInput)
	}
	if !strings.Contains(changeInput, "fit the side cards too") {
		t.Fatalf("the work was not given the chosen turn:\n%s", changeInput)
	}
	if strings.Contains(changeInput, "make it multilingual") {
		t.Fatalf("the work was given an unchosen turn:\n%s", changeInput)
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "recalled 1 earlier turn") {
		t.Fatalf("the trail should say what was recalled:\n%s", trail)
	}
}

func TestNumberListTolerance(t *testing.T) {
	for _, payload := range []string{`{"relevant":[3,7]}`, `{"relevant":["3","7"]}`} {
		var decision routeDecision
		if err := json.Unmarshal([]byte(payload), &decision); err != nil {
			t.Fatalf("%s: %v", payload, err)
		}
		if len(decision.Relevant) != 2 || decision.Relevant[0] != 3 || decision.Relevant[1] != 7 {
			t.Fatalf("%s gave %v", payload, decision.Relevant)
		}
	}
	var single routeDecision
	if err := json.Unmarshal([]byte(`{"relevant":3}`), &single); err != nil {
		t.Fatal(err)
	}
	if len(single.Relevant) != 1 || single.Relevant[0] != 3 {
		t.Fatalf("relevant = %v", single.Relevant)
	}
}

func TestNotesGoAheadOfHistoryInTheRequest(t *testing.T) {
	var inputs []any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		inputs = append(inputs, body["input"])
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"coding_task\":false,\"reply\":\"ok\",\"relevant\":[],\"context\":\"\"}"}]}]}`)
	}))
	defer server.Close()
	runner := NewRunner(&Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}, &repo.Repository{Root: t.TempDir()})

	supplied := append([]Recollection{{Message: "this is a Django project", Note: true}}, pastTurns...)
	if _, err := runner.route(context.Background(), "and now?", supplied); err != nil {
		t.Fatal(err)
	}
	parts, ok := inputs[0].([]any)
	if !ok || len(parts) != 3 {
		t.Fatalf("input = %#v, want notes, history, then message", inputs[0])
	}
	// Notes are fixed for the whole invocation and history grows every
	// turn, so the notes must come first for the prefix cache to hold.
	first := content(t, parts[0])
	if !strings.HasPrefix(first, "PROJECT CONTEXT:") || !strings.Contains(first, "Django") {
		t.Fatalf("first block = %q, want the supplied context", first)
	}
	if second := content(t, parts[1]); !strings.HasPrefix(second, "EARLIER IN THIS FOLDER:") {
		t.Fatalf("second block = %q, want the history", second)
	}
	// Unnumbered, so a note can never be confused with a recorded turn.
	if strings.Contains(first, "1.") {
		t.Fatalf("notes should not be numbered: %q", first)
	}
}

func TestSuppliedNotesSurviveARouterThatSelectsNothing(t *testing.T) {
	// The caller passed these in deliberately; dropping them is not a
	// judgement the router gets to make.
	supplied := append([]Recollection{{Message: "the app is called Foo", Note: true}}, pastTurns...)
	kept := recall(supplied, nil)
	if len(kept) != 1 || !kept[0].Note {
		t.Fatalf("recall kept %+v, want just the note", kept)
	}
	kept = recall(supplied, []int{3})
	if len(kept) != 2 || !kept[0].Note || kept[1].Number != 3 {
		t.Fatalf("recall kept %+v, want the note and turn 3", kept)
	}
}

func TestBackgroundSeparatesSuppliedNotesFromRecalledTurns(t *testing.T) {
	text := background("", []Recollection{{Message: "uses Postgres", Note: true}})
	if !strings.Contains(text, "PROJECT CONTEXT:") || strings.Contains(text, "EARLIER IN THIS FOLDER:") {
		t.Fatalf("background = %q, want only the context block", text)
	}
	text = background("carries on from turn 1", []Recollection{
		{Message: "uses Postgres", Note: true},
		{Number: 1, Message: "add a login page", Summary: "Added it"},
	})
	notes := strings.Index(text, "PROJECT CONTEXT:")
	history := strings.Index(text, "EARLIER IN THIS FOLDER:")
	if notes < 0 || history < 0 || notes > history {
		t.Fatalf("background = %q, want the context block first", text)
	}
	if !strings.Contains(text, "1. asked: add a login page") {
		t.Fatalf("background = %q, want the numbered turn", text)
	}
}

func TestBackgroundIsEmptyWithNothingToSay(t *testing.T) {
	if text := background("", nil); text != "" {
		t.Fatalf("background = %q, want empty", text)
	}
}
