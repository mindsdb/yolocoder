package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const routePolicyMessage = "The model policy is temporarily unavailable. Please retry shortly."

func routeRecoveryScript(t *testing.T, replies ...retryReply) (*Client, *[]retryRequest, *int) {
	t.Helper()
	var seen []retryRequest
	closed := 0
	client := &Client{baseURL: "https://example.invalid", apiKey: "test-only"}
	client.http = &http.Client{Transport: wholeContextTransport(func(request *http.Request) (*http.Response, error) {
		if closed != len(seen) {
			t.Error("previous response was not closed before replay")
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, retryRequest{string(body), request.URL.String(), request.Header.Clone(), request.Context()})
		if len(seen) > len(replies) {
			t.Error("unscripted request")
			return nil, errors.New("unscripted request")
		}
		reply := replies[len(seen)-1]
		if reply.err != nil {
			return nil, reply.err
		}
		var reader io.Reader = strings.NewReader(reply.body)
		if reply.readErr != nil {
			reader = io.MultiReader(reader, retryReadError{reply.readErr})
		}
		return &http.Response{StatusCode: reply.status, Header: make(http.Header), Body: retryBody{reader, func() {
			closed++
			if reply.onClose != nil {
				reply.onClose()
			}
		}}}, nil
	})}
	return client, &seen, &closed
}

func TestEditRouterPolicyRecoveryReplaysSameRequestOnce(t *testing.T) {
	for name, body := range map[string]string{
		"plain": " \n" + routePolicyMessage + "\n",
		"json":  `{"error":{"message":"` + routePolicyMessage + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			reply := strings.ReplaceAll(routerReply("normal", .99, 1), "jev-test", "jev-1.13.0")
			client, seen, closed := routeRecoveryScript(t, retryReply{status: 503, body: body}, retryReply{status: 200, body: reply})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			answers, usage, err := client.selectEditFiles(ctx, "jev-1.13.0", "Change the file", []string{"app.tsx"})
			if err != nil || !answers["route"].accepts("normal") || usage.TotalTokens != 24 || len(*seen) != 2 || *closed != 2 {
				t.Fatalf("answers=%v usage=%+v err=%v requests=%d closed=%d", answers, usage, err, len(*seen), *closed)
			}
			if !reflect.DeepEqual((*seen)[0], (*seen)[1]) || (*seen)[0].ctx != ctx || (*seen)[0].url != "https://example.invalid/v1/decisions" {
				t.Fatal("replay changed bytes, headers, endpoint, context or deadline")
			}
		})
	}
}

func TestEditRouterPolicyRecoveryExclusionsAndSingleAllowance(t *testing.T) {
	for _, test := range []struct {
		name, model string
		replies     []retryReply
		wantCalls   int
	}{
		{"second refusal", "jev-1.13.0", []retryReply{{status: 503, body: routePolicyMessage}, {status: 503, body: routePolicyMessage}}, 2},
		{"other model", "other", []retryReply{{status: 503, body: routePolicyMessage}}, 1},
		{"generic unavailable", "jev-1.13.0", []retryReply{{status: 503, body: "Service unavailable"}}, 1},
		{"substring", "jev-1.13.0", []retryReply{{status: 503, body: routePolicyMessage + " unrelated"}}, 1},
		{"malformed JSON", "jev-1.13.0", []retryReply{{status: 503, body: `{"error":{"message":"` + routePolicyMessage + `"}`}}, 1},
		{"partial body", "jev-1.13.0", []retryReply{{status: 503, body: routePolicyMessage, readErr: io.ErrUnexpectedEOF}}, 1},
		{"capped body", "jev-1.13.0", []retryReply{{status: 503, body: routePolicyMessage + strings.Repeat(" ", (8<<10)-len(routePolicyMessage))}}, 1},
		{"transport", "jev-1.13.0", []retryReply{{err: errors.New("connection reset")}}, 1},
		{"malformed success", "jev-1.13.0", []retryReply{{status: 200, body: routePolicyMessage}}, 1},
		{"unauthorized", "jev-1.13.0", []retryReply{{status: 401, body: routePolicyMessage}}, 1},
		{"billing", "jev-1.13.0", []retryReply{{status: 402, body: routePolicyMessage}}, 1},
		{"forbidden", "jev-1.13.0", []retryReply{{status: 403, body: routePolicyMessage}}, 1},
		{"bad gateway", "jev-1.13.0", []retryReply{{status: 502, body: routePolicyMessage}}, 1},
		{"gateway timeout", "jev-1.13.0", []retryReply{{status: 524, body: routePolicyMessage}}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, seen, closed := routeRecoveryScript(t, test.replies...)
			_, _, err := client.selectEditFiles(context.Background(), test.model, "task", []string{"app.tsx"})
			if err == nil || len(*seen) != test.wantCalls {
				t.Fatalf("err=%v calls=%d", err, len(*seen))
			}
			wantClosed := test.wantCalls
			if test.replies[0].err != nil {
				wantClosed = 0
			}
			if *closed != wantClosed {
				t.Fatalf("closed=%d want=%d", *closed, wantClosed)
			}
		})
	}
}

