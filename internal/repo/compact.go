package repo

import (
	"fmt"
	"strings"
)

// The compact patch format. A model writing a unified diff has to get
// three things right that have nothing to do with the edit — the "@@"
// header, the line numbers in it, and the counts beside them — and gets
// them wrong often enough that placing hunks by content exists at all.
// This format asks for none of them:
//
//	@path            modify
//	@+path           create; every following line is literal content
//	@-path           delete  (refused, see below)
//	@>path           move    (refused, see below)
//
//	 context
//	-removed
//	+added
//
// A blank line separates one edit from the next, and that is the whole
// specification. What is left is exactly the shape hunks already have
// here: the " " and "-" lines are a hunk's before, the " " and "+" lines
// are its after, and locate places it by matching text.
//
// Delete and move are parsed only so they can be refused by name rather
// than silently misread as a modification. Removing a file is the highest
// blast radius an edit has, and a move that arrives alongside edits to the
// moved file can lose it outright; neither has been needed.

// isCompactPatch reports whether patch is in the compact format, decided
// by its first line that says anything at all.
func isCompactPatch(patch string) bool {
	for _, line := range strings.Split(patch, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		return compactHeader(line) != nil
	}
	return false
}

// compactHeader reads a file header, or nil if the line is not one.
type compactHeader_ struct {
	mode rune
	path string
}

func compactHeader(line string) *compactHeader_ {
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, "@") {
		return nil
	}
	rest := line[1:]
	mode := ' '
	if rest != "" && (rest[0] == '+' || rest[0] == '-' || rest[0] == '>') {
		mode, rest = rune(rest[0]), rest[1:]
	}
	if !looksLikePath(rest) {
		return nil
	}
	return &compactHeader_{mode: mode, path: rest}
}

// looksLikePath is what keeps a created file's own contents from being
// read as the next file's header. A CSS file full of "@media (...)" and
// "@import url(...)", or a TypeScript file of "@Component({...})", would
// otherwise cut the file short at its first at-rule and start writing a
// second file named after it. Real paths carry no spaces or brackets, and
// the at-rules and decorators that matter all do.
//
// It is a heuristic, not a proof: a bare "@Component" on its own line
// inside a created file would still be mistaken for a header. Anything
// with a space, a bracket, a quote or a semicolon is safe, which covers
// every form seen in practice.
func looksLikePath(text string) bool {
	if text == "" || len(text) > 200 || strings.HasPrefix(text, "@") {
		return false
	}
	return !strings.ContainsAny(text, " \t(){}[]\"';,")
}

// parseCompact turns a compact patch into the same per-file hunks the
// unified-diff parser produces, so everything downstream — locate, the
// three-tier matching, nearMiss, PatchError — is untouched by which
// format the model chose to write in.
func parseCompact(patch string) ([]filePatch, error) {
	var patches []filePatch
	var current *filePatch
	var active *hunk

	// flush closes the edit being read. An empty one is dropped rather
	// than kept: a blank line after a header, or two in a row, is not an
	// edit that changes nothing, it is just spacing.
	flush := func() {
		if active != nil && (len(active.before) > 0 || len(active.after) > 0) {
			current.hunks = append(current.hunks, *active)
		}
		active = nil
	}

	lines := strings.Split(strings.TrimRight(patch, "\n"), "\n")
	for index := 0; index < len(lines); index++ {
		line := strings.TrimRight(lines[index], "\r")

		if header := compactHeader(line); header != nil {
			if current != nil {
				flush()
			}
			switch header.mode {
			case '-':
				return nil, fmt.Errorf("the patch deletes %s, which YoloCoder does not do", header.path)
			case '>':
				return nil, fmt.Errorf("the patch moves %s, which YoloCoder does not do", header.path)
			}
			patches = append(patches, filePatch{path: header.path})
			current = &patches[len(patches)-1]

			if header.mode == '+' {
				// A created file's lines are its literal contents, with
				// no prefix of their own. Prefixing them would make a
				// file that genuinely begins with "+", "-" or a space —
				// a Markdown list, a README quoting a diff, indented
				// YAML — impossible to write down unambiguously.
				//
				// Blank lines are content here, not separators: there is
				// only ever one edit per created file, so there is
				// nothing for a blank line to separate.
				var content []string
				for index+1 < len(lines) && compactHeader(lines[index+1]) == nil {
					index++
					content = append(content, strings.TrimRight(lines[index], "\r"))
				}
				current.hunks = append(current.hunks, hunk{after: content})
			}
			continue
		}

		if current == nil {
			if strings.TrimSpace(line) == "" {
				continue
			}
			return nil, fmt.Errorf("an edit appears before any file header: %q", clipLine(line))
		}

		if strings.TrimSpace(line) == "" {
			// A blank line always separates, even though it could also be
			// a context line whose single leading space the model dropped
			// — which they do constantly. Splitting is the safe way to be
			// wrong: a hunk that loses a line of context still places, and
			// locate says so if what remains is ambiguous. Merging two
			// edits that should have been separate produces one block
			// that matches nothing anywhere in the file.
			flush()
			continue
		}

		if active == nil {
			active = &hunk{}
		}
		switch line[0] {
		case ' ':
			active.before = append(active.before, line[1:])
			active.after = append(active.after, line[1:])
		case '-':
			active.before = append(active.before, line[1:])
		case '+':
			active.after = append(active.after, line[1:])
		default:
			// An unprefixed line is a context line that lost its leading
			// space. Taking it as context is the reading that can still
			// succeed: the whitespace-insensitive pass in locate matches
			// it either way, where refusing the patch outright costs a
			// whole round trip over one missing character.
			active.before = append(active.before, line)
			active.after = append(active.after, line)
		}
	}
	if current != nil {
		flush()
	}
	if len(patches) == 0 {
		return nil, fmt.Errorf("no file headers in patch")
	}
	return patches, nil
}

// PatchPaths are the files a patch touches, in the order it names them.
// Exported so a caller can report what an edit changed without parsing
// the patch a second time itself, and so it can say which files a failed
// one was aiming at.
func PatchPaths(patch string) []string {
	patches, err := parsePatch(patch)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var paths []string
	for _, file := range patches {
		if file.path != "" && !seen[file.path] {
			seen[file.path] = true
			paths = append(paths, file.path)
		}
	}
	return paths
}
