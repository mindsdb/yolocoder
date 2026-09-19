package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

// calls is a model reply that calls one tool.
func calls(name, id, arguments string) string {
	return fmt.Sprintf(`{"id":"r","output":[{"type":"function_call","name":%s,"call_id":%s,"arguments":%s}]}`,
		strconv.Quote(name), strconv.Quote(id), strconv.Quote(arguments))
}

// edits is a model reply that calls apply_diff with a compact patch and
// no closing note, so the turn carries on.
func edits(id, patch string) string {
	arguments, _ := json.Marshal(map[string]string{"patch": patch, "response_comment_for_user": ""})
	return calls("apply_diff", id, string(arguments))
}

// editsAndFinishes is the same call carrying the model's closing note,
// which ends the turn if the edit lands and the check passes.
func editsAndFinishes(id, patch, comment string) string {
	arguments, _ := json.Marshal(map[string]string{"patch": patch, "response_comment_for_user": comment})
	return calls("apply_diff", id, string(arguments))
}

// reads is a model reply that calls read_files.
func reads(id string, paths ...string) string {
	arguments, _ := json.Marshal(map[string][]string{"paths": paths})
	return calls("read_files", id, string(arguments))
}

// saysAndCalls is a reply carrying the model's own words alongside a
// tool call, which is what a provider actually returns most rounds.
func saysAndCalls(aside, name, id, arguments string) string {
	return fmt.Sprintf(`{"id":"r","output":[`+
		`{"type":"message","content":[{"type":"output_text","text":%s}]},`+
		`{"type":"function_call","name":%s,"call_id":%s,"arguments":%s}]}`,
		strconv.Quote(aside), strconv.Quote(name), strconv.Quote(id), strconv.Quote(arguments))
}

// finishes is a model reply with no tool call, which ends the turn.
func finishes(text string) string {
	return fmt.Sprintf(`{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":%s}]}]}`,
		strconv.Quote(text))
}

// cutOff is a reply the provider stopped before finishing: status
// incomplete, reason max_output_tokens, and no usable text — the shape a
// real trace showed ending the turn with "no output text".
func cutOff() string {
	return `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":""}]}]}`
}

// scripted answers each request in turn with the replies given, and
// records every request body it saw.
func scripted(t *testing.T, replies ...string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var seen []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		seen = append(seen, body)
		writer.Header().Set("Content-Type", "application/json")
		if len(seen) > len(replies) {
			t.Errorf("unexpected request %d", len(seen))
			fmt.Fprint(writer, finishes("(unscripted)"))
			return
		}
		fmt.Fprint(writer, replies[len(seen)-1])
	}))
	return server, &seen
}

func folder(t *testing.T, files map[string]string) *repo.Repository {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &repo.Repository{Root: root}
}

func run(t *testing.T, repository *repo.Repository, server *httptest.Server, task string) (Outcome, *recordingProgress, error) {
	t.Helper()
	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	progress := &recordingProgress{}
	outcome, err := NewRunner(client, repository).Run(context.Background(), task, nil, nil, progress)
	return outcome, progress, err
}

func TestReadEditFinishInOneConversation(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "const x = 1;\nconst y = 2;\n"})
	server, seen := scripted(t,
		reads("c1", "a.ts"),
		edits("c2", "@a.ts\n-const x = 1;\n+const x = 42;\n"),
		finishes("Bumped `x` to 42."),
	)
	defer server.Close()

	outcome, progress, err := run(t, repository, server, "make x 42")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(repository.Root, "a.ts"))
	if !strings.Contains(string(content), "const x = 42;") {
		t.Fatalf("the edit did not land: %q", content)
	}
	if outcome.Reply != "Bumped `x` to 42." {
		t.Fatalf("reply = %q — the plain message is the reply, markdown and all", outcome.Reply)
	}
	if !outcome.Applied || !outcome.Coding {
		t.Fatalf("outcome = %+v, want an applied coding turn", outcome)
	}
	if len(*seen) != 3 {
		t.Fatalf("requests = %d, want read, edit, finish", len(*seen))
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "edit a.ts") {
		t.Fatalf("the trail should name what was edited:\n%s", trail)
	}
}

