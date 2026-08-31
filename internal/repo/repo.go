package repo

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	maxReadFiles = 12
	maxReadBytes = 256 << 10
	maxSearchOut = 64 << 10
)

// ignoredDirectories filters the plain-folder map fallback used when a
// folder has no Git repository of its own to ask for a .gitignore-aware
// listing.
var ignoredDirectories = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	".next": true, ".nuxt": true, "target": true, "__pycache__": true, ".venv": true,
	"venv": true, ".idea": true, ".vscode": true, ".cache": true, ".pytest_cache": true,
	".mypy_cache": true, ".tox": true,
}

type Repository struct {
	Root string
}

func (repository *Repository) Exists(path string) bool {
	fullPath, err := repository.safePath(path)
	if err != nil {
		return false
	}
	info, err := os.Stat(fullPath)
	return err == nil && info.Mode().IsRegular()
}

// Open uses path directly as the repository root. No Git repository is
// required: YoloCoder can map and patch a plain folder. If path already has
// its own .git, it's used opportunistically for a .gitignore-aware map and
// faster patch checks. Unlike `git rev-parse --show-toplevel`, this never
// searches parent directories, so it can't silently adopt an unrelated
// ancestor repository (for example the user's home directory).
func Open(path string) (*Repository, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("open working directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("working path is not a directory: %s", absolutePath)
	}
	return &Repository{Root: absolutePath}, nil
}

func (repository *Repository) hasGit() bool {
	_, err := os.Stat(filepath.Join(repository.Root, ".git"))
	return err == nil
}

func (repository *Repository) Map() (string, error) {
	if repository.hasGit() {
		if mapText, err := repository.gitMap(); err == nil {
			return mapText, nil
		}
	}
	return repository.walkMap()
}

func (repository *Repository) gitMap() (string, error) {
	command := repository.gitCommand("ls-files", "--cached", "--others", "--exclude-standard", "-z")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("map repository: %w", err)
	}
	return repository.formatMap(strings.Split(string(output), "\x00"))
}

// gitCommand builds a `git` invocation scoped strictly to repository.Root.
// GIT_CEILING_DIRECTORIES stops Git's own upward search for a repository at
// Root's parent: without it, a plain folder nested under an unrelated
// ancestor repository (the parent directory it was created in, say) lets
// Git silently adopt that ancestor's toplevel instead. `git apply` in
// particular then treats the patch's paths as relative to that unrelated
// toplevel, decides they fall outside the current directory, and silently
// skips every hunk (exit 0, no output, nothing written) instead of erroring.
func (repository *Repository) gitCommand(args ...string) *exec.Cmd {
	command := exec.Command("git", args...)
	command.Dir = repository.Root
	command.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(repository.Root))
	return command
}

func (repository *Repository) walkMap() (string, error) {
	var paths []string
	err := filepath.WalkDir(repository.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == repository.Root {
			return nil
		}
		if entry.IsDir() {
			if ignoredDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == ".DS_Store" {
			return nil
		}
		relative, err := filepath.Rel(repository.Root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("map repository: %w", err)
	}
	return repository.formatMap(paths)
}

func (repository *Repository) formatMap(paths []string) (string, error) {
	sort.Strings(paths)
	var mapText strings.Builder
	for _, path := range paths {
		if path == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(repository.Root, filepath.FromSlash(path)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		fmt.Fprintf(&mapText, "%-60s %s\n", path, formatSize(info.Size()))
	}
	return mapText.String(), nil
}

func (repository *Repository) Read(paths []string) (string, error) {
	if len(paths) == 0 || len(paths) > maxReadFiles {
		return "", fmt.Errorf("read_files accepts 1-%d paths", maxReadFiles)
	}
	var result strings.Builder
	total := 0
	for _, path := range paths {
		fullPath, err := repository.safePath(path)
		if err != nil {
			return "", err
		}
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		total += len(content)
		if total > maxReadBytes {
			return "", fmt.Errorf("requested files exceed %d KiB", maxReadBytes>>10)
		}
		fmt.Fprintf(&result, "--- %s ---\n%s\n", filepath.ToSlash(path), content)
	}
	return result.String(), nil
}

func (repository *Repository) Search(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("search query is required")
	}
	command := exec.CommandContext(ctx, "rg", "-n", "--hidden", "--glob", "!.git", "--", query, ".")
	command.Dir = repository.Root
	output, err := command.CombinedOutput()
	if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
		return "No matches.", nil
	}
	if err != nil {
		return "", fmt.Errorf("search: %w: %s", err, output)
	}
	if len(output) > maxSearchOut {
		output = append(output[:maxSearchOut], []byte("\n... output truncated ...\n")...)
	}
	return string(bytes.TrimSpace(output)), nil
}

// Apply patches files with `git apply`, which works against a plain folder
// without requiring an initialized repository. core.autocrlf is pinned off
// so the result doesn't depend on ambient Git config (system-wide, or from
// an unrelated ancestor repository). --recount tells git to infer each
// hunk's line counts from its body instead of trusting the @@ header, since
// a model-generated diff miscounting them (a common mistake, especially on
// longer hunks) otherwise fails with "corrupt patch at line N". --whitespace=fix
// silently normalizes trailing whitespace rather than erroring on it.
// --verbose makes a rejection report the exact text git searched for and
// could not find, which is the only part of the failure specific enough
// for the model to correct itself from.
func (repository *Repository) Apply(patch string) error {
	if strings.TrimSpace(patch) == "" {
		return fmt.Errorf("patch is empty")
	}
	applyArgs := []string{"-c", "core.autocrlf=false", "apply", "--recount", "--whitespace=fix", "--verbose"}
	for _, args := range [][]string{
		append(append([]string{}, applyArgs...), "--check", "-"),
		append(append([]string{}, applyArgs...), "-"),
	} {
		command := repository.gitCommand(args...)
		command.Stdin = strings.NewReader(patch)
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
		}
	}
	return nil
}

// Write replaces path's entire contents, creating it and any parent
// directories when needed and preserving an existing file's mode. It is
// the fallback for edits a unified diff can't express reliably: a diff
// only applies when its context and removed lines match the file byte for
// byte, which a model-written one often doesn't.
func (repository *Repository) Write(path, content string) error {
	fullPath, err := repository.safePath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(fullPath); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(fullPath, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func (repository *Repository) safePath(path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(path))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the repository", path)
	}
	fullPath := filepath.Join(repository.Root, clean)
	relative, err := filepath.Rel(repository.Root, fullPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the repository", path)
	}
	return fullPath, nil
}

func formatSize(size int64) string {
	if size < 1024 {
		return strconv.FormatInt(size, 10) + " B"
	}
	return fmt.Sprintf("%.1f KB", float64(size)/1024)
}
