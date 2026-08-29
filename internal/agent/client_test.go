package agent

import "testing"

func TestResponsesEndpoint(t *testing.T) {
	tests := map[string]string{
		"https://api.openai.com/v1": "https://api.openai.com/v1/responses",
		"https://api.example.com":   "https://api.example.com/v1/responses",
		"https://api.example.com/":  "https://api.example.com/v1/responses",
	}
	for input, expected := range tests {
		if actual := responsesEndpoint(input); actual != expected {
			t.Fatalf("responsesEndpoint(%q) = %q, want %q", input, actual, expected)
		}
	}
}