func TestNoSchemaIsSentAlongsideTools(t *testing.T) {
	// Asking for tools and a response_format in the same request is what
	// produced both provider workarounds in client.go. The loop finishes
	// on a plain message instead, so neither is needed.
	repository := folder(t, map[string]string{"a.ts": "x\n"})
	server, seen := scripted(t, finishes("Hi."))
	defer server.Close()

	if _, _, err := run(t, repository, server, "hi"); err != nil {
		t.Fatal(err)
	}
	body := (*seen)[0]
	if _, present := body["text"]; present {
		t.Fatal("no JSON schema should be sent with tools")
	}
	if _, present := body["response_format"]; present {
		t.Fatal("no response_format should be sent with tools")
	}
	tools, _ := body["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("the tools themselves should still be offered")
	}
}

func TestAConversationalMessageCostsOneCallAndChangesNothing(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "x\n"})
	server, seen := scripted(t, finishes("You're welcome!"))
	defer server.Close()

	outcome, _, err := run(t, repository, server, "thanks!")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Reply != "You're welcome!" || outcome.Coding || outcome.Applied {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(*seen) != 1 {
		t.Fatalf("requests = %d, want 1", len(*seen))
	}
}

func TestARejectedEditComesBackAsAToolResult(t *testing.T) {
	// The repair happens inside the conversation rather than by starting
	// a new attempt: the failure is just what that tool returned.
	repository := folder(t, map[string]string{"a.ts": "const board = useState(empty());\n"})
	server, seen := scripted(t,
		edits("c1", "@a.ts\n-const board = useState(() => empty());\n+const board = useState(fresh());\n"),
		edits("c2", "@a.ts\n-const board = useState(empty());\n+const board = useState(fresh());\n"),
		finishes("Fixed."),
	)
	defer server.Close()

	outcome, progress, err := run(t, repository, server, "use fresh()")
	if err != nil {
		t.Fatal(err)
	}
	// The second request carries the first attempt's failure, with the
	// line that differs, as an ordinary tool result.
	// Compared against the decoded text: json.Marshal escapes ">" as
	// \u003e, which is not what the model is handed.
	encoded, _ := json.Marshal((*seen)[1]["input"])
	var decoded []any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	conversation := fmt.Sprint(decoded...)
	for _, want := range []string{"did not apply", "() => empty()", "useState(empty())"} {
		if !strings.Contains(conversation, want) {
			t.Fatalf("the failure detail %q never reached the model:\n%s", want, conversation)
		}
	}
	// It never read the file, so the edit's result carries it — which is
	// the round trip this saves: no second call just to ask for it.
	if !strings.Contains(conversation, "THESE FILES AS THEY NOW STAND") {
		t.Fatalf("a failed edit should hand back the file it could not place against:\n%s", conversation)
	}
	if !outcome.Applied {
		t.Fatalf("the second edit should have landed: %+v", outcome)
	}
	if outcome.Attempts != 2 {
		t.Fatalf("Attempts = %d, want both apply_diff calls counted", outcome.Attempts)
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "could not find") {
		t.Fatalf("the trail should say why the edit was rejected:\n%s", trail)
	}
}

