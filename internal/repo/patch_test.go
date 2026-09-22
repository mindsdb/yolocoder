package repo

import (
	"errors"
	"fmt"
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

func TestAmbiguousHunkErrorPointsAtEachMatch(t *testing.T) {
	// The whole point: a repair attempt needs to know where the matches
	// actually are, not just that there's more than one, or it's just as
	// likely to reproduce the same ambiguity as fix it.
	root := t.TempDir()
	original := "function a() {\n  setSession(x);\n}\nfunction b() {\n  setSession(x);\n}\n"
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}
	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1,1 +1,1 @@\n-  setSession(x);\n+  setSession(y);\n"
	err := repository.Apply(patch)
	if err == nil {
		t.Fatal("expected an ambiguous hunk to be refused")
	}
	for _, want := range []string{"line 2", "line 5", "above: \"function a() {\"", "above: \"function b() {\"", "below: \"}\""} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to contain %q", err, want)
		}
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

func TestApplyRecoversFromASpelledOutHTMLEntity(t *testing.T) {
	// The instructions warn the model that this is a common way to get a
	// hunk wrong; this is that exact case recovering without a retry.
	root := t.TempDir()
	writeFile(t, root, "index.html", "<p>Salt &amp; pepper</p>\n<p>unrelated</p>\n")
	repository := &Repository{Root: root}
	patch := "--- a/index.html\n+++ b/index.html\n@@ -1,1 +1,1 @@\n" +
		"-<p>Salt & pepper</p>\n" +
		"+<p>Salt & vinegar</p>\n"
	if err := repository.Apply(patch); err != nil {
		t.Fatalf("Apply() = %v, want the entity difference to be recovered from", err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "index.html"))
	if !strings.Contains(string(content), "Salt & vinegar") {
		t.Fatalf("index.html = %q", content)
	}
	if !strings.Contains(string(content), "unrelated") {
		t.Fatal("the rest of the file must survive")
	}
}

func TestApplyFailureReportsTheClosestMatchingLine(t *testing.T) {
	// One line differs from the real file by a single character; the
	// error should point at exactly that line rather than just repeating
	// the whole block the model already wrote.
	patch := "--- a/index.html\n+++ b/index.html\n@@ -1,1 +1,1 @@\n" +
		"   <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\" />\n" +
		"-  <title>Tec-Tac-Tris!</title>\n" +
		"+  <title>New Title</title>\n"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}
	err := repository.Apply(patch)
	if err == nil {
		t.Fatal("expected the trailing ! to make this hunk unplaceable")
	}
	if !strings.Contains(err.Error(), "closest match is at line") {
		t.Fatalf("err = %v, want it to name the closest matching line", err)
	}
	if !strings.Contains(err.Error(), `"  <title>Tec-Tac-Tris!</title>"`) {
		t.Fatalf("err = %v, want it to show what the model wrote", err)
	}
	if !strings.Contains(err.Error(), `"  <title>Tec-Tac-Tris</title>"`) {
		t.Fatalf("err = %v, want it to show the file's real line", err)
	}
}

