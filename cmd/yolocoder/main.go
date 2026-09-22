package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mindsdb/yolocoder/internal/agent"
	"github.com/mindsdb/yolocoder/internal/app"
	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/debug"
	"github.com/mindsdb/yolocoder/internal/session"
	"github.com/mindsdb/yolocoder/internal/terminal"
	"github.com/mindsdb/yolocoder/internal/ui"
	"github.com/mindsdb/yolocoder/internal/update"
	"github.com/mindsdb/yolocoder/internal/version"
	"github.com/mindsdb/yolocoder/internal/web"
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

	notes, args, err := app.ParseContext(args, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fromEnvironment := false
	if len(args) > 0 && args[0] == "--llm-from-env-vars" {
		fromEnvironment = true
		args = args[1:]
	}

	useWeb, port, debugOn, args, err := app.ParseWeb(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("\x1b[36m[^_^] YoloCoder %s\x1b[0m  \x1b[2m%s\x1b[0m\n", version.Display(), app.Folder())

	provider, err := app.Provider(fromEnvironment)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if useWeb {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		task := strings.TrimSpace(strings.Join(args, " "))
		if err := web.Serve(ctx, provider, port, task, fromEnvironment, debugOn); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if debugOn {
		toggleDebug()
		fmt.Println()
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
		if err := runTask(task, provider, history, app.Notes(notes)); err != nil {
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
		if err := runTask(task, provider, history, earlier(notes)); err != nil {
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
	case "/errors":
		printRecentFailures()
		fmt.Println()
		return true, false
	case "/recall":
		toggleRecall(fromEnvironment, provider)
		fmt.Println()
		return true, false
	case "/preselect":
		togglePreselect(fromEnvironment, provider)
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
	// A mistyped command, slash-led or otherwise. "--ebug" used to reach
	// the model, which answered the question it appeared to be.
	if nearest, ok := app.LooksLikeCommand(input); ok {
		fmt.Printf("[*_*] %q isn't a command. Did you mean %s?\n", strings.TrimSpace(input), nearest)
		app.PrintCommands()
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

// toggleRecall turns the recall tool on or off for the turns from here
// on. It takes effect immediately either way; saving it is what makes it
// stick, and an OPENAI_* environment provider isn't ours to write, so
// that case toggles for this session only and says so.
func toggleRecall(fromEnvironment bool, provider *config.LLM) {
	provider.Recall = !provider.Recall
	state := "off"
	if provider.Recall {
		state = "on"
	}
	if fromEnvironment {
		fmt.Printf("[*_*] Recall %s for this session. An OPENAI_* environment provider can't be saved.\n", state)
		return
	}
	if err := config.Save(*provider); err != nil {
		fmt.Printf("[*_*] Recall %s for this session, but it could not be saved: %v\n", state, err)
		return
	}
	fmt.Printf("[*_*] Recall %s. The agent %s read turns older than the last %d it is shown.\n",
		state, map[bool]string{true: "can now", false: "can no longer"}[provider.Recall], 3)
}

// togglePreselect turns file preselection on or off from here on, the
// same way /recall does, and for the same reason: it is worth keeping
// only while it costs less than the call it removes, so it has to be
// possible to run a few turns each way.
func togglePreselect(fromEnvironment bool, provider *config.LLM) {
	provider.Preselect = !provider.Preselect
	state := "off"
	if provider.Preselect {
		state = "on"
	}
	if fromEnvironment {
		fmt.Printf("[*_*] File preselection %s for this session. An OPENAI_* environment provider can't be saved.\n", state)
		return
	}
	if err := config.Save(*provider); err != nil {
		fmt.Printf("[*_*] File preselection %s for this session, but it could not be saved: %v\n", state, err)
		return
	}
	fmt.Printf("[*_*] File preselection %s. A turn %s the files it needs before asking the model for them.\n",
		state, map[bool]string{true: "now picks", false: "no longer picks"}[provider.Preselect])
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
func runTask(task string, provider config.LLM, history *session.Log, recalled []agent.Recollection) error {
	reporter := ui.NewSession(os.Stdout)
	reporter.Start("Thinking...")
	activeSession = reporter
	outcome, err := app.RunTask(context.Background(), task, nil, provider, recalled, reporter)
	activeSession = nil
	reporter.Stop()
	if err != nil {
		return err
	}
	record(history, task, outcome)
	reply := outcome.Reply
	if reply == "" {
		reply = "Done."
	}
	fmt.Printf("[*_*] %s\n", reply)
	if line := outcome.Usage.Summary(); line != "" {
		fmt.Printf("\x1b[2m  %s\x1b[0m\n", line)
	}
	if line := outcome.Profile.Summary(); line != "" {
		fmt.Printf("\x1b[2m  %s\x1b[0m\n", line)
	}
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
	kind := "chat"
	if outcome.Coding {
		kind = "code"
	}
	_ = history.Append(session.Turn{
		Message:         task,
		Kind:            kind,
		Summary:         outcome.Reply,
		Files:           outcome.Files,
		Applied:         outcome.Applied,
		Attempts:        outcome.Attempts,
		Rewrote:         outcome.Rewrote,
		InputTokens:     outcome.Usage.InputTokens,
		CachedTokens:    outcome.Usage.CachedTokens,
		OutputTokens:    outcome.Usage.OutputTokens,
		TotalTokens:     outcome.Usage.TotalTokens,
		TotalMillis:     millis(outcome.Profile.Total()),
		RecallMillis:    millis(outcome.Profile.Recall.Spent),
		RecallAvailable: outcome.Profile.Recall.Available,
		RecallServed:    outcome.Profile.Recall.Served,
		RecallBytes:     outcome.Profile.Recall.Bytes,
	})
}

// millis rounds a duration for the session log, which stores plain
// numbers rather than Go duration strings so the file stays readable
// with jq and friends.
func millis(spent time.Duration) int {
	return int(spent.Round(time.Millisecond) / time.Millisecond)
}

// printRecentFailures shows the patches that were rejected lately, from
// the record every run keeps. Short on purpose — enough to recognise the
// problem, with the file named for anyone who wants the whole thing.
func printRecentFailures() {
	recent := debug.Recent(5)
	if len(recent) == 0 {
		fmt.Println("[^_^] Nothing has failed to apply. Records go to " + debug.RecordsPath())
		return
	}
	for _, entry := range recent {
		fmt.Printf("[*_*] %v  %v\n", entry["at"], entry["folder"])
		if task, ok := entry["task"].(string); ok && task != "" {
			fmt.Println("      task: " + firstLine(task))
		}
		if reasons, ok := entry["reasons"].([]any); ok {
			for _, reason := range reasons {
				fmt.Printf("      %v\n", reason)
			}
		}
	}
	fmt.Println("\n[^_^] Full records, patches included: " + debug.RecordsPath())
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if len(line) > 100 {
		return line[:100] + "..."
	}
	return line
}
