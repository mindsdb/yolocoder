package app

import (
	"strings"
	"testing"
)

func TestParseWebRecognizesEachFlagAnywhereInArgs(t *testing.T) {
	useWeb, port, debugOn, rest, err := ParseWeb([]string{"build", "--web", "me", "--debug", "a", "game"})
	if err != nil {
		t.Fatal(err)
	}
	if !useWeb || !debugOn {
		t.Fatalf("useWeb=%v debugOn=%v, want both true", useWeb, debugOn)
	}
	if port != 0 {
		t.Fatalf("port = %d, want 0 (not given)", port)
	}
	if strings.Join(rest, " ") != "build me a game" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestParseWebPortBothSpellings(t *testing.T) {
	for _, args := range [][]string{
		{"--web", "--port", "9000"},
		{"--web", "--port=9000"},
	} {
		useWeb, port, _, rest, err := ParseWeb(args)
		if err != nil {
			t.Fatalf("ParseWeb(%v) = %v", args, err)
		}
		if !useWeb || port != 9000 {
			t.Fatalf("ParseWeb(%v) = useWeb=%v port=%d, want true, 9000", args, useWeb, port)
		}
		if len(rest) != 0 {
			t.Fatalf("ParseWeb(%v) rest = %v, want none left over", args, rest)
		}
	}
}

func TestParseWebWithoutAnyFlags(t *testing.T) {
	useWeb, port, debugOn, rest, err := ParseWeb([]string{"fix", "the", "build"})
	if err != nil {
		t.Fatal(err)
	}
	if useWeb || debugOn || port != 0 {
		t.Fatalf("useWeb=%v debugOn=%v port=%d, want all false/zero", useWeb, debugOn, port)
	}
	if strings.Join(rest, " ") != "fix the build" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestParseWebRefusesAMissingPortValue(t *testing.T) {
	if _, _, _, _, err := ParseWeb([]string{"--web", "--port"}); err == nil {
		t.Fatal("expected an error for --port with no value")
	}
}

func TestParseWebRefusesANonNumericPort(t *testing.T) {
	if _, _, _, _, err := ParseWeb([]string{"--web", "--port", "abc"}); err == nil {
		t.Fatal("expected an error for a non-numeric --port")
	}
}

func TestParseContextTakesBothSpellings(t *testing.T) {
	notes, rest, err := ParseContext([]string{"--context", "one", "--context=two", "fix", "the", "build"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0] != "one" || notes[1] != "two" {
		t.Fatalf("notes = %q", notes)
	}
	if strings.Join(rest, " ") != "fix the build" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestParseContextKeepsTheOrderOfTheTask(t *testing.T) {
	// The task is rebuilt by joining what is left, so --context appearing
	// in the middle must not shuffle the words around it.
	_, rest, err := ParseContext([]string{"rename", "--context", "note", "the", "title"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rest, " "); got != "rename the title" {
		t.Fatalf("rest = %q", got)
	}
}

func TestParseContextReadsStdinForDash(t *testing.T) {
	notes, rest, err := ParseContext([]string{"--context", "-", "go"}, strings.NewReader("piped background\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0] != "piped background" {
		t.Fatalf("notes = %q", notes)
	}
	if len(rest) != 1 || rest[0] != "go" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestParseContextRefusesASecondStdinRead(t *testing.T) {
	// The first read consumes stdin entirely, so a second would silently
	// come back empty.
	_, _, err := ParseContext([]string{"--context", "-", "--context", "-"}, strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected an error for two stdin notes")
	}
}

func TestParseContextRefusesAMissingValue(t *testing.T) {
	if _, _, err := ParseContext([]string{"do", "it", "--context"}, nil); err == nil {
		t.Fatal("expected an error for a trailing --context")
	}
}

func TestParseContextDropsEmptyNotes(t *testing.T) {
	notes, _, err := ParseContext([]string{"--context", "   ", "--context=", "task"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %q, want none", notes)
	}
}

func TestParseContextStopsAtADoubleDash(t *testing.T) {
	notes, rest, err := ParseContext([]string{"--context", "real", "--", "--context", "literal"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0] != "real" {
		t.Fatalf("notes = %q", notes)
	}
	if strings.Join(rest, " ") != "--context literal" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestNotesCarryNoTurnNumber(t *testing.T) {
	// A number would make a note look like a recorded turn and collide
	// with the folder history's own numbering.
	for _, note := range Notes([]string{"a", "b"}) {
		if note.Number != 0 {
			t.Fatalf("note %+v should not be numbered", note)
		}
		if !note.Note {
			t.Fatalf("note %+v should be marked as supplied", note)
		}
	}
}
