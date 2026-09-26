package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mindsdb/yolocoder/internal/debug"
	"github.com/mindsdb/yolocoder/internal/repo"
)

// Timeouts for the model calls the agent loop makes. Both are generous
// on purpose: a reasoning model that thinks before answering, or a
// slower or more heavily loaded endpoint, can easily take longer than a
// quick request would, and a request that's merely slow (not actually
// stuck) failing partway through a build is worse than it taking longer.
const produceTimeout = 5 * time.Minute

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
	Paths []string `json:"paths"`
	// Matching is a pattern on file contents. Files that match are read
	// along with Paths, so knowing what the code says is enough — the
	// model no longer has to search, wait, and then ask for what the
	// search found.
	Matching string `json:"matching"`
	Query    string `json:"query"`
	Reason   string `json:"reason"`
	Patch    string `json:"patch"`
	// Comment is what the model would have said at the end of the turn,
	// written with its last edit instead of in a round trip of its own.
	Comment string `json:"response_comment_for_user"`
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
	client          *Client
	repository      *repo.Repository
	editRouterModel string
	smallEditModel  string
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
	// preselect reports whether the files a task needs are chosen before
	// the model is asked, rather than by it.
	preselect bool
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
	return &Runner{client: client, repository: repository, served: map[string]string{}, editRouterModel: editRouterModel, smallEditModel: smallEditModel}
}

// UseRecall offers (or withholds) the tool for reading further back than
// the turns carried inline.
func (runner *Runner) UseRecall(on bool) { runner.recall = on }

// UsePreselect turns on choosing a turn's files before the model is
// asked for them.
func (runner *Runner) UsePreselect(on bool) { runner.preselect = on }

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

// recallTurns bounds what the recall tool hands over. A window rather
// than everything on record: the point of reaching back is to pick up a
// thread, and ten turns is far enough to find one while staying small
// enough that asking for it is cheap. Whatever the session store happens
// to be holding is not a number this should inherit.
const recallTurns = 10

// recallable are the turns recall would serve — the ones just before the
// window already carried inline, newest first in the file's order and
// capped. Older than that is out of reach, which is deliberate: history
// that far back is better re-stated than guessed at.
func (runner *Runner) recallable() []Recollection {
	_, beyond := runner.recentTurns()
	older := runner.earlier[:beyond]
	if len(older) > recallTurns {
		older = older[len(older)-recallTurns:]
	}
	return older
}

// Run works the message out in one conversation: the model is given the
// map and the message together and ends in whichever of three ways fits
// — a direct reply, an answer drawn from files it read, or a diff.
//
// Normal builds have no separate routing call ahead of this. One used
// to decide "question or change?" before anything was read, but it had no
// tools and so could not actually settle it for any message that needed
// the files to answer; it said "change" and deferred, costing a serial
// round trip to reach a foregone conclusion. The same judgement is made
// here instead, by the call that can act on it.
// An experimental edit router can prefetch context; it never decides the
// outcome or replaces the normal conversation.
//
// images are data URLs (screenshots pasted into the web UI) attached to
// the message, nil when there are none — most models the terminal talks
// to aren't multimodal at all, so this is never required.
func (runner *Runner) Run(ctx context.Context, task string, images []string, history []Recollection, progress Progress) (Outcome, error) {
	notes, turns := split(history)
	runner.earlier = turns
	runner.profile.Recall.Available = len(runner.recallable())

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

	session := runner.newChangeSession(task, repoMap, notes, images)

	// The files first, if that is switched on. It is an optimization and
	// behaves like one: anything at all going wrong leaves the turn
	// exactly as it was, with the model asking for what it wants.
	if runner.editRouterModel == "" && runner.preselect {
		progress.Status("Choosing the files...")
		started := time.Now()
		chosen := runner.chooseFiles(ctx, task, mapped)
		spent := time.Since(started)
		runner.profile.record(StepFiles, spent)
		if len(chosen) > 0 {
			session.preread(chosen, progress)
		}
		progress.Log("  chose files · " + formatDuration(spent))
	}
	if len(images) == 0 {
		runner.prefetchEdit(ctx, session, mapped, progress)
	}
	progress.Status("Working out the change...")
	outcome, err := session.work(ctx, progress)
	// The whole-file fallback is for a model that ran out of room, not
	// one that made up its mind. A turn that finished on its own saying
	// it could not do something has decided that, and overriding it by
	// rewriting a file whole would be the tool talking over the judgement
	// it just asked for. Running out of rounds or out of apply_diff calls
	// is the opposite: no decision was reached, and the conversation
	// already knows everything the rewrite needs.
	outOfEdits := err == nil && !outcome.Applied && session.used["apply_diff"] >= toolQuota["apply_diff"]
	stuck := len(session.attempted) > 0 && (outOfEdits || errors.Is(err, errOutOfRounds))
	if stuck {
		return runner.rewriteInstead(ctx, task, session, mapped, images, progress)
	}
	if err != nil {
		return Outcome{Profile: runner.profile}, err
	}
	return outcome, nil
}

