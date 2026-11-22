//go:build !windows

package web

import (
	"os/exec"
	"os/signal"
	"syscall"
)

// ignoreTerminalStops makes reading or configuring the terminal from the
// background fail with an error rather than suspend this process.
func ignoreTerminalStops() {
	signal.Ignore(syscall.SIGTTIN, syscall.SIGTTOU)
}

// detachFromTerminal starts command in a new session, which has no
// controlling terminal.
func detachFromTerminal(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
