package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mindsdb/yolocoder/internal/repo"
	"github.com/mindsdb/yolocoder/internal/shell"
)

const maxToolRounds = 8

// Change is one attempt at the whole job: what the model means to do,
// which files it touches, and the diff that does it.
//
// Planning and patching are deliberately one request. Splitting them cost
// a round trip and, worse, resent every file: the contents are already in
// the conversation from the tool calls, so a separate patch call shipped
// them a second time to learn nothing new. Summary and files come before
// the diff in the schema so the model still states its intent first.
type Change struct {
	Summary       string     `json:"summary"`
	FilesToModify stringList `json:"files_to_modify"`
	Diff          string     `json:"diff"`
}

// stringList is a list of strings that also accepts the shapes models
// reach for when a provider doesn't enforce the schema: a list of objects
// ([{"path":"index.html","changes":[...]}]) or a bare string. Refusing
// those outright cost us the whole plan over a wrapper the content was
// perfectly good inside of.
type stringList []string

func (list *stringList) UnmarshalJSON(data []byte) error {
	var plain []string
	if err := json.Unmarshal(data, &plain); err == nil {
		*list = plain
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*list = stringList{single}
		return nil
	}
	var objects []struct {
		Path        string `json:"path"`
		File        string `json:"file"`
		Name        string `json:"name"`
		Step        string `json:"step"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &objects); err != nil {
		return err
	}
	values := make(stringList, 0, len(objects))
	for _, object := range objects {
		if value := firstNonEmpty(object.Path, object.File, object.Name, object.Step, object.Description); value != "" {
			values = append(values, value)
		}
	}
	*list = values
	return nil
}

// Outcome is what a run amounted to: what to tell the user, whether it
// was a coding task at all, and what it touched. The caller needs more
// than the reply text so it can record the turn.
type Outcome struct {
	Reply string
	// Kind is which route the message took: KindChat, KindCode or
	// KindCommand.
	Kind    string
	Files   []string
	Applied bool
	// Command is the script the command route produced, recorded whether
	// or not it was allowed to run.
	Command string
}

// The routes a message can take. Everything that reads or changes files
// in this folder is KindCode; KindCommand is for the work that is not
// about this folder's contents at all.
const (
	KindChat    = "chat"
	KindCode    = "code"
	KindCommand = "command"
)

// Coding reports whether the message was routed to a change.
func (outcome Outcome) Coding() bool { return outcome.Kind == KindCode }

// Recollection is one earlier turn in this folder, as the agent sees it.
// The agent deliberately takes this rather than reading the store itself,
// so it stays free of any notion of where history lives.
type Recollection struct {
	Number  int
	Message string
	Summary string
	Files   []string
	// Note marks background handed in with --context rather than a turn
	// that actually happened here, so it can be stated as a fact instead
	// of being retold as something the user once asked for.
	Note bool
}

type routeDecision struct {
	// Action is "chat", "code" or "command".
	Action string `json:"action"`
	Reply  string `json:"reply"`
	// Command is the script to run when Action is "command".
	Command string `json:"command"`
	// Relevant are the turn numbers worth carrying into the work, and
	// Context ties them to the new message. Numbers are asked for as well
	// as prose because they can be checked: the summaries fed onward are
	// the ones actually recorded, so a brief that drifts cannot invent
	// history that never happened.
	Relevant numberList `json:"relevant"`
	Context  string     `json:"context"`
}

// route normalises what the model put in action. Models reach for
// neighbouring words when asked for an enum, and a value nobody
// recognises is better read as chat than guessed at: chat is the one
// route that cannot touch anything.
func (decision routeDecision) route() string {
	switch strings.ToLower(strings.TrimSpace(decision.Action)) {
	case "code", "coding", "coding_task", "change", "edit", "patch":
		return KindCode
	case "command", "commands", "shell", "cmd", "script", "run", "command_line":
		// A command route with nothing to run is not one. Falling back to
		// the reply is better than reporting an empty script as a refusal
		// the user then has to make sense of.
		if strings.TrimSpace(decision.Command) == "" {
			return KindChat
		}
		return KindCommand
	default:
		return KindChat
	}
}

// Commander runs a command script the model produced.
//
// The agent takes this rather than executing anything itself, so that
// deciding whether a generated command may run stays with the caller
// that has the user's terminal to ask on.
// watch is consulted while the script runs, so a long one is not simply
// trusted from the moment it was approved.
type Commander interface {
	Run(ctx context.Context, script string, watch shell.Supervisor) error
}

// ErrCommandDeclined is what a Commander returns when the user was asked
// and said no. It is not a failure of the run.
var ErrCommandDeclined = errors.New("command not run")

// numberList tolerates the shapes a model reaches for when asked for a
// list of numbers: [3, 7], ["3", "7"], or a bare 3.
type numberList []int

func (list *numberList) UnmarshalJSON(data []byte) error {
	var numbers []int
	if err := json.Unmarshal(data, &numbers); err == nil {
		*list = numbers
		return nil
	}
	var single int
	if err := json.Unmarshal(data, &single); err == nil {
		*list = numberList{single}
		return nil
	}
	var text stringList
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	values := make(numberList, 0, len(text))
	for _, entry := range text {
		if number, err := strconv.Atoi(strings.TrimSpace(entry)); err == nil {
			values = append(values, number)
		}
	}
	*list = values
	return nil
}

// Rewrite carries one file's complete new contents, for when no diff will
// apply. It is deliberately one file per request with a flat schema:
// asking for an array of path/content objects made weak providers return
// an empty array, and a single long file is likelier to fit in one reply.
type Rewrite struct {
	Summary string `json:"summary"`
	Content string `json:"content"`
}

type toolArguments struct {
	Paths []string `json:"paths"`
	Query string   `json:"query"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// decodeJSON unmarshals text into target, falling back to the first
// {...} or [...] span in text. Not every OpenAI-compatible provider
// enforces the requested JSON schema strictly; some let the model preface
// the JSON with prose or wrap it in a markdown fence.
func decodeJSON(text string, target any) error {
	directErr := json.Unmarshal([]byte(text), target)
	if directErr == nil {
		return nil
	}
	if span := jsonSpan(text); span != "" {
		spanErr := json.Unmarshal([]byte(span), target)
		if spanErr == nil {
			return nil
		}
		// Well-formed JSON in a shape we didn't ask for is a different
		// problem from a reply that isn't JSON at all, and saying "no
		// valid JSON" about a perfectly good object sends the reader
		// looking in the wrong place.
		if isShapeError(spanErr) {
			return fmt.Errorf("JSON did not match the expected shape (%w): %s", spanErr, strings.TrimSpace(text))
		}
	}
	if isShapeError(directErr) {
		return fmt.Errorf("JSON did not match the expected shape (%w): %s", directErr, strings.TrimSpace(text))
	}
	return fmt.Errorf("no valid JSON found in response: %s", strings.TrimSpace(text))
}

// isShapeError reports whether the reply parsed as JSON but its types
// didn't line up with the target.
func isShapeError(err error) bool {
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr)
}

// jsonSpan is the first complete JSON value in text, found by matching
// brackets rather than by taking the last one. A model that closes its
// object one brace too many, or writes anything at all after it, would
// otherwise poison the whole span. Brackets inside strings are skipped,
// which matters because the diff field is full of them.
func jsonSpan(text string) string {
	for _, delimiters := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		start := strings.IndexByte(text, delimiters[0])
		if start == -1 {
			continue
		}
		if end := matchingBracket(text, start, delimiters[0], delimiters[1]); end > start {
			return text[start : end+1]
		}
	}
	return ""
}

