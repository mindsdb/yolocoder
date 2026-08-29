package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const CurrentVersion = 1

type LLM struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
}

type settings struct {
	Version  int    `json:"version"`
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model,omitempty"`
}

type credentials struct {
	APIKey string `json:"api_key"`
}

func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "yolocoder")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config directory: %w", err)
	}
	return dir, nil
}

func Load() (LLM, bool, error) {
	dir, err := Dir()
	if err != nil {
		return LLM{}, false, err
	}
	settingsData, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if os.IsNotExist(err) {
		return LLM{}, false, nil
	}
	if err != nil {
		return LLM{}, false, fmt.Errorf("read configuration: %w", err)
	}
	var saved settings
	if err := json.Unmarshal(settingsData, &saved); err != nil {
		return LLM{}, false, fmt.Errorf("parse configuration: %w", err)
	}
	if saved.Version > CurrentVersion {
		return LLM{}, false, fmt.Errorf("configuration requires a newer YoloCoder version")
	}
	credentialData, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		return LLM{}, false, fmt.Errorf("read credentials: %w", err)
	}
	var secret credentials
	if err := json.Unmarshal(credentialData, &secret); err != nil {
		return LLM{}, false, fmt.Errorf("parse credentials: %w", err)
	}
	return LLM{Provider: saved.Provider, BaseURL: saved.BaseURL, APIKey: secret.APIKey, Model: saved.Model}, true, nil
}

func Save(provider LLM) error {
	baseURL, err := ValidateBaseURL(provider.BaseURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(provider.APIKey) == "" {
		return fmt.Errorf("API key is required")
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	public := settings{Version: CurrentVersion, Provider: provider.Provider, BaseURL: baseURL, Model: provider.Model}
	if err := writeJSON(filepath.Join(dir, "config.json"), public, 0o644); err != nil {
		return fmt.Errorf("save configuration: %w", err)
	}
	if err := writeJSON(filepath.Join(dir, "credentials.json"), credentials{APIKey: provider.APIKey}, 0o600); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return nil
}

func Reset() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	for _, name := range []string{"config.json", "credentials.json"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func FromEnvironment(getenv func(string) string) (LLM, error) {
	baseURL := strings.TrimSpace(getenv("OPENAI_BASE_URL"))
	if baseURL == "" {
		baseURL = strings.TrimSpace(getenv("OPENAI_API_BASE"))
	}
	if baseURL == "" {
		return LLM{}, fmt.Errorf("OPENAI_BASE_URL is required with --llm-from-env-vars")
	}
	baseURL, err := ValidateBaseURL(baseURL)
	if err != nil {
		return LLM{}, fmt.Errorf("OPENAI_BASE_URL: %w", err)
	}
	apiKey := strings.TrimSpace(getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return LLM{}, fmt.Errorf("OPENAI_API_KEY is required with --llm-from-env-vars")
	}
	return LLM{Provider: "environment", BaseURL: baseURL, APIKey: apiKey, Model: strings.TrimSpace(getenv("OPENAI_MODEL"))}, nil
}

func ValidateBaseURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("base URL must include a valid host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("base URL must start with http:// or https://")
	}
	return trimmed, nil
}

func Paths() (string, string, error) {
	dir, err := Dir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, "config.json"), filepath.Join(dir, "credentials.json"), nil
}

func writeJSON(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".yolocoder-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temporaryPath, mode); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
