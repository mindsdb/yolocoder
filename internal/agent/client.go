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
	"github.com/mindsdb/yolocoder/internal/debug"
)

type Client struct {
	endpoint string
	baseURL  string
	apiKey   string
	model    string
	// chat selects the /v1/chat/completions dialect, which most
	// OpenAI-compatible providers implement instead of the Responses API.
	chat bool
	// dropSchema means this provider rejected response_format alongside
	// tools, so the shape is asked for in the instructions instead.
	dropSchema bool
	// autoDialect means the saved provider didn't record which API the
	// endpoint speaks, so a 404 on the Responses route is taken as the
	// answer rather than an error. Configs saved before the dialect was
	// recorded would otherwise keep failing until reconnected by hand.
	autoDialect bool
	http        *http.Client
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
	Status string         `json:"status,omitempty"`
	Output []responseItem `json:"output"`
	Error  *apiError      `json:"error,omitempty"`
	Usage  *responseUsage `json:"usage,omitempty"`
	// IncompleteDetails is why a response stopped before finishing. The
	// case that matters here is the model running out of output room
	// (reason "max_output_tokens" on the Responses API, finish_reason
	// "length" on chat completions, mapped into this same shape by
	// fromChat): the reply carries no usable text, and asking again,
	// shorter, can still finish the turn.
	IncompleteDetails *incompleteDetails `json:"incomplete_details,omitempty"`
}

type incompleteDetails struct {
	Reason string `json:"reason"`
}

// responseUsage is the Responses API's own token accounting shape.
// chat.go's fromChat converts the /v1/chat/completions shape into this
// same one, so usage() below is the one place that reads it regardless
// of which dialect the provider actually speaks.
type responseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// usage reads this response's token accounting, normalized regardless of
// dialect. Zero when the provider didn't report it at all.
func (response responseEnvelope) usage() Usage {
	if response.Usage == nil {
		return Usage{}
	}
	return Usage{
		InputTokens:  response.Usage.InputTokens,
		CachedTokens: response.Usage.InputTokensDetails.CachedTokens,
		OutputTokens: response.Usage.OutputTokens,
		TotalTokens:  response.Usage.TotalTokens,
	}
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
	client := &Client{
		endpoint:    responsesEndpoint(provider.BaseURL),
		baseURL:     provider.BaseURL,
		apiKey:      provider.APIKey,
		model:       provider.Model,
		autoDialect: strings.TrimSpace(provider.API) == "",
		http:        http.DefaultClient,
	}
	if provider.API == config.APIChat {
		client.endpoint = chatEndpoint(provider.BaseURL)
		client.chat = true
	}
	return client, nil
}

// Dialect is the API this client ended up speaking, which callers persist
// so a later run skips rediscovering it.
func (client *Client) Dialect() string {
	if client.chat {
		return config.APIChat
	}
	return config.APIResponses
}

func (client *Client) create(ctx context.Context, request responseRequest) (responseEnvelope, error) {
	request.Model = client.model
	body := any(request)
	if client.chat {
		converted, err := toChat(request, client.dropSchema)
		if err != nil {
			return responseEnvelope{}, err
		}
		body = converted
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return responseEnvelope{}, err
	}
	// The request body carries no credentials (the key travels in the
	// Authorization header), so it is safe to trace.
	label := requestLabel(request)
	debug.Log("REQUEST "+label, string(payload))
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
	replyBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return responseEnvelope{}, err
	}
	debug.Log(fmt.Sprintf("RESPONSE %s (%s)", label, response.Status), string(replyBody))

	// Check the status before parsing. Decoding first turned a 404 with
	// an empty body into "decode LLM response: unexpected end of JSON
	// input", which says nothing about the endpoint being wrong.
	// A 404 on the Responses route from a provider that never told us
	// which API it speaks is the answer to that question, not a failure:
	// switch to chat completions and ask again.
	if response.StatusCode == http.StatusNotFound && client.autoDialect && !client.chat {
		debug.Log("DIALECT", "the Responses API returned 404; switching to /v1/chat/completions")
		client.chat = true
		client.endpoint = chatEndpoint(client.baseURL)
		return client.create(ctx, request)
	}

	// Some providers constrain generation to a tool call whenever tools
	// are offered, then fail outright when the model would rather answer
	// directly — which is exactly what a message needing no files does.
	// Asking again with no tools at all gives it what it was trying to
	// do, and is far better than failing the turn over it.
	if len(request.Tools) > 0 && failedToGenerateToolCall(replyBody) {
		debug.Log("TOOLS", "the provider demanded a tool call the model would not make; asking again without tools")
		request.Tools = nil
		request.ToolChoice = ""
		return client.create(ctx, request)
	}

	// Some providers won't take tool definitions and a JSON schema in the
	// same request. Ask again without the schema, describing the shape in
	// the instructions instead, and keep doing that for this run.
	if client.chat && !client.dropSchema && rejectsSchemaWithTools(replyBody) {
		debug.Log("SCHEMA", "the provider rejects response_format alongside tools; describing the shape in the instructions instead")
		client.dropSchema = true
		return client.create(ctx, request)
	}

	envelope, parseErr := client.decode(replyBody)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseEnvelope{}, client.statusError(response.StatusCode, response.Status, replyBody, envelope)
	}
	if parseErr != nil {
		return responseEnvelope{}, fmt.Errorf("decode LLM response: %w: %s", parseErr, snippet(string(replyBody)))
	}
	return envelope, nil
}