// matchingBracket finds the close that balances the open at start, or -1.
func matchingBracket(text string, start int, open, close byte) int {
	depth, inString, escaped := 0, false, false
	for index := start; index < len(text); index++ {
		character := text[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case character == '\\':
				escaped = true
			case character == '"':
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case open:
			depth++
		case close:
			if depth--; depth == 0 {
				return index
			}
		}
	}
	return -1
}

// Progress reports what the agent is doing. Status replaces a transient
// activity line; Log writes a permanent line, leaving the user a readable
// trail of the work rather than a single message that overwrites itself.
type Progress interface {
	Status(string)
	Log(string)
}

type Runner struct {
	client     *Client
	repository *repo.Repository
	// served is the contents already handed to the model, so asking for
	// the same unchanged file again costs a sentence instead of another
	// copy of it in a transcript that is resent on every turn.
	served map[string]string
	// commander runs what the command route produces. Without one that
	// route can only describe what it would have done, which is the right
	// default: nothing should execute because a caller forgot to say who
	// gets to approve it.
	commander Commander
}

func NewRunner(client *Client, repository *repo.Repository) *Runner {
	return &Runner{client: client, repository: repository, served: map[string]string{}}
}

// Commands supplies what runs a generated script, and returns the runner
// so it can be set where the runner is built.
func (runner *Runner) Commands(commander Commander) *Runner {
	runner.commander = commander
	return runner
}

