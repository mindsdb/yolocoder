package agent

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mindsdb/yolocoder/internal/repo"
)

type replacementArguments struct {
	Edits   []anchoredReplacement `json:"edits"`
	Comment string                `json:"response_comment_for_user"`
}

type anchoredReplacement struct {
	repo.Replacement
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
}

func (session *changeSession) instructions() string {
	switch {
	case session.prefetched:
		return smallEditInstructions
	case session.whole:
		return wholeProjectChangeInstructions
	default:
		return changeInstructions
	}
}

// wholeProjectChangeInstructions are changeInstructions for a project
// handed over whole and in memory (--web-fe): nothing is left to read,
// and a file being rewritten is written out in full rather than as a
// diff of every one of its lines. The two sentences that say otherwise
// are replaced rather than contradicted further down: with both in the
// prompt, the model split the difference and wrote a whole file as "+"
// lines under a plain @path, which places nowhere.
var wholeProjectChangeInstructions = strings.Replace(strings.Replace(changeInstructions,
	"Use @+ only for a new file, with literal content, not added-line prefixes.\n"+
		"To replace an existing file, remove its current contents with - lines before adding the new\n"+
		"contents with + lines.",
	"Use @+path, with literal content and no added-line prefixes, both for a new file and to\n"+
		"replace an existing file whole: whenever you are rewriting most of a file (building the app\n"+
		"from a starter, for one), write the complete new file under @+path. Never write a whole file as\n"+
		"+ lines under a plain @path. For a small change, use @path with only the lines that change.", 1),
	"THE PATCH FORMAT", `THIS PROJECT

Every file in the project is already above, complete and current. There is nothing else to read or
search for: go straight to the edit. It runs in a browser preview, not on this machine.

THE PATCH FORMAT`, 1)

const smallEditInstructions = `Implement the user's small UI edit in this folder. Preserve unrelated content and behaviour.
The initial read_files call already read the selected source and project context; use those exact contents.
Treat background app specifications as behaviour to preserve. Restrict modifications to the requested values;
keep existing logic, layout rules and markup intact. Make the smallest complete edit.

For literal text or style changes, use replace_text. Batch all requested substitutions into one call.
old_text and new_text are only the short values being changed, at most 256 characters each.
Put any immediately surrounding matching context in prefix and suffix (128 characters each).
The tool matches prefix + old_text + suffix exactly once and preserves prefix/suffix automatically.
Use empty anchors when the old value is already unique. Do not copy whole lines or functions.
Example: prefix="<span>", old_text="Before", new_text="After", suffix="</span>".
Example: prefix="border-color: ", old_text="teal", new_text="navy", suffix=";".
Use read_files or search only if necessary context is actually missing. Never edit a file you have not read.

FINISHING THE CHANGE
When your edit implements the complete request, put a short, factual closing note in response_comment_for_user
on that edit call. The application automatically runs the project's existing check after applying the edit.
It delivers your note only when the check passes; failures return here for repair. Do not leave the note empty
just to request a test or send a separate "done" reply. Do not claim tests passed before they run.
Leave the note empty only when there is more implementation work to do. If you cannot complete the task,
say what remains without claiming success. Do not reread an applied edit merely to verify it.

For structural edits, apply_diff is also available. Its patch uses @path followed by full -old and +new lines.
Optional context lines start with a space. Separate hunks with a bare @@; no line numbers or counts.
Copy every removed/context line exactly and completely, preserving whitespace and escapes.
Create files with @+path followed by their full literal content. Include a closing note on the final edit.
All edits share one bounded budget. Correct rejected edits using the returned error and current contents.
Use recall, when offered, only for earlier context not already in the conversation.
`

