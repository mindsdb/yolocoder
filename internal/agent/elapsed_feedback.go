package agent

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// timedInput keeps one current timing note on normal requests, without
// changing the persistent transcript or the cacheable instruction prefix.
// Both production timestamps carry Go's monotonic clock. This is elapsed
// coding-loop time, excluding the earlier map and routing work.
func (session *changeSession) timedInput(ctx context.Context, started, now time.Time) []any {
	if session.prefetched {
		return session.transcript
	}
	note := fmt.Sprintf("Runtime timing: normal coding loop elapsed %.1f seconds.", max(0, now.Sub(started).Seconds()))
	if deadline, ok := ctx.Deadline(); ok {
		note += fmt.Sprintf(" Parent context deadline remaining: %.1f seconds.", max(0, deadline.Sub(now).Seconds()))
	}
	note += " Complete the requested work and signal completion when ready so the existing project check can run. Timing is not evidence that the task is complete."
	// Clip makes append copy rather than write into the transcript's spare room.
	return append(slices.Clip(session.transcript), inputMessage{Role: "user", Content: note})
}
