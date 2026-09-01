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

type routeDecision struct {
	CodingTask bool   `json:"coding_task"`
	Reply      string `json:"reply"`
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
}

func NewRunner(client *Client, repository *repo.Repository) *Runner {
	return &Runner{client: client, repository: repository, served: map[string]string{}}
}

// Run routes the message first: a plain conversational message gets the
// model's direct reply with the repository never touched, and only an
// actual coding task goes through the map/plan/patch/test loop.
func (runner *Runner) Run(ctx context.Context, task string, progress Progress) (string, error) {
	progress.Status("Reading your message...")
	decision, err := runner.route(ctx, task)
	if err != nil {
		return "", err
	}
	if !decision.CodingTask {
		return decision.Reply, nil
	}

	progress.Status("Mapping the folder...")
	repoMap, err := runner.repository.Map()
	if err != nil {
		return "", err
	}
	mapped := mappedPaths(repoMap)
	progress.Log(fmt.Sprintf("  mapped %d files", len(mapped)))

	progress.Status("Working out the change...")
	session := runner.newChangeSession(task, repoMap)

	var evidence string
	var change Change
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			progress.Status("Repairing from new evidence...")
		}
		change, err = session.produce(ctx, progress)
		if err != nil {
			return "", err
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

		progress.Status("Testing the change...")
		testResult := RunTests(ctx, runner.repository.Root)
		if testResult.Passed {
			if testResult.Skipped {
				progress.Log("  no test command detected")
			} else {
				progress.Log("  tests passed")
			}
			return change.Summary, nil
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
			return "", fmt.Errorf("no diff would apply and the model named no file to rewrite:\n%s", evidence)
		}
		progress.Status("Rewriting the file instead...")
		summary := ""
		for _, path := range targets {
			current, _ := runner.repository.ReadFile(path)
			rewrite, err := runner.rewrite(ctx, task, path, current, evidence)
			if err != nil {
				return "", err
			}
			// An empty rewrite of a file that has content is the model
			// failing, not an instruction to truncate the user's file.
			if strings.TrimSpace(rewrite.Content) == "" && strings.TrimSpace(current) != "" {
				return "", fmt.Errorf("refusing to empty %s: the model returned no content for it", path)
			}
			if err := runner.repository.Write(path, rewrite.Content); err != nil {
				return "", err
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
			return summary, nil
		}
		return "", fmt.Errorf("rewrote %s, but tests failed:\n%s", strings.Join(targets, ", "), testResult.Output)
	}
	return "", fmt.Errorf("could not complete the task after repair attempts:\n%s", evidence)
}

func (runner *Runner) route(ctx context.Context, task string) (routeDecision, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	response, err := runner.client.create(callCtx, responseRequest{
		Instructions: routingInstructions,
		Input:        task,
		Text:         strictSchema("message_route", routeSchema()),
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

func (runner *Runner) newChangeSession(task, repoMap string) *changeSession {
	opening := fmt.Sprintf("TASK:\n%s\n\nREPOSITORY MAP:\n%s", task, repoMap)
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

func routeSchema() map[string]any {
	return objectSchema(map[string]any{
		"coding_task": map[string]any{"type": "boolean"},
		"reply":       map[string]any{"type": "string"},
	}, []string{"coding_task", "reply"})
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

const routingInstructions = `Decide whether the user's message is a coding task that requires reading or changing files in this project, or just a conversational message.
Set coding_task to true only when the user wants code written, fixed, explained from the files, or otherwise wants the project inspected or changed.
When coding_task is false, put your complete, direct reply to the user in reply. When coding_task is true, leave reply empty.
Respond with only the JSON object, no other text before or after it.`
