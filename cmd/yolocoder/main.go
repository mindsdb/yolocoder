package main

import (
	"fmt"
	"os"

	"github.com/mindsdb/yolocoder/internal/app"
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

	ui.WithRobot(os.Stdout, "Starting YoloCoder...", func(status ui.RobotStatus) {
		update.CheckOnLaunch(version.Commit, status)
	})

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

	if len(args) > 0 {
		switch args[0] {
		case "config":
			os.Exit(app.RunConfig(args[1:]))
		case "--llm-from-env-vars":
			if len(args) != 1 {
				fmt.Fprintln(os.Stderr, "--llm-from-env-vars does not accept additional arguments")
				os.Exit(2)
			}
			if err := app.UseLLMFromEnvironment(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "unknown argument %q\n\n%s", args[0], app.Help)
			os.Exit(2)
		}
	} else if err := app.EnsureLLM(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("YoloCoder %s\n", version.Display())
	fmt.Println("Welcome to YoloCode CLI")
}
