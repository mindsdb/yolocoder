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

// edits is a model reply that calls apply_diff with a compact patch.
func edits(id, patch string) string {
	arguments, _ := json.Marshal(map[string]string{"patch": patch})
	return calls("apply_diff", id, string(arguments))
}

// reads is a model reply that calls read_files.
func reads(id string, paths ...string) string {
	arguments, _ := json.Marshal(map[string][]string{"paths": paths})
	return calls("read_files", id, string(arguments))
}

// finishes is a model reply with no tool call, which ends the turn.
func finishes(text string) string {
	return fmt.Sprintf(`{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":%s}]}]}`,
		strconv.Quote(text))
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