// decode reads a reply in whichever dialect this client speaks, returning
// it in the Responses shape the rest of the agent works with.
func (client *Client) decode(body []byte) (responseEnvelope, error) {
	if client.chat {
		chat, err := decodeChat(body)
		if err != nil {
			return responseEnvelope{}, err
		}
		return fromChat(chat), nil
	}
	var envelope responseEnvelope
	err := json.Unmarshal(body, &envelope)
	return envelope, err
}

// failedToGenerateToolCall recognizes a provider that pinned tool_choice
// to "required" on its own account — we only ever send "auto" — and then
// errored because the model produced an ordinary reply instead of a tool
// call. MindsHub: "Failed to generate tool call but tool_choice =
// 'required'."
//
// This is not a retry of a flaky request: repeating it unchanged would
// fail the same way. It is a signal that the turn does not need a tool at
// all, so the retry drops them.
func failedToGenerateToolCall(body []byte) bool {
	text := strings.ToLower(string(body))
	return strings.Contains(text, "tool_choice") && strings.Contains(text, "failed to generate tool call")
}

// rejectsSchemaWithTools recognizes a provider refusing a request that
// carries both tool definitions and a JSON schema, which several
// OpenAI-compatible endpoints do (Cerebras: "\"tools\" is incompatible
// with \"response_format\"").
func rejectsSchemaWithTools(body []byte) bool {
	text := strings.ToLower(string(body))
	if !strings.Contains(text, "response_format") {
		return false
	}
	for _, complaint := range []string{"incompatible", "not supported", "cannot be used", "can not be used", "unsupported"} {
		if strings.Contains(text, complaint) {
			return true
		}
	}
	return false
}

// statusError explains a non-2xx reply. A 404 in particular almost always
// means the endpoint doesn't implement the Responses API rather than
// anything being wrong with the request, and saying so saves the user
// hunting through their key and model name for a fault that isn't there.
func (client *Client) statusError(code int, status string, body []byte, envelope responseEnvelope) error {
	message := strings.TrimSpace(string(body))
	if envelope.Error != nil && envelope.Error.Message != "" {
		message = envelope.Error.Message
	}
	if code == http.StatusNotFound {
		return fmt.Errorf("LLM returned %s for %s\n\n"+
			"That endpoint does not implement the OpenAI Responses API, which is what YoloCoder speaks. "+
			"Many providers only offer /v1/chat/completions. Check the provider's docs for a Responses API "+
			"endpoint, or connect one that has it with `yolocoder config connect`.", status, client.endpoint)
	}
	if message == "" {
		message = "(no response body)"
	}
	return fmt.Errorf("LLM returned %s: %s", status, message)
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

// cutOff reports whether the model ran out of output room before it
// finished, rather than deciding anything. The reply then carries no
// usable text — an empty message, or none at all — and the turn should
// ask again, shorter, instead of failing over it.
func (response responseEnvelope) cutOff() bool {
	if response.IncompleteDetails == nil {
		return false
	}
	return response.IncompleteDetails.Reason == "max_output_tokens"
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
	if response.cutOff() {
		return "", fmt.Errorf("LLM reply was cut off: the model ran out of output room before it finished")
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

// requestLabel names a request by the schema it asks for, which is what
// distinguishes the route, plan, patch and rewrite calls in a trace.
func requestLabel(request responseRequest) string {
	if request.Text != nil && request.Text.Format.Name != "" {
		return request.Text.Format.Name
	}
	return "response"
}

func modelsEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/models"
	}
	return baseURL + "/v1/models"
}

// ModelInfo is one entry from an endpoint's GET /v1/models listing.
// OwnedBy is the OpenAI-compatible spec's own field for who owns the
// model; a multi-vendor gateway (Groq, Together, Fireworks and similar)
// typically fills it in with the real upstream vendor ("meta", "google",
// "mistralai"...), which is what lets a model picker group entries by
// provider. A single-vendor endpoint (MindsHub included) usually leaves
// it empty, in which case there is nothing to group by.
type ModelInfo struct {
	ID      string
	OwnedBy string
}

// ListModels queries an endpoint's OpenAI-compatible GET /v1/models listing
// and returns what it offers, sorted by OwnedBy then ID so entries that
// share a provider already sit together.
func ListModels(ctx context.Context, baseURL, apiKey string) ([]ModelInfo, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsEndpoint(baseURL), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("list models: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode models list: %w", err)
	}
	models := make([]ModelInfo, 0, len(envelope.Data))
	for _, item := range envelope.Data {
		if item.ID != "" {
			models = append(models, ModelInfo{ID: item.ID, OwnedBy: strings.TrimSpace(item.OwnedBy)})
		}
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].OwnedBy != models[j].OwnedBy {
			return models[i].OwnedBy < models[j].OwnedBy
		}
		return models[i].ID < models[j].ID
	})
	return models, nil
}

func strictSchema(name string, schema map[string]any) *textConfig {
	return &textConfig{Format: schemaFormat{Type: "json_schema", Name: name, Schema: schema, Strict: true}}
}
