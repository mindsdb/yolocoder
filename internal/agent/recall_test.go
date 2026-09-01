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
	"github.com/mindsdb/yolocoder/internal/shell"
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
		fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"action\":\"chat\",\"reply\":\"ok\",\"command\":\"\",\"relevant\":[],\"context\":\"\"}"}]}]}`)
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
		fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"action\":\"chat\",\"reply\":\"ok\",\"command\":\"\",\"relevant\":[],\"context\":\"\"}"}]}]}`)
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
			fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"action\":\"code\",\"reply\":\"\",\"command\":\"\",\"relevant\":[3],\"context\":\"Continuing the layout work.\"}"}]}]}`)
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
		fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"action\":\"chat\",\"reply\":\"ok\",\"command\":\"\",\"relevant\":[],\"context\":\"\"}"}]}]}`)
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

// fakeCommander records what it was asked to run.
type fakeCommander struct {
	scripts []string
	err     error
}

func (commander *fakeCommander) Run(_ context.Context, script string, _ shell.Supervisor) error {
	commander.scripts = append(commander.scripts, script)
	return commander.err
}

// routeAs replies to the router with one decision and fails any other call.
func routeAs(t *testing.T, decision string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		if name := schemaName(body); name != "message_route" {
			t.Errorf("unexpected %q call: the command route must not reach the change loop", name)
		}
		writer.Header().Set("Content-Type", "application/json")
		writeOutputText(writer, decision)
	}))
}

func TestCommandRouteRunsTheScriptWithoutMappingTheFolder(t *testing.T) {
	server := routeAs(t, `{"action":"command","reply":"Checking the node version.","command":"node --version"}`)
	defer server.Close()
	commander := &fakeCommander{}
	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	runner := NewRunner(client, &repo.Repository{Root: t.TempDir()}).Commands(commander)

	outcome, err := runner.Run(context.Background(), "what node am I on?", nil, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if len(commander.scripts) != 1 || commander.scripts[0] != "node --version" {
		t.Fatalf("scripts = %q", commander.scripts)
	}
	if outcome.Kind != KindCommand || !outcome.Applied {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Command != "node --version" {
		t.Fatalf("outcome did not carry the script: %+v", outcome)
	}
}

func TestDeclinedCommandIsNotAFailure(t *testing.T) {
	server := routeAs(t, `{"action":"command","reply":"","command":"rm -rf /tmp/whatever"}`)
	defer server.Close()
	commander := &fakeCommander{err: ErrCommandDeclined}
	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	runner := NewRunner(client, &repo.Repository{Root: t.TempDir()}).Commands(commander)

	outcome, err := runner.Run(context.Background(), "clean that up", nil, &recordingProgress{})
	if err != nil {
		t.Fatalf("saying no is not an error: %v", err)
	}
	if outcome.Applied {
		t.Fatalf("outcome = %+v, want it not marked applied", outcome)
	}
	// Still recorded, so "what was that command?" has an answer.
	if outcome.Command == "" {
		t.Fatalf("outcome = %+v, want the declined script kept", outcome)
	}
}

func TestCommandRouteDescribesItselfWithNoCommander(t *testing.T) {
	// Nothing executes because a caller forgot to say who approves it.
	server := routeAs(t, `{"action":"command","reply":"Installing it.","command":"npm install"}`)
	defer server.Close()
	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}

	outcome, err := NewRunner(client, &repo.Repository{Root: t.TempDir()}).
		Run(context.Background(), "install it", nil, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Applied {
		t.Fatal("nothing should run without a commander")
	}
	if !strings.Contains(outcome.Reply, "npm install") {
		t.Fatalf("reply = %q, want it to say what it would have run", outcome.Reply)
	}
}

func TestAnEmptyCommandFallsBackToChat(t *testing.T) {
	// "command" with nothing to run is not a command route; replying is
	// better than reporting an empty script the user has to make sense of.
	server := routeAs(t, `{"action":"command","reply":"I need more to go on.","command":""}`)
	defer server.Close()
	commander := &fakeCommander{}
	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	runner := NewRunner(client, &repo.Repository{Root: t.TempDir()}).Commands(commander)

	outcome, err := runner.Run(context.Background(), "run it", nil, &recordingProgress{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != KindChat || len(commander.scripts) != 0 {
		t.Fatalf("outcome = %+v, scripts = %q", outcome, commander.scripts)
	}
}

func TestUnknownActionsAreReadAsChat(t *testing.T) {
	// Chat is the only route that cannot touch anything, so it is where an
	// unrecognised answer belongs.
	for action, want := range map[string]string{
		"code": KindCode, "coding": KindCode, "CODE": KindCode,
		"command": KindCommand, "shell": KindCommand, "cmd": KindCommand,
		"chat": KindChat, "": KindChat, "banana": KindChat, "delete_everything": KindChat,
	} {
		decision := routeDecision{Action: action, Command: "true"}
		if got := decision.route(); got != want {
			t.Fatalf("action %q routed to %q, want %q", action, got, want)
		}
	}
}
