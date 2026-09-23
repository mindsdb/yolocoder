package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

func runnerOn(t *testing.T, files map[string]string) *Runner {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Runner{repository: &repo.Repository{Root: root}, served: map[string]string{}}
}

func opening(t *testing.T, runner *Runner) string {
	t.Helper()
	session := runner.newChangeSession("rename the title", "a.ts\nb.ts", nil, nil)
	message, ok := session.transcript[0].(inputMessage)
	if !ok {
		t.Fatalf("first item is %T", session.transcript[0])
	}
	return message.Content
}

// Two traces spent a round trip fetching this file. It rides along now.
func TestTheArchitectureRidesAlongWithTheMap(t *testing.T) {
	runner := runnerOn(t, map[string]string{
		"ARCHITECTURE.md": "The frontend talks to the backend over /api.",
	})
	text := opening(t, runner)

	if !strings.Contains(text, "The frontend talks to the backend over /api.") {
		t.Fatalf("architecture missing from the opening:\n%s", text)
	}
	// Ahead of the task, where the prefix cache can hold it.
	if strings.Index(text, "ARCHITECTURE.md") > strings.Index(text, "TASK:") {
		t.Fatal("architecture came after the task, so no two turns share a prefix")
	}
}

// Sent once. A read_files for it afterwards costs a sentence, not
// another copy in a transcript that is resent every round.
func TestTheArchitectureCountsAsAlreadyShown(t *testing.T) {
	runner := runnerOn(t, map[string]string{"ARCHITECTURE.md": "Notes."})
	opening(t, runner)

	if !runner.alreadyShown("ARCHITECTURE.md") {
		t.Fatal("not recorded as shown, so the model can be sent it twice")
	}
	answer, err := runner.readFiles([]string{"ARCHITECTURE.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "unchanged since it was shown above") {
		t.Fatalf("resent instead of noted:\n%s", answer)
	}
}

// An edit to it has to be visible, or the model repairs against text
// that is no longer there.
func TestAnEditedArchitectureIsNotStillCountedAsShown(t *testing.T) {
	runner := runnerOn(t, map[string]string{"ARCHITECTURE.md": "Before."})
	opening(t, runner)
	if err := os.WriteFile(filepath.Join(runner.repository.Root, "ARCHITECTURE.md"), []byte("After."), 0o644); err != nil {
		t.Fatal(err)
	}
	if runner.alreadyShown("ARCHITECTURE.md") {
		t.Fatal("a changed file still counts as shown")
	}
}

func TestAFolderWithNoArchitectureIsUnchanged(t *testing.T) {
	runner := runnerOn(t, map[string]string{"a.ts": "x"})
	text := opening(t, runner)
	if strings.Contains(text, "account of how it fits together") {
		t.Fatalf("invented an architecture section:\n%s", text)
	}
}

// Half an architecture note is worse than none: what is missing does
// not announce itself.
func TestAHugeArchitectureIsSkippedRatherThanCut(t *testing.T) {
	runner := runnerOn(t, map[string]string{"ARCHITECTURE.md": strings.Repeat("x", 9<<10)})
	text := opening(t, runner)
	if strings.Contains(text, "ARCHITECTURE.md") {
		t.Fatal("a 9 KiB architecture was carried anyway")
	}
	if runner.alreadyShown("ARCHITECTURE.md") {
		t.Fatal("marked as shown without being sent")
	}
}

func TestAnEmptyArchitectureIsIgnored(t *testing.T) {
	runner := runnerOn(t, map[string]string{"ARCHITECTURE.md": ""})
	if strings.Contains(opening(t, runner), "ARCHITECTURE.md") {
		t.Fatal("carried an empty file")
	}
}
