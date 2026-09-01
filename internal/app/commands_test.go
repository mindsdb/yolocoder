package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/agent"
)

func commandFolder(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the scripts in these tests are POSIX shell")
	}
	return t.TempDir()
}

// marker is a script that leaves proof it ran.
func marker(folder string) (script, path string) {
	path = filepath.Join(folder, "it-ran")
	return "touch " + path, path
}

func ran(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestCommandRunsOnlyAfterAYes(t *testing.T) {
	folder := commandFolder(t)
	script, proof := marker(folder)
	var out bytes.Buffer
	commander := &Commander{Folder: folder, Out: &out, In: strings.NewReader("y\n"), Interactive: true}

	if err := commander.Run(context.Background(), script, nil); err != nil {
		t.Fatal(err)
	}
	if !ran(proof) {
		t.Fatal("the command did not run after being approved")
	}
	// The exact script is shown before the question, never a summary of
	// it: the text is what is being agreed to.
	if !strings.Contains(out.String(), script) {
		t.Fatalf("output did not show the script:\n%s", out.String())
	}
}

func TestCommandIsDeclinedByDefault(t *testing.T) {
	folder := commandFolder(t)
	script, proof := marker(folder)
	var out bytes.Buffer

	// Everything that is not a clear yes is a no, a bare newline included.
	for _, answer := range []string{"\n", "n\n", "sure\n", "yes please\n", ""} {
		out.Reset()
		commander := &Commander{Folder: folder, Out: &out, In: strings.NewReader(answer), Interactive: true}
		err := commander.Run(context.Background(), script, nil)
		if !errors.Is(err, agent.ErrCommandDeclined) {
			t.Fatalf("answer %q: err = %v, want a decline", answer, err)
		}
		if ran(proof) {
			t.Fatalf("answer %q ran the command", answer)
		}
	}
}

func TestCommandWillNotRunWithNobodyToAsk(t *testing.T) {
	// A scripted run has no one at the terminal. Running anyway would
	// make the confirmation vanish exactly where it is least supervised.
	folder := commandFolder(t)
	script, proof := marker(folder)
	var out bytes.Buffer
	commander := &Commander{Folder: folder, Out: &out, Interactive: false}

	if err := commander.Run(context.Background(), script, nil); !errors.Is(err, agent.ErrCommandDeclined) {
		t.Fatalf("err = %v, want a decline", err)
	}
	if ran(proof) {
		t.Fatal("the command ran with nobody to approve it")
	}
	if !strings.Contains(out.String(), allowCommandsFlag) {
		t.Fatalf("the refusal should name the flag that allows it:\n%s", out.String())
	}
}

func TestAssumeRunsWithoutAsking(t *testing.T) {
	folder := commandFolder(t)
	script, proof := marker(folder)
	var out bytes.Buffer
	commander := &Commander{Folder: folder, Out: &out, Assume: true, Interactive: false}

	if err := commander.Run(context.Background(), script, nil); err != nil {
		t.Fatal(err)
	}
	if !ran(proof) {
		t.Fatal("--allow-commands should have run it")
	}
	// Still shown, even when not asked about: the user should be able to
	// read afterwards what was run on their behalf.
	if !strings.Contains(out.String(), script) {
		t.Fatalf("output did not show the script:\n%s", out.String())
	}
}

func TestCommandReportsANonZeroExit(t *testing.T) {
	folder := commandFolder(t)
	var out bytes.Buffer
	commander := &Commander{Folder: folder, Out: &out, In: strings.NewReader("y\n"), Interactive: true}

	err := commander.Run(context.Background(), "exit 7", nil)
	if err == nil {
		t.Fatal("a failing command should be an error")
	}
	if !strings.Contains(out.String(), "exit 7") {
		t.Fatalf("the closing line should give the status:\n%s", out.String())
	}
}

func TestAffirmativeIsStrict(t *testing.T) {
	for _, yes := range []string{"y", "Y", "yes", " YES \n"} {
		if !affirmative(yes) {
			t.Fatalf("%q should be a yes", yes)
		}
	}
	for _, no := range []string{"", "n", "no", "ye", "yep", "1", "true", "y y"} {
		if affirmative(no) {
			t.Fatalf("%q should not be a yes", no)
		}
	}
}
