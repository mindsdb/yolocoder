package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// Usage is the token accounting for one call, or several calls summed
// together for a whole turn — a turn can cost more than one call (route,
// then one or more tool rounds, then possibly a rewrite), and what's
// worth reporting is the total, not any single call's share of it.
//
// A zero value means no provider usage was ever recorded, as opposed to
// a turn that genuinely cost zero tokens: not every OpenAI-compatible
// endpoint reports usage at all, and fewer still report how much of the
// input was served from cache, so CachedTokens in particular is often
// legitimately zero even on a real, non-empty Usage.
type Usage struct {
	InputTokens  int
	CachedTokens int
	OutputTokens int
	TotalTokens  int
}

func (usage Usage) add(other Usage) Usage {
	return Usage{
		InputTokens:  usage.InputTokens + other.InputTokens,
		CachedTokens: usage.CachedTokens + other.CachedTokens,
		OutputTokens: usage.OutputTokens + other.OutputTokens,
		TotalTokens:  usage.TotalTokens + other.TotalTokens,
	}
}

// Empty reports whether nothing was ever recorded for this turn — there
// is nothing worth showing, rather than a turn that cost zero tokens.
func (usage Usage) Empty() bool {
	return usage == Usage{}
}

// Summary is a compact, human-readable line — "1,234 tokens · 890 in
// (120 cached) · 344 out" — with no styling of its own, so a caller can
// wrap it in whatever the terminal or the web UI needs. Empty when
// nothing was recorded, so a caller can skip printing a line at all.
func (usage Usage) Summary() string {
	if usage.Empty() {
		return ""
	}
	line := fmt.Sprintf("%s tokens · %s in", formatCount(usage.TotalTokens), formatCount(usage.InputTokens))
	if usage.CachedTokens > 0 {
		line += fmt.Sprintf(" (%s cached)", formatCount(usage.CachedTokens))
	}
	line += fmt.Sprintf(" · %s out", formatCount(usage.OutputTokens))
	return line
}

// formatCount adds thousands separators, since Go has no built-in for it
// without pulling in golang.org/x/text.
func formatCount(n int) string {
	digits := strconv.Itoa(n)
	if len(digits) <= 3 {
		return digits
	}
	var groups []string
	for len(digits) > 3 {
		groups = append([]string{digits[len(digits)-3:]}, groups...)
		digits = digits[:len(digits)-3]
	}
	groups = append([]string{digits}, groups...)
	return strings.Join(groups, ",")
}
