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
