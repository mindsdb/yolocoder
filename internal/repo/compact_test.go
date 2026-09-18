package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func project(t *testing.T, files map[string]string) *Repository {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Repository{Root: root}
}

func read(t *testing.T, repository *Repository, path string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repository.Root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestCompactAppliesTheDocumentedExample(t *testing.T) {
	repository := project(t, map[string]string{
		"src/app.py":    "def start():\n    server.run(config)\n    return server\n",
		"src/config.py": "PORT = 8000\nHOST = \"localhost\"\n",
	})

	err := repository.Apply(`@src/app.py
 def start():
-    server.run(config)
+    server.run(config, debug=True)
     return server

@src/config.py
-PORT = 8000
+PORT = 8080
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, repository, "src/app.py"); !strings.Contains(got, "debug=True") {
		t.Fatalf("src/app.py = %q", got)
	}
	if got := read(t, repository, "src/config.py"); !strings.Contains(got, "PORT = 8080") {
		t.Fatalf("src/config.py = %q", got)
	}
	// The untouched line survives.
	if got := read(t, repository, "src/config.py"); !strings.Contains(got, `HOST = "localhost"`) {
		t.Fatalf("an unrelated line was lost: %q", got)
	}
}

func TestCompactCreatesAFileFromLiteralContent(t *testing.T) {
	repository := project(t, map[string]string{"keep.txt": "x\n"})
	err := repository.Apply("@+docs/notes.md\n# Notes\n\n- first\n- second\n")
	if err != nil {
		t.Fatal(err)
	}
	// Literal content: no prefixes stripped, and the blank line inside it
	// is part of the file rather than an edit separator.
	if got, want := read(t, repository, "docs/notes.md"), "# Notes\n\n- first\n- second"; got != want {
		t.Fatalf("created file = %q, want %q", got, want)
	}
}

func TestCompactCreatedContentMayBeginWithDiffCharacters(t *testing.T) {
	// The reason created lines carry no prefix: a file whose own content
	// starts with "+", "-" or a space would otherwise be unwritable.
	repository := project(t, map[string]string{"keep.txt": "x\n"})
	if err := repository.Apply("@+README.md\n- a bullet\n+ not an addition\n  indented\n"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, repository, "README.md"), "- a bullet\n+ not an addition\n  indented"; got != want {
		t.Fatalf("created file = %q, want %q", got, want)
	}
}

func TestCompactCreatedContentSurvivesAtRules(t *testing.T) {
	// A CSS file full of at-rules must not be cut short and read as a
	// series of new files named "media", "import" and so on.
	repository := project(t, map[string]string{"keep.txt": "x\n"})
	css := "@import url(\"x.css\");\n\n@media (max-width: 520px) {\n  body { color: red; }\n}\n"
	if err := repository.Apply("@+styles/app.css\n" + css); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, repository, "styles/app.css"), strings.TrimRight(css, "\n"); got != want {
		t.Fatalf("created file = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(repository.Root, "media")); err == nil {
		t.Fatal("an at-rule was mistaken for a file header")
	}
}

func TestCompactTakesSeveralEditsToOneFile(t *testing.T) {
	repository := project(t, map[string]string{
		"a.ts": "const first = 1;\nconst middle = 2;\nconst last = 3;\n",
	})
	err := repository.Apply("@a.ts\n-const first = 1;\n+const first = 10;\n\n-const last = 3;\n+const last = 30;\n")
	if err != nil {
		t.Fatal(err)
	}
	got := read(t, repository, "a.ts")
	if !strings.Contains(got, "first = 10") || !strings.Contains(got, "last = 30") {
		t.Fatalf("both edits should have landed: %q", got)
	}
	if !strings.Contains(got, "middle = 2") {
		t.Fatalf("the line between them was lost: %q", got)
	}
}

func TestCompactToleratesAContextLineThatLostItsSpace(t *testing.T) {
	// Models strip trailing and leading whitespace constantly. An
	// unprefixed line is read as context rather than refused.
	repository := project(t, map[string]string{
		"a.ts": "function go() {\n  const x = 1;\n}\n",
	})
	err := repository.Apply("@a.ts\nfunction go() {\n-  const x = 1;\n+  const x = 2;\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, repository, "a.ts"); !strings.Contains(got, "const x = 2") {
		t.Fatalf("a.ts = %q", got)
	}
}

func TestCompactRefusesDeleteAndMoveByName(t *testing.T) {
	repository := project(t, map[string]string{"gone.txt": "x\n"})

	err := repository.Apply("@-gone.txt\n")
	if err == nil || !strings.Contains(err.Error(), "deletes gone.txt") {
		t.Fatalf("delete should be refused by name, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repository.Root, "gone.txt")); statErr != nil {
		t.Fatal("the file should still be there")
	}

	err = repository.Apply("@>gone.txt\nelsewhere.txt\n")
	if err == nil || !strings.Contains(err.Error(), "moves gone.txt") {
		t.Fatalf("move should be refused by name, got %v", err)
	}
}

func TestCompactFailuresCarryTheSameDiagnostics(t *testing.T) {
	// The whole point of parsing into the existing hunk shape: the format
	// changes, the reporting does not.
	repository := project(t, map[string]string{
		"a.ts": "const board = useState(empty());\n",
		"b.ts": "const other = 1;\n",
	})
	err := repository.Apply("@a.ts\n-const board = useState(() => empty());\n+const board = useState(fresh());\n\n@b.ts\n-const other = 99;\n+const other = 2;\n")
	if err == nil {
		t.Fatal("neither edit can be placed")
	}
	lines := Explain(err)
	if lines[0] != "2 edits could not be placed" {
		t.Fatalf("Explain() led with %q", lines[0])
	}
	if !strings.Contains(strings.Join(lines, "\n"), "() => empty()") {
		t.Fatalf("the near miss should still be reported:\n%v", lines)
	}
}

func TestIsCompactPatchTellsTheFormatsApart(t *testing.T) {
	compact := map[string]string{
		"a plain modification": "@src/app.py\n-a\n+b\n",
		"a create":             "@+src/new.py\nprint(1)\n",
		"leading blank lines":  "\n\n@src/app.py\n-a\n+b\n",
	}
	for name, patch := range compact {
		if !isCompactPatch(patch) {
			t.Errorf("%s should read as compact: %q", name, patch)
		}
	}
	others := map[string]string{
		"a unified diff": "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n",
		"a git diff":     "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@\n-a\n+b\n",
		"an apply_patch": "*** Begin Patch\n*** Update File: x\n-a\n+b\n*** End Patch\n",
		"a bare hunk":    "@@ -1,3 +1,3 @@\n-a\n+b\n",
		"nothing at all": "",
	}
	for name, patch := range others {
		if isCompactPatch(patch) {
			t.Errorf("%s should not read as compact: %q", name, patch)
		}
	}
}

func TestCompactAndUnifiedStillCoexist(t *testing.T) {
	// A model that reaches for a unified diff keeps working.
	repository := project(t, map[string]string{"a.ts": "const x = 1;\n"})
	if err := repository.Apply("--- a/a.ts\n+++ b/a.ts\n@@ -1 +1 @@\n-const x = 1;\n+const x = 2;\n"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, repository, "a.ts"); !strings.Contains(got, "x = 2") {
		t.Fatalf("a.ts = %q", got)
	}
}

func TestASingleLineEditStillGetsANearMiss(t *testing.T) {
	// The compact format asks for only enough context to be unique, so
	// one-line edits are the common case — and they used to be the ones
	// reported with nothing but "could not find it".
	repository := project(t, map[string]string{
		"a.ts": "const board = useState(empty());\nconst other = 1;\n",
	})
	err := repository.Apply("@a.ts\n-const board = useState(() => empty());\n+const board = useState(fresh());\n")
	if err == nil {
		t.Fatal("that line is not in the file")
	}
	lines := Explain(err)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "expected: const board = useState(() => empty());") {
		t.Fatalf("should say what the edit wanted:\n%s", joined)
	}
	if !strings.Contains(joined, "in file:  const board = useState(empty());") {
		t.Fatalf("should say what is really there:\n%s", joined)
	}
}

func TestNoNearMissWhenNothingResembles(t *testing.T) {
	// Pointing at an unrelated line and calling it a near miss would send
	// the reader hunting in the wrong place.
	repository := project(t, map[string]string{"a.ts": "import fs from \"fs\";\n"})
	err := repository.Apply("@a.ts\n-const completelyDifferent = compute(alpha, beta);\n+const x = 1;\n")
	if err == nil {
		t.Fatal("expected a failure")
	}
	if joined := strings.Join(Explain(err), "\n"); strings.Contains(joined, "in file:") {
		t.Fatalf("nothing in that file resembles the line:\n%s", joined)
	}
}

func TestSimilarityScoresATranscriptionSlip(t *testing.T) {
	cases := []struct {
		name    string
		a, b    string
		atLeast float64
		atMost  float64
	}{
		{"identical", "const x = 1;", "const x = 1;", 1, 1},
		{"one word changed mid-line", "server.run(config)", "server.run(config, debug=True)", 0.5, 1},
		{"nothing in common", "import fs from \"fs\";", "export default function App() {", 0, 0.4},
		{"both empty", "", "", 1, 1},
	}
	for _, testCase := range cases {
		got := similarity(testCase.a, testCase.b)
		if got < testCase.atLeast || got > testCase.atMost {
			t.Errorf("%s: similarity = %.2f, want between %.2f and %.2f", testCase.name, got, testCase.atLeast, testCase.atMost)
		}
	}
}

func TestTwoEditsUnderOneHeaderWithNoBlankLineBetween(t *testing.T) {
	// Verbatim from a real run: two unrelated one-line changes written
	// under a single header with the blank line left out. Read as one
	// hunk, the two lines are nowhere near each other in the file and it
	// matches nothing — which cost a round trip to a punctuation rule.
	repository := project(t, map[string]string{
		"App.tsx": "const a = 1;\n" +
			"          <header><div><h1>tictacos</h1></div></header>\n" +
			"          <section className=\"lobby-card\">\n" +
			"          <header><div><p>ROOM</p><h1>tictacos</h1></div></header>\n" +
			"const b = 2;\n",
	})
	err := repository.Apply("@App.tsx\n" +
		"-          <header><div><h1>tictacos</h1></div></header>\n" +
		"+          <header><div><h1>tacos tacos</h1></div></header>\n" +
		"-          <header><div><p>ROOM</p><h1>tictacos</h1></div></header>\n" +
		"+          <header><div><p>ROOM</p><h1>tacos tacos</h1></div></header>\n")
	if err != nil {
		t.Fatalf("both edits should have placed: %v", err)
	}
	got := read(t, repository, "App.tsx")
	if strings.Contains(got, "tictacos") {
		t.Fatalf("an edit was missed:\n%s", got)
	}
	if strings.Count(got, "tacos tacos") != 2 {
		t.Fatalf("want both headings renamed:\n%s", got)
	}
	// The line between them is untouched and still in place.
	if !strings.Contains(got, `<section className="lobby-card">`) {
		t.Fatalf("the line between the two edits was disturbed:\n%s", got)
	}
}

func TestAContiguousReplacementIsNotSplit(t *testing.T) {
	// A genuine multi-line replacement writes its removals together and
	// then its additions, so it never hits the split rule.
	repository := project(t, map[string]string{"a.ts": "keep;\nfirst;\nsecond;\nkeep2;\n"})
	if err := repository.Apply("@a.ts\n-first;\n-second;\n+only;\n"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, repository, "a.ts"), "keep;\nonly;\nkeep2;\n"; got != want {
		t.Fatalf("a.ts = %q, want %q", got, want)
	}
}

func TestContextStillHoldsAHunkTogether(t *testing.T) {
	// The split only fires on a "-" straight after a "+". A context line
	// between two changes keeps them in one hunk, where they belong.
	repository := project(t, map[string]string{"a.ts": "one;\nmiddle;\ntwo;\n"})
	if err := repository.Apply("@a.ts\n-one;\n+ONE;\n middle;\n-two;\n+TWO;\n"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, repository, "a.ts"), "ONE;\nmiddle;\nTWO;\n"; got != want {
		t.Fatalf("a.ts = %q, want %q", got, want)
	}
}

// Separate edits bunched into one hunk used to cost the whole turn: the
// patch was rejected, the model regenerated it, and on a real run it
// never recovered. Each part places on its own, so the split happens here
// rather than in a round trip.
func TestBunchedEditsAreSplitAndPlaced(t *testing.T) {
	repository := project(t, map[string]string{
		"App.tsx": `type Language = "en" | "ru";
import x from "y";

function load() {
  return saved === "ru" ? saved : "en";
}

const translations = {
  pt: { createRoom: "Criar sala" },
};
`,
	})
	err := repository.Apply("@App.tsx\n" +
		"-type Language = \"en\" | \"ru\";\n" +
		"+type Language = \"en\" | \"ru\" | \"pl\";\n" +
		"-  return saved === \"ru\" ? saved : \"en\";\n" +
		"+  return saved === \"ru\" || saved === \"pl\" ? saved : \"en\";\n" +
		"   pt: { createRoom: \"Criar sala\" },\n" +
		"+  pl: { createRoom: \"Utwórz pokój\" },\n")
	if err != nil {
		t.Fatalf("three bunched edits should have been split and placed: %v", err)
	}
	got := read(t, repository, "App.tsx")
	for _, want := range []string{
		`type Language = "en" | "ru" | "pl";`,
		`saved === "ru" || saved === "pl" ? saved : "en";`,
		`pl: { createRoom: "Utwórz pokój" },`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	// And the lines between them are exactly where they were.
	for _, untouched := range []string{`import x from "y";`, `pt: { createRoom: "Criar sala" },`} {
		if !strings.Contains(got, untouched) {
			t.Fatalf("an untouched line was disturbed:\n%s", got)
		}
	}
}

// A hunk is only split where every anchor occurs exactly once. A line
// that appears twice gives no honest answer about which edit belongs
// where, so it is reported instead of guessed at.
func TestAnAmbiguousAnchorIsReportedRatherThanSplit(t *testing.T) {
	repository := project(t, map[string]string{
		"a.ts": "call();\nfiller\ncall();\nmore\nother();\n",
	})
	err := repository.Apply("@a.ts\n call();\n-other();\n+changed();\n")
	if err == nil {
		t.Fatal("that hunk's anchor appears twice and is not adjacent to the rest")
	}
	if joined := strings.Join(Explain(err), "\n"); !strings.Contains(joined, "a.ts:") {
		t.Fatalf("Explain() = %q", joined)
	}
}

// A line cannot differ from itself. When a block fails for a structural
// reason rather than a textual one, the report has to say something other
// than "expected X, in file X".
func TestANearMissNeverReportsALineAsDifferingFromItself(t *testing.T) {
	repository := project(t, map[string]string{
		"a.ts": "const kept = 1;\nfiller\nconst kept = 1;\nmore\nconst other = 2;\n",
	})
	// "const kept = 1;" appears twice, so the hunk cannot be split and
	// falls through to the reporting path.
	err := repository.Apply("@a.ts\n const kept = 1;\n-const other = 2;\n+merged;\n")
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, line := range Explain(err) {
		if strings.HasPrefix(line, "expected: ") && strings.Contains(line, "const kept") {
			t.Fatalf("a line cannot differ from itself:\n%v", Explain(err))
		}
	}
}

func TestAPatchThatChangesNothingIsRefused(t *testing.T) {
	// Pure context lines place perfectly and write the file back byte for
	// byte. Reported as "Applied", that spent one of the turn's few edits
	// and told the model its change had landed when nothing happened.
	repository := project(t, map[string]string{"a.ts": "const x = 1;\nconst y = 2;\n"})
	err := repository.Apply("@a.ts\n const x = 1;\n const y = 2;\n")
	if err == nil {
		t.Fatal("a patch with no changes in it should be refused")
	}
	if !strings.Contains(err.Error(), "changes nothing") {
		t.Fatalf("err = %v", err)
	}
	if got := read(t, repository, "a.ts"); got != "const x = 1;\nconst y = 2;\n" {
		t.Fatalf("the file should be untouched: %q", got)
	}
}

func TestAtAtSeparatesEditsTheWayModelsWriteThem(t *testing.T) {
	// Verbatim shape from a real run. The model separated its edits with
	// "@@" — the marker in the one diff format everything has seen —
	// rather than the blank line this format asks for. Read as content,
	// "@@" is a line to match and is nowhere in any file, so every edit
	// after the first failed. That turn spent its whole edit budget and a
	// 64-second whole-file rewrite on it.
	repository := project(t, map[string]string{
		"tetrapong.ts": `export type TetrominoId = "i" | "o";
export type Phase = "idle" | "playing";
filler
  private sfx: Sfx;
  private onHud: (h: Hud) => void;
more filler
  setMouse(x: number | null) {
    this.mouseX = x;
  }
`,
	})
	err := repository.Apply(`@tetrapong.ts
 export type TetrominoId = "i" | "o";
+export type Language = "en" | "es";
 export type Phase = "idle" | "playing";
@@
   private onHud: (h: Hud) => void;
+  private language: Language = "en";
@@
   setMouse(x: number | null) {
     this.mouseX = x;
   }
+
+  setLanguage(language: Language) {
+    this.language = language;
+  }
`)
	if err != nil {
		t.Fatalf("all three edits should have placed: %v", err)
	}
	got := read(t, repository, "tetrapong.ts")
	for _, want := range []string{
		`export type Language = "en" | "es";`,
		`private language: Language = "en";`,
		"setLanguage(language: Language) {",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "@@") {
		t.Fatalf("a separator was written into the file:\n%s", got)
	}
}

func TestApplyPatchMarkersAreSeparatorsToo(t *testing.T) {
	// The same mistake in the other borrowed format: a compact patch that
	// trails off into "*** End Patch".
	repository := project(t, map[string]string{"a.ts": "const x = 1;\nconst y = 2;\n"})
	if err := repository.Apply("@a.ts\n-const x = 1;\n+const x = 9;\n*** End Patch\n"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, repository, "a.ts"); got != "const x = 9;\nconst y = 2;\n" {
		t.Fatalf("a.ts = %q", got)
	}
}

func TestALineThatReallyStartsWithAtAtIsStillContent(t *testing.T) {
	// The separator check is anchored at column zero, so a context line —
	// which always carries a leading space — is never mistaken for one.
	repository := project(t, map[string]string{"notes.md": "intro\n@@ this is content @@\ntrailer\n"})
	if err := repository.Apply("@notes.md\n @@ this is content @@\n-trailer\n+footer\n"); err != nil {
		t.Fatalf("a real line beginning with @@ should still match: %v", err)
	}
	if got := read(t, repository, "notes.md"); got != "intro\n@@ this is content @@\nfooter\n" {
		t.Fatalf("notes.md = %q", got)
	}
}

func TestABareRowOfStarsSeparatesEdits(t *testing.T) {
	// Verbatim from a real run: the model divided its edits with "***"
	// and the separator check wanted "*** " with a space, so the stars
	// were read as a line of code to find. Reported as `expected: ***`
	// against a real line, four patches in a row.
	repository := project(t, map[string]string{"a.ts": "const x = 1;\nfiller\nconst y = 2;\n"})
	if err := repository.Apply("@a.ts\n-const x = 1;\n+const x = 9;\n***\n-const y = 2;\n+const y = 8;\n"); err != nil {
		t.Fatalf("both edits should have placed: %v", err)
	}
	got := read(t, repository, "a.ts")
	if got != "const x = 9;\nfiller\nconst y = 8;\n" {
		t.Fatalf("a.ts = %q", got)
	}
	if strings.Contains(got, "***") {
		t.Fatalf("a divider was written into the file:\n%s", got)
	}
}

func TestStarsInsideAFileAreStillContent(t *testing.T) {
	// Markdown emphasis at the start of a line survives, because a
	// context line carries its leading space.
	repository := project(t, map[string]string{"notes.md": "intro\n***bold***\ntrailer\n"})
	if err := repository.Apply("@notes.md\n ***bold***\n-trailer\n+footer\n"); err != nil {
		t.Fatalf("a real line of stars should still match: %v", err)
	}
	if got := read(t, repository, "notes.md"); got != "intro\n***bold***\nfooter\n" {
		t.Fatalf("notes.md = %q", got)
	}
}

func TestAStrayMarkerInAHunkIsNamed(t *testing.T) {
	// The generic answer to a convention we have not met yet. Four of
	// them have each cost a whole turn — "@@", "*** ", "***" and a
	// missing blank line — and each was diagnosed by hand afterwards.
	// A line with no relative anywhere in the file says so for itself.
	repository := project(t, map[string]string{
		"a.ts": "const first = 1;\nconst second = 2;\nconst third = 3;\n",
	})
	// "~~~~" is a divider this format has never heard of.
	err := repository.Apply("@a.ts\n const first = 1;\n~~~~\n-const second = 2;\n+const second = 9;\n")
	if err == nil {
		t.Fatal("that hunk cannot be placed")
	}
	lines := strings.Join(Explain(err), "\n")
	if !strings.Contains(lines, "nowhere in the file") {
		t.Fatalf("the stray line should be called out:\n%s", lines)
	}
	if !strings.Contains(lines, "~~~~") {
		t.Fatalf("it should say which line:\n%s", lines)
	}
	if !strings.Contains(err.Error(), "a blank line does that") {
		t.Fatalf("and what to do about it:\n%s", err.Error())
	}
}

func TestATranscriptionSlipStillGetsTheNearMiss(t *testing.T) {
	// A line copied almost right is also absent, but for that the
	// near-miss report says far more than "it is not there".
	repository := project(t, map[string]string{
		"a.ts": "const a = 1;\nconst board = useState(empty());\nconst c = 3;\n",
	})
	err := repository.Apply("@a.ts\n const a = 1;\n-const board = useState(() => empty());\n+const board = useState(fresh());\n const c = 3;\n")
	if err == nil {
		t.Fatal("expected a failure")
	}
	joined := strings.Join(Explain(err), "\n")
	if strings.Contains(joined, "nowhere in the file") {
		t.Fatalf("a near miss is not a stray marker:\n%s", joined)
	}
	if !strings.Contains(joined, "useState(empty())") {
		t.Fatalf("it should show what is really there:\n%s", joined)
	}
}

func TestAFileHeaderBehindAMarkerIsStillAFileHeader(t *testing.T) {
	// Verbatim from a real run: a second file introduced as
	// "*** @path". Read as a plain separator that header is dropped, and
	// every edit under it is attributed to the previous file — nine edits
	// went looking for their lines in App.tsx, where they had never been.
	repository := project(t, map[string]string{
		"App.tsx":      "const app = 1;\n",
		"tetrapong.ts": "export type Language = \"en\" | \"es\";\n",
	})
	err := repository.Apply("@App.tsx\n-const app = 1;\n+const app = 2;\n" +
		"*** @tetrapong.ts\n-export type Language = \"en\" | \"es\";\n+export type Language = \"en\" | \"es\" | \"de\";\n")
	if err != nil {
		t.Fatalf("both files should have been edited: %v", err)
	}
	if got := read(t, repository, "App.tsx"); got != "const app = 2;\n" {
		t.Fatalf("App.tsx = %q", got)
	}
	if got := read(t, repository, "tetrapong.ts"); !strings.Contains(got, `"de"`) {
		t.Fatalf("tetrapong.ts = %q", got)
	}
}

func TestAPathBehindAMarkerWithNoAtSignWorksToo(t *testing.T) {
	repository := project(t, map[string]string{
		"a.ts": "const a = 1;\n",
		"b.ts": "const b = 1;\n",
	})
	if err := repository.Apply("@a.ts\n-const a = 1;\n+const a = 2;\n" +
		"*** b.ts\n-const b = 1;\n+const b = 2;\n"); err != nil {
		t.Fatalf("apply_patch's own header shape should route too: %v", err)
	}
	if got := read(t, repository, "b.ts"); got != "const b = 2;\n" {
		t.Fatalf("b.ts = %q", got)
	}
}

func TestEndPatchAndBareStarsAreStillSeparators(t *testing.T) {
	// Neither remainder looks like a path, so neither is mistaken for a
	// header — which is what keeps the previous fix working.
	repository := project(t, map[string]string{"a.ts": "const x = 1;\nfiller\nconst y = 2;\n"})
	if err := repository.Apply("@a.ts\n-const x = 1;\n+const x = 9;\n***\n-const y = 2;\n+const y = 8;\n*** End Patch\n"); err != nil {
		t.Fatalf("both edits should have placed: %v", err)
	}
	if got := read(t, repository, "a.ts"); got != "const x = 9;\nfiller\nconst y = 8;\n" {
		t.Fatalf("a.ts = %q", got)
	}
	for _, stray := range []string{"End Patch", "***"} {
		if _, err := os.Stat(filepath.Join(repository.Root, stray)); err == nil {
			t.Fatalf("%q was taken for a file name", stray)
		}
	}
}

func TestEditsUnderTheWrongHeaderSayWhereTheyBelong(t *testing.T) {
	// An edit under the wrong header reads as lines that are simply not
	// there — true, and no help at all: the model takes its own text to
	// be wrong and rewrites what was right. Naming the file they are
	// really in ends that in one round trip.
	repository := project(t, map[string]string{
		"App.tsx":      "const app = 1;\n",
		"tetrapong.ts": "export type Language = \"en\" | \"es\";\n",
	})
	// The patch names both files, and one edit sits under the wrong
	// header — a section boundary missed rather than a file forgotten.
	err := repository.Apply("@App.tsx\n-const app = 1;\n+const app = 2;\n\n" +
		"-export type Language = \"en\" | \"es\";\n+export type Language = \"en\" | \"es\" | \"de\";\n" +
		"\n@tetrapong.ts\n const unrelated = 1;\n")
	if err == nil {
		t.Fatal("the second edit is not in App.tsx")
	}
	joined := strings.Join(Explain(err), "\n")
	if !strings.Contains(joined, "these lines are in tetrapong.ts, not in App.tsx") {
		t.Fatalf("it should say where they really are:\n%s", joined)
	}
	if !strings.Contains(err.Error(), "header naming tetrapong.ts") {
		t.Fatalf("and what to do about it:\n%s", err.Error())
	}
	// And nothing was written, as always.
	if got := read(t, repository, "App.tsx"); got != "const app = 1;\n" {
		t.Fatalf("App.tsx = %q", got)
	}
}
