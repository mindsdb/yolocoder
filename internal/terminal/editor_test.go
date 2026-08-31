package terminal

import (
	"bufio"
	"strings"
	"testing"
)

func TestTextBufferMultilineNavigationAndEdit(t *testing.T) {
	buffer := &textBuffer{}
	buffer.insert("first\nsecond")
	buffer.moveVertical(-1)
	buffer.home()
	buffer.insert("new ")
	buffer.end()
	buffer.backspace()
	if got := string(buffer.text); got != "new firs\nsecond" {
		t.Fatalf("buffer text = %q", got)
	}
	row, column := buffer.position()
	if row != 0 || column != 8 {
		t.Fatalf("position = %d,%d", row, column)
	}
}

func TestReadBracketedPastePreservesNewlines(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("line one\nline two\n\x1b[201~after"))
	pasted, err := readBracketedPaste(reader)
	if err != nil {
		t.Fatal(err)
	}
	if pasted != "line one\nline two\n" {
		t.Fatalf("paste = %q", pasted)
	}
	next, _ := reader.ReadByte()
	if next != 'a' {
		t.Fatalf("next byte = %q", next)
	}
}

func TestHandleKeyboardProtocolShiftEnterInsertsNewline(t *testing.T) {
	buffer := &textBuffer{text: []rune("hi"), cursor: 2}
	if handleKeyboardProtocol("13;2", buffer) {
		t.Fatal("shift+enter should not report enter")
	}
	if string(buffer.text) != "hi\n" {
		t.Fatalf("buffer text = %q", buffer.text)
	}
}

func TestHandleKeyboardProtocolPlainEnterReportsEnter(t *testing.T) {
	buffer := &textBuffer{text: []rune("hi")}
	if !handleKeyboardProtocol("13", buffer) {
		t.Fatal("plain enter should report enter")
	}
	if string(buffer.text) != "hi" {
		t.Fatalf("buffer text = %q", buffer.text)
	}
}

func TestHandleKeyboardProtocolIgnoresOtherKeys(t *testing.T) {
	buffer := &textBuffer{text: []rune("hi")}
	if handleKeyboardProtocol("65", buffer) {
		t.Fatal("non-enter key should not report enter")
	}
	if string(buffer.text) != "hi" {
		t.Fatalf("buffer text = %q", buffer.text)
	}
}

func TestReadRuneHandlesMultibyteInput(t *testing.T) {
	encoded := []byte("界")
	reader := bufio.NewReader(strings.NewReader(string(encoded[1:])))
	character, err := readRune(reader, encoded[0])
	if err != nil {
		t.Fatal(err)
	}
	if character != '界' {
		t.Fatalf("character = %q", character)
	}
}
