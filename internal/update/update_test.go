package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAsset(t *testing.T) {
	tests := []struct {
		goos, goarch, expected string
	}{
		{"darwin", "arm64", "yolocoder_Darwin_arm64.tar.gz"},
		{"linux", "amd64", "yolocoder_Linux_x86_64.tar.gz"},
		{"windows", "amd64", "yolocoder_Windows_x86_64.zip"},
	}
	for _, test := range tests {
		actual, err := Asset(test.goos, test.goarch)
		if err != nil || actual != test.expected {
			t.Fatalf("Asset(%q, %q) = %q, %v", test.goos, test.goarch, actual, err)
		}
	}
}

func TestDue(t *testing.T) {
	now := time.Now()
	checker := &Checker{Dir: t.TempDir()}
	if !checker.Due(now) {
		t.Fatal("new checker should be due")
	}
	if err := checker.MarkChecked(now); err != nil {
		t.Fatal(err)
	}
	if !checker.Due(now.Add(time.Hour)) {
		t.Fatal("rolling builds should check on every launch")
	}
	checker.Getenv = func(string) string { return "1" }
	if checker.Due(now.Add(48 * time.Hour)) {
		t.Fatal("disabled checker should never be due")
	}
}

func TestShortCommit(t *testing.T) {
	if got := shortCommit("1234567890"); got != "1234567" {
		t.Fatalf("shortCommit() = %q", got)
	}
	if got := shortCommit("abc"); got != "abc" {
		t.Fatalf("shortCommit() = %q", got)
	}
}

func TestCheckNowRejectsDevelopmentBuild(t *testing.T) {
	if _, _, err := CheckNow("", func(string) {}); err == nil {
		t.Fatal("expected development build update to fail")
	}
}

func TestCheckNowHonorsDisableEnvironment(t *testing.T) {
	t.Setenv(disableEnv, "1")
	if _, _, err := CheckNow("current", func(string) {}); err == nil {
		t.Fatal("expected disabled update to fail")
	}
}

func TestApplyVerifiesAndReplacesBinary(t *testing.T) {
	asset, err := Asset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	binaryName := "yolocoder"
	if runtime.GOOS == "windows" {
		t.Skip("tar fixture only applies to Unix test runners")
	}
	wanted := []byte("new binary")
	archive := tarGz(t, binaryName, wanted)
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/" + asset:
			_, _ = writer.Write(archive)
		case "/checksums.txt":
			_, _ = writer.Write([]byte(checksums))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	executable := filepath.Join(t.TempDir(), binaryName)
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	checker := &Checker{BaseURL: server.URL, Client: server.Client()}
	if err := checker.Apply(context.Background(), executable); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(executable)
	if err != nil || !bytes.Equal(actual, wanted) {
		t.Fatalf("updated binary = %q, %v", actual, err)
	}
}

func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
