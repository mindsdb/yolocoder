package repo

import (
	"fmt"
	"strings"
)

// A model writing a unified diff reliably gets the content right and the
// bookkeeping wrong: hunk headers that miscount lines, start lines that
// are off, and hunks with no trailing context (which git rejects outright,
// since a hunk that ends at its last change implies the file ends there).
//
// None of that needs the model's help. The file is right here, so this
// applies hunks by locating their content, ignoring every line number and
// count in the patch.

type hunk struct {
	before []string // context and removed lines, in order
	after  []string // context and added lines, in order
}

type filePatch struct {
	path  string
	hunks []hunk
}

// parsePatch pulls the per-file hunks out of a unified diff, keeping only
// what is needed to locate and replace content.
func parsePatch(patch string) ([]filePatch, error) {
	var patches []filePatch
	var current *filePatch
	var active *hunk

	// Trailing blank lines are an artifact of the patch text ending in a
	// newline, not empty context lines, and counting them as context would
	// make every last hunk unmatchable.
	for _, line := range strings.Split(strings.TrimRight(patch, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "), strings.HasPrefix(line, "--- "):
			active = nil
		case strings.HasPrefix(line, "+++ "):
			active = nil
			path := patchPath(strings.TrimPrefix(line, "+++ "))
			if path == "" {
				current = nil
				continue
			}
			patches = append(patches, filePatch{path: path})
			current = &patches[len(patches)-1]

		// Models trained on OpenAI's apply_patch format reach for it
		// instead of a unified diff. It carries no line numbers at all,
		// which suits placing hunks by content exactly, so the only thing
		// missing was recognizing its headers.
		case strings.HasPrefix(line, "*** Begin Patch"), strings.HasPrefix(line, "*** End Patch"):
			active = nil
		case strings.HasPrefix(line, "*** Update File:"), strings.HasPrefix(line, "*** Add File:"):
			active = nil
			_, raw, _ := strings.Cut(line, ":")
			path := patchPath(raw)
			if path == "" {
				current = nil
				continue
			}
			patches = append(patches, filePatch{path: path})
			current = &patches[len(patches)-1]
		case strings.HasPrefix(line, "*** Delete File:"):
			_, raw, _ := strings.Cut(line, ":")
			return nil, fmt.Errorf("the patch deletes %s, which YoloCoder does not do", strings.TrimSpace(raw))

		case strings.HasPrefix(line, "@@"):
			if current == nil {
				return nil, fmt.Errorf("hunk before any file header")
			}
			current.hunks = append(current.hunks, hunk{})
			active = &current.hunks[len(current.hunks)-1]
		case active == nil:
			// Preamble, index lines, or trailing noise.
		case strings.HasPrefix(line, "-"):
			active.before = append(active.before, line[1:])
		case strings.HasPrefix(line, "+"):
			active.after = append(active.after, line[1:])
		case strings.HasPrefix(line, " "):
			active.before = append(active.before, line[1:])
			active.after = append(active.after, line[1:])
		case line == "":
			// An empty line inside a hunk is an empty context line, but a
			// trailing blank at the end of the patch is not. Treat it as
			// context only while the hunk is still collecting.
			active.before = append(active.before, "")
			active.after = append(active.after, "")
		case strings.HasPrefix(line, `\`):
			// "\ No newline at end of file"
		default:
			active = nil
		}
	}
	if len(patches) == 0 {
		return nil, fmt.Errorf("no file headers in patch")
	}
	return patches, nil
}

// isApplyPatchFormat reports whether a patch uses OpenAI's apply_patch
// headers. Git cannot read that format at all, so there is no point
// handing it over first and no failure to report when it declines.
func isApplyPatchFormat(patch string) bool {
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "*** Begin Patch"),
			strings.HasPrefix(line, "*** Update File:"),
			strings.HasPrefix(line, "*** Add File:"),
			strings.HasPrefix(line, "*** Delete File:"):
			return true
		case strings.HasPrefix(line, "diff --git "), strings.HasPrefix(line, "--- "):
			return false
		}
	}
	return false
}

// patchPath turns a diff header path into a repository-relative one.
func patchPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if tab := strings.IndexByte(raw, '\t'); tab != -1 {
		raw = raw[:tab]
	}
	if raw == "/dev/null" {
		return ""
	}
	for _, prefix := range []string{"a/", "b/"} {
		if strings.HasPrefix(raw, prefix) {
			return raw[len(prefix):]
		}
	}
	return raw
}

// applyByContent applies patch by finding each hunk's content in the file
// rather than trusting its line numbers. It reports an error rather than
// guessing whenever a hunk can't be placed unambiguously.
func (repository *Repository) applyByContent(patch string) error {
	patches, err := parsePatch(patch)
	if err != nil {
		return err
	}
	// Work out every file's new contents before writing anything, so a
	// failure on the second file doesn't leave the first one changed.
	updated := make(map[string]string, len(patches))
	for _, file := range patches {
		if len(file.hunks) == 0 {
			continue
		}
		// A patch may touch the same file more than once — models happily
		// emit several "*** Begin Patch" blocks for one file. Each block
		// has to build on the last, or the final one silently discards
		// every change before it.
		current, carried := updated[file.path]
		if !carried {
			read, err := repository.ReadFile(file.path)
			if err != nil {
				return err
			}
			current = read
		}
		content, err := applyHunks(current, file.hunks)
		if err != nil {
			return fmt.Errorf("%s: %w", file.path, err)
		}
		updated[file.path] = content
	}
	if len(updated) == 0 {
		return fmt.Errorf("patch changed nothing")
	}
	for path, content := range updated {
		if err := repository.Write(path, content); err != nil {
			return err
		}
	}
	return nil
}

func applyHunks(content string, hunks []hunk) (string, error) {
	lines := strings.Split(content, "\n")
	for _, current := range hunks {
		if len(current.before) == 0 {
			// A pure insertion with no context could go anywhere.
			if strings.TrimSpace(content) == "" {
				lines = current.after
				continue
			}
			return "", fmt.Errorf("a hunk has no context to place it by")
		}
		index, err := locate(lines, current.before)
		if err != nil {
			return "", err
		}
		replaced := make([]string, 0, len(lines)-len(current.before)+len(current.after))
		replaced = append(replaced, lines[:index]...)
		replaced = append(replaced, current.after...)
		replaced = append(replaced, lines[index+len(current.before):]...)
		lines = replaced
	}
	return strings.Join(lines, "\n"), nil
}

// locate finds the one place block occurs in lines. A short block that
// appears more than once is ambiguous, and picking one would risk editing
// the wrong part of the file, so it is refused instead.
func locate(lines, block []string) (int, error) {
	matches := findAll(lines, block, func(a, b string) bool { return a == b })
	if len(matches) == 0 {
		// Fall back to ignoring indentation and line-ending drift, which
		// a model reproducing a file by eye often gets slightly wrong.
		matches = findAll(lines, block, func(a, b string) bool {
			return strings.TrimSpace(a) == strings.TrimSpace(b)
		})
	}
	switch {
	case len(matches) == 0:
		return 0, fmt.Errorf("could not find this hunk's lines in the file:\n%s", preview(block))
	case len(matches) > 1 && len(block) < 3:
		return 0, fmt.Errorf("this hunk's lines appear %d times, too ambiguous to place:\n%s", len(matches), preview(block))
	default:
		return matches[0], nil
	}
}

func findAll(lines, block []string, equal func(string, string) bool) []int {
	var matches []int
	for start := 0; start+len(block) <= len(lines); start++ {
		found := true
		for offset, want := range block {
			if !equal(lines[start+offset], want) {
				found = false
				break
			}
		}
		if found {
			matches = append(matches, start)
		}
	}
	return matches
}

func preview(block []string) string {
	if len(block) > 4 {
		block = block[:4]
	}
	return strings.Join(block, "\n")
}
