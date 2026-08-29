package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectTestCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, args := detectTestCommand(root)
	if name != "go" || len(args) != 2 || args[0] != "test" || args[1] != "./..." {
		t.Fatalf("detectTestCommand() = %q %v", name, args)
	}
}