// work is one conversation that reads, edits and replies. An accepted
// final edit can signal completion; otherwise a plain reply ends the turn.
// Both completion paths run the project's check after changes.
func (session *changeSession) work(ctx context.Context, progress Progress) (Outcome, error) {
	runner := session.runner
	workStarted := time.Now()
	for round := 0; round < maxRounds; round++ {
		input := session.timedInput(ctx, workStarted, time.Now())
		if !session.prefetched {
			progress.Log("  normal timing feedback attached")
		}
		callCtx, cancel := context.WithTimeout(ctx, produceTimeout)
		started := time.Now()
		response, err := session.create(callCtx, responseRequest{
			Instructions: session.instructions(),
			Input:        input,
			Tools:        session.tools(),
			ToolChoice:   "auto",
		}, progress)
		spent := time.Since(started)
		cancel()
		runner.profile.record(StepThink, spent)
		if err != nil {
			return Outcome{}, err
		}
		if !session.prefetched && ctx.Err() != nil {
			return Outcome{}, ctx.Err()
		}
		runner.usage = runner.usage.add(response.usage())
		progress.Log("  thought · " + formatDuration(spent))

		calls := response.calls()
		if len(calls) == 0 {
			reply, err := response.text()
			if !session.prefetched && response.cutOff() && err == nil {
				// Partial text is not a terminal answer, even when nonempty.
				session.complete = false
				session.closing = ""
				progress.Log("  completion deferred: max_output_tokens before plain reply")
				session.report("Your last reply was cut off before it finished. Continue the remaining work without repeating completed edits, then give a complete reply.")
				continue
			}
			if err != nil {
				// A reply cut off by the output cap decided nothing — the
				// model ran out of room before it finished, so there is no
				// finished text and no tool call to act on. Failing the
				// turn over it ("LLM response contained no output text")
				// is what a real trace showed: a read answered fine, then
				// an incomplete reply with empty text ended everything.
				// Ask again, shorter, in the same conversation instead.
				if response.cutOff() {
					progress.Log("  reply was cut off, asking again...")
					session.report("Your last reply was cut off before it finished (you ran out of output room). " +
						"Continue what you were doing, keeping this reply short: finish the tool call or message you started without repeating what is already above.")
					continue
				}
				return Outcome{}, err
			}
			// The model believes it is finished. If it changed anything,
			// the project gets a say before that is accepted: a failing
			// check goes back into this same conversation rather than
			// ending the turn, so it cannot declare victory over a build
			// it just broke.
			if len(session.applied) > 0 {
				result, testSpent := runner.runTests(ctx)
				if !session.prefetched && ctx.Err() != nil {
					return Outcome{}, ctx.Err()
				}
				switch {
				case result.Skipped:
					progress.Log("  no check command detected")
				case !result.Passed:
					progress.Log("  check failed, back to it · " + formatDuration(testSpent))
					for _, line := range firstFailures(result.Output) {
						progress.Log("    " + line)
					}
					session.report("The edits applied, but the project's check failed. Fix it.\n" + result.Output)
					continue
				default:
					progress.Log("  check passed · " + formatDuration(testSpent))
				}
			}
			return session.outcome(reply), nil
		}

		// A reply can carry the model's own words alongside its tool calls
		// — a plan, a correction, a note on what it just found. Those were
		// being dropped twice over: never shown, and never put back into
		// the conversation, so the model could not see on round three the
		// four-step plan it had written on round one.
		if aside := strings.TrimSpace(asideOf(response)); aside != "" {
			for _, line := range asideLines(aside) {
				progress.Log("    " + line)
			}
			session.transcript = append(session.transcript,
				inputMessage{Role: "assistant", Content: clip(aside, maxAside)})
		}

		batchOK := true
		for _, call := range calls {
			if !session.prefetched && ctx.Err() != nil {
				return Outcome{}, ctx.Err()
			}
			// Described before the tool runs, not after: describeCall
			// reports which paths were already shown by consulting the
			// same served map that answering a read_files call updates,
			// so asking afterwards would report every file as a re-read.
			described := runner.describeCall(call)
			// Deliberately not the same words as the line logged below.
			// Setting the status to the text we are about to log meant
			// the transient line and the permanent one said exactly the
			// same thing a millisecond apart, which reads as the tool
			// having run twice — reported as "why did it read twice?"
			progress.Status(activityFor(call.Name))
			toolStarted := time.Now()
			output, detail, ok := session.runTool(ctx, call)
			batchOK = batchOK && ok
			if !session.prefetched && ctx.Err() != nil {
				return Outcome{}, ctx.Err()
			}
			toolSpent := time.Since(toolStarted)
			runner.profile.record(stepFor(call.Name), toolSpent)
			if call.Name == "recall" {
				runner.profile.Recall.Spent += toolSpent
			}
			progress.Log("  " + described + " · " + formatDuration(toolSpent))
			// Under the line that reports the call, not before it: the
			// timing is only known once the tool has run, and the reason
			// an edit was rejected belongs beneath what it was rejecting.
			for _, line := range detail {
				progress.Log("    " + line)
			}
			session.transcript = append(session.transcript, call)
			session.transcript = append(session.transcript, toolOutput{Type: "function_call_output", CallID: call.CallID, Output: output})
		}

		if !session.prefetched && response.cutOff() {
			// Keep every tool result, but an incomplete envelope cannot finish
			// the turn. Accepted edits must not be replayed on continuation.
			session.complete = false
			session.closing = ""
			progress.Log("  completion deferred: max_output_tokens after tool batch")
			session.report("Your last response was cut off. The tool calls above have been processed once; use their results, do not repeat accepted edits, and continue any remaining work before declaring completion.")
			continue
		}

		if !session.prefetched && !session.complete && batchOK &&
			calls[len(calls)-1].Name == "apply_diff" && !session.checkpointUsed && round+1 < maxRounds {
			session.checkpointUsed = true
			started := time.Now()
			status, diagnostic := diagnosticCheckpoint(ctx, runner.repository.Root)
			spent := time.Since(started)
			if ctx.Err() != nil {
				return Outcome{}, ctx.Err()
			}
			if status != "skipped" {
				runner.profile.record(StepTest, spent)
			}
			progress.Log("  diagnostic checkpoint: " + status + " · " + formatDuration(spent))
			session.report(diagnostic)
		}

		if !session.prefetched && !batchOK {
			// The model declared completion before seeing this batch's results.
			// Return every failure for review, even if its final edit landed.
			session.complete = false
			session.closing = ""
		}

		// Normal edits signal completion separately from their optional note.
		// Only the final tool can leave that signal pending; all calls above
		// still run. The small-UI path keeps its closing-note contract.
		if session.complete || (session.prefetched && session.closing != "") {
			result, testSpent := runner.runTests(ctx)
			if !session.prefetched && ctx.Err() != nil {
				return Outcome{}, ctx.Err()
			}
			switch {
			case result.Skipped:
				progress.Log("  no check command detected")
			case !result.Passed:
				progress.Log("  check failed, back to it · " + formatDuration(testSpent))
				for _, line := range firstFailures(result.Output) {
					progress.Log("    " + line)
				}
				session.closing = ""
				session.complete = false
				session.report("The edits applied, but the project's check failed. Fix it.\n" + result.Output)
				continue
			default:
				progress.Log("  check passed · " + formatDuration(testSpent))
			}
			reply := session.closing
			if !session.prefetched {
				if reply == "" {
					reply = "Updated " + strings.Join(session.applied, ", ") + "."
				}
				if result.Skipped {
					reply += "\n\nNo project check was detected."
				}
			}
			return session.outcome(reply), nil
		}
	}
	return Outcome{}, fmt.Errorf("%w after %d rounds", errOutOfRounds, maxRounds)
}

