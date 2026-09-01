// Package session records what was asked of YoloCoder in a folder and
// what came of it, so a later run can be told what has been going on.
//
// It deliberately records intent and outcome, never file contents. The
// files are on disk and the agent reads them itself; a snapshot kept here
// would go stale and invite the model to patch against a version that no
// longer exists.
package session

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// These bound how much history a later run is offered. Whichever binds
// first wins. There is deliberately no attempt to measure against the
// model's context window: providers do not report it reliably and we have
// no tokenizer, so a fixed budget is honest where a computed one would be
// fiction.
const (
	MaxTurns = 20
	MaxBytes = 8 << 10
	MaxAge   = 48 * time.Hour
)

// Turn is one exchange: what was asked, and what happened.
type Turn struct {
	// Number is the turn's position in its session, counted from one and
	// never reassigned. Renumbering a window would change the prefix of
	// every later request and cost the provider's prompt cache.
	Number  int       `json:"number"`
	At      time.Time `json:"at"`
	Message string    `json:"message"`
	// Kind is "code" or "chat".
	Kind    string   `json:"kind"`
	Summary string   `json:"summary,omitempty"`
	Files   []string `json:"files,omitempty"`
	Applied bool     `json:"applied,omitempty"`
}

// header is the first line of a session file.
type header struct {
	Type     string    `json:"type"`
	Folder   string    `json:"folder"`
	Terminal string    `json:"terminal,omitempty"`
	Started  time.Time `json:"started"`
}

type record struct {
	Type string `json:"type"`
	Turn
}

// Log appends turns to one session's file.
type Log struct {
	path   string
	folder string
	turns  int
}

// Open starts a session for folder, or continues the most recent one that
// belongs to the same folder and terminal. History is scoped to the folder
// so that one project's context can never bleed into another's; the
// terminal only breaks ties between sessions in the same folder, since tty
// paths and process ids are recycled and make poor identities.
func Open(folder, terminal string) (*Log, error) {
	directory, err := folderDirectory(folder)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	if existing, err := resume(directory, terminal); err == nil && existing != nil {
		return existing, nil
	}
	// A suffix as well as the timestamp: two shells opening a folder in
	// the same second would otherwise pick the same name and one would
	// append into the other's thread.
	path := filepath.Join(directory, time.Now().UTC().Format("20060102T150405Z")+"-"+unique()+".jsonl")
	log := &Log{path: path, folder: folder}
	// 0600 throughout: prompts are the user's own words and can be
	// sensitive.
	if err := log.append(header{Type: "session", Folder: folder, Terminal: terminal, Started: time.Now().UTC()}); err != nil {
		return nil, err
	}
	return log, nil
}

// resume finds the newest session for this folder started by the same
// terminal, so two shells in one project keep their own threads.
func resume(directory, terminal string) (*Log, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(first, second int) bool { return entries[first].Name() > entries[second].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		head, turns, err := read(path)
		if err != nil {
			continue
		}
		if time.Since(head.Started) > MaxAge {
			return nil, nil
		}
		if terminal != "" && head.Terminal == terminal {
			return &Log{path: path, folder: head.Folder, turns: highest(turns)}, nil
		}
	}
	return nil, nil
}

// Append records one turn.
func (log *Log) Append(turn Turn) error {
	log.turns++
	turn.Number = log.turns
	if turn.At.IsZero() {
		turn.At = time.Now().UTC()
	}
	turn.Message = strings.TrimSpace(turn.Message)
	return log.append(record{Type: "turn", Turn: turn})
}

// Path is the file this session is written to.
func (log *Log) Path() string { return log.path }

func (log *Log) append(value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(log.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open session log: %w", err)
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}

// Recent returns the turns a later run should be offered for this folder,
// oldest first, within the age, count and size caps.
func Recent(folder string) ([]Turn, error) {
	directory, err := folderDirectory(folder)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(first, second int) bool { return entries[first].Name() > entries[second].Name() })

	var collected []Turn
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		_, turns, err := read(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		// Newest file first, so prepend to keep oldest-first order.
		collected = append(turns, collected...)
		if len(collected) >= MaxTurns {
			break
		}
	}
	return within(collected), nil
}

// within trims to the caps, dropping the oldest first.
func within(turns []Turn) []Turn {
	fresh := turns[:0]
	for _, turn := range turns {
		if time.Since(turn.At) <= MaxAge {
			fresh = append(fresh, turn)
		}
	}
	if len(fresh) > MaxTurns {
		fresh = fresh[len(fresh)-MaxTurns:]
	}
	// Trim from the front until the whole window fits the byte budget.
	for len(fresh) > 0 && size(fresh) > MaxBytes {
		fresh = fresh[1:]
	}
	return fresh
}

func size(turns []Turn) int {
	total := 0
	for _, turn := range turns {
		total += len(turn.Message) + len(turn.Summary)
		for _, file := range turn.Files {
			total += len(file)
		}
	}
	return total
}

func read(path string) (header, []Turn, error) {
	file, err := os.Open(path)
	if err != nil {
		return header{}, nil, err
	}
	defer file.Close()

	var head header
	var turns []Turn
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &kind) != nil {
			continue // a half-written line from a crash is not fatal
		}
		switch kind.Type {
		case "session":
			_ = json.Unmarshal(line, &head)
		case "turn":
			var entry record
			if json.Unmarshal(line, &entry) == nil {
				turns = append(turns, entry.Turn)
			}
		}
	}
	return head, turns, scanner.Err()
}

func highest(turns []Turn) int {
	highest := 0
	for _, turn := range turns {
		if turn.Number > highest {
			highest = turn.Number
		}
	}
	return highest
}

// unique is a short random tag that keeps two sessions started in the
// same second from sharing a filename.
func unique() string {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return strconv.Itoa(os.Getpid())
	}
	return hex.EncodeToString(buffer)
}

// folderDirectory is where a folder's sessions live. The path is hashed
// rather than escaped so that separators, spaces and unicode in project
// paths cannot produce a surprising filename.
func folderDirectory(folder string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(folder)))
	return filepath.Join(home, ".config", "yolocoder", "sessions", hex.EncodeToString(sum[:6])), nil
}

// Terminal identifies the terminal a session is running in, well enough
// to tell two shells apart in one folder. It is only ever a tie-break:
// these values are recycled, so they are never treated as an identity.
func Terminal(getenv func(string) string) string {
	for _, name := range []string{"TERM_SESSION_ID", "WINDOWID", "TMUX_PANE", "STY"} {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			return value
		}
	}
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		return fmt.Sprintf("ppid-%d", os.Getppid())
	}
	return ""
}
