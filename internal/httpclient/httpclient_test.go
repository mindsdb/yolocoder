package httpclient

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// The wrapper hands the transport a clone, and the clone has to keep the
// caller's context. The file chooser's 6s limit and the launch update
// check's 2s limit both ride on it.
func TestTheCallersDeadlineStillApplies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Client.Do(request)
	if err == nil {
		response.Body.Close()
		t.Fatal("the request outlived its caller's deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
}

// Every outbound call goes through Client. A new call site that reaches
// for Go's default client or transport sends "Go-http-client/…", which an
// edge rule can turn away, so this fails the build instead of the user's
// turn.
func TestNoCodeBypassesTheSharedClient(t *testing.T) {
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
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if exempt[filepath.ToSlash(relative)] {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found, err := bypasses(path, source)
		if err != nil {
			return err
		}
		for _, one := range found {
			t.Errorf("%s sends without the shared client", one)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// exempt is the code allowed to build its own sender: Client itself, and
// the --web preview proxy, which forwards the browser's own requests to
// the local dev server. httputil.ReverseProxy keeps the browser's
// User-Agent there instead of adding Go's.
var exempt = map[string]bool{
	"internal/httpclient/httpclient.go": true,
	"internal/web/proxy.go":             true,
}

// bypasses lists each place in one Go file that could send with Go's
// default User-Agent: net/http's default client or transport, its
// package-level Get, Head, Post and PostForm, or an http.Client or
// http.Transport made as a value rather than handed around as a pointer.
// It reads the syntax tree, not the text, so comments, strings and a
// struct field that happens to be named http are not taken for sends.
func bypasses(name string, source []byte) ([]string, error) {
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, name, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var found []string
	report := func(node ast.Node, what string) {
		found = append(found, fmt.Sprintf("%s: %s", files.Position(node.Pos()), what))
	}
	local := ""
	for _, spec := range file.Imports {
		if spec.Path.Value != `"net/http"` {
			continue
		}
		local = "http"
		if spec.Name != nil {
			local = spec.Name.Name
		}
		if local == "." {
			report(spec, "net/http imported with a dot")
		}
	}
	if local == "" || local == "_" || local == "." {
		return found, nil
	}
	pointed := map[ast.Expr]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		if star, ok := node.(*ast.StarExpr); ok {
			pointed[star.X] = true
		}
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := selector.X.(*ast.Ident); !ok || pkg.Name != local {
			return true
		}
		switch selector.Sel.Name {
		case "DefaultClient", "DefaultTransport", "Get", "Head", "Post", "PostForm":
			report(selector, local+"."+selector.Sel.Name)
		case "Client", "Transport":
			if !pointed[selector] {
				report(selector, "its own "+local+"."+selector.Sel.Name)
			}
		}
		return true
	})
	return found, nil
}

// The guard has to see every way to reach Go's default sender, and none
// of the lines that only look like one.
func TestTheGuardSeesEveryBypassAndNothingElse(t *testing.T) {
	inside := func(code string) string {
		return "package probe\n\nimport \"net/http\"\n\nfunc f(r *http.Request) {\n\t" + code + "\n}\n"
	}
	for _, probe := range []struct {
		name   string
		source string
		want   int
	}{
		{"default client", inside(`http.DefaultClient.Do(r)`), 1},
		{"default transport", inside(`http.DefaultTransport.RoundTrip(r)`), 1},
		{"package get", inside(`http.Get("u")`), 1},
		{"package post form", inside(`http.PostForm("u", nil)`), 1},
		{"client literal", inside(`(&http.Client{}).Do(r)`), 1},
		{"new client", inside(`new(http.Client).Do(r)`), 1},
		{"zero-value client", inside(`var client http.Client; client.Do(r)`), 1},
		{"transport literal", inside(`(&http.Transport{}).RoundTrip(r)`), 1},
		{"aliased import", "package probe\n\nimport h \"net/http\"\n\nvar _ = h.DefaultClient\n", 1},
		{"dot import", "package probe\n\nimport . \"net/http\"\n\nvar _ = DefaultClient\n", 1},
		{"shared client by pointer", inside(`var client *http.Client; client.Do(r)`), 0},
		{"comment", inside(`// never call http.DefaultClient here`), 0},
		{"string", inside(`_ = "do not use http.Get"`), 0},
		{"field named http", "package probe\n\nimport \"net/http\"\n\ntype c struct{ http *http.Client }\n\nfunc (x c) f() { x.http.Get(\"u\") }\n", 0},
	} {
		t.Run(probe.name, func(t *testing.T) {
			found, err := bypasses("probe.go", []byte(probe.source))
			if err != nil {
				t.Fatal(err)
			}
			if len(found) != probe.want {
				t.Fatalf("found %q, want %d", found, probe.want)
			}
		})
	}
}
