package agent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Step names one phase of a turn. A turn spends its wall clock in a
// handful of distinguishable places, and which one is dominant is the
// only thing that says what to fix: a slow think is the provider, a slow
// test is the project, and a slow recall is us handing the model more
// history than the answer is worth.
type Step string

const (
	// StepRecall is the message_route call: deciding whether the message
	// is a coding task and, when this folder has history, choosing which
	// earlier turns to carry. It is called out separately from the rest
	// of the model calls because it is the one that grows with the
	// folder's age rather than with the size of the task.
	StepRecall Step = "recall"
	StepMap    Step = "map"
	// StepThink is time inside a code_change call, StepTools the time
	// spent answering the tool calls one came back with. Splitting them
	// separates what the provider costs from what reading the repository
	// costs, which are fixed by completely different things.
	StepThink   Step = "think"
	StepTools   Step = "tools"
	StepPatch   Step = "patch"
	StepTest    Step = "test"
	StepRewrite Step = "rewrite"
)

// Span is one step's share of a turn: how long it took in total and how
// many times it ran, since three slow think calls and one very slow one
// are different problems.
type Span struct {
	Step  Step
	Spent time.Duration
	Calls int
}

// RecallDetail is what the history-selection call was handed and what it
// made of it. Recorded in this much detail because selection is the step
// most likely to quietly become the expensive one: it is shown every
// turn this folder has ever taken (up to the session store's cap) to
// pick out the few that matter, so its input grows with the folder's
// history while its output stays one or two sentences. Offered against
// Chosen is the ratio that says whether the call is still earning the
// round trip it costs.
type RecallDetail struct {
	// Offered is how many earlier turns it was shown, Bytes how much
	// rendered history that came to.
	Offered int
	Bytes   int
	// Chosen is how many of those it picked as relevant.
	Chosen int
	Spent  time.Duration
}

// Line is the progress line for this step, streamed as soon as the call
// returns. With no history there is nothing to select from and the call
// only routed, so it says that rather than reporting "0 of 0 turns".
func (detail RecallDetail) Line() string {
	if detail.Offered == 0 {
		return "routed the message · " + formatDuration(detail.Spent)
	}
	return fmt.Sprintf("recalled %d of %d earlier %s · %s offered · %s",
		detail.Chosen, detail.Offered, plural(detail.Offered, "turn", "turns"),
		formatBytes(detail.Bytes), formatDuration(detail.Spent))
}

// Profile is where a turn's wall clock went. Like Usage it covers the
// whole turn — every call, tool round, patch and test run it took — not
// the last one, since a Runner is created fresh per turn.
//
// Steps are reported in the order they first ran, which is the order the
// turn actually works in, and repeats of a step fold into that first
// entry rather than appending: a turn that thinks, reads, thinks, reads
// and then patches should read as three steps with counts, not as five.
type Profile struct {
	spans  []Span
	Recall RecallDetail
}

// record adds spent to a step, starting it at its first occurrence.
func (profile *Profile) record(step Step, spent time.Duration) {
	for index := range profile.spans {
		if profile.spans[index].Step == step {
			profile.spans[index].Spent += spent
			profile.spans[index].Calls++
			return
		}
	}
	profile.spans = append(profile.spans, Span{Step: step, Spent: spent, Calls: 1})
}

// Spans are the steps this turn spent time in, in the order they ran.
func (profile Profile) Spans() []Span {
	return profile.spans
}

// Total is the whole turn's measured time. It is the sum of the steps
// rather than a clock around the turn, so it deliberately excludes the
// slivers between them: what it accounts for is what could be fixed.
func (profile Profile) Total() time.Duration {
	var total time.Duration
	for _, span := range profile.spans {
		total += span.Spent
	}
	return total
}

// Empty reports whether nothing was measured at all, so a caller can
// skip printing a line rather than print an empty one.
func (profile Profile) Empty() bool {
	return len(profile.spans) == 0
}

// Summary is a compact, human-readable line — "7.2s total · recall 1.0s
// · map 11ms · think 4.7s ×3" — with no styling of its own, so a caller
// can wrap it in whatever the terminal or the web UI needs. Empty when
// nothing was measured.
func (profile Profile) Summary() string {
	if profile.Empty() {
		return ""
	}
	parts := []string{formatDuration(profile.Total()) + " total"}
	for _, span := range profile.spans {
		part := string(span.Step) + " " + formatDuration(span.Spent)
		if span.Calls > 1 {
			part += " ×" + strconv.Itoa(span.Calls)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " · ")
}

// formatDuration keeps a duration to one glanceable token. Sub-millisecond
// steps are reported as "<1ms" rather than a fake precision nobody can act
// on: the point of the line is which step to go and fix.
func formatDuration(spent time.Duration) string {
	switch {
	case spent >= time.Second:
		return strconv.FormatFloat(spent.Seconds(), 'f', 1, 64) + "s"
	case spent >= time.Millisecond:
		return strconv.FormatInt(int64(spent/time.Millisecond), 10) + "ms"
	case spent <= 0:
		return "0ms"
	default:
		return "<1ms"
	}
}

// formatBytes sizes the history block in the units a reader thinks in.
func formatBytes(size int) string {
	if size < 1024 {
		return strconv.Itoa(size) + " B"
	}
	return fmt.Sprintf("%.1f KB", float64(size)/1024)
}
