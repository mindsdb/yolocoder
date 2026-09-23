package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ripgrep is not a dependency anyone installs on purpose, and when it
// is missing every search used to fail instantly — found on a machine
// that had been running this for weeks, with the model left guessing
// which files to read.
func TestSearchWorksWithoutRipgrep(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/a.ts", "const accent = \"red\"\nconst other = 1\n")
	write("src/b.ts", "no match here\n")
	write("node_modules/dep/c.ts", "accent everywhere\n")

	out, err := (&Repository{Root: root}).searchWithoutRipgrep("accent")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "src/a.ts:1:const accent") {
		t.Fatalf("missing the match, or the wrong shape:\n%s", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Fatalf("searched node_modules:\n%s", out)
	}
	// The shape downstream code parses.
	if paths := len(strings.Split(strings.TrimSpace(out), "\n")); paths != 1 {
		t.Fatalf("expected one matching line, got:\n%s", out)
	}
}

func TestSearchWithoutRipgrepSaysWhenNothingMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := (&Repository{Root: root}).searchWithoutRipgrep("zzz")
	if err != nil || out != "No matches." {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// Whichever path it takes, an empty query is refused the same way.
func TestAnEmptyQueryIsStillRefused(t *testing.T) {
	if _, err := (&Repository{Root: t.TempDir()}).Search(context.Background(), "  "); err == nil {
		t.Fatal("an empty query was accepted")
	}
}

func TestABinaryFileIsSkipped(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.bin"), []byte("accent\x00accent"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := (&Repository{Root: root}).searchWithoutRipgrep("accent")
	if out != "No matches." {
		t.Fatalf("read a binary file: %q", out)
	}
}
