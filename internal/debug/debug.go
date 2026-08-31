// Package debug writes a plain-text trace of what YoloCoder sent to the
// model and got back, plus what Git said about each patch. When a provider
// returns something unexpected, this is the difference between guessing
// and reading the actual exchange.
//
// Set YOLOCODER_DEBUG_LOG to a file path, or to 1 to use the default path
// under the config directory. Logging is off otherwise, and every function
// here is a no-op.
package debug

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const PathEnv = "YOLOCODER_DEBUG_LOG"

var (
	once  sync.Once
	mutex sync.Mutex
	file  *os.File
	path  string

	sinkMutex sync.RWMutex
	sink      func(title, body string)
)

// SetSink registers a destination for trace entries in addition to the
// file, so a session can show them live. Passing nil turns that off.
func SetSink(destination func(title, body string)) {
	sinkMutex.Lock()
	defer sinkMutex.Unlock()
	sink = destination
}

// SinkEnabled reports whether entries are being shown live.
func SinkEnabled() bool {
	sinkMutex.RLock()
	defer sinkMutex.RUnlock()
	return sink != nil
}

func currentSink() func(title, body string) {
	sinkMutex.RLock()
	defer sinkMutex.RUnlock()
	return sink
}

// Path is the file being written, or "" when logging is off.
func Path() string {
	once.Do(open)
	return path
}

func open() {
	target := strings.TrimSpace(os.Getenv(PathEnv))
	if target == "" {
		return
	}
	if target == "1" || strings.EqualFold(target, "true") {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "debug log: %v\n", err)
			return
		}
		directory := filepath.Join(home, ".config", "yolocoder")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "debug log: %v\n", err)
			return
		}
		target = filepath.Join(directory, "debug.log")
	}
	// 0600: the trace carries the contents of the files being worked on.
	handle, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "debug log: %v\n", err)
		return
	}
	file, path = handle, target
	fmt.Fprintf(file, "\n===== yolocoder started %s =====\n", time.Now().Format(time.RFC3339))
}

// Log records a titled section: to the file when one is configured, and
// to the live sink when a session has asked to see the trace.
func Log(title, body string) {
	once.Do(open)
	if file != nil {
		mutex.Lock()
		fmt.Fprintf(file, "\n--- %s [%s] ---\n%s\n", title, time.Now().Format("15:04:05"), body)
		mutex.Unlock()
	}
	if destination := currentSink(); destination != nil {
		destination(title, body)
	}
}

// Logf records a titled section built from a format string.
func Logf(title, format string, args ...any) {
	if !Active() {
		return
	}
	Log(title, fmt.Sprintf(format, args...))
}

// Active reports whether anything is listening, so callers can skip
// assembling an expensive message.
func Active() bool {
	return Path() != "" || SinkEnabled()
}
