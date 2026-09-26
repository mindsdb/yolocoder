package repo

import (
	"strings"
	"testing"
)

func TestAnchorRepairRejectsWholeBatchThenPlacesRepair(t *testing.T) {
	const original = "start\n  anchor\tvalue\nend\n"
	for _, test := range []struct {
		name, repair, want string
	}{
		{"before", "+added\n   anchor\tvalue", "start\nadded\n  anchor\tvalue\nend\n"},
		{"after", "   anchor\tvalue\n+added", "start\n  anchor\tvalue\nadded\nend\n"},
		{"replacement", "-  anchor\tvalue\n+added", "start\nadded\nend\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := project(t, map[string]string{"target.txt": original, "companion.txt": "old\n"})
			const companion = "@companion.txt\n-old\n+new\n\n"
			err := repository.Apply(companion + "@target.txt\n+added\n")
			failure, ok := err.(*PatchError)
			if !ok || len(failure.Failures) != 1 {
				t.Fatalf("want one direct placement failure, got %v", err)
			}
			one := failure.Failures[0]
			const reason = "a hunk has no context to place it by"
			if one.Path != "target.txt" || one.Reason != reason {
				t.Fatalf("placement identity changed: %#v", one)
			}
			if got := strings.Join(Explain(err), "\n"); got != "target.txt: "+reason {
				t.Fatalf("progress category changed: %q", got)
			}
			// Detail reaches callers through Error; progress stays concise. The
			// advice describes the rule without inventing source for the repair.
			if one.Detail == reason || !strings.Contains(err.Error(), one.Detail) ||
				!strings.Contains(one.Detail, "current file") || strings.Contains(one.Detail, "anchor\tvalue") {
				t.Fatalf("missing or fabricated placement explanation: %q", one.Detail)
			}
			if read(t, repository, "target.txt") != original || read(t, repository, "companion.txt") != "old\n" {
				t.Fatal("rejected batch wrote the target or its valid companion")
			}
			if err := repository.Apply(companion + "@target.txt\n" + test.repair + "\n"); err != nil {
				t.Fatal(err)
			}
			if got := read(t, repository, "target.txt"); got != test.want {
				t.Fatalf("repaired target = %q, want %q", got, test.want)
			}
			if got := read(t, repository, "companion.txt"); got != "new\n" {
				t.Fatalf("companion repair = %q", got)
			}
		})
	}
}

func TestAnchorRepairPreservesEmptyAndNewFileInsertion(t *testing.T) {
	for _, test := range []struct {
		name, body, patch string
		missing           bool
	}{
		{name: "empty", patch: "@target.txt\n+added\n"},
		{name: "whitespace", body: " \t\n\t\n", patch: "@target.txt\n+added\n"},
		{name: "new", missing: true, patch: "@+target.txt\nadded\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{}
			if !test.missing {
				files["target.txt"] = test.body
			}
			repository := project(t, files)
			if err := repository.Apply(test.patch); err != nil {
				t.Fatal(err)
			}
			if got := read(t, repository, "target.txt"); got != "added" {
				t.Fatalf("inserted content = %q", got)
			}
		})
	}
}

func TestAnchorRepairCreateHeaderCannotReplaceNonemptyFile(t *testing.T) {
	repository := project(t, map[string]string{"target.txt": "existing\n"})
	err := repository.Apply("@+target.txt\nreplacement\n")
	failure, ok := err.(*PatchError)
	if !ok || len(failure.Failures) != 1 || failure.Failures[0].Reason != "a hunk has no context to place it by" {
		t.Fatalf("want unchanged contextless rejection, got %v", err)
	}
	if got := read(t, repository, "target.txt"); got != "existing\n" {
		t.Fatalf("create header overwrote existing content: %q", got)
	}
}
