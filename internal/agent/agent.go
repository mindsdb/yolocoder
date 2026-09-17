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
)

const maxToolRounds = 8

// Timeouts for the model calls the agent loop makes. Both are generous
// on purpose: a reasoning model that thinks before answering, or a
// slower or more heavily loaded endpoint, can easily take longer than a
// quick request would, and a request that's merely slow (not actually
// stuck) failing partway through a build is worse than it taking longer.
const produceTimeout = 5 * time.Minute

// Change is one attempt at the whole job: what the model means to do,
// which files it touches, and the diff that does it.
//
// Planning and patching are deliberately one request. Splitting them cost
// a round trip and, worse, resent every file: the contents are already in
// the conversation from the tool calls, so a separate patch call shipped
// them a second time to learn nothing new. Summary and files come before
// the diff in the schema so the model still states its intent first.
//
// Answer is set instead of the three fields above when the task turns out
// to be a question rather than a change. Routing alone cannot tell the two
// apart for anything that needs the files to answer — "what color is the
// background" is coding_task at that point, since routing has no tools
// and cannot read the file itself — so it resolves here instead, once
// this session actually has the file in hand. Without a field for it, a
// model that reaches this conclusion has nowhere to put the answer but
// summary, which reads as a change description, not prose meant for the
// user; asking for it explicitly is what lets an informational question
// end in a real answer instead of "the model returned no diff."
type Change struct {
	Summary       string     `json:"summary"`
	FilesToModify stringList `json:"files_to_modify"`
	Diff          string     `json:"diff"`
	Answer        string     `json:"answer"`
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
	Reply   string
	Coding  bool
	Files   []string
	Applied bool
	// Attempts is how many plan/patch/test passes the change took (1 if
	// it applied and passed on the first try). Recorded so a folder's
	// session log can answer "how often does this need a repair?" later
	// without needing full debug logging turned on to find out.
	Attempts int
	// Rewrote reports whether no diff would apply at all and the change
	// fell back to writing whole files instead.
	Rewrote bool
	// Usage is every call this turn made, summed. Zero when the provider
	// never reported usage at all (see Usage.Empty).
	Usage Usage
	// Profile is where the turn's wall clock went, step by step, summed
	// the same way and over the same scope as Usage: the whole turn, not
	// the last call it happened to make.
	Profile Profile
}

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

// Rewrite carries one file's complete new contents, for when no diff will
// apply. It is deliberately one file per request with a flat schema:
// asking for an array of path/content objects made weak providers return
// an empty array, and a single long file is likelier to fit in one reply.
type Rewrite struct {
	Summary string `json:"summary"`
	Content string `json:"content"`
}

type toolArguments struct {
	Paths  []string `json:"paths"`
	Query  string   `json:"query"`
	Reason string   `json:"reason"`
}

// inputMessage is one message sent to the model. Images (data URLs pasted
// into the web UI) ride alongside Content rather than replacing it; when
// there are none, it marshals as the plain {role, content} shape every
// provider expects for a text-only message. MarshalJSON is what makes that
// switch, since a message can be dropped into responseRequest.Input as a
// bare value or inside a []any slice — either way, encoding/json reaches
// this method regardless of which position it sits in.
type inputMessage struct {
	Role    string
	Content string
	Images  []string
}