func TestApplyFailureOmitsNearMissWhenNothingIsClose(t *testing.T) {
	// A block that doesn't resemble anything in the file shouldn't get a
	// misleading "closest match" pointed at some unrelated line.
	patch := "--- a/index.html\n+++ b/index.html\n@@ -1,1 +1,1 @@\n" +
		"-totally unrelated content that appears nowhere in the file at all\n" +
		"+replacement\n"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}
	err := repository.Apply(patch)
	if err == nil {
		t.Fatal("expected this hunk to fail to place")
	}
	if strings.Contains(err.Error(), "closest match") {
		t.Fatalf("err = %v, want no near-miss guess when nothing is actually close", err)
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

func TestParsePatchAcceptsAMinusHeaderWithNoPlusHeader(t *testing.T) {
	// Traced from a real session: the model wrote "--- path" and went
	// straight to a bare "@@" with no "+++" line at all. Nothing else in
	// the patch named the file, so this used to be unrecoverable and cost
	// a full extra round trip to regenerate a diff the model had already
	// named the file in correctly once.
	patches, err := parsePatch("--- frontend/src/App.tsx\n@@\n-old\n+new\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 || patches[0].path != "frontend/src/App.tsx" {
		t.Fatalf("patches = %+v", patches)
	}
}

func TestApplyRecoversWhenThePatchHasOnlyAMinusHeader(t *testing.T) {
	patch := "--- index.html\n@@\n-  <title>Tec-Tac-Tris</title>\n+  <title>TeIC-TAC-TRoIS</title>\n"
	got := patchedPage(t, patch)
	if !strings.Contains(got, "<title>TeIC-TAC-TRoIS</title>") {
		t.Fatalf("title not changed:\n%s", got)
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

func TestApplyOpenAIApplyPatchFormat(t *testing.T) {
	// Verbatim from a real session: gpt-oss-120b answered with OpenAI's
	// apply_patch format rather than a unified diff. The content was
	// exactly right both times, but git rejected it ("No valid patches in
	// input") and three attempts were spent before falling back to
	// rewriting the whole file. That format carries no line numbers,
	// which is precisely what placing hunks by content wants.
	patch := "*** Begin Patch\n" +
		"*** Update File: index.html\n" +
		"@@\n" +
		"   <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\" />\n" +
		"-  <title>Tec-Tac-Tris</title>\n" +
		"+  <title>bambaloba</title>\n" +
		"   <style>\n" +
		"@@\n" +
		"-      <div><h1>Tec-Tac-Tris</h1><p class=\"tagline\">Drop a piece.</p></div>\n" +
		"+      <div><h1>bambaloba</h1><p class=\"tagline\">Drop a piece.</p></div>\n" +
		"*** End Patch"

	got := patchedPage(t, patch)
	if strings.Contains(got, "Tec-Tac-Tris") {
		t.Fatalf("an occurrence was left behind:\n%s", got)
	}
	if strings.Count(got, "bambaloba") != 2 {
		t.Fatalf("expected both title and heading changed:\n%s", got)
	}
	// The rest of the file has to survive untouched.
	if !strings.Contains(got, `<button id="restart">New game</button>`) {
		t.Fatalf("unrelated content was lost:\n%s", got)
	}
}

func TestParsePatchReadsApplyPatchHeaders(t *testing.T) {
	patches, err := parsePatch("*** Begin Patch\n*** Update File: a/b/c.go\n@@\n ctx\n-old\n+new\n*** End Patch")
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 || patches[0].path != "b/c.go" {
		t.Fatalf("patches = %+v, want the a/ prefix stripped", patches)
	}
	if len(patches[0].hunks) != 1 {
		t.Fatalf("hunks = %+v", patches[0].hunks)
	}
	if strings.Join(patches[0].hunks[0].before, "|") != "ctx|old" {
		t.Fatalf("before = %v", patches[0].hunks[0].before)
	}
}

func TestApplyPatchFormatCanAddAFile(t *testing.T) {
	root := t.TempDir()
	repository := &Repository{Root: root}
	patch := "*** Begin Patch\n*** Add File: notes/new.txt\n@@\n+hello\n+world\n*** End Patch"
	if err := repository.Apply(patch); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "notes", "new.txt"))
	if err != nil || string(content) != "hello\nworld" {
		t.Fatalf("new.txt = %q, %v", content, err)
	}
}

func TestApplyRefusesToDeleteFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "gone.txt", "still here\n")
	repository := &Repository{Root: root}
	err := repository.Apply("*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "does not do") {
		t.Fatalf("err = %v, want a refusal to delete", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "gone.txt")); statErr != nil {
		t.Fatal("the file must be left alone")
	}
}

