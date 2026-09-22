package debug

import (
	"os"
	"strings"
	"testing"
)

// Records go under the home directory, so the test moves home rather
// than writing into the real one.
func useTemporaryHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YOLOCODER_TEST_RECORDS", "1")
	t.Setenv("YOLOCODER_TEST_RECORDS", "1")
}

func TestARecordCanBeReadBack(t *testing.T) {
	useTemporaryHome(t)
	Record("patch_rejected", map[string]any{"folder": "/tmp/x", "reasons": []string{"no such line"}})

	recent := Recent(5)
	if len(recent) != 1 {
		t.Fatalf("wrote one record, read back %d", len(recent))
	}
	if recent[0]["event"] != "patch_rejected" || recent[0]["folder"] != "/tmp/x" {
		t.Fatalf("record came back as %v", recent[0])
	}
	if recent[0]["at"] == nil {
		t.Fatal("a record with no time on it cannot be placed in a session")
	}
}

func TestTheNewestRecordComesFirst(t *testing.T) {
	useTemporaryHome(t)
	Record("patch_rejected", map[string]any{"folder": "first"})
	Record("patch_rejected", map[string]any{"folder": "second"})

	recent := Recent(5)
	if len(recent) != 2 || recent[0]["folder"] != "second" {
		t.Fatalf("wanted the newest first, got %v", recent)
	}
}

// A patch runs to tens of kilobytes and the file is always on, so a
// record keeps only the head of one.
func TestALongFieldIsClipped(t *testing.T) {
	useTemporaryHome(t)
	Record("patch_rejected", map[string]any{"patch": strings.Repeat("x", 10000)})

	patch, _ := Recent(1)[0]["patch"].(string)
	if len(patch) > 5000 {
		t.Fatalf("kept %d bytes of a 10000-byte patch", len(patch))
	}
	if !strings.HasSuffix(patch, "truncated ...") {
		t.Fatal("clipped without saying so")
	}
}

func TestNoRecordsIsNotAnError(t *testing.T) {
	useTemporaryHome(t)
	if recent := Recent(5); recent != nil {
		t.Fatalf("expected nothing, got %v", recent)
	}
}

// Nothing here may fail a run, so an unwritable home is silent.
func TestAnUnwritableHomeIsSurvived(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOLOCODER_TEST_RECORDS", "1")
	if err := os.Chmod(home, 0o500); err != nil {
		t.Skip("cannot make the directory unwritable here")
	}
	defer os.Chmod(home, 0o700)

	Record("patch_rejected", map[string]any{"folder": "x"})
	if recent := Recent(5); recent != nil {
		t.Fatalf("expected nothing, got %v", recent)
	}
}
