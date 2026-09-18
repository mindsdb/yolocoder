package web

import (
	"sync"
	"testing"
	"time"
)

func TestLooksLikeError(t *testing.T) {
	cases := map[string]bool{
		"GET /api/hello 200 3ms":                                false,
		"Server listening on http://localhost:3001":             false,
		"TypeError: Cannot read properties of undefined":        true,
		"Uncaught ReferenceError: foo is not defined":           true,
		"    at Object.<anonymous> (/app/server/index.ts:12:5)": true,
		"panic: runtime error: index out of range":              true,
		"UnhandledPromiseRejectionWarning: rejected":            true,
		"An exception occurred while handling the request":      true,
	}
	for line, want := range cases {
		if got := looksLikeError(line); got != want {
			t.Errorf("looksLikeError(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestErrorWatcherCoalescesABurstIntoOneReport(t *testing.T) {
	var mutex sync.Mutex
	var fired []string
	watcher := newErrorWatcher(func(text string) {
		mutex.Lock()
		fired = append(fired, text)
		mutex.Unlock()
	})
	watcher.debounce = 30 * time.Millisecond

	watcher.Feed("this is a normal log line")
	watcher.Feed("TypeError: boom")
	watcher.Feed("    at handler (/app/server/index.ts:9:3)")
	watcher.Feed("    at run (/app/server/index.ts:20:1)")

	time.Sleep(100 * time.Millisecond)

	mutex.Lock()
	defer mutex.Unlock()
	if len(fired) != 1 {
		t.Fatalf("got %d reports, want 1: %v", len(fired), fired)
	}
	if got := fired[0]; got == "" || got == "this is a normal log line" {
		t.Fatalf("report should start from the error line, not the earlier ordinary one; got %q", got)
	}
}

func TestErrorWatcherIgnoresOrdinaryOutput(t *testing.T) {
	fired := false
	watcher := newErrorWatcher(func(string) { fired = true })
	watcher.debounce = 20 * time.Millisecond
	watcher.Feed("GET / 200 1ms")
	watcher.Feed("ready in 300ms")
	time.Sleep(60 * time.Millisecond)
	if fired {
		t.Fatal("watcher fired on output with no error in it")
	}
}

func TestAutoFixGuardStopsAfterMaxAttempts(t *testing.T) {
	var guard autoFixGuard
	sig := signature("TypeError: boom\nat foo\nat bar")
	for attempt := 1; attempt <= maxAutoFixAttempts; attempt++ {
		if !guard.allow(sig) {
			t.Fatalf("attempt %d should still be allowed", attempt)
		}
	}
	if guard.allow(sig) {
		t.Fatal("attempt beyond the max should not be allowed")
	}
}

func TestAutoFixGuardResetsOnDifferentSignature(t *testing.T) {
	var guard autoFixGuard
	first := signature("TypeError: boom\nat foo")
	second := signature("ReferenceError: bar is not defined\nat baz")
	for i := 0; i < maxAutoFixAttempts; i++ {
		guard.allow(first)
	}
	if !guard.allow(second) {
		t.Fatal("a different error should get its own fresh attempts")
	}
}

func TestAutoFixGuardReset(t *testing.T) {
	var guard autoFixGuard
	sig := signature("TypeError: boom")
	for i := 0; i < maxAutoFixAttempts; i++ {
		guard.allow(sig)
	}
	guard.reset()
	if !guard.allow(sig) {
		t.Fatal("reset should give the same signature a fresh set of attempts")
	}
}

func TestSignatureIgnoresVolatileDetail(t *testing.T) {
	first := signature("Error: boom\n    at foo (/app/index.ts:12:4)\n    at bar (/app/index.ts:20:1)\nsome trailing detail that changes")
	second := signature("Error: boom\n    at foo (/app/index.ts:12:4)\n    at bar (/app/index.ts:20:1)\na different trailing detail")
	if first != second {
		t.Fatal("signature should be stable across lines beyond the first few")
	}
	third := signature("Error: something else entirely\n    at foo (/app/index.ts:12:4)")
	if first == third {
		t.Fatal("a genuinely different error should not share a signature")
	}
}

func TestAnErrorArrivingJustAfterAFixIsTreatedAsStale(t *testing.T) {
	// Seen on a real run: a turn renamed a title, broke a JSX line on one
	// edit and repaired it on the next, and finished clean. Vite had
	// compiled the broken moment and logged a parse error. One second
	// after the turn ended that error arrived, busy was already false,
	// and a second turn "fixed" a file that was no longer broken.
	server := &Server{hub: newHub()}
	if server.settling() {
		t.Fatal("a server that has changed nothing should believe every error")
	}

	server.reload()
	if !server.settling() {
		t.Fatal("straight after a change, the page has not re-run yet — errors are about the old code")
	}

	// The window is a window, not a latch.
	server.stateMutex.Lock()
	server.settleUntil = time.Now().Add(-time.Millisecond)
	server.stateMutex.Unlock()
	if server.settling() {
		t.Fatal("once the window passes, errors are believed again")
	}
}

func TestSettlingDoesNotSwallowAPersistentError(t *testing.T) {
	// A real error introduced by the change keeps being thrown, so
	// dropping one report costs nothing. This just pins that the guard is
	// time-based rather than per-incident.
	server := &Server{hub: newHub()}
	server.reload()
	if !server.settling() {
		t.Fatal("expected to be settling")
	}
	server.stateMutex.Lock()
	server.settleUntil = time.Now().Add(-time.Second)
	server.stateMutex.Unlock()
	if server.settling() {
		t.Fatal("the same server should accept the next throw of the same error")
	}
}