func TestAFailingCheckSendsTheModelBackToWork(t *testing.T) {
	// A model cannot declare victory over a build it just broke: the
	// check runs when it tries to finish, and a failure continues the
	// same conversation.
	repository := folder(t, map[string]string{
		"go.mod":       "module demo\n\ngo 1.24\n",
		"demo.go":      "package demo\n\nfunc Add(a, b int) int { return a - b }\n",
		"demo_test.go": "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n",
	})
	server, seen := scripted(t,
		edits("c1", "@demo.go\n-func Add(a, b int) int { return a - b }\n+func Add(a, b int) int { return a * b }\n"),
		finishes("Done."),
		edits("c2", "@demo.go\n-func Add(a, b int) int { return a * b }\n+func Add(a, b int) int { return a + b }\n"),
		finishes("Fixed the operator."),
	)
	defer server.Close()

	outcome, progress, err := run(t, repository, server, "fix Add")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Reply != "Fixed the operator." {
		t.Fatalf("reply = %q, want the one given after the check passed", outcome.Reply)
	}
	if len(*seen) != 4 {
		t.Fatalf("requests = %d, want the first finish to be refused", len(*seen))
	}
	encoded, _ := json.Marshal((*seen)[2]["input"])
	if !strings.Contains(string(encoded), "check failed") && !strings.Contains(string(encoded), "FAIL") {
		t.Fatalf("the check failure should reach the model:\n%s", encoded)
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "check failed, back to it") {
		t.Fatalf("the trail should show the refusal:\n%s", trail)
	}
}

func TestTheOutcomeReportsWhatLandedNotWhatWasClaimed(t *testing.T) {
	// Files used to be the model's own claim, which had to be audited
	// afterwards. Here it is simply the edits that were accepted.
	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n", "b.ts": "const y = 2;\n"})
	server, _ := scripted(t,
		edits("c1", "@a.ts\n-const x = 1;\n+const x = 9;\n"),
		finishes("Updated a.ts and b.ts."),
	)
	defer server.Close()

	outcome, _, err := run(t, repository, server, "update both")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Files) != 1 || outcome.Files[0] != "a.ts" {
		t.Fatalf("Files = %v, want only the file that was actually edited", outcome.Files)
	}
	content, _ := os.ReadFile(filepath.Join(repository.Root, "b.ts"))
	if string(content) != "const y = 2;\n" {
		t.Fatalf("b.ts should be untouched: %q", content)
	}
}

func TestNothingLandsWhenOneEditInAPatchCannotBePlaced(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n", "b.ts": "const y = 2;\n"})
	server, _ := scripted(t,
		edits("c1", "@a.ts\n-const x = 1;\n+const x = 9;\n\n@b.ts\n-const y = 99;\n+const y = 8;\n"),
		finishes("Could not do it."),
	)
	defer server.Close()

	outcome, _, err := run(t, repository, server, "update both")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Applied {
		t.Fatalf("outcome = %+v, want nothing applied", outcome)
	}
	content, _ := os.ReadFile(filepath.Join(repository.Root, "a.ts"))
	if string(content) != "const x = 1;\n" {
		t.Fatalf("the placeable half was written anyway: %q", content)
	}
}

func TestEachToolIsHeldToItsOwnBudget(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "x\n"})
	quota := toolQuota["read_files"]
	var replies []string
	for index := 0; index <= quota; index++ {
		replies = append(replies, reads(fmt.Sprintf("c%d", index), "a.ts"))
	}
	replies = append(replies, finishes("Out of reads."))
	server, seen := scripted(t, replies...)
	defer server.Close()

	if _, _, err := run(t, repository, server, "read it a lot"); err != nil {
		t.Fatal(err)
	}
	// The call past the limit is answered with the limit, not an error
	// that ends the turn — the model can still finish with what it has.
	last := (*seen)[len(*seen)-1]
	encoded, _ := json.Marshal(last["input"])
	if !strings.Contains(string(encoded), "which is the limit for one turn") {
		t.Fatalf("the model should be told it ran out:\n%s", encoded)
	}
}

