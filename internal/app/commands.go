package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/mindsdb/yolocoder/internal/agent"
	"github.com/mindsdb/yolocoder/internal/shell"
)

// Commander shows a generated command, asks whether it may run, and runs
// it if the answer is yes.
//
// Asking is the whole point. Everything upstream of this — the router's
// instructions, the schema, the model's own judgement — is advisory; this
// is the one place a person sees the exact text that is about to be
// executed with their permissions, in their folder, before it is.
type Commander struct {
	Folder string
	Out    io.Writer
	In     io.Reader
	// Assume skips the question. It exists for scripted runs, where there
	// is nobody at the terminal to answer one, and it is opt-in for the
	// obvious reason.
	Assume bool
	// Interactive says whether there is someone who can be asked. Without
	// one, and without Assume, nothing runs.
	Interactive bool
	// Suspend stops whatever else is drawing on the terminal and returns
	// what resumes it, for the duration of the command.
	Suspend func() func()

	// reader is kept across calls so nothing read ahead is lost.
	reader *bufio.Reader
}

// Run implements agent.Commander.
func (commander *Commander) Run(ctx context.Context, script string, watch shell.Supervisor) error {
	// Before anything is printed, not just before the command runs. The
	// question is a prompt on a line of its own with no newline after it,
	// so a spinner still animating in that spot erases it between frames
	// and the user is left looking at a session that has silently stopped.
	if commander.Suspend != nil {
		defer commander.Suspend()()
	}
	commander.show(script)
	allowed, err := commander.permitted()
	if err != nil {
		return err
	}
	if !allowed {
		return agent.ErrCommandDeclined
	}
	fmt.Fprintf(commander.Out, "\n\x1b[2m--- output ---\x1b[0m\n")
	runner := &shell.Runner{
		Folder: commander.Folder,
		Out:    commander.Out,
		In:     commander.In,
		Watch:  watch,
		Notice: func(message string) {
			fmt.Fprintf(commander.Out, "\x1b[2m[^_^] %s\x1b[0m\n", message)
		},
	}
	result, err := runner.Run(ctx, script)
	fmt.Fprintf(commander.Out, "\x1b[2m--- %s ---\x1b[0m\n", ending(result, err))
	return err
}

// ending is the one line closing off the output, so it is always clear
// where the command's own writing stopped and ours resumed.
func ending(result shell.Result, err error) string {
	switch {
	case result.Stopped:
		return "stopped"
	case err != nil:
		return fmt.Sprintf("exit %d", result.ExitCode)
	default:
		return "done"
	}
}

// show prints the script the way a quotation is printed: set apart, and
// unchanged. It is what the user is being asked about, so it is never
// summarised, shortened or reflowed.
func (commander *Commander) show(script string) {
	fmt.Fprintf(commander.Out, "\n\x1b[33m[>_<] This wants to run a command in %s:\x1b[0m\n", commander.Folder)
	for _, line := range strings.Split(strings.TrimSpace(script), "\n") {
		fmt.Fprintf(commander.Out, "  \x1b[1m%s\x1b[0m\n", line)
	}
}

func (commander *Commander) permitted() (bool, error) {
	if commander.Assume {
		fmt.Fprintf(commander.Out, "\x1b[2mRunning it: %s was given.\x1b[0m\n", allowCommandsFlag)
		return true, nil
	}
	if !commander.Interactive {
		// Refusing is the only honest answer here: there is nobody to ask,
		// and running it anyway would make the confirmation a formality
		// that disappears exactly when it is least supervised.
		fmt.Fprintf(commander.Out, "\x1b[2mNot running it: nobody to ask. Pass %s to allow commands in a scripted run.\x1b[0m\n", allowCommandsFlag)
		return false, nil
	}
	fmt.Fprint(commander.Out, "\x1b[33mRun it? [y/N] \x1b[0m")
	line, err := commander.answer()
	fmt.Fprintln(commander.Out)
	if err != nil && strings.TrimSpace(line) == "" {
		// A closed input is a "no", not a crash.
		return false, nil
	}
	return affirmative(line), nil
}

// answer reads one typed line.
//
// It ends the line on carriage return as well as newline. A terminal
// left in raw mode by whatever drew the prompt before us sends Enter as
// \r, and waiting for a \n that is never coming would hang the session
// on a question the user has already answered.
func (commander *Commander) answer() (string, error) {
	if commander.reader == nil {
		// Kept across calls: a fresh bufio.Reader would abandon whatever
		// the last one read ahead of what it returned.
		commander.reader = bufio.NewReader(commander.In)
	}
	var line strings.Builder
	for {
		character, _, err := commander.reader.ReadRune()
		if err != nil {
			return line.String(), err
		}
		if character == '\n' || character == '\r' {
			return line.String(), nil
		}
		line.WriteRune(character)
	}
}

// affirmative reads the answer strictly: only a clear yes is a yes, so a
// stray keystroke or a pasted newline cannot run a command.
func affirmative(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}
