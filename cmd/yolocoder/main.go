package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mindsdb/yolocoder/internal/app"
	"github.com/mindsdb/yolocoder/internal/config"
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

	fmt.Printf("\x1b[2mEnter: send  •  Shift+Enter: new line  •  Ctrl+C or /exit: quit\x1b[0m\n\n")
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
		switch task {
		case "":
			continue
		case "/exit", "/quit":
			fmt.Println("[^_^] Bye.")
			return
		}
		if err := runTask(task, provider); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		fmt.Println()
	}
}

// runTask runs one task, reporting progress as it goes and printing the
// model's reply at the end.
func runTask(task string, provider config.LLM) error {
	session := ui.NewSession(os.Stdout)
	session.Start("Thinking...")
	reply, err := app.RunTask(context.Background(), task, provider, session)
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
