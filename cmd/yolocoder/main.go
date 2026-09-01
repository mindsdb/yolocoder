package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mindsdb/yolocoder/internal/agent"
	"github.com/mindsdb/yolocoder/internal/app"
	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/debug"
	"github.com/mindsdb/yolocoder/internal/session"
	"github.com/mindsdb/yolocoder/internal/terminal"
	"github.com/mindsdb/yolocoder/internal/ui"
	"github.com/mindsdb/yolocoder/internal/update"
	"github.com/mindsdb/yolocoder/internal/version"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "update" {
		var latest string
		var updated bool
		var err error
		ui.WithRobot(os.Stdout, "Checking for updates...", func(status ui.RobotStatus) {
			latest, updated, err = update.CheckNow(version.Commit, status)
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if updated {
			fmt.Printf("YoloCoder updated to %s. Run it again to use the new version.\n", latest)
		} else {
			fmt.Printf("YoloCoder is current at %s.\n", version.Display())
		}
		return
	}

	var updated bool
	ui.WithRobot(os.Stdout, "Starting YoloCoder...", func(status ui.RobotStatus) {
		updated = update.CheckOnLaunch(version.Commit, status)
	})
	if updated {
		// Relaunch replaces the file on disk; without re-executing it,
		// this process would keep running the old code it already
		// loaded until the next separate invocation. If it can't even
		// start the new binary, fall through and keep running on the
		// old one rather than aborting.
		fmt.Println("[^_^] Updated to a new build, restarting...")
		if err := update.Relaunch(); err != nil {
			fmt.Fprintln(os.Stderr, "restart after update:", err)
		}
	}

	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Printf("yolocoder %s\n", version.Display())
			return
		case "help", "--help", "-h":
			fmt.Print(app.Help)
			return
		}
	}

	if len(args) > 0 && args[0] == "config" {
		os.Exit(app.RunConfig(args[1:]))
	}

	if len(args) > 0 && args[0] == "model" {
		os.Exit(app.RunModel(args[1:]))
	}

	flags, args, err := app.ParseFlags(args, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	commander := app.NewCommander(os.Stdout, flags.AllowCommands)

	fromEnvironment := false
	if len(args) > 0 && args[0] == "--llm-from-env-vars" {
		fromEnvironment = true
		args = args[1:]
	}

	fmt.Printf("\x1b[36m[^_^] YoloCoder %s\x1b[0m  \x1b[2m%s\x1b[0m\n", version.Display(), app.Folder())

	provider, err := app.Provider(fromEnvironment)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Turns are recorded per folder so a later run can be told what has
	// been going on here. A store that cannot be opened is not worth
	// failing over: the run matters, the note about it does not.
	history, historyErr := openHistory()
	if historyErr != nil {
		fmt.Fprintln(os.Stderr, "session log:", historyErr)
	}

	// A task on the command line is a one-shot run; without one, stay in
	// an interactive session so follow-up tasks keep the same context on
	// screen instead of ending after a single change.
	if task := strings.TrimSpace(strings.Join(args, " ")); task != "" {
		if err := runTask(task, provider, history, app.Notes(flags.Notes), commander); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("\x1b[2mEnter: send  •  Shift+Enter: new line  •  ↑ earlier  •  / for commands  •  Ctrl+C to quit\x1b[0m\n\n")
	// Seeded from what this folder has already been asked, so Up reaches
	// back past the start of this session rather than only within it.
	typed := app.Messages(recorded())
	for {
		task, err := app.PromptTask(typed)
		if err != nil {
			if errors.Is(err, terminal.ErrEditorCancelled) {
				fmt.Println("[^_^] Bye.")
				return
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if task == "" {
			continue
		}
		// Recorded before anything else happens to it, so a task that goes
		// on to fail is still one keystroke away from being retried.
		typed = append(typed, task)
		// Commands are handled here rather than sent to the model, which
		// would otherwise cheerfully answer "/model" as a question.
		if handled, quit := runCommand(task, fromEnvironment, &provider); handled {
			if quit {
				return
			}
			continue
		}
		if err := runTask(task, provider, history, earlier(flags.Notes), commander); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		fmt.Println()
	}
}

// runCommand handles a session command, reporting whether the input was
// one and whether the session should end. "exit" is accepted with or
// without the slash, since both are natural to type.
func runCommand(input string, fromEnvironment bool, provider *config.LLM) (handled, quit bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "/exit", "/quit", "exit", "quit":
		fmt.Println("[^_^] Bye.")
		return true, true
	case "/help", "help":
		app.PrintCommands()
		fmt.Println()
		return true, false
	case "/debug":
		toggleDebug()
		fmt.Println()
		return true, false
	case "/setup":
		if fromEnvironment {
			fmt.Println("[*_*] /setup can't change an OPENAI_* environment provider; restart without --llm-from-env-vars to use a saved one.")
			fmt.Println()
			return true, false
		}
		if code := app.RunConfig([]string{"connect"}); code == 0 {
			reloadProvider(provider)
		}
		fmt.Println()
		return true, false
	case "/model":
		if fromEnvironment {
			fmt.Println("[*_*] /model can't change an OPENAI_* environment provider; set OPENAI_MODEL instead.")
			fmt.Println()
			return true, false
		}
		if code := app.RunModel(nil); code == 0 {
			reloadProvider(provider)
		}
		fmt.Println()
		return true, false
	}
	if strings.HasPrefix(input, "/") {
		fmt.Printf("[*_*] Unknown command %q. Available:\n", strings.Fields(input)[0])
		app.PrintCommands()
		fmt.Println()
		return true, false
	}
	return false, false
}

// openHistory starts or continues this folder's session log.
func openHistory() (*session.Log, error) {
	folder, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return session.Open(folder, session.Terminal(os.Getenv))
}

// reloadProvider picks up a provider the user just changed, so the next
// task runs against it rather than the one the session started with.
func reloadProvider(provider *config.LLM) {
	if updated, err := app.Provider(false); err == nil {
		*provider = updated
	}
}

// activeSession is the session currently reporting progress, so trace
// entries can be written above its spinner rather than through it.
var activeSession *ui.Session

// toggleDebug turns the live trace of the model exchange on or off.
func toggleDebug() {
	if debug.SinkEnabled() {
		debug.SetSink(nil)
		fmt.Println("[*_*] Debug output off.")
		return
	}
	debug.SetSink(func(title, body string) {
		text := "  \x1b[2m[debug] " + title + "\x1b[0m"
		if body = strings.TrimSpace(body); body != "" {
			text += "\n" + indent(clip(body, debugClip))
		}
		if activeSession != nil {
			activeSession.Log(text)
			return
		}
		fmt.Println(text)
	})
	fmt.Println("[*_*] Debug output on: every request and reply will be shown.")
	if path := debug.Path(); path != "" {
		fmt.Printf("[*_*] Full trace is also being written to %s\n", path)
	} else {
		fmt.Printf("[*_*] For a full untruncated trace, restart with %s=1\n", debug.PathEnv)
	}
}

// debugClip keeps a traced body readable in the terminal; the file log
// gets it untruncated.
const debugClip = 2000

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf("\n... %d more bytes (see the debug log file)", len(text)-limit)
}

func indent(text string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = "  \x1b[2m" + line + "\x1b[0m"
	}
	return strings.Join(lines, "\n")
}

