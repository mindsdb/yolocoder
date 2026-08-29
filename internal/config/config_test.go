package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := LLM{Provider: "openai-compatible", BaseURL: "https://llm.example/v1/", APIKey: "secret", Model: "test-model"}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, configured, err := Load()
	if err != nil || !configured {
		t.Fatalf("Load() = %+v, %v, %v", got, configured, err)
	}
	want.BaseURL = "https://llm.example/v1"
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
	_, credentialsPath, _ := Paths()
	info, err := os.Stat(credentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials permissions = %o", info.Mode().Perm())
	}
	if filepath.Dir(credentialsPath) != filepath.Join(os.Getenv("HOME"), ".config", "yolocoder") {
		t.Fatalf("credentials path = %s", credentialsPath)
	}
	configPath, _, _ := Paths()
	publicConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(publicConfig, []byte(want.APIKey)) {
		t.Fatal("public configuration contains the API key")
	}
}

func TestFromEnvironment(t *testing.T) {
	values := map[string]string{
		"OPENAI_BASE_URL": "https://api.example/v1",
		"OPENAI_API_KEY":  "key",
		"OPENAI_MODEL":    "model",
	}
	got, err := FromEnvironment(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	want := LLM{Provider: "environment", BaseURL: "https://api.example/v1", APIKey: "key", Model: "model"}
	if got != want {
		t.Fatalf("FromEnvironment() = %+v, want %+v", got, want)
	}
}

func TestFromEnvironmentRequiresOpenAIValues(t *testing.T) {
	if _, err := FromEnvironment(func(string) string { return "" }); err == nil {
		t.Fatal("expected missing environment variables to fail")
	}
}

func TestMindsHubDomainOverride(t *testing.T) {
	t.Setenv(EnvMindsHubDomain, "staging.mindshub.ai")
	if got := MindsHubBaseURL(); got != "https://api.staging.mindshub.ai" {
		t.Fatalf("MindsHubBaseURL() = %s", got)
	}
	if got := MindsHubAuthAPI(); got != "https://auth.staging.mindshub.ai/v1" {
		t.Fatalf("MindsHubAuthAPI() = %s", got)
	}
}
