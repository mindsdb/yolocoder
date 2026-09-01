package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/mindsdb/yolocoder/internal/config"
)

// Most OpenAI-compatible providers implement /v1/chat/completions and not
// the Responses API. Rather than teach the agent two dialects, the chat
// dialect is translated at the edge: a responseRequest is converted on the
// way out and the reply is converted back into a responseEnvelope, so
// everything above this file sees one shape.

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Tools          []chatTool    `json:"tools,omitempty"`
	ToolChoice     string        `json:"tool_choice,omitempty"`
	ResponseFormat *chatFormat   `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatCallFunction `json:"function"`
}

type chatCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict,omitempty"`
}

type chatFormat struct {
	Type       string          `json:"type"`
	JSONSchema *chatJSONSchema `json:"json_schema,omitempty"`
}

type chatJSONSchema struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict"`
}

type chatEnvelope struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Error   *apiError    `json:"error,omitempty"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

// toChat converts a Responses-style request into a chat completion.
// dropSchema omits response_format for providers that reject it alongside
// tools, describing the shape in the instructions instead so the reply is
// still JSON we can read.
func toChat(request responseRequest, dropSchema bool) (chatRequest, error) {
	converted := chatRequest{Model: request.Model, ToolChoice: request.ToolChoice}
	instructions := request.Instructions
	if dropSchema && request.Text != nil {
		if hint := schemaHint(request.Text.Format); hint != "" {
			instructions = strings.TrimSpace(instructions + "\n" + hint)
		}
	}
	if instructions != "" {
		converted.Messages = append(converted.Messages, chatMessage{Role: "system", Content: instructions})
	}
	messages, err := chatMessages(request.Input)
	if err != nil {
		return chatRequest{}, err
	}
	converted.Messages = append(converted.Messages, messages...)
	for _, tool := range request.Tools {
		converted.Tools = append(converted.Tools, chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
				Strict:      tool.Strict,
			},
		})
	}
	if request.Text != nil && !dropSchema {
		converted.ResponseFormat = &chatFormat{
			Type: "json_schema",
			JSONSchema: &chatJSONSchema{
				Name:   request.Text.Format.Name,
				Schema: request.Text.Format.Schema,
				Strict: request.Text.Format.Strict,
			},
		}
	}
	return converted, nil
}

// schemaHint describes a schema in a sentence, for providers that won't
// take one alongside tools. Without enforcement the shape has to be asked
// for in words, or the reply comes back in whatever shape the model likes.
func schemaHint(format schemaFormat) string {
	properties, _ := format.Schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return ""
	}
	names := orderedKeys(format.Schema, properties)
	described := make([]string, 0, len(names))
	for _, name := range names {
		described = append(described, name+" ("+jsonTypeOf(properties[name])+")")
	}
	return "Return only a JSON object with exactly these keys: " + strings.Join(described, ", ") + "."
}

// orderedKeys lists a schema's properties in its stated required order,
// so the description matches the order the fields should be written in.
func orderedKeys(schema map[string]any, properties map[string]any) []string {
	var names []string
	seen := map[string]bool{}
	if required, ok := schema["required"].([]string); ok {
		for _, name := range required {
			if _, present := properties[name]; present && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	var rest []string
	for name := range properties {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(names, rest...)
}

func jsonTypeOf(property any) string {
	definition, ok := property.(map[string]any)
	if !ok {
		return "value"
	}
	kind, _ := definition["type"].(string)
	if kind == "array" {
		return "array of strings"
	}
	if kind == "" {
		return "value"
	}
	return kind
}

// chatMessages turns a Responses input — a bare string, or the transcript
// of messages, tool calls and tool outputs — into chat messages.
func chatMessages(input any) ([]chatMessage, error) {
	switch value := input.(type) {
	case nil:
		return nil, nil
	case string:
		return []chatMessage{{Role: "user", Content: value}}, nil
	case []any:
		var messages []chatMessage
		for _, item := range value {
			message, err := chatMessageFor(item)
			if err != nil {
				return nil, err
			}
			messages = append(messages, message...)
		}
		return messages, nil
	default:
		return nil, fmt.Errorf("cannot convert %T to chat messages", input)
	}
}

func chatMessageFor(item any) ([]chatMessage, error) {
	switch value := item.(type) {
	case inputMessage:
		return []chatMessage{{Role: value.Role, Content: value.Content}}, nil
	case responseItem:
		if value.Type != "function_call" {
			return nil, nil
		}
		// An assistant turn that called a tool. The id has to match the
		// tool message that answers it or the provider rejects the pair.
		return []chatMessage{{
			Role: "assistant",
			ToolCalls: []chatToolCall{{
				ID:       value.CallID,
				Type:     "function",
				Function: chatCallFunction{Name: value.Name, Arguments: value.Arguments},
			}},
		}}, nil
	case toolOutput:
		return []chatMessage{{Role: "tool", ToolCallID: value.CallID, Content: value.Output}}, nil
	default:
		return nil, fmt.Errorf("cannot convert %T to a chat message", item)
	}
}

// fromChat converts a chat completion back into the Responses shape the
// rest of the agent reads.
func fromChat(envelope chatEnvelope) responseEnvelope {
	converted := responseEnvelope{ID: envelope.ID, Error: envelope.Error}
	for _, choice := range envelope.Choices {
		for _, call := range choice.Message.ToolCalls {
			converted.Output = append(converted.Output, responseItem{
				Type:      "function_call",
				Name:      call.Function.Name,
				CallID:    call.ID,
				Arguments: call.Function.Arguments,
			})
		}
		if strings.TrimSpace(choice.Message.Content) != "" {
			converted.Output = append(converted.Output, responseItem{
				Type:    "message",
				Content: []contentItem{{Type: "output_text", Text: choice.Message.Content}},
			})
		}
	}
	return converted
}

func chatEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/chat/completions"
	}
	return baseURL + "/v1/chat/completions"
}

// decodeChat parses a chat completion reply.
func decodeChat(body []byte) (chatEnvelope, error) {
	var envelope chatEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return chatEnvelope{}, err
	}
	return envelope, nil
}

// DetectAPI asks an endpoint which dialect it speaks by seeing whether it
// has a Responses route at all. A 404 there means chat completions, which
// is what most OpenAI-compatible providers offer. Anything else — a 200,
// an auth failure, a complaint about the body — means the route exists.
// An endpoint that can't be reached is reported as unknown rather than
// guessed at, since guessing wrong sends the user chasing a fault that
// isn't in their key or model.
func DetectAPI(ctx context.Context, baseURL, apiKey string) (string, error) {
	probe := map[string]any{"model": "probe", "input": "probe"}
	payload, err := json.Marshal(probe)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, responsesEndpoint(baseURL), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("reach %s: %w", baseURL, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
	if response.StatusCode == http.StatusNotFound {
		return config.APIChat, nil
	}
	return config.APIResponses, nil
}