// Run routes the message first: a plain conversational message gets the
// model's direct reply with the repository never touched, and only an
// actual coding task goes through the map/plan/patch/test loop.
func (runner *Runner) Run(ctx context.Context, task string, history []Recollection, progress Progress) (Outcome, error) {
	progress.Status("Reading your message...")
	decision, err := runner.route(ctx, task, history)
	if err != nil {
		return Outcome{}, err
	}
	switch decision.route() {
	case KindChat:
		return Outcome{Kind: KindChat, Reply: decision.Reply}, nil
	case KindCommand:
		return runner.command(ctx, decision, progress)
	}

	carried := recall(history, decision.Relevant)
	if len(carried) > 0 {
		progress.Log(fmt.Sprintf("  recalled %d earlier %s", len(carried), plural(len(carried), "turn", "turns")))
	}

	progress.Status("Mapping the folder...")
	repoMap, err := runner.repository.Map()
	if err != nil {
		return Outcome{}, err
	}
	mapped := mappedPaths(repoMap)
	progress.Log(fmt.Sprintf("  mapped %d files", len(mapped)))

	progress.Status("Working out the change...")
	session := runner.newChangeSession(task, repoMap, background(decision.Context, carried))

	var evidence string
	var change Change
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			progress.Status("Repairing from new evidence...")
		}
		change, err = session.produce(ctx, progress)
		if err != nil {
			return Outcome{}, err
		}
		if attempt == 0 {
			logSummary(progress, "plan", change.Summary)
			for _, path := range change.FilesToModify {
				progress.Log("  will edit " + path)
			}
		}

		progress.Status("Applying the patch...")
		if err := runner.repository.Apply(change.Diff); err != nil {
			progress.Log("  patch did not apply, retrying")
			// Feed back the diff that failed alongside git's complaint.
			// Without seeing its own output the model has no way to tell
			// what was wrong with it and tends to reproduce it verbatim.
			evidence = fmt.Sprintf(
				"The patch did not apply. Git looks for the context and removed lines exactly as written, "+
					"so any difference from the real file (an HTML entity spelled out, a changed attribute, "+
					"reflowed whitespace) makes the whole hunk fail. Compare git's \"while searching for\" text "+
					"below against the file contents you already read and copy those lines character for character.\n\n"+
					"GIT REPORTED:\n%s\n\nTHE DIFF THAT FAILED:\n%s",
				err.Error(), change.Diff)
			session.report(evidence)
			continue
		}
		progress.Log("  applied the patch")

		// The model names the files it means to touch, and that claim is
		// worth checking: a patch that quietly leaves out the new files it
		// promised applies perfectly well and looks like success.
		if missing := runner.missingFiles(change.FilesToModify); len(missing) > 0 {
			progress.Log("  but " + strings.Join(missing, ", ") + " was not created, retrying")
			evidence = fmt.Sprintf(
				"The patch applied, but it did not create %s, which you said it would modify. "+
					"A file that does not exist yet has to be created by the patch itself: use "+
					"\"*** Add File: <path>\" followed by every line of its contents, or a unified "+
					"diff whose header is \"--- /dev/null\". Include the complete contents of each "+
					"file, not a description of them.",
				strings.Join(missing, ", "))
			session.report(evidence)
			continue
		}

		progress.Status("Testing the change...")
		testResult := RunTests(ctx, runner.repository.Root)
		if testResult.Passed {
			if testResult.Skipped {
				progress.Log("  no test command detected")
			} else {
				progress.Log("  tests passed")
			}
			return Outcome{Reply: change.Summary, Kind: KindCode, Files: change.FilesToModify, Applied: true}, nil
		}
		progress.Log("  tests failed, retrying")
		evidence = "The patch applied, but tests failed. Produce an incremental diff against the current repository.\n" + testResult.Output
		session.report(evidence)
	}

	// Every diff was rejected. A diff only applies when its context and
	// removed lines match the file exactly, which a model-written one
	// often gets subtly wrong, so fall back to writing whole files.
	if strings.HasPrefix(evidence, "The patch did not apply") {
		targets := rewriteTargets(change.FilesToModify, session.readPaths, mapped)
		if len(targets) == 0 {
			return Outcome{}, fmt.Errorf("no diff would apply and the model named no file to rewrite:\n%s", evidence)
		}
		progress.Status("Rewriting the file instead...")
		summary := ""
		for _, path := range targets {
			current, _ := runner.repository.ReadFile(path)
			rewrite, err := runner.rewrite(ctx, task, path, current, evidence)
			if err != nil {
				return Outcome{}, err
			}
			// An empty rewrite of a file that has content is the model
			// failing, not an instruction to truncate the user's file.
			if strings.TrimSpace(rewrite.Content) == "" && strings.TrimSpace(current) != "" {
				return Outcome{}, fmt.Errorf("refusing to empty %s: the model returned no content for it", path)
			}
			if err := runner.repository.Write(path, rewrite.Content); err != nil {
				return Outcome{}, err
			}
			progress.Log("  wrote " + path)
			if summary == "" {
				summary = rewrite.Summary
			}
		}
		progress.Status("Testing the change...")
		testResult := RunTests(ctx, runner.repository.Root)
		if testResult.Passed {
			if testResult.Skipped {
				progress.Log("  no test command detected")
			} else {
				progress.Log("  tests passed")
			}
			if summary == "" {
				summary = "Rewrote " + strings.Join(targets, ", ")
			}
			return Outcome{Reply: summary, Kind: KindCode, Files: targets, Applied: true}, nil
		}
		return Outcome{}, fmt.Errorf("rewrote %s, but tests failed:\n%s", strings.Join(targets, ", "), testResult.Output)
	}
	return Outcome{}, fmt.Errorf("could not complete the task after repair attempts:\n%s", evidence)
}

