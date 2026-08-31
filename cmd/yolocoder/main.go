package main

import (
	"context"
	"fmt"
	"os"
	"strings"

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

	if len(args) > 0 && args[0] == "config" {
		os.Exit(app.RunConfig(args[1:]))
	}

	fmt.Printf("YoloCoder %s\n", version.Display())
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "find current folder: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Folder: %s\n", workingDirectory)
	fromEnvironment := false
	if len(args) > 0 && args[0] == "--llm-from-env-vars" {
		fromEnvironment = true
		args = args[1:]
	}
	task := strings.TrimSpace(strings.Join(args, " "))
	task, provider, err := app.PrepareTask(task, fromEnvironment)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var reply string
	var runErr error
	ui.WithRobot(os.Stdout, "Thinking...", func(status ui.RobotStatus) {
		reply, runErr = app.RunTask(context.Background(), task, provider, status)
	})
	if runErr != nil {
		fmt.Fprintln(os.Stderr, runErr)
		os.Exit(1)
	}
	if reply != "" {
		fmt.Printf("[*_*] %s\n", reply)
	} else {
		fmt.Println("[*_*] Done.")
	}
}
