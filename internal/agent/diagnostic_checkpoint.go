package agent

import (
	"context"
	"strings"
	"time"
)

const checkpointTimeout = 3 * time.Second
const maxCheckpointBytes = 8 << 10

// Diagnostic only: neither success nor failure changes completion or edit state.
func diagnosticCheckpoint(ctx context.Context, root string) (string, string) {
	name, args := detectTestCommand(root)
	status, output := "skipped", "No supported project check detected."
	if name != "" {
		child, cancel := context.WithTimeout(ctx, checkpointTimeout)
		defer cancel()
		status, output = runCheckpoint(child, root, name, args)
	}
	message := "Diagnostic checkpoint " + status + ". This does not establish task completion; the final project check is still required.\n" + output
	message = strings.ToValidUTF8(message, "")
	if len(message) > maxCheckpointBytes {
		message = strings.ToValidUTF8(message[:maxCheckpointBytes-32], "") + "\n[diagnostic truncated]"
	}
	return status, message
}

// A shared writer caps memory even when stdout/stderr are noisy. os/exec
// serializes writes when both streams use this same comparable writer.
type checkpointOutput struct {
	text      strings.Builder
	truncated bool
}

func (output *checkpointOutput) Write(p []byte) (int, error) {
	remaining := maxCheckpointBytes - 512 - output.text.Len()
	keep := len(p)
	if keep > remaining {
		keep = remaining
		output.truncated = true
	}
	output.text.Write(p[:keep])
	return len(p), nil
}

func (output *checkpointOutput) String() string {
	// Strip invalid bytes instead of expanding them beyond the byte cap.
	text := strings.ToValidUTF8(output.text.String(), "")
	if output.truncated {
		text += "\n[diagnostic output truncated]"
	}
	return text
}
