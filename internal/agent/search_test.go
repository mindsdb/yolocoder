package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

func folderWith(t *testing.T, files map[string]string) *Runner {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Runner{repository: &repo.Repository{Root: root}, served: map[string]string{}}
}

func TestPathsAreTakenFromRipgrepOutput(t *testing.T) {
	output := "./src/a.ts:3:const accent = 1\n" +
		"src/a.ts:9:use(accent)\n" +
		"src/b.ts:2:accent\n" +
		"No matches.\n" +
		"... output truncated ...\n"
	got := searchPaths(output)
	want := []string{"src/a.ts", "src/b.ts"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The round trip this removes: search, then read what was found.
func TestASearchHandsBackTheFilesItMatched(t *testing.T) {
	runner := folderWith(t, map[string]string{
		"src/a.ts": "const accent = \"red\"\n",
		"src/b.ts": "import { accent } from \"./a\"\n",
	})
	answer := runner.filesBehind("src/a.ts:1:const accent = \"red\"\nsrc/b.ts:1:import { accent }\n")

	for _, want := range []string{"src/a.ts", "src/b.ts", "const accent", "Do not call read_files"} {
		if !strings.Contains(answer, want) {
			t.Fatalf("answer missing %q:\n%s", want, answer)
		}
	}
}

// A search matching everything narrowed nothing, and sending it all
// costs more than the round trip it would save.
func TestAWideSearchHandsBackNothing(t *testing.T) {
	files := map[string]string{}
	var output strings.Builder
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		files["src/"+name+".ts"] = "accent\n"
		output.WriteString("src/" + name + ".ts:1:accent\n")
	}
	runner := folderWith(t, files)
	if answer := runner.filesBehind(output.String()); answer != "" {
		t.Fatalf("expected nothing for a five-file match, got:\n%s", answer)
	}
}

// Every copy of a file is resent on every following round, so one the
// model has already been shown is not sent again.
func TestASearchDoesNotResendAFileAlreadyShown(t *testing.T) {
	runner := folderWith(t, map[string]string{"src/a.ts": "accent\n"})
	if _, err := runner.readFiles([]string{"src/a.ts"}); err != nil {
		t.Fatal(err)
	}
	if answer := runner.filesBehind("src/a.ts:1:accent\n"); answer != "" {
		t.Fatalf("resent a file already shown:\n%s", answer)
	}
}

func TestASearchWithNoMatchesHandsBackNothing(t *testing.T) {
	runner := folderWith(t, map[string]string{"src/a.ts": "x\n"})
	if answer := runner.filesBehind("No matches."); answer != "" {
		t.Fatalf("got %q", answer)
	}
}
