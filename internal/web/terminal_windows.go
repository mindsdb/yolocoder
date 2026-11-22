package web

import "os/exec"

// Windows has no job-control signals or controlling terminals to keep
// away from, so both are no-ops there.
func ignoreTerminalStops() {}

func detachFromTerminal(*exec.Cmd) {}