func TestApplySeveralPatchBlocksForOneFile(t *testing.T) {
	// Models emit one "*** Begin Patch" block per edit rather than one
	// patch with several hunks. Each block has to build on the last:
	// reading the file fresh for every block made the final one silently
	// discard every change before it.
	patch := "*** Begin Patch\n" +
		"*** Update File: index.html\n" +
		"@@\n" +
		"-  <title>Tec-Tac-Tris</title>\n" +
		"+  <title>one</title>\n" +
		"   <style>\n" +
		"*** End Patch\n" +
		"*** Begin Patch\n" +
		"*** Update File: index.html\n" +
		"@@\n" +
		"-      <div><h1>Tec-Tac-Tris</h1><p class=\"tagline\">Drop a piece.</p></div>\n" +
		"+      <div><h1>two</h1><p class=\"tagline\">Drop a piece.</p></div>\n" +
		"*** End Patch"

	got := patchedPage(t, patch)
	if !strings.Contains(got, "<title>one</title>") {
		t.Fatalf("the first block was discarded:\n%s", got)
	}
	if !strings.Contains(got, "<h1>two</h1>") {
		t.Fatalf("the second block was not applied:\n%s", got)
	}
	if strings.Contains(got, "Tec-Tac-Tris") {
		t.Fatalf("an occurrence was left behind:\n%s", got)
	}
}

func TestIsApplyPatchFormat(t *testing.T) {
	applyPatch := []string{
		"*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** End Patch",
		"*** Update File: a.go\n@@\n-x\n+y",
		"  *** Begin Patch\n*** Add File: new.txt\n@@\n+hi",
	}
	for _, patch := range applyPatch {
		if !isApplyPatchFormat(patch) {
			t.Fatalf("isApplyPatchFormat(%q) = false, want true", patch)
		}
	}
	unified := []string{
		"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y",
		"--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y",
	}
	for _, patch := range unified {
		if isApplyPatchFormat(patch) {
			t.Fatalf("isApplyPatchFormat(%q) = true, want false", patch)
		}
	}
	if isApplyPatchFormat("") {
		t.Fatal("empty patch must not be treated as apply_patch")
	}
}

func TestApplyPatchFormatDoesNotConsultGit(t *testing.T) {
	// git cannot read this format, so asking it first only produced a
	// "PATCH FAILED" line on the way to the applier that could. Pointing
	// the repository at a git binary that would fail loudly proves git is
	// never invoked for it.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}

	patch := "*** Begin Patch\n*** Update File: index.html\n@@\n" +
		"-  <title>Tec-Tac-Tris</title>\n+  <title>fits</title>\n   <style>\n*** End Patch"

	// PATH is emptied so any attempt to run git would error; the apply
	// must still succeed.
	t.Setenv("PATH", "")
	if err := repository.Apply(patch); err != nil {
		t.Fatalf("Apply() = %v, want it to succeed without git", err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "index.html"))
	if !strings.Contains(string(content), "<title>fits</title>") {
		t.Fatalf("index.html = %q", content)
	}
}

func TestAddFileWithNoHunkHeader(t *testing.T) {
	// Verbatim shape from a real reply: apply_patch puts the "+" lines
	// straight after "*** Add File:" with no "@@" of its own. Requiring
	// one meant every content line was read as noise, no hunk was built,
	// and the whole patch reported "patch changed nothing".
	root := t.TempDir()
	repository := &Repository{Root: root}
	patch := "*** Begin Patch\n" +
		"*** Add File: package.json\n" +
		"+{\n" +
		"+  \"name\": \"super-tic-tac-tris\",\n" +
		"+  \"scripts\": { \"start\": \"node server.js\" }\n" +
		"+}\n" +
		"*** Add File: server.js\n" +
		"+const express = require('express');\n" +
		"+const app = express();\n" +
		"*** Add File: launch.sh\n" +
		"+#!/bin/sh\n" +
		"+node server.js\n" +
		"*** End Patch"

	if err := repository.Apply(patch); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	for path, want := range map[string]string{
		"package.json": `"name": "super-tic-tac-tris"`,
		"server.js":    "const express = require('express');",
		"launch.sh":    "node server.js",
	} {
		content, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("%s was not created: %v", path, err)
		}
		if !strings.Contains(string(content), want) {
			t.Fatalf("%s = %q, want it to contain %q", path, content, want)
		}
	}
}

