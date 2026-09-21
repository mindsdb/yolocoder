package web

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// debounce is how long the watcher waits after the last error-looking line
// before treating what it collected as one complete error. A stack trace
// arrives as a burst of lines, not one line at a time, so firing on the
// first line alone would hand the agent a truncated trace.
const debounce = 1500 * time.Millisecond

// errorWatcher turns a stream of server log lines into complete error
// reports. It only starts collecting once a line looks like an error, so
// ordinary request logs never trigger it.
type errorWatcher struct {
	mutex    sync.Mutex
	active   bool
	lines    []string
	timer    *time.Timer
	debounce time.Duration
	onFire   func(text string)
}

func newErrorWatcher(onFire func(string)) *errorWatcher {
	return &errorWatcher{onFire: onFire, debounce: debounce}
}

// Feed offers one line from the dev server's log. Lines outside an active
// error burst are dropped; once a burst starts, everything is kept until
// the debounce window closes so the trace stays intact.
func (watcher *errorWatcher) Feed(line string) {
	watcher.mutex.Lock()
	defer watcher.mutex.Unlock()
	if !watcher.active {
		if !looksLikeError(line) {
			return
		}
		watcher.active = true
		watcher.lines = nil
	}
	watcher.lines = append(watcher.lines, line)
	if watcher.timer != nil {
		watcher.timer.Stop()
	}
	watcher.timer = time.AfterFunc(watcher.debounce, watcher.flush)
}

func (watcher *errorWatcher) flush() {
	watcher.mutex.Lock()
	lines := watcher.lines
	watcher.active = false
	watcher.lines = nil
	watcher.mutex.Unlock()
	if len(lines) == 0 {
		return
	}
	watcher.onFire(strings.Join(lines, "\n"))
}

// looksLikeError is a deliberately loose heuristic: false positives cost
// one extra agent turn, but a missed error costs the whole point of
// watching the log in the first place, so it leans toward catching more.
func looksLikeError(line string) bool {
	lower := strings.ToLower(line)
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.Contains(line, "Error:"):
		return true
	case strings.Contains(lower, "uncaught"), strings.Contains(lower, "unhandled"):
		return true
	case strings.Contains(lower, "exception"):
		return true
	case strings.HasPrefix(trimmed, "at ") && strings.Contains(trimmed, "("):
		return true // a JS/TS stack frame, once a burst is already active
	case strings.Contains(lower, "panic:"):
		return true
	}
	return false
}

// isNonActionableBrowserError filters diagnostics and the proxy's brief
// reconnect state from /client-error. These are useful in DevTools, but they
// are not bugs an agent can fix: forwarding them would turn a slow frame or a
// normal dev-server restart into an ask-first card and invite a needless loop.
func isNonActionableBrowserError(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"[violation]",
		"canvas2d:",
		"willreadfrequently",
		"requestanimationframe",
		"forced reflow",
		"long task",
		"dev server is not reachable",
		"the dev server isn't running yet",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// autoFixGuard stops the same error from re-triggering an auto-fix
// indefinitely. The runner's own patch/test loop already retries a single
// attempt a few times; this bounds how many separate auto-fix *attempts*
// (each its own full task, each followed by a restart) the same recurring
// error gets before it's left for the user instead.
type autoFixGuard struct {
	mutex    sync.Mutex
	lastSig  string
	attempts int
}

const maxAutoFixAttempts = 2

// allow reports whether an error with this signature should still trigger
// an automatic fix, and records the attempt either way.
func (guard *autoFixGuard) allow(signature string) bool {
	guard.mutex.Lock()
	defer guard.mutex.Unlock()
	if signature != guard.lastSig {
		guard.lastSig = signature
		guard.attempts = 0
	}
	guard.attempts++
	return guard.attempts <= maxAutoFixAttempts
}

// reset clears the guard, used when the user sends a message of their own:
// that's a sign a human is now driving, so the next error deserves a fresh
// set of automatic attempts.
func (guard *autoFixGuard) reset() {
	guard.mutex.Lock()
	defer guard.mutex.Unlock()
	guard.lastSig = ""
	guard.attempts = 0
}

// signature reduces an error report to something stable enough to compare
// across occurrences (timestamps and line numbers in a repeated stack
// trace would otherwise make every occurrence look new).
func signature(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kept = append(kept, line)
		if len(kept) == 3 {
			break
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(kept, "\n")))
	return hex.EncodeToString(sum[:])
}