// command runs what the router produced, once whoever owns the terminal
// has agreed to it.
//
// The script is reported back on the outcome whether or not it ran, so a
// declined command is still recorded: "what was that command again?" is
// a fair question to ask after saying no to one.
func (runner *Runner) command(ctx context.Context, decision routeDecision, progress Progress) (Outcome, error) {
	script := strings.TrimSpace(decision.Command)
	outcome := Outcome{Kind: KindCommand, Command: script, Reply: strings.TrimSpace(decision.Reply)}
	if runner.commander == nil {
		// Nothing is executed just because a caller neglected to say who
		// approves it; describing the command is the honest fallback.
		outcome.Reply = strings.TrimSpace(outcome.Reply + "\n\nThis would run:\n" + script)
		return outcome, nil
	}
	progress.Status("Running the command...")
	err := runner.commander.Run(ctx, script, &watcher{runner: runner, progress: progress})
	// The status line comes back the moment the command hands the
	// terminal over, and "Running the command..." would be a lie by then.
	progress.Status("Finishing up...")
	if err != nil {
		if errors.Is(err, ErrCommandDeclined) {
			outcome.Reply = "Left it alone."
			return outcome, nil
		}
		return outcome, err
	}
	outcome.Applied = true
	if outcome.Reply == "" {
		outcome.Reply = "Done."
	}
	return outcome, nil
}

// route decides whether the message is a question or a change, and when
// there is history to draw on, which of it matters.
//
// The request is ordered constant-first: the instructions, then the
// history, then the new message. History only ever grows at its end, so
// each turn's prompt is the previous turn's prompt plus a little more,
// which is the only arrangement a provider's prefix cache can reuse.
// Putting the new message first would defeat it entirely.
func (runner *Runner) route(ctx context.Context, task string, history []Recollection) (routeDecision, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	notes, turns := split(history)
	instructions, schema := routingInstructions(runner.repository.Root), routeSchema()
	input := any(task)
	if len(history) > 0 {
		// Asking for relevance selection costs schema and instructions,
		// so it is only asked for when there is something to select from.
		instructions, schema = recallingInstructions(runner.repository.Root), recallSchema()
		var messages []any
		// Notes are fixed for the whole invocation where history grows
		// each turn, so they sit ahead of it: the more stable a block is,
		// the earlier it belongs.
		if len(notes) > 0 {
			messages = append(messages, inputMessage{Role: "user", Content: "PROJECT CONTEXT:\n" + renderNotes(notes)})
		}
		if len(turns) > 0 {
			messages = append(messages, inputMessage{Role: "user", Content: "EARLIER IN THIS FOLDER:\n" + renderHistory(turns)})
		}
		input = append(messages, inputMessage{Role: "user", Content: "MESSAGE:\n" + task})
	}
	response, err := runner.client.create(callCtx, responseRequest{
		Instructions: instructions,
		Input:        input,
		Text:         strictSchema("message_route", schema),
	})
	if err != nil {
		return routeDecision{}, err
	}
	text, err := response.text()
	if err != nil {
		return routeDecision{}, err
	}
	var decision routeDecision
	if err := decodeJSON(text, &decision); err != nil {
		return routeDecision{}, fmt.Errorf("decode message route: %w", err)
	}
	return decision, nil
}

// changeSession is one continuous conversation that reads what it needs
// and produces a diff. Keeping the transcript here rather than asking the
// provider to remember it is deliberate: not every OpenAI-compatible
// provider persists server-side response state, and one that doesn't
// rejects a function_call_output referencing a call_id it never stored.
//
// Repairs continue this same conversation. The file contents are already
// in it from the tool calls, so a failed attempt costs only the evidence
// appended to the end, not another copy of everything.
type changeSession struct {
	runner     *Runner
	transcript []any
	readPaths  []string
}