func (message inputMessage) MarshalJSON() ([]byte, error) {
	if len(message.Images) == 0 {
		return json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{message.Role, message.Content})
	}
	// The Responses API's multimodal shape: content becomes a list of
	// typed parts instead of a bare string.
	var parts []map[string]any
	if message.Content != "" {
		parts = append(parts, map[string]any{"type": "input_text", "text": message.Content})
	}
	for _, image := range message.Images {
		parts = append(parts, map[string]any{"type": "input_image", "image_url": image})
	}
	return json.Marshal(struct {
		Role    string           `json:"role"`
		Content []map[string]any `json:"content"`
	}{message.Role, parts})
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
	// usage accumulates every call this Runner makes — route, each tool
	// round, and any rewrite — since a Runner is created fresh per turn
	// (see app.RunTask), so by the time Run returns this is the whole
	// turn's cost, not any one call's.
	usage Usage
	// profile accumulates the same way over the same scope, in time
	// rather than tokens.
	profile Profile
	// earlier is everything this folder has been asked before. The last
	// few turns of it ride along in the opening message; the rest is held
	// here for the recall tool to serve, when that tool is offered at all.
	earlier []Recollection
	// recall reports whether the recall tool is offered this run. Off by
	// default (see config.LLM.Recall): the inline turns already answer
	// almost every follow-up, and an unused tool is still a definition on
	// every request.
	recall bool
	// told reports whether earlier has already been handed over, so a
	// second recall costs a sentence rather than another copy of it in a
	// transcript that is resent on every following round.
	told bool
}

func NewRunner(client *Client, repository *repo.Repository) *Runner {
	return &Runner{client: client, repository: repository, served: map[string]string{}}
}

// UseRecall offers (or withholds) the tool for reading further back than
// the turns carried inline.
func (runner *Runner) UseRecall(on bool) { runner.recall = on }

// inlineTurns is how many of the most recent turns ride along in the
// opening message. Measured on a real folder, three of them come to
// around 1.5 KB — far less than a round trip spent fetching them would
// cost, and they cover the follow-ups that make up most of a session
// ("make it bigger", "undo that", "now the other one").
const inlineTurns = 3

// recentTurns splits the history into the turns carried inline and the
// count of older ones behind them, which is all the recall tool has left
// to offer.
func (runner *Runner) recentTurns() ([]Recollection, int) {
	if len(runner.earlier) <= inlineTurns {
		return runner.earlier, 0
	}
	return runner.earlier[len(runner.earlier)-inlineTurns:], len(runner.earlier) - inlineTurns
}

