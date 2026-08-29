package terminal

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

var ErrSelectionCancelled = errors.New("selection cancelled")

type Choice struct {
	Label  string
	Detail string
}

type Reader struct {
	input  *os.File
	buffer *bufio.Reader
}

func NewReader(input *os.File) *Reader {
	return &Reader{input: input, buffer: bufio.NewReader(input)}
}

func IsTTY(file *os.File) bool {
	return term.IsTerminal(int(file.Fd()))
}

func (reader *Reader) ReadLine() (string, error) {
	line, err := reader.buffer.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" && err == io.EOF {
		return "", io.EOF
	}
	return line, nil
}

func (reader *Reader) ReadPassword(output io.Writer) (string, error) {
	fmt.Fprint(output, "API key\n> ")
	secret, err := term.ReadPassword(int(reader.input.Fd()))
	fmt.Fprintln(output)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(secret)), nil
}

func (reader *Reader) Select(output io.Writer, choices []Choice, initial int) (int, error) {
	if len(choices) == 0 {
		return 0, errors.New("no choices to select from")
	}
	if !IsTTY(reader.input) {
		return 0, errors.New("provider selection requires an interactive terminal")
	}
	state, err := term.MakeRaw(int(reader.input.Fd()))
	if err != nil {
		return 0, err
	}
	defer term.Restore(int(reader.input.Fd()), state)

	current := initial
	if current < 0 || current >= len(choices) {
		current = 0
	}
	fmt.Fprint(output, "\x1b[?25l")
	defer fmt.Fprint(output, "\x1b[?25h")

	drawn := false
	draw := func() {
		if drawn {
			fmt.Fprintf(output, "\x1b[%dF", len(choices))
		}
		drawn = true
		for index, choice := range choices {
			marker := "  "
			label := choice.Label
			if index == current {
				marker = "\x1b[32m❯\x1b[0m "
				label = "\x1b[1m" + label + "\x1b[0m"
			}
			line := marker + label
			if choice.Detail != "" {
				line += "  \x1b[2m" + choice.Detail + "\x1b[0m"
			}
			fmt.Fprintf(output, "%s\x1b[K\r\n", line)
		}
	}
	draw()

	for {
		key, err := reader.buffer.ReadByte()
		if err != nil {
			return 0, ErrSelectionCancelled
		}
		switch key {
		case 3, 'q':
			return 0, ErrSelectionCancelled
		case '\r', '\n':
			fmt.Fprintln(output)
			return current, nil
		case 'k':
			current = moveSelection(current, len(choices), -1)
		case 'j':
			current = moveSelection(current, len(choices), 1)
		case 27:
			next, err := reader.buffer.ReadByte()
			if err != nil || next != '[' {
				continue
			}
			direction, err := reader.buffer.ReadByte()
			if err != nil {
				return 0, ErrSelectionCancelled
			}
			switch direction {
			case 'A':
				current = moveSelection(current, len(choices), -1)
			case 'B':
				current = moveSelection(current, len(choices), 1)
			default:
				continue
			}
		default:
			continue
		}
		draw()
	}
}

func moveSelection(current, count, direction int) int {
	return (current + direction + count) % count
}