// errOutOfRounds means the conversation never reached a plain reply. It
// is a distinct error because a turn that spent its rounds trying to
// edit is not a failed turn yet: the whole-file fallback can still
// finish the job from what it already knows.
var errOutOfRounds = errors.New("the model did not finish")

// outcome is what the turn amounted to, built from what actually
// happened rather than from what the model said it would do. Applied and
// Files used to be the model's own claim, which had to be audited
// afterwards because a patch could quietly omit a file it promised;
// here they are simply the edits that landed.
func (session *changeSession) outcome(reply string) Outcome {
	return Outcome{
		Reply:    strings.TrimSpace(reply),
		Coding:   len(session.applied) > 0,
		Files:    session.applied,
		Applied:  len(session.applied) > 0,
		Attempts: session.used["apply_diff"],
		Usage:    session.runner.usage,
		Profile:  session.runner.profile,
	}
}

// runTool answers one call, holding each tool to its own budget. The
// budgets are separate because the tools fail differently: a model that
// cannot place a hunk will burn every round retrying apply_diff, where
// one that is exploring reads a few files and stops. A tool that is out
// of budget says so as its result rather than erroring, so the model can
// still finish with what it has instead of the turn dying.
// It returns the tool's result for the model, and any lines worth
// showing the person watching underneath it. The success flag gates normal
// batch completion; the small-UI path does not use it.
func (session *changeSession) runTool(ctx context.Context, call responseItem) (string, []string, bool) {
	if !session.prefetched {
		// A later read, rejected edit or refused call cannot inherit an
		// earlier edit's declaration that the whole task is complete.
		session.complete = false
		session.closing = ""
		if err := ctx.Err(); err != nil {
			return "ERROR: " + err.Error(), nil, false
		}
	}
	budget := call.Name
	if call.Name == "replace_text" && session.prefetched {
		budget = "apply_diff"
	}
	quota, known := toolQuota[budget]
	if !known {
		return "ERROR: unknown tool " + call.Name, nil, false
	}
	if session.used[budget] >= quota {
		return fmt.Sprintf("ERROR: %s has been used %d times, which is the limit for one turn. "+
				"Work with what you already have, and say what you were unable to do.", call.Name, quota),
			[]string{fmt.Sprintf("out of %s calls for this turn", call.Name)}, false
	}
	session.used[budget]++
	if call.Name == "replace_text" {
		output, detail := session.replaceText(call)
		// This tool is small-UI only; the batch-completion guard is unused.
		return output, detail, false
	}

	if call.Name == "apply_diff" {
		return session.applyDiff(call)
	}
	session.readPaths = append(session.readPaths, readFilePaths(call)...)
	output, ok := session.runner.runTool(ctx, call)
	return output, nil, ok
}

