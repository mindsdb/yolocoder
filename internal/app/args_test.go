package app

import (
	"strings"
	"testing"
)

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
