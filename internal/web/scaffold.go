package web

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:template/scratch
var scratchTemplate embed.FS

const scratchTemplateRoot = "template/scratch"

// markers are files whose presence means "this is already a project of
// some kind," whatever stack it's in. Their absence, alongside an
// otherwise near-empty folder, is what makes a folder a from-scratch
// candidate; project files (README, .git, .gitignore, an editor's dotfile)
// don't count against it, since starting yolocoder --web in a folder
// that's otherwise a fresh git init is the common case, not an edge one.
var projectMarkers = []string{
	"package.json", "go.mod", "Cargo.toml", "pyproject.toml", "requirements.txt", "Gemfile",
}

var ignorableEntries = map[string]bool{
	".git": true, ".gitignore": true, ".github": true, "README.md": true, "readme.md": true,
	"LICENSE": true, ".DS_Store": true, ".yolocoder": true,
}

// looksEmpty reports whether root has no recognizable project in it yet,
// so --web should offer the from-scratch scaffold instead of asking the
// agent to find a dev command that doesn't exist.
func looksEmpty(root string) bool {
	for _, marker := range projectMarkers {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
			return false
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !ignorableEntries[entry.Name()] {
			return false
		}
	}
	return true
}

// scaffoldProject copies the embedded from-scratch template into root
// as-is. It's a plain file copy rather than model output: the stack is
// fixed (Vite + React + Tailwind + a couple of hand-written shadcn-style
// primitives on top, Express + Drizzle + SQLite underneath), so there is
// nothing for a model to decide, and boilerplate like this is exactly
// where a model is most likely to get some small, hard-to-spot config
// detail wrong.
func scaffoldProject(root string) error {
	sub, err := fs.Sub(scratchTemplate, scratchTemplateRoot)
	if err != nil {
		return err
	}
	return fs.WalkDir(sub, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		destination := filepath.Join(root, path)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if filepath.Dir(path) == "scripts" {
			mode = 0o755
		}
		return os.WriteFile(destination, data, mode)
	})
}
