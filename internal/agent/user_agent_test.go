package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/httpclient"
	"github.com/mindsdb/yolocoder/internal/repo"
)

// Every call this package makes to the provider names YoloCoder. The
// client comes from NewClient, which is the wiring a real session uses,
// so a call site that goes back to http.DefaultClient (or a Client built
// without the shared one) sends Go's default agent and fails here.
func TestEveryProviderCallNamesYoloCoder(t *testing.T) {
	var mutex sync.Mutex
	agents := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		agents[r.URL.Path] = append(agents[r.URL.Path], r.Header.Get("User-Agent"))
		mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/decisions":
			fmt.Fprint(w, `{"model":"jev-test","answers":{"f0":{"type":"noul","noul":0.9}}}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
		default:
			fmt.Fprint(w, finishes("ok"))
		}
	}))
	defer server.Close()

	newClient := func(t *testing.T, api string) *Client {
		t.Helper()
		client, err := NewClient(config.LLM{BaseURL: server.URL, APIKey: "k", Model: "m", API: api})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	ctx := context.Background()
	for _, path := range []struct {
		name  string
		route string
		call  func(t *testing.T)
	}{
		{"file chooser", "/v1/decisions", func(t *testing.T) {
			runner := NewRunner(newClient(t, config.APIResponses), &repo.Repository{Root: t.TempDir()})
			runner.chooseFiles(ctx, "task", []string{"a.ts"}, &recordingProgress{})
		}},
		{"edit router", "/v1/decisions", func(t *testing.T) {
			_, _, _ = newClient(t, config.APIResponses).selectEditFiles(ctx, "jev-test", "task", []string{"a.tsx"})
		}},
		{"responses", "/v1/responses", func(t *testing.T) {
			if _, err := newClient(t, config.APIResponses).create(ctx, responseRequest{Input: "hi"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"chat completions", "/v1/chat/completions", func(t *testing.T) {
			_, _ = newClient(t, config.APIChat).create(ctx, responseRequest{Input: "hi"})
		}},
		{"model listing", "/v1/models", func(t *testing.T) {
			if _, err := ListModels(ctx, server.URL, "k"); err != nil {
				t.Fatal(err)
			}
		}},
		{"dialect probe", "/v1/responses", func(t *testing.T) {
			if _, err := DetectAPI(ctx, server.URL, "k"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(path.name, func(t *testing.T) {
			mutex.Lock()
			delete(agents, path.route)
			mutex.Unlock()
			path.call(t)
			mutex.Lock()
			seen := agents[path.route]
			mutex.Unlock()
			if len(seen) == 0 {
				t.Fatalf("no request reached %s", path.route)
			}
			for _, agent := range seen {
				if agent != httpclient.UserAgent() {
					t.Fatalf("User-Agent = %q, want %q", agent, httpclient.UserAgent())
				}
			}
		})
	}
}