// applyDiff places one patch and reports what happened in the terms the
// next round needs.
//
// A failure carries the current contents of the files it touched when
// they are not the ones the model was last shown — which happens once an
// earlier apply_diff has landed and moved the ground under this one.
// Without that the model has to spend a whole round asking to read a
// file again just to see what it already changed.
func (session *changeSession) applyDiff(call responseItem) (string, []string, bool) {
	var arguments toolArguments
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return "ERROR: invalid tool arguments: " + err.Error(), nil, false
	}
	var completion struct {
		Complete *bool `json:"task_complete"`
	}
	if !session.prefetched {
		// Decode the new field only here: other tools and small-UI patches
		// retain their existing treatment of unknown arguments.
		if err := json.Unmarshal([]byte(call.Arguments), &completion); err != nil || completion.Complete == nil {
			return "ERROR: task_complete must be a boolean: true only when this edit completes the entire request, false while work remains. Nothing was changed.", nil, false
		}
	}
	patch := strings.TrimSpace(arguments.Patch)
	if patch == "" {
		return "ERROR: no patch was given.", []string{"no patch was given"}, false
	}
	runner := session.runner
	started := time.Now()
	err := runner.repository.Apply(patch)
	runner.profile.record(StepPatch, time.Since(started))
	if err != nil {
		var failure *repo.PatchError
		if errors.As(err, &failure) {
			for _, one := range failure.Failures {
				session.attempted = appendUnique(session.attempted, one.Path)
			}
		}
		session.lastFailure = err
		session.recordFailure(patch, err)
		message := "The patch did not apply and nothing was changed."
		if !session.prefetched {
			message = "The patch failed."
			// Only the direct content-placement error proves this call
			// stopped before writing. A wrapped Git or I/O error may not.
			if placement, staged := err.(*repo.PatchError); staged {
				message = "The patch did not apply. Nothing was changed by this call; earlier accepted edits were not rolled back."
				message += session.editBudgetFeedback()
				message += rejectedPatchPaths(patch, placement)
			}
		}
		return message + "\n\n" + err.Error() + session.staleContents(err),
			repo.Explain(err), false
	}
	changed := repo.PatchPaths(patch)
	// Recorded only now, after the edit actually landed: a note left on a
	// patch that could not be placed would end the turn on a promise.
	// Said plainly, because the alternative is what happened on the first
	// real run: the model applied an edit, was told only "Applied", and
	// spent three further round trips reading the files back to see
	// whether it had worked.
	output := session.appliedEdit(changed, arguments.Comment)
	if !session.prefetched {
		session.complete = *completion.Complete
		output += session.editBudgetFeedback()
	}
	return output, nil, true
}

func (session *changeSession) editBudgetFeedback() string {
	return fmt.Sprintf("\nRemaining edit calls this turn: %d. Rejected calls also use the edit budget.",
		max(0, toolQuota["apply_diff"]-session.used["apply_diff"]))
}

// recordFailure writes a rejected patch where it can be read back later.
//
// This is the one thing worth keeping from a turn that went wrong, and
// until now nothing kept it: the session log holds that a patch failed
// and how many times, never the patch or what the rejection said. So
// "why does this folder need a second attempt so often?" could only be
// answered by counting, which says there is a problem and nothing about
// what it is.
//
// Only failures are recorded. A turn that lands first time has already
// told us everything by landing.
func (session *changeSession) recordFailure(patch string, err error) {
	debug.Record("patch_rejected", map[string]any{
		"folder":  session.runner.repository.Root,
		"task":    session.task,
		"attempt": session.used["apply_diff"],
		"reasons": repo.Explain(err),
		"paths":   repo.PatchPaths(patch),
		"patch":   patch,
		"detail":  err.Error(),
	})
}

// staleContents are the current contents of the files a failed patch
// touched, for the ones the model has not been shown as they now stand.
func (session *changeSession) staleContents(err error) string {
	var failure *repo.PatchError
	if !errors.As(err, &failure) {
		return ""
	}
	var stale []string
	for _, one := range failure.Failures {
		if one.Path != "" && !session.runner.alreadyShown(one.Path) {
			stale = appendUnique(stale, one.Path)
		}
	}
	if len(stale) == 0 {
		return ""
	}
	text, readErr := session.runner.readFiles(stale)
	if readErr != nil {
		return ""
	}
	return "\n\nTHESE FILES AS THEY NOW STAND — match your context lines against this, not against what you saw earlier:\n" + text
}