// Run works the message out in one conversation: the model is given the
// map and the message together and ends in whichever of three ways fits
// — a direct reply, an answer drawn from files it read, or a diff.
//
// There is deliberately no separate routing call ahead of this. One used
// to decide "question or change?" before anything was read, but it had no
// tools and so could not actually settle it for any message that needed
// the files to answer; it said "change" and deferred, costing a serial
// round trip to reach a foregone conclusion. The same judgement is made
// here instead, by the call that can act on it.
//
// images are data URLs (screenshots pasted into the web UI) attached to
// the message, nil when there are none — most models the terminal talks
// to aren't multimodal at all, so this is never required.
func (runner *Runner) Run(ctx context.Context, task string, images []string, history []Recollection, progress Progress) (Outcome, error) {
	notes, turns := split(history)
	runner.earlier = turns
	_, beyond := runner.recentTurns()
	runner.profile.Recall.Available = beyond

	progress.Status("Mapping the folder...")
	mapStarted := time.Now()
	repoMap, err := runner.repository.Map()
	mapSpent := time.Since(mapStarted)
	runner.profile.record(StepMap, mapSpent)
	if err != nil {
		return Outcome{Profile: runner.profile}, err
	}
	mapped := mappedPaths(repoMap)
	progress.Log(fmt.Sprintf("  mapped %d files · %s", len(mapped), formatDuration(mapSpent)))

	progress.Status("Working out the change...")
	session := runner.newChangeSession(task, repoMap, notes, images)

	var evidence string
	var change Change
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			progress.Status("Repairing from new evidence...")
		}
		change, err = session.produce(ctx, progress)
		if err != nil {
			if !errors.Is(err, errNoDiff) {
				return Outcome{Profile: runner.profile}, err
			}
			progress.Log("  no diff came back, retrying")
			logSummary(progress, "  it said", change.Summary)
			evidence = fmt.Sprintf(
				"You returned a summary but the diff field was empty, so nothing changed. "+
					"The summary said:\n\n%s\n\nProduce the diff that makes exactly that change now, "+
					"in the diff field. Keep the summary to a single short line: it is written before "+
					"the diff, and a long one leaves less room for the diff that actually matters.",
				strings.TrimSpace(change.Summary))
			session.report(evidence)
			continue
		}
		if strings.TrimSpace(change.Diff) == "" && strings.TrimSpace(change.Answer) != "" {
			// The task turned out to be a question, not a change: nothing
			// to apply or test, so this concludes the turn immediately
			// rather than entering the loop meant for an actual change.
			progress.Log("  answered without changing anything")
			return Outcome{Reply: change.Answer, Usage: runner.usage, Profile: runner.profile}, nil
		}
		if attempt == 0 {
			logSummary(progress, "plan", change.Summary)
			for _, path := range change.FilesToModify {
				progress.Log("  will edit " + path)
			}
		}

		progress.Status("Applying the patch...")
		patchStarted := time.Now()
		applyErr := runner.repository.Apply(change.Diff)
		patchSpent := time.Since(patchStarted)
		runner.profile.record(StepPatch, patchSpent)
		if applyErr != nil {
			progress.Log("  patch did not apply, retrying · " + formatDuration(patchSpent))
			// Why, not just that. The applier already works out which
			// file, which hunk and which single line differs; until now
			// all of that went to the model and to the debug log, and
			// the person watching got three words they could do nothing
			// with — on a run where roughly a quarter of changes need a
			// repair, that is the thing worth seeing.
			for _, line := range repo.Explain(applyErr) {
				progress.Log("    " + line)
			}
			// Feed back the diff that failed alongside git's complaint.
			// Without seeing its own output the model has no way to tell
			// what was wrong with it and tends to reproduce it verbatim.
			evidence = fmt.Sprintf(
				"The patch did not apply. Git looks for the context and removed lines exactly as written, "+
					"so any difference from the real file (an HTML entity spelled out, a changed attribute, "+
					"reflowed whitespace) makes the whole hunk fail. Compare git's \"while searching for\" text "+
					"below against the file contents you already read and copy those lines character for character.\n\n"+
					"GIT REPORTED:\n%s\n\nTHE DIFF THAT FAILED:\n%s",
				applyErr.Error(), change.Diff)
			session.report(evidence)
			continue
		}
		progress.Log("  applied the patch · " + formatDuration(patchSpent))

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
		testResult, testSpent := runner.runTests(ctx)
		if testResult.Passed {
			if testResult.Skipped {
				progress.Log("  no test command detected")
			} else {
				progress.Log("  tests passed · " + formatDuration(testSpent))
			}
			return Outcome{Reply: change.Summary, Coding: true, Files: change.FilesToModify, Applied: true, Attempts: attempt + 1, Usage: runner.usage, Profile: runner.profile}, nil
		}
		progress.Log("  tests failed, retrying · " + formatDuration(testSpent))
		for _, line := range firstFailures(testResult.Output) {
			progress.Log("    " + line)
		}
		evidence = "The patch applied, but tests failed. Produce an incremental diff against the current repository.\n" + testResult.Output
		session.report(evidence)
	}

	// Every diff was rejected. A diff only applies when its context and
	// removed lines match the file exactly, which a model-written one
	// often gets subtly wrong, so fall back to writing whole files.
	if strings.HasPrefix(evidence, "The patch did not apply") || strings.HasPrefix(evidence, "You returned a summary but the diff field was empty") {
		targets := rewriteTargets(change.FilesToModify, session.readPaths, mapped)
		if len(targets) == 0 {
			return Outcome{Profile: runner.profile}, fmt.Errorf("no diff would apply and the model named no file to rewrite:\n%s", evidence)
		}
		progress.Status("Rewriting the file instead...")
		summary := ""
		for _, path := range targets {
			current, _ := runner.repository.ReadFile(path)
			rewriteStarted := time.Now()
			rewrite, err := runner.rewrite(ctx, task, path, current, evidence, images)
			rewriteSpent := time.Since(rewriteStarted)
			runner.profile.record(StepRewrite, rewriteSpent)
			if err != nil {
				return Outcome{Profile: runner.profile}, err
			}
			// An empty rewrite of a file that has content is the model
			// failing, not an instruction to truncate the user's file.
			if strings.TrimSpace(rewrite.Content) == "" && strings.TrimSpace(current) != "" {
				return Outcome{Profile: runner.profile}, fmt.Errorf("refusing to empty %s: the model returned no content for it", path)
			}
			if err := runner.repository.Write(path, rewrite.Content); err != nil {
				return Outcome{Profile: runner.profile}, err
			}
			progress.Log("  wrote " + path + " · " + formatDuration(rewriteSpent))
			if summary == "" {
				summary = rewrite.Summary
			}
		}
		progress.Status("Testing the change...")
		testResult, testSpent := runner.runTests(ctx)
		if testResult.Passed {
			if testResult.Skipped {
				progress.Log("  no test command detected")
			} else {
				progress.Log("  tests passed · " + formatDuration(testSpent))
			}
			if summary == "" {
				summary = "Rewrote " + strings.Join(targets, ", ")
			}
			return Outcome{Reply: summary, Coding: true, Files: targets, Applied: true, Attempts: 3, Rewrote: true, Usage: runner.usage, Profile: runner.profile}, nil
		}
		return Outcome{Profile: runner.profile}, fmt.Errorf("rewrote %s, but tests failed:\n%s", strings.Join(targets, ", "), testResult.Output)
	}
	return Outcome{Profile: runner.profile}, fmt.Errorf("could not complete the task after repair attempts:\n%s", evidence)
}

