package web

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLooksEmptyOnATrulyEmptyFolder(t *testing.T) {
	dir := t.TempDir()
	if !looksEmpty(dir) {
		t.Fatal("an empty folder should look empty")
	}
}

func TestLooksEmptyIgnoresProjectHousekeepingFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"README.md", ".gitignore"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !looksEmpty(dir) {
		t.Fatal("README/.gitignore/.git alone should still count as empty")
	}
}

func TestLooksEmptyFalseWithAProjectMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if looksEmpty(dir) {
		t.Fatal("a folder with package.json is an existing project, not empty")
	}
}

func TestLooksEmptyFalseWithUnrecognizedContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("todo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if looksEmpty(dir) {
		t.Fatal("a folder with content that isn't housekeeping shouldn't be scaffolded over")
	}
}

func TestScaffoldProjectWritesTheScriptsExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := scaffoldProject(dir); err != nil {
		t.Fatal(err)
	}
	if !scriptsExist(dir) {
		t.Fatal("scaffold should include scripts/{start,restart,stop}.sh")
	}
	info, err := os.Stat(filepath.Join(dir, "scripts", "start.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// NTFS has no POSIX executable bit, so os.FileMode on Windows never
	// reflects what was passed to WriteFile; the check only means anything
	// on a filesystem that tracks it.
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Fatal("start.sh should be executable")
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		t.Fatal("scaffold should include package.json:", err)
	}
}
