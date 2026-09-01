//go:build !windows

package shell

import "syscall"

// killGroup signals everything the script started. pty.Start puts the
// command in its own session, so the group id is the command's pid and
// one signal reaches the whole tree; without this, killing the shell
// would leave whatever it had spawned running.
func killGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
