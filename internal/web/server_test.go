package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
