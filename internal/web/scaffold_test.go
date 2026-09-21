package web

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	appPath := filepath.Join(dir, "frontend", "src", "App.tsx")
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

func TestAProjectMadeBeforeTheHelpersExistedGetsThem(t *testing.T) {
	// The reason this is a list of names rather than a copy of a
	// directory: a project scaffolded last week has no llm.ts, and an
	// upgrade should hand it one.
	dir := t.TempDir()
	if err := scaffoldProject(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range backendHelpers {
		if err := os.Remove(filepath.Join(dir, "backend", name)); err != nil {
			t.Fatal(err)
		}
	}
	missing := missingHelpers(dir)
	if len(missing) != len(backendHelpers) {
		t.Fatalf("missingHelpers() = %v, want all of them", missing)
	}
	if err := restoreHelpers(dir, missing); err != nil {
		t.Fatal(err)
	}
	for _, name := range backendHelpers {
		body, err := os.ReadFile(filepath.Join(dir, "backend", name))
		if err != nil {
			t.Fatalf("%s was not restored: %v", name, err)
		}
		if !strings.Contains(string(body), "OPENAI_API_KEY") {
			t.Fatalf("%s does not read the key from the environment", name)
		}
	}
	if left := missingHelpers(dir); len(left) != 0 {
		t.Fatalf("still missing %v", left)
	}
}

func TestRestoringNeverOverwritesAHelperThatIsThere(t *testing.T) {
	// A helper is an ordinary file of the project once it lands. An
	// upgrade that reverted somebody's edits would be a worse bargain
	// than the file was worth.
	dir := t.TempDir()
	if err := scaffoldProject(dir); err != nil {
		t.Fatal(err)
	}
	edited := filepath.Join(dir, "backend", "llm.ts")
	if err := os.WriteFile(edited, []byte("// mine now\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if missing := missingHelpers(dir); len(missing) != 0 {
		t.Fatalf("nothing is missing, got %v", missing)
	}
	// And restoring what is genuinely missing leaves the edited one alone.
	if err := os.Remove(filepath.Join(dir, "backend", "decisions.ts")); err != nil {
		t.Fatal(err)
	}
	if err := restoreHelpers(dir, missingHelpers(dir)); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(edited)
	if string(body) != "// mine now\n" {
		t.Fatalf("llm.ts was overwritten: %q", body)
	}
}
