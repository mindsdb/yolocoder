package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/mindsdb/yolocoder/internal/auth"
)

const EnvMindsHubDomain = "YOLOCODER_MINDSHUB_DOMAIN"

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
