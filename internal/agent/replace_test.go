package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

func replacementCall(edits []repo.Replacement) responseItem {
	var anchored []anchoredReplacement
	for _, edit := range edits {
		anchored = append(anchored, anchoredReplacement{Replacement: edit})
	}
	return anchoredCall(anchored)
}

func anchoredCall(edits []anchoredReplacement) responseItem {
	args, _ := json.Marshal(replacementArguments{Edits: edits, Comment: "Updated the heading."})
	return responseItem{Type: "function_call", Name: "replace_text", CallID: "replacement", Arguments: string(args)}
}

func TestAnchoredReplacementPreservesMarkupAndCSSContext(t *testing.T) {
	r := folder(t, map[string]string{
		"page.html": "<span>Draft</span><label>Draft</label>",
		"style.css": "a{border-color:teal;} b{color:teal;}",
	})
	runner := NewRunner(&Client{}, r)
	_, _ = runner.readFiles([]string{"page.html", "style.css"})
	session := runner.newChangeSession("Update a label and border", "", nil, nil)
	session.prefetched = true
	call := anchoredCall([]anchoredReplacement{
		{Replacement: repo.Replacement{Path: "page.html", Old: "Draft", New: "Final"}, Prefix: "<span>", Suffix: "</span>"},
		{Replacement: repo.Replacement{Path: "style.css", Old: "teal", New: "navy"}, Prefix: "border-color:", Suffix: ";"},
	})
	output, _, _ := session.runTool(context.Background(), call)
	if !strings.HasPrefix(output, "Applied") || session.used["apply_diff"] != 1 {
		t.Fatal(output)
	}
	for path, want := range map[string]string{"page.html": "<span>Final</span><label>Draft</label>", "style.css": "a{border-color:navy;} b{color:teal;}"} {
		got, _ := r.ReadFile(path)
		if got != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestAnchoredReplacementRejectsOversizedBatchBeforeWriting(t *testing.T) {
	base := anchoredReplacement{Replacement: repo.Replacement{Path: "page.html", Old: "Draft", New: "Final"}}
	for _, field := range []string{"old", "new", "prefix", "suffix", "empty old"} {
		t.Run(field, func(t *testing.T) {
			r := folder(t, map[string]string{"page.html": "Draft", "first.txt": "Before"})
			runner := NewRunner(&Client{}, r)
			_, _ = runner.readFiles([]string{"page.html", "first.txt"})
			session := runner.newChangeSession("Update labels", "", nil, nil)
			session.prefetched = true
			bad := base
			switch field {
			case "old":
				bad.Old = strings.Repeat("x", 257)
			case "new":
				bad.New = strings.Repeat("x", 257)
			case "prefix":
				bad.Prefix = strings.Repeat("x", 129)
			case "suffix":
				bad.Suffix = strings.Repeat("x", 129)
			case "empty old":
				bad.Old = ""
				bad.Prefix = "Draft"
			}
			output, _, _ := session.runTool(context.Background(), anchoredCall([]anchoredReplacement{
				{Replacement: repo.Replacement{Path: "first.txt", Old: "Before", New: "After"}}, bad,
			}))
			first, _ := r.ReadFile("first.txt")
			last, _ := r.ReadFile("page.html")
			if !strings.HasPrefix(output, "ERROR") || first != "Before" || last != "Draft" {
				t.Fatalf("invalid batch changed files: %s", output)
			}
		})
	}
}

func TestAnchoredReplacementLimitCountsUnicodeCharacters(t *testing.T) {
	old, next := strings.Repeat("猫", 256), strings.Repeat("犬", 256)
	r := folder(t, map[string]string{"page.html": old})
	runner := NewRunner(&Client{}, r)
	_, _ = runner.readFiles([]string{"page.html"})
	session := runner.newChangeSession("Update text", "", nil, nil)
	session.prefetched = true
	output, _, _ := session.runTool(context.Background(), replacementCall([]repo.Replacement{{Path: "page.html", Old: old, New: next}}))
	got, _ := r.ReadFile("page.html")
	if !strings.HasPrefix(output, "Applied") || got != next {
		t.Fatal(output)
	}
}

func TestReplacementToolUsesReadStateAndSharedEditBudget(t *testing.T) {
	r := folder(t, map[string]string{"page.html": "<h1>Hello</h1>"})
	runner := NewRunner(&Client{}, r)
	session := runner.newChangeSession("Rename heading", "page.html", nil, nil)
	call := replacementCall([]repo.Replacement{{Path: "page.html", Old: "Hello", New: "Atlas"}})
	text, _, _ := session.runTool(context.Background(), call)
	if !strings.Contains(text, "unknown tool") {
		t.Fatal("replacement exposed without routing")
	}
	session.prefetched = true
	text, _, _ = session.runTool(context.Background(), call)
	if !strings.Contains(text, "read page.html first") {
		t.Fatal("unread file accepted")
	}
	_, _ = runner.readFiles([]string{"page.html"})
	session.used["apply_diff"] = toolQuota["apply_diff"]
	text, _, _ = session.runTool(context.Background(), call)
	if !strings.Contains(text, "limit") {
		t.Fatal("separate edit budget bypassed")
	}
	session.used["apply_diff"] = 0
	if err := r.Write("page.html", "<h1>Changed externally</h1>"); err != nil {
		t.Fatal(err)
	}
	text, _, _ = session.runTool(context.Background(), call)
	if !strings.Contains(text, "changed since") {
		t.Fatal("stale read accepted")
	}
}

func TestReplacementToolFinishesThroughNormalProjectCheck(t *testing.T) {
	r := folder(t, map[string]string{
		"page.html":    "<h1>Hello</h1>",
		"package.json": `{"scripts":{"test":"node -e \"require('fs').writeFileSync('checked','yes')\""}}`,
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/decisions" {
			fmt.Fprint(w, routerReply("small_ui", .99, 1))
			return
		}
		var request responseRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Instructions != smallEditInstructions {
			t.Error("small UI request did not retain its specialized instructions")
		}
		found := false
		for _, tool := range request.Tools {
			if tool.Name == "replace_text" {
				found = true
			}
		}
		if !found {
			t.Error("replacement tool missing")
		}
		call := replacementCall([]repo.Replacement{{Path: "page.html", Old: "Hello", New: "Atlas"}})
		_ = json.NewEncoder(w).Encode(responseEnvelope{ID: "r", Output: []responseItem{call}})
	}))
	defer server.Close()
	runner := NewRunner(&Client{baseURL: server.URL, endpoint: server.URL + "/v1/responses", model: "m", http: server.Client()}, r)
	runner.editRouterModel = "jev-test"
	outcome, err := runner.Run(context.Background(), "Rename heading to Atlas", nil, nil, &recordingProgress{})
	if err != nil || !outcome.Applied || outcome.Attempts != 1 {
		t.Fatalf("outcome=%+v error=%v", outcome, err)
	}
	if data, err := os.ReadFile(filepath.Join(r.Root, "checked")); err != nil || string(data) != "yes" {
		t.Fatal("project check skipped")
	}
	text, _ := r.ReadFile("page.html")
	if text != "<h1>Atlas</h1>" {
		t.Fatal("wrong result")
	}
}

