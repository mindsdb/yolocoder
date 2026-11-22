package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestInsertShimAfterHead(t *testing.T) {
	html := "<html><head><title>x</title></head><body></body></html>"
	out := string(insertShim([]byte(html)))
	wantPrefix := "<html><head>" + errorShim
	if !strings.HasPrefix(out, wantPrefix) {
		t.Fatalf("shim not inserted right after <head>: %s", out)
	}
	if !strings.Contains(out, "<title>x</title>") {
		t.Fatal("insertion should not drop the rest of the document")
	}
}

func TestInsertShimWithoutHeadPrependsIt(t *testing.T) {
	html := "<body>fragment, no head tag</body>"
	out := insertShim([]byte(html))
	if !bytes.HasPrefix(out, []byte(errorShim)) {
		t.Fatalf("shim should be prepended when there's no <head>: %s", out)
	}
	if !strings.Contains(string(out), "fragment, no head tag") {
		t.Fatal("insertion should not drop the original content")
	}
}

func TestAppProxyServesASelfHealingPageWhenNothingIsRunning(t *testing.T) {
	proxy := newAppProxy(func() int { return 0 })
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("X-YoloCoder-Reconnecting"); got != "true" {
		t.Fatalf("reconnecting header = %q, want true", got)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "isn't running yet") {
		t.Fatalf("body = %s, want it to say the dev server isn't running", body)
	}
	// The whole point: it has to poll and reload itself, or a viewer is
	// stuck on this page until they refresh the browser by hand.
	if !strings.Contains(body, "fetch(location.href") || !strings.Contains(body, "location.reload()") {
		t.Fatalf("body = %s, want a self-polling reload script", body)
	}
}

func TestWriteSelfHealingPageSubstitutesTheMessage(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeSelfHealingPage(recorder, "dev server is not reachable yet: dial tcp refused")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("X-YoloCoder-Reconnecting"); got != "true" {
		t.Fatalf("reconnecting header = %q, want true", got)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "dial tcp refused") {
		t.Fatalf("body = %s, want the specific error message included", body)
	}
	if !strings.Contains(body, "maxAttempts=60") || !strings.Contains(body, "attempt+1") {
		t.Fatalf("body = %s, want bounded retry logic", body)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}
}

func TestErrorShimFiltersBrowserDiagnostics(t *testing.T) {
	for _, marker := range []string{"willreadfrequently", "requestanimationframe", "forced reflow", "long task"} {
		if !strings.Contains(errorShim, marker) {
			t.Fatalf("error shim does not filter %q diagnostics", marker)
		}
	}
}

// The shim runs in node against a stand-in window: a handled 500 from an
// API call is reported with what the server said, and a 404 or a success
// is not.
func TestErrorShimReportsServerErrorsEvenWhenTheAppCatchesThem(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := strings.TrimSuffix(strings.TrimPrefix(errorShim, "<script>"), "</script>")
	harness := `
var posted=[];
var window={parent:{postMessage:function(m){posted.push(m);}},addEventListener:function(){}};
function reply(status,text,body){return {status:status,statusText:text,clone:function(){return {text:function(){return Promise.resolve(body);}};}};}
window.fetch=function(url){
  if(url==="/api/move") return Promise.resolve(reply(500,"Internal Server Error","<pre>TypeError: board[y] is undefined<br> at move (index.ts:12)</pre>"));
  if(url==="/api/room") return Promise.resolve(reply(404,"Not Found","{\"error\":\"Room not found\"}"));
  return Promise.resolve(reply(200,"OK","{}"));
};
` + script + `
Promise.all([
  window.fetch("/api/move",{method:"post"}).then(function(r){ if(r.status!==500) throw new Error("response changed"); }),
  window.fetch("/api/room"),
  window.fetch("/api/state"),
]).then(function(){ setTimeout(function(){ console.log(JSON.stringify(posted)); }, 10); });
`
	output, err := exec.Command(node, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, output)
	}
	var posted []struct {
		Type  string `json:"type"`
		Error struct {
			Message string `json:"message"`
			Stack   string `json:"stack"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output, &posted); err != nil {
		t.Fatalf("decode %q: %v", output, err)
	}
	if len(posted) != 1 || posted[0].Type != "APP_FETCH_ERROR" ||
		posted[0].Error.Message != "POST /api/move returned 500 Internal Server Error" ||
		!strings.Contains(posted[0].Error.Stack, "TypeError: board[y] is undefined") || strings.Contains(posted[0].Error.Stack, "<pre>") {
		t.Fatalf("posted = %+v, want only the 500, with the server's text and no markup", posted)
	}
}
