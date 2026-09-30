package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// reasoningServer answers decisions with choice and the coding model with
// replies in turn, recording the reasoning each coding request asked for.
func reasoningServer(t *testing.T, choice string, replies ...string) (*httptest.Server, *[]any, *int) {
	t.Helper()
	var efforts []any
	decisions := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/v1/decisions" {
			decisions++
			fmt.Fprintf(writer, `{"answers":{"effort":{"type":"choice","choice":%q,"confidence":0.9}}}`, choice)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		efforts = append(efforts, body["reasoning"])
		if len(efforts) > len(replies) {
			t.Errorf("unexpected request %d", len(efforts))
			fmt.Fprint(writer, finishes("(unscripted)"))
			return
		}
		fmt.Fprint(writer, replies[len(efforts)-1])
	}))
	return server, &efforts, &decisions
}

func reasoningRunner(server *httptest.Server, files map[string]string, t *testing.T, efforts []string) *Runner {
	client := &Client{endpoint: server.URL + "/v1/responses", baseURL: server.URL, apiKey: "k", model: "m", http: server.Client()}
	client.KnowEfforts(efforts, "medium")
	return NewRunner(client, folder(t, files))
}

// Auto: reading the files needs no reasoning, and the change gets what the
// decision model chose for it.
func TestAutoReasoningIsNoneUntilFilesAreReadThenChosen(t *testing.T) {
	server, efforts, decisions := reasoningServer(t, "low", reads("r", "a.ts"), finishes("Done."))
	defer server.Close()
	runner := reasoningRunner(server, map[string]string{"a.ts": "const x = 1;\n"}, t, []string{"none", "low", "medium", "high"})
	runner.preloadWhole = false
	runner.UseReasoning("")
	progress := &recordingProgress{}
	if _, err := runner.Run(context.Background(), "tidy a.ts", nil, nil, progress); err != nil {
		t.Fatal(err)
	}
	if *decisions != 1 || len(*efforts) != 2 || fmt.Sprint((*efforts)[0]) != "map[effort:none]" || fmt.Sprint((*efforts)[1]) != "map[effort:low]" {
		t.Fatalf("decisions=%d reasoning per request=%v, want none to read and then low", *decisions, *efforts)
	}
	if trail := strings.Join(progress.logs, "\n"); !strings.Contains(trail, "reasoning: low, chosen for this task") {
		t.Fatalf("the trail should say what was chosen:\n%s", trail)
	}
}

// A project sent whole starts with its files in view, so the first round
// is already the change and gets the change's effort.
func TestAFixedReasoningSettingIsUsedWithoutAskingForOne(t *testing.T) {
	server, efforts, decisions := reasoningServer(t, "none", finishes("Done."))
	defer server.Close()
	runner := reasoningRunner(server, map[string]string{"a.ts": "const x = 1;\n"}, t, []string{"none", "low", "medium", "high"})
	runner.UseReasoning("high")
	if _, err := runner.Run(context.Background(), "tidy a.ts", nil, nil, &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	if *decisions != 0 || len(*efforts) != 1 || fmt.Sprint((*efforts)[0]) != "map[effort:high]" {
		t.Fatalf("decisions=%d reasoning=%v, want the setting and no decision", *decisions, *efforts)
	}
}

func TestAModelWithoutReasoningLevelsIsSentNone(t *testing.T) {
	server, efforts, decisions := reasoningServer(t, "low", reads("r", "a.ts"), finishes("Done."))
	defer server.Close()
	runner := reasoningRunner(server, map[string]string{"a.ts": "const x = 1;\n"}, t, nil)
	runner.preloadWhole = false
	runner.UseReasoning("")
	if _, err := runner.Run(context.Background(), "tidy a.ts", nil, nil, &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	if *decisions != 0 || (*efforts)[0] != nil || (*efforts)[1] != nil {
		t.Fatalf("decisions=%d reasoning=%v, want nothing asked and nothing sent", *decisions, *efforts)
	}
}

// MindsHub's Cloudflare rules refuse Go's default User-Agent on
// /v1/decisions, so every request says it is yolocoder.
func TestEveryRequestNamesYolocoder(t *testing.T) {
	var agents []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		agents = append(agents, request.URL.Path+" "+request.UserAgent())
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/models":
			fmt.Fprint(writer, `{"data":[{"id":"m","reasoning_efforts":["none","high"],"default_reasoning_effort":"high"}]}`)
		case "/v1/decisions":
			fmt.Fprint(writer, `{"answers":{"effort":{"choice":"none"}}}`)
		default:
			fmt.Fprint(writer, finishes("Done."))
		}
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL + "/v1/responses", baseURL: server.URL, apiKey: "k", model: "m", http: server.Client()}
	runner := NewRunner(client, folder(t, map[string]string{"a.ts": "x\n"}))
	runner.UseReasoning("")
	if _, err := runner.Run(context.Background(), "hi", nil, nil, &recordingProgress{}); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 3 {
		t.Fatalf("requests = %v, want models, decisions and one coding call", agents)
	}
	for _, agent := range agents {
		if !strings.Contains(agent, " yolocoder/") {
			t.Fatalf("request without a yolocoder User-Agent: %q", agent)
		}
	}
}
