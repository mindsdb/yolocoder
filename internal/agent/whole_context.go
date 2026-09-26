package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	wholeContextMaxFiles = 24
	// Each completed read respects the ordinary read_files path limit and
	// consumes one of its existing calls; this does not enlarge any quota.
	wholeContextReadFiles = 12
)

// completeWholeContext supplies all mapped, supported project text only when
// the entire eligible set fits. It is called only for confident normal routes;
// failure leaves read state untouched for the existing ranked prefetch.
func (runner *Runner) completeWholeContext(ctx context.Context, session *changeSession, mapped []string) bool {
	var paths []string
	for _, path := range mapped {
		clean := filepath.Clean(filepath.FromSlash(path))
		if !utf8.ValidString(path) || !filepath.IsLocal(clean) || filepath.ToSlash(clean) != path {
			return false
		}
		if wholeContextPath(path) {
			paths = appendUnique(paths, path)
			if len(paths) > wholeContextMaxFiles {
				return false
			}
		}
	}
	reads := (len(paths) + wholeContextReadFiles - 1) / wholeContextReadFiles
	if reads == 0 || session.used["read_files"]+reads > toolQuota["read_files"] || ctx.Err() != nil {
		return false
	}
	sort.Strings(paths)
	root, err := os.OpenRoot(runner.repository.Root)
	if err != nil {
		return false
	}
	defer root.Close()

	// Stage both reads before committing any transcript or served snapshots.
	// The byte limit includes exactly the path framing shown to the model.
	snapshots := make(map[string]string, len(paths))
	outputs := make([]strings.Builder, reads)
	total := 0
	for i, path := range paths {
		if ctx.Err() != nil {
			return false
		}
		content, ok := readWholeContextFile(root, path)
		if !ok {
			return false
		}
		framed := fmt.Sprintf("--- %s ---\n%s\n", path, content)
		total += len(framed)
		if total > prefetchMaxBytes {
			return false
		}
		outputs[i/wholeContextReadFiles].WriteString(framed)
		snapshots[path] = content
	}
	if ctx.Err() != nil {
		return false
	}
	for i := range outputs {
		end := min((i+1)*wholeContextReadFiles, len(paths))
		arguments, _ := json.Marshal(map[string]any{"paths": paths[i*wholeContextReadFiles : end], "matching": ""})
		call := responseItem{Type: "function_call", Name: "read_files", CallID: fmt.Sprintf("whole_context_%d", i+1), Arguments: string(arguments)}
		session.transcript = append(session.transcript, call,
			toolOutput{Type: "function_call_output", CallID: call.CallID, Output: outputs[i].String()})
	}
	session.used["read_files"] += reads
	session.readPaths = append(session.readPaths, paths...)
	for path, content := range snapshots {
		runner.served[path] = content
	}
	return true
}

// This is complete supported text context, not a claim to read every repository
// file. Generated dependencies, locks, caches, minified files and binary formats
// are deliberately outside the set. Other formats remain available to normal
// model-requested reads.
func wholeContextPath(path string) bool {
	parts := strings.Split(path, "/")
	for _, dir := range parts[:len(parts)-1] {
		switch dir {
		case ".git", ".yolocoder", "node_modules", "vendor", "dist", "build", ".next", ".nuxt", "target", "__pycache__", ".venv", "venv", ".cache", ".idea", ".vscode", ".pytest_cache", ".mypy_cache", ".tox":
			return false
		}
	}
	name := strings.ToLower(parts[len(parts)-1])
	if strings.HasSuffix(name, ".lock") || strings.Contains(name, "-lock.") || name == "lock.json" || strings.Contains(name, ".min.") {
		return false
	}
	switch name {
	case ".gitignore", ".gitattributes", ".editorconfig":
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".css", ".scss", ".sass", ".less", ".html", ".htm", ".vue", ".svelte", ".json", ".jsonc", ".yaml", ".yml", ".toml", ".ini", ".conf", ".md", ".mdx", ".txt", ".sh", ".bash", ".zsh":
		return true
	}
	return false
}

// The rooted handle prevents reads from escaping the project. Reject symlinks
// in every component and require the opened, bounded regular file to still
// match its path after reading. This is a snapshot, not a filesystem lock.
func readWholeContextFile(root *os.Root, path string) (string, bool) {
	before, ok := wholeContextFileInfo(root, path)
	if !ok {
		return "", false
	}
	file, err := root.Open(filepath.FromSlash(path))
	if err != nil {
		return "", false
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return "", false
	}
	content, err := io.ReadAll(io.LimitReader(file, prefetchMaxFileBytes+1))
	if err != nil || len(content) > prefetchMaxFileBytes || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return "", false
	}
	after, ok := wholeContextFileInfo(root, path)
	if !ok || !os.SameFile(opened, after) || before.Size() != int64(len(content)) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", false
	}
	return string(content), true
}

func wholeContextFileInfo(root *os.Root, path string) (os.FileInfo, bool) {
	parts := strings.Split(path, "/")
	var info os.FileInfo
	for i := range parts {
		var err error
		info, err = root.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) {
			return nil, false
		}
	}
	return info, info.Mode().IsRegular() && info.Size() <= prefetchMaxFileBytes
}
