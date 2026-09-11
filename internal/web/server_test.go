package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigReportsTheAppProxyPortAndHistoryIsGone(t *testing.T) {
	server := &Server{root: t.TempDir(), hub: newHub(), appProxyPort: 54321}
	mux := http.NewServeMux()
	server.routes(mux)

	request := httptest.NewRequest(http.MethodGet, "/config", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("/config: got status %d", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "54321") {
		t.Fatalf("/config should report the app proxy port, got %q", body)
	}

	// The chat pane no longer replays a folder's whole recorded backlog
	// as a transcript (see the comment on handleConfig): there is nothing
	// left at /history to serve it from.
	request = httptest.NewRequest(http.MethodGet, "/history", nil)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("/history should be gone, got status %d", recorder.Code)
	}
}

func TestPrepareProjectRejectsAForeignExistingProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := &Server{root: dir, hub: newHub()}
	if err := server.prepareProject(context.Background()); err == nil {
		t.Fatal("a non-empty folder yolocoder didn't create should be refused, not adopted")
	}
}

func TestPrepareProjectRestoresScriptsForAnExistingYolocoderProject(t *testing.T) {
	dir := t.TempDir()
	if err := scaffoldProject(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "scripts")); err != nil {
		t.Fatal(err)
	}
	server := &Server{root: dir, hub: newHub()}
	if err := server.prepareProject(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !scriptsExist(dir) {
		t.Fatal("prepareProject should have restored the missing scripts")
	}
}

func TestStateReflectsSetBusyAndSetPhase(t *testing.T) {
	dir := t.TempDir()
	hub := newHub()
	server := &Server{root: dir, hub: hub, proc: newProcess(dir, hub, newErrorWatcher(func(string) {}))}
	mux := http.NewServeMux()
	server.routes(mux)

	get := func() string {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/state", nil))
		return recorder.Body.String()
	}

	if body := get(); !strings.Contains(body, `"busy":false`) {
		t.Fatalf("expected busy:false initially, got %q", body)
	}

	server.setBusy(true)
	server.setPhase("build")
	body := get()
	if !strings.Contains(body, `"busy":true`) {
		t.Fatalf("expected busy:true after setBusy(true), got %q", body)
	}
	if !strings.Contains(body, `"phase":"build"`) {
		t.Fatalf("expected phase:build after setPhase(\"build\"), got %q", body)
	}

	server.setBusy(false)
	if body := get(); !strings.Contains(body, `"busy":false`) {
		t.Fatalf("expected busy:false after setBusy(false), got %q", body)
	}
}

func TestLineStreamerSplitsAcrossArbitraryChunkBoundaries(t *testing.T) {
	var lines []string
	streamer := &lineStreamer{onLine: func(line string) { lines = append(lines, line) }}

	// Two lines delivered in three writes that don't line up with the
	// newlines at all, the way a pipe's Write calls actually arrive.
	streamer.Write([]byte("first li"))
	streamer.Write([]byte("ne\r\nsecond"))
	streamer.Write([]byte(" line\n"))

	want := []string{"first line", "second line"}
	if len(lines) != len(want) {
		t.Fatalf("got %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("got %v, want %v", lines, want)
		}
	}
}

func TestLineStreamerFlushReportsATrailingPartialLine(t *testing.T) {
	var lines []string
	streamer := &lineStreamer{onLine: func(line string) { lines = append(lines, line) }}
	streamer.Write([]byte("no trailing newline"))
	if len(lines) != 0 {
		t.Fatal("a line with no terminator yet shouldn't fire until flush")
	}
	streamer.flush()
	if len(lines) != 1 || lines[0] != "no trailing newline" {
		t.Fatalf("flush should report the buffered partial line, got %v", lines)
	}
}

func TestPrepareProjectLeavesAnIntactYolocoderProjectAlone(t *testing.T) {
	dir := t.TempDir()
	if err := scaffoldProject(dir); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "src", "App.tsx")
	original, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{root: dir, hub: newHub()}
	if err := server.prepareProject(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != string(after) {
		t.Fatal("prepareProject should not touch an existing yolocoder project's own files")
	}
}
