package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

func folderOf(t *testing.T, files map[string]string) *Runner {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Runner{repository: &repo.Repository{Root: root}, served: map[string]string{}}
}

// The whole point: the files it knew about and the ones it did not, in
// one call rather than a search and then a read.
func TestAReadTakesNamedPathsAndMatchesTogether(t *testing.T) {
	runner := folderOf(t, map[string]string{
		"a.ts": "known file\n", "b.ts": "has accent here\n", "c.ts": "accent again\n", "d.ts": "unrelated\n",
	})
	paths, note := runner.pathsMatching(context.Background(), []string{"a.ts"}, "accent")

	for _, want := range []string{"a.ts", "b.ts", "c.ts"} {
		if !contains(paths, want) {
			t.Fatalf("missing %s from %v", want, paths)
		}
	}
	if contains(paths, "d.ts") {
		t.Fatalf("read a file that does not match: %v", paths)
	}
	if !strings.Contains(note, "b.ts") {
		t.Fatalf("the trail should say what it added: %q", note)
	}
}

func TestNoPatternReadsExactlyWhatWasNamed(t *testing.T) {
	runner := folderOf(t, map[string]string{"a.ts": "x\n", "b.ts": "x\n"})
	paths, note := runner.pathsMatching(context.Background(), []string{"a.ts"}, "")
	if len(paths) != 1 || paths[0] != "a.ts" || note != "" {
		t.Fatalf("paths=%v note=%q", paths, note)
	}
}

// A pattern that matches half the repository degrades to what a search
// would have given, rather than reading everything or failing.
func TestAPatternMatchingTooMuchComesBackAsAList(t *testing.T) {
	files := map[string]string{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m"} {
		files[name+".ts"] = "common\n"
	}
	runner := folderOf(t, files)
	paths, note := runner.pathsMatching(context.Background(), nil, "common")

	if len(paths) != 0 {
		t.Fatalf("read %d files for a pattern that matched everything", len(paths))
	}
	if !strings.Contains(note, "too many to read at once") || !strings.Contains(note, "a.ts") {
		t.Fatalf("expected the match list back, got %q", note)
	}
}

// Every copy is resent on every later round, so a file already shown is
// not pulled in again by a pattern.
func TestAPatternDoesNotResendAFileAlreadyShown(t *testing.T) {
	runner := folderOf(t, map[string]string{"a.ts": "accent\n", "b.ts": "accent\n"})
	if _, err := runner.readFiles([]string{"a.ts"}); err != nil {
		t.Fatal(err)
	}
	paths, _ := runner.pathsMatching(context.Background(), nil, "accent")
	if contains(paths, "a.ts") {
		t.Fatalf("resent a file already shown: %v", paths)
	}
	if !contains(paths, "b.ts") {
		t.Fatalf("lost the new match: %v", paths)
	}
}

func TestAPatternThatMatchesNothingSaysSo(t *testing.T) {
	runner := folderOf(t, map[string]string{"a.ts": "x\n"})
	paths, note := runner.pathsMatching(context.Background(), []string{"a.ts"}, "zzz-nothing")
	if len(paths) != 1 || !strings.Contains(note, "Nothing matched") {
		t.Fatalf("paths=%v note=%q", paths, note)
	}
}

// Never fails: a pattern ripgrep cannot compile still reads the named
// files.
func TestABrokenPatternStillReadsTheNamedFiles(t *testing.T) {
	runner := folderOf(t, map[string]string{"a.ts": "x\n"})
	paths, note := runner.pathsMatching(context.Background(), []string{"a.ts"}, "a(b")
	if !contains(paths, "a.ts") {
		t.Fatalf("lost the named file on a bad pattern: %v", paths)
	}
	if note == "" {
		t.Fatal("said nothing about the pattern failing")
	}
}
