package shell

import (
	"path/filepath"
	"strings"
)

// Review is what reading a script concluded about it.
type Review struct {
	// Confined is true only when every command in the script was
	// recognised as one that reads, and every path it names stays inside
	// the folder. It is the answer to "can this run without asking?".
	Confined bool
	// Reason says what stopped it being confined, for a caller that wants
	// to explain why it is asking.
	Reason string
}

// Inspect decides whether a script is safe enough to run without asking.
//
// It works by recognising safety rather than by spotting danger, and that
// direction is the whole design. A list of dangerous things is a list of
// the dangerous things somebody thought of: everything omitted, every new
// tool, every spelling nobody predicted, sails through. A list of things
// known to only read has the failure the other way round — the worst it
// can do is ask about something harmless, which costs a keystroke.
//
// So anything not positively recognised is not confined. That includes
// scripts this is simply not clever enough to read: shell substitution,
// redirection, escapes, backgrounding. A real shell parser here would be
// a liability, because being almost right about what a script does is
// exactly the way to wave through the one that matters.
func Inspect(script, folder string) Review {
	text := strings.TrimSpace(normalise(script))
	if text == "" {
		return Review{Reason: "there is nothing to run"}
	}
	if character := strings.IndexAny(text, unreadable); character >= 0 {
		return Review{Reason: "it uses shell syntax (" + string(text[character]) + ") this cannot read safely"}
	}
	for _, segment := range split(text) {
		if segment = strings.TrimSpace(segment); segment == "" {
			continue
		}
		if strings.Contains(segment, "&") {
			return Review{Reason: "it puts something in the background"}
		}
		tokens, ok := tokenise(segment)
		if !ok {
			return Review{Reason: "it has an unclosed quote"}
		}
		if review := inspectCommand(tokens, folder); !review.Confined {
			return review
		}
	}
	return Review{Confined: true}
}

// unreadable are the characters that mean a script does something this
// cannot follow: substitute a command, redirect a stream, escape a
// character, or group commands. Their presence is not evidence of harm —
// it is the absence of evidence of safety, which is the same answer here.
const unreadable = "$`><\\(){}[]!#"

// inspectCommand checks one command: what it runs, and what it names.
func inspectCommand(tokens []string, folder string) Review {
	if len(tokens) == 0 {
		return Review{Confined: true}
	}
	head := tokens[0]
	// A leading VAR=value is an assignment, and what follows it is a
	// different command than it appears to be.
	if strings.Contains(head, "=") {
		return Review{Reason: "it sets an environment variable"}
	}
	// Only the bare name, so a path to a lookalike binary is not taken
	// for the real thing.
	if strings.ContainsAny(head, "/") {
		return Review{Reason: "it runs a program by path (" + head + ")"}
	}
	arguments := tokens[1:]
	if !readOnly(head, arguments) {
		return Review{Reason: "\"" + strings.Join(tokens, " ") + "\" is not a command known to only read"}
	}
	for _, token := range arguments {
		if strings.HasPrefix(token, "-") {
			if vetoedFlags[token] || vetoedFor[head][token] {
				return Review{Reason: "the " + token + " option can write or run something"}
			}
			continue
		}
		if looksLikePath(token) && !inside(folder, token) {
			return Review{Reason: token + " is outside " + folder}
		}
	}
	return Review{Confined: true}
}

// readOnly reports whether this program, with these arguments, only
// reads. Some programs are read-only whatever you pass them; others are
// a whole toolbox behind one name and are judged by their subcommand.
func readOnly(head string, arguments []string) bool {
	if readers[head] {
		return true
	}
	if allowed, ok := subcommands[head]; ok {
		return allowed[firstWord(arguments)]
	}
	// A program asked only for its version is running its own reporting
	// path, whatever else it is otherwise capable of.
	return len(arguments) == 1 && versionFlags[arguments[0]]
}

// firstWord is the first argument that is not an option, which is where
// a subcommand lives.
func firstWord(arguments []string) string {
	for _, argument := range arguments {
		if !strings.HasPrefix(argument, "-") {
			return argument
		}
	}
	return ""
}

