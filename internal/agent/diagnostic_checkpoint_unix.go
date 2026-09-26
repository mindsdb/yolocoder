//go:build darwin || linux

package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func runCheckpoint(ctx context.Context, root, name string, args []string) (string, string) {
	if ctx.Err() != nil {
		return "unfinished", "Diagnostic canceled before starting."
	}
	executable, err := exec.LookPath(name)
	if err != nil {
		return "unfinished", "Could not find project check: " + err.Error()
	}
	resultRead, resultWrite, err := os.Pipe()
	if err != nil {
		return "unfinished", err.Error()
	}
	defer resultRead.Close()
	defer resultWrite.Close()
	holdRead, holdWrite, err := os.Pipe()
	if err != nil {
		return "unfinished", err.Error()
	}
	defer holdRead.Close()
	defer holdWrite.Close()
	// Hold the group leader alive after the check exits. This lets us kill any
	// background descendants BEFORE reaping, never targeting a reused group ID.
	// The check gets neither the private status descriptor nor supervisor stdin.
	command := exec.Command("/bin/sh", append([]string{"-c", `"$@" </dev/null 3>&-; code=$?; printf '%s\n' "$code" >&3; read -r ignored`, "checkpoint", executable}, args...)...)
	command.Dir = root
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdin = holdRead
	command.ExtraFiles = []*os.File{resultWrite}
	command.WaitDelay = 100 * time.Millisecond
	output := &checkpointOutput{}
	command.Stdout, command.Stderr = output, output
	if err = command.Start(); err != nil {
		return "unfinished", "Could not start project check: " + err.Error()
	}
	resultWrite.Close()
	holdRead.Close()
	result := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(resultRead).ReadString('\n'); result <- strings.TrimSpace(line) }()
	status, detail := "unfinished", "Project check did not finish within the diagnostic deadline; this is not a failed check."
	select {
	case code := <-result:
		exit, parseErr := strconv.Atoi(code)
		if parseErr == nil && exit >= 0 && exit <= 255 {
			if exit == 0 {
				status, detail = "passed", ""
			} else {
				status, detail = "failed", fmt.Sprintf("Project check exited with status%d.\n", exit)
			}
		} else {
			detail = "Project check exit status was unavailable.\n"
		}
	case <-ctx.Done():
	}
	// The supervisor is waiting on our still-open stdin pipe, so it has not
	// exited/reaped. Kill the whole group, then collect all bounded output.
	cleanup := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	command.Wait()
	resultRead.Close()
	if cleanup != nil {
		return "unfinished", "Could not clean up project check: " + cleanup.Error() + "\n" + output.String()
	}
	if ctx.Err() != nil {
		status, detail = "unfinished", "Project check did not finish within the diagnostic deadline; this is not a failed check.\n"
	}
	return status, detail + output.String()
}
