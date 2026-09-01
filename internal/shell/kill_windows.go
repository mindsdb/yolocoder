//go:build windows

package shell

import (
	"os/exec"
	"strconv"
)

// killGroup ends the process tree. Windows has no process groups to
// signal, so this asks taskkill to walk the tree instead.
func killGroup(pid int) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
