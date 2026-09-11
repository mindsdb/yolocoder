package web

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// States the dev server can be in, published to the UI as they change.
const (
	stateStopped  = "stopped"
	stateStarting = "starting"
	stateRunning  = "running"
	stateError    = "error"
)

// stateDir is where a running dev server reports itself, by a convention
// shared with scripts/{start,restart,stop}.sh: hand-authored for a
// scaffolded project, agent-authored for an existing one, but either way
// start.sh writes its port here once bound, and its combined output goes
// to server.log.
const stateDir = ".yolocoder/web"

const portTimeout = 30 * time.Second

// process owns the dev server's lifecycle. It never runs the dev command
// itself: it runs scripts/{start,restart,stop}.sh, the same scripts the
// UI's buttons call and a person could run by hand from a terminal, and
// discovers the outcome (port, log) through the state directory those
// scripts write to.
type process struct {
	root    string
	hub     *hub
	watcher *errorWatcher

	mutex      sync.Mutex
	state      string
	port       int
	cancelTail context.CancelFunc
}

func newProcess(root string, hub *hub, watcher *errorWatcher) *process {
	return &process{root: root, hub: hub, watcher: watcher, state: stateStopped}
}

func (proc *process) Port() int {
	proc.mutex.Lock()
	defer proc.mutex.Unlock()
	return proc.port
}

func (proc *process) State() string {
	proc.mutex.Lock()
	defer proc.mutex.Unlock()
	return proc.state
}

func (proc *process) Start(ctx context.Context) error   { return proc.run(ctx, "start.sh") }
func (proc *process) Restart(ctx context.Context) error { return proc.run(ctx, "restart.sh") }

func (proc *process) Stop(ctx context.Context) error {
	err := proc.runScript(ctx, "stop.sh")
	proc.stopTail()
	proc.setState(stateStopped, 0)
	return err
}

func (proc *process) run(ctx context.Context, script string) error {
	proc.setState(stateStarting, 0)
	if err := proc.runScript(ctx, script); err != nil {
		proc.setState(stateError, 0)
		return fmt.Errorf("%s: %w", script, err)
	}
	port, err := proc.waitForPort(ctx)
	if err != nil {
		proc.setState(stateError, 0)
		return err
	}
	proc.stopTail()
	proc.startTail()
	proc.setState(stateRunning, port)
	return nil
}

func (proc *process) runScript(ctx context.Context, script string) error {
	relative := filepath.Join("scripts", script)
	if _, err := os.Stat(filepath.Join(proc.root, relative)); err != nil {
		return fmt.Errorf("%s not found", relative)
	}
	command := exec.CommandContext(ctx, "sh", relative)
	command.Dir = proc.root
	output, err := command.CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		if line != "" {
			proc.hub.publish("serverlog", map[string]string{"text": line})
		}
	}
	if err != nil {
		return fmt.Errorf("%s\n%s", err, output)
	}
	return nil
}

// waitForPort polls for start.sh (or restart.sh) to report the port it
// bound. A fixed poll rather than a filesystem watcher: this only runs a
// few times per session, and it keeps the package dependency-free.
func (proc *process) waitForPort(ctx context.Context) (int, error) {
	portFile := filepath.Join(proc.root, stateDir, "port")
	deadline := time.Now().Add(portTimeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(portFile); err == nil {
			if port, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && port > 0 {
				return port, nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return 0, fmt.Errorf("timed out waiting for %s to report its port (is scripts/start.sh writing it?)", portFile)
}

func (proc *process) startTail() {
	tailCtx, cancel := context.WithCancel(context.Background())
	proc.cancelTail = cancel
	go proc.tail(tailCtx)
}

func (proc *process) stopTail() {
	if proc.cancelTail != nil {
		proc.cancelTail()
		proc.cancelTail = nil
	}
}

// tail streams new lines from server.log as they're written, feeding each
// one to both the UI (as a serverlog event) and the error watcher. It
// re-reads from the top when the file shrinks, which is what a fresh
// start.sh truncating its own log looks like.
func (proc *process) tail(ctx context.Context) {
	path := filepath.Join(proc.root, stateDir, "server.log")
	var offset int64
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		proc.readNewLines(path, &offset)
	}
}

func (proc *process) readNewLines(path string, offset *int64) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return
	}
	if info.Size() < *offset {
		*offset = 0
	}
	if info.Size() <= *offset {
		return
	}
	if _, err := file.Seek(*offset, 0); err != nil {
		return
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		proc.hub.publish("serverlog", map[string]string{"text": line})
		proc.watcher.Feed(line)
	}
	*offset = info.Size()
}

func (proc *process) setState(state string, port int) {
	proc.mutex.Lock()
	proc.state = state
	if port > 0 || state == stateStopped {
		proc.port = port
	}
	current := proc.port
	proc.mutex.Unlock()
	proc.hub.publish("process", map[string]any{"state": state, "port": current})
}

// scriptsExist reports whether root already has the standard
// scripts/{start,restart,stop}.sh trio.
func scriptsExist(root string) bool {
	for _, name := range []string{"start.sh", "restart.sh", "stop.sh"} {
		if _, err := os.Stat(filepath.Join(root, "scripts", name)); err != nil {
			return false
		}
	}
	return true
}