func (runner *Runner) newChangeSession(task, repoMap, earlier string) *changeSession {
	opening := earlier + fmt.Sprintf("TASK:\n%s\n\nREPOSITORY MAP:\n%s", task, repoMap)
	return &changeSession{
		runner:     runner,
		transcript: []any{inputMessage{Role: "user", Content: opening}},
	}
}

// report adds what reality said about the last attempt, so the next one
// answers it rather than repeating itself.
func (session *changeSession) report(evidence string) {
	session.transcript = append(session.transcript, inputMessage{Role: "user", Content: evidence})
}

// produce runs tool rounds until the model returns a change.
func (session *changeSession) produce(ctx context.Context, progress Progress) (Change, error) {
	for round := 0; round < maxToolRounds; round++ {
		callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		response, err := session.runner.client.create(callCtx, responseRequest{
			Instructions: changeInstructions,
			Input:        session.transcript,
			Tools:        repositoryTools(),
			ToolChoice:   "auto",
			Text:         strictSchema("code_change", changeSchema()),
		})
		cancel()
		if err != nil {
			return Change{}, err
		}
		calls := response.calls()
		if len(calls) == 0 {
			text, err := response.text()
			if err != nil {
				return Change{}, err
			}
			var change Change
			if err := decodeJSON(text, &change); err != nil {
				return Change{}, fmt.Errorf("decode the change: %w", err)
			}
			salvageChange(&change, text)
			if strings.TrimSpace(change.Diff) == "" {
				return change, fmt.Errorf("the model returned no diff; it replied: %s", snippet(text))
			}
			return change, nil
		}
		for _, call := range calls {
			progress.Log("  " + session.runner.describeCall(call))
			session.readPaths = append(session.readPaths, readFilePaths(call)...)
			output := session.runner.runTool(ctx, call)
			session.transcript = append(session.transcript, call)
			session.transcript = append(session.transcript, toolOutput{Type: "function_call_output", CallID: call.CallID, Output: output})
		}
	}
	return Change{}, fmt.Errorf("the model exceeded %d repository tool rounds", maxToolRounds)
}

// rewrite asks for one file's complete new contents, used when no diff
// would apply.
func (runner *Runner) rewrite(ctx context.Context, task, path, current, evidence string) (Rewrite, error) {
	input := fmt.Sprintf("TASK:\n%s\n\nFILE TO REWRITE:\n%s\n\nITS CURRENT CONTENTS:\n%s\n\nWHY THE DIFF FAILED:\n%s", task, path, current, evidence)
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	response, err := runner.client.create(callCtx, responseRequest{
		Instructions: rewriteInstructions,
		Input:        input,
		Text:         strictSchema("file_rewrite", rewriteSchema()),
	})
	if err != nil {
		return Rewrite{}, err
	}
	text, err := response.text()
	if err != nil {
		return Rewrite{}, err
	}
	var result Rewrite
	if err := decodeJSON(text, &result); err != nil {
		return Rewrite{}, fmt.Errorf("decode rewrite of %s: %w", path, err)
	}
	if result.Content == "" {
		return Rewrite{}, fmt.Errorf("the model returned no content for %s; it replied: %s", path, snippet(text))
	}
	return result, nil
}

// rewriteTargets are the files to rewrite, in descending order of
// confidence: the ones the plan named, else the ones the model actually
// opened, else the only file in the folder. A weak model often returns an
// empty files_to_modify, and the file it read (or the single file there
// is) is then the best evidence of what it meant to change.
func rewriteTargets(named, readPaths, mapped []string) []string {
	if len(mapped) != 1 {
		mapped = nil
	}
	seen := map[string]bool{}
	var targets []string
	for _, group := range [][]string{named, readPaths, mapped} {
		for _, path := range group {
			if path = strings.TrimSpace(path); path != "" && !seen[path] {
				seen[path] = true
				targets = append(targets, path)
			}
		}
		if len(targets) > 0 {
			return targets
		}
	}
	return targets
}

// readFilePaths are the paths a read_files call asked for.
func readFilePaths(call responseItem) []string {
	if call.Name != "read_files" {
		return nil
	}
	var arguments toolArguments
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return nil
	}
	return arguments.Paths
}

// snippet trims a model reply down to something short enough to put in an
// error while still showing what came back.
func snippet(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 300 {
		return text[:300] + "..."
	}
	if text == "" {
		return "(nothing)"
	}
	return text
}

