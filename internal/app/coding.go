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

// SessionCommands are the slash commands the interactive prompt offers.
var SessionCommands = []terminal.Command{
	{Name: "/setup", Description: "connect an LLM provider"},
	{Name: "/model", Description: "choose the model to use"},
	{Name: "/debug", Description: "show the raw model exchange"},
	{Name: "/help", Description: "show these commands"},
	{Name: "/exit", Description: "end the session"},
}

// PromptTask reads one task from the interactive editor. It returns
// terminal.ErrEditorCancelled when the user presses Ctrl+C.
func PromptTask() (string, error) {
	if !terminalInput() {
		return "", fmt.Errorf("a task is required\n\nUsage: yolocoder [--llm-from-env-vars] <task>")
	}
	task, err := terminal.NewReader(os.Stdin).EditTask(os.Stdout, SessionCommands)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(task), nil
}

// PrintCommands lists the session's slash commands.
func PrintCommands() {
	for _, command := range SessionCommands {
		fmt.Printf("  \x1b[35m%-10s\x1b[0m \x1b[2m%s\x1b[0m\n", command.Name, command.Description)
	}
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
	reply, runErr := agent.NewRunner(client, repository).Run(ctx, task, progress)
	rememberDialect(provider, client)
	return reply, runErr
}

// rememberDialect saves which API the endpoint turned out to speak, so a
// provider saved before that was recorded costs one 404 to discover rather
// than one on every run. An environment provider isn't ours to write.
func rememberDialect(provider config.LLM, client *agent.Client) {
	if provider.Provider == "environment" || provider.API == client.Dialect() {
		return
	}
	provider.API = client.Dialect()
	_ = config.Save(provider)
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
