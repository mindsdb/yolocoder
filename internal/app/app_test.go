package app

import (
	"testing"

	"github.com/mindsdb/yolocoder/internal/config"
)

func TestRunModelSetsModelDirectly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := config.Save(config.LLM{Provider: "mindshub", BaseURL: "https://api.mindshub.ai", APIKey: "secret", Model: "old-model"}); err != nil {
		t.Fatal(err)
	}
	if code := RunModel([]string{"new-model"}); code != 0 {
		t.Fatalf("RunModel() = %d", code)
	}
	provider, configured, err := config.Load()
	if err != nil || !configured {
		t.Fatalf("Load() = %+v, %v, %v", provider, configured, err)
	}
	if provider.Model != "new-model" {
		t.Fatalf("Model = %q, want %q", provider.Model, "new-model")
	}
}

func TestRunModelRequiresConfiguredProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if code := RunModel([]string{"new-model"}); code == 0 {
		t.Fatal("expected a failure with no saved provider")
	}
}
