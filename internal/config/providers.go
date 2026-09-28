package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/mindsdb/yolocoder/internal/auth"
)

const EnvMindsHubDomain = "YOLOCODER_MINDSHUB_DOMAIN"

const (
	DefaultMindsHubModel  = "muse-spark-1-3"
	MindsHubDecisionModel = "jev-1.13.0"
)

// UsesMindsHub recognizes the inference endpoint, including connections made
// through environment variables or the custom-provider form. A provider label
// alone must not enable MindsHub-only models on another service.
func (provider LLM) UsesMindsHub() bool {
	endpoint, err := url.Parse(strings.TrimSpace(provider.BaseURL))
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return false
	}
	if port := endpoint.Port(); port != "" && port != "443" {
		return false
	}
	if path := strings.TrimRight(endpoint.Path, "/"); path != "" && path != "/v1" {
		return false
	}
	return strings.EqualFold(endpoint.Hostname(), "api.mindshub.ai") ||
		strings.EqualFold(endpoint.Hostname(), "api."+mindsHubDomain())
}

// WithDefaults keeps explicit model choices and supplies the standard coding
// model when a MindsHub connection has no selection yet.
func (provider LLM) WithDefaults() LLM {
	if strings.TrimSpace(provider.Model) == "" && provider.UsesMindsHub() {
		provider.Model = DefaultMindsHubModel
	}
	return provider
}

func mindsHubDomain() string {
	if domain := strings.TrimSpace(os.Getenv(EnvMindsHubDomain)); domain != "" {
		return domain
	}
	return "mindshub.ai"
}

func MindsHubBaseURL() string {
	return fmt.Sprintf("https://api.%s", mindsHubDomain())
}

func MindsHubAuthAPI() string {
	return fmt.Sprintf("https://auth.%s/v1", mindsHubDomain())
}

func MindsHubOIDC() auth.Config {
	domain := mindsHubDomain()
	return auth.Config{
		Issuer:          fmt.Sprintf("https://auth.%s/auth", domain),
		Realm:           "mindsdb",
		ClientID:        "anton-desktop",
		Scopes:          []string{"openid", "profile", "email"},
		SuccessRedirect: fmt.Sprintf("https://console.%s/settings/organization/billing", domain),
	}
}