// firstFailures are the few lines of test output most likely to say what
// broke, for the trail. The model gets the whole thing either way; this
// is so the person watching does not have to turn on debug logging to
// find out whether it was one type error or forty.
func firstFailures(output string) []string {
	var picked []string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "$ ") {
			continue
		}
		picked = append(picked, clip(trimmed, 110))
		if len(picked) == 3 {
			break
		}
	}
	return picked
}

// clip keeps a line inside a terminal's width.
func clip(line string, limit int) string {
	if len(line) <= limit {
		return line
	}
	return line[:limit] + "..."
}

// runTests runs the project's tests and records what they cost,
// returning both the result and the time it took so the caller can say
// so on the line it was already printing.
//
// A run that found no test command is deliberately not recorded as a
// step: nothing ran, so there is nothing there to make faster, and a
// 0ms "test" entry would only pad the profile line.
func (runner *Runner) runTests(ctx context.Context) (TestResult, time.Duration) {
	started := time.Now()
	result := RunTests(ctx, runner.repository.Root)
	spent := time.Since(started)
	if !result.Skipped {
		runner.profile.record(StepTest, spent)
	}
	return result, spent
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

// newChangeSession opens the conversation, ordered constant-first: the
// supplied notes, which are fixed for the whole invocation, then the map,
// which only moves when the folder does, and the message last because it
// is the one part that is different every time.
//
// That order is the only one a provider's prefix cache can reuse. Putting
// the task ahead of the map — as this did — meant no two turns in a
// session ever shared a prefix, so the map was re-read as fresh input on
// every one of them.
func (runner *Runner) newChangeSession(task, repoMap string, notes []Recollection, images []string) *changeSession {
	var opening strings.Builder
	if len(notes) > 0 {
		opening.WriteString("PROJECT CONTEXT:\n" + renderNotes(notes) + "\n")
	}
	fmt.Fprintf(&opening, "REPOSITORY MAP:\n%s\n", repoMap)
	recent, beyond := runner.recentTurns()
	if len(recent) > 0 {
		opening.WriteString("EARLIER IN THIS FOLDER:\n" + renderHistory(recent) + "\n")
	}
	if beyond > 0 && runner.recall {
		// Said out loud because the model cannot otherwise know there is
		// anything behind what it was shown, and the tool would sit
		// unused however far back the message actually reaches.
		fmt.Fprintf(&opening, "%d further %s came before %s, and %s not shown. Call recall to read %s if this message reaches back past what is above.\n\n",
			beyond, plural(beyond, "turn", "turns"), plural(len(recent), "that one", "those"),
			plural(beyond, "is", "are"), plural(beyond, "it", "them"))
	}
	fmt.Fprintf(&opening, "TASK:\n%s", task)
	return &changeSession{
		runner:     runner,
		transcript: []any{inputMessage{Role: "user", Content: opening.String(), Images: images}},
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
		callCtx, cancel := context.WithTimeout(ctx, produceTimeout)
		started := time.Now()
		response, err := session.runner.client.create(callCtx, responseRequest{
			Instructions: changeInstructions,
			Input:        session.transcript,
			Tools:        repositoryTools(session.runner.recall),
			ToolChoice:   "auto",
			Text:         strictSchema("code_change", changeSchema()),
		})
		spent := time.Since(started)
		cancel()
		session.runner.profile.record(StepThink, spent)
		if err != nil {
			return Change{}, err
		}
		progress.Log("  thought · " + formatDuration(spent))
		session.runner.usage = session.runner.usage.add(response.usage())
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
			if strings.TrimSpace(change.Diff) == "" && strings.TrimSpace(change.Answer) == "" {
				// Repairable, not fatal: the model described a change
				// and then did not produce one, which is a slip it can
				// answer for — and it has already read the files, so
				// making it try again costs the evidence and nothing
				// else. Failing the turn here threw away everything the
				// conversation had gathered over a missing field.
				return change, fmt.Errorf("%w; it replied: %s", errNoDiff, snippet(text))
			}
			return change, nil
		}
		for _, call := range calls {
			// Described before the tool runs, not after: describeCall
			// reports which paths were already shown by consulting the
			// same served map that answering a read_files call updates,
			// so asking afterwards would report every file as a re-read.
			described := session.runner.describeCall(call)
			progress.Status(described)
			toolStarted := time.Now()
			session.readPaths = append(session.readPaths, readFilePaths(call)...)
			output := session.runner.runTool(ctx, call)
			toolSpent := time.Since(toolStarted)
			// Recall is recorded as its own step rather than lumped in
			// with the rest: whether a turn reached for history at all
			// is the measurement this design rests on, and it is lost
			// the moment it is averaged in with reading files.
			step := StepTools
			if call.Name == "recall" {
				step = StepRecall
				session.runner.profile.Recall.Spent += toolSpent
			}
			session.runner.profile.record(step, toolSpent)
			progress.Log("  " + described + " · " + formatDuration(toolSpent))
			session.transcript = append(session.transcript, call)
			session.transcript = append(session.transcript, toolOutput{Type: "function_call_output", CallID: call.CallID, Output: output})
		}
	}
	return Change{}, fmt.Errorf("the model exceeded %d repository tool rounds", maxToolRounds)
}

