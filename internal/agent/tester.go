package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxTestOutput = 128 << 10

type TestResult struct {
	Passed bool
	// Skipped reports that no test command was detected, so Passed is
	// true only because there was nothing to run.
	Skipped bool
	Output  string
}

func RunTests(ctx context.Context, root string) TestResult {
	name, args := detectTestCommand(root)
	if name == "" {
		return TestResult{Passed: true, Skipped: true, Output: "No supported test command detected."}
	}
	testCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	command := exec.CommandContext(testCtx, name, args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if len(output) > maxTestOutput {
		output = append(output[:maxTestOutput], []byte("\n... output truncated ...\n")...)
	}
	result := fmt.Sprintf("$ %s %s\n%s", name, strings.Join(args, " "), output)
	if testCtx.Err() == context.DeadlineExceeded {
		return TestResult{Passed: false, Output: result + "\nTests timed out."}
	}
	return TestResult{Passed: err == nil, Output: result}
}

func detectTestCommand(root string) (string, []string) {
	if exists(root, "go.mod") {
		return "go", []string{"test", "./..."}
	}
	if exists(root, "Cargo.toml") {
		return "cargo", []string{"test"}
	}
	if exists(root, "package.json") {
		switch {
		case exists(root, "pnpm-lock.yaml"):
			return "pnpm", []string{"test"}
		case exists(root, "yarn.lock"):
			return "yarn", []string{"test"}
		default:
			return "npm", []string{"test"}
		}
	}
	if exists(root, "pyproject.toml") || exists(root, "pytest.ini") {
		return "pytest", nil
	}
	return "", nil
}

func exists(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && !info.IsDir()
}
