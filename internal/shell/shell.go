// Package shell runs a command script the model produced.
//
// It deliberately does not inspect or rewrite the script. Filtering it,
// or deciding which parts of it look safe, would give the impression of a
// sandbox where there is none: the script gets the same shell and the
// same permissions the user has. The real protection is the confirmation
// the caller asks for before any of this is reached, which is why running
// a script and deciding whether it may run are kept in separate places.
//
// What this package does provide is visibility. The command runs on a
// pseudo-terminal, so everything it prints is captured as the user sees
// it, and a Supervisor can be consulted while it is still running — which
// matters most for exactly the case that motivates it, a long generated
// script whose first line looked fine.
package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// How closely a running command is watched. The first check does not
// happen until CheckEvery has passed, so an ordinary command that
// finishes in a second is never supervised at all and costs nothing; a
// script that is still going after half a minute is worth a look.
const (
	DefaultCheckEvery = 30 * time.Second
	DefaultMaxChecks  = 4
	// TranscriptLimit is how much of the output is kept for the
	// supervisor. It is the tail that matters: the error is at the end,
	// and a full build log would cost more to send than it could
	// possibly be worth.
	TranscriptLimit = 6 << 10
)

// Report is what a Supervisor is given about a command still running.
type Report struct {
	// Script is what was started.
	Script string
	// Output is the tail of everything printed so far.
	Output string
	// Elapsed is how long it has been running, and Idle how long since it
	// last printed anything. A long Idle on a short Output is the shape of
	// a command waiting for input nobody is going to give it.
	Elapsed time.Duration
	Idle    time.Duration
	// Check counts this consultation, from one.
	Check int
}

// Supervisor is asked, while a command is still running, whether it is
// going to plan. Returning an error stops the command, and that error is
// what the run reports.
type Supervisor interface {
	Check(ctx context.Context, report Report) error
}

// Result is what running a command amounted to.
type Result struct {
	// Output is the tail of what it printed, as the supervisor saw it.
	Output string
	// ExitCode is the command's status, or -1 if it never got that far.
	ExitCode int
	// Stopped says the supervisor cut it short rather than it ending.
	Stopped bool
}

// Runner executes scripts in one folder.
type Runner struct {
	// Folder is the working directory the script runs in.
	Folder string
	// Out receives the command's output as it is produced, so a long
	// command shows its progress rather than going quiet and dumping
	// everything at the end.
	Out io.Writer
	// In is the command's input, so something that asks a question can be
	// answered by the person watching.
	In io.Reader
	// Watch, when set, is consulted periodically while the command runs.
	Watch Supervisor
	// CheckEvery and MaxChecks bound that supervision. Zero means the
	// defaults.
	CheckEvery time.Duration
	MaxChecks  int
	// Notice, when set, is told each time the supervisor is about to be
	// consulted, so the caller can say so rather than appearing to stall.
	Notice func(string)
}

// Run executes script and waits for it to finish. A script that exits
// non-zero is an error, with the status in the message: the caller needs
// to know it failed, and the output has already gone to Out.
func (runner *Runner) Run(ctx context.Context, script string) (Result, error) {
	if strings.TrimSpace(script) == "" {
		return Result{ExitCode: -1}, errors.New("no command to run")
	}
	path, cleanup, err := write(script)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	defer cleanup()

	name, args := interpreter(path)
	command := exec.Command(name, args...)
	command.Dir = runner.Folder

	transcript := &tail{limit: TranscriptLimit}
	copied, closeStream, err := runner.attach(command, transcript)
	if err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("run command: %w", err)
	}
	defer closeStream()

	// Wait only once the output is drained, so nothing the command
	// printed on its way out is lost.
	finished := make(chan error, 1)
	go func() {
		<-copied
		finished <- command.Wait()
	}()

	return runner.supervise(ctx, script, command, transcript, finished)
}

// supervise waits for the command, checking in on it along the way.
func (runner *Runner) supervise(ctx context.Context, script string, command *exec.Cmd, transcript *tail, finished <-chan error) (Result, error) {
	started := time.Now()
	ticker := time.NewTicker(runner.checkEvery())
	defer ticker.Stop()

	checks := 0
	for {
		select {
		case err := <-finished:
			return result(transcript, err, false)

		case <-ctx.Done():
			terminate(command)
			<-finished
			outcome, _ := result(transcript, nil, true)
			return outcome, ctx.Err()

		case <-ticker.C:
			if runner.Watch == nil || checks >= runner.maxChecks() {
				continue
			}
			checks++
			elapsed := time.Since(started)
			if runner.Notice != nil {
				runner.Notice(fmt.Sprintf("still running after %s, checking on it", elapsed.Round(time.Second)))
			}
			report := Report{
				Script:  script,
				Output:  transcript.String(),
				Elapsed: elapsed,
				Idle:    transcript.idle(),
				Check:   checks,
			}
			if err := runner.Watch.Check(ctx, report); err != nil {
				terminate(command)
				<-finished
				outcome, _ := result(transcript, nil, true)
				return outcome, err
			}
		}
	}
}

