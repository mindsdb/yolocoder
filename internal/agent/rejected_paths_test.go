package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mindsdb/yolocoder/internal/repo"
)

func TestRejectedPathsPreservesBatchAndEarlierEdits(t *testing.T) {
	repository := folder(t, map[string]string{"store.txt": "old\n", "view.txt": "old\n"})
	session := NewRunner(&Client{}, repository).newChangeSession("change both", "", nil, nil)
	ctx := context.Background()
	if output, _, ok := session.runTool(ctx, feedbackCall(t, edits("first", "@store.txt\n-old\n+saved\n"))); !ok {
		t.Fatal(output)
	}
	patch := "@store.txt\n-saved\n+final\n\n@view.txt\n-absent\n+good\n\n@store.txt\n+extra\n final\n"
	output, _, ok := session.runTool(ctx, feedbackCall(t, editsAndFinishes("bad", patch, "Premature completion.")))
	store, _ := repository.ReadFile("store.txt")
	view, _ := repository.ReadFile("view.txt")
	if ok || store != "saved\n" || view != "old\n" || session.used["apply_diff"] != 2 || session.complete || session.closing != "" {
		t.Fatalf("batch/earlier edit changed: output=%s store=%q view=%q used=%d", output, store, view, session.used["apply_diff"])
	}
	if !strings.Contains(output, "earlier accepted edits were not rolled back") ||
		!strings.Contains(output, "\n- \"store.txt\"\n- \"view.txt\" [placement error]\n") ||
		strings.Count(output, "\n- \"store.txt\"") != 1 {
		t.Fatalf("batch paths, classification, order or dedup lost: %s", output)
	}
	assertRemainingEdits(t, output, 2)
}

func TestRejectedPathsReachRepairRequestBeforeRealCheck(t *testing.T) {
	check := `node -e "const fs=require('fs');fs.appendFileSync('checked','yes\n');process.exitCode=fs.readFileSync('store.txt','utf8').trim()==='saved'&&fs.readFileSync('view.txt','utf8').trim()==='good'?0:1"`
	pkg, _ := json.Marshal(map[string]any{"scripts": map[string]string{"test": check}})
	repository := folder(t, map[string]string{"store.txt": "old\n", "view.txt": "old\n", "package.json": string(pkg)})
	server, seen := scripted(t,
		editsAndFinishes("bad", "@store.txt\n-old\n+saved\n\n@view.txt\n-absent\n+good\n", "Not done."),
		editsAndFinishes("repair", "@store.txt\n-old\n+saved\n\n@view.txt\n-old\n+good\n", "Both complete."),
	)
	defer server.Close()
	runner := NewRunner(&Client{endpoint: server.URL, model: "m", http: server.Client()}, repository)
	session := runner.newChangeSession("change both", "", nil, nil)
	outcome, err := session.work(context.Background(), &recordingProgress{})
	checked, _ := os.ReadFile(filepath.Join(repository.Root, "checked"))
	if err != nil || len(*seen) != 2 || outcome.Attempts != 2 || !outcome.Applied || outcome.Reply != "Both complete." || string(checked) != "yes\n" {
		t.Fatalf("repair/check sequence changed: outcome=%+v err=%v requests=%d checks=%q", outcome, err, len(*seen), checked)
	}
	output := feedbackResult(t, (*seen)[1]["input"], "bad")
	if !strings.Contains(output, "\n- \"store.txt\"\n- \"view.txt\" [placement error]\n") || !strings.Contains(output, "Unmarked paths were not applied either") {
		t.Fatalf("outgoing request lost unlanded companion evidence: %s", output)
	}
	assertRemainingEdits(t, output, 3)
	assertCompletionSchema(t, (*seen)[1]["tools"], true)
}

func TestRejectedPathsDisplayBoundsAndEscaping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		paths   []string
		omitted int
	}{
		{name: "path count", paths: func() []string {
			var paths []string
			for i := 0; i < 27; i++ {
				paths = append(paths, fmt.Sprintf("file-%02d.txt", i))
			}
			return paths
		}(), omitted: 3},
		{name: "byte limit", paths: []string{strings.Repeat("界", 900) + ".txt", "later.txt"}, omitted: 2},
		{name: "quoted path", paths: []string{"space quote\"control\x01雪.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var patch strings.Builder
			for _, path := range tc.paths {
				fmt.Fprintf(&patch, "--- a/%s\n+++ b/%s\n@@\n-old\n+new\n", path, path)
			}
			failure := &repo.PatchError{Failures: []*repo.HunkError{{Path: tc.paths[0], Reason: "not found"}}}
			output := rejectedPatchPaths(patch.String(), failure)
			if len(output) > 2<<10 || !utf8.ValidString(output) || strings.Count(output, "\n- ") > 24 {
				t.Fatalf("display bounds broken: bytes=%d output=%q", len(output), output)
			}
			if tc.omitted > 0 {
				if !strings.Contains(output, fmt.Sprintf("%d declared paths omitted", tc.omitted)) {
					t.Fatalf("missing exact omissions: %q", output)
				}
			} else if !strings.Contains(output, fmt.Sprintf("- %q [placement error]\n", tc.paths[0])) || strings.Contains(output, "\x01") {
				t.Fatalf("unsafe or missing quoted path: %q", output)
			}
			if tc.name == "path count" && (!strings.Contains(output, "\"file-23.txt\"") || strings.Contains(output, "\"file-24.txt\"")) {
				t.Fatal("path cap did not preserve declaration prefix")
			}
			if tc.name == "byte limit" && strings.Contains(output, "界") {
				t.Fatal("oversize path was partially displayed")
			}
		})
	}
}

func TestRejectedPathsDoNotChangeOtherFailuresOrSmallUI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		patch   string
		smallUI bool
	}{
		{name: "invalid arguments"},
		{name: "malformed patch", patch: "not a patch"},
		{name: "read error", patch: "@directory\n-old\n+new\n"},
		{name: "wrapped error", patch: "--- a/a.txt\n+++ b/a.txt\n@@\n-absent\n+new\n"},
		{name: "small UI", patch: "@a.txt\n-absent\n+new\n", smallUI: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := folder(t, map[string]string{"a.txt": "old\n", "directory/child": "present\n"})
			runner := NewRunner(&Client{}, repository)
			session := runner.newChangeSession("change it", "", nil, nil)
			session.prefetched = tc.smallUI
			if tc.smallUI {
				if _, err := runner.readFiles([]string{"a.txt"}); err != nil {
					t.Fatal(err)
				}
			}
			call := feedbackCall(t, edits("one", tc.patch))
			if tc.name == "invalid arguments" {
				call.Arguments = "{"
			}
			output, _, ok := session.runTool(context.Background(), call)
			content, _ := repository.ReadFile("a.txt")
			if ok || content != "old\n" || session.used["apply_diff"] != 1 || strings.Contains(output, "Declared paths in this rejected call") {
				t.Fatalf("unexpected new behavior: ok=%v source=%q output=%s", ok, content, output)
			}
			if tc.smallUI {
				want := "The patch did not apply and nothing was changed.\n\n" + session.lastFailure.Error() + session.staleContents(session.lastFailure)
				if output != want {
					t.Fatalf("small-UI output changed: %q", output)
				}
			}
		})
	}
}
