package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

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

func TestModelsEndpoint(t *testing.T) {
	tests := map[string]string{
		"https://api.mindshub.ai":    "https://api.mindshub.ai/v1/models",
		"https://api.example.com/v1": "https://api.example.com/v1/models",
	}
	for input, expected := range tests {
		if actual := modelsEndpoint(input); actual != expected {
			t.Fatalf("modelsEndpoint(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization = %s", request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"object":"list","data":[{"id":"mindshub_air"},{"id":"mindshub_pro"}]}`)
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), server.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mindshub_air", "mindshub_pro"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("ListModels() = %v, want %v", models, want)
	}
}
