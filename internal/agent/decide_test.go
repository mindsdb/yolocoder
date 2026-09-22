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

// decides answers a decisions call with the given probability per file,
// keyed the way chooseFiles asks.
func decides(t *testing.T, odds map[string]float64, paths []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/decisions") {
			t.Errorf("asked %s, want the decisions route", request.URL.Path)
		}
		answers := map[string]map[string]any{}
		for index, path := range paths {
			answers[fmt.Sprintf("f%d", index)] = map[string]any{"type": "noul", "noul": odds[path]}
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers})
	}))
}

func TestChosenFilesAreTheOnesAboveTheLine(t *testing.T) {
	paths := []string{"a.ts", "b.ts", "c.ts"}
	server := decides(t, map[string]float64{"a.ts": 0.91, "b.ts": 0.40, "c.ts": 0.04}, paths)
	defer server.Close()

	runner := NewRunner(&Client{baseURL: server.URL, apiKey: "k", http: server.Client()}, &repo.Repository{Root: t.TempDir()})
	chosen := runner.chooseFiles(context.Background(), "do a thing", paths)
	if len(chosen) != 2 || chosen[0] != "a.ts" || chosen[1] != "b.ts" {
		t.Fatalf("chose %v, want a.ts and b.ts, most confident first", chosen)
	}
}

func TestAShrugChoosesNothing(t *testing.T) {
	// Everything above the line is not a selection, it is the whole
	// repository, and reading all of it costs more than the call saved.
	paths := []string{"a.ts", "b.ts", "c.ts", "d.ts", "e.ts", "f.ts", "g.ts", "h.ts", "i.ts"}
	odds := map[string]float64{}
	for _, p := range paths {
		odds[p] = 0.8
	}
	server := decides(t, odds, paths)
	defer server.Close()

	runner := NewRunner(&Client{baseURL: server.URL, apiKey: "k", http: server.Client()}, &repo.Repository{Root: t.TempDir()})
	if chosen := runner.chooseFiles(context.Background(), "do a thing", paths); chosen != nil {
		t.Fatalf("chose %v, want nothing", chosen)
	}
}

func TestAnythingGoingWrongJustChoosesNothing(t *testing.T) {
	// A 504 was seen once in twenty-five real calls. It must cost the
	// optimization, never the turn.
	for _, broken := range []http.HandlerFunc{
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusGatewayTimeout) },
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "not json at all") },
	} {
		server := httptest.NewServer(broken)
		runner := NewRunner(&Client{baseURL: server.URL, apiKey: "k", http: server.Client()}, &repo.Repository{Root: t.TempDir()})
		if chosen := runner.chooseFiles(context.Background(), "task", []string{"a.ts"}); chosen != nil {
			t.Fatalf("chose %v from a broken endpoint", chosen)
		}
		server.Close()
	}
}

func TestATooLargeMapIsLeftToTheModel(t *testing.T) {
	var paths []string
	for index := 0; index <= maxDecideFiles; index++ {
		paths = append(paths, fmt.Sprintf("f%d.ts", index))
	}
	asked := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked = true }))
	defer server.Close()
	runner := NewRunner(&Client{baseURL: server.URL, apiKey: "k", http: server.Client()}, &repo.Repository{Root: t.TempDir()})
	if chosen := runner.chooseFiles(context.Background(), "task", paths); chosen != nil || asked {
		t.Fatal("past the ceiling the map is cheaper for the model to narrow by name")
	}
}

func TestPreselectionRemovesTheCallThatOnlyPicked(t *testing.T) {
	// The whole point: the model opens already holding the file, so its
	// first call is the one that does the work.
	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n", "b.ts": "const y = 2;\n"})
	decider := decides(t, map[string]float64{"a.ts": 0.9, "b.ts": 0.1}, []string{"a.ts", "b.ts"})
	defer decider.Close()

	model, seen := scripted(t,
		editsAndFinishes("c1", "@a.ts\n-const x = 1;\n+const x = 9;\n", "Bumped x."),
	)
	defer model.Close()

	client := &Client{endpoint: model.URL, baseURL: decider.URL, apiKey: "k", model: "m", http: model.Client()}
	runner := NewRunner(client, repository)
	runner.UsePreselect(true)
	progress := &recordingProgress{}
	outcome, err := runner.Run(context.Background(), "bump x", nil, nil, progress)
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 {
		t.Fatalf("model calls = %d, want the edit to have been the whole turn", len(*seen))
	}
	encoded, _ := json.Marshal((*seen)[0]["input"])
	if !strings.Contains(string(encoded), "const x = 1;") {
		t.Fatalf("the chosen file should already be in the conversation:\n%s", encoded)
	}
	if strings.Contains(string(encoded), "const y = 2;") {
		t.Fatal("a file below the line was read anyway")
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "picked a.ts") {
		t.Fatalf("the trail should say which files were chosen for it:\n%s", trail)
	}
	content, _ := os.ReadFile(filepath.Join(repository.Root, "a.ts"))
	if string(content) != "const x = 9;\n" {
		t.Fatalf("a.ts = %q", content)
	}
	if !outcome.Applied {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestPreselectionIsOffUnlessAskedFor(t *testing.T) {
	repository := folder(t, map[string]string{"a.ts": "const x = 1;\n"})
	asked := false
	decider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked = true }))
	defer decider.Close()
	model, _ := scripted(t, finishes("Nothing to do."))
	defer model.Close()

	client := &Client{endpoint: model.URL, baseURL: decider.URL, apiKey: "k", model: "m", http: model.Client()}
	if _, err := NewRunner(client, repository).Run(context.Background(), "hi", nil, nil, &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	if asked {
		t.Fatal("a decision was asked for without the flag")
	}
}
