package update

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A release is downloaded once, verified, cached, and run with self-update
// off and the caller's arguments.
func TestAReleaseIsDownloadedOnceAndRunWithoutUpdating(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture binary is a shell script")
	}
	asset, err := Asset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	out := filepath.Join(t.TempDir(), "ran")
	archive := tarGz(t, "yolocoder", []byte("#!/bin/sh\necho \"$"+disableEnv+" $@\" > \"$RELEASE_OUT\"\n"))
	sum := sha256.Sum256(archive)
	downloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v0.0.0-test/" + asset:
			downloads++
			_, _ = writer.Write(archive)
		case "/v0.0.0-test/checksums.txt":
			_, _ = writer.Write([]byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	previous := releasesURL
	releasesURL = server.URL + "/"
	defer func() { releasesURL = previous }()
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("RELEASE_OUT", out)

	for range 2 {
		binary, err := ReleaseBinary("v0.0.0-test", func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		if code, err := Run(binary, "v0.0.0-test", []string{"--web", "hi"}); err != nil || code != 0 {
			t.Fatalf("code=%d err=%v", code, err)
		}
	}
	if downloads != 1 {
		t.Fatalf("downloads = %d, want the cached copy the second time", downloads)
	}
	ran, _ := os.ReadFile(out)
	if strings.TrimSpace(string(ran)) != "1 --web hi" {
		t.Fatalf("release ran as %q, want self-update off and the arguments passed on", ran)
	}
}

func TestAReleaseTagMustBeATag(t *testing.T) {
	for _, tag := range []string{"latest", "../etc", "a/b", ""} {
		if _, err := ReleaseBinary(tag, func(string) {}); err == nil {
			t.Fatalf("%q was accepted as a release tag", tag)
		}
	}
}