// errNoDiff marks a reply that promised a change and carried neither a
// diff nor an answer. Run recognizes it and repairs rather than giving up.
var errNoDiff = errors.New("the model returned no diff")

// rewrite asks for one file's complete new contents, used when no diff
// would apply.
func (runner *Runner) rewrite(ctx context.Context, task, path, current, evidence string, images []string) (Rewrite, error) {
	prompt := fmt.Sprintf("TASK:\n%s\n\nFILE TO REWRITE:\n%s\n\nITS CURRENT CONTENTS:\n%s\n\nWHY THE DIFF FAILED:\n%s", task, path, current, evidence)
	input := any(prompt)
	if len(images) > 0 {
		input = []any{inputMessage{Role: "user", Content: prompt, Images: images}}
	}
	callCtx, cancel := context.WithTimeout(ctx, produceTimeout)
	defer cancel()
	response, err := runner.client.create(callCtx, responseRequest{
		Instructions: rewriteInstructions,
		Input:        input,
		Text:         strictSchema("file_rewrite", rewriteSchema()),
	})
	if err != nil {
		return Rewrite{}, err
	}
	runner.usage = runner.usage.add(response.usage())
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

// recallEarlier answers a recall call with everything older than the
// turns already carried inline, unfiltered and in the recorded words.
// There is no selection: choosing which turns matter was its own model
// call once, and paying a serial round trip to narrow a few kilobytes
// was a worse trade than simply handing them over when asked.
//
// A second call costs a sentence rather than another copy, for the same
// reason readFiles refuses to resend a file already shown: every copy
// lands in a transcript that is resent in full on every following round.
func (runner *Runner) recallEarlier() string {
	_, beyond := runner.recentTurns()
	if beyond == 0 {
		return "Nothing came before the turns you were already shown."
	}
	if runner.told {
		return "(already recalled above)"
	}
	runner.told = true
	older := runner.earlier[:beyond]
	rendered := renderHistory(older)
	runner.profile.Recall.Served = len(older)
	runner.profile.Recall.Bytes = len(rendered)
	return "BEFORE THAT, IN THIS FOLDER:\n" + rendered
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
	case "recall":
		if reason := strings.TrimSpace(arguments.Reason); reason != "" {
			return "recall earlier turns: " + reason
		}
		return "recall earlier turns"
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
	case "recall":
		return runner.recallEarlier()
	default:
		return "ERROR: unknown tool " + call.Name
	}
}

