package agent

import (
	"testing"

	"github.com/mindsdb/yolocoder/internal/debug"
	"github.com/mindsdb/yolocoder/internal/repo"
)

// The point of the record is the question it answers later: what was
// being asked, in which folder, on which attempt, and why the patch was
// turned down. All four have to survive the trip.
func TestARejectedPatchIsRecorded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOLOCODER_TEST_RECORDS", "1")
	session := &changeSession{
		runner: &Runner{repository: &repo.Repository{Root: "/tmp/project"}},
		task:   "rename the title",
		used:   map[string]int{"apply_diff": 2},
	}
	failure := &repo.PatchError{Failures: []*repo.HunkError{{Path: "src/App.tsx", Reason: "context not found"}}}

	session.recordFailure("*** @src/App.tsx\n-old\n+new\n", failure)

	recent := debug.Recent(1)
	if len(recent) != 1 {
		t.Fatalf("a rejected patch left %d records", len(recent))
	}
	entry := recent[0]
	if entry["folder"] != "/tmp/project" || entry["task"] != "rename the title" {
		t.Fatalf("record lost its context: %v", entry)
	}
	if entry["attempt"] != float64(2) {
		t.Fatalf("wanted the second attempt, record says %v", entry["attempt"])
	}
	if reasons, _ := entry["reasons"].([]any); len(reasons) == 0 {
		t.Fatalf("recorded a failure with no reason: %v", entry)
	}
	if patch, _ := entry["patch"].(string); patch == "" {
		t.Fatal("recorded a rejection without the patch that was rejected")
	}
}
