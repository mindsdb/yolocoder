package terminal

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

var ErrEditorCancelled = errors.New("task entry cancelled")

// editorPrefixWidth is the width of the "› " / "│ " marker each prompt
// line is drawn with.
const editorPrefixWidth = 2

// Command is a slash command the prompt offers. Typing "/" lists them and
// filters as you keep typing, so they're discoverable rather than
// something you have to already know.
type Command struct {
	Name        string
	Description string
}

// commandToken is the leading "/word" of text, or "" when text isn't a
// single-line slash command.
func commandToken(text string) string {
	if !strings.HasPrefix(text, "/") || strings.Contains(text, "\n") {
		return ""
	}
	if space := strings.IndexAny(text, " \t"); space != -1 {
		return text[:space]
	}
	return text
}

// matchingCommands are the commands whose names start with text's leading
// slash token. A token that already names a command exactly matches only
// that one, so the list collapses to a confirmation as you finish typing.
func matchingCommands(commands []Command, text string) []Command {
	token := commandToken(text)
	if token == "" {
		return nil
	}
	var matches []Command
	for _, command := range commands {
		if strings.HasPrefix(command.Name, token) {
			matches = append(matches, command)
		}
	}
	return matches
}

// renderPromptLine colors a leading slash command so it reads as a
// command rather than as text that will be sent to the model.
func renderPromptLine(line string, first bool) string {
	if !first {
		return line
	}
	token := commandToken(line)
	if token == "" {
		return line
	}
	return "\x1b[35m" + token + "\x1b[0m" + line[len(token):]
}

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

// set replaces the whole buffer and leaves the cursor at the end, which
// is where recalling an earlier task should drop you.
func (buffer *textBuffer) set(text string) {
	buffer.text = []rune(text)
	buffer.cursor = len(buffer.text)
}

// rows is the number of lines the buffer holds.
func (buffer *textBuffer) rows() int {
	return strings.Count(string(buffer.text), "\n") + 1
}

// recall steps back through earlier tasks with Up and forward with Down.
//
// Index len(entries) is the draft — whatever was being typed before the
// first Up — so stepping all the way forward lands back on it rather than
// on an empty prompt.
type recall struct {
	entries []string // oldest first
	index   int
	// edited holds changes made to a recalled task, so stepping past one
	// and back does not silently throw away what was typed into it.
	edited map[int]string
}

func newRecall(entries []string) *recall {
	return &recall{entries: entries, index: len(entries)}
}

// active reports whether an earlier task is currently on screen, which is
// what makes Down a step forward rather than a cursor move.
func (history *recall) active() bool {
	return history != nil && history.index < len(history.entries)
}

func (history *recall) at(index int) string {
	if text, ok := history.edited[index]; ok {
		return text
	}
	if index < 0 || index >= len(history.entries) {
		return ""
	}
	return history.entries[index]
}

func (history *recall) stash(index int, text string) {
	if history.edited == nil {
		history.edited = make(map[int]string)
	}
	history.edited[index] = text
}

// step moves by direction through the entries, loading what it lands on
// into the buffer. It reports whether it moved; when it did not, the key
// falls through to its ordinary cursor movement.
func (history *recall) step(buffer *textBuffer, direction int) bool {
	if history == nil {
		return false
	}
	target := history.index + direction
	if target < 0 || target > len(history.entries) {
		return false
	}
	history.stash(history.index, string(buffer.text))
	history.index = target
	buffer.set(history.at(target))
	return true
}

func (buffer *textBuffer) home() {
	row, _ := buffer.position()
	buffer.setPosition(row, 0)
}

func (buffer *textBuffer) end() {
	row, _ := buffer.position()
	buffer.setPosition(row, int(^uint(0)>>1))
}

// EditTask reads one task. earlier is the tasks already entered, oldest
// first, which Up steps back through from the prompt's top line.
func (reader *Reader) EditTask(output io.Writer, commands []Command, earlier []string) (string, error) {
	if !IsTTY(reader.input) {
		return "", errors.New("multiline task entry requires an interactive terminal")
	}
	state, err := term.MakeRaw(int(reader.input.Fd()))
	if err != nil {
		return "", err
	}
	defer term.Restore(int(reader.input.Fd()), state)

	// >4;2m and >1u ask the terminal to report Shift+Enter distinctly from
	// Enter (xterm modifyOtherKeys and the Kitty keyboard protocol,
	// respectively); terminals that don't understand them ignore them.
	// Deliberately no alternate screen: the prompt draws inline so the
	// session's transcript above it stays on screen and in scrollback.
	fmt.Fprint(output, "\x1b[?2004h\x1b[>4;2m\x1b[>1u")
	defer fmt.Fprint(output, "\x1b[<u\x1b[>4;0m\x1b[?2004l")

	buffer := &textBuffer{}
	history := newRecall(earlier)
	layout := editorLayout{}
	defer func() {
		// Leave the cursor on a fresh line below the prompt so whatever
		// prints next starts cleanly instead of overwriting it.
		if down := layout.rows - layout.cursorRow; down > 0 {
			fmt.Fprintf(output, "\x1b[%dB", down)
		}
		fmt.Fprint(output, "\r")
	}()
	for {
		layout = drawEditor(output, buffer, reader.width(), commands, layout)
		key, err := reader.buffer.ReadByte()
		if err != nil {
			return "", ErrEditorCancelled
		}
		switch key {
		case 3:
			return "", ErrEditorCancelled
		case 4, '\r':
			if task, ok := submit(buffer); ok {
				return task, nil
			}
		case '\n':
			buffer.insert("\n")
		case 8, 127:
			buffer.backspace()
		case 1:
			buffer.home()
		case 5:
			buffer.end()
		case 27:
			enter, err := handleEditorEscape(reader.buffer, buffer, history)
			if err != nil {
				return "", err
			}
			if enter {
				if task, ok := submit(buffer); ok {
					return task, nil
				}
			}
		default:
			character, err := readRune(reader.buffer, key)
			if err == nil && character >= ' ' {
				buffer.insert(string(character))
			}
		}
	}
}

