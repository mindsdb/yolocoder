package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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
	want := []ModelInfo{{ID: "mindshub_air"}, {ID: "mindshub_pro"}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("ListModels() = %v, want %v", models, want)
	}
}

func TestListModelsCapturesOwnedByAndGroupsByIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"data":[
			{"id":"llama-3-70b","owned_by":"meta"},
			{"id":"gpt-oss-120b","owned_by":"openai"},
			{"id":"mixtral-8x7b","owned_by":"mistralai"}
		]}`)
	}))
	defer server.Close()

	models, err := ListModels(context.Background(), server.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by OwnedBy first, so entries sharing a provider sit together.
	want := []ModelInfo{
		{ID: "llama-3-70b", OwnedBy: "meta"},
		{ID: "mixtral-8x7b", OwnedBy: "mistralai"},
		{ID: "gpt-oss-120b", OwnedBy: "openai"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("ListModels() = %+v, want %+v", models, want)
	}
}

func TestCreateReportsTheStatusBeforeTryingToParse(t *testing.T) {
	// A 404 with an empty body is what an endpoint without the Responses
	// API returns. Parsing first turned that into "unexpected end of JSON
	// input", which sent the user looking at their key and model.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL + "/v1/responses", apiKey: "k", model: "m", http: server.Client()}
	_, err := client.create(context.Background(), responseRequest{Input: "hi"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "end of JSON input") {
		t.Fatalf("err = %v, want the status, not a parse failure", err)
	}
	for _, want := range []string{"404", "does not implement the OpenAI Responses API"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to mention %q", err, want)
		}
	}
}

func TestCreateSurfacesAProviderErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(writer, `{"error":{"message":"Wrong API Key"}}`)
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	_, err := client.create(context.Background(), responseRequest{Input: "hi"})
	if err == nil || !strings.Contains(err.Error(), "Wrong API Key") {
		t.Fatalf("err = %v, want the provider's own message", err)
	}
}

func TestProviderThatDemandsAToolCallIsAskedAgainWithoutTools(t *testing.T) {
	// Some providers pin tool_choice to "required" on their own account —
	// we only ever send "auto" — and then fail when the model would rather
	// answer directly, which is what a message needing no files does.
	var sawTools []bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		tools, _ := body["tools"].([]any)
		sawTools = append(sawTools, len(tools) > 0)
		writer.Header().Set("Content-Type", "application/json")
		if len(tools) > 0 {
			writer.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(writer, `{"message":"Failed to generate tool_calls. Exception: Failed to generate tool call but tool_choice = 'required'.","type":"generation_error","code":"parser_error"}`)
			return
		}
		fmt.Fprint(writer, `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"{\"answer\":\"Hi there!\"}"}]}]}`)
	}))
	defer server.Close()

	client := &Client{endpoint: server.URL, apiKey: "k", model: "m", http: server.Client()}
	response, err := client.create(context.Background(), responseRequest{
		Input:      "hi",
		Tools:      repositoryTools(false),
		ToolChoice: "auto",
	})
	if err != nil {
		t.Fatalf("the turn should survive the provider's demand: %v", err)
	}
	if len(sawTools) != 2 || !sawTools[0] || sawTools[1] {
		t.Fatalf("tools per request = %v, want the retry to drop them", sawTools)
	}
	text, err := response.text()
	if err != nil || !strings.Contains(text, "Hi there!") {
		t.Fatalf("text = %q, %v", text, err)
	}
}
