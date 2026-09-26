package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type wholeContextTransport func(*http.Request) (*http.Response, error)

func (transport wholeContextTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

type wholeContextCancelBody struct {
	io.Reader
	cancel context.CancelFunc
}

func (body wholeContextCancelBody) Close() error {
	body.cancel()
	return nil
}

func TestWholeContextCanceledRouterCannotCommitRankedFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	client := &Client{baseURL: "https://example.invalid", http: &http.Client{Transport: wholeContextTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		// selectEditFiles closes this body after successfully decoding Jev's
		// normal selection, canceling the parent before prefetch can proceed.
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: wholeContextCancelBody{
			Reader: strings.NewReader(routerReply("normal", .99, 1)), cancel: cancel,
		}}, nil
	})}}
	runner := NewRunner(client, folder(t, map[string]string{"view.tsx": "export const title = 'Hello';", "package.json": "{}"}))
	runner.editRouterModel = "jev-test"
	session := runner.newChangeSession("Update the view", "", nil, nil)
	progress := &recordingProgress{}
	runner.prefetchEdit(ctx, session, []string{"view.tsx", "package.json"}, progress)
	if requests != 1 || ctx.Err() == nil || len(session.transcript) != 1 || len(session.readPaths) != 0 || len(runner.served) != 0 || session.used["read_files"] != 0 || session.prefetched {
		t.Fatalf("canceled prefetch committed state: requests=%d error=%v used=%v paths=%v", requests, ctx.Err(), session.used, session.readPaths)
	}
	if !strings.Contains(strings.Join(progress.logs, "\n"), "reason=cancelled") {
		t.Fatal("caller did not stop the canceled fallback")
	}
}

func TestWholeContextOutgoingReadsKeepNormalWriterAndCheck(t *testing.T) {
	files := map[string]string{
		"service.ts":   "export const enabled = false;\n",
		"package.json": `{"scripts":{"test":"node -e \"require('fs').writeFileSync('checked','yes')\""}}`,
	}
	for i := range 12 {
		files[fmt.Sprintf("docs/guide%02d.md", i)] = fmt.Sprintf("Project note %02d\n", i)
	}
	repository := folder(t, files)
	var routes, coding int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/decisions" {
			routes++
			fmt.Fprint(w, routerReply("normal", .99, 1))
			return
		}
		coding++
		var body responseRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "primary" || body.Instructions != changeInstructions {
			t.Error("whole context changed the writer or instructions")
		}
		for _, tool := range body.Tools {
			if tool.Name == "replace_text" {
				t.Error("whole context enabled small-UI editing")
			}
		}
		encoded, _ := json.Marshal(body.Input)
		var input []struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
			Output    string `json:"output"`
		}
		if err := json.Unmarshal(encoded, &input); err != nil {
			t.Error(err)
			return
		}
		var reads int
		shown := map[string]bool{}
		for i, item := range input {
			if item.Type != "function_call" {
				continue
			}
			reads++
			var args struct{ Paths []string }
			if err := json.Unmarshal([]byte(item.Arguments), &args); err != nil || len(args.Paths) == 0 || len(args.Paths) > 12 {
				t.Errorf("invalid completed read: %s", item.Arguments)
			}
			if item.Name != "read_files" || i+1 >= len(input) || input[i+1].Type != "function_call_output" || input[i+1].CallID != item.CallID {
				t.Error("unpaired completed read")
				continue
			}
			var want strings.Builder
			for _, path := range args.Paths {
				if shown[path] {
					t.Errorf("duplicate context: %s", path)
				}
				shown[path] = true
				fmt.Fprintf(&want, "--- %s ---\n%s\n", path, files[path])
			}
			if input[i+1].Output != want.String() {
				t.Error("outgoing read did not contain the exact initial file bytes")
			}
		}
		if reads != 2 || len(shown) != len(files) {
			t.Errorf("incomplete initial context: reads=%d paths=%v", reads, shown)
		}
		fmt.Fprint(w, editsAndFinishes("edit", "@service.ts\n-export const enabled = false;\n+export const enabled = true;\n", "Enabled the service."))
	}))
	defer server.Close()
	runner := NewRunner(&Client{baseURL: server.URL, endpoint: server.URL + "/v1/responses", model: "primary", http: server.Client()}, repository)
	runner.editRouterModel, runner.smallEditModel = "jev-test", "small"
	outcome, err := runner.Run(context.Background(), "Enable the service", nil, nil, &recordingProgress{})
	if err != nil || !outcome.Applied || routes != 1 || coding != 1 || outcome.Attempts != 1 {
		t.Fatalf("outcome=%+v err=%v routes=%d coding=%d", outcome, err, routes, coding)
	}
	if content, err := os.ReadFile(filepath.Join(repository.Root, "checked")); err != nil || string(content) != "yes" {
		t.Fatal("mandatory project check did not run")
	}
}

