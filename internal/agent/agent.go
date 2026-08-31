package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
func (runner *Runner) Run(ctx context.Context, task string, progress func(string)) (string, error) {
	progress("Reading your message...")
	decision, err := runner.route(ctx, task)
	if err != nil {
		return "", err
	}
	if !decision.CodingTask {
		return decision.Reply, nil
	}

	repoMap, err := runner.repository.Map()
	if err != nil {
		return "", err
	}
	progress("Finding the right code...")
	plan, contextText, err := runner.plan(ctx, task, repoMap)
	if err != nil {
		return "", err
	}
	plannedContext, err := runner.readPlanFiles(plan)
	if err != nil {
		return "", err
	}
	contextText += "\nPLANNED FILES:\n" + plannedContext
	progress("Making the change...")
	patch, err := runner.patch(ctx, task, repoMap, plan, contextText, "")
	if err != nil {
		return "", err
	}

	var evidence string
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			progress("Repairing from new evidence...")
			contextText, err = runner.readPlanFiles(plan)
			if err != nil {
				return "", err
			}
			patch, err = runner.patch(ctx, task, repoMap, plan, contextText, evidence)
			if err != nil {
				return "", err
			}
		}
		progress("Applying the patch...")
		if err := runner.repository.Apply(patch.Diff); err != nil {
			evidence = "The patch did not apply:\n" + err.Error()
			continue
		}
		progress("Testing the change...")
		testResult := RunTests(ctx, runner.repository.Root)
		if testResult.Passed {
			progress("Done.")
			return patch.Summary, nil
		}
		evidence = "The patch applied, but tests failed. Produce an incremental diff against the current repository.\n" + testResult.Output
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
func (runner *Runner) plan(ctx context.Context, task, repoMap string) (Plan, string, error) {
	taskText := fmt.Sprintf("TASK:\n%s\n\nREPOSITORY MAP:\n%s", task, repoMap)
	var transcript []any
	var collected strings.Builder
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
			return Plan{}, "", err
		}
		calls := response.calls()
		if len(calls) == 0 {
			text, err := response.text()
			if err != nil {
				return Plan{}, "", err
			}
			var plan Plan
			if err := decodeJSON(text, &plan); err != nil {
				return Plan{}, "", fmt.Errorf("decode implementation plan: %w", err)
			}
			return plan, collected.String(), nil
		}
		if transcript == nil {
			transcript = []any{inputMessage{Role: "user", Content: taskText}}
		}
		for _, call := range calls {
			output := runner.runTool(ctx, call)
			fmt.Fprintf(&collected, "\nTOOL %s:\n%s\n", call.Name, output)
			transcript = append(transcript, call)
			transcript = append(transcript, toolOutput{Type: "function_call_output", CallID: call.CallID, Output: output})
		}
	}
	return Plan{}, "", fmt.Errorf("the model exceeded %d repository tool rounds", maxToolRounds)
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
		if len(paths) == 0 {
			return "", fmt.Errorf("plan selected no files")
		}
		return "No planned files exist yet; the plan creates new files.", nil
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

const routingInstructions = `Decide whether the user's message is a coding task that requires reading or changing files in this project, or just a conversational message.
Set coding_task to true only when the user wants code written, fixed, explained from the files, or otherwise wants the project inspected or changed.
When coding_task is false, put your complete, direct reply to the user in reply. When coding_task is true, leave reply empty.
Respond with only the JSON object, no other text before or after it.`
