package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mindsdb/yolocoder/internal/repo"
)

const maxToolRounds = 8

type Plan struct {
	Summary       string   `json:"summary"`
	FilesToModify []string `json:"files_to_modify"`
	ContextFiles  []string `json:"context_files"`
	Steps         []string `json:"steps"`
}

type Patch struct {
	Summary string `json:"summary"`
	Diff    string `json:"diff"`
}

type routeDecision struct {
	CodingTask bool   `json:"coding_task"`
	Reply      string `json:"reply"`
}

// planResult is what the planning phase learned: the plan itself, the
// tool output gathered along the way, and the files the model actually
// read. The read paths matter because a weak model often returns a plan
// with an empty files_to_modify, and the files it opened are then the
// best evidence of what it meant to change.
type planResult struct {
	Plan      Plan
	Context   string
	ReadPaths []string
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
	if err := json.Unmarshal([]byte(text), target); err == nil {
		return nil
	}
	if span := jsonSpan(text); span != "" {
		if err := json.Unmarshal([]byte(span), target); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no valid JSON found in response: %s", strings.TrimSpace(text))
}

func jsonSpan(text string) string {
	for _, delimiters := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		start := strings.IndexByte(text, delimiters[0])
		end := strings.LastIndexByte(text, delimiters[1])
		if start != -1 && end > start {
			return text[start : end+1]
		}
	}
	return ""
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
}