func TestUpdateFileWithAHunkHeaderIsUnaffected(t *testing.T) {
	// Starting a hunk at the header leaves an empty one when a "@@"
	// follows; it must be dropped, or it would read as "replace this
	// file with nothing".
	patch := "*** Begin Patch\n*** Update File: index.html\n@@\n" +
		"-  <title>Tec-Tac-Tris</title>\n+  <title>kept</title>\n   <style>\n*** End Patch"
	got := patchedPage(t, patch)
	if !strings.Contains(got, "<title>kept</title>") {
		t.Fatalf("title not changed:\n%s", got)
	}
	if !strings.Contains(got, `<button id="restart">New game</button>`) {
		t.Fatalf("the rest of the file was lost:\n%s", got)
	}
}

func TestExplainNamesTheFileAndTheLineThatDiffers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.tsx"), []byte("const a = 1;\nconst board = useState(empty());\nconst c = 3;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}

	// The middle line is subtly wrong — the shape of near miss a model
	// reproducing a file by eye actually produces.
	err := repository.Apply("--- a/app.tsx\n+++ b/app.tsx\n@@\n const a = 1;\n-const board = useState(() => empty());\n+const board = useState(fresh());\n const c = 3;\n")
	if err == nil {
		t.Fatal("a hunk whose context is not in the file should not apply")
	}
	lines := Explain(err)
	if len(lines) != 3 {
		t.Fatalf("Explain() = %q, want the reason plus the two lines that differ", lines)
	}
	if !strings.HasPrefix(lines[0], "app.tsx: could not find this hunk's lines") {
		t.Fatalf("first line should name the file and the reason: %q", lines[0])
	}
	if !strings.Contains(lines[1], "() => empty()") {
		t.Fatalf("expected line should be what the hunk wanted: %q", lines[1])
	}
	if !strings.Contains(lines[2], "useState(empty())") {
		t.Fatalf("found line should be what is really there: %q", lines[2])
	}
}

func TestExplainFallsBackToTheErrorsOwnFirstLine(t *testing.T) {
	if got := Explain(errors.New("something else went wrong\nwith more detail below")); len(got) != 1 || got[0] != "something else went wrong" {
		t.Fatalf("Explain() = %q", got)
	}
	if got := Explain(nil); got != nil {
		t.Fatalf("Explain(nil) = %q, want nothing to print", got)
	}
}

func TestExplainClipsALongLine(t *testing.T) {
	long := strings.Repeat("x", 300)
	got := Explain(&HunkError{Path: "a.ts", Reason: "nope", Expected: long, Found: long})
	for _, line := range got[1:] {
		if len(line) > 130 {
			t.Fatalf("a line escaped clipping at %d characters", len(line))
		}
	}
}

// threeFiles writes a small project whose files each contain a line a
// patch can miss in a different way.
func threeFiles(t *testing.T) *Repository {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"app.tsx":  "const a = 1;\nconst board = useState(empty());\nconst c = 3;\n",
		"main.css": ":root {\n  --bg: #b7c98a;\n}\n",
		"dup.ts":   "call();\nother();\ncall();\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Repository{Root: root}
}

