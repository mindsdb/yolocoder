package repo

import (
	"errors"
	"fmt"
	"strings"
)

// HunkError is why one hunk could not be placed. It carries the pieces
// apart as well as the full text so the two audiences can be served
// differently: the model gets Detail, which shows it the block it wrote
// and everything known about why it did not fit, while a person watching
// the run gets Summary — the file, the one-line reason, and the single
// line that actually differs. Before this, only the model was told
// anything; the trail said "patch did not apply" and stopped, which is
// the one thing nobody can act on.
type HunkError struct {
	// Path is the file it failed on, filled in by the caller that knows
	// which file's hunks were being placed.
	Path   string
	Reason string
	// Expected and Found are the first line of the closest near miss,
	// empty when nothing in the file resembled the block at all.
	Expected string
	Found    string
	// Extra is anything else worth putting on the progress line — the
	// stray line a hunk carried, where each of its scattered lines sits.
	// Detail always has it too; this is the short form.
	Extra  []string
	Detail string
	// Block is the lines this hunk was looking for, kept so a caller
	// holding the other files in the patch can check whether they are
	// simply in one of those instead.
	Block []string
}

func (failure *HunkError) Error() string {
	if failure.Path != "" {
		return failure.Path + ": " + failure.Detail
	}
	return failure.Detail
}

// Summary is the short form, for a progress line.
func (failure *HunkError) Summary() []string {
	head := failure.Reason
	if failure.Path != "" {
		head = failure.Path + ": " + failure.Reason
	}
	lines := []string{head}
	if failure.Expected != "" {
		lines = append(lines, "expected: "+clipLine(failure.Expected), "in file:  "+clipLine(failure.Found))
	}
	return append(lines, failure.Extra...)
}

// PatchError is every edit in one patch that could not be placed, rather
// than the first one found.
//
// Validating the whole patch before giving up is what makes a repair cost
// one round trip instead of several: a patch with three bad hunks used to
// report one, get a fix for it, and only then reveal the next — three
// model calls to learn what one pass already knew. The trade is that
// hunks apply in sequence, so a hunk that only fails because an earlier
// one did not land is reported approximately; three approximate reports
// still beat one exact one when each costs a round trip.
type PatchError struct {
	Failures []*HunkError
}

// trailLimit is how many failures the progress trail shows before saying
// how many more there are. The model always gets all of them — it is the
// one that has to fix them — but a person watching wants the shape of the
// problem, not forty lines of it.
const trailLimit = 4

func (failure *PatchError) Error() string {
	if len(failure.Failures) == 1 {
		return failure.Failures[0].Error()
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%d edits in this patch could not be placed. Fix every one of them:\n", len(failure.Failures))
	for index, one := range failure.Failures {
		fmt.Fprintf(&text, "\n[%d] %s\n", index+1, one.Error())
	}
	return text.String()
}

// Summary is the short form, for a progress line.
func (failure *PatchError) Summary() []string {
	if len(failure.Failures) == 1 {
		return failure.Failures[0].Summary()
	}
	// Grouped by what went wrong and where. Twelve hunks that all missed
	// the same file produced twelve identical lines, of which the trail
	// showed four and then "... and 8 more" — which says nothing that the
	// count on the first line had not already said.
	lines := []string{fmt.Sprintf("%d edits could not be placed", len(failure.Failures))}
	seen := map[string]int{}
	var order []*HunkError
	for _, one := range failure.Failures {
		key := one.Path + "\x00" + one.Reason
		if seen[key] == 0 {
			order = append(order, one)
		}
		seen[key]++
	}
	for index, one := range order {
		if index == trailLimit {
			lines = append(lines, fmt.Sprintf("... and %d more kind%s", len(order)-trailLimit,
				map[bool]string{true: "", false: "s"}[len(order)-trailLimit == 1]))
			break
		}
		group := one.Summary()
		if count := seen[one.Path+"\x00"+one.Reason]; count > 1 {
			group[0] += fmt.Sprintf("  ×%d", count)
		}
		lines = append(lines, group...)
	}
	return lines
}

// Explain reduces an apply failure to a few lines worth showing someone.
// A hunk that could not be placed knows exactly what went wrong; anything
// else (git refusing the patch outright, an unreadable file) falls back to
// the error's own first line.
func Explain(err error) []string {
	var whole *PatchError
	if errors.As(err, &whole) {
		return whole.Summary()
	}
	var failure *HunkError
	if errors.As(err, &failure) {
		return failure.Summary()
	}
	if err == nil {
		return nil
	}
	first, _, _ := strings.Cut(err.Error(), "\n")
	if first = strings.TrimSpace(first); first == "" {
		return nil
	}
	return []string{clipLine(first)}
}

// clipLine keeps a line inside a terminal's width rather than letting a
// long source line wrap into an unreadable block.
func clipLine(line string) string {
	const limit = 110
	line = strings.TrimRight(line, "\r\n")
	if len(line) <= limit {
		return line
	}
	return line[:limit] + "..."
}

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
	// ops is the same content with its shape kept: which lines were
	// context, which removed, which added. before and after are what
	// placing a hunk needs; ops is what splitting one needs, and a hunk
	// that has to be split is one a model wrote as a single edit when it
	// meant several.
	ops []patchOp
}