// readFiles answers a read_files call, replacing any file already shown
// and unchanged with a one-line note. A model will happily ask for the
// same file several times over, and every copy lands in a transcript that
// is resent in full on every following turn, so the cost of a needless
// re-read is paid again and again.
func (runner *Runner) readFiles(paths []string) (string, error) {
	var answer strings.Builder
	var fresh []string
	for _, path := range paths {
		if !runner.alreadyShown(path) {
			// Unreadable paths go through the normal read so it can
			// report the error properly.
			fresh = append(fresh, path)
			continue
		}
		fmt.Fprintf(&answer, "--- %s ---\n(unchanged since it was shown above)\n", path)
	}
	if len(fresh) == 0 {
		return answer.String(), nil
	}
	text, err := runner.repository.Read(fresh)
	if err != nil {
		return "", err
	}
	for _, path := range fresh {
		if content, readErr := runner.repository.ReadFile(path); readErr == nil {
			runner.served[path] = content
		}
	}
	return answer.String() + text, nil
}

// describeCall renders a tool call as a short line for the progress log,
// so the user can see which files the model is actually looking at. A
// re-read that costs nothing says so, rather than looking like the file
// was fetched all over again.
func (runner *Runner) describeCall(call responseItem) string {
	var arguments toolArguments
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return call.Name
	}
	switch call.Name {
	case "read_files":
		var fresh, seen []string
		for _, path := range arguments.Paths {
			if runner.alreadyShown(path) {
				seen = append(seen, path)
			} else {
				fresh = append(fresh, path)
			}
		}
		switch {
		case len(seen) == 0:
			return "read " + strings.Join(fresh, ", ")
		case len(fresh) == 0:
			return "read " + strings.Join(seen, ", ") + " (already shown, not resent)"
		default:
			return "read " + strings.Join(fresh, ", ") + " (" + strings.Join(seen, ", ") + " already shown)"
		}
	case "search":
		return "search " + strconv.Quote(arguments.Query)
	default:
		return call.Name
	}
}

// renderHistory lays out earlier turns for the router, numbered by their
// own absolute turn numbers so the selection that comes back can be
// matched to real records.
func renderHistory(history []Recollection) string {
	var text strings.Builder
	for _, turn := range history {
		fmt.Fprintf(&text, "%d. asked: %s", turn.Number, strings.TrimSpace(turn.Message))
		if turn.Summary != "" {
			fmt.Fprintf(&text, "\n   result: %s", turn.Summary)
		}
		if len(turn.Files) > 0 {
			fmt.Fprintf(&text, " (%s)", strings.Join(turn.Files, ", "))
		}
		text.WriteString("\n")
	}
	return text.String()
}

// renderNotes lays out background the caller supplied. It is unnumbered
// because, unlike recorded history, it is not a list to choose from.
func renderNotes(notes []Recollection) string {
	var text strings.Builder
	for _, note := range notes {
		fmt.Fprintf(&text, "- %s\n", strings.TrimSpace(note.Message))
	}
	return text.String()
}

// split separates supplied background from turns that actually happened,
// which are handled differently at every step: notes always carry, turns
// are selected from.
func split(history []Recollection) (notes, turns []Recollection) {
	for _, entry := range history {
		if entry.Note {
			notes = append(notes, entry)
			continue
		}
		turns = append(turns, entry)
	}
	return notes, turns
}

// recall picks out the turns the router chose, keeping the recorded text
// rather than any retelling of it. Supplied notes are kept whatever the
// router decided: the caller passed them in on purpose, so dropping them
// is not a judgement the router gets to make.
func recall(history []Recollection, chosen []int) []Recollection {
	wanted := make(map[int]bool, len(chosen))
	for _, number := range chosen {
		wanted[number] = true
	}
	var kept []Recollection
	for _, turn := range history {
		if turn.Note || wanted[turn.Number] {
			kept = append(kept, turn)
		}
	}
	return kept
}

