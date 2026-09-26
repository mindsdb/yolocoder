package agent

import (
	"context"
	"fmt"
	"net/http"
)

// Experimental and off in normal builds. This model handles the normal loop
// only after Jev has selected the small-edit route and context was read.
var smallEditModel string

func (session *changeSession) create(ctx context.Context, request responseRequest, progress Progress) (responseEnvelope, error) {
	runner := session.runner
	if session.writer == nil {
		session.writer = runner.client
		if session.prefetched && runner.smallEditModel != "" && runner.smallEditModel != runner.client.model {
			// Preserve negotiation across this session's requests without changing
			// the configured client used by normal tasks and whole-file fallback.
			writer := *runner.client
			writer.model = runner.smallEditModel
			session.writer = &writer
			progress.Log("  small-edit writer: " + writer.model)
		}
	}
	// Both routes share one replay token. Small UI permits only the exact
	// Muse policy error checked by Client, never the normal gateway replay.
	retry := func(status int) bool {
		if session.transientRetryUsed || (session.prefetched && status != http.StatusServiceUnavailable) {
			return false
		}
		session.transientRetryUsed = true
		progress.Log(fmt.Sprintf("  transient retry: dispatch status=%d attempt=1", status))
		return true
	}
	response, err := session.writer.createWithRetry(ctx, request, retry)
	if err == nil || session.writer == runner.client || ctx.Err() != nil {
		return response, err
	}
	// A provider failure sticks for the rest of this turn. Edit rejections and
	// failed project checks instead stay in the same bounded coding loop.
	session.writer = runner.client
	progress.Log("  small-edit writer unavailable; continuing with " + runner.client.model)
	return runner.client.createWithRetry(ctx, request, retry)
}
