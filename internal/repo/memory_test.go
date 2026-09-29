package repo

import (
	"context"
	"os"
	"strings"
	"testing"
)

func memoryProject() *Repository {
	return NewMemory(map[string]string{
		"src/App.tsx":         "export default function App() {\n  return <h1>Hello</h1>;\n}\n",
		"src/index.css":       "body { margin: 0; }\n",
		"ARCHITECTURE.md":     "A frontend only app.\n",
		"node_modules/x/a.js": "ignored\n",
	})
}

// The whole point of a repository in memory: a patch lands in the map,
// Changed reports it, and the folder the process happens to run in is
// never looked at, let alone written.
func TestMemoryPatchChangesOnlyTheMap(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	repository := memoryProject()

	err := repository.Apply("@src/App.tsx\n-  return <h1>Hello</h1>;\n+  return <h1>Hi</h1>;\n\n@+src/Game.ts\nexport const speed = 3;\n")
	if err != nil {
		t.Fatal(err)
	}
	changed := repository.Changed()
	if len(changed) != 2 || !strings.Contains(changed["src/App.tsx"], "<h1>Hi</h1>") || changed["src/Game.ts"] != "export const speed = 3;" {
		t.Fatalf("Changed() = %#v", changed)
	}
	if entries, _ := os.ReadDir(directory); len(entries) != 0 {
		t.Fatalf("a repository in memory wrote to the working folder: %v", entries)
	}
}

// A unified diff has no git to go to in memory, so it is placed by
// content like every other format.
func TestMemoryAppliesAUnifiedDiffByContent(t *testing.T) {
	repository := memoryProject()
	err := repository.Apply("--- a/src/index.css\n+++ b/src/index.css\n@@ -1 +1 @@\n-body { margin: 0; }\n+body { margin: 0; padding: 0; }\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := repository.Changed()["src/index.css"]; got != "body { margin: 0; padding: 0; }\n" {
		t.Fatalf("src/index.css = %q", got)
	}
}

func TestMemoryRefusesPathsOutsideTheProject(t *testing.T) {
	repository := memoryProject()
	for _, path := range []string{"../escape.txt", "/etc/passwd", "..\\escape.txt", "."} {
		if err := repository.Write(path, "x"); err == nil {
			t.Fatalf("Write(%q) succeeded", path)
		}
		if _, err := repository.ReadFile(path); err == nil {
			t.Fatalf("ReadFile(%q) succeeded", path)
		}
	}
	if len(repository.Changed()) != 0 {
		t.Fatalf("a refused write changed something: %v", repository.Changed())
	}
}

func TestMemoryMapSearchAndArchitecture(t *testing.T) {
	repository := memoryProject()
	mapText, err := repository.Map()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mapText, "src/App.tsx") || strings.Contains(mapText, "node_modules") {
		t.Fatalf("map = %q", mapText)
	}
	found, err := repository.Search(context.Background(), "h1")
	if err != nil || found != "src/App.tsx:2:  return <h1>Hello</h1>;" {
		t.Fatalf("search = %q, %v", found, err)
	}
	if name, text := repository.Architecture(); name != "ARCHITECTURE.md" || text != "A frontend only app.\n" {
		t.Fatalf("architecture = %q, %q", name, text)
	}
	read, err := repository.Read([]string{"src/index.css"})
	if err != nil || read != "--- src/index.css ---\nbody { margin: 0; }\n\n" {
		t.Fatalf("read = %q, %v", read, err)
	}
	if _, err := repository.Read([]string{"missing.ts"}); err == nil {
		t.Fatal("reading a missing file succeeded")
	}
}

func TestMemoryReplaceText(t *testing.T) {
	repository := memoryProject()
	paths, err := repository.ReplaceText([]Replacement{{Path: "src/index.css", Old: "margin: 0", New: "margin: 4px"}})
	if err != nil || len(paths) != 1 {
		t.Fatalf("ReplaceText = %v, %v", paths, err)
	}
	if got := repository.Changed()["src/index.css"]; got != "body { margin: 4px; }\n" {
		t.Fatalf("src/index.css = %q", got)
	}
}

// A model rewriting a file writes the new one faithfully and invents the
// old lines it is meant to remove. In memory, @+ over an existing file
// is simply its new contents, so there are no old lines to get wrong.
func TestMemoryWholeFileWriteReplacesAnExistingFile(t *testing.T) {
	repository := memoryProject()
	err := repository.Apply("@+src/App.tsx\nexport default function App() {\n  return <p>rewritten</p>;\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := repository.Changed()["src/App.tsx"]; got != "export default function App() {\n  return <p>rewritten</p>;\n}" {
		t.Fatalf("src/App.tsx = %q", got)
	}
}

// On disk a whole-file write over an existing file stays refused, as it
// was before memory repositories existed.
func TestAWholeFileWriteOverAFileOnDiskIsStillRefused(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(root+"/a.txt", []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &Repository{Root: root}
	if err := repository.Apply("@+a.txt\nreplaced\n"); err == nil {
		t.Fatal("a whole-file write replaced a file on disk")
	}
	if data, _ := os.ReadFile(root + "/a.txt"); string(data) != "keep\n" {
		t.Fatalf("a.txt = %q", data)
	}
}
