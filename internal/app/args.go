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

// allowCommandsFlag runs a generated command without asking first.
const allowCommandsFlag = "--allow-commands"

// Flags are the options a run was started with.
type Flags struct {
	// Notes is what --context carried, in the order it was given.
	Notes []string
	// AllowCommands skips the confirmation before a generated command
	// runs. It is for scripted runs, where there is nobody to ask.
	AllowCommands bool
}

// ParseFlags pulls the options out of args and returns what is left, in
// the order it was given, to be joined back into the task.
//
// --context accepts "--context text" and "--context=text", repeats to
// accumulate, and takes "-" to read the note from stdin so a caller can
// pipe a file in without worrying about quoting. "--" ends flag parsing,
// for the benefit of a task that begins with a dash.
func ParseFlags(args []string, stdin io.Reader) (flags Flags, rest []string, err error) {
	usedStdin := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--":
			return flags, append(rest, args[index+1:]...), nil
		case argument == allowCommandsFlag:
			flags.AllowCommands = true
		case argument == contextFlag:
			// The value is the next argument, which must exist. Silently
			// treating a trailing --context as empty would quietly drop
			// context the caller believed they had passed.
			if index+1 >= len(args) {
				return Flags{}, nil, fmt.Errorf("%s needs a value", contextFlag)
			}
			index++
			note, err := contextValue(args[index], stdin, &usedStdin)
			if err != nil {
				return Flags{}, nil, err
			}
			flags.Notes = appendNote(flags.Notes, note)
		case strings.HasPrefix(argument, contextFlag+"="):
			note, err := contextValue(strings.TrimPrefix(argument, contextFlag+"="), stdin, &usedStdin)
			if err != nil {
				return Flags{}, nil, err
			}
			flags.Notes = appendNote(flags.Notes, note)
		default:
			rest = append(rest, argument)
		}
	}
	return flags, rest, nil
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
