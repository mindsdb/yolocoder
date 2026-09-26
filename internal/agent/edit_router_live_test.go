//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mindsdb/yolocoder/internal/config"
)

// Explicitly opt-in paid route calibration, separate from unit tests and
// scored app trials. All cases are fixed before asking the live model.
func TestEditRouterLiveCalibration(t *testing.T) {
	output := os.Getenv("YOLOCODER_ROUTE_PROBE_OUTPUT")
	if output == "" {
		t.Skip("live router calibration is opt-in")
	}
	provider, ok, err := config.Load()
	if err != nil || !ok {
		t.Fatal("configured provider unavailable")
	}
	client, err := NewClient(provider)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ID, Task string
		Want     bool
	}{
		{"heading", "Change the visible heading from Welcome to Project Atlas. Preserve all behaviour.", true},
		{"colour", "Change the existing primary button background to #186350. Do not change its action.", true},
		{"spacing", "Increase the space below the existing main heading to 24px; leave everything else as it is.", true},
		{"feature", "Add a counter reset button and persist the count across reloads.", false},
		{"bug", "The counter skips numbers after fast clicks. Find and fix the cause.", false},
		{"ambiguous", "Make this app much better.", false},
		{"question", "What does this app do? Explain its architecture.", false},
		{"new_app", "Create a weather dashboard with a location search and hourly forecasts.", false},
		{"mixed", "Rename the heading to Atlas and add login with email and password.", false},
	}
	var rows []map[string]any
	for _, c := range cases {
		repository := folder(t, map[string]string{
			"src/Counter.tsx": "export default function Counter(){return <main><h1>Welcome</h1><button>Increment</button></main>}",
			"src/theme.css":   "h1 { margin-bottom: 12px } button { background: #223344 }",
			"server/api.ts":   "export const health = () => ({ok:true});",
		})
		runner := NewRunner(client, repository)
		runner.editRouterModel = "jev-1.13.0"
		mapping, _ := repository.Map()
		session := runner.newChangeSession(c.Task, mapping, nil, nil)
		progress := &recordingProgress{}
		start := time.Now()
		runner.prefetchEdit(context.Background(), session, mappedPaths(mapping), progress)
		actual := session.prefetched
		row := map[string]any{"id": c.ID, "task": c.Task, "expected_small_ui": c.Want, "actual_small_ui": actual, "matched": actual == c.Want, "paths": session.readPaths, "duration_s": time.Since(start).Seconds(), "usage": runner.usage, "log": progress.logs}
		rows = append(rows, row)
		data, _ := json.MarshalIndent(rows, "", "  ")
		if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: wanted small_ui=%v actual small_ui=%v", c.ID, c.Want, actual)
		if actual != c.Want {
			t.Errorf("route mismatch: %s", c.ID)
		}
	}
}
