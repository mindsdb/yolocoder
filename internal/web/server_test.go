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