// runTask runs one task, reporting progress as it goes and printing the
// model's reply at the end.
func runTask(task string, provider config.LLM, history *session.Log, recalled []agent.Recollection, commander *app.Commander) error {
	reporter := ui.NewSession(os.Stdout)
	reporter.Start("Thinking...")
	activeSession = reporter
	// A command takes the terminal over while it runs, so the spinner has
	// to stand down for the duration rather than draw through its output.
	commander.Suspend = reporter.Suspend
	outcome, err := app.RunTask(context.Background(), task, provider, recalled, commander, reporter)
	activeSession = nil
	reporter.Stop()
	// Recorded even when the run returned an error: a command that exited
	// non-zero is exactly the sort of thing the next turn needs to know
	// about. A run that got nowhere has no Kind and nothing to record.
	if outcome.Kind != "" {
		record(history, task, outcome)
	}
	if err != nil {
		return err
	}
	reply := outcome.Reply
	if reply == "" {
		reply = "Done."
	}
	fmt.Printf("[*_*] %s\n", reply)
	return nil
}

// earlier is the history an interactive turn should be told about: the
// notes given on the command line, then what has been recorded in this
// folder. A one-shot invocation never comes through here, and so gets
// only its notes: a scripted call should do the same thing every time
// rather than depend on whatever happened in this folder earlier.
func earlier(notes []string) []agent.Recollection {
	return append(app.Notes(notes), app.Recollections(recorded())...)
}

// recorded is what this folder has already been asked, oldest first. A
// log that cannot be read is not worth reporting over: history is a
// convenience, and the run works without it.
func recorded() []session.Turn {
	folder, err := os.Getwd()
	if err != nil {
		return nil
	}
	turns, err := session.Recent(folder)
	if err != nil {
		return nil
	}
	return turns
}

// record keeps a note of the turn for a later run to draw on. It is only
// ever a convenience, so a store that cannot be written must not take the
// run down with it.
func record(history *session.Log, task string, outcome agent.Outcome) {
	if history == nil {
		return
	}
	summary := outcome.Reply
	if outcome.Kind == agent.KindCommand && outcome.Command != "" {
		// The script matters more than the sentence describing it: "run
		// that install again" needs the command, not a retelling of it.
		summary = strings.TrimSpace(summary + "\n" + outcome.Command)
	}
	_ = history.Append(session.Turn{
		Message: task,
		Kind:    outcome.Kind,
		Summary: summary,
		Files:   outcome.Files,
		Applied: outcome.Applied,
	})
}
