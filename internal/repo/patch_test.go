package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// page is a small stand-in for the real index.html: the change is near the
// top and the file continues well past it, which is what makes a hunk with
// no trailing context unplaceable by line number alone.
const page = `<!doctype html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Tec-Tac-Tris</title>
  <style>
    h1 { margin:0; }
  </style>
</head>
<body>
  <main class="wrap">
    <header>
      <div><h1>Tec-Tac-Tris</h1><p class="tagline">Drop a piece.</p></div>
      <button id="restart">New game</button>
    </header>
  </main>
</body>
</html>
`

func patchedPage(t *testing.T, patch string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}
	if err := repository.Apply(patch); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestApplyHunkWithNoTrailingContext(t *testing.T) {
	// Taken from a real failure: correct content, but the hunk stops at
	// its own change. git rejects that outright, since a hunk ending at
	// its last change implies the file ends there.
	patch := "diff --git a/index.html b/index.html\n" +
		"--- a/index.html\n+++ b/index.html\n" +
		"@@ -2,7 +2,7 @@\n" +
		" <html lang=\"en\">\n <head>\n" +
		"   <meta charset=\"UTF-8\" />\n" +
		"   <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\" />\n" +
		"-  <title>Tec-Tac-Tris</title>\n" +
		"+  <title>TeIC-TAC-TRoIS</title>\n"
	got := patchedPage(t, patch)
	if !strings.Contains(got, "<title>TeIC-TAC-TRoIS</title>") {
		t.Fatalf("title not changed:\n%s", got)
	}
	// Everything after the hunk has to survive.
	if !strings.Contains(got, "<button id=\"restart\">New game</button>") {
		t.Fatalf("rest of the file was lost:\n%s", got)
	}
}

func TestApplyHunkWithWrongLineNumbersAndCounts(t *testing.T) {
	// Line numbers point nowhere near the change and the counts are
	// nonsense; the content is still unambiguous.
	patch := "--- a/index.html\n+++ b/index.html\n" +
		"@@ -900,42 +900,42 @@\n" +
		"-  <title>Tec-Tac-Tris</title>\n" +
		"+  <title>TeIC-TAC-TRoIS</title>\n" +
		"   <style>\n"
	got := patchedPage(t, patch)
	if !strings.Contains(got, "<title>TeIC-TAC-TRoIS</title>") {
		t.Fatalf("title not changed:\n%s", got)
	}
}

func TestApplyMultipleHunksInOneFile(t *testing.T) {
	patch := "--- a/index.html\n+++ b/index.html\n" +
		"@@ -5,1 +5,1 @@\n" +
		"-  <title>Tec-Tac-Tris</title>\n" +
		"+  <title>TeIC-TAC-TRoIS</title>\n" +
		"@@ -65,1 +65,1 @@\n" +
		"-      <div><h1>Tec-Tac-Tris</h1><p class=\"tagline\">Drop a piece.</p></div>\n" +
		"+      <div><h1>TeIC-TAC-TRoIS</h1><p class=\"tagline\">Drop a piece.</p></div>\n"
	got := patchedPage(t, patch)
	if strings.Contains(got, "Tec-Tac-Tris") {
		t.Fatalf("an occurrence was left behind:\n%s", got)
	}
	if strings.Count(got, "TeIC-TAC-TRoIS") != 2 {
		t.Fatalf("expected both occurrences replaced:\n%s", got)
	}
}

func TestApplyIsRefusedWhenTheHunkIsAmbiguous(t *testing.T) {
	// A single short line that occurs several times could be any of them,
	// so it must be refused rather than guessed at.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x\nx\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}
	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1,1 +1,1 @@\n-x\n+y\n"
	err := repository.Apply(patch)
	if err == nil {
		t.Fatal("expected an ambiguous hunk to be refused")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err = %v, want it to explain the ambiguity", err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(content) != "x\nx\nx\n" {
		t.Fatalf("file was modified despite the refusal: %q", content)
	}
}

func TestApplyLeavesFilesAloneWhenAHunkCannotBePlaced(t *testing.T) {
	patch := "--- a/index.html\n+++ b/index.html\n@@ -1,1 +1,1 @@\n" +
		"-  <title>Something Else Entirely</title>\n" +
		"+  <title>TeIC-TAC-TRoIS</title>\n"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}
	if err := repository.Apply(patch); err == nil {
		t.Fatal("expected a hunk that matches nothing to fail")
	}
	content, _ := os.ReadFile(filepath.Join(root, "index.html"))
	if string(content) != page {
		t.Fatal("the file must be left untouched when the patch cannot be placed")
	}
}

func TestParsePatchReadsPathsAndHunks(t *testing.T) {
	patches, err := parsePatch("diff --git a/x.go b/x.go\nindex 111..222 100644\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 || patches[0].path != "x.go" {
		t.Fatalf("patches = %+v", patches)
	}
	only := patches[0].hunks
	if len(only) != 1 {
		t.Fatalf("hunks = %+v", only)
	}
	if strings.Join(only[0].before, "|") != "a|b" {
		t.Fatalf("before = %v", only[0].before)
	}
	if strings.Join(only[0].after, "|") != "a|c" {
		t.Fatalf("after = %v", only[0].after)
	}
}

func TestApplyStillPrefersGitWhenThePatchIsWellFormed(t *testing.T) {
	// A correct patch must keep working exactly as before.
	patch := "--- a/index.html\n+++ b/index.html\n" +
		"@@ -5,3 +5,3 @@\n" +
		"   <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\" />\n" +
		"-  <title>Tec-Tac-Tris</title>\n" +
		"+  <title>TeIC-TAC-TRoIS</title>\n" +
		"   <style>\n"
	got := patchedPage(t, patch)
	if !strings.Contains(got, "<title>TeIC-TAC-TRoIS</title>") {
		t.Fatalf("title not changed:\n%s", got)
	}
}