// rewriteInstead is the last resort: the model spent its budget without
// landing an edit, so the files it was trying to patch are asked for
// whole instead of in pieces.
func (runner *Runner) rewriteInstead(ctx context.Context, task string, session *changeSession, mapped []string, images []string, progress Progress) (Outcome, error) {
	targets := rewriteTargets(session.attempted, session.readPaths, mapped)
	if len(targets) == 0 {
		return Outcome{Profile: runner.profile}, fmt.Errorf("no edit would apply and there was no file to rewrite")
	}
	evidence := "Your edits could not be placed."
	if session.lastFailure != nil {
		evidence = session.lastFailure.Error()
	}
	progress.Status("Rewriting the file instead...")
	summary := ""
	for _, path := range targets {
		current, _ := runner.repository.ReadFile(path)
		started := time.Now()
		rewrite, err := runner.rewrite(ctx, task, path, current, evidence, images)
		spent := time.Since(started)
		runner.profile.record(StepRewrite, spent)
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
		progress.Log("  wrote " + path + " · " + formatDuration(spent))
		if summary == "" {
			summary = rewrite.Summary
		}
	}
	progress.Status("Testing the change...")
	result, testSpent := runner.runTests(ctx)
	if !result.Passed {
		return Outcome{Profile: runner.profile}, fmt.Errorf("rewrote %s, but the check failed:\n%s", strings.Join(targets, ", "), result.Output)
	}
	if result.Skipped {
		progress.Log("  no check command detected")
	} else {
		progress.Log("  check passed · " + formatDuration(testSpent))
	}
	if summary == "" {
		summary = "Rewrote " + strings.Join(targets, ", ")
	}
	return Outcome{Reply: summary, Coding: true, Files: targets, Applied: true, Attempts: session.used["apply_diff"], Rewrote: true, Usage: runner.usage, Profile: runner.profile}, nil
}

func appendUnique(list []string, value string) []string {
	if value == "" {
		return list
	}
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// maxAside bounds what is carried forward. Measured over a real turn the
// asides came to about 176 tokens in total against transcripts of eleven
// thousand — under two per cent, and in the cached prefix at that. The
// cap is insurance against a model that decides to think out loud at
// length every round, not a budget anyone is expected to reach.
const maxAside = 800

// asideOf is whatever the model said beside its tool calls, or empty.
// text() reports an error when a reply carried no message at all, which
// is the ordinary case here rather than a fault.
func asideOf(response responseEnvelope) string {
	text, err := response.text()
	if err != nil {
		return ""
	}
	return text
}

// asideLines are the first couple of lines of an aside, for the trail.
// The model gets all of it; a person watching wants the gist without a
// paragraph landing in the middle of the progress log.
func asideLines(aside string) []string {
	var lines []string
	for _, line := range strings.Split(aside, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		lines = append(lines, clip(line, 110))
		if len(lines) == 2 {
			break
		}
	}
	return lines
}

// firstFailures are the few lines of check output most likely to say
// what broke, for the trail. The model gets the whole thing either way;
// this is so the person watching does not have to turn on debug logging
// to find out whether it was one type error or forty.
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
	runner *Runner
	// task is kept only so a failure record says what was being asked
	// when a patch was rejected. Nothing reads it during the turn.
	task   string
	writer *Client
	// A transient HTTP replay is shared across every coding round in this turn.
	transientRetryUsed bool
	checkpointUsed     bool // One diagnostic on an already-required continuation.
	// prefetched enables the bounded small-UI path only. Normal tasks can
	// receive initial context without opting into its writer or edit tools.
	prefetched bool
	transcript []any
	readPaths  []string
	// applied are the files edits actually landed in, attempted the ones
	// an edit was tried on and failed. Both are recorded as they happen,
	// so the turn's outcome is what the repository says rather than what
	// the model claimed it would do.
	applied     []string
	attempted   []string
	lastFailure error
	// closing is the note the model left with an edit it called its last.
	// Set only by an edit that actually applied, so a failed one cannot
	// end the turn on a promise.
	closing string
	// complete is set only by an accepted normal edit, until another tool
	// runs or the project check fails. It does not depend on the note.
	complete bool
	// used counts calls per tool, against toolQuota.
	used map[string]int
}

// maxRounds bounds the whole conversation; toolQuota bounds each tool
// within it. Separate budgets because the tools go wrong differently: a
// model that cannot place a hunk will spend every round retrying
// apply_diff, where one that is merely exploring reads a few files and
// stops. One total ceiling would let the first starve the second.
const maxRounds = 20

var toolQuota = map[string]int{
	"read_files": 8,
	"search":     5,
	"apply_diff": 4,
	"recall":     1,
}

// activityFor is what to show on the status line while a tool runs: what
// is happening, where the log line that follows says what happened.
func activityFor(tool string) string {
	switch tool {
	case "read_files":
		return "Reading the files..."
	case "search":
		return "Searching the folder..."
	case "apply_diff", "replace_text":
		return "Applying the edit..."
	case "recall":
		return "Reading earlier turns..."
	default:
		return "Working..."
	}
}

