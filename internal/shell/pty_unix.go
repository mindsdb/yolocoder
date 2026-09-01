//go:build !windows

package shell

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// startTerminal runs the command on a pseudo-terminal.
//
// This is what makes a command behave the way it would if the user had
// typed it themselves: progress bars redraw, colour survives, and
// anything that asks whether it is talking to a terminal gets the answer
// it expects. It also means everything the command writes arrives on one
// handle, interleaved exactly as it was written, rather than as two pipes
// whose ordering against each other is anyone's guess.
func startTerminal(command *exec.Cmd) (*os.File, error) {
	terminal, err := pty.Start(command)
	if err != nil {
		return nil, err
	}
	// Match the real terminal, so the command wraps its output where the
	// user can read it rather than at a default 80 columns.
	if size, err := pty.GetsizeFull(os.Stdout); err == nil {
		_ = pty.Setsize(terminal, size)
	}
	return terminal, nil
}
