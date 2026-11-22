package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Some providers cap how long a request may run without streaming
// ("This request exceeded the 110s non-streaming budget. Retry with
// "stream": true, or lower max_tokens."). A long patch can take longer
// than that to write, so once a provider says so, this client streams
// every request for the rest of the run. The stream is gathered back into
// the reply the provider would have sent without streaming, so nothing
// above post sees the difference.

// asksForStreaming recognizes a provider refusing a request only because
// it was not streamed.
func asksForStreaming(body []byte) bool {
	text := strings.ToLower(string(body))
	return strings.Contains(text, "non-streaming") ||
		strings.Contains(text, "streaming is required") ||
		strings.Contains(text, `"stream": true`) ||
		strings.Contains(text, `\"stream\": true`)
}

// isEventStream reports whether a reply body is server-sent events rather
// than one JSON document. Error replies to a streamed request are usually
// plain JSON, so the body is checked, not the request.
func isEventStream(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return true
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:"))
}

// streamEvents returns the data payload of each event, in order, without
// the closing "[DONE]".
func streamEvents(body []byte) []string {
	var events []string
	var data []string
	flush := func() {
		if len(data) > 0 {
			if joined := strings.Join(data, "\n"); joined != "[DONE]" {
				events = append(events, joined)
			}
			data = nil
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64<<10), len(body)+1)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	flush()
	return events
}

// gatherStream turns a streamed reply into the JSON body the same request
// would have returned without streaming, in this client's dialect.
func (client *Client) gatherStream(body []byte) ([]byte, error) {
	if client.chat {
		return gatherChatStream(body)
	}
	return gatherResponsesStream(body)
}

// gatherResponsesStream takes the response the Responses API sends whole
// in its closing event. If that event lists no output, the items the
// stream finished one by one are used instead.
func gatherResponsesStream(body []byte) ([]byte, error) {
	var final json.RawMessage
	var items []json.RawMessage
	for _, data := range streamEvents(body) {
		var event struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Item     json.RawMessage `json:"item"`
			Message  string          `json:"message"`
			Error    *apiError       `json:"error"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		switch event.Type {
		case "response.completed", "response.incomplete", "response.failed":
			final = event.Response
		case "response.output_item.done":
			items = append(items, event.Item)
		case "error":
			message := event.Message
			if event.Error != nil && event.Error.Message != "" {
				message = event.Error.Message
			}
			return json.Marshal(map[string]any{"error": apiError{Message: message}})
		}
	}
	if final == nil {
		return nil, fmt.Errorf("the streamed reply ended before the response was complete")
	}
	var response map[string]any
	if err := json.Unmarshal(final, &response); err != nil {
		return nil, err
	}
	if output, _ := response["output"].([]any); len(output) == 0 && len(items) > 0 {
		response["output"] = items
	}
	return json.Marshal(response)
}

// gatherChatStream joins chat completion chunks: content deltas are
// concatenated, and tool calls are assembled by index from their id,
// name and argument fragments.
func gatherChatStream(body []byte) ([]byte, error) {
	type choiceState struct {
		content strings.Builder
		calls   map[int]*chatToolCall
		finish  string
	}
	envelope := chatEnvelope{}
	choices := map[int]*choiceState{}
	sawChunk := false
	for _, data := range streamEvents(body) {
		var chunk struct {
			ID      string     `json:"id"`
			Error   *apiError  `json:"error"`
			Usage   *chatUsage `json:"usage"`
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		sawChunk = true
		if chunk.Error != nil {
			return json.Marshal(chatEnvelope{Error: chunk.Error})
		}
		if chunk.ID != "" {
			envelope.ID = chunk.ID
		}
		if chunk.Usage != nil {
			envelope.Usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			state := choices[choice.Index]
			if state == nil {
				state = &choiceState{calls: map[int]*chatToolCall{}}
				choices[choice.Index] = state
			}
			state.content.WriteString(choice.Delta.Content)
			for _, fragment := range choice.Delta.ToolCalls {
				call := state.calls[fragment.Index]
				if call == nil {
					call = &chatToolCall{Type: "function"}
					state.calls[fragment.Index] = call
				}
				if fragment.ID != "" {
					call.ID = fragment.ID
				}
				call.Function.Name += fragment.Function.Name
				call.Function.Arguments += fragment.Function.Arguments
			}
			if choice.FinishReason != "" {
				state.finish = choice.FinishReason
			}
		}
	}
	if !sawChunk {
		return nil, fmt.Errorf("the streamed reply carried no chunks")
	}
	for _, index := range sortedKeys(choices) {
		state := choices[index]
		message := chatMessage{Role: "assistant"}
		if text := state.content.String(); text != "" {
			message.Content = text
		}
		for _, callIndex := range sortedKeys(state.calls) {
			message.ToolCalls = append(message.ToolCalls, *state.calls[callIndex])
		}
		envelope.Choices = append(envelope.Choices, chatChoice{Message: message, FinishReason: state.finish})
	}
	return json.Marshal(envelope)
}

func sortedKeys[V any](values map[int]V) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	return keys
}