// attach connects the command to a pseudo-terminal, or to pipes where
// there is none, and starts it. The returned channel closes once all the
// output has been read.
func (runner *Runner) attach(command *exec.Cmd, transcript *tail) (copied <-chan struct{}, cleanup func(), err error) {
	sink := io.MultiWriter(runner.out(), transcript)
	done := make(chan struct{})

	terminal, terminalErr := startTerminal(command)
	if terminalErr == nil {
		go func() {
			defer close(done)
			// A pseudo-terminal reports the far end hanging up as an
			// error rather than as EOF, so a failed copy here is the
			// ordinary way a command finishes, not a problem.
			_, _ = io.Copy(sink, terminal)
		}()
		if runner.In != nil {
			// Nothing waits on this: it blocks on the user's keyboard,
			// and the pseudo-terminal closing is what ends it.
			go func() { _, _ = io.Copy(terminal, runner.In) }()
		}
		return done, func() { terminal.Close() }, nil
	}

	// Without a pseudo-terminal the output is still captured and still
	// supervised; the command is simply told it is not on a terminal.
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	command.Stderr = command.Stdout
	command.Stdin = runner.In
	if err := command.Start(); err != nil {
		return nil, nil, err
	}
	go func() {
		defer close(done)
		_, _ = io.Copy(sink, output)
	}()
	return done, func() {}, nil
}

func result(transcript *tail, waitErr error, stopped bool) (Result, error) {
	outcome := Result{Output: transcript.String(), Stopped: stopped}
	if stopped {
		outcome.ExitCode = -1
		return outcome, nil
	}
	var exit *exec.ExitError
	switch {
	case waitErr == nil:
		return outcome, nil
	case errors.As(waitErr, &exit):
		outcome.ExitCode = exit.ExitCode()
		return outcome, fmt.Errorf("command exited with status %d", outcome.ExitCode)
	default:
		outcome.ExitCode = -1
		return outcome, fmt.Errorf("run command: %w", waitErr)
	}
}

// terminate stops a command that has been cut short. Killing the whole
// process group rather than just the shell matters here: the script is
// what was started, but its children are what is actually doing the work.
func terminate(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	killGroup(command.Process.Pid)
	_ = command.Process.Kill()
}

func (runner *Runner) out() io.Writer {
	if runner.Out == nil {
		return io.Discard
	}
	return runner.Out
}

func (runner *Runner) checkEvery() time.Duration {
	if runner.CheckEvery <= 0 {
		return DefaultCheckEvery
	}
	return runner.CheckEvery
}

func (runner *Runner) maxChecks() int {
	if runner.MaxChecks <= 0 {
		return DefaultMaxChecks
	}
	return runner.MaxChecks
}

// tail keeps the last limit bytes written to it, and when they were
// written. It is deliberately a window rather than the whole transcript:
// a command that prints a hundred megabytes of build log should not be
// able to exhaust memory on its way to telling us it failed.
type tail struct {
	limit int

	mutex sync.Mutex
	kept  []byte
	last  time.Time
	// dropped counts what fell out of the window, so the supervisor is
	// told it is reading an excerpt rather than the whole thing.
	dropped int
}

func (buffer *tail) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	buffer.last = time.Now()
	buffer.kept = append(buffer.kept, data...)
	if excess := len(buffer.kept) - buffer.limit; excess > 0 {
		buffer.kept = buffer.kept[excess:]
		buffer.dropped += excess
	}
	return len(data), nil
}

func (buffer *tail) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	if buffer.dropped > 0 {
		return fmt.Sprintf("... %d earlier bytes omitted ...\n%s", buffer.dropped, buffer.kept)
	}
	return string(buffer.kept)
}

// idle is how long since the command last printed anything. A command
// that has printed nothing at all reports zero rather than a misleading
// age measured from some arbitrary start.
func (buffer *tail) idle() time.Duration {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	if buffer.last.IsZero() {
		return 0
	}
	return time.Since(buffer.last)
}

// write puts the script in a temporary file. Running it from a file
// rather than passing it as an argument is what makes a multi-line script
// behave the same on every platform, and sidesteps the argument length
// limits a long one would otherwise run into.
func write(script string) (path string, cleanup func(), err error) {
	file, err := os.CreateTemp("", "yolocoder-*"+scriptExtension())
	if err != nil {
		return "", nil, fmt.Errorf("write command script: %w", err)
	}
	cleanup = func() { os.Remove(file.Name()) }
	if _, err := file.WriteString(prepare(script)); err != nil {
		file.Close()
		cleanup()
		return "", nil, fmt.Errorf("write command script: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write command script: %w", err)
	}
	return file.Name(), cleanup, nil
}

// prepare makes the script fit for the interpreter it is about to be
// handed to.
func prepare(script string) string {
	if runtime.GOOS == "windows" {
		// A batch file echoes every line it runs unless told not to, which
		// would double up the output against the script the user was just
		// shown.
		return "@echo off\r\n" + strings.ReplaceAll(normalise(script), "\n", "\r\n")
	}
	// -e stops at the first failing command rather than carrying on
	// through the rest of a script whose premise has already broken.
	return "set -e\n" + normalise(script)
}

// normalise strips the code fence a model sometimes wraps a script in and
// settles the line endings, so neither reaches the shell as syntax.
func normalise(script string) string {
	script = strings.ReplaceAll(strings.ReplaceAll(script, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(strings.TrimSpace(script), "\n")
	if len(lines) >= 2 && strings.HasPrefix(lines[0], "```") && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		lines = lines[1 : len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n")) + "\n"
}

// Describe says what a script will be handed to, so a model can be told
// what to write for rather than left to guess from the folder's contents.
func Describe() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe on Windows"
	}
	return "/bin/sh on " + runtime.GOOS
}

func scriptExtension() string {
	if runtime.GOOS == "windows" {
		return ".cmd"
	}
	return ".sh"
}

func interpreter(path string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", filepath.Clean(path)}
	}
	return "/bin/sh", []string{path}
}
