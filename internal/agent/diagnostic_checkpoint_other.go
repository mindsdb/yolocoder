//go:build !darwin && !linux

package agent

import "context"

func runCheckpoint(context.Context, string, string, []string) (string, string) {
	return "skipped", "Bounded diagnostic process cleanup is unsupported on this platform."
}
