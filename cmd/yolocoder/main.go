package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mindsdb/yolocoder/internal/app"
	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/debug"
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

	// A task on the command line is a one-shot run; without one, stay in
	// an interactive session so follow-up tasks keep the same context on
	// screen instead of ending after a single change.
	if task := strings.TrimSpace(strings.Join(args, " ")); task != "" {
		if err := runTask(task, provider); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("\x1b[2mEnter: send  •  Shift+Enter: new line  •  / for commands  •  Ctrl+C to quit\x1b[0m\n\n")
	for {
		task, err := app.PromptTask()
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
		// Commands are handled here rather than sent to the model, which
		// would otherwise cheerfully answer "/model" as a question.
		if handled, quit := runCommand(task, fromEnvironment, &provider); handled {
			if quit {
				return
			}
			continue
		}
		if err := runTask(task, provider); err != nil {
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
func runTask(task string, provider config.LLM) error {
	session := ui.NewSession(os.Stdout)
	session.Start("Thinking...")
	activeSession = session
	reply, err := app.RunTask(context.Background(), task, provider, session)
	activeSession = nil
	session.Stop()
	if err != nil {
		return err
	}
	if reply == "" {
		reply = "Done."
	}
	fmt.Printf("[*_*] %s\n", reply)
	return nil
}