// stepFor is the profile step a tool's time belongs to. Recall is kept
// apart from the other tools because whether a turn reached for history
// at all is the measurement, and it is lost the moment it is averaged in
// with reading files.
func stepFor(tool string) Step {
	switch tool {
	case "apply_diff", "replace_text":
		return StepPatch
	case "recall":
		return StepRecall
	default:
		return StepTools
	}
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
	// Beside the map, because it is the same kind of thing — what the
	// folder is, rather than what this message wants — and because both
	// stay put while the task changes, which is what the prefix cache
	// needs. Recorded as shown so a later read_files answers with a
	// sentence instead of another copy.
	if path, text := runner.repository.Architecture(); text != "" {
		fmt.Fprintf(&opening, "%s — the project's own account of how it fits together, "+
			"already read for you:\n%s\n\n", path, text)
		runner.served[path] = text
	}
	recent, _ := runner.recentTurns()
	if len(recent) > 0 {
		opening.WriteString("EARLIER IN THIS FOLDER:\n" + renderHistory(recent) + "\n")
	}
	if beyond := len(runner.recallable()); beyond > 0 && runner.recall {
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
		task:       task,
		transcript: []any{inputMessage{Role: "user", Content: opening.String(), Images: images}},
		used:       map[string]int{},
	}
}

// report adds what reality said about the last attempt, so the next one
// answers it rather than repeating itself.
func (session *changeSession) report(evidence string) {
	session.transcript = append(session.transcript, inputMessage{Role: "user", Content: evidence})
}

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
// named no file at all, and the file it read (or the single file there
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

// filesBehind are the contents of the files a search just matched in,
// appended to its own output.
//
// Searching and then reading what was found is two round trips for one
// intention, and the second one is pure latency: the model has the paths
// already, it just has no way to say "and those" without another call.
// A probe of the provider showed it will happily emit read_files and
// search together in one response — so the parallel case already costs
// nothing — but it cannot read what it has not yet searched for. That
// sequence is the one still worth collapsing.
//
// Only when the search actually narrowed something. A query matching
// nine files was exploratory and the model should pick from the list;
// the files it wants get read next round either way, and sending all
// nine costs more than the trip it saves. Files already shown are left
// out, as everywhere else, because every copy is resent on every round
// that follows.
func (runner *Runner) filesBehind(output string) string {
	paths := searchPaths(output)
	var fresh []string
	for _, path := range paths {
		if !runner.alreadyShown(path) {
			fresh = append(fresh, path)
		}
	}
	if len(fresh) == 0 || len(fresh) > maxSearchFiles {
		return ""
	}
	text, err := runner.readFiles(fresh)
	if err != nil {
		// Too large to be worth it, unreadable, whatever it was: the
		// model still has the match list and reads what it wants.
		return ""
	}
	return "\n\nThose matches are in " + strconv.Itoa(len(fresh)) + " " +
		plural(len(fresh), "file", "files") + ", so here " +
		plural(len(fresh), "it is", "they are") + " in full. " +
		"Do not call read_files for " + plural(len(fresh), "it", "them") + ".\n\n" + text
}

// maxSearchFiles is how many files a search may hand back with it. Past
// this the search did not narrow anything and the list is the answer.
const maxSearchFiles = 4

