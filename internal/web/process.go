package web

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mindsdb/yolocoder/internal/config"
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

// Health-check tuning: how often to check, how long a single dial may
// take, and how many checks in a row must fail before treating the dev
// server as actually dead rather than just briefly slow to answer.
const (
	healthCheckInterval          = 3 * time.Second
	healthCheckDialTimeout       = 2 * time.Second
	maxConsecutiveHealthFailures = 2
)

// maxAutoRecoverAttempts bounds how many times a crash is restarted from
// within recoveryWindow of the last one, so a dev server that can't stay
// up at all doesn't get restarted forever; a crash isolated by more than
// that resets the count, since it's not part of the same loop.
const (
	maxAutoRecoverAttempts = 3
	recoveryWindow         = 2 * time.Minute
)

// process owns the dev server's lifecycle. It never runs the dev command
// itself: it runs scripts/{start,restart,stop}.sh, the same scripts the
// UI's buttons call and a person could run by hand from a terminal, and
// discovers the outcome (port, log) through the state directory those
// scripts write to.
type process struct {
	root string
	hub  *hub
	// inference reports the connected provider as environment entries,
	// read fresh on every start because the model can change mid-session.
	inference func() []string
	watcher   *errorWatcher

	// execMutex serializes actual script runs: Start/Restart/Stop calls
	// arriving from an HTTP handler, and now also a health check
	// recovering from a crash on its own, must not overlap the same
	// scripts/pidfile/log at once.
	execMutex sync.Mutex

	mutex        sync.Mutex
	state        string
	port         int
	cancelTail   context.CancelFunc
	cancelHealth context.CancelFunc

	recoverAttempts int
	lastRecoveryAt  time.Time

	// Overridable so tests can run the health-check loop and the crash-
	// loop budget on a timescale of milliseconds instead of minutes,
	// rather than either skipping this logic entirely or making the
	// suite slow.
	healthCheckInterval    time.Duration
	healthCheckDialTimeout time.Duration
	maxHealthFailures      int
	maxRecoverAttempts     int
	recoveryWindow         time.Duration

	// onCrash runs when the health check gives up on a port ever
	// answering again; defaults to recoverFromCrash, and is a plain
	// field so a test can replace the actual restart with something
	// that doesn't need real scripts to observe the detection logic in
	// isolation.
	onCrash func()
}

// inferenceEnv is a provider in the variable names every OpenAI client
// already reads without being told: the SDK finds them with no arguments,
// and so does backend/llm.ts. Naming them after the provider instead
// would be a lie the moment someone connects a different one.
func inferenceEnv(provider config.LLM) []string {
	var env []string
	if key := strings.TrimSpace(provider.APIKey); key != "" {
		env = append(env, "OPENAI_API_KEY="+key)
	}
	if base := versionedBase(provider.BaseURL); base != "" {
		env = append(env, "OPENAI_BASE_URL="+base)
	}
	if model := strings.TrimSpace(provider.Model); model != "" {
		env = append(env, "OPENAI_MODEL="+model)
	}
	return env
}

// versionedBase is a base URL with the version segment a client expects
// to append paths to. The saved provider holds the host on its own
// ("https://api.mindshub.ai"), which is right for a config file and
// wrong for anything that concatenates "/chat/completions" onto it.
func versionedBase(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" || strings.HasSuffix(trimmed, "/v1") {
		return trimmed
	}
	return trimmed + "/v1"
}

func newProcess(root string, hub *hub, watcher *errorWatcher) *process {
	proc := &process{
		root: root, hub: hub, watcher: watcher, state: stateStopped,
		healthCheckInterval:    healthCheckInterval,
		healthCheckDialTimeout: healthCheckDialTimeout,
		maxHealthFailures:      maxConsecutiveHealthFailures,
		maxRecoverAttempts:     maxAutoRecoverAttempts,
		recoveryWindow:         recoveryWindow,
	}
	proc.onCrash = proc.recoverFromCrash
	return proc
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
	proc.execMutex.Lock()
	defer proc.execMutex.Unlock()
	err := proc.runScript(ctx, "stop.sh")
	proc.stopTail()
	proc.stopHealthCheck()
	proc.setState(stateStopped, 0)
	return err
}

