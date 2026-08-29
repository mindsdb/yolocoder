package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const DefaultTimeout = 3 * time.Minute

type Config struct {
	Issuer          string
	Realm           string
	ClientID        string
	Scopes          []string
	SuccessRedirect string
}

func (config Config) endpoint(name string) string {
	return fmt.Sprintf("%s/realms/%s/protocol/openid-connect/%s", strings.TrimRight(config.Issuer, "/"), config.Realm, name)
}

func Login(ctx context.Context, config Config, notify func(string)) (string, error) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		return "", err
	}
	state, err := randomString(24)
	if err != nil {
		return "", err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("open local callback: %w", err)
	}
	defer listener.Close()
	redirectURI := fmt.Sprintf("http://%s/callback", listener.Addr())
	authorizationURL := config.endpoint("auth") + "?" + url.Values{
		"response_type": {"code"}, "client_id": {config.ClientID}, "redirect_uri": {redirectURI},
		"scope": {strings.Join(config.Scopes, " ")}, "state": {state}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/callback" {
			http.NotFound(writer, request)
			return
		}
		query := request.URL.Query()
		if query.Get("state") != state {
			results <- result{err: fmt.Errorf("sign-in state mismatch")}
			return
		}
		if providerError := query.Get("error"); providerError != "" {
			results <- result{err: fmt.Errorf("provider returned %s", providerError)}
			return
		}
		code := query.Get("code")
		if code == "" {
			results <- result{err: fmt.Errorf("provider returned no authorization code")}
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if config.SuccessRedirect != "" {
			fmt.Fprintf(writer, `<meta http-equiv="refresh" content="2;url=%s"><p>Signed in. Returning to MindsHub...</p>`, html.EscapeString(config.SuccessRedirect))
		} else {
			fmt.Fprint(writer, "<p>Signed in. You can close this tab.</p>")
		}
		results <- result{code: code}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	notify(authorizationURL)
	_ = openBrowser(authorizationURL)
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("timed out waiting for sign-in")
	case completed := <-results:
		if completed.err != nil {
			return "", completed.err
		}
		return exchangeCode(ctx, config, completed.code, redirectURI, verifier)
	}
}

func CreateAPIKey(ctx context.Context, baseURL, accessToken, name string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"name": name})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api-keys/", strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("request API key: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("API key endpoint returned %s", response.Status)
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Key == "" {
		return "", fmt.Errorf("API key response contained no key")
	}
	return created.Key, nil
}

func exchangeCode(ctx context.Context, config Config, code, redirectURI, verifier string) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}, "client_id": {config.ClientID}, "code_verifier": {verifier}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.endpoint("token"), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("exchange authorization code: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %s", response.Status)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil || token.AccessToken == "" {
		return "", fmt.Errorf("token response contained no access token")
	}
	return token.AccessToken, nil
}

func pkcePair() (string, string, error) {
	verifier, err := randomString(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomString(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

var openBrowser = func(target string) error {
	command := "xdg-open"
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	}
	return exec.Command(command, append(args, target)...).Start()
}