// searchPaths are the distinct files named in ripgrep output, in the
// order they first appear. Lines are "path:line:text"; anything without
// that shape (a truncation notice, "No matches.") is skipped.
func searchPaths(output string) []string {
	var paths []string
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		path, rest, found := strings.Cut(line, ":")
		if !found || path == "" || strings.HasPrefix(path, " ") {
			continue
		}
		// The second field is the line number. Without it this is prose,
		// not a match.
		if number, _, ok := strings.Cut(rest, ":"); !ok {
			continue
		} else if _, err := strconv.Atoi(number); err != nil {
			continue
		}
		path = strings.TrimPrefix(path, "./")
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

// pathsMatching is the files a read should answer with: the ones asked
// for by name, plus the ones whose contents match the pattern.
//
// Searching and then reading what was found is two round trips for one
// intention. Handing the files back with a search fixes half of it, but
// only half: a model that also wants three files it already knew about
// still spends a round asking, so the trip only disappears when both
// can be named at once. This is that — the paths it knows and the
// pattern for what it does not, in one call.
//
// It never fails. A pattern matching half the repository comes back as
// the list of names with the explicit paths read, which is exactly what
// a search would have given — no worse than before, and the model picks
// from it.
func (runner *Runner) pathsMatching(ctx context.Context, asked []string, pattern string) ([]string, string) {
	chosen := append([]string{}, asked...)
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return chosen, ""
	}
	found, err := runner.repository.Search(ctx, pattern)
	if err != nil {
		return chosen, "That pattern could not be searched for (" + err.Error() + "), so only the named files are below.\n\n"
	}
	matched := searchPaths(found)
	if len(matched) == 0 {
		return chosen, "Nothing matched " + strconv.Quote(pattern) + ".\n\n"
	}
	var extra []string
	for _, path := range matched {
		if !contains(chosen, path) && !runner.alreadyShown(path) {
			extra = append(extra, path)
		}
	}
	// Room is what read_files will take at once, minus what was named.
	room := repo.MaxReadFiles - len(chosen)
	if room < 0 {
		room = 0
	}
	if len(extra) > room {
		return chosen, "That pattern matched " + strconv.Itoa(len(matched)) +
			" files, too many to read at once. Here is where it matched; ask again for the ones you want.\n\n" +
			found + "\n\n"
	}
	chosen = append(chosen, extra...)
	if len(extra) == 0 {
		return chosen, ""
	}
	return chosen, "Also read, for matching " + strconv.Quote(pattern) + ": " + strings.Join(extra, ", ") + "\n\n"
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
	older := runner.recallable()
	if len(older) == 0 {
		return "Nothing came before the turns you were already shown."
	}
	if runner.told {
		return "(already recalled above)"
	}
	runner.told = true
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
		if pattern := strings.TrimSpace(arguments.Matching); pattern != "" && len(arguments.Paths) == 0 {
			return "read files matching " + strconv.Quote(pattern)
		} else if pattern != "" {
			return "read " + strings.Join(arguments.Paths, ", ") + " and files matching " + strconv.Quote(pattern)
		}
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
	case "apply_diff":
		if paths := repo.PatchPaths(arguments.Patch); len(paths) > 0 {
			return "edit " + strings.Join(paths, ", ")
		}
		return "apply an edit"
	case "replace_text":
		return "replace text in " + strings.Join(replacementPaths(call), ", ")
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

func (runner *Runner) runTool(ctx context.Context, call responseItem) (string, bool) {
	var arguments toolArguments
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return "ERROR: invalid tool arguments: " + err.Error(), false
	}
	switch call.Name {
	case "read_files":
		paths, note := runner.pathsMatching(ctx, arguments.Paths, arguments.Matching)
		if len(paths) == 0 {
			return note + "Nothing to read: no paths were given and nothing matched.", true
		}
		output, err := runner.readFiles(paths)
		if err != nil {
			return "ERROR: " + err.Error(), false
		}
		return note + output, true
	case "search":
		output, err := runner.repository.Search(ctx, arguments.Query)
		if err != nil {
			return "ERROR: " + err.Error(), false
		}
		return output + runner.filesBehind(output), true
	case "recall":
		return runner.recallEarlier(), true
	default:
		return "ERROR: unknown tool " + call.Name, false
	}
}

func repositoryTools(recall bool) []functionTool {
	tools := []functionTool{
		{Type: "function", Name: "read_files", Description: "Read repository files. Give the paths you know from the map, and optionally a regular expression in matching to also read every file whose contents match it — so you do not have to search first and then read what the search found.", Strict: true, Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"paths":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 12},
				"matching": map[string]any{"type": "string"},
			}, "required": []string{"paths", "matching"}, "additionalProperties": false,
		}},
		{Type: "function", Name: "search", Description: "Search repository text with ripgrep when the map and files are insufficient.", Strict: true, Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false,
		}},
		// patch is named first, and required first, so a long comment
		// cannot spend the output room the patch needs — which is the
		// failure a summary field written ahead of a diff used to cause.
		{Type: "function", Name: "apply_diff", Description: "Apply edits to the repository, as a patch in the compact format. Nothing is written unless every edit in it can be placed. On your last edit, put your closing note to the user in response_comment_for_user and the turn ends there; leave it empty while you still have work to do.", Strict: true, Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"patch":                     map[string]any{"type": "string"},
				"response_comment_for_user": map[string]any{"type": "string"},
			},
			"required": []string{"patch", "response_comment_for_user"}, "additionalProperties": false,
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
a question about the project, or ordinary conversation, and deciding which is part of your job.

For a completed change, set task_complete=true on your final apply_diff call. The application
runs the project's check before ending the turn; failures return here for repair. Put a short,
factual closing note in response_comment_for_user, or leave it empty for a local change summary.
Do not claim checks passed before they run. Leave task_complete=false while any requested work
remains. Do not make a separate call just to request the check or say "done".
For questions, conversation or work you cannot complete, reply in plain markdown with no tool
call. Answer what was actually asked: the real colors, values or structure for a question,
an ordinary reply for conversation, and an honest account of any incomplete work.
Do not describe a change you have not made — an edit only exists once apply_diff has accepted
it. If you could not do something, say so plainly.

A message that needs nothing from the repository — a greeting, a thank-you, a remark, a
question you can already answer — gets that reply straight away, with no tool call at all.
Do not go looking through the files for a message that did not ask you to.

TOOLS