// background is what the work should know about what came before: the
// router's brief, then the turns it pointed at, verbatim from the record.
func background(brief string, turns []Recollection) string {
	if strings.TrimSpace(brief) == "" && len(turns) == 0 {
		return ""
	}
	notes, recorded := split(turns)
	var text strings.Builder
	if len(notes) > 0 {
		text.WriteString("PROJECT CONTEXT:\n" + renderNotes(notes) + "\n")
	}
	if strings.TrimSpace(brief) == "" && len(recorded) == 0 {
		return text.String()
	}
	text.WriteString("EARLIER IN THIS FOLDER:\n")
	if brief = strings.TrimSpace(brief); brief != "" {
		text.WriteString(brief + "\n")
	}
	if len(recorded) > 0 {
		text.WriteString(renderHistory(recorded))
	}
	return text.String() + "\n"
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// missingFiles are the paths a change said it would touch that still
// aren't there, which is how a patch that silently omitted the new files
// it promised gives itself away.
func (runner *Runner) missingFiles(claimed []string) []string {
	var missing []string
	for _, path := range claimed {
		if path = strings.TrimSpace(path); path != "" && !runner.repository.Exists(path) {
			missing = append(missing, path)
		}
	}
	return missing
}

// alreadyShown reports whether path's current contents are the ones the
// model was already given.
func (runner *Runner) alreadyShown(path string) bool {
	served, ok := runner.served[path]
	if !ok || served == "" {
		return false
	}
	current, err := runner.repository.ReadFile(path)
	return err == nil && current == served
}

// logSummary logs "label: summary", skipping the line entirely when the
// model left the summary empty rather than printing a dangling label.
func logSummary(progress Progress, label, summary string) {
	if summary = strings.TrimSpace(summary); summary != "" {
		progress.Log("  " + label + ": " + summary)
	}
}

// alternatePlan matches a shape providers return when they ignore the
// requested schema, for example
// {"plan":[{"file":"index.html","changes":[...]}],"notes":"..."}.
type alternatePlan struct {
	Plan []struct {
		File string `json:"file"`
		Path string `json:"path"`
	} `json:"plan"`
	// Spellings models reach for instead of files_to_modify. Missing one
	// costs the file list silently: the trail loses its "will edit" line
	// and the whole-file fallback loses its first choice of target.
	Files         stringList `json:"files"`
	FilesModified stringList `json:"files_modified"`
	FilesChanged  stringList `json:"files_changed"`
	ModifiedFiles stringList `json:"modified_files"`
	Notes         string     `json:"notes"`
}

// salvageChange fills in a change whose file list came back empty because
// the provider let the model answer in its own shape rather than the
// schema that was asked for. Losing the file list is expensive: it costs
// the whole-file fallback its target.
func salvageChange(change *Change, text string) {
	if len(change.FilesToModify) > 0 {
		return
	}
	var alternate alternatePlan
	if err := decodeJSON(text, &alternate); err != nil {
		return
	}
	for _, entry := range alternate.Plan {
		if path := firstNonEmpty(entry.File, entry.Path); path != "" {
			change.FilesToModify = append(change.FilesToModify, path)
		}
	}
	for _, group := range []stringList{alternate.Files, alternate.FilesModified, alternate.FilesChanged, alternate.ModifiedFiles} {
		for _, path := range group {
			if path != "" {
				change.FilesToModify = append(change.FilesToModify, path)
			}
		}
	}
	if change.Summary == "" {
		change.Summary = alternate.Notes
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// mappedPaths are the file paths listed in a repository map.
func mappedPaths(repoMap string) []string {
	var paths []string
	for _, line := range strings.Split(repoMap, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			paths = append(paths, fields[0])
		}
	}
	return paths
}

func (runner *Runner) runTool(ctx context.Context, call responseItem) string {
	var arguments toolArguments
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return "ERROR: invalid tool arguments: " + err.Error()
	}
	switch call.Name {
	case "read_files":
		output, err := runner.readFiles(arguments.Paths)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		return output
	case "search":
		output, err := runner.repository.Search(ctx, arguments.Query)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		return output
	default:
		return "ERROR: unknown tool " + call.Name
	}
}

func repositoryTools() []functionTool {
	return []functionTool{
		{Type: "function", Name: "read_files", Description: "Read one or more repository files after choosing them from the map.", Strict: true, Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 12}}, "required": []string{"paths"}, "additionalProperties": false,
		}},
		{Type: "function", Name: "search", Description: "Search repository text with ripgrep when the map and files are insufficient.", Strict: true, Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false,
		}},
	}
}

func changeSchema() map[string]any {
	return objectSchema(map[string]any{
		"summary":         map[string]any{"type": "string"},
		"files_to_modify": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"diff":            map[string]any{"type": "string"},
	}, []string{"summary", "files_to_modify", "diff"})
}

func rewriteSchema() map[string]any {
	return objectSchema(map[string]any{
		"summary": map[string]any{"type": "string"},
		"content": map[string]any{"type": "string"},
	}, []string{"summary", "content"})
}

// recallSchema is the routing schema plus relevance selection, used only
// when there is history to select from.
// actionSchema is the route itself. Spelling the choices out as an enum
// rather than asking for prose is what keeps a provider that enforces the
// schema from inventing a fourth route.
func actionSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{KindChat, KindCode, KindCommand}}
}

func recallSchema() map[string]any {
	return objectSchema(map[string]any{
		"action":   actionSchema(),
		"reply":    map[string]any{"type": "string"},
		"command":  map[string]any{"type": "string"},
		"relevant": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
		"context":  map[string]any{"type": "string"},
	}, []string{"action", "reply", "command", "relevant", "context"})
}

