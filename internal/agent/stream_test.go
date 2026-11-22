package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mindsdb/yolocoder/internal/config"
)

const nonStreamingBudget = `{"type":"error","error":{"type":"overloaded_error","message":"This request exceeded the 110s non-streaming budget. Retry with \"stream\": true, or lower max_tokens."}}`

// budgetServer refuses any request that is not streamed, the way the
// provider with a 110s non-streaming budget did, and answers streamed
// requests with events.
func budgetServer(t *testing.T, events string, requests *[]map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		*requests = append(*requests, decoded)
		if decoded["stream"] != true {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(writer, nonStreamingBudget)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, events)
	}))
}

func TestChatStreamsOnceTheProviderAsksForIt(t *testing.T) {
	events := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Fixing \"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"it.\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"apply_diff\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"patch\\\":\"}}]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"x\\\"}\"}}]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n" +
		"data: [DONE]\n\n"
	var requests []map[string]any
	server := budgetServer(t, events, &requests)
	defer server.Close()
	client, err := NewClient(config.LLM{BaseURL: server.URL, Model: "m", API: config.APIChat})
	if err != nil {
		t.Fatal(err)
	}
	for turn := 0; turn < 2; turn++ {
		response, err := client.create(context.Background(), responseRequest{Input: "fix it"})
		if err != nil {
			t.Fatal(err)
		}
		calls := response.calls()
		if len(calls) != 1 || calls[0].Name != "apply_diff" || calls[0].CallID != "call_1" || calls[0].Arguments != `{"patch":"x"}` {
			t.Fatalf("calls = %+v", calls)
		}
		if len(response.Output) != 2 || response.Output[1].Content[0].Text != "Fixing it." {
			t.Fatalf("output = %+v", response.Output)
		}
		if usage := response.usage(); usage.InputTokens != 10 || usage.OutputTokens != 5 {
			t.Fatalf("usage = %+v", usage)
		}
	}
	// One refused request, then every request streams without being told again.
	if len(requests) != 3 || requests[0]["stream"] != nil || requests[1]["stream"] != true || requests[2]["stream"] != true {
		t.Fatalf("requests = %v", requests)
	}
	if options, _ := requests[1]["stream_options"].(map[string]any); options["include_usage"] != true {
		t.Fatalf("stream_options = %v", requests[1]["stream_options"])
	}
}

func TestResponsesStreamsOnceTheProviderAsksForIt(t *testing.T) {
	events := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Done.\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Done.\"}]}],\"usage\":{\"input_tokens\":7,\"output_tokens\":2,\"total_tokens\":9}}}\n\n"
	var requests []map[string]any
	server := budgetServer(t, events, &requests)
	defer server.Close()
	client, err := NewClient(config.LLM{BaseURL: server.URL, Model: "m", API: config.APIResponses})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.create(context.Background(), responseRequest{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "r1" || len(response.Output) != 1 || response.Output[0].Content[0].Text != "Done." || response.usage().OutputTokens != 2 {
		t.Fatalf("response = %+v", response)
	}
	if len(requests) != 2 || requests[1]["stream"] != true {
		t.Fatalf("requests = %v", requests)
	}
}

func TestStreamedErrorIsReported(t *testing.T) {
	var requests []map[string]any
	server := budgetServer(t, "data: {\"type\":\"error\",\"message\":\"model is overloaded\"}\n\n", &requests)
	defer server.Close()
	client, _ := NewClient(config.LLM{BaseURL: server.URL, Model: "m", API: config.APIResponses})
	response, err := client.create(context.Background(), responseRequest{Input: "hi"})
	if err == nil && (response.Error == nil || response.Error.Message != "model is overloaded") {
		t.Fatalf("response = %+v, err = %v", response, err)
	}
}

func TestAsksForStreaming(t *testing.T) {
	if !asksForStreaming([]byte(nonStreamingBudget)) {
		t.Fatal("did not recognize the non-streaming budget error")
	}
	if asksForStreaming([]byte(`{"error":{"message":"rate limited"}}`)) {
		t.Fatal("took a rate limit for a streaming request")
	}
}