func (proc *process) run(ctx context.Context, script string) error {
	proc.execMutex.Lock()
	defer proc.execMutex.Unlock()
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
	proc.stopHealthCheck()
	proc.startHealthCheck(port)
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
	// The dev server inherits our environment, plus the connected
	// provider. Appended rather than prepended: a later entry wins, so
	// these override an OPENAI_* already in the environment, which is
	// where the provider came from in the first place when one is.
	if proc.inference != nil {
		command.Env = append(os.Environ(), proc.inference()...)
	}
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

// startHealthCheck watches for the dev server dying on its own — a
// crash, an OS resource limit, anything that isn't a chat task's doing —
// which nothing else here would ever notice: the error watcher only sees
// text that reaches server.log, and a process that simply stops
// answering doesn't necessarily write anything on its way out.
func (proc *process) startHealthCheck(port int) {
	healthCtx, cancel := context.WithCancel(context.Background())
	proc.cancelHealth = cancel
	go proc.watchHealth(healthCtx, port)
}

func (proc *process) stopHealthCheck() {
	if proc.cancelHealth != nil {
		proc.cancelHealth()
		proc.cancelHealth = nil
	}
}

// devServer is the address the dev server is reached at.
//
// "localhost" rather than "127.0.0.1" because which of the two a dev
// server binds is not ours to decide and is not consistent: Vite binds
// IPv6 loopback only, so a process happily serving on [::1] looked, to a
// dial at 127.0.0.1, exactly like a process that had not started —
// "dev server is not reachable yet: dial tcp 127.0.0.1:5173: connection
// refused", retried every couple of seconds, forever, against a server
// that was answering the whole time. The name resolves to both families
// and Go tries them in turn.
//
// Listening is the other way round and stays as it is: where we choose
// the address, 127.0.0.1 is the right one to choose.
func devServer(port int) string {
	return fmt.Sprintf("localhost:%d", port)
}

func (proc *process) watchHealth(ctx context.Context, port int) {
	ticker := time.NewTicker(proc.healthCheckInterval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		conn, err := net.DialTimeout("tcp", devServer(port), proc.healthCheckDialTimeout)
		if err == nil {
			conn.Close()
			failures = 0
			continue
		}
		failures++
		if failures < proc.maxHealthFailures {
			continue
		}
		// It's stopped answering for several checks running rather than
		// having a bad moment; recover on its own account instead of
		// leaving the iframe on "not reachable" until someone notices.
		// This goroutine's job ends here either way: a successful
		// recovery starts a fresh one for the new instance, and a
		// recovery that gives up has nothing left to watch.
		proc.onCrash()
		return
	}
}

// nextRecoveryAttempt records one crash-recovery attempt and reports
// whether it's still within budget, resetting the count first if it's
// been long enough since the last one that this isn't part of the same
// crash loop. Kept separate from recoverFromCrash so the counting and
// resetting can be tested without needing a real process to restart.
func (proc *process) nextRecoveryAttempt() (attempt int, withinBudget bool) {
	proc.mutex.Lock()
	defer proc.mutex.Unlock()
	if time.Since(proc.lastRecoveryAt) > proc.recoveryWindow {
		proc.recoverAttempts = 0
	}
	proc.recoverAttempts++
	proc.lastRecoveryAt = time.Now()
	return proc.recoverAttempts, proc.recoverAttempts <= proc.maxRecoverAttempts
}

// recoverFromCrash restarts the dev server after it stops responding
// unasked, bounded by nextRecoveryAttempt so a dev server that can't
// stay up at all doesn't get restarted forever.
func (proc *process) recoverFromCrash() {
	attempt, withinBudget := proc.nextRecoveryAttempt()
	if !withinBudget {
		proc.hub.publish("chat", chatMessage{Role: "system", Text: fmt.Sprintf(
			"The dev server keeps crashing (%d times in the last %s) and yolocoder has stopped "+
				"restarting it automatically. Check .yolocoder/web/server.log for why, then use "+
				"POST /process/restart or the Restart button once it's fixed.",
			attempt-1, proc.recoveryWindow)})
		proc.setState(stateError, 0)
		return
	}
	proc.hub.publish("chat", chatMessage{Role: "system", Text: "The dev server stopped responding; restarting it automatically."})
	ctx, cancel := context.WithTimeout(context.Background(), portTimeout+10*time.Second)
	defer cancel()
	if err := proc.run(ctx, "restart.sh"); err != nil {
		proc.hub.publish("chat", chatMessage{Role: "system", Text: "Automatic restart failed: " + err.Error()})
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