// readers only ever read. Deliberately short: a name earns its place by
// being one nobody has to think twice about, and the cost of leaving one
// out is a question, not an accident. Notably absent are find (-delete,
// -exec), xargs, env, sed and awk, each of which is a way to run
// something else under a harmless-looking name.
var readers = map[string]bool{
	"ls": true, "pwd": true, "cat": true, "head": true, "tail": true,
	"wc": true, "echo": true, "printf": true, "date": true, "cal": true,
	"whoami": true, "hostname": true, "uname": true, "id": true, "groups": true,
	"which": true, "type": true, "file": true, "stat": true,
	"basename": true, "dirname": true, "realpath": true, "readlink": true,
	"tree": true, "du": true, "df": true, "ps": true, "uptime": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
	"diff": true, "cmp": true, "sort": true, "uniq": true, "cut": true,
	"nl": true, "tr": true, "column": true, "jq": true, "true": true, "false": true,
}

// subcommands are the programs that are a toolbox behind one name, with
// the subcommands of each that only read.
var subcommands = map[string]map[string]bool{
	"git": {
		"status": true, "log": true, "diff": true, "show": true, "branch": true,
		"remote": true, "tag": true, "describe": true, "rev-parse": true,
		"ls-files": true, "ls-remote": true, "blame": true, "shortlog": true,
		"whatchanged": true, "cat-file": true, "grep": true, "version": true,
	},
	"npm":     {"ls": true, "list": true, "view": true, "outdated": true, "root": true, "prefix": true, "why": true},
	"yarn":    {"list": true, "why": true, "info": true},
	"pnpm":    {"list": true, "ls": true, "why": true, "root": true},
	"pip":     {"list": true, "show": true, "freeze": true},
	"pip3":    {"list": true, "show": true, "freeze": true},
	"go":      {"version": true, "env": true, "list": true, "vet": true, "doc": true},
	"cargo":   {"tree": true, "search": true},
	"docker":  {"ps": true, "images": true, "version": true, "info": true, "logs": true},
	"kubectl": {"get": true, "describe": true, "logs": true, "version": true},
	"brew":    {"list": true, "info": true, "search": true, "config": true, "--version": true},
}

// versionFlags are the ways a program is asked what it is, which is a
// read however capable the program otherwise is.
var versionFlags = map[string]bool{
	"--version": true, "-version": true, "-V": true, "--help": true, "-h": true, "help": true,
}

// vetoedFlags never belong on a command that only reads, whatever the
// program. The exec family is the important one: it is how a harmless
// name is made to run something else.
var vetoedFlags = map[string]bool{
	"-delete": true, "-exec": true, "-execdir": true, "-ok": true,
	"-okdir": true, "--exec": true, "--in-place": true, "--write": true,
	"--fix": true, "--output": true,
}

// vetoedFor are flags that only matter for particular programs. -o
// cannot be vetoed outright: it writes a file for sort, but for grep it
// means print only the match, and asking about every grep -o would make
// the whole thing a nuisance rather than a help.
var vetoedFor = map[string]map[string]bool{
	"sort": {"-o": true},
	"jq":   {"-o": true},
	"tr":   {"-o": true},
}

// looksLikePath is true for a token that names somewhere on disk. A bare
// word that could be either is treated as a path, so it gets checked.
func looksLikePath(token string) bool {
	return token != "" && !strings.Contains(token, "=")
}

// inside reports whether a path stays within folder. It is a check on the
// text, not on the filesystem: the target may not exist yet, and
// following symlinks would answer a question about now rather than about
// what the command is going to do.
func inside(folder, token string) bool {
	if strings.HasPrefix(token, "~") {
		// The shell expands this to somewhere else entirely.
		return false
	}
	path := token
	if !filepath.IsAbs(path) {
		path = filepath.Join(folder, path)
	}
	path = filepath.Clean(path)
	folder = filepath.Clean(folder)
	return path == folder || strings.HasPrefix(path, folder+string(filepath.Separator))
}

// split breaks a script into the separate commands it runs.
func split(text string) []string {
	replacer := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	return strings.Split(replacer.Replace(text), "\n")
}

// tokenise splits one command on whitespace, keeping quoted runs whole.
// Escapes are not handled because a backslash has already disqualified
// the script; anything this cannot read plainly never reaches here.
func tokenise(segment string) ([]string, bool) {
	var tokens []string
	var current strings.Builder
	quote := rune(0)
	written := false

	for _, character := range segment {
		switch {
		case quote != 0:
			if character == quote {
				quote = 0
				continue
			}
			current.WriteRune(character)
		case character == '\'' || character == '"':
			quote = character
			written = true
		case character == ' ' || character == '\t':
			if current.Len() > 0 || written {
				tokens = append(tokens, current.String())
				current.Reset()
				written = false
			}
		default:
			current.WriteRune(character)
		}
	}
	if quote != 0 {
		return nil, false
	}
	if current.Len() > 0 || written {
		tokens = append(tokens, current.String())
	}
	return tokens, true
}
