package web

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func testProcess(t *testing.T) *process {
	t.Helper()
	return newProcess(t.TempDir(), newHub(), newErrorWatcher(func(string) {}))
}

func TestWatchHealthWaitsForConsecutiveFailuresBeforeActing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close() // dead from the start, standing in for a crashed dev server

	proc := testProcess(t)
	proc.healthCheckInterval = 20 * time.Millisecond
	proc.healthCheckDialTimeout = 50 * time.Millisecond
	proc.maxHealthFailures = 2

	var calls int32
	done := make(chan struct{})
	proc.onCrash = func() {
		atomic.AddInt32(&calls, 1)
		close(done)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go proc.watchHealth(ctx, port)

	// After only one failed check it shouldn't have decided anything —
	// maxHealthFailures asks for two in a row before acting.
	time.Sleep(30 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("onCrash fired after a single failed check, want it to wait for a second")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onCrash never fired for a port that never came back")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("onCrash called %d times, want exactly 1", got)
	}
}

func TestWatchHealthDoesNotActOnAHealthyPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port

	proc := testProcess(t)
	proc.healthCheckInterval = 10 * time.Millisecond
	proc.healthCheckDialTimeout = 50 * time.Millisecond
	proc.maxHealthFailures = 2

	var calls int32
	proc.onCrash = func() { atomic.AddInt32(&calls, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	go proc.watchHealth(ctx, port)
	time.Sleep(100 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond) // let the goroutine actually exit before asserting

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("onCrash called %d times against a healthy port, want 0", got)
	}
}

func TestNextRecoveryAttemptStaysWithinBudget(t *testing.T) {
	proc := testProcess(t)
	proc.maxRecoverAttempts = 3
	proc.recoveryWindow = time.Hour // long enough not to reset mid-test

	for i := 1; i <= 3; i++ {
		attempt, ok := proc.nextRecoveryAttempt()
		if attempt != i || !ok {
			t.Fatalf("attempt %d: got (%d, %v), want (%d, true)", i, attempt, ok, i)
		}
	}
	if attempt, ok := proc.nextRecoveryAttempt(); ok {
		t.Fatalf("attempt %d should be over budget, got ok=true", attempt)
	}
}

func TestNextRecoveryAttemptResetsAfterTheWindowPasses(t *testing.T) {
	proc := testProcess(t)
	proc.maxRecoverAttempts = 1
	proc.recoveryWindow = 30 * time.Millisecond

	if attempt, ok := proc.nextRecoveryAttempt(); attempt != 1 || !ok {
		t.Fatalf("first attempt = (%d, %v), want (1, true)", attempt, ok)
	}
	if _, ok := proc.nextRecoveryAttempt(); ok {
		t.Fatal("a second attempt within the window should be over budget")
	}
	time.Sleep(40 * time.Millisecond)
	if attempt, ok := proc.nextRecoveryAttempt(); attempt != 1 || !ok {
		t.Fatalf("attempt after the window passed = (%d, %v), want (1, true) again, a fresh loop", attempt, ok)
	}
}

func TestDevServerIsReachedByNameNotByIPv4(t *testing.T) {
	// Vite binds IPv6 loopback only. A dial at 127.0.0.1 then looks
	// exactly like a server that never started — "connection refused",
	// retried every couple of seconds, forever, against a dev server that
	// was answering the whole time. The name resolves to both families.
	if got := devServer(5173); got != "localhost:5173" {
		t.Fatalf("devServer(5173) = %q, want a name rather than an address family", got)
	}
}

func TestAServerOnIPv6LoopbackIsReachable(t *testing.T) {
	// The real shape of the bug, end to end: listen on [::1] only, then
	// dial the way the health check and the proxy do.
	listener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback on this machine")
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	conn, err := net.DialTimeout("tcp", devServer(port), 2*time.Second)
	if err != nil {
		t.Fatalf("a server on [::1] should be reachable: %v", err)
	}
	conn.Close()

	// And the dial that was there before is exactly the one that fails.
	if direct, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
		direct.Close()
		t.Skip("this machine maps 127.0.0.1 to the IPv6 listener too; the bug needs a stricter stack to show")
	}
}
