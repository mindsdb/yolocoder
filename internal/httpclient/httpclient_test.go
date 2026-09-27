package httpclient

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mindsdb/yolocoder/internal/version"
)

func TestTheAgentNamesTheStampedVersion(t *testing.T) {
	stamped := version.Version
	version.Version = "v9.9.9"
	t.Cleanup(func() { version.Version = stamped })

	if got, want := UserAgent(), "yolocoder/v9.9.9 (+https://github.com/mindsdb/yolocoder)"; got != want {
		t.Fatalf("UserAgent() = %q, want %q", got, want)
	}
}

// The self-updater's downloads redirect, so the header has to ride on
// every hop, not only the request the caller built.
func TestEveryHopCarriesTheAgentAndTheCallersRequestIsUntouched(t *testing.T) {
	var agents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents = append(agents, r.Header.Get("User-Agent"))
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/second", http.StatusFound)
		}
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/first", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	if len(agents) != 2 {
		t.Fatalf("hops = %d, want the request and its redirect", len(agents))
	}
	for _, agent := range agents {
		if agent != UserAgent() {
			t.Fatalf("User-Agent = %q, want %q", agent, UserAgent())
		}
	}
	if got := request.Header.Get("User-Agent"); got != "" {
		t.Fatalf("the caller's request was changed: User-Agent = %q", got)
	}
}

// Over TLS the transport speaks HTTP/2, where Go's default agent is
// "Go-http-client/2.0". That is what a real session sends to the provider,
// so the header has to win there too.
func TestTheAgentRidesOnHTTP2(t *testing.T) {
	var protocol int
	var agent string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol, agent = r.ProtoMajor, r.Header.Get("User-Agent")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	client := &http.Client{Transport: userAgent{next: server.Client().Transport}}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if protocol != 2 {
		t.Fatalf("spoke HTTP/%d, want HTTP/2", protocol)
	}
	if agent != UserAgent() {
		t.Fatalf("User-Agent = %q, want %q", agent, UserAgent())
	}
}

// Every outbound call goes through Client. A new call site that reaches
// for Go's default client sends "Go-http-client/…", which an edge rule
// can turn away, so this fails the build instead of the user's turn.
func TestNoCodeBypassesTheSharedClient(t *testing.T) {
	bypass := regexp.MustCompile(`http\.(DefaultClient|Get|Head|Post|PostForm)\b|http\.Client\{`)
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "httpclient" {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for number, line := range strings.Split(string(source), "\n") {
			if bypass.MatchString(line) {
				t.Errorf("%s:%d sends without the shared client: %s", path, number+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
