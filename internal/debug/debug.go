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
	"encoding/json"
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

// Records are the small, always-on counterpart to the trace above.
//
// The trace is the whole exchange and has to be asked for, because one
// request runs to 160 KB and a session of them to tens of megabytes. So
// it is never on when something interesting happens, and the question
// "what went wrong last time?" has had no answer: the session log keeps
// that a patch was rejected three times, never what the rejections said.
//
// A record is one line of JSON for one event worth keeping — a rejected
// patch, so far. Small enough to leave on always, which is the only way
// it is there when it is wanted.
const recordsName = "records.jsonl"

// maxRecords caps the file rather than rotating it. This is diagnostic
// scratch, not an audit trail: when it fills, the oldest half goes and
// nobody should mind.
const maxRecords = 4 << 20

// Record appends one event. Failure is silent throughout — a diagnostic
// that can break a run is worse than no diagnostic.
func Record(event string, fields map[string]any) {
	if underTest() {
		return
	}
	path, err := recordsPath()
	if err != nil {
		return
	}
	entry := map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "event": event}
	for name, value := range fields {
		if text, ok := value.(string); ok && len(text) > 4096 {
			value = text[:4096] + "\n... truncated ..."
		}
		entry[name] = value
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	recordMutex.Lock()
	defer recordMutex.Unlock()
	if info, err := os.Stat(path); err == nil && info.Size() > maxRecords {
		trimRecords(path)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(line, '\n'))
}

// RecordsPath is where records are written, for anything that wants to
// read them back.
func RecordsPath() string {
	path, err := recordsPath()
	if err != nil {
		return ""
	}
	return path
}

func recordsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(home, ".config", "yolocoder")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(directory, recordsName), nil
}

// trimRecords drops the oldest half. Called with recordMutex held.
func trimRecords(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	_ = os.WriteFile(path, []byte(strings.Join(lines[len(lines)/2:], "\n")), 0o600)
}

// underTest keeps `go test ./...` out of the real config directory.
// Records are written from deep inside a turn, so every test that drives
// a turn writes one, and they landed in the developer's own records
// alongside the failures that actually matter. Tests that mean to
// exercise this set HOME first and say so.
func underTest() bool {
	if strings.HasSuffix(os.Args[0], ".test") {
		return os.Getenv("YOLOCODER_TEST_RECORDS") == ""
	}
	for _, argument := range os.Args[1:] {
		if strings.HasPrefix(argument, "-test.") {
			return os.Getenv("YOLOCODER_TEST_RECORDS") == ""
		}
	}
	return false
}

var recordMutex sync.Mutex

// Recent are the last few records, newest first, for showing someone.
// An unreadable or absent file is simply no records.
func Recent(limit int) []map[string]any {
	path, err := recordsPath()
	if err != nil {
		return nil
	}
	recordMutex.Lock()
	data, err := os.ReadFile(path)
	recordMutex.Unlock()
	if err != nil {
		return nil
	}
	var found []map[string]any
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for index := len(lines) - 1; index >= 0 && len(found) < limit; index-- {
		var entry map[string]any
		if json.Unmarshal([]byte(lines[index]), &entry) == nil {
			found = append(found, entry)
		}
	}
	return found
}
