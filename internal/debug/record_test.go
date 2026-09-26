package debug

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Records go under the home directory, so the test moves home rather
// than writing into the real one.
func useTemporaryHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
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
	useTemporaryHome(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// A file blocks creation of the config directory on every platform,
	// including Windows and privileged users that ignore Unix mode bits.
	if err := os.WriteFile(filepath.Join(home, ".config"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	Record("patch_rejected", map[string]any{"folder": "x"})
	if recent := Recent(5); recent != nil {
		t.Fatalf("expected nothing, got %v", recent)
	}
}
