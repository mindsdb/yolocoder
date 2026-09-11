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

// yolocoderMarker identifies a folder as one yolocoder --web scaffolded
// itself, as opposed to some other existing project. It's plain JSON
// (rather than something under the gitignored .yolocoder/ state
// directory) because it's meant to be committed: it's how a folder keeps
// identifying itself as a yolocoder project across clones and machines.
const yolocoderMarker = "yolocoder.json"

// isYolocoderProject reports whether root was scaffolded by yolocoder
// --web. For now, that's the only kind of non-empty folder --web will
// work in: an arbitrary existing project's dev command is a guess an
// agent has to make and could get wrong in a way that's confusing to
// debug from inside the very UI meant to help debug it, so that's out of
// scope until there's a more reliable way to establish it.
func isYolocoderProject(root string) bool {
	_, err := os.Stat(filepath.Join(root, yolocoderMarker))
	return err == nil
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

// restoreScripts recopies just the scripts/ subtree from the embedded
// scaffold template, for a yolocoder project whose scripts have gone
// missing. It only ever touches scripts/: the rest of a yolocoder project
// is meant to be built on by the agent (and by hand), so nothing else
// here is ours to overwrite.
func restoreScripts(root string) error {
	sub, err := fs.Sub(scratchTemplate, scratchTemplateRoot+"/scripts")
	if err != nil {
		return err
	}
	destination := filepath.Join(root, "scripts")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := fs.ReadFile(sub, entry.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), data, 0o755); err != nil {
			return err
		}
	}
	return nil
}
