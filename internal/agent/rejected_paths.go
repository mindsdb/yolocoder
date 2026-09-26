package agent

import (
	"fmt"
	"strings"

	"github.com/mindsdb/yolocoder/internal/repo"
)

// rejectedPatchPaths describes this rejected batch, not unfinished obligations
// for the turn. Only a direct PatchError proves none of this call was written.
func rejectedPatchPaths(patch string, failure *repo.PatchError) string {
	const maxPaths = 24
	const maxBytes = 2 << 10
	const heading = "\nDeclared paths in this rejected call (none were applied):\n"
	const note = "Unmarked paths were not applied either. A repair must retain their still-needed edits, not just fix the marked paths.\n"
	paths := repo.PatchPaths(patch) // Deduplicated in first-declaration order.
	if len(paths) == 0 {
		return ""
	}
	failed := make(map[string]bool)
	for _, one := range failure.Failures {
		failed[one.Path] = true
	}
	omitted := func(count int) string {
		return fmt.Sprintf("%d declared paths omitted by the display limits.\n", count)
	}
	// Reserve the footer before adding complete quoted paths; never truncate a
	// path into something that could be mistaken for a different file.
	reserved := len(note) + len(omitted(len(paths)))
	var text strings.Builder
	text.WriteString(heading)
	shown := 0
	for _, path := range paths {
		line := fmt.Sprintf("- %q", path)
		if failed[path] {
			line += " [placement error]"
		}
		line += "\n"
		if shown == maxPaths || text.Len()+len(line)+reserved > maxBytes {
			break
		}
		text.WriteString(line)
		shown++
	}
	if shown < len(paths) {
		text.WriteString(omitted(len(paths) - shown))
	}
	text.WriteString(note)
	return text.String()
}