read_files — read only files whose current contents are needed to implement or verify the
  request. Reuse source already supplied by read_files, including initial context. Batch
  those needed reads in one call; appearing in the map or search results alone is not a
  reason to read a file. Resolve concrete dependencies and project rules when needed,
  and read a file before editing it.
  matching — optionally name a regular expression alongside explicit paths to read the
  matching files in the same call. Leave it empty when paths are already known.
search — for when the map is not enough to find something. Narrow searches also return
  matching files in full; reuse those contents instead of reading them again.
apply_diff — make the edit. Read a file before changing it; never write an edit against
  contents you have not seen. When it says the patch applied, it applied: every edit was
  placed and the file contains it. Do not read the file back to check.
  task_complete — a required boolean. Set true only when this edit implements the entire
  request and is your final tool call in the response. Otherwise set false. A closing note
  alone does not end the turn; a rejected edit cannot declare completion.
  response_comment_for_user — a short, factual note of what changed and any limitations.
  It may be empty. Completion depends on task_complete and the automatic project check.
recall, when it is offered — the turns before the few already above, for a message that
  reaches back further than they go.

Everything you have already read stays in this conversation. Do not read a file a second time
or search for text you have been shown; scroll up and use it. Each tool has a limited number
of uses per turn, so spend them on what the task needs.

Once the needed context is available, prefer one patch that completes the whole request,
with task_complete=true so the automatic check runs immediately. Do not split already-known
work by file or layer merely to await an acknowledgement. Use an intermediate edit only
when further evidence or output-size limits require it; keep task_complete=false whenever
requested work remains.

REQUEST CONTRACT

For a coding task, track the concrete requirements in the request and relevant project
context: behaviour, data, validation, exact names and UI semantics. Before your final edit,
compare its resulting contents with those requirements using the files and accepted edits
already in this conversation. Include any missing implementation in that edit; do not add
unrequested features or a separate round trip just to announce this review.

Write source literals for their target language; tool JSON encoding is a separate layer.
After that encoding is decoded, the source must contain exactly the intended characters;
do not automatically unescape source text. Preserve exact requested stored and API values,
including case. Display labels and formatting do not authorize changing those values.

Ground schema and compatibility changes in the request and observed project evidence.
Preserve existing records, fields and values except for changes the user requests.
Add migrations when the observed schema or requested change requires them; do not invent
legacy schemas, units or conversions.
Do not destructively rebuild or drop stored data merely to fit a preferred layout. If
necessary schema evidence is missing, obtain it within the available tools or disclose
the gap instead of guessing.

For UI work, visible text and accessible names are distinct requirements. Give a requested
named control, table or region its own semantic name using native HTML associations or
aria-label/aria-labelledby. A nearby heading, placeholder or labelled wrapper does not
automatically name the element itself. Associate form labels with their controls, and name
tables with a caption or explicit accessible label when the request specifies a table name.

The automatic project check catches only what that project's check covers; a successful
typecheck does not establish that the requested behaviour or accessibility is complete.
Keep the closing note factual and disclose any unmet requirement or unverified behaviour.

THE PATCH FORMAT

The patch field of apply_diff contains only a patch in this format.

@path            modify this file
@+path           create this file; every following line is its literal content

For modifications:

 context before
-old line
+new line
 context after

Removed lines can anchor a replacement without unchanged context. For an insertion with no
removed lines in an existing nonempty file, include unchanged context prefixed with a space
that matches exactly and identifies the insertion point uniquely. Prefer a short anchor
that appears once. Use @+ only for a new file, with literal content, not added-line prefixes.
To replace an existing file, remove its current contents with - lines before adding the new
contents with + lines. A blank line separates one edit from the next, and so does a bare @@ if that is
what comes naturally. No line numbers and no counts — edits are placed by matching your text
against the file, so none of that is read.

Two edits in one file, which is the common case:

@src/app.py
 def start():
-    server.run(config)
+    server.run(config, debug=True)
     return server
@@
-PORT = 8000
+PORT = 8080

And a second file, under its own header:

@src/config.py
-DEBUG = False
+DEBUG = True

Preserve whitespace exactly. Copy every context line and every removed line from the file
character for character, including indentation, escapes and HTML entities such as &amp;. A
line that differs by even one character cannot be found, and nothing in the patch is written
unless every edit in it can be placed.

Long lines: copy the whole line. All of it, to the end. Do not stop halfway. Do not write "..."
or leave the rest off. A cut line matches nothing in the file, so the edit is thrown away and
you write it again. A 300-character line of JSX is still one line. Copy all 300.

Make the smallest complete change, and include tests when the repository already has them.
If an edit is rejected, the reply tells you which lines could not be found and what is in the
file instead: fix every one it lists, not just the first, and do not send the same patch again.
`

const rewriteInstructions = `You are the repair phase of a small coding agent.
Your edits could not be placed in the file, so supply the whole file instead.
Return the named file's complete new contents in the content field, copied
from the supplied current contents with only the required edit made.
Never abbreviate, summarize, or elide any part of the file with comments
like "unchanged" or "...": what you return replaces the file exactly, so
anything you leave out is deleted.
Respond with only the JSON object, no other text before or after it.`
