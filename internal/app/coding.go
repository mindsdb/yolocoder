package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mindsdb/yolocoder/internal/agent"
	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/repo"
	"github.com/mindsdb/yolocoder/internal/session"
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

// RunTask reports what the run amounted to: the reply to show, whether
// it was a coding task, and what it touched, which is what the caller
// needs to record the turn.
func RunTask(ctx context.Context, task string, provider config.LLM, history []agent.Recollection, progress agent.Progress) (agent.Outcome, error) {
	repository, err := repo.Open(".")
	if err != nil {
		return agent.Outcome{}, err
	}
	client, err := agent.NewClient(provider)
	if err != nil {
		return agent.Outcome{}, err
	}
	outcome, runErr := agent.NewRunner(client, repository).Run(ctx, task, history, progress)
	rememberDialect(provider, client)
	return outcome, runErr
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

// Recollections maps recorded turns into what the agent takes, keeping
// the agent free of any notion of where history is stored.
func Recollections(turns []session.Turn) []agent.Recollection {
	recalled := make([]agent.Recollection, 0, len(turns))
	for _, turn := range turns {
		recalled = append(recalled, agent.Recollection{
			Number:  turn.Number,
			Message: turn.Message,
			Summary: turn.Summary,
			Files:   turn.Files,
		})
	}
	return recalled
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
