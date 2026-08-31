package repo

import (
	"bytes"
	"context"
	"fmt"
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

func Open(path string) (*Repository, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git is required but was not found on PATH")
	}
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

	root, err := repositoryRoot(absolutePath)
	if err == nil {
		return &Repository{Root: root}, nil
	}

	command := exec.Command("git", "init", "-q")
	command.Dir = absolutePath
	if output, initErr := command.CombinedOutput(); initErr != nil {
		return nil, fmt.Errorf("initialize Git repository: %w: %s", initErr, strings.TrimSpace(string(output)))
	}
	root, err = repositoryRoot(absolutePath)
	if err != nil {
		return nil, err
	}
	return &Repository{Root: root}, nil
}

func repositoryRoot(path string) (string, error) {
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	command.Dir = path
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("find Git repository: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func (repository *Repository) Map() (string, error) {
	command := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	command.Dir = repository.Root
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("map repository: %w", err)
	}
	paths := strings.Split(string(output), "\x00")
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

func (repository *Repository) Apply(patch string) error {
	if strings.TrimSpace(patch) == "" {
		return fmt.Errorf("patch is empty")
	}
	for _, args := range [][]string{{"apply", "--check", "-"}, {"apply", "-"}} {
		command := exec.Command("git", args...)
		command.Dir = repository.Root
		command.Stdin = strings.NewReader(patch)
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
		}
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
