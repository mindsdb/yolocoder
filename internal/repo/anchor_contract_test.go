package repo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the normal prompt's patch forms against the real applier, not
// assertions about the wording of the prompt.
func TestCompactAnchorContractEdits(t *testing.T) {
	for _, test := range []struct {
		name, before, patch, want string
	}{
		{
			"removed line anchors replacement", "enabled = false\nkeep = true\n",
			"@settings.txt\n-enabled = false\n+enabled = true\n",
			"enabled = true\nkeep = true\n",
		},
		{
			"unchanged context anchors insertion", "[first]\nvalue = 1\n[second]\nvalue = 1\n",
			"@settings.txt\n [second]\n value = 1\n+extra = 2\n",
			"[first]\nvalue = 1\n[second]\nvalue = 1\nextra = 2\n",
		},
		{
			"whole file removes current contents", "old\n\nremaining\n",
			"@settings.txt\n-old\n-\n-remaining\n+new\n+contents\n",
			"new\ncontents\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := project(t, map[string]string{"settings.txt": test.before})
			if err := repository.Apply(test.patch); err != nil {
				t.Fatal(err)
			}
			if got := read(t, repository, "settings.txt"); got != test.want {
				t.Fatalf("contents = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCompactAnchorContractRefusalAndRepairAreAtomic(t *testing.T) {
	for _, test := range []struct {
		name, before, refused, repaired, want string
	}{
		{
			"insertion lacks context", "first\nlast\n",
			"@settings.txt\n+inserted\n",
			"@settings.txt\n first\n+inserted\n",
			"first\ninserted\nlast\n",
		},
		{
			"insertion anchor repeats", "first\nvalue\nlast\nvalue\n",
			"@settings.txt\n value\n+inserted\n",
			"@settings.txt\n last\n value\n+inserted\n",
			"first\nvalue\nlast\nvalue\ninserted\n",
		},
		{
			"creation cannot replace nonempty file", "first\nlast\n",
			"@+settings.txt\nreplacement\n",
			"@settings.txt\n-first\n-last\n+replacement\n",
			"replacement\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := project(t, map[string]string{
				"settings.txt": test.before,
				"other.txt":    "old\n",
			})
			// A later placement failure must not land either an earlier valid
			// replacement or a staged new file in the same patch.
			const prefix = "@other.txt\n-old\n+changed\n@+notes.txt\nNotes\n+ literal plus\n"
			err := repository.Apply(prefix + test.refused)
			var failure *PatchError
			if !errors.As(err, &failure) || len(failure.Failures) != 1 || failure.Failures[0].Path != "settings.txt" {
				t.Fatalf("expected settings placement failure, got %v", err)
			}
			if got := read(t, repository, "settings.txt"); got != test.before {
				t.Fatalf("rejected target changed: %q", got)
			}
			if got := read(t, repository, "other.txt"); got != "old\n" {
				t.Fatalf("earlier edit landed despite rejection: %q", got)
			}
			if _, err := os.Stat(filepath.Join(repository.Root, "notes.txt")); !os.IsNotExist(err) {
				t.Fatalf("new file exists after rejection: %v", err)
			}
			if err := repository.Apply(prefix + test.repaired); err != nil {
				t.Fatal(err)
			}
			for path, want := range map[string]string{
				"settings.txt": test.want,
				"other.txt":    "changed\n",
				"notes.txt":    "Notes\n+ literal plus",
			} {
				if got := read(t, repository, path); got != want {
					t.Fatalf("%s = %q, want %q", path, got, want)
				}
			}
		})
	}
}
