//go:build windows

package shell

import (
	"errors"
	"os"
	"os/exec"
)

// errNoTerminal says a pseudo-terminal could not be had, and the caller
// should fall back to pipes.
var errNoTerminal = errors.New("no pseudo-terminal on this platform")

// startTerminal has no ConPTY implementation here. Saying so plainly and
// letting the caller fall back to pipes is better than a half-working
// one: the output is still captured and still supervised, the command is
// simply told it is not talking to a terminal.
func startTerminal(*exec.Cmd) (*os.File, error) {
	return nil, errNoTerminal
}
