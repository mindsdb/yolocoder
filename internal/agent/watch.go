package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mindsdb/yolocoder/internal/shell"
)

// watchVerdict is what the model makes of a command still running.
type watchVerdict struct {
	// Status is "fine", "stuck" or "failing".
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// watcher asks the model, while a generated command is still running,
// whether it is going the way it was supposed to.
//
// This exists because a script is agreed to as a whole and then runs
// unattended. A single command either works or does not, and the exit
// status says which; a script can install three things, silently pick the
// wrong Python, and sit at a prompt nobody is watching. The exit status
// arrives far too late to be the only thing checking.
type watcher struct {
	runner   *Runner
	progress Progress
}

// Check implements shell.Supervisor. Anything short of a clear verdict
// that something is wrong lets the command carry on: interrupting work
// that was going fine is the more expensive mistake, and the user can
// always interrupt it themselves.
func (watch *watcher) Check(ctx context.Context, report shell.Report) error {
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	response, err := watch.runner.client.create(callCtx, responseRequest{
		Instructions: watchInstructions,
		Input:        renderReport(report),
		Text:         strictSchema("command_watch", watchSchema()),
	})
	if err != nil {
		// The command is running and this was only a second opinion.
		// Failing to get one is not a reason to kill it.
		watch.log("could not check on it: " + err.Error())
		return nil
	}
	text, err := response.text()
	if err != nil {
		watch.log("could not check on it: " + err.Error())
		return nil
	}
	var verdict watchVerdict
	if err := decodeJSON(text, &verdict); err != nil {
		watch.log("could not read the check: " + err.Error())
		return nil
	}

	reason := strings.TrimSpace(verdict.Reason)
	switch strings.ToLower(strings.TrimSpace(verdict.Status)) {
	case "stuck":
		return fmt.Errorf("stopped it: it looks stuck. %s", reason)
	case "failing":
		return fmt.Errorf("stopped it: it looks to be failing. %s", reason)
	default:
		if reason != "" {
			watch.log("looks fine: " + reason)
		}
		return nil
	}
}

func (watch *watcher) log(message string) {
	if watch.progress != nil {
		watch.progress.Log("  " + message)
	}
}

// renderReport lays out what the command has done so far. The output goes
// last: it is the part that grows between checks, and putting it after
// the fixed preamble is what lets the earlier bytes stay cached.
func renderReport(report shell.Report) string {
	var text strings.Builder
	fmt.Fprintf(&text, "THE COMMAND THAT IS RUNNING:\n%s\n\n", strings.TrimSpace(report.Script))
	fmt.Fprintf(&text, "It has been running for %s", report.Elapsed.Round(time.Second))
	if report.Idle > time.Second {
		fmt.Fprintf(&text, " and has printed nothing for %s", report.Idle.Round(time.Second))
	}
	fmt.Fprintf(&text, ". This is check %d.\n\nWHAT IT HAS PRINTED SO FAR:\n", report.Check)
	if output := strings.TrimSpace(report.Output); output != "" {
		text.WriteString(output)
	} else {
		text.WriteString("(nothing yet)")
	}
	return text.String()
}

func watchSchema() map[string]any {
	return objectSchema(map[string]any{
		"status": map[string]any{"type": "string", "enum": []string{"fine", "stuck", "failing"}},
		"reason": map[string]any{"type": "string"},
	}, []string{"status", "reason"})
}

const watchInstructions = `A command is running and you are being shown what it has printed so far.
Say whether it is going to plan.

"fine" — it is doing what it was meant to do, including when that means taking a long time.
Compiling, downloading, installing, running tests and serving are all slow on purpose. A server
or a watcher that starts and then stays quiet has succeeded; it is not stuck.
"stuck" — it is waiting for something it will never get: a prompt with nobody to answer it, a
credential it does not have, a lock or a port it is going to keep waiting on.
"failing" — the output shows it has gone wrong and continuing will not fix it.

Prefer "fine" when it is a close call. Stopping work that was going to succeed costs more than
letting a doomed command run a little longer, and the person watching can always interrupt it.
Silence on its own is not evidence: plenty of commands print nothing for minutes.
In reason, give one short sentence citing what in the output you are going by.
Respond with only the JSON object, no other text before or after it.`
