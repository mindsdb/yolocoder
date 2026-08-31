package debug

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// reset clears the package's one-time state so each test starts fresh.
func reset(t *testing.T) {
	t.Helper()
	mutex.Lock()
	if file != nil {
		file.Close()
	}
	file, path = nil, ""
	once = sync.Once{}
	mutex.Unlock()
	SetSink(nil)
	t.Cleanup(func() {
		mutex.Lock()
		if file != nil {
			file.Close()
		}
		file, path = nil, ""
		once = sync.Once{}
		mutex.Unlock()
		SetSink(nil)
	})
}

func TestLogIsInertWhenNotConfigured(t *testing.T) {
	reset(t)
	t.Setenv(PathEnv, "")
	if Active() {
		t.Fatal("expected logging to be off")
	}
	Log("REQUEST", "body") // must not panic or write anywhere
}

func TestLogWritesToTheConfiguredFile(t *testing.T) {
	reset(t)
	target := filepath.Join(t.TempDir(), "trace.log")
	t.Setenv(PathEnv, target)

	Log("RESPONSE unified_patch", `{"diff":"..."}`)
	if Path() != target {
		t.Fatalf("Path() = %q, want %q", Path(), target)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RESPONSE unified_patch", `{"diff":"..."}`} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("trace missing %q:\n%s", want, content)
		}
	}
}

func TestSinkReceivesEntriesWithoutAFile(t *testing.T) {
	reset(t)
	t.Setenv(PathEnv, "")
	var titles, bodies []string
	SetSink(func(title, body string) {
		titles = append(titles, title)
		bodies = append(bodies, body)
	})
	if !Active() || !SinkEnabled() {
		t.Fatal("expected the sink to count as active")
	}
	Log("PATCH FAILED", "does not apply")
	if len(titles) != 1 || titles[0] != "PATCH FAILED" || bodies[0] != "does not apply" {
		t.Fatalf("sink got %v / %v", titles, bodies)
	}
	SetSink(nil)
	Log("IGNORED", "after the sink is cleared")
	if len(titles) != 1 {
		t.Fatalf("sink still receiving after being cleared: %v", titles)
	}
}
