package config

import "testing"

func TestMindsHubEndpointRecognition(t *testing.T) {
	t.Setenv(EnvMindsHubDomain, "staging.mindshub.ai")
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://api.mindshub.ai", true},
		{"https://api.mindshub.ai/v1/", true},
		{"https://API.MINDSHUB.AI:443/v1", true},
		{"https://api.staging.mindshub.ai/v1", true},
		{"https://api.example.com/v1", false},
		{"http://api.mindshub.ai", false},
		{"https://api.mindshub.ai:8443", false},
		{"https://api.mindshub.ai.example.com", false},
		{"https://api.mindshub.ai@other.example", false},
		{"https://user@api.mindshub.ai", false},
		{"https://api.mindshub.ai/other/v1", false},
		{"https://api.mindshub.ai?provider=other", false},
		{"https://api.mindshub.ai#other", false},
		{"", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			for _, label := range []string{"mindshub", "environment", "openai-compatible"} {
				provider := LLM{Provider: label, BaseURL: tc.url}
				if got := provider.UsesMindsHub(); got != tc.want {
					t.Fatalf("UsesMindsHub(%q, %q) = %t", label, tc.url, got)
				}
			}
		})
	}
}

func TestMindsHubEnvironmentDefaultsAndExplicitModels(t *testing.T) {
	for _, model := range []string{"", "custom-choice", "mindshub_air"} {
		t.Run(model, func(t *testing.T) {
			values := map[string]string{"OPENAI_BASE_URL": "https://api.mindshub.ai/v1/", "OPENAI_API_KEY": "key", "OPENAI_MODEL": model}
			got, err := FromEnvironment(func(key string) string { return values[key] })
			want := model
			if want == "" {
				want = "muse-spark-1-3"
			}
			if err != nil || got.Model != want || !got.UsesMindsHub() {
				t.Fatalf("FromEnvironment() = %+v, %v; want model %q", got, err, want)
			}
		})
	}
}

func TestSavedMindsHubDefaultUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name              string
		version           int
		base, model, want string
	}{
		{"old default", 1, "https://api.mindshub.ai", "mindshub_air", "muse-spark-1-3"},
		{"old explicit choice", 1, "https://api.mindshub.ai", "chosen-model", "chosen-model"},
		{"new explicit old model", 2, "https://api.mindshub.ai", "mindshub_air", "mindshub_air"},
		{"unset MindsHub model", 1, "https://api.mindshub.ai/v1", "", "muse-spark-1-3"},
		{"other provider", 1, "https://example.com/v1", "mindshub_air", "mindshub_air"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			public, secret, err := Paths()
			if err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(public, settings{Version: tc.version, Provider: "openai-compatible", BaseURL: tc.base, Model: tc.model, API: APIChat, Recall: true, Preselect: true}, 0600); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(secret, credentials{APIKey: "test-key"}, 0600); err != nil {
				t.Fatal(err)
			}
			got, ok, err := Load()
			if err != nil || !ok || got.Model != tc.want || got.APIKey != "test-key" || got.API != APIChat || !got.Recall || !got.Preselect {
				t.Fatalf("Load() = %+v, %t, %v", got, ok, err)
			}
			if err := Save(got); err != nil {
				t.Fatal(err)
			}
			reloaded, ok, err := Load()
			if err != nil || !ok || reloaded != got {
				t.Fatalf("round trip changed settings: %+v, %t, %v", reloaded, ok, err)
			}
		})
	}
}
