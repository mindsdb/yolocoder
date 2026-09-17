package agent

import (
	"fmt"
	"strconv"
	"time"
)

// Step names one phase of a turn. A turn spends its wall clock in a
// handful of distinguishable places, and which one is dominant is the
// only thing that says what to fix: a slow think is the provider, a slow
// test is the project, and a slow recall is us handing the model more
// history than the answer is worth.
type Step string

const (
	// StepRecall is time spent in the recall tool, reading what this
	// folder was asked before. It is separated from the other tools
	// because it is the one whose cost grows with the folder's age
	// rather than with the size of the task, and because whether it gets
	// called at all is the thing worth watching: history is offered on
	// demand rather than pushed into every turn, and how often a turn
	// actually reaches for it is what says whether that was right.
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

// RecallDetail is what history this folder had and what the turn did
// about it. Available against Served is the whole point: history is held
// back and offered through a tool rather than pushed into every turn, so
// the number that says whether that was the right call is how often a
// turn with history available actually asked for it.
//
// Served, Bytes and Spent are all zero on a turn that never reached for
// it, which is the answer rather than missing data.
type RecallDetail struct {
	// Available is how many earlier turns this folder had to offer.
	Available int
	// Served is how many the recall tool actually handed over, Bytes how
	// much rendered history that came to, and Spent how long it took.
	Served int
	Bytes  int
	Spent  time.Duration
}

// Line describes what the recall tool served, for the trail.
func (detail RecallDetail) Line() string {
	return fmt.Sprintf("recalled %d earlier %s · %s",
		detail.Served, plural(detail.Served, "turn", "turns"), formatBytes(detail.Bytes))
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

// Summary is the turn's total — "2.9s total" — with no styling of its
// own, so a caller can wrap it in whatever the terminal or the web UI
// needs. Empty when nothing was measured.
//
// Deliberately only the total: every step already streamed its own time
// onto the trail as it happened, a few lines above wherever this ends
// up, so repeating the breakdown underneath says nothing new and buries
// the one number worth comparing between turns. Spans keeps the detail
// for a caller that wants it.
func (profile Profile) Summary() string {
	if profile.Empty() {
		return ""
	}
	return formatDuration(profile.Total()) + " total"
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
