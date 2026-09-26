package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/mindsdb/yolocoder/internal/repo"
)

func TestSmallWriterRoutesOnlyPrefetchedSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prefetched bool
		model      string
		want       []string
	}{
		{"normal", false, "small", []string{"primary", "primary"}},
		{"disabled", true, "", []string{"primary", "primary"}},
		{"same model", true, "primary", []string{"primary", "primary"}},
		{"prefetched", true, "small", []string{"small", "small"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, seen := scripted(t, finishes("done"), finishes("done"))
			defer server.Close()
			client := &Client{endpoint: server.URL, model: "primary", http: server.Client()}
			runner := NewRunner(client, folder(t, map[string]string{"page.html": "Hello"}))
			runner.smallEditModel = tc.model
			session := runner.newChangeSession("Rename heading", "page.html", nil, nil)
			session.prefetched = tc.prefetched
			for round := 0; round < 2; round++ {
				if _, err := session.create(context.Background(), responseRequest{Input: session.transcript}, &recordingProgress{}); err != nil {
					t.Fatal(err)
				}
			}
			var models []string
			for _, request := range *seen {
				models = append(models, request["model"].(string))
			}
			if !reflect.DeepEqual(models, tc.want) || client.model != "primary" {
				t.Fatalf("models=%v client=%s", models, client.model)
			}
		})
	}
}

func TestSmallWriterFailureReturnsToPrimary(t *testing.T) {
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request responseRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		models = append(models, request.Model)
		if request.Model == "small" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(responseEnvelope{Output: []responseItem{{Type: "message", Content: []contentItem{{Type: "output_text", Text: "done"}}}}})
	}))
	defer server.Close()
	runner := NewRunner(&Client{endpoint: server.URL, model: "primary", http: server.Client()}, folder(t, map[string]string{"page.html": "Hello"}))
	runner.smallEditModel = "small"
	session := runner.newChangeSession("Rename heading", "page.html", nil, nil)
	session.prefetched = true
	if _, err := session.create(context.Background(), responseRequest{Input: session.transcript}, &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.create(context.Background(), responseRequest{Input: session.transcript}, &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(models, []string{"small", "primary", "primary"}) {
		t.Fatalf("models=%v", models)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := runner.newChangeSession("Rename heading", "page.html", nil, nil)
	cancelled.prefetched = true
	if _, err := cancelled.create(ctx, responseRequest{Input: cancelled.transcript}, &recordingProgress{}); err == nil || len(models) != 3 || cancelled.writer == runner.client {
		t.Fatal("cancelled request continued")
	}
}

func TestSmallWriterKeepsDialectNegotiationLocalToSession(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, endpoint: server.URL + "/v1/responses", model: "primary", autoDialect: true, http: server.Client()}
	runner := NewRunner(client, folder(t, map[string]string{"page.html": "Hello"}))
	runner.smallEditModel = "small"
	session := runner.newChangeSession("Rename heading", "page.html", nil, nil)
	session.prefetched = true
	for i := 0; i < 2; i++ {
		if _, err := session.create(context.Background(), responseRequest{Input: session.transcript}, &recordingProgress{}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(paths, []string{"/v1/responses", "/v1/chat/completions", "/v1/chat/completions"}) || client.chat || client.model != "primary" {
		t.Fatalf("paths=%v primary=%+v", paths, client)
	}
}

func TestSmallWriterRepairsFailedProjectCheckInSameBoundedLoop(t *testing.T) {
	r := folder(t, map[string]string{
		"page.html":    "<h1>Hello</h1>",
		"package.json": `{"scripts":{"test":"node -e \"process.exit(require('fs').readFileSync('page.html','utf8').includes('Atlas') ? 0 : 1)\""}}`,
	})
	bad := replacementCall([]repo.Replacement{{Path: "page.html", Old: "Hello", New: "Oops"}})
	good := replacementCall([]repo.Replacement{{Path: "page.html", Old: "Oops", New: "Atlas"}})
	server, seen := scripted(t, calls(bad.Name, bad.CallID, bad.Arguments), reads("refresh", "page.html"), calls(good.Name, "fixed", good.Arguments))
	defer server.Close()
	runner := NewRunner(&Client{endpoint: server.URL, model: "primary", http: server.Client()}, r)
	runner.smallEditModel = "small"
	session := runner.newChangeSession("Rename heading to Atlas", "page.html", nil, nil)
	if !runner.completePrefetch(session, []string{"page.html"}, []string{"page.html", "package.json"}) {
		t.Fatal("prefetch rejected")
	}
	session.prefetched = true
	outcome, err := session.work(context.Background(), &recordingProgress{})
	if err != nil || !outcome.Applied || outcome.Attempts != 2 || len(*seen) != 3 {
		t.Fatalf("outcome=%+v err=%v requests=%d", outcome, err, len(*seen))
	}
	for i, want := range []string{"small", "small", "small"} {
		if (*seen)[i]["model"] != want {
			t.Fatalf("round %d used %v", i, (*seen)[i]["model"])
		}
	}
	got, _ := r.ReadFile("page.html")
	if got != "<h1>Atlas</h1>" {
		t.Fatalf("bad repaired content: %s", got)
	}
}
