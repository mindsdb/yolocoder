package terminal

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

var ErrEditorCancelled = errors.New("task entry cancelled")

const editorContentRow = 4

type textBuffer struct {
	text   []rune
	cursor int
}

func (buffer *textBuffer) insert(text string) {
	runes := []rune(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"))
	updated := make([]rune, 0, len(buffer.text)+len(runes))
	updated = append(updated, buffer.text[:buffer.cursor]...)
	updated = append(updated, runes...)
	updated = append(updated, buffer.text[buffer.cursor:]...)
	buffer.text = updated
	buffer.cursor += len(runes)
}

func (buffer *textBuffer) backspace() {
	if buffer.cursor == 0 {
		return
	}
	buffer.text = append(buffer.text[:buffer.cursor-1], buffer.text[buffer.cursor:]...)
	buffer.cursor--
}

func (buffer *textBuffer) delete() {
	if buffer.cursor >= len(buffer.text) {
		return
	}
	buffer.text = append(buffer.text[:buffer.cursor], buffer.text[buffer.cursor+1:]...)
}

func (buffer *textBuffer) moveHorizontal(direction int) {
	buffer.cursor += direction
	if buffer.cursor < 0 {
		buffer.cursor = 0
	}
	if buffer.cursor > len(buffer.text) {
		buffer.cursor = len(buffer.text)
	}
}

func (buffer *textBuffer) moveVertical(direction int) {
	row, column := buffer.position()
	buffer.setPosition(row+direction, column)
}

func (buffer *textBuffer) position() (int, int) {
	row, column := 0, 0
	for _, character := range buffer.text[:buffer.cursor] {
		if character == '\n' {
			row++
			column = 0
		} else {
			column++
		}
	}
	return row, column
}

func (buffer *textBuffer) setPosition(targetRow, targetColumn int) {
	lines := strings.Split(string(buffer.text), "\n")
	if targetRow < 0 {
		targetRow = 0
	}
	if targetRow >= len(lines) {
		targetRow = len(lines) - 1
	}
	lineLength := len([]rune(lines[targetRow]))
	if targetColumn < 0 {
		targetColumn = 0
	}
	if targetColumn > lineLength {
		targetColumn = lineLength
	}
	index := 0
	for row := 0; row < targetRow; row++ {
		index += len([]rune(lines[row])) + 1
	}
	buffer.cursor = index + targetColumn
}

func (buffer *textBuffer) home() {
	row, _ := buffer.position()
	buffer.setPosition(row, 0)
}

func (buffer *textBuffer) end() {
	row, _ := buffer.position()
	buffer.setPosition(row, int(^uint(0)>>1))
}

func (reader *Reader) EditTask(output io.Writer) (string, error) {
	if !IsTTY(reader.input) {
		return "", errors.New("multiline task entry requires an interactive terminal")
	}
	state, err := term.MakeRaw(int(reader.input.Fd()))
	if err != nil {
		return "", err
	}
	defer term.Restore(int(reader.input.Fd()), state)

	fmt.Fprint(output, "\x1b[?1049h\x1b[?2004h\x1b[?1000h\x1b[?1006h")
	defer fmt.Fprint(output, "\x1b[?1006l\x1b[?1000l\x1b[?2004l\x1b[?1049l")

	buffer := &textBuffer{}
	for {
		drawEditor(output, buffer)
		key, err := reader.buffer.ReadByte()
		if err != nil {
			return "", ErrEditorCancelled
		}
		switch key {
		case 3:
			return "", ErrEditorCancelled
		case 4:
			task := strings.TrimSpace(string(buffer.text))
			if task != "" {
				return task, nil
			}
		case '\r', '\n':
			buffer.insert("\n")
		case 8, 127:
			buffer.backspace()
		case 1:
			buffer.home()
		case 5:
			buffer.end()
		case 27:
			if err := handleEditorEscape(reader.buffer, buffer); err != nil {
				return "", err
			}
		default:
			character, err := readRune(reader.buffer, key)
			if err == nil && character >= ' ' {
				buffer.insert(string(character))
			}
		}
	}
}

func drawEditor(output io.Writer, buffer *textBuffer) {
	fmt.Fprint(output, "\x1b[H\x1b[2J")
	fmt.Fprint(output, "Describe the coding task\r\n")
	fmt.Fprint(output, "\x1b[2mEnter: new line  •  Ctrl+D: submit  •  Ctrl+C: cancel  •  arrows/click: move cursor\x1b[0m\r\n\r\n")
	lines := strings.Split(string(buffer.text), "\n")
	for _, line := range lines {
		fmt.Fprintf(output, "\x1b[36m│\x1b[0m %s\x1b[K\r\n", line)
	}
	row, column := buffer.position()
	fmt.Fprintf(output, "\x1b[%d;%dH", editorContentRow+row, 3+column)
}

func handleEditorEscape(reader *bufio.Reader, buffer *textBuffer) error {
	next, err := reader.ReadByte()
	if err != nil || next != '[' {
		return nil
	}
	code, err := reader.ReadByte()
	if err != nil {
		return ErrEditorCancelled
	}
	switch code {
	case 'A':
		buffer.moveVertical(-1)
	case 'B':
		buffer.moveVertical(1)
	case 'C':
		buffer.moveHorizontal(1)
	case 'D':
		buffer.moveHorizontal(-1)
	case 'H':
		buffer.home()
	case 'F':
		buffer.end()
	case '3':
		if terminator, _ := reader.ReadByte(); terminator == '~' {
			buffer.delete()
		}
	case '2':
		sequence, err := readControlSequence(reader, '~')
		if err != nil {
			return err
		}
		if sequence == "00" {
			pasted, err := readBracketedPaste(reader)
			if err != nil {
				return err
			}
			buffer.insert(pasted)
		}
	case '<':
		sequence, err := readMouseSequence(reader)
		if err != nil {
			return err
		}
		positionMouse(buffer, sequence)
	}
	return nil
}

func readControlSequence(reader *bufio.Reader, terminator byte) (string, error) {
	var sequence strings.Builder
	for {
		character, err := reader.ReadByte()
		if err != nil {
			return "", ErrEditorCancelled
		}
		if character == terminator {
			return sequence.String(), nil
		}
		sequence.WriteByte(character)
	}
}

func readBracketedPaste(reader *bufio.Reader) (string, error) {
	const end = "\x1b[201~"
	var pasted strings.Builder
	window := ""
	for {
		character, err := reader.ReadByte()
		if err != nil {
			return "", ErrEditorCancelled
		}
		window += string(character)
		if strings.HasSuffix(window, end) {
			pasted.WriteString(strings.TrimSuffix(window, end))
			return pasted.String(), nil
		}
		if len(window) >= len(end) {
			pasted.WriteByte(window[0])
			window = window[1:]
		}
	}
}

func readMouseSequence(reader *bufio.Reader) (string, error) {
	var sequence strings.Builder
	for {
		character, err := reader.ReadByte()
		if err != nil {
			return "", ErrEditorCancelled
		}
		sequence.WriteByte(character)
		if character == 'M' || character == 'm' {
			return sequence.String(), nil
		}
	}
}

func positionMouse(buffer *textBuffer, sequence string) {
	if !strings.HasSuffix(sequence, "M") {
		return
	}
	fields := strings.Split(strings.TrimSuffix(sequence, "M"), ";")
	if len(fields) != 3 || fields[0] != "0" {
		return
	}
	column, columnErr := strconv.Atoi(fields[1])
	row, rowErr := strconv.Atoi(fields[2])
	if columnErr != nil || rowErr != nil || row < editorContentRow {
		return
	}
	buffer.setPosition(row-editorContentRow, column-3)
}

func readRune(reader *bufio.Reader, first byte) (rune, error) {
	if first < utf8.RuneSelf {
		return rune(first), nil
	}
	encoded := []byte{first}
	for len(encoded) < utf8.UTFMax && !utf8.FullRune(encoded) {
		next, err := reader.ReadByte()
		if err != nil {
			return utf8.RuneError, err
		}
		encoded = append(encoded, next)
	}
	character, size := utf8.DecodeRune(encoded)
	if character == utf8.RuneError && size == 1 {
		return character, fmt.Errorf("invalid UTF-8 input")
	}
	return character, nil
}
