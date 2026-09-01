package terminal

import (
	"bufio"
	"strings"
	"testing"
)

// press feeds one escape sequence to the editor's key handling and
// reports what the buffer holds afterwards.
func press(t *testing.T, buffer *textBuffer, history *recall, sequence string) string {
	t.Helper()
	reader := bufio.NewReader(strings.NewReader(sequence))
	// The leading ESC is consumed by the caller in EditTask.
	if first, err := reader.ReadByte(); err != nil || first != 27 {
		t.Fatalf("sequence %q must start with ESC", sequence)
	}
	if _, err := handleEditorEscape(reader, buffer, history); err != nil {
		t.Fatal(err)
	}
	return string(buffer.text)
}

const (
	up   = "\x1b[A"
	down = "\x1b[B"
)

func TestUpRecallsEarlierTasksNewestFirst(t *testing.T) {
	buffer := &textBuffer{}
	history := newRecall([]string{"first task", "second task"})

	if got := press(t, buffer, history, up); got != "second task" {
		t.Fatalf("first Up = %q, want the newest task", got)
	}
	if got := press(t, buffer, history, up); got != "first task" {
		t.Fatalf("second Up = %q, want the older task", got)
	}
	// Past the oldest, Up does nothing rather than clearing the prompt.
	if got := press(t, buffer, history, up); got != "first task" {
		t.Fatalf("third Up = %q, want to stay on the oldest", got)
	}
	if buffer.cursor != len([]rune("first task")) {
		t.Fatalf("cursor = %d, want the end of the recalled task", buffer.cursor)
	}
}

func TestDownReturnsToWhatWasBeingTyped(t *testing.T) {
	buffer := &textBuffer{}
	buffer.insert("half-written")
	history := newRecall([]string{"older"})

	if got := press(t, buffer, history, up); got != "older" {
		t.Fatalf("Up = %q", got)
	}
	if got := press(t, buffer, history, down); got != "half-written" {
		t.Fatalf("Down = %q, want the draft back", got)
	}
	// Already back at the draft, Down is an ordinary cursor move.
	if got := press(t, buffer, history, down); got != "half-written" {
		t.Fatalf("second Down = %q", got)
	}
}

func TestEditsToARecalledTaskSurviveSteppingAway(t *testing.T) {
	buffer := &textBuffer{}
	history := newRecall([]string{"older", "newer"})

	press(t, buffer, history, up) // "newer"
	buffer.insert("!")
	press(t, buffer, history, up) // "older"
	if got := press(t, buffer, history, down); got != "newer!" {
		t.Fatalf("Down = %q, want the edit kept", got)
	}
}

func TestUpMovesTheCursorInsideAMultiLineDraft(t *testing.T) {
	// Recall must not hijack the arrow keys mid-draft: from anywhere but
	// the top line, Up is still just a cursor move.
	buffer := &textBuffer{}
	buffer.insert("line one\nline two")
	history := newRecall([]string{"an earlier task"})

	if got := press(t, buffer, history, up); got != "line one\nline two" {
		t.Fatalf("Up rewrote the draft: %q", got)
	}
	if row, _ := buffer.position(); row != 0 {
		t.Fatalf("cursor row = %d, want the first line", row)
	}
	// Now on the top line, a second Up does recall.
	if got := press(t, buffer, history, up); got != "an earlier task" {
		t.Fatalf("second Up = %q, want the recalled task", got)
	}
}

func TestDownMovesTheCursorInsideARecalledMultiLineTask(t *testing.T) {
	buffer := &textBuffer{}
	history := newRecall([]string{"one\ntwo"})

	press(t, buffer, history, up)
	buffer.setPosition(0, 0)
	// Not on the last line, so Down moves within the task.
	if got := press(t, buffer, history, down); got != "one\ntwo" {
		t.Fatalf("Down left the task: %q", got)
	}
	if row, _ := buffer.position(); row != 1 {
		t.Fatalf("cursor row = %d, want the second line", row)
	}
	// On the last line now, Down steps forward to the empty draft.
	if got := press(t, buffer, history, down); got != "" {
		t.Fatalf("Down = %q, want the draft", got)
	}
}

func TestRecallWithNoHistoryLeavesTheArrowsAlone(t *testing.T) {
	buffer := &textBuffer{}
	buffer.insert("typing")
	history := newRecall(nil)
	if got := press(t, buffer, history, up); got != "typing" {
		t.Fatalf("Up = %q", got)
	}
	if got := press(t, buffer, history, down); got != "typing" {
		t.Fatalf("Down = %q", got)
	}
}