func TestWholeContextReadStateAndExclusions(t *testing.T) {
	files := map[string]string{}
	var paths []string
	for i := range 24 {
		path := fmt.Sprintf("notes/%02d.md", i)
		files[path] = fmt.Sprintf("note %d\n", i)
		paths = append(paths, path)
	}
	for _, path := range []string{"package-lock.json", "yarn.lock", "lock.json", "bundle.min.js", "bundle.js.map", "node_modules/pkg/index.js", "dist/index.js", ".yolocoder/state.json", ".env", "image.png"} {
		files[path] = strings.Repeat("excluded", 4000)
	}
	repository := folder(t, files)
	runner := NewRunner(&Client{}, repository)
	session := runner.newChangeSession("Update project notes", "", nil, nil)
	mapped := make([]string, 0, len(files)+1)
	for path := range files {
		mapped = append(mapped, path)
	}
	mapped = append(mapped, paths[0]) // Duplicate map entries are one read.
	if !runner.completeWholeContext(context.Background(), session, mapped) {
		t.Fatal("eligible bounded context rejected")
	}
	if session.prefetched || session.used["read_files"] != 2 || session.used["apply_diff"] != 0 || len(session.transcript) != 5 || !reflect.DeepEqual(paths, session.readPaths) {
		t.Fatalf("incorrect completed-read state: used=%v paths=%v", session.used, session.readPaths)
	}
	if len(runner.served) != len(paths) {
		t.Fatal("excluded content was served")
	}
	for _, path := range paths {
		if runner.served[path] != files[path] {
			t.Errorf("snapshot differs at %s", path)
		}
	}
	if _, err := chatMessages(session.transcript); err != nil {
		t.Fatalf("invalid completed-read transcript for chat dialect: %v", err)
	}
	text, err := runner.readFiles(paths[:1])
	if err != nil || !strings.Contains(text, "unchanged since it was shown") {
		t.Fatal("shown snapshot was not deduplicated")
	}
	if err := repository.Write(paths[0], "changed externally"); err != nil {
		t.Fatal(err)
	}
	text, err = runner.readFiles(paths[:1])
	if err != nil || !strings.Contains(text, "changed externally") {
		t.Fatal("new content was hidden by stale served state")
	}
}

func TestWholeContextBoundsCommitAllOrNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		used  int
		want  bool
	}{
		{"exact_file_limit", map[string]string{"a.ts": strings.Repeat("x", 16000)}, 0, true},
		{"file_over_limit", map[string]string{"a.ts": strings.Repeat("x", 16001)}, 0, false},
		{"exact_framed_limit", map[string]string{"a.ts": strings.Repeat("x", 12000), "b.ts": strings.Repeat("y", 11972)}, 0, true},
		{"framed_over_limit", map[string]string{"a.ts": strings.Repeat("x", 12000), "b.ts": strings.Repeat("y", 11973)}, 0, false},
		{"invalid_utf8", map[string]string{"a.ts": "valid", "z.txt": "\xff"}, 0, false},
		{"nul", map[string]string{"a.ts": "valid", "z.txt": "a\x00b"}, 0, false},
		{"one_quota_left", map[string]string{"a.ts": "valid"}, 7, true},
		{"quota_exhausted", map[string]string{"a.ts": "valid"}, 8, false},
		{"no_supported_files", map[string]string{"image.png": "data"}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := NewRunner(&Client{}, folder(t, tc.files))
			session := runner.newChangeSession("Update project", "", nil, nil)
			session.used["read_files"] = tc.used
			var paths []string
			for path := range tc.files {
				paths = append(paths, path)
			}
			if got := runner.completeWholeContext(context.Background(), session, paths); got != tc.want {
				t.Fatalf("completed=%v want=%v", got, tc.want)
			}
			if !tc.want && (len(session.transcript) != 1 || len(session.readPaths) != 0 || len(runner.served) != 0 || session.used["read_files"] != tc.used) {
				t.Fatal("failed whole-context read changed state")
			}
		})
	}
	for _, tc := range []struct{ count, used int }{{25, 0}, {13, 7}} {
		files := map[string]string{}
		var paths []string
		for i := range tc.count {
			path := fmt.Sprintf("%02d.txt", i)
			files[path] = "note"
			paths = append(paths, path)
		}
		runner := NewRunner(&Client{}, folder(t, files))
		session := runner.newChangeSession("Update project", "", nil, nil)
		session.used["read_files"] = tc.used
		if runner.completeWholeContext(context.Background(), session, paths) || len(session.transcript) != 1 || len(runner.served) != 0 || session.used["read_files"] != tc.used {
			t.Fatalf("file/read-count bound failed: %+v", tc)
		}
	}
}

