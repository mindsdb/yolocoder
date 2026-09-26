package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

func sessionOn(t *testing.T, name, content string) *changeSession {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{repository: &repo.Repository{Root: root}, served: map[string]string{}}
	return &changeSession{runner: runner, used: map[string]int{}}
}

func applyWith(t *testing.T, session *changeSession, arguments map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	output, _, _ := session.applyDiff(responseItem{Name: "apply_diff", Arguments: string(encoded)})
	return output
}

// The flag is the signal the model is asked for.
func TestAFinishedEditEndsTheTurn(t *testing.T) {
	session := sessionOn(t, "a.css", ".a {\n  color: red;\n}\n")
	applyWith(t, session, map[string]any{
		"patch":                     "@a.css\n-  color: red;\n+  color: blue;\n",
		"task_complete":             true,
		"response_comment_for_user": "Made it blue.",
	})
	if session.closing != "Made it blue." {
		t.Fatalf("closing = %q, so the turn would spend a round saying done", session.closing)
	}
}

// A closing note alone must not end an unfinished normal task.
func TestAReplyWithoutTheFlagDoesNotCompleteTheTask(t *testing.T) {
	session := sessionOn(t, "a.css", ".a {\n  color: red;\n}\n")
	applyWith(t, session, map[string]any{
		"patch":                     "@a.css\n-  color: red;\n+  color: blue;\n",
		"task_complete":             false,
		"response_comment_for_user": "Made it blue.",
	})
	if session.complete {
		t.Fatal("closing note completed an unfinished task")
	}
}

func TestAnUnfinishedEditDoesNotEndTheTurn(t *testing.T) {
	session := sessionOn(t, "a.css", ".a {\n  color: red;\n}\n")
	applyWith(t, session, map[string]any{
		"patch":                     "@a.css\n-  color: red;\n+  color: blue;\n",
		"task_complete":             false,
		"response_comment_for_user": "",
	})
	if session.closing != "" {
		t.Fatalf("closing = %q, ending a turn with work left", session.closing)
	}
}

// A note on a patch that was rejected would end the turn on a promise.
func TestAFailedEditCannotEndTheTurn(t *testing.T) {
	session := sessionOn(t, "a.css", ".a {\n  color: red;\n}\n")
	applyWith(t, session, map[string]any{
		"patch":                     "@a.css\n-  color: nowhere;\n+  color: blue;\n",
		"task_complete":             true,
		"response_comment_for_user": "Made it blue.",
	})
	if session.closing != "" {
		t.Fatalf("a rejected patch ended the turn with %q", session.closing)
	}
}
