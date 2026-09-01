package app

import (
	"strings"
	"testing"
)

func TestParseFlagsTakesBothSpellings(t *testing.T) {
	flags, rest, err := ParseFlags([]string{"--context", "one", "--context=two", "fix", "the", "build"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags.Notes) != 2 || flags.Notes[0] != "one" || flags.Notes[1] != "two" {
		t.Fatalf("notes = %q", flags.Notes)
	}
	if strings.Join(rest, " ") != "fix the build" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestParseFlagsKeepsTheOrderOfTheTask(t *testing.T) {
	// The task is rebuilt by joining what is left, so --context appearing
	// in the middle must not shuffle the words around it.
	_, rest, err := ParseFlags([]string{"rename", "--context", "note", "the", "title"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rest, " "); got != "rename the title" {
		t.Fatalf("rest = %q", got)
	}
}

func TestParseFlagsReadsStdinForDash(t *testing.T) {
	flags, rest, err := ParseFlags([]string{"--context", "-", "go"}, strings.NewReader("piped background\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(flags.Notes) != 1 || flags.Notes[0] != "piped background" {
		t.Fatalf("notes = %q", flags.Notes)
	}
	if len(rest) != 1 || rest[0] != "go" {
		t.Fatalf("rest = %q", rest)
	}
}

func TestParseFlagsRefusesASecondStdinRead(t *testing.T) {
	// The first read consumes stdin entirely, so a second would silently
	// come back empty.
	_, _, err := ParseFlags([]string{"--context", "-", "--context", "-"}, strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected an error for two stdin notes")
	}
}

func TestParseFlagsRefusesAMissingValue(t *testing.T) {
	if _, _, err := ParseFlags([]string{"do", "it", "--context"}, nil); err == nil {
		t.Fatal("expected an error for a trailing --context")
	}
}

func TestParseFlagsDropsEmptyNotes(t *testing.T) {
	flags, _, err := ParseFlags([]string{"--context", "   ", "--context=", "task"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags.Notes) != 0 {
		t.Fatalf("notes = %q, want none", flags.Notes)
	}
}

func TestParseFlagsStopsAtADoubleDash(t *testing.T) {
	flags, rest, err := ParseFlags([]string{"--context", "real", "--", "--context", "literal"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags.Notes) != 1 || flags.Notes[0] != "real" {
		t.Fatalf("notes = %q", flags.Notes)
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