func TestEditsThatNeverLandFallBackToWholeFiles(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "const real = 1;\n"})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.Header().Set("Content-Type", "application/json")
		if schemaName(body) == "file_rewrite" {
			payload, _ := json.Marshal(Rewrite{Summary: "Rewrote a.ts", Content: "const real = 2;\n"})
			fmt.Fprint(writer, finishes(string(payload)))
			return
		}
		// Every edit names a line that is not in the file.
		fmt.Fprint(writer, edits("c", "@a.ts\n-const imaginary = 1;\n+const imaginary = 2;\n"))
	}))
	defer server.Close()

	outcome, progress, err := run(t, repository, server, "change it")
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Rewrote || !outcome.Applied {
		t.Fatalf("outcome = %+v, want the whole-file fallback", outcome)
	}
	content, _ := os.ReadFile(filepath.Join(repository.Root, "a.ts"))
	if string(content) != "const real = 2;\n" {
		t.Fatalf("a.ts = %q", content)
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "wrote a.ts") {
		t.Fatalf("the trail should say the file was written whole:\n%s", trail)
	}
}

func TestRewriteRefusesToEmptyAFileThatHasContent(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "const real = 1;\n"})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.Header().Set("Content-Type", "application/json")
		if schemaName(body) == "file_rewrite" {
			payload, _ := json.Marshal(Rewrite{Summary: "Emptied it", Content: ""})
			fmt.Fprint(writer, finishes(string(payload)))
			return
		}
		fmt.Fprint(writer, edits("c", "@a.ts\n-const imaginary = 1;\n+const imaginary = 2;\n"))
	}))
	defer server.Close()

	_, _, err := run(t, repository, server, "change it")
	if err == nil || !strings.Contains(err.Error(), "a.ts") {
		t.Fatalf("err = %v, want a refusal naming the file", err)
	}
	content, _ := os.ReadFile(filepath.Join(repository.Root, "a.ts"))
	if string(content) != "const real = 1;\n" {
		t.Fatalf("the file should be untouched: %q", content)
	}
}

func TestUsageAndProfileCoverEveryCallInTheTurn(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n"})
	withUsage := func(reply string) string {
		return strings.TrimSuffix(reply, "}") +
			`,"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110,"input_tokens_details":{"cached_tokens":20}}}`
	}
	server, _ := scripted(t,
		withUsage(reads("c1", "a.ts")),
		withUsage(edits("c2", "@a.ts\n-const x = 1;\n+const x = 2;\n")),
		withUsage(finishes("Done.")),
	)
	defer server.Close()

	outcome, _, err := run(t, repository, server, "bump x")
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{InputTokens: 300, CachedTokens: 60, OutputTokens: 30, TotalTokens: 330}
	if outcome.Usage != want {
		t.Fatalf("Usage = %+v, want every call summed: %+v", outcome.Usage, want)
	}
	steps := map[Step]bool{}
	for _, span := range outcome.Profile.Spans() {
		steps[span.Step] = true
	}
	for _, step := range []Step{StepMap, StepThink, StepTools, StepPatch} {
		if !steps[step] {
			t.Errorf("%s was not measured; profile = %v", step, outcome.Profile.Spans())
		}
	}
}

func TestASuccessfulEditSaysNotToReadItBack(t *testing.T) {
	// On the first real run the model applied an edit, was told only
	// "Applied", and spent three further round trips reading the files
	// back to see whether it had worked.
	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n"})
	server, seen := scripted(t,
		edits("c1", "@a.ts\n-const x = 1;\n+const x = 2;\n"),
		finishes("Done."),
	)
	defer server.Close()

	if _, _, err := run(t, repository, server, "bump x"); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal((*seen)[1]["input"])
	result := string(encoded)
	if !strings.Contains(result, "Do not read them again to check") {
		t.Fatalf("a successful edit should say it is authoritative:\n%s", result)
	}
	if !strings.Contains(result, "a.ts") {
		t.Fatalf("it should still say what changed:\n%s", result)
	}
}

