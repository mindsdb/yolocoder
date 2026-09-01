package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sandbox points the session store at a temporary home.
func sandbox(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestAppendAndReadBack(t *testing.T) {
	sandbox(t)
	folder := "/projects/game"

	log, err := Open(folder, "term-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(Turn{Message: "make it full screen", Kind: "code", Summary: "Fit the viewport", Files: []string{"index.html"}, Applied: true}); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(Turn{Message: "what does this do?", Kind: "chat"}); err != nil {
		t.Fatal(err)
	}

	turns, err := Recent(folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	// Oldest first, and numbered from one so a window never renumbers.
	if turns[0].Number != 1 || turns[1].Number != 2 {
		t.Fatalf("numbers = %d, %d", turns[0].Number, turns[1].Number)
	}
	if turns[0].Message != "make it full screen" || turns[0].Summary != "Fit the viewport" {
		t.Fatalf("first turn = %+v", turns[0])
	}
	if len(turns[0].Files) != 1 || turns[0].Files[0] != "index.html" {
		t.Fatalf("files = %v", turns[0].Files)
	}
	if turns[1].Kind != "chat" {
		t.Fatalf("second turn = %+v", turns[1])
	}
}

func TestHistoryIsScopedToItsFolder(t *testing.T) {
	// One project's context must never bleed into another's.
	sandbox(t)
	first, err := Open("/projects/alpha", "term-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Append(Turn{Message: "alpha work", Kind: "code"}); err != nil {
		t.Fatal(err)
	}
	second, err := Open("/projects/beta", "term-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Append(Turn{Message: "beta work", Kind: "code"}); err != nil {
		t.Fatal(err)
	}

	alpha, err := Recent("/projects/alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(alpha) != 1 || alpha[0].Message != "alpha work" {
		t.Fatalf("alpha history = %+v", alpha)
	}
	beta, err := Recent("/projects/beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(beta) != 1 || beta[0].Message != "beta work" {
		t.Fatalf("beta history = %+v", beta)
	}
}

func TestSameTerminalContinuesItsOwnSession(t *testing.T) {
	sandbox(t)
	folder := "/projects/game"

	first, err := Open(folder, "term-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Append(Turn{Message: "one", Kind: "code"}); err != nil {
		t.Fatal(err)
	}

	// The same terminal picks the thread back up, and numbering carries
	// on rather than restarting.
	again, err := Open(folder, "term-1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Path() != first.Path() {
		t.Fatalf("expected the same session file, got %s and %s", again.Path(), first.Path())
	}
	if err := again.Append(Turn{Message: "two", Kind: "code"}); err != nil {
		t.Fatal(err)
	}
	turns, _ := Recent(folder)
	if len(turns) != 2 || turns[1].Number != 2 {
		t.Fatalf("turns = %+v", turns)
	}

	// A different terminal in the same folder starts its own thread but
	// still contributes to the folder's history.
	other, err := Open(folder, "term-2")
	if err != nil {
		t.Fatal(err)
	}
	if other.Path() == first.Path() {
		t.Fatal("a different terminal should start its own session file")
	}
	if err := other.Append(Turn{Message: "three", Kind: "code"}); err != nil {
		t.Fatal(err)
	}
	if turns, _ := Recent(folder); len(turns) != 3 {
		t.Fatalf("folder history = %+v", turns)
	}
}

func TestRecentDropsAnythingOlderThanTheAgeCap(t *testing.T) {
	sandbox(t)
	folder := "/projects/game"
	log, err := Open(folder, "term-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(Turn{At: time.Now().Add(-72 * time.Hour), Message: "ancient", Kind: "code"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(Turn{Message: "recent", Kind: "code"}); err != nil {
		t.Fatal(err)
	}
	turns, err := Recent(folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Message != "recent" {
		t.Fatalf("turns = %+v, want only the recent one", turns)
	}
}

func TestRecentKeepsTheNewestWithinTheCaps(t *testing.T) {
	sandbox(t)
	folder := "/projects/game"
	log, err := Open(folder, "term-1")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxTurns+10; index++ {
		if err := log.Append(Turn{Message: "turn", Kind: "code"}); err != nil {
			t.Fatal(err)
		}
	}
	turns, err := Recent(folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) > MaxTurns {
		t.Fatalf("turns = %d, want at most %d", len(turns), MaxTurns)
	}
	// The newest survive, and their absolute numbers are preserved.
	last := turns[len(turns)-1]
	if last.Number != MaxTurns+10 {
		t.Fatalf("last turn number = %d, want %d", last.Number, MaxTurns+10)
	}
}

func TestRecentRespectsTheByteBudget(t *testing.T) {
	sandbox(t)
	folder := "/projects/game"
	log, err := Open(folder, "term-1")
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 3000)
	for index := 0; index < 5; index++ {
		if err := log.Append(Turn{Message: big, Kind: "code"}); err != nil {
			t.Fatal(err)
		}
	}
	turns, err := Recent(folder)
	if err != nil {
		t.Fatal(err)
	}
	if size(turns) > MaxBytes {
		t.Fatalf("window is %d bytes, over the %d budget", size(turns), MaxBytes)
	}
	if len(turns) == 0 {
		t.Fatal("the budget should trim the window, not empty it")
	}
}

func TestRecentIsEmptyForAnUntouchedFolder(t *testing.T) {
	sandbox(t)
	turns, err := Recent("/projects/never-used")
	if err != nil {
		t.Fatalf("an unused folder should not be an error: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestASessionFileSurvivesAHalfWrittenLine(t *testing.T) {
	// A crash mid-write must not make the whole history unreadable.
	sandbox(t)
	folder := "/projects/game"
	log, err := Open(folder, "term-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(Turn{Message: "good", Kind: "code"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(log.Path(), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":"turn","messa`); err != nil {
		t.Fatal(err)
	}
	file.Close()

	turns, err := Recent(folder)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Message != "good" {
		t.Fatalf("turns = %+v, want the intact turn", turns)
	}
}

func TestSessionFilesAreNotWorldReadable(t *testing.T) {
	sandbox(t)
	log, err := Open("/projects/game", "term-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(Turn{Message: "something private", Kind: "code"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("session file mode = %o, want 600", permissions)
	}
}

func TestFolderDirectoryIsStableAndPathSafe(t *testing.T) {
	sandbox(t)
	// Awkward paths must not escape into surprising filenames.
	for _, folder := range []string{"/projects/a b/c", "/proj/ünïcode", `/weird/..\name`} {
		directory, err := folderDirectory(folder)
		if err != nil {
			t.Fatal(err)
		}
		if base := filepath.Base(directory); strings.ContainsAny(base, `/\ .`) {
			t.Fatalf("directory %q for %q is not a plain name", base, folder)
		}
		again, _ := folderDirectory(folder)
		if again != directory {
			t.Fatalf("folderDirectory(%q) is not stable", folder)
		}
	}
	first, _ := folderDirectory("/projects/one")
	second, _ := folderDirectory("/projects/two")
	if first == second {
		t.Fatal("different folders must not share a directory")
	}
}

func TestTerminalPrefersAnExplicitIdentifier(t *testing.T) {
	values := map[string]string{"TERM_SESSION_ID": "abc-123"}
	if got := Terminal(func(key string) string { return values[key] }); got != "abc-123" {
		t.Fatalf("Terminal() = %q", got)
	}
	if got := Terminal(func(string) string { return "" }); strings.Contains(got, "abc") {
		t.Fatalf("Terminal() = %q, want a fallback", got)
	}
}