func TestReplacementClosingNoteCannotBypassFailingCheck(t *testing.T) {
	r := folder(t, map[string]string{
		"page.html":    "<h1>Hello</h1>",
		"package.json": `{"scripts":{"test":"node -e \"process.exit(require('fs').readFileSync('page.html','utf8').includes('Atlas') ? 0 : 1)\""}}`,
	})
	bad := replacementCall([]repo.Replacement{{Path: "page.html", Old: "Hello", New: "Oops"}})
	good := replacementCall([]repo.Replacement{{Path: "page.html", Old: "Oops", New: "Atlas"}})
	server, seen := scripted(t, calls(bad.Name, bad.CallID, bad.Arguments), reads("refresh", "page.html"), calls(good.Name, "fixed", good.Arguments))
	defer server.Close()
	runner := NewRunner(&Client{endpoint: server.URL, model: "m", http: server.Client()}, r)
	session := runner.newChangeSession("Rename heading", "page.html", nil, nil)
	if !runner.completePrefetch(session, []string{"page.html"}, []string{"page.html", "package.json"}) {
		t.Fatal("prefetch rejected")
	}
	session.prefetched = true
	outcome, err := session.work(context.Background(), &recordingProgress{})
	if err != nil || !outcome.Applied || len(*seen) != 3 || outcome.Attempts != 2 {
		t.Fatalf("outcome=%+v err=%v requests=%d", outcome, err, len(*seen))
	}
	data, _ := json.Marshal((*seen)[1]["input"])
	if !strings.Contains(string(data), "check failed") {
		t.Fatal("failed check was not returned to model")
	}
}
