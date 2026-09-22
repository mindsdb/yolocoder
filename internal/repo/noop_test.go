package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// From a real run. The model pointed at where its next edit went with a
// context-only hunk; the selector appeared twice in the stylesheet, so
// that no-op was rejected as ambiguous and took four real edits down
// with it. Two further rounds went on repairing something that was
// never an edit.
func TestAContextOnlyHunkDoesNotKillThePatch(t *testing.T) {
	root := t.TempDir()
	css := strings.Join([]string{
		".game-intro-screen {",
		"  image-rendering: pixelated;",
		"}",
		".game-intro-screen__panel {",
		"  box-shadow: 6px 6px 0 #071f07;",
		"}",
		".other {",
		"}",
		".game-intro-screen__panel {",
		"  color: #e0f8a0;",
		"}",
		"",
	}, "\n")
	path := filepath.Join(root, "index.css")
	if err := os.WriteFile(path, []byte(css), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}

	patch := strings.Join([]string{
		"@index.css",
		" .game-intro-screen {",
		"@@",
		"  image-rendering: pixelated;",
		"+ text-shadow: 2px 0 0 rgba(15, 56, 15, .35);",
		"@@",
		" .game-intro-screen__panel {",
		"@@",
		"  box-shadow: 6px 6px 0 #071f07;",
		"+ image-rendering: pixelated;",
		"",
	}, "\n")

	if err := repository.Apply(patch); err != nil {
		t.Fatalf("a patch whose only ambiguity was a no-op hunk was rejected:\n%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"text-shadow: 2px 0 0", "image-rendering: pixelated;\n}"} {
		if !strings.Contains(string(after), want) {
			t.Fatalf("edit missing %q:\n%s", want, after)
		}
	}
}

// The no-op must not be mistaken for a change: a patch made of nothing
// but context has to still report that nothing happened.
func TestAPatchOfOnlyContextIsNotCountedAsApplied(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.css")
	if err := os.WriteFile(path, []byte(".a {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}

	err := repository.Apply("@a.css\n .a {\n }\n")
	if err == nil {
		t.Fatal("a patch that changes nothing reported success")
	}
}