func repositoryTools(recall bool) []functionTool {
	tools := []functionTool{
		{Type: "function", Name: "read_files", Description: "Read one or more repository files after choosing them from the map.", Strict: true, Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 12}}, "required": []string{"paths"}, "additionalProperties": false,
		}},
		{Type: "function", Name: "search", Description: "Search repository text with ripgrep when the map and files are insufficient.", Strict: true, Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false,
		}},
	}
	if recall {
		// reason is required rather than the tool taking no arguments at
		// all: a strict empty-properties schema is the shape fussy
		// providers are likeliest to reject, and asking why it is
		// reaching back makes the trail say so too.
		tools = append(tools, functionTool{Type: "function", Name: "recall", Description: "Read what was asked in this folder before the turns you were shown. Use it when the message reaches back further than those.", Strict: true, Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"reason": map[string]any{"type": "string"}}, "required": []string{"reason"}, "additionalProperties": false,
		}})
	}
	return tools
}

func changeSchema() map[string]any {
	return objectSchema(map[string]any{
		"summary":         map[string]any{"type": "string"},
		"files_to_modify": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"diff":            map[string]any{"type": "string"},
		"answer":          map[string]any{"type": "string"},
	}, []string{"summary", "files_to_modify", "diff", "answer"})
}

func rewriteSchema() map[string]any {
	return objectSchema(map[string]any{
		"summary": map[string]any{"type": "string"},
		"content": map[string]any{"type": "string"},
	}, []string{"summary", "content"})
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

const changeInstructions = `You are a small coding agent working in one folder.

You are given the repository map and the user's message. The message may be a coding task,
a question about the project, or ordinary conversation, and deciding which is part of your
job: end in exactly one of these three ways.

Nothing to read and nothing to change — a greeting, a thank-you, a remark, or a question you
can already answer: put your reply in answer and call no tool at all. Do not go looking
through the repository for a message that did not ask you to.

A question the files can answer: read what you need, then put the answer in answer — the
actual colors, values, structure, or whatever was asked, drawn from what you read, not a
description that an answer exists — leaving summary, files_to_modify and diff empty.

A change: read what you need, then return a one-line summary, the files it modifies, and the
diff that makes it, leaving answer empty. Make the smallest complete change, and include
tests when the repository already has them. The summary is one short line and nothing more —
it is written before the diff, so a long one spends the room the diff needs; describe the
change in the diff, not in prose about it. A summary with an empty diff changes nothing at
all, and is the one reply that is always wrong.

Reading: start from the repository map. Use read_files for the files you need, naming every
file you want in one call rather than a call per file, and search only when the map is not
enough. Read a file before changing it; never write a diff against contents you have not
seen. The last few turns in this folder are already above; recall, when it is offered, reads what
came before those, for a message that reaches back further than they go. Everything you have already
read stays in this conversation, so do not read a file a second time or search for text you
have already been shown; scroll up and use it. Avoid wandering beyond what the task needs.

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
