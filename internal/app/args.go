package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/mindsdb/yolocoder/internal/agent"
)

// contextFlag supplies background for a one-shot run, standing in for the
// folder history a scripted call deliberately does not get.
const contextFlag = "--context"

// ParseContext pulls --context out of args and returns what it carried
// along with the arguments left over, in the order they were given.
//
// It accepts "--context text", "--context=text" and repetition, which
// accumulates. A value of "-" reads the note from stdin, so a caller can
// pipe in a file without worrying about quoting. "--" ends flag parsing
// for the benefit of a task that begins with a dash.
func ParseContext(args []string, stdin io.Reader) (notes []string, rest []string, err error) {
	usedStdin := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--":
			return notes, append(rest, args[index+1:]...), nil
		case argument == contextFlag:
			// The value is the next argument, which must exist. Silently
			// treating a trailing --context as empty would quietly drop
			// context the caller believed they had passed.
			if index+1 >= len(args) {
				return nil, nil, fmt.Errorf("%s needs a value", contextFlag)
			}
			index++
			note, err := contextValue(args[index], stdin, &usedStdin)
			if err != nil {
				return nil, nil, err
			}
			notes = appendNote(notes, note)
		case strings.HasPrefix(argument, contextFlag+"="):
			note, err := contextValue(strings.TrimPrefix(argument, contextFlag+"="), stdin, &usedStdin)
			if err != nil {
				return nil, nil, err
			}
			notes = appendNote(notes, note)
		default:
			rest = append(rest, argument)
		}
	}
	return notes, rest, nil
}

// contextValue resolves one --context value, reading stdin for "-".
func contextValue(value string, stdin io.Reader, usedStdin *bool) (string, error) {
	if value != "-" {
		return value, nil
	}
	if *usedStdin {
		return "", fmt.Errorf("%s - can only be given once: stdin is read to the end", contextFlag)
	}
	if stdin == nil {
		return "", fmt.Errorf("%s - has no stdin to read", contextFlag)
	}
	*usedStdin = true
	read, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read %s from stdin: %w", contextFlag, err)
	}
	return string(read), nil
}

// appendNote keeps only notes with something in them, so a stray empty
// --context does not become a blank line in the prompt.
func appendNote(notes []string, note string) []string {
	if note = strings.TrimSpace(note); note == "" {
		return notes
	}
	return append(notes, note)
}

// Notes turns --context values into what the agent takes. They carry no
// turn number: unlike recorded history, background the caller handed over
// deliberately is not the router's to select from or drop.
func Notes(notes []string) []agent.Recollection {
	recalled := make([]agent.Recollection, 0, len(notes))
	for _, note := range notes {
		recalled = append(recalled, agent.Recollection{Message: note, Note: true})
	}
	return recalled
}