func TestWholeContextRejectsUnsafeUnavailableAndCanceledReads(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "safe", "dir/z.ts": "safe"})
	if err := os.Mkdir(filepath.Join(repository.Root, "directory.ts"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"linked.ts": "a.ts", "linked_dir": "dir", "outside.ts": filepath.Join(t.TempDir(), "missing.ts")} {
		if err := os.Symlink(target, filepath.Join(repository.Root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"linked.ts", "linked_dir/z.ts", "outside.ts", "directory.ts", "missing.ts", "../outside.ts", "/outside.ts", "dir/../a.ts"} {
		t.Run(path, func(t *testing.T) {
			runner := NewRunner(&Client{}, repository)
			session := runner.newChangeSession("Update project", "", nil, nil)
			if runner.completeWholeContext(context.Background(), session, []string{"a.ts", path}) || len(session.transcript) != 1 || len(runner.served) != 0 || session.used["read_files"] != 0 {
				t.Fatal("unsafe or incomplete read changed context")
			}
		})
	}
	runner := NewRunner(&Client{}, repository)
	session := runner.newChangeSession("Update project", "", nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if runner.completeWholeContext(ctx, session, []string{"a.ts"}) || len(session.transcript) != 1 || len(runner.served) != 0 {
		t.Fatal("canceled context was committed")
	}
}

func TestWholeContextPreservesSmallUIAndRankedFallback(t *testing.T) {
	for _, route := range []string{"normal", "small_ui"} {
		t.Run(route, func(t *testing.T) {
			files := map[string]string{"view.tsx": "export const title = 'Hello';", "package.json": "{}", "README.md": "useful project note"}
			if route == "normal" {
				files["README.md"] = strings.Repeat("x", 16001) // Whole context must fail closed.
			}
			repository := folder(t, files)
			var mapped []string
			for path := range files {
				mapped = append(mapped, path)
			}
			sort.Strings(mapped)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, routerReply(route, .99, 1))
			}))
			defer server.Close()
			runner := NewRunner(&Client{baseURL: server.URL, http: server.Client()}, repository)
			runner.editRouterModel = "jev-test"
			session := runner.newChangeSession("Update the heading", "", nil, nil)
			runner.prefetchEdit(context.Background(), session, mapped, &recordingProgress{})

			baseline := NewRunner(&Client{}, repository)
			want := baseline.newChangeSession("Update the heading", "", nil, nil)
			if !baseline.completePrefetch(want, []string{"view.tsx"}, mapped) {
				t.Fatal("invalid baseline fixture")
			}
			want.prefetched = route == "small_ui"
			if !reflect.DeepEqual(session.transcript, want.transcript) || !reflect.DeepEqual(session.used, want.used) || !reflect.DeepEqual(session.readPaths, want.readPaths) || !reflect.DeepEqual(runner.served, baseline.served) || session.prefetched != want.prefetched || session.instructions() != want.instructions() || !reflect.DeepEqual(session.tools(), want.tools()) {
				t.Fatal("small-UI/fallback read state or tools differ from existing prefetch")
			}
		})
	}
}
