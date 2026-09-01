package shell

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func unixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the scripts in these tests are POSIX shell")
	}
}

func TestRunCapturesAndStreamsOutput(t *testing.T) {
	unixOnly(t)
	var seen bytes.Buffer
	runner := &Runner{Folder: t.TempDir(), Out: &seen}

	result, err := runner.Run(context.Background(), "echo hello\necho world")
	if err != nil {
		t.Fatal(err)
	}
	// The same bytes reach the terminal and the transcript: what the
	// supervisor reads has to be what the user saw.
	for _, where := range []string{seen.String(), result.Output} {
		if !strings.Contains(where, "hello") || !strings.Contains(where, "world") {
			t.Fatalf("output = %q, want both lines", where)
		}
	}
	if result.ExitCode != 0 || result.Stopped {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunReportsTheExitStatus(t *testing.T) {
	unixOnly(t)
	runner := &Runner{Folder: t.TempDir()}
	result, err := runner.Run(context.Background(), "exit 3")
	if err == nil {
		t.Fatal("a non-zero exit should be an error")
	}
	if result.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", result.ExitCode)
	}
	if !strings.Contains(err.Error(), "3") {
		t.Fatalf("err = %v, want the status in it", err)
	}
}

func TestRunStopsAtTheFirstFailingLine(t *testing.T) {
	unixOnly(t)
	// set -e: a script whose premise has broken should not carry on.
	runner := &Runner{Folder: t.TempDir()}
	result, err := runner.Run(context.Background(), "false\necho should-not-appear")
	if err == nil {
		t.Fatal("expected the script to fail")
	}
	if strings.Contains(result.Output, "should-not-appear") {
		t.Fatalf("the script carried on past a failure: %q", result.Output)
	}
}

func TestRunGivesTheCommandARealTerminal(t *testing.T) {
	unixOnly(t)
	// The point of the pseudo-terminal: a command that asks whether it is
	// talking to a terminal has to get the same answer it would if the
	// user had typed it.
	runner := &Runner{Folder: t.TempDir()}
	result, err := runner.Run(context.Background(), "if [ -t 1 ]; then echo ON-A-TTY; else echo A-PIPE; fi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "ON-A-TTY") {
		t.Fatalf("output = %q, want the command to see a terminal", result.Output)
	}
}

func TestRunRefusesAnEmptyScript(t *testing.T) {
	if _, err := (&Runner{}).Run(context.Background(), "   \n  "); err == nil {
		t.Fatal("expected an error for an empty script")
	}
}

func TestRunInTheGivenFolder(t *testing.T) {
	unixOnly(t)
	folder := t.TempDir()
	runner := &Runner{Folder: folder}
	result, err := runner.Run(context.Background(), "pwd")
	if err != nil {
		t.Fatal(err)
	}
	// macOS reports /private in front of a temporary path, so match the
	// leaf rather than the whole thing.
	if leaf := folder[strings.LastIndex(folder, "/")+1:]; !strings.Contains(result.Output, leaf) {
		t.Fatalf("ran in %q, want %s", result.Output, folder)
	}
}

// stopper is a supervisor that cuts the command short on its first look.
type stopper struct {
	reports []Report
}

func (watch *stopper) Check(_ context.Context, report Report) error {
	watch.reports = append(watch.reports, report)
	return errors.New("that is quite enough of that")
}

func TestSupervisorCanStopARunningCommand(t *testing.T) {
	unixOnly(t)
	watch := &stopper{}
	runner := &Runner{
		Folder:     t.TempDir(),
		Watch:      watch,
		CheckEvery: 150 * time.Millisecond,
	}
	started := time.Now()
	result, err := runner.Run(context.Background(), "echo starting\nsleep 30")
	elapsed := time.Since(started)

	if err == nil || !strings.Contains(err.Error(), "quite enough") {
		t.Fatalf("err = %v, want the supervisor's reason", err)
	}
	if !result.Stopped {
		t.Fatalf("result = %+v, want it marked stopped", result)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("took %s: the command was not actually killed", elapsed)
	}
	if len(watch.reports) == 0 {
		t.Fatal("the supervisor was never consulted")
	}
	// It is shown the script and what has been printed, which is what it
	// needs to judge anything at all.
	report := watch.reports[0]
	if !strings.Contains(report.Script, "sleep 30") || !strings.Contains(report.Output, "starting") {
		t.Fatalf("report = %+v", report)
	}
	if report.Check != 1 {
		t.Fatalf("check number = %d, want 1", report.Check)
	}
}

// counter counts how often it is asked and always approves.
type counter struct{ checks int }

func (watch *counter) Check(context.Context, Report) error {
	watch.checks++
	return nil
}

func TestSupervisionIsBounded(t *testing.T) {
	unixOnly(t)
	// A watched command must not turn into an open-ended series of model
	// calls; the cap is what keeps a slow build from being expensive.
	watch := &counter{}
	runner := &Runner{
		Folder:     t.TempDir(),
		Watch:      watch,
		CheckEvery: 50 * time.Millisecond,
		MaxChecks:  2,
	}
	if _, err := runner.Run(context.Background(), "sleep 1"); err != nil {
		t.Fatal(err)
	}
	if watch.checks != 2 {
		t.Fatalf("checks = %d, want the cap of 2", watch.checks)
	}
}

func TestAQuickCommandIsNeverSupervised(t *testing.T) {
	unixOnly(t)
	// The first check is one interval away, so an ordinary command that
	// finishes immediately costs nothing extra at all.
	watch := &counter{}
	runner := &Runner{Folder: t.TempDir(), Watch: watch, CheckEvery: 5 * time.Second}
	if _, err := runner.Run(context.Background(), "echo quick"); err != nil {
		t.Fatal(err)
	}
	if watch.checks != 0 {
		t.Fatalf("checks = %d, want none", watch.checks)
	}
}

func TestCancellingTheContextStopsTheCommand(t *testing.T) {
	unixOnly(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := (&Runner{Folder: t.TempDir()}).Run(ctx, "sleep 30"); err == nil {
		t.Fatal("expected the cancellation to surface")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("took %s: the command outlived its context", elapsed)
	}
}

func TestTailKeepsTheEndAndSaysWhatItDropped(t *testing.T) {
	buffer := &tail{limit: 16}
	buffer.Write([]byte(strings.Repeat("a", 40)))
	buffer.Write([]byte("THE-END"))
	text := buffer.String()
	if !strings.Contains(text, "THE-END") {
		t.Fatalf("tail = %q, want the newest bytes", text)
	}
	if !strings.Contains(text, "omitted") {
		t.Fatalf("tail = %q, should admit it is an excerpt", text)
	}
	if len(buffer.kept) > 16 {
		t.Fatalf("kept %d bytes, over the limit", len(buffer.kept))
	}
}

func TestNormaliseStripsACodeFence(t *testing.T) {
	// Models wrap scripts in fences; a fence reaching the shell is a
	// syntax error rather than a command.
	if got := normalise("```sh\necho hi\n```"); strings.TrimSpace(got) != "echo hi" {
		t.Fatalf("normalise = %q", got)
	}
	if got := normalise("echo hi"); strings.TrimSpace(got) != "echo hi" {
		t.Fatalf("normalise = %q", got)
	}
	// A fence that only opens is not a fence, and stripping one line of a
	// real script would be worse than leaving it be.
	if got := normalise("```\necho hi"); !strings.Contains(got, "```") {
		t.Fatalf("normalise = %q, want the unbalanced fence left alone", got)
	}
}