func submit(buffer *textBuffer) (string, bool) {
	task := strings.TrimSpace(string(buffer.text))
	return task, task != ""
}

// editorLayout records how much of the terminal the prompt currently
// occupies, so the next redraw can replace exactly that region in place
// instead of clearing the screen and losing the transcript above it.
type editorLayout struct {
	rows      int // terminal rows the prompt occupies, wrapping included
	cursorRow int // 0-based row of the text cursor within those rows
}

// width reports the terminal's column count, falling back to a sane
// default when it can't be determined.
func (reader *Reader) width() int {
	columns, _, err := term.GetSize(int(reader.input.Fd()))
	if err != nil || columns < editorPrefixWidth+2 {
		return 80
	}
	return columns
}

// wrappedRows is how many terminal rows a prompt line of the given rune
// count occupies once the marker prefix and wrapping are accounted for.
// A line that exactly fills the width still occupies one row, so this
// rounds up rather than adding one unconditionally.
func wrappedRows(runes, width int) int {
	cells := runes + editorPrefixWidth
	rows := (cells + width - 1) / width
	if rows < 1 {
		return 1
	}
	return rows
}

func drawEditor(output io.Writer, buffer *textBuffer, width int, commands []Command, previous editorLayout) editorLayout {
	if previous.cursorRow > 0 {
		fmt.Fprintf(output, "\x1b[%dA", previous.cursorRow)
	}
	// Back to column 1 and wipe everything below: handles the prompt
	// shrinking as well as growing.
	fmt.Fprint(output, "\r\x1b[0J")

	lines := strings.Split(string(buffer.text), "\n")
	row, column := buffer.position()
	layout := editorLayout{}
	for index, line := range lines {
		marker := "\x1b[36m│\x1b[0m "
		if index == 0 {
			marker = "\x1b[36m›\x1b[0m "
		}
		fmt.Fprintf(output, "%s%s\r\n", marker, renderPromptLine(line, index == 0))
		if index == row {
			layout.cursorRow = layout.rows + (column+editorPrefixWidth)/width
		}
		layout.rows += wrappedRows(len([]rune(line)), width)
	}

	// The command list sits below the input, so it costs rows the next
	// redraw has to clear but never moves the text cursor.
	for _, command := range matchingCommands(commands, string(buffer.text)) {
		line := fmt.Sprintf("  %-10s %s", command.Name, command.Description)
		fmt.Fprintf(output, "\x1b[35m  %-10s\x1b[0m \x1b[2m%s\x1b[0m\r\n", command.Name, command.Description)
		layout.rows += wrappedRows(len([]rune(line))-editorPrefixWidth, width)
	}

	// The trailing \r\n left the cursor below everything drawn; come back
	// up to the line the text cursor belongs on and sit at the right column.
	if up := layout.rows - layout.cursorRow; up > 0 {
		fmt.Fprintf(output, "\x1b[%dA", up)
	}
	fmt.Fprintf(output, "\r\x1b[%dG", (column+editorPrefixWidth)%width+1)
	return layout
}

// handleEditorEscape parses one CSI sequence following an ESC byte and
// applies it to buffer. It reports enter=true when the sequence represents
// a plain Enter key reported through the CSI u keyboard protocol.
func handleEditorEscape(reader *bufio.Reader, buffer *textBuffer, history *recall) (bool, error) {
	next, err := reader.ReadByte()
	if err != nil {
		return false, ErrEditorCancelled
	}
	if next != '[' {
		return false, nil
	}
	first, err := reader.ReadByte()
	if err != nil {
		return false, ErrEditorCancelled
	}
	var params strings.Builder
	final := first
	for final < 0x40 || final > 0x7e {
		params.WriteByte(final)
		final, err = reader.ReadByte()
		if err != nil {
			return false, ErrEditorCancelled
		}
	}
	switch final {
	case 'A':
		// Up recalls an earlier task only from the top line, so it still
		// moves the cursor normally inside a multi-line draft.
		if row, _ := buffer.position(); row > 0 || !history.step(buffer, -1) {
			buffer.moveVertical(-1)
		}
	case 'B':
		// Symmetrically, Down steps forward only from the bottom line,
		// and only while an earlier task is what is on screen.
		row, _ := buffer.position()
		if row < buffer.rows()-1 || !history.active() || !history.step(buffer, 1) {
			buffer.moveVertical(1)
		}
	case 'C':
		buffer.moveHorizontal(1)
	case 'D':
		buffer.moveHorizontal(-1)
	case 'H':
		buffer.home()
	case 'F':
		buffer.end()
	case '~':
		switch params.String() {
		case "3":
			buffer.delete()
		case "200":
			pasted, err := readBracketedPaste(reader)
			if err != nil {
				return false, err
			}
			buffer.insert(pasted)
		}
	case 'u':
		return handleKeyboardProtocol(params.String(), buffer), nil
	}
	return false, nil
}

// handleKeyboardProtocol interprets a CSI u sequence ("codepoint;modifiers")
// sent by terminals that support xterm's modifyOtherKeys or the Kitty
// keyboard protocol. It reports enter=true for a plain Enter key press.
func handleKeyboardProtocol(params string, buffer *textBuffer) bool {
	codepoint, modifiers, _ := strings.Cut(params, ";")
	if codepoint != "13" {
		return false
	}
	if modifiers == "2" {
		buffer.insert("\n")
		return false
	}
	return true
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