func TestAnEditCarryingItsOwnClosingNoteEndsTheTurn(t *testing.T) {
	// The round trip this removes existed only to hear "done": the whole
	// transcript resent to generate forty words.
	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n"})
	server, seen := scripted(t,
		editsAndFinishes("c1", "@a.ts\n-const x = 1;\n+const x = 2;\n", "Bumped `x` to 42."),
	)
	defer server.Close()

	outcome, _, err := run(t, repository, server, "bump x")
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 {
		t.Fatalf("requests = %d, want the edit to have been the whole turn", len(*seen))
	}
	if outcome.Reply != "Bumped `x` to 42." {
		t.Fatalf("reply = %q, want the note written with the edit", outcome.Reply)
	}
	if !outcome.Applied || !outcome.Coding {
		t.Fatalf("outcome = %+v", outcome)
	}
	content, _ := os.ReadFile(filepath.Join(repository.Root, "a.ts"))
	if string(content) != "const x = 2;\n" {
		t.Fatalf("a.ts = %q", content)
	}
}

func TestAnEmptyNoteLetsTheTurnCarryOn(t *testing.T) {
	// Only a filled-in note means "finished". An edit with none is just
	// an edit, and the model goes on working.
	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n", "b.ts": "const y = 2;\n"})
	server, seen := scripted(t,
		edits("c1", "@a.ts\n-const x = 1;\n+const x = 9;\n"),
		editsAndFinishes("c2", "@b.ts\n-const y = 2;\n+const y = 8;\n", "Changed both."),
	)
	defer server.Close()

	outcome, _, err := run(t, repository, server, "change both")
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 {
		t.Fatalf("requests = %d, want the first edit not to have ended the turn", len(*seen))
	}
	if outcome.Reply != "Changed both." {
		t.Fatalf("reply = %q", outcome.Reply)
	}
	if len(outcome.Files) != 2 {
		t.Fatalf("Files = %v, want both", outcome.Files)
	}
}

func TestAFailedEditsNoteDoesNotEndTheTurn(t *testing.T) {
	// A note on a patch that could not be placed would end the turn on a
	// promise. It is recorded only once the edit has actually landed.
	repository := folder(t, map[string]string{"a.ts": "const real = 1;\n"})
	server, seen := scripted(t,
		editsAndFinishes("c1", "@a.ts\n-const imaginary = 1;\n+const imaginary = 2;\n", "All done!"),
		finishes("Actually, I could not place that edit."),
	)
	defer server.Close()

	outcome, _, err := run(t, repository, server, "change it")
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 {
		t.Fatalf("requests = %d, want the failed edit not to have ended the turn", len(*seen))
	}
	if outcome.Reply == "All done!" {
		t.Fatal("a note on a failed edit must not become the reply")
	}
	if outcome.Applied {
		t.Fatalf("outcome = %+v, want nothing applied", outcome)
	}
}

func TestAClosingNoteStillWaitsForTheCheck(t *testing.T) {
	// Declaring victory slightly earlier is still declaring victory: the
	// check has the final say either way.
	repository := folder(t, map[string]string{
		"go.mod":       "module demo\n\ngo 1.24\n",
		"demo.go":      "package demo\n\nfunc Add(a, b int) int { return a - b }\n",
		"demo_test.go": "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n",
	})
	server, seen := scripted(t,
		editsAndFinishes("c1", "@demo.go\n-func Add(a, b int) int { return a - b }\n+func Add(a, b int) int { return a * b }\n", "Fixed it!"),
		editsAndFinishes("c2", "@demo.go\n-func Add(a, b int) int { return a * b }\n+func Add(a, b int) int { return a + b }\n", "Fixed it properly."),
	)
	defer server.Close()

	outcome, progress, err := run(t, repository, server, "fix Add")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Reply != "Fixed it properly." {
		t.Fatalf("reply = %q, want the note from the edit that actually passed", outcome.Reply)
	}
	if len(*seen) != 2 {
		t.Fatalf("requests = %d, want the first note refused by the check", len(*seen))
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "check failed, back to it") {
		t.Fatalf("the trail should show the refusal:\n%s", trail)
	}
}

