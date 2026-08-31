package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mindsdb/yolocoder/internal/agent"
	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/repo"
	"github.com/mindsdb/yolocoder/internal/terminal"
)

// Provider resolves the LLM provider to use, prompting for one when none
// is configured yet.
func Provider(fromEnvironment bool) (config.LLM, error) {
	return resolveProvider(fromEnvironment)
}

// PromptTask reads one task from the interactive editor. It returns
// terminal.ErrEditorCancelled when the user presses Ctrl+C.
func PromptTask() (string, error) {
	if !terminalInput() {
		return "", fmt.Errorf("a task is required\n\nUsage: yolocoder [--llm-from-env-vars] <task>")
	}
	task, err := terminal.NewReader(os.Stdin).EditTask(os.Stdout)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(task), nil
}

// RunTask returns the model's reply. For a plain conversational message,
// that's its direct answer; for a completed coding task, it's a short
// summary of the change (may be empty).
func RunTask(ctx context.Context, task string, provider config.LLM, progress agent.Progress) (string, error) {
	repository, err := repo.Open(".")
	if err != nil {
		return "", err
	}
	client, err := agent.NewClient(provider)
	if err != nil {
		return "", err
	}
	return agent.NewRunner(client, repository).Run(ctx, task, progress)
}

func resolveProvider(fromEnvironment bool) (config.LLM, error) {
	if fromEnvironment {
		return config.FromEnvironment(os.Getenv)
	}
	provider, configured, err := config.Load()
	if err != nil {
		return config.LLM{}, err
	}
	if !configured {
		if err := EnsureLLM(); err != nil {
			return config.LLM{}, err
		}
		provider, configured, err = config.Load()
	}
	if err != nil {
		return config.LLM{}, err
	}
	if !configured {
		return config.LLM{}, fmt.Errorf("no LLM inference provider configured")
	}
	return provider, nil
}

// Folder is the current working directory, shortened with ~ for display.
func Folder() string {
	directory, err := os.Getwd()
	if err != nil {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil {
		if relative, ok := strings.CutPrefix(directory, home); ok {
			return "~" + relative
		}
	}
	return directory
}
