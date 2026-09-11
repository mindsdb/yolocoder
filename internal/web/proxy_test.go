package web

import (
	"bytes"
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
