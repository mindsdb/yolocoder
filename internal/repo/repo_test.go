package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMapRespectsGitignore(t *testing.T) {
	repository := testRepository(t)
	writeFile(t, repository.Root, ".gitignore", "ignored.txt\n")
	writeFile(t, repository.Root, "kept.txt", "hello")
	writeFile(t, repository.Root, "ignored.txt", "secret")
	mapText, err := repository.Map()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mapText, "kept.txt") || strings.Contains(mapText, "ignored.txt") {
		t.Fatalf("unexpected map:\n%s", mapText)
	}
}

func TestOpenDoesNotAdoptAncestorRepository(t *testing.T) {
	repository := testRepository(t)
	nested := filepath.Join(repository.Root, "nested", "folder")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDirectory(t, opened.Root, nested) {
		t.Fatalf("Open() root = %q, want %q (must not walk up to the ancestor repository)", opened.Root, nested)
	}
}

func TestOpenPlainFolderRequiresNoGit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "existing.txt", "keep me")
	opened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDirectory(t, opened.Root, root) {
		t.Fatalf("Open() root = %q, want %q", opened.Root, root)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		t.Fatal("Open() must not create a .git directory")
	}
	mapText, err := opened.Map()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mapText, "existing.txt") {
		t.Fatalf("plain folder map missing file:\n%s", mapText)
	}
}

func TestMapWalksPlainFolderIgnoringBuildDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "kept.txt", "hello")
	writeFile(t, root, "node_modules/some-package/index.js", "module.exports = {}")
	repository := &Repository{Root: root}
	mapText, err := repository.Map()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mapText, "kept.txt") || strings.Contains(mapText, "node_modules") {
		t.Fatalf("unexpected map:\n%s", mapText)
	}
}

func TestApplyInPlainFolderNestedUnderAnAncestorRepository(t *testing.T) {
	// Reproduces a real failure: `git apply` auto-discovers a Git
	// repository by walking up from cwd, independent of our own Open()
	// logic. If an ancestor directory happens to have a .git (a stray
	// repo the folder was created inside, for example), git apply
	// resolves the patch's paths against that ancestor's toplevel,
	// decides they fall outside the current directory, and silently
	// skips every hunk: exit 0, no output, no file written.
	ancestor := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = ancestor
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	root := filepath.Join(ancestor, "plain-subfolder")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}
	patch := "diff --git a/hello.txt b/hello.txt\nnew file mode 100644\n--- /dev/null\n+++ b/hello.txt\n@@ -0,0 +1 @@\n+hello\n"
	if err := repository.Apply(patch); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatalf("hello.txt was not created: %v", err)
	}
	if string(content) != "hello\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestApplyToleratesAMiscountedHunkHeader(t *testing.T) {
	// Reproduces a real failure: "git apply --check - failed: ...
	// error: corrupt patch at line N". Models sometimes get a hunk's
	// @@ -a,b +c,d @@ line counts wrong, especially on longer hunks;
	// --recount asks git to infer them from the hunk body instead.
	root := t.TempDir()
	writeFile(t, root, "file.txt", "a\nb\nc\n")
	repository := &Repository{Root: root}
	// Header claims 3 lines both sides (-1,3 +1,3), but the body below
	// actually spans 4 lines (a, b, +new line, c).
	patch := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1,3 +1,3 @@\n a\n b\n+new line\n c\n"
	if err := repository.Apply(patch); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil || string(content) != "a\nb\nnew line\nc\n" {
		t.Fatalf("content = %q, %v", content, err)
	}
}

func TestApplyWithoutGitRepository(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "file.txt", "old\n")
	repository := &Repository{Root: root}
	patch := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new\n"
	if err := repository.Apply(patch); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil || string(content) != "new\n" {
		t.Fatalf("content = %q, %v", content, err)
	}
}

func sameDirectory(t *testing.T, first, second string) bool {
	t.Helper()
	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(firstInfo, secondInfo)
}

func TestReadRejectsParentPath(t *testing.T) {
	repository := testRepository(t)
	if _, err := repository.Read([]string{"../outside"}); err == nil {
		t.Fatal("expected parent path to be rejected")
	}
}

func TestApplyUnifiedDiff(t *testing.T) {
	repository := testRepository(t)
	writeFile(t, repository.Root, "file.txt", "old\n")
	command := exec.Command("git", "add", "file.txt")
	command.Dir = repository.Root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	patch := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new\n"
	if err := repository.Apply(patch); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(repository.Root, "file.txt"))
	if string(content) != "new\n" {
		t.Fatalf("content = %q", content)
	}
}

func testRepository(t *testing.T) *Repository {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return &Repository{Root: root}
}

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
