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
	{Name: "/errors", Description: "show what recently failed to apply"},
	{Name: "/recall", Description: "toggle reading turns older than the last few"},
	{Name: "/preselect", Description: "toggle choosing a turn's files before asking"},
	{Name: "/help", Description: "show these commands"},
	{Name: "/exit", Description: "end the session"},
}

// PromptTask reads one task from the interactive editor. earlier is what
// has already been asked, oldest first, which Up steps back through. It
// returns terminal.ErrEditorCancelled when the user presses Ctrl+C.
func PromptTask(earlier []string) (string, error) {
	if !terminalInput() {
		return "", fmt.Errorf("a task is required\n\nUsage: yolocoder [--llm-from-env-vars] <task>")
	}
	task, err := terminal.NewReader(os.Stdin).EditTask(os.Stdout, SessionCommands, earlier)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(task), nil
}

// Messages are just what was asked, oldest first, which is what the
// prompt's Up-arrow recall steps back through.
func Messages(turns []session.Turn) []string {
	messages := make([]string, 0, len(turns))
	for _, turn := range turns {
		if message := strings.TrimSpace(turn.Message); message != "" {
			messages = append(messages, message)
		}
	}
	return messages
}

// PrintCommands lists the session's slash commands.
func PrintCommands() {
	for _, command := range SessionCommands {
		fmt.Printf("  \x1b[35m%-10s\x1b[0m \x1b[2m%s\x1b[0m\n", command.Name, command.Description)
	}
}

// RunTask reports what the run amounted to: the reply to show, whether
// it was a coding task, and what it touched, which is what the caller
// needs to record the turn. images are data URLs (screenshots pasted into
// the web UI) attached to task, nil from the terminal, which has no way
// to paste one in.
func RunTask(ctx context.Context, task string, images []string, provider config.LLM, history []agent.Recollection, progress agent.Progress) (agent.Outcome, error) {
	repository, err := repo.Open(".")
	if err != nil {
		return agent.Outcome{}, err
	}
	client, err := agent.NewClient(provider)
	if err != nil {
		return agent.Outcome{}, err
	}
	runner := agent.NewRunner(client, repository)
	runner.UseRecall(provider.Recall)
	runner.UsePreselect(provider.Preselect)
	outcome, runErr := runner.Run(ctx, task, images, history, progress)
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

// LooksLikeCommand reports whether a message was probably meant as a
// command rather than said to the model, and names the closest one.
//
// "--ebug" cost a round trip and a puzzled reply: it is not slash-
// prefixed, so it went to the model as a question, which answered it as
// best it could. Anything short and punctuation-led is a better guess at
// a mistyped command than at something worth asking.
func LooksLikeCommand(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || len(strings.Fields(trimmed)) > 1 {
		return "", false
	}
	word := strings.ToLower(strings.TrimLeft(trimmed, "-/"))
	if word == "" || word == trimmed {
		return "", false // no leading punctuation: ordinary words stay ordinary
	}
	best, distance := "", len(word)/2+2
	for _, command := range SessionCommands {
		name := strings.TrimPrefix(command.Name, "/")
		if gap := editDistance(word, name); gap < distance {
			best, distance = command.Name, gap
		}
	}
	return best, best != ""
}

// editDistance is Levenshtein, over inputs short enough that the simple
// full-matrix version costs nothing worth saving.
func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(min(current[j-1]+1, previous[j]+1), previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}
