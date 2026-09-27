package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mindsdb/yolocoder/internal/httpclient"
)

// Sign-in talks to MindsHub twice after the browser step: once to trade
// the code for a token, once to mint the API key. Both name YoloCoder.
func TestSignInCallsNameYoloCoder(t *testing.T) {
	agents := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents[r.URL.Path] = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/realms/mindsdb/protocol/openid-connect/token":
			fmt.Fprint(w, `{"access_token":"token"}`)
		case "/api-keys/":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"key":"key"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	if _, err := exchangeCode(ctx, Config{Issuer: server.URL, Realm: "mindsdb", ClientID: "c"}, "code", "http://127.0.0.1/callback", "verifier"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateAPIKey(ctx, server.URL, "token", "yolocoder"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/realms/mindsdb/protocol/openid-connect/token", "/api-keys/"} {
		if agents[path] != httpclient.UserAgent() {
			t.Fatalf("%s: User-Agent = %q, want %q", path, agents[path], httpclient.UserAgent())
		}
	}
}