func TestEditRouterPolicyRecoveryCancellationDeclinesWithoutContext(t *testing.T) {
	for _, name := range []string{"parent canceled before", "parent canceled on refusal", "parent deadline during refusal"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "parent deadline during refusal" {
				ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
				defer cancel()
			}
			if name == "parent canceled before" {
				cancel()
			}
			client, seen, _ := routeRecoveryScript(t, retryReply{status: 503, body: routePolicyMessage, onClose: func() {
				if name == "parent deadline during refusal" {
					<-ctx.Done()
				} else {
					cancel()
				}
			}})
			runner := NewRunner(client, folder(t, map[string]string{"app.tsx": "export const x = 1;"}))
			runner.editRouterModel = "jev-1.13.0"
			session := runner.newChangeSession("Change the file", "", nil, nil)
			runner.prefetchEdit(ctx, session, []string{"app.tsx"}, &recordingProgress{})
			wantCalls := 1
			if name == "parent canceled before" {
				wantCalls = 0
			}
			if ctx.Err() == nil || len(*seen) != wantCalls || len(session.transcript) != 1 || len(runner.served) != 0 || session.used["read_files"] != 0 {
				t.Fatalf("canceled refusal changed context: err=%v calls=%d transcript=%d", ctx.Err(), len(*seen), len(session.transcript))
			}
		})
	}
}

func TestEditRouterPolicyRecoveryPreservesBothWritersAndChecks(t *testing.T) {
	for _, route := range []string{"normal", "small_ui"} {
		t.Run(route, func(t *testing.T) {
			repository := folder(t, map[string]string{
				"app.tsx":      "export const title = 'Hello';\n",
				"package.json": `{"scripts":{"test":"node -e \"require('fs').appendFileSync('checked','x')\""}}`,
			})
			var routes, coding int
			var first retryRequest
			closed := false
			client := &Client{baseURL: "https://example.invalid", endpoint: "https://example.invalid/v1/responses", apiKey: "test-only", model: "primary"}
			client.http = &http.Client{Transport: wholeContextTransport(func(request *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(request.Body)
				if request.URL.Path == "/v1/decisions" {
					routes++
					current := retryRequest{string(body), request.URL.String(), request.Header.Clone(), request.Context()}
					if routes == 1 {
						first = current
						deadline, ok := request.Context().Deadline()
						if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > editRouteTimeout {
							t.Error("missing original bounded router deadline")
						}
						return &http.Response{StatusCode: 503, Body: retryBody{strings.NewReader(routePolicyMessage), func() { closed = true }}}, nil
					}
					if routes != 2 || !closed || !reflect.DeepEqual(first, current) {
						t.Error("router replay did not preserve request or close refusal")
					}
					reply := strings.ReplaceAll(routerReply(route, .99, 1), "jev-test", "jev-1.13.0")
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply))}, nil
				}
				coding++
				var requestBody responseRequest
				if err := json.Unmarshal(body, &requestBody); err != nil {
					t.Fatal(err)
				}
				instructions := changeInstructions
				if route == "small_ui" {
					instructions = smallEditInstructions
				}
				if requestBody.Model != "primary" || requestBody.Instructions != instructions || !strings.Contains(string(body), "export const title") {
					t.Error("recovered route changed coding model/instructions or omitted prefetched context")
				}
				assertCompletionSchema(t, requestBody.Tools, route == "normal")
				assertNeededReadTool(t, requestBody.Tools, route == "normal")
				patch := "@app.tsx\n-export const title = 'Hello';\n+export const title = 'Welcome';\n"
				reply := editsAndFinishes("edit", patch, "Updated the heading.")
				if route == "small_ui" {
					args, _ := json.Marshal(map[string]string{"patch": patch, "response_comment_for_user": "Updated the heading."})
					reply = calls("apply_diff", "edit", string(args))
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply))}, nil
			})}
			runner := NewRunner(client, repository)
			runner.editRouterModel = "jev-1.13.0"
			outcome, err := runner.Run(context.Background(), "Change the heading to Welcome", nil, nil, &recordingProgress{})
			content, _ := repository.ReadFile("app.tsx")
			checks, _ := os.ReadFile(filepath.Join(repository.Root, "checked"))
			if err != nil || !outcome.Applied || outcome.Attempts != 1 || routes != 2 || coding != 1 || content != "export const title = 'Welcome';\n" || string(checks) != "x" {
				t.Fatalf("route=%s outcome=%+v err=%v requests=%d/%d source=%q checks=%q", route, outcome, err, routes, coding, content, checks)
			}
			if outcome.Usage.TotalTokens != 24 {
				t.Fatal(fmt.Sprintf("unexpected routing usage: %+v", outcome.Usage))
			}
		})
	}
}