func routeSchema() map[string]any {
	return objectSchema(map[string]any{
		"action":  actionSchema(),
		"reply":   map[string]any{"type": "string"},
		"command": map[string]any{"type": "string"},
	}, []string{"action", "reply", "command"})
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

const changeInstructions = `You are a small coding agent making one change.
Start from the repository map. Use read_files for the files you need and search only when the
map is not enough. Read a file before changing it; never write a diff against contents you have
not seen. Avoid wandering beyond what the task needs.
Everything you have already read stays in this conversation, so do not read a file a second
time or search for text you have already been shown; scroll up and use it.
When you have enough, answer with the change: a one-line summary, the files it modifies, and the
diff that makes it. Make the smallest complete change, and include tests when the repository
already has them.
The diff may be a unified diff or the "*** Begin Patch / *** Update File:" format; either is
read by matching its text against the file, so line numbers and hunk counts are ignored and do
not need to be correct.
A file that does not exist yet is created by the same diff: use "*** Add File: <path>" followed
by every line of its contents, or a unified diff whose header is "--- /dev/null". Every file you
list as modified must appear in the diff with its full contents; describing a file you mean to
add, or naming it without including it, leaves it uncreated.
What must be exact is the text itself. Copy every context and removed line from the file
character for character, including indentation, escapes and HTML entities such as &amp;. A line
that differs by even one character cannot be located. Surround each change with a few unchanged
lines so there is only one place it can go.
If told a previous attempt failed, answer the evidence rather than repeating the same diff.
Respond with only the JSON object, no other text before or after it.`

const rewriteInstructions = `You are the repair phase of a small coding agent.
Your unified diff could not be applied, so supply the whole file instead.
Return the named file's complete new contents in the content field, copied
from the supplied current contents with only the required edit made.
Never abbreviate, summarize, or elide any part of the file with comments
like "unchanged" or "...": what you return replaces the file exactly, so
anything you leave out is deleted.
Respond with only the JSON object, no other text before or after it.`

// routeChoices describes the three routes. Both router prompts share it
// so the boundary between them can only be stated once and cannot drift.
func routeChoices(folder string) string {
	return `You are working in the folder ` + folder + `, on ` + shell.Describe() + `.
Where you are, what this folder is called and what platform this is are things you already know
from that line: answer them yourself rather than running a command to find out.

Choose one action for the user's message.

"code" — the user wants files in this project written, fixed, explained from their contents, or
otherwise inspected or changed. Anything that needs to know what is in this folder is "code".

"command" — the user wants something run that is not about this folder's contents: installing or
upgrading tools, starting or stopping a process, git operations, network or system queries,
checking a version, looking at the environment. Put a shell script in command, and put one line
saying what it does in reply.
Do not reach for "command" to write, edit, read, list or search this project's files in order to
answer something. That is "code", which has proper tools for it and shows its work; a shell
command doing the same thing blindly is worse at the job and leaves nothing to review.
The exception is a message that is itself a shell command the user typed — "ls", "git status",
"npm test". Run it. They are asking for that command, not for you to choose how to find out.
The script runs in the user's current folder, by ` + shell.Describe() + `. Several lines are fine.
Prefer the least destructive command that does the job, and add no deletion, overwrite or force
flag the user did not ask for.

"chat" — anything else: the message can be answered directly, with nothing read, changed or run.
Put your complete reply in reply.

Set command to "" unless the action is "command", and reply to "" when the action is "code".`
}

func recallingInstructions(folder string) string {
	return `You are the first step of a small coding agent, and you are shown what has already
been asked in this folder before the new message.

` + routeChoices(folder) + `

When the action is "chat", use the earlier turns where the question is about them ("what did I
ask before?" is answered from the list, not guessed at).
A PROJECT CONTEXT block, if present, was handed to you directly rather than asked in an earlier
turn. Treat it as fact about the project, and do not list it in relevant: it is carried anyway.
In relevant, list the numbers of the earlier turns that genuinely bear on the new message.
Prefer constraints the user stated, corrections they made, and the goal they are working toward;
a correction is worth more than a success, because it is the user refining what they meant.
Leave out turns that merely went well and have no bearing now. An empty list is the right answer
when nothing earlier matters.
In context, write one or two sentences tying those turns to the new message, for whoever does
the work. Say only what the list supports; do not invent history.
Respond with only the JSON object, no other text before or after it.`
}

func routingInstructions(folder string) string {
	return routeChoices(folder) + `
Respond with only the JSON object, no other text before or after it.`
}