func (session *changeSession) tools() []functionTool {
	tools := repositoryTools(session.runner.recall)
	if session.whole {
		// Offered anyway, read_files and search were only ever a way to
		// spend a round trip re-reading what is already in the transcript.
		kept := tools[:0]
		for _, tool := range tools {
			if tool.Name != "read_files" && tool.Name != "search" {
				kept = append(kept, tool)
			}
		}
		tools = kept
	}
	if !session.prefetched {
		for i := range tools {
			if tools[i].Name == "read_files" {
				tools[i].Description = "Read current repository files needed to resolve a concrete uncertainty in the user's task. Reuse source already supplied by read_files; batch needed unread or changed files. Use matching for a contents pattern alongside known paths; leave it empty when all paths are known."
			}
			if tools[i].Name == "apply_diff" {
				tools[i].Description = "Apply a compact patch. Nothing is written unless every edit can be placed. Set task_complete=true only when this edit completes the entire request and is your final tool call; the project check still runs. Set false while work remains. response_comment_for_user may be empty and does not determine completion."
				properties := tools[i].Parameters["properties"].(map[string]any)
				properties["task_complete"] = map[string]any{"type": "boolean"}
				tools[i].Parameters["required"] = []string{"patch", "response_comment_for_user", "task_complete"}
			}
		}
	}
	if session.prefetched {
		tools = append(tools, functionTool{Type: "function", Name: "replace_text", Strict: true,
			Description: "Change short literal values in files already read. The exact combination prefix + old_text + suffix must be unique. Only old_text is replaced; anchors are preserved automatically. Use empty anchors when unnecessary. All substitutions are checked before writes. Include a closing note on the final edit; project checks still run.",
			Parameters: objectSchema(map[string]any{
				"edits": map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "items": objectSchema(map[string]any{
					"path":     map[string]any{"type": "string"},
					"old_text": map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "Only the literal value being changed; put unchanged context in prefix/suffix."},
					"new_text": map[string]any{"type": "string", "maxLength": 256, "description": "Only the new literal value, without the prefix/suffix anchors."},
					"prefix":   map[string]any{"type": "string", "maxLength": 128, "description": "Exact text immediately before old_text, preserved automatically. Empty if unnecessary."},
					"suffix":   map[string]any{"type": "string", "maxLength": 128, "description": "Exact text immediately after old_text, preserved automatically. Empty if unnecessary."},
				}, []string{"path", "old_text", "new_text", "prefix", "suffix"})},
				"response_comment_for_user": map[string]any{"type": "string"},
			}, []string{"edits", "response_comment_for_user"})})
	}
	return tools
}

func (session *changeSession) replaceText(call responseItem) (string, []string) {
	var args replacementArguments
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return "ERROR: invalid replacement arguments: " + err.Error(), nil
	}
	edits := make([]repo.Replacement, 0, len(args.Edits))
	for _, edit := range args.Edits {
		if edit.Old == "" || utf8.RuneCountInString(edit.Old) > 256 || utf8.RuneCountInString(edit.New) > 256 || utf8.RuneCountInString(edit.Prefix) > 128 || utf8.RuneCountInString(edit.Suffix) > 128 {
			return "ERROR: use a nonempty old value and old/new values of at most 256 characters, with prefix/suffix anchors at most 128 characters each. Use apply_diff for larger edits. Nothing was changed.", nil
		}
		if !session.runner.alreadyShown(edit.Path) {
			return "ERROR: read " + edit.Path + " first; it is unread or changed since the last read. Nothing was changed.", nil
		}
		session.attempted = appendUnique(session.attempted, edit.Path)
		edits = append(edits, repo.Replacement{Path: edit.Path, Old: edit.Prefix + edit.Old + edit.Suffix, New: edit.Prefix + edit.New + edit.Suffix})
	}
	started := time.Now()
	paths, err := session.runner.repository.ReplaceText(edits)
	session.runner.profile.record(StepPatch, time.Since(started))
	if err != nil {
		session.lastFailure = err
		return "The replacements failed: " + err.Error() + ". Use prefix/suffix for unchanged matching context; keep old_text/new_text as short values.", []string{err.Error()}
	}
	return session.appliedEdit(paths, args.Comment), nil
}

func (session *changeSession) appliedEdit(paths []string, comment string) string {
	session.lastFailure = nil
	for _, path := range paths {
		session.applied = appendUnique(session.applied, path)
	}
	session.closing = strings.TrimSpace(comment)
	return "Applied. Changed: " + strings.Join(paths, ", ") +
		". Every edit in that patch was placed; the files contain them now. Do not read them again to check."
}

func replacementPaths(call responseItem) []string {
	var args replacementArguments
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return nil
	}
	var paths []string
	for _, edit := range args.Edits {
		paths = appendUnique(paths, edit.Path)
	}
	return paths
}
