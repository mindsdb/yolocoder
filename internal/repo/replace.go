package repo

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Replacement struct {
	Path string `json:"path"`
	Old  string `json:"old_text"`
	New  string `json:"new_text"`
}

// ReplaceText validates every substitution before writing any file. Matches
// are literal and unique; unlike patch placement there is no fuzzy matching.
// Multiple substitutions in a file are interpreted in the supplied order.
func (repository *Repository) ReplaceText(edits []Replacement) ([]string, error) {
	if len(edits) == 0 || len(edits) > 8 {
		return nil, fmt.Errorf("replace_text accepts 1-8 substitutions")
	}
	root, err := filepath.EvalSymlinks(repository.Root)
	if err != nil {
		return nil, err
	}
	updated := map[string]string{}
	originals := map[string]string{}
	var paths []string
	for _, edit := range edits {
		edit.Path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(edit.Path)))
		if edit.Old == "" || edit.Old == edit.New || len(edit.Old) > 4096 || len(edit.New) > 4096 {
			return nil, fmt.Errorf("%s: supply a nonempty old_text and different new_text, each at most 4096 bytes", edit.Path)
		}
		content, ok := updated[edit.Path]
		if !ok {
			if !repository.Exists(edit.Path) {
				return nil, fmt.Errorf("%s: expected an existing file", edit.Path)
			}
			full := filepath.Join(root, filepath.FromSlash(edit.Path))
			resolved, err := filepath.EvalSymlinks(full)
			if err != nil || resolved != full {
				return nil, fmt.Errorf("%s: replace_text requires a regular path without symlinks", edit.Path)
			}
			content, err = repository.ReadFile(edit.Path)
			if err != nil {
				return nil, err
			}
			if len(content) > 256<<10 {
				return nil, fmt.Errorf("%s: file exceeds 256 KiB", edit.Path)
			}
			paths = append(paths, edit.Path)
			originals[edit.Path] = content
		}
		at := strings.Index(content, edit.Old)
		if at < 0 {
			return nil, fmt.Errorf("%s: matching text was not found exactly", edit.Path)
		}
		if strings.Contains(content[at+1:], edit.Old) {
			return nil, fmt.Errorf("%s: matching text occurs more than once; make the exact match unique", edit.Path)
		}
		content = strings.Replace(content, edit.Old, edit.New, 1)
		if strings.TrimSpace(content) == "" || len(content) > 256<<10 {
			return nil, fmt.Errorf("%s: replacement would empty or oversize the file", edit.Path)
		}
		updated[edit.Path] = content
	}
	var changed []string
	for _, path := range paths {
		if updated[path] != originals[path] {
			changed = append(changed, path)
		}
	}
	if len(changed) == 0 {
		return nil, fmt.Errorf("replacements changed nothing")
	}
	for _, path := range changed {
		if err := repository.Write(path, updated[path]); err != nil {
			return nil, err
		}
	}
	return changed, nil
}
