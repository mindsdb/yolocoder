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

func TestIsYolocoderProject(t *testing.T) {
	dir := t.TempDir()
	if isYolocoderProject(dir) {
		t.Fatal("a folder with no marker shouldn't count as a yolocoder project")
	}
	if err := scaffoldProject(dir); err != nil {
		t.Fatal(err)
	}
	if !isYolocoderProject(dir) {
		t.Fatal("a scaffolded folder should carry the yolocoder.json marker")
	}
}

func TestRestoreScriptsOnlyTouchesScripts(t *testing.T) {
	dir := t.TempDir()
	if err := scaffoldProject(dir); err != nil {
		t.Fatal(err)
	}
	// Simulate the user's own edit to the app surviving a script repair.
	appPath := filepath.Join(dir, "src", "App.tsx")
	if err := os.WriteFile(appPath, []byte("// edited by the user"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "scripts")); err != nil {
		t.Fatal(err)
	}
	if err := restoreScripts(dir); err != nil {
		t.Fatal(err)
	}
	if !scriptsExist(dir) {
		t.Fatal("restoreScripts should recreate scripts/{start,restart,stop}.sh")
	}
	content, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "// edited by the user" {
		t.Fatal("restoreScripts should not touch anything outside scripts/")
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
