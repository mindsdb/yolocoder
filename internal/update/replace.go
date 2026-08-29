package update

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func replaceSelf(path string, content []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".yolocoder-update-*")
	if err != nil {
		return fmt.Errorf("create update file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return fmt.Errorf("write update: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close update: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o755); err != nil {
		return fmt.Errorf("set update permissions: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return fmt.Errorf("install update: %w", err)
	}
	oldPath := path + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(path, oldPath); err != nil {
		return fmt.Errorf("move current binary: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Rename(oldPath, path)
		return fmt.Errorf("install update: %w", err)
	}
	return nil
}
