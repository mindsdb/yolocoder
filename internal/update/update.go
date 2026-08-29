package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mindsdb/yolocoder/internal/ui"
)

const (
	disableEnv     = "YOLOCODER_NO_AUTOUPDATE"
	checkInterval  = 24 * time.Hour
	defaultBaseURL = "https://github.com/mindsdb/yolocoder/releases/download/latest"
	throttleChecks = false
)

type Checker struct {
	Dir     string
	Client  *http.Client
	Getenv  func(string) string
	BaseURL string
}

func CheckOnLaunch(currentCommit string, status ui.RobotStatus) {
	_, _, err := check(currentCommit, false, status)
	if err != nil {
		debugf("%v", err)
	}
}

func CheckNow(currentCommit string, status ui.RobotStatus) (string, bool, error) {
	if currentCommit == "" {
		return "", false, fmt.Errorf("self-update is unavailable in a development build")
	}
	if os.Getenv(disableEnv) != "" {
		return "", false, fmt.Errorf("self-update is disabled by %s", disableEnv)
	}
	latest, updated, err := check(currentCommit, true, status)
	return shortCommit(latest), updated, err
}

func check(currentCommit string, force bool, status ui.RobotStatus) (string, bool, error) {
	if currentCommit == "" || os.Getenv(disableEnv) != "" {
		return "", false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("find home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "yolocoder")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("create config directory: %w", err)
	}
	checker := &Checker{Dir: dir}
	now := time.Now()
	if !force && !checker.Due(now) {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	status("Checking for updates...")
	latest, err := checker.LatestCommit(ctx)
	cancel()
	_ = checker.MarkChecked(now)
	if err != nil {
		return "", false, fmt.Errorf("check for update: %w", err)
	}
	if latest == currentCommit {
		return latest, false, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return latest, false, fmt.Errorf("locate executable: %w", err)
	}
	status("Updating YoloCoder...")
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := checker.Apply(ctx, executable); err != nil {
		if !force {
			status("Update failed; starting current version...")
		}
		return latest, false, fmt.Errorf("apply update: %w", err)
	}
	if !force {
		status("Updated; starting YoloCoder...")
	}
	return latest, true, nil
}

func (checker *Checker) Due(now time.Time) bool {
	getenv := checker.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if getenv(disableEnv) != "" {
		return false
	}
	if !throttleChecks {
		return true
	}
	data, err := os.ReadFile(checker.statePath())
	if err != nil {
		return true
	}
	lastCheck, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	return err != nil || now.Sub(lastCheck) >= checkInterval
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func debugf(format string, args ...any) {
	if os.Getenv("YOLOCODER_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "debug: self-update: "+format+"\n", args...)
	}
}

func (checker *Checker) MarkChecked(now time.Time) error {
	return os.WriteFile(checker.statePath(), []byte(now.UTC().Format(time.RFC3339)), 0o644)
}

func (checker *Checker) LatestCommit(ctx context.Context) (string, error) {
	content, err := checker.get(ctx, "commit.txt", 1<<20)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(content))
	if commit == "" {
		return "", fmt.Errorf("commit.txt is empty")
	}
	return commit, nil
}

func (checker *Checker) Apply(ctx context.Context, executable string) error {
	asset, err := Asset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	archive, err := checker.get(ctx, asset, 200<<20)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	checksums, err := checker.get(ctx, "checksums.txt", 1<<20)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	if err := verifyChecksum(archive, asset, checksums); err != nil {
		return err
	}
	binaryName := "yolocoder"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binary, err := extract(archive, asset, binaryName)
	if err != nil {
		return err
	}
	return replaceSelf(executable, binary)
}

func Asset(goos, goarch string) (string, error) {
	osNames := map[string]string{"darwin": "Darwin", "linux": "Linux", "windows": "Windows"}
	archNames := map[string]string{"amd64": "x86_64", "arm64": "arm64"}
	osName, ok := osNames[goos]
	if !ok {
		return "", fmt.Errorf("unsupported OS: %s", goos)
	}
	archName, ok := archNames[goarch]
	if !ok {
		return "", fmt.Errorf("unsupported architecture: %s", goarch)
	}
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	return "yolocoder_" + osName + "_" + archName + extension, nil
}

func (checker *Checker) statePath() string {
	return filepath.Join(checker.Dir, "last-update-check")
}

func (checker *Checker) get(ctx context.Context, name string, limit int64) ([]byte, error) {
	baseURL := checker.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	client := checker.Client
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", request.URL, response.Status)
	}
	return io.ReadAll(io.LimitReader(response.Body, limit))
}

func verifyChecksum(content []byte, asset string, checksums []byte) error {
	var expected string
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			expected = fields[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("%s is not listed in checksums.txt", asset)
	}
	sum := sha256.Sum256(content)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s", asset)
	}
	return nil
}

func extract(content []byte, asset, binaryName string) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
		if err != nil {
			return nil, err
		}
		for _, file := range archive.File {
			if file.Name == binaryName {
				reader, err := file.Open()
				if err != nil {
					return nil, err
				}
				defer reader.Close()
				return io.ReadAll(reader)
			}
		}
	} else {
		gzipReader, err := gzip.NewReader(bytes.NewReader(content))
		if err != nil {
			return nil, err
		}
		defer gzipReader.Close()
		tarReader := tar.NewReader(gzipReader)
		for {
			header, err := tarReader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if header.Name == binaryName {
				return io.ReadAll(tarReader)
			}
		}
	}
	return nil, fmt.Errorf("%s not found in archive", binaryName)
}
