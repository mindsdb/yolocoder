package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

// releasesURL is where a tagged release's assets are published. A
// variable only so tests can serve releases of their own.
var releasesURL = "https://github.com/mindsdb/yolocoder/releases/download/"

// releaseTag is what a tag may look like: letters, digits and . _ -, so
// it can name a directory and a URL path without escaping either.
var releaseTag = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ReleaseBinary is the path of release tag's binary for this machine,
// downloading it first when it is not cached yet. It is checked against
// the release's checksums and kept under the user cache directory, so a
// second run starts it straight away.
func ReleaseBinary(tag string, status func(string)) (string, error) {
	if !releaseTag.MatchString(tag) {
		return "", fmt.Errorf("%q is not a release tag", tag)
	}
	if tag == "latest" {
		return "", fmt.Errorf("latest is the build yolocoder already updates to; run yolocoder without --release")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find cache directory: %w", err)
	}
	name := "yolocoder"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(cache, "yolocoder", "releases", tag, name)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create release cache: %w", err)
	}
	status("Downloading release " + tag + "...")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	checker := &Checker{BaseURL: releasesURL + tag}
	if err := checker.Apply(ctx, path); err != nil {
		return "", fmt.Errorf("download release %s: %w", tag, err)
	}
	return path, nil
}

// Run runs a release's binary with args in place of this build and
// returns its exit code. It runs with self-update off: an older build
// would otherwise see it is behind latest on its first launch and replace
// itself, which is the opposite of asking for it.
func Run(binary, tag string, args []string) (int, error) {
	command := exec.Command(binary, args...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.Env = append(os.Environ(), disableEnv+"=1")
	if err := command.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, fmt.Errorf("run release %s: %w", tag, err)
	}
	return 0, nil
}
