package repo

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// NewMemory is a repository with no folder behind it: the project is
// the files handed in, and every read, search and patch works on that
// map. Nothing touches the disk and nothing runs, which is what lets
// --web-fe take a stranger's project, edit it and hand it back without
// the server ever holding more than the request.
func NewMemory(files map[string]string) *Repository {
	copied := make(map[string]string, len(files))
	for name, content := range files {
		copied[name] = content
	}
	return &Repository{memory: copied, original: files}
}

// InMemory reports a repository made by NewMemory. There is no project
// check to run and no folder for anything else to look at, so callers
// that would shell out or stat a path ask this first.
func (repository *Repository) InMemory() bool {
	return repository.memory != nil
}

// Changed is every file whose contents differ from what NewMemory was
// given, created files included. Patches never delete, so this is the
// whole of what a turn did.
func (repository *Repository) Changed() map[string]string {
	changed := map[string]string{}
	for name, content := range repository.memory {
		if before, ok := repository.original[name]; !ok || before != content {
			changed[name] = content
		}
	}
	return changed
}

// memoryPath is safePath for the map: the same answer to "is this inside
// the project", keyed the way the map is.
func memoryPath(name string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q is outside the repository", name)
	}
	return clean, nil
}

func (repository *Repository) memoryMap() string {
	var paths []string
	for name := range repository.memory {
		if !memoryIgnored(name) {
			paths = append(paths, name)
		}
	}
	sort.Strings(paths)
	var mapText strings.Builder
	for _, name := range paths {
		fmt.Fprintf(&mapText, "%-60s %s\n", name, formatSize(int64(len(repository.memory[name]))))
	}
	return mapText.String()
}

func memoryIgnored(name string) bool {
	parts := strings.Split(name, "/")
	for _, dir := range parts[:len(parts)-1] {
		if ignoredDirectories[dir] {
			return true
		}
	}
	return parts[len(parts)-1] == ".DS_Store"
}

// memorySearch is searchWithoutRipgrep over the map, shaped the same
// "path:line:text" way for the same readers.
func (repository *Repository) memorySearch(pattern *regexp.Regexp) string {
	var paths []string
	for name := range repository.memory {
		if !memoryIgnored(name) {
			paths = append(paths, name)
		}
	}
	sort.Strings(paths)
	var out strings.Builder
	for _, name := range paths {
		data := repository.memory[name]
		if len(data) > maxReadBytes || strings.IndexByte(data, 0) >= 0 {
			continue
		}
		for number, line := range strings.Split(data, "\n") {
			if pattern.MatchString(line) {
				fmt.Fprintf(&out, "%s:%d:%s\n", name, number+1, line)
				if out.Len() > maxSearchOut {
					out.WriteString("\n... output truncated ...\n")
					return strings.TrimSpace(out.String())
				}
			}
		}
	}
	if out.Len() == 0 {
		return "No matches."
	}
	return strings.TrimSpace(out.String())
}