func TestWhatTheModelSaysBesideAToolCallIsKept(t *testing.T) {
	// It was being dropped twice: never shown, and never put back into the
	// conversation, so on round three the model could not see the plan it
	// had written on round one.
	plan := "Adding Polish needs four changes: the Language type, a pl entry, the saved-preference check, and the option."
	readArgs, _ := json.Marshal(map[string][]string{"paths": {"a.ts"}})
	server, seen := scripted(t,
		saysAndCalls(plan, "read_files", "c1", string(readArgs)),
		finishes("Done."),
	)
	defer server.Close()

	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n"})
	_, progress, err := run(t, repository, server, "add polish")
	if err != nil {
		t.Fatal(err)
	}
	// It reaches the person watching.
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "Adding Polish needs four changes") {
		t.Fatalf("the model's own words should show on the trail:\n%s", trail)
	}
	// And it reaches the model again on the next round.
	encoded, _ := json.Marshal((*seen)[1]["input"])
	var decoded []any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	conversation := fmt.Sprint(decoded...)
	if !strings.Contains(conversation, "Adding Polish needs four changes") {
		t.Fatalf("the plan should still be in the conversation:\n%s", conversation)
	}
	if !strings.Contains(conversation, "assistant") {
		t.Fatalf("and carried as the model's own turn:\n%s", conversation)
	}
}

func TestAnEmptyAsideIsNotCarried(t *testing.T) {
	// Most rounds the message is just "\n\n". Carrying that would put an
	// empty assistant turn in the conversation every single round.
	readArgs, _ := json.Marshal(map[string][]string{"paths": {"a.ts"}})
	server, seen := scripted(t,
		saysAndCalls("\n\n", "read_files", "c1", string(readArgs)),
		finishes("Done."),
	)
	defer server.Close()

	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n"})
	if _, _, err := run(t, repository, server, "read it"); err != nil {
		t.Fatal(err)
	}
	parts, _ := (*seen)[1]["input"].([]any)
	for _, part := range parts {
		message, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if message["role"] == "assistant" && strings.TrimSpace(fmt.Sprint(message["content"])) == "" {
			t.Fatalf("an empty aside was carried: %#v", message)
		}
	}
}

func TestALongAsideIsCappedBeforeItIsCarried(t *testing.T) {
	long := strings.Repeat("thinking out loud at some length. ", 80)
	readArgs, _ := json.Marshal(map[string][]string{"paths": {"a.ts"}})
	server, seen := scripted(t,
		saysAndCalls(long, "read_files", "c1", string(readArgs)),
		finishes("Done."),
	)
	defer server.Close()

	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n"})
	_, progress, err := run(t, repository, server, "read it")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal((*seen)[1]["input"])
	if len(encoded) > len(long) {
		t.Fatalf("the whole ramble was carried: %d bytes of input", len(encoded))
	}
	for _, line := range progress.logs {
		if len(line) > 140 {
			t.Fatalf("a trail line ran to %d characters", len(line))
		}
	}
}

func TestACutOffReplyAsksAgainInsteadOfFailing(t *testing.T) {
	// A real trace showed this: a read answered fine, then an incomplete
	// reply with empty text ended the whole turn with "no output text".
	// A reply cut off by the output cap decided nothing, so the turn asks
	// again, shorter, in the same conversation.
	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n"})
	server, seen := scripted(t,
		reads("c1", "a.ts"),
		cutOff(),
		finishes("Done."),
	)
	defer server.Close()

	outcome, progress, err := run(t, repository, server, "read it")
	if err != nil {
		t.Fatalf("a cut-off reply should retry, not fail: %v", err)
	}
	if outcome.Reply != "Done." {
		t.Fatalf("reply = %q, want the finish after the retry", outcome.Reply)
	}
	if len(*seen) != 3 {
		t.Fatalf("requests = %d, want read, cut-off, finish", len(*seen))
	}
	encoded, _ := json.Marshal((*seen)[2]["input"])
	if !strings.Contains(string(encoded), "cut off") {
		t.Fatalf("the retry should say the reply was cut off:\n%s", encoded)
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "cut off, asking again") {
		t.Fatalf("the trail should show the retry:\n%s", trail)
	}
}
