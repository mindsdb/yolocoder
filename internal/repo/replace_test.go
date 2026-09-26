package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceTextRejectsOverlappingMatches(t *testing.T) {
	r := replacementRepo(t, map[string]string{"text.txt": "aaaa"})
	if _, err := r.ReplaceText([]Replacement{{Path: "text.txt", Old: "aaa", New: "b"}}); err == nil {
		t.Fatal("overlapping matches are ambiguous")
	}
	got, _ := r.ReadFile("text.txt")
	if got != "aaaa" {
		t.Fatal("ambiguous replacement changed the file")
	}
}

func replacementRepo(t *testing.T, files map[string]string) *Repository {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &Repository{Root: root}
}

func TestReplaceTextPreservesLiteralBytesAndCombinesAliases(t *testing.T) {
	r := replacementRepo(t, map[string]string{"page.tsx": "<h1>Résumé</h1>\r\nconst hue = 'blue';", "style.css": "button { color: blue }\n"})
	paths, err := r.ReplaceText([]Replacement{{"page.tsx", "Résumé", "Atlas"}, {"./page.tsx", "'blue'", "'green'"}, {"style.css", "blue", "green"}})
	if err != nil || len(paths) != 2 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	text, _ := r.ReadFile("page.tsx")
	if text != "<h1>Atlas</h1>\r\nconst hue = 'green';" {
		t.Fatalf("bytes changed unexpectedly: %q", text)
	}
}

func TestReplaceTextRejectsEntireInvalidBatch(t *testing.T) {
	for name, edit := range map[string]Replacement{
		"ambiguous":       {"b.ts", "blue", "green"},
		"absent":          {"b.ts", "missing", "green"},
		"empty_old":       {"b.ts", "", "green"},
		"same_value":      {"b.ts", "blue", "blue"},
		"outside":         {"../b.ts", "blue", "green"},
		"missing_file":    {"missing.ts", "blue", "green"},
		"oversized_value": {"b.ts", "blue", strings.Repeat("x", 4097)},
	} {
		t.Run(name, func(t *testing.T) {
			r := replacementRepo(t, map[string]string{"a.ts": "red", "b.ts": "blue blue"})
			_, err := r.ReplaceText([]Replacement{{"a.ts", "red", "green"}, edit})
			if err == nil {
				t.Fatal("invalid batch accepted")
			}
			a, _ := r.ReadFile("a.ts")
			b, _ := r.ReadFile("b.ts")
			if a != "red" || b != "blue blue" {
				t.Fatal("part of rejected batch was written")
			}
		})
	}
}

func TestReplaceTextRejectsEmptyFileAndNoNetChange(t *testing.T) {
	r := replacementRepo(t, map[string]string{"a.ts": "red"})
	for _, edits := range [][]Replacement{{{"a.ts", "red", ""}}, {{"a.ts", "red", "blue"}, {"a.ts", "blue", "red"}}, nil} {
		if _, err := r.ReplaceText(edits); err == nil {
			t.Fatal("empty/no-op change accepted")
		}
	}
	text, _ := r.ReadFile("a.ts")
	if text != "red" {
		t.Fatal("rejected request changed the file")
	}
}

func TestReplaceTextRejectsSymlinkAliases(t *testing.T) {
	r := replacementRepo(t, map[string]string{"a.ts": "red blue"})
	if err := os.Symlink("a.ts", filepath.Join(r.Root, "link.ts")); err != nil {
		t.Fatal(err)
	}
	_, err := r.ReplaceText([]Replacement{{"a.ts", "red", "green"}, {"link.ts", "blue", "yellow"}})
	if err == nil {
		t.Fatal("symlink alias accepted")
	}
	text, _ := r.ReadFile("a.ts")
	if text != "red blue" {
		t.Fatal("partial batch was written")
	}
}