func NewRunner(client *Client, repository *repo.Repository) *Runner {
	return &Runner{client: client, repository: repository}
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

	progress.Status("Finding the right code...")
	planned, err := runner.plan(ctx, task, repoMap, progress)
	if err != nil {
		return "", err
	}
	plan := planned.Plan
	logSummary(progress, "plan", plan.Summary)
	for _, path := range plan.FilesToModify {
		progress.Log("  will edit " + path)
	}
	contextText := planned.Context
	plannedContext, err := runner.readPlanFiles(plan)
	if err != nil {
		return "", err
	}
	contextText += "\nPLANNED FILES:\n" + plannedContext

	var evidence string
	var patch Patch
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			progress.Status("Repairing from new evidence...")
			contextText, err = runner.readPlanFiles(plan)
			if err != nil {
				return "", err
			}
		} else {
			progress.Status("Writing the change...")
		}
		patch, err = runner.patch(ctx, task, repoMap, plan, contextText, evidence)
		if err != nil {
			return "", err
		}
		logSummary(progress, "patch", patch.Summary)

		progress.Status("Applying the patch...")
		if err := runner.repository.Apply(patch.Diff); err != nil {
			progress.Log("  patch did not apply, retrying")
			// Feed back the diff that failed alongside git's complaint.
			// Without seeing its own output the model has no way to tell
			// what was wrong with it and tends to reproduce it verbatim.
			evidence = fmt.Sprintf(
				"The patch did not apply. Git looks for the context and removed lines exactly as written, "+
					"so any difference from the real file (an HTML entity spelled out, a changed attribute, "+
					"reflowed whitespace) makes the whole hunk fail. Compare git's \"while searching for\" text "+
					"below against the real contents above and copy those lines character for character.\n\n"+
					"GIT REPORTED:\n%s\n\nTHE DIFF THAT FAILED:\n%s",
				err.Error(), patch.Diff)
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
			return patch.Summary, nil
		}
		progress.Log("  tests failed, retrying")
		evidence = "The patch applied, but tests failed. Produce an incremental diff against the current repository.\n" + testResult.Output
	}

	// Every diff was rejected. A diff only applies when its context and
	// removed lines match the file exactly, which a model-written one
	// often gets subtly wrong, so fall back to writing whole files.
	if strings.HasPrefix(evidence, "The patch did not apply") {
		targets := rewriteTargets(plan, planned.ReadPaths, mapped)
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

// plan drives the tool-calling loop by resending the full conversation
// transcript on every round rather than relying on previous_response_id.
// Not every OpenAI-compatible provider actually persists server-side
// response state, and one that doesn't will reject a function_call_output
// referencing a call_id it never stored, so the transcript is tracked here
// instead.
func (runner *Runner) plan(ctx context.Context, task, repoMap string, progress Progress) (planResult, error) {
	taskText := fmt.Sprintf("TASK:\n%s\n\nREPOSITORY MAP:\n%s", task, repoMap)
	var transcript []any
	var collected strings.Builder
	var readPaths []string
	for round := 0; round < maxToolRounds; round++ {
		var input any = taskText
		if transcript != nil {
			input = transcript
		}
		request := responseRequest{
			Instructions: planningInstructions,
			Input:        input,
			Tools:        repositoryTools(),
			ToolChoice:   "auto",
			Text:         strictSchema("implementation_plan", planSchema()),
		}
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		response, err := runner.client.create(callCtx, request)
		cancel()
		if err != nil {
			return planResult{}, err
		}
		calls := response.calls()
		if len(calls) == 0 {
			text, err := response.text()
			if err != nil {
				return planResult{}, err
			}
			var plan Plan
			if err := decodeJSON(text, &plan); err != nil {
				return planResult{}, fmt.Errorf("decode implementation plan: %w", err)
			}
			return planResult{Plan: plan, Context: collected.String(), ReadPaths: readPaths}, nil
		}
		if transcript == nil {
			transcript = []any{inputMessage{Role: "user", Content: taskText}}
		}
		for _, call := range calls {
			progress.Log("  " + describeCall(call))
			readPaths = append(readPaths, readFilePaths(call)...)
			output := runner.runTool(ctx, call)
			fmt.Fprintf(&collected, "\nTOOL %s:\n%s\n", call.Name, output)
			transcript = append(transcript, call)
			transcript = append(transcript, toolOutput{Type: "function_call_output", CallID: call.CallID, Output: output})
		}
	}
	return planResult{}, fmt.Errorf("the model exceeded %d repository tool rounds", maxToolRounds)
}

func (runner *Runner) patch(ctx context.Context, task, repoMap string, plan Plan, contextText, evidence string) (Patch, error) {
	planJSON, _ := json.Marshal(plan)
	input := fmt.Sprintf("TASK:\n%s\n\nREPOSITORY MAP:\n%s\n\nPLAN:\n%s\n\nRELEVANT FILE CONTENTS:\n%s", task, repoMap, planJSON, contextText)
	if evidence != "" {
		input += "\n\nNEW EVIDENCE FROM REALITY:\n" + evidence
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	response, err := runner.client.create(callCtx, responseRequest{
		Instructions: patchInstructions,
		Input:        input,
		Text:         strictSchema("unified_patch", patchSchema()),
	})
	if err != nil {
		return Patch{}, err
	}
	text, err := response.text()
	if err != nil {
		return Patch{}, err
	}
	var patch Patch
	if err := decodeJSON(text, &patch); err != nil {
		return Patch{}, fmt.Errorf("decode patch: %w", err)
	}
	if strings.TrimSpace(patch.Diff) == "" {
		return Patch{}, fmt.Errorf("model returned an empty patch")
	}
	return patch, nil
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
func rewriteTargets(plan Plan, readPaths, mapped []string) []string {
	if len(mapped) != 1 {
		mapped = nil
	}
	seen := map[string]bool{}
	var targets []string
	for _, group := range [][]string{plan.FilesToModify, readPaths, mapped} {
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

// describeCall renders a tool call as a short line for the progress log,
// so the user can see which files the model is actually looking at.
func describeCall(call responseItem) string {
	var arguments toolArguments
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return call.Name
	}
	switch call.Name {
	case "read_files":
		return "read " + strings.Join(arguments.Paths, ", ")
	case "search":
		return "search " + strconv.Quote(arguments.Query)
	default:
		return call.Name
	}
}

// logSummary logs "label: summary", skipping the line entirely when the
// model left the summary empty rather than printing a dangling label.
func logSummary(progress Progress, label, summary string) {
	if summary = strings.TrimSpace(summary); summary != "" {
		progress.Log("  " + label + ": " + summary)
	}
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
		output, err := runner.repository.Read(arguments.Paths)
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

func (runner *Runner) readPlanFiles(plan Plan) (string, error) {
	paths := append([]string{}, plan.FilesToModify...)
	paths = append(paths, plan.ContextFiles...)
	seen := map[string]bool{}
	unique := paths[:0]
	for _, path := range paths {
		if path != "" && !seen[path] && runner.repository.Exists(path) {
			seen[path] = true
			unique = append(unique, path)
		}
	}
	if len(unique) == 0 {
		// The plan named no files, or named files that don't exist yet
		// (a common, valid case for an empty or new project) — either
		// way there's nothing to read, and the patch phase creates them.
		return "No files exist yet; the plan creates new files.", nil
	}
	return runner.repository.Read(unique)
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

func planSchema() map[string]any {
	return objectSchema(map[string]any{
		"summary":         map[string]any{"type": "string"},
		"files_to_modify": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"context_files":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"steps":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, []string{"summary", "files_to_modify", "context_files", "steps"})
}

func patchSchema() map[string]any {
	return objectSchema(map[string]any{
		"summary": map[string]any{"type": "string"},
		"diff":    map[string]any{"type": "string"},
	}, []string{"summary", "diff"})
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

const planningInstructions = `You are the context and planning phase of a small coding agent.
Start from the repository map. Use read_files for likely files. Use search only when needed.
Get enough context to make the requested change safely, but avoid wandering.
Return a concrete plan. Distinguish files to modify from files needed only as context.
Once you are done with tool calls, respond with only the JSON object, no other text before or after it.`

const patchInstructions = `You are the patch phase of a small coding agent.
Use only the supplied task, map, plan, relevant file contents, and any new test/apply evidence.
Return one git-compatible unified diff in the diff field. Do not use markdown fences.
Make the smallest complete change. Include tests when the repository already has tests.
If repairing a test failure, produce an incremental diff against the current repository state.
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