// patchOp is one line of a hunk and what it was doing there.
type patchOp struct {
	kind byte // ' ' context, '-' removed, '+' added
	text string
}

// add records a line in both representations at once, so they cannot
// drift apart.
func (current *hunk) add(kind byte, text string) {
	current.ops = append(current.ops, patchOp{kind: kind, text: text})
	if kind != '+' {
		current.before = append(current.before, text)
	}
	if kind != '-' {
		current.after = append(current.after, text)
	}
}

type filePatch struct {
	path  string
	hunks []hunk
}

// parsePatch pulls the per-file hunks out of a unified diff, keeping only
// what is needed to locate and replace content.
func parsePatch(patch string) ([]filePatch, error) {
	// The compact format is its own parser, but it produces the same
	// per-file hunks, so nothing past this line knows which one was used.
	if isCompactPatch(patch) {
		return parseCompact(patch)
	}
	var patches []filePatch
	var current *filePatch
	var active *hunk
	// pendingPath is the file named by a "--- " line, kept in case the
	// "+++ " line that would normally follow and take priority (it's the
	// new-file side of the pair) never comes. Seen in practice: a model
	// approximating the format rather than reproducing it exactly writes
	// only "--- path" and goes straight to "@@", which used to be
	// unrecoverable — nothing else in the patch named the file at all —
	// and cost a full extra round trip to regenerate the whole diff for
	// what the model had actually already said correctly once.
	var pendingPath string

	// Trailing blank lines are an artifact of the patch text ending in a
	// newline, not empty context lines, and counting them as context would
	// make every last hunk unmatchable.
	for _, line := range strings.Split(strings.TrimRight(patch, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			active = nil
			pendingPath = ""
		case strings.HasPrefix(line, "--- "):
			active = nil
			pendingPath = patchPath(strings.TrimPrefix(line, "--- "))
		case strings.HasPrefix(line, "+++ "):
			active = nil
			pendingPath = ""
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
			_, raw, _ := strings.Cut(line, ":")
			path := patchPath(raw)
			if path == "" {
				current = nil
				active = nil
				continue
			}
			patches = append(patches, filePatch{path: path})
			current = &patches[len(patches)-1]
			// "*** Add File:" is followed straight by its "+" lines with
			// no "@@" of its own, so start collecting immediately or the
			// whole file's contents are read as noise and nothing is
			// created. Update File normally does have a "@@"; the empty
			// hunk that leaves behind is dropped below.
			current.hunks = append(current.hunks, hunk{})
			active = &current.hunks[0]
		case strings.HasPrefix(line, "*** Delete File:"):
			_, raw, _ := strings.Cut(line, ":")
			return nil, fmt.Errorf("the patch deletes %s, which YoloCoder does not do", strings.TrimSpace(raw))

		case strings.HasPrefix(line, "@@"):
			if current == nil && pendingPath != "" {
				patches = append(patches, filePatch{path: pendingPath})
				current = &patches[len(patches)-1]
			}
			pendingPath = ""
			if current == nil {
				return nil, fmt.Errorf("hunk before any file header")
			}
			current.hunks = append(current.hunks, hunk{})
			active = &current.hunks[len(current.hunks)-1]
		case active == nil:
			// Preamble, index lines, or trailing noise.
		case strings.HasPrefix(line, "-"):
			active.add('-', line[1:])
		case strings.HasPrefix(line, "+"):
			active.add('+', line[1:])
		case strings.HasPrefix(line, " "):
			active.add(' ', line[1:])
		case line == "":
			// An empty line inside a hunk is an empty context line, but a
			// trailing blank at the end of the patch is not. Treat it as
			// context only while the hunk is still collecting.
			active.add(' ', "")
		case strings.HasPrefix(line, `\`):
			// "\ No newline at end of file"
		default:
			active = nil
		}
	}
	if len(patches) == 0 {
		return nil, fmt.Errorf("no file headers in patch")
	}
	// A hunk that collected nothing carries no instruction, and leaving
	// one in would read as "replace this file with nothing".
	for index := range patches {
		kept := patches[index].hunks[:0]
		for _, current := range patches[index].hunks {
			if len(current.before) > 0 || len(current.after) > 0 {
				kept = append(kept, current)
			}
		}
		patches[index].hunks = kept
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
	// originals is what each file said before, so a patch that places
	// perfectly and changes nothing can be told apart from one that did
	// something. A hunk of pure context lines is exactly that: it matches,
	// it writes the file back byte for byte, and it used to be reported as
	// "Applied" — which spent one of the turn's few edits and told the
	// model its change had landed when nothing had happened at all.
	originals := make(map[string]string, len(patches))
	var failures []*HunkError
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
			originals[file.path] = read
		}
		content, hunkFailures := applyHunks(current, file.hunks)
		for _, failure := range hunkFailures {
			// The file is known here and not where the failure was
			// raised, so it is filled in on the way past.
			failure.Path = file.path
			failures = append(failures, failure)
		}
		// Kept even when some hunks failed, so a second block for the
		// same file is validated against what did place rather than
		// against the original — otherwise one bad hunk cascades into
		// spurious failures for every edit after it.
		updated[file.path] = content
	}
	// Nothing is written until the whole patch has been checked. This is
	// the same code that does the real work rather than a second opinion
	// on it, so a patch that validates here cannot then fail to apply.
	if len(failures) > 0 {
		misrouted(failures, originals)
		return &PatchError{Failures: failures}
	}
	if len(updated) == 0 {
		return fmt.Errorf("patch changed nothing")
	}
	moved := false
	for path, content := range updated {
		if content != originals[path] {
			moved = true
			break
		}
	}
	if !moved {
		return fmt.Errorf("this patch places correctly but changes nothing — every line in it " +
			"is already exactly as written. Check that the lines you meant to change are on " +
			"\"-\" and \"+\" lines rather than context lines")
	}
	for path, content := range updated {
		if err := repository.Write(path, content); err != nil {
			return err
		}
	}
	return nil
}

// applyHunks places every hunk it can and reports every one it cannot.
// A hunk that fails is skipped rather than aborting the pass, so the rest
// are still checked against the content as it stands — which is what lets
// one validation pass find every problem in the patch.
func applyHunks(content string, hunks []hunk) (string, []*HunkError) {
	lines := strings.Split(content, "\n")
	var failures []*HunkError
	for _, current := range hunks {
		if len(current.before) == 0 {
			// A pure insertion with no context could go anywhere.
			if strings.TrimSpace(content) == "" {
				lines = current.after
				continue
			}
			const reason = "a hunk has no context to place it by"
			failures = append(failures, &HunkError{Reason: reason, Detail: reason})
			continue
		}
		index, err := locate(lines, current.before)
		if err != nil {
			// The hunk may be several edits written as one — anchors
			// gathered from all over the file, which is what a model
			// produces when it puts everything in a single patch. That
			// is mechanically recoverable: each part places on its own,
			// and doing it here costs nothing where sending it back
			// costs a round trip and a whole patch regenerated.
			if parts := splitByAnchor(lines, current); parts != nil {
				placed, partFailures := applyHunks(strings.Join(lines, "\n"), parts)
				if len(partFailures) == 0 {
					lines = strings.Split(placed, "\n")
					continue
				}
			}
			failures = append(failures, err)
			continue
		}
		replaced := make([]string, 0, len(lines)-len(current.before)+len(current.after))
		replaced = append(replaced, lines[:index]...)
		replaced = append(replaced, current.after...)
		replaced = append(replaced, lines[index+len(current.before):]...)
		lines = replaced
	}
	return strings.Join(lines, "\n"), failures
}

// splitByAnchor breaks a hunk into the separate edits it was probably
// meant to be, or nil if it is not that kind of hunk.
//
// The test is positional: every context and removed line is looked up in
// the file, and wherever two consecutive ones are not adjacent there, the
// hunk is cut. Added lines stay with the anchor above them, which is
// where the model put them. A hunk that is genuinely one edit has
// adjacent anchors throughout and comes back nil, so nothing is split
// that did not need splitting.
//
// Each anchor has to occur exactly once for this to be safe: a line that
// appears twice gives no honest answer about where its edit belongs, and
// guessing would put a change somewhere nobody asked for.
func splitByAnchor(lines []string, current hunk) []hunk {
	if len(current.ops) == 0 {
		return nil
	}
	at := func(text string) int {
		found := -1
		for index, line := range lines {
			if line == text || strings.TrimSpace(line) == strings.TrimSpace(text) {
				if found != -1 {
					return -1 // ambiguous
				}
				found = index
			}
		}
		return found
	}

	var parts []hunk
	var part hunk
	previous := -1
	for _, op := range current.ops {
		if op.kind == '+' {
			part.add(op.kind, op.text)
			continue
		}
		position := at(op.text)
		if position == -1 {
			return nil
		}
		if previous != -1 && position != previous+1 {
			parts = append(parts, part)
			part = hunk{}
		}
		part.add(op.kind, op.text)
		previous = position
	}
	parts = append(parts, part)
	if len(parts) < 2 {
		return nil
	}
	return parts
}

// locate finds the one place block occurs in lines. A short block that
// appears more than once is ambiguous, and picking one would risk editing
// the wrong part of the file, so it is refused instead.
func locate(lines, block []string) (int, *HunkError) {
	matches := findAll(lines, block, func(a, b string) bool { return a == b })
	if len(matches) == 0 {
		// Fall back to ignoring indentation and line-ending drift, which
		// a model reproducing a file by eye often gets slightly wrong.
		matches = findAll(lines, block, func(a, b string) bool {
			return strings.TrimSpace(a) == strings.TrimSpace(b)
		})
	}
	if len(matches) == 0 {
		// The instructions warn the model that an HTML entity spelled out
		// is a common way to get this wrong; recovering from it here means
		// the warning doesn't have to work every time.
		matches = findAll(lines, block, func(a, b string) bool {
			return normalizeEntities(a) == normalizeEntities(b)
		})
	}
	switch {
	case len(matches) == 0:
		// Every line is in the file, just not together. This is a whole
		// class of mistake on its own — anchors gathered from all over a
		// file into one hunk — and it needs saying as such, because the
		// near-miss report below is actively misleading about it: the
		// closest line to "type Language = ..." is that very line, so it
		// prints expected and found as the same text and sends the model
		// hunting for a difference that is not there. Traced from a real
		// run that burned its whole edit budget on exactly that.
		// One or two lines of the block are nowhere in the file while the
		// rest are all there. Almost always a marker this format did not
		// recognise — "@@", "***", "*** End Patch" have each cost a whole
		// turn — or a line invented rather than copied. Naming it is the
		// generic answer: it does not need us to have anticipated which
		// convention the model would reach for next.
		if absent := absentLines(lines, block); absent != "" {
			reason := "this hunk has a line that is nowhere in the file"
			return 0, &HunkError{Reason: reason, Extra: summaryOf(absent), Detail: reason + ":" + absent +
				"\n\nEverything else in the hunk was found. If that line was meant to divide " +
				"two edits, a blank line does that; otherwise copy it from the file exactly."}
		}
		if scattered := scatteredLines(lines, block); scattered != "" {
			reason := "this hunk's lines are each in the file, but not next to each other"
			return 0, &HunkError{Reason: reason, Extra: summaryOf(scattered), Detail: reason + ":" + scattered +
				"\n\nA hunk's lines have to be consecutive in the file. Separate edits go in " +
				"separate hunks, each under its own header or separated by a blank line."}
		}
		expected, found, at := nearMiss(lines, block)
		reason := "could not find this hunk's lines in the file"
		detail := fmt.Sprintf("%s:\n%s", reason, preview(block))
		if expected != "" {
			detail += fmt.Sprintf("\n\nthe closest match is at line %d, where it differs:\n  expected: %q\n  in file:  %q", at, expected, found)
		}
		return 0, &HunkError{Reason: reason, Expected: expected, Found: found, Detail: detail, Block: block}
	case len(matches) > 1 && len(block) < 3:
		reason := fmt.Sprintf("this hunk's lines appear %d times, too ambiguous to place", len(matches))
		return 0, &HunkError{Reason: reason, Detail: fmt.Sprintf("%s:\n%s%s", reason, preview(block), disambiguate(lines, matches))}
	default:
		return matches[0], nil
	}
}

// disambiguate reports where each ambiguous match actually sits in the
// file, plus the line immediately before it, so a repair attempt can add
// that as distinguishing context and land on the right one directly —
// without this, "appears twice" tells the model nothing it doesn't
// already know from writing the hunk itself, and a repair is just as
// likely to reproduce the same ambiguity as fix it (seen in practice: a
// short call-site line repeated verbatim in two handlers took three
// failed repair attempts before falling back to a whole-file rewrite).
func disambiguate(lines []string, matches []int) string {
	var text strings.Builder
	text.WriteString("\n\nadd a line of context right before it to tell them apart:")
	for _, index := range matches {
		before := "(start of file)"
		if index > 0 {
			before = strings.TrimSpace(lines[index-1])
		}
		fmt.Fprintf(&text, "\n  line %d, preceded by: %q", index+1, before)
	}
	return text.String()
}

var entityReplacer = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&apos;", "'",
)

func normalizeEntities(line string) string {
	return entityReplacer.Replace(strings.TrimSpace(line))
}

// nearMiss looks for the window in lines that matches block the most
// (ignoring whitespace), and reports the first line where it actually
// differs. A block that doesn't exist anywhere close gets nothing added:
// a low-quality guess would be worse than no guess. This turns "could not
// find this hunk" from a dead end into something the model can act on
// directly, rather than having it reproduce the same failing diff again.
// nearMiss finds the closest place the block almost fits and reports the
// first line that differs there: what the hunk expected, what is actually
// in the file, and which line that is. Empty when nothing resembles it.
func nearMiss(lines, block []string) (expected, found string, at int) {
	bestStart, bestScore := -1, 0
	for start := 0; start+len(block) <= len(lines); start++ {
		score := 0
		for offset, want := range block {
			if strings.TrimSpace(lines[start+offset]) == strings.TrimSpace(want) {
				score++
			}
		}
		if score > bestScore {
			bestStart, bestScore = start, score
		}
	}
	// Require at least half the block to already line up, or this is
	// pointing at an unrelated part of the file rather than a near miss.
	if bestStart != -1 && bestScore*2 >= len(block) {
		for offset, want := range block {
			got := lines[bestStart+offset]
			if got != want {
				return want, got, bestStart + offset + 1
			}
		}
	}
	// A one-line block can never clear the half-match bar above: half of
	// one line is a whole line, and a whole line matching is the case
	// that did not happen. One-line edits are the common case in the
	// compact format, which asks for only enough context to be unique,
	// so without this the edits likeliest to be written are the ones
	// reported with the least to go on.
	return closestLine(lines, block)
}

// closestLine finds the line in the file most nearly the one the hunk
// wanted, for when no run of lines aligned well enough to compare
// position by position.
func closestLine(lines, block []string) (expected, found string, at int) {
	want := ""
	for _, line := range block {
		if strings.TrimSpace(line) != "" {
			want = line
			break
		}
	}
	if want == "" {
		return "", "", 0
	}
	bestIndex, bestScore := -1, 0.0
	for index, line := range lines {
		if score := similarity(line, want); score > bestScore {
			bestIndex, bestScore = index, score
		}
	}
	// Below half matching, this is pointing at an unrelated line and
	// saying "did you mean" about it would send the reader the wrong way.
	// An exact match is worse still: reporting a line as differing from
	// itself is the most confusing thing this can say, and it means the
	// trouble is elsewhere in the block rather than on this line.
	if bestIndex == -1 || bestScore < 0.5 || lines[bestIndex] == want {
		return "", "", 0
	}
	return want, lines[bestIndex], bestIndex + 1
}

// misrouted rewrites any failure whose lines are sitting, whole and
// findable, in another file the same patch touches. An edit under the
// wrong header is otherwise reported as lines that are simply not there,
// which is true and says nothing: the model reads it as its own text
// being wrong and rewrites text that was right all along. Seen when a
// second file's header was written in a shape that got dropped, sending
// nine edits to look for their lines in a file they had never been in.
func misrouted(failures []*HunkError, contents map[string]string) {
	for _, failure := range failures {
		if len(failure.Block) == 0 {
			continue
		}
		for path, content := range contents {
			if path == failure.Path {
				continue
			}
			if _, err := locate(strings.Split(content, "\n"), failure.Block); err != nil {
				continue
			}
			failure.Reason = fmt.Sprintf("these lines are in %s, not in %s", path, failure.Path)
			failure.Expected, failure.Found, failure.Extra = "", "", nil
			failure.Detail = failure.Reason + ":\n" + preview(failure.Block) +
				"\n\nPut this edit under a header naming " + path + "."
			break
		}
	}
}

// summaryOf turns one of the indented detail blocks above into the lines
// a progress trail shows, capped so a long one cannot flood it.
func summaryOf(detail string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimPrefix(detail, "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
		if len(lines) == 3 {
			break
		}
	}
	return lines
}

// absentLines names the few lines of a block that appear nowhere in the
// file, when everything else in it does. Empty unless that is the case:
// a block where half the lines are missing is an ordinary mismatch and
// the near-miss report says more about it.
func absentLines(lines, block []string) string {
	present := make(map[string]bool, len(lines))
	for _, line := range lines {
		present[strings.TrimSpace(line)] = true
	}
	var missing []string
	counted := 0
	for _, want := range block {
		if strings.TrimSpace(want) == "" {
			continue
		}
		counted++
		if present[strings.TrimSpace(want)] {
			continue
		}
		// Absent is not enough: a line transcribed almost right is also
		// absent, and for that the near-miss report — which shows what is
		// really there beside what was asked for — says far more. This is
		// for a line with no relative in the file at all, which is what a
		// stray "***" or "@@" looks like.
		best := 0.0
		for _, line := range lines {
			if score := similarity(line, want); score > best {
				best = score
			}
		}
		if best < 0.5 {
			missing = append(missing, want)
		}
	}
	// Only worth saying when the block is mostly right and a line or two
	// stands out. One missing line out of two is not a standout.
	if len(missing) == 0 || len(missing) > 2 || counted-len(missing) < 2 {
		return ""
	}
	var text strings.Builder
	for _, line := range missing {
		fmt.Fprintf(&text, "\n  not in the file: %s", clipLine(strings.TrimSpace(line)))
	}
	return text.String()
}

// scatteredLines reports where each of a block's lines actually sits,
// when every one of them is in the file but they are not consecutive.
// Empty unless that is exactly the case: if any line is genuinely absent
// the ordinary near-miss report is the more useful one.
func scatteredLines(lines, block []string) string {
	var text strings.Builder
	counted := 0
	for _, want := range block {
		if strings.TrimSpace(want) == "" {
			continue
		}
		at := -1
		for index, line := range lines {
			if line == want || strings.TrimSpace(line) == strings.TrimSpace(want) {
				at = index + 1
				break
			}
		}
		if at == -1 {
			return ""
		}
		counted++
		fmt.Fprintf(&text, "\n  line %d: %s", at, clipLine(strings.TrimSpace(want)))
	}
	if counted < 2 {
		return ""
	}
	return text.String()
}

// similarity scores two lines by how much of them agrees from each end,
// which is the shape of the mistake worth catching: a line transcribed
// almost right, differing somewhere in the middle. It is deliberately not
// a general edit distance — this runs against every line of every file in
// a patch, and the cases it misses are ones where nothing close exists.
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	longest := len(a)
	if len(b) > longest {
		longest = len(b)
	}
	if longest == 0 {
		return 0
	}
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	return float64(prefix+suffix) / float64(longest)
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