func TestOnePassReportsEveryEditThatCannotBePlaced(t *testing.T) {
	repository := threeFiles(t)
	// Three separate problems in one patch: a wrong line in app.tsx, a
	// wrong value in main.css, and an ambiguous one-line hunk in dup.ts.
	patch := "--- a/app.tsx\n+++ b/app.tsx\n@@\n const a = 1;\n-const board = useState(() => empty());\n+const board = useState(fresh());\n const c = 3;\n" +
		"--- a/main.css\n+++ b/main.css\n@@\n-  --bg: #001933;\n+  --bg: #123456;\n" +
		"--- a/dup.ts\n+++ b/dup.ts\n@@\n-call();\n+called();\n"

	err := repository.Apply(patch)
	if err == nil {
		t.Fatal("a patch with three unplaceable edits should not apply")
	}
	var whole *PatchError
	if !errors.As(err, &whole) {
		t.Fatalf("error should carry every failure, got %T: %v", err, err)
	}
	if len(whole.Failures) != 3 {
		t.Fatalf("reported %d failures, want all 3 in one pass:\n%v", len(whole.Failures), Explain(err))
	}

	// Each one names its own file, so a repair can fix all three at once.
	named := map[string]bool{}
	for _, failure := range whole.Failures {
		named[failure.Path] = true
	}
	for _, want := range []string{"app.tsx", "main.css", "dup.ts"} {
		if !named[want] {
			t.Errorf("no failure reported for %s", want)
		}
	}

	// The model's text must list all of them and say to fix all of them.
	text := err.Error()
	if !strings.Contains(text, "Fix every one of them") {
		t.Errorf("the model is not told to fix them all:\n%s", text)
	}
	for _, want := range []string{"[1]", "[2]", "[3]"} {
		if !strings.Contains(text, want) {
			t.Errorf("failure %s missing from the text given to the model", want)
		}
	}

	// And the trail leads with the count.
	if lines := Explain(err); lines[0] != "3 edits could not be placed" {
		t.Fatalf("Explain() led with %q", lines[0])
	}
}

func TestNothingIsWrittenWhenAnyEditFails(t *testing.T) {
	repository := threeFiles(t)
	// The first edit is perfectly good; the second is not. Neither lands.
	patch := "--- a/app.tsx\n+++ b/app.tsx\n@@\n const a = 1;\n-const board = useState(empty());\n+const board = useState(fresh());\n const c = 3;\n" +
		"--- a/main.css\n+++ b/main.css\n@@\n-  --bg: #001933;\n+  --bg: #123456;\n"

	if err := repository.Apply(patch); err == nil {
		t.Fatal("the patch should be refused")
	}
	content, err := os.ReadFile(filepath.Join(repository.Root, "app.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "fresh()") {
		t.Fatalf("the placeable edit was written even though the patch failed:\n%s", content)
	}
}

func TestASingleFailureStillReadsAsOne(t *testing.T) {
	repository := threeFiles(t)
	err := repository.Apply("--- a/main.css\n+++ b/main.css\n@@\n-  --bg: #001933;\n+  --bg: #123456;\n")
	if err == nil {
		t.Fatal("expected a failure")
	}
	lines := Explain(err)
	if strings.HasSuffix(lines[0], "edits could not be placed") {
		t.Fatalf("one failure should not be counted like a list: %q", lines[0])
	}
	if !strings.HasPrefix(lines[0], "main.css: ") {
		t.Fatalf("Explain() = %q", lines[0])
	}
}

func TestIdenticalFailuresCollapseIntoOneLine(t *testing.T) {
	// Twelve hunks that all missed the same file used to produce twelve
	// identical lines, of which the trail showed four and then "... and 8
	// more" — saying nothing the count on the first line had not.
	var many PatchError
	for index := 0; index < 9; index++ {
		many.Failures = append(many.Failures, &HunkError{Path: "a.ts", Reason: "could not find this hunk's lines in the file"})
	}
	lines := many.Summary()
	if lines[0] != "9 edits could not be placed" {
		t.Fatalf("led with %q", lines[0])
	}
	if len(lines) != 2 {
		t.Fatalf("nine of the same thing is one line, not nine: %v", lines)
	}
	if !strings.HasSuffix(lines[1], "×9") {
		t.Fatalf("the repeat should be counted, got %q", lines[1])
	}
}

func TestTheTrailStopsAfterEnoughDistinctKinds(t *testing.T) {
	var many PatchError
	for index := 0; index < 7; index++ {
		many.Failures = append(many.Failures, &HunkError{
			Path:   fmt.Sprintf("file%d.ts", index),
			Reason: "could not find this hunk's lines in the file",
		})
	}
	lines := many.Summary()
	if last := lines[len(lines)-1]; last != "... and 3 more kinds" {
		t.Fatalf("trail should stop and say how many kinds remain, got %q", last)
	}
}
