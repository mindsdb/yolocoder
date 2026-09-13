package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
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
	writeSelfHealingPage(recorder, http.StatusBadGateway, "dev server is not reachable yet: dial tcp refused")
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "dial tcp refused") {
		t.Fatalf("body = %s, want the specific error message included", body)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}
}
