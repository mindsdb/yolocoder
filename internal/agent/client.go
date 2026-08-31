package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mindsdb/yolocoder/internal/config"
)

type Client struct {
	endpoint string
	apiKey   string
	model    string
	http     *http.Client
}

type responseRequest struct {
	Model        string         `json:"model"`
	Instructions string         `json:"instructions,omitempty"`
	Input        any            `json:"input"`
	Tools        []functionTool `json:"tools,omitempty"`
	ToolChoice   string         `json:"tool_choice,omitempty"`
	Text         *textConfig    `json:"text,omitempty"`
}

type functionTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

type textConfig struct {
	Format schemaFormat `json:"format"`
}

type schemaFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict"`
}

type responseEnvelope struct {
	ID     string         `json:"id"`
	Output []responseItem `json:"output"`
	Error  *apiError      `json:"error,omitempty"`
}

type apiError struct {
	Message string `json:"message"`
}

type responseItem struct {
	Type      string        `json:"type"`
	Name      string        `json:"name,omitempty"`
	CallID    string        `json:"call_id,omitempty"`
	Arguments string        `json:"arguments,omitempty"`
	Content   []contentItem `json:"content,omitempty"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type toolOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

func NewClient(provider config.LLM) (*Client, error) {
	if strings.TrimSpace(provider.Model) == "" {
		return nil, fmt.Errorf("an LLM model is required; reconnect with `yolocoder config connect` or set OPENAI_MODEL")
	}
	return &Client{
		endpoint: responsesEndpoint(provider.BaseURL),
		apiKey:   provider.APIKey,
		model:    provider.Model,
		http:     http.DefaultClient,
	}, nil
}

func (client *Client) create(ctx context.Context, request responseRequest) (responseEnvelope, error) {
	request.Model = client.model
	payload, err := json.Marshal(request)
	if err != nil {
		return responseEnvelope{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(payload))
	if err != nil {
		return responseEnvelope{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := client.http.Do(httpRequest)
	if err != nil {
		return responseEnvelope{}, fmt.Errorf("call LLM: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return responseEnvelope{}, err
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return responseEnvelope{}, fmt.Errorf("decode LLM response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if envelope.Error != nil && envelope.Error.Message != "" {
			message = envelope.Error.Message
		}
		return responseEnvelope{}, fmt.Errorf("LLM returned %s: %s", response.Status, message)
	}
	return envelope, nil
}

func (response responseEnvelope) calls() []responseItem {
	var calls []responseItem
	for _, item := range response.Output {
		if item.Type == "function_call" {
			calls = append(calls, item)
		}
	}
	return calls
}

func (response responseEnvelope) text() (string, error) {
	for _, item := range response.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" && content.Text != "" {
				return content.Text, nil
			}
		}
	}
	return "", fmt.Errorf("LLM response contained no output text")
}

func responsesEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/responses"
	}
	return baseURL + "/v1/responses"
}

func strictSchema(name string, schema map[string]any) *textConfig {
	return &textConfig{Format: schemaFormat{Type: "json_schema", Name: name, Schema: schema, Strict: true}}
}
