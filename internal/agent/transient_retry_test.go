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

type retryReply struct {
	status  int
	body    string
	err     error
	readErr error
	onClose func()
}

type retryRequest struct {
	body   string
	url    string
	header http.Header
	ctx    context.Context
}

type retryBody struct {
	io.Reader
	close func()
}

func (b retryBody) Close() error { b.close(); return nil }

type retryReadError struct{ err error }

func (r retryReadError) Read([]byte) (int, error) { return 0, r.err }

// The transport is synchronous and local: no provider or socket is involved.
// Captured contexts and bytes distinguish an HTTP replay from a new model round.
func retryScript(t *testing.T, replies ...retryReply) (*changeSession, *[]retryRequest) {
	t.Helper()
	var seen []retryRequest
	closed := true
	client := &Client{endpoint: "https://example.invalid/v1/responses", baseURL: "https://example.invalid", model: "muse-test", apiKey: "test-only"}
	client.http = &http.Client{Transport: wholeContextTransport(func(r *http.Request) (*http.Response, error) {
		if !closed {
			t.Error("previous response body is still open")
		}
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, retryRequest{string(body), r.URL.String(), r.Header.Clone(), r.Context()})
		if len(seen) > len(replies) {
			t.Errorf("unexpected request %d", len(seen))
			return nil, errors.New("unscripted request")
		}
		reply := replies[len(seen)-1]
		if reply.err != nil {
			return nil, reply.err
		}
		closed = false
		var reader io.Reader = strings.NewReader(reply.body)
		if reply.readErr != nil {
			reader = io.MultiReader(reader, retryReadError{reply.readErr})
		}
		return &http.Response{StatusCode: reply.status, Status: fmt.Sprintf("%d status", reply.status), Header: make(http.Header), Body: retryBody{reader, func() {
			closed = true
			if reply.onClose != nil {
				reply.onClose()
			}
		}}}, nil
	})}
	runner := NewRunner(client, folder(t, map[string]string{"a.txt": "old\n"}))
	runner.smallEditModel = "muse-test"
	return runner.newChangeSession("change the file", "a.txt", nil, nil), &seen
}

func TestTransientRetryReplaysBytesAndAppliesOnlyAcceptedTool(t *testing.T) {
	for _, status := range []int{502, 524} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			// Even plausible tool output inside an eligible error is never dispatched.
			session, seen := retryScript(t,
				retryReply{status: status, body: editsAndFinishes("unaccepted", "@a.txt\n-old\n+wrong\n", "wrong")},
				retryReply{status: 200, body: editsAndFinishes("accepted", "@a.txt\n-old\n+new\n", "done")},
			)
			root := session.runner.repository.Root
			if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"test":"node -e \"require('fs').appendFileSync('checks','x')\""}}`), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			progress := &recordingProgress{}
			outcome, err := session.work(ctx, progress)
			content, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			checks, _ := os.ReadFile(filepath.Join(root, "checks"))
			if err != nil || !outcome.Applied || outcome.Attempts != 1 || string(content) != "new\n" || string(checks) != "x" || len(*seen) != 2 {
				t.Fatalf("outcome=%+v err=%v content=%q checks=%q requests=%d", outcome, err, content, checks, len(*seen))
			}
			first, second := (*seen)[0], (*seen)[1]
			if first.body != second.body || first.url != second.url || !reflect.DeepEqual(first.header, second.header) || first.ctx != second.ctx {
				t.Fatal("replay changed encoded request, headers, endpoint or context")
			}
			if got, ok := second.ctx.Deadline(); !ok || got != deadlineOf(ctx) {
				t.Fatal("replay extended the parent deadline")
			}
			var request responseRequest
			_ = json.Unmarshal([]byte(first.body), &request)
			if request.Model != "muse-test" || session.used["apply_diff"] != 1 || strings.Count(strings.Join(progress.logs, "\n"), "transient retry:") != 1 {
				t.Fatalf("wrong model, quota or trace: %s %v %v", request.Model, session.used, progress.logs)
			}
			if !strings.Contains(strings.Join(progress.logs, "\n"), fmt.Sprintf("dispatch status=%d attempt=1", status)) {
				t.Fatalf("retry marker reports wrong status: %v", progress.logs)
			}
		})
	}
}

func deadlineOf(ctx context.Context) time.Time { deadline, _ := ctx.Deadline(); return deadline }

func TestTransientRetryIsOnceAcrossRounds(t *testing.T) {
	session, seen := retryScript(t,
		retryReply{status: 524, body: "error code: 524"},
		retryReply{status: 200, body: reads("read", "a.txt")},
		retryReply{status: 502, body: "second failure"},
	)
	_, err := session.work(context.Background(), &recordingProgress{})
	if err == nil || !strings.Contains(err.Error(), "second failure") || len(*seen) != 3 || session.used["read_files"] != 1 || !session.transientRetryUsed {
		t.Fatalf("err=%v requests=%d quota=%v", err, len(*seen), session.used)
	}
}

func TestTransientRetryExcludesOtherErrorsAndSmallUI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   retryReply
		smallUI bool
	}{
		{"small UI 524", retryReply{status: 524, body: "error code: 524"}, true},
		{"small UI 502", retryReply{status: 502, body: "gateway error"}, true},
		{"budget refusal", retryReply{status: 429, body: "Evaluation request limit reached"}, false},
		{"bad request", retryReply{status: 400, body: "bad request"}, false},
		{"server error", retryReply{status: 500, body: "error"}, false},
		{"authentication", retryReply{status: 401, body: "error"}, false},
		{"billing", retryReply{status: 402, body: "error"}, false},
		{"authorization", retryReply{status: 403, body: "error"}, false},
		{"unavailable", retryReply{status: 503, body: "error"}, false},
		{"gateway timeout", retryReply{status: 504, body: "error"}, false},
		{"transport", retryReply{err: io.ErrUnexpectedEOF}, false},
		{"deadline", retryReply{err: context.DeadlineExceeded}, false},
		{"partial 502 body", retryReply{status: 502, body: "partial", readErr: io.ErrUnexpectedEOF}, false},
		{"capped 502 body", retryReply{status: 502, body: strings.Repeat("x", 8<<20)}, false},
		{"partial 524 body", retryReply{status: 524, body: "partial", readErr: io.ErrUnexpectedEOF}, false},
		{"capped 524 body", retryReply{status: 524, body: strings.Repeat("x", 8<<20)}, false},
		{"partial successful body", retryReply{status: 200, body: "partial", readErr: io.ErrUnexpectedEOF}, false},
		{"malformed successful JSON", retryReply{status: 200, body: "{"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, seen := retryScript(t, tc.reply)
			session.prefetched = tc.smallUI
			_, err := session.create(context.Background(), responseRequest{Input: "hello"}, &recordingProgress{})
			if err == nil || len(*seen) != 1 || session.transientRetryUsed {
				t.Fatalf("err=%v requests=%d retried=%t", err, len(*seen), session.transientRetryUsed)
			}
		})
	}
}

func TestTransientRetryCancelAfterBodyReadDoesNotResubmit(t *testing.T) {
	for _, status := range []int{502, 524} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			session, seen := retryScript(t, retryReply{status: status, body: "error code: 524", onClose: cancel})
			progress := &recordingProgress{}
			_, err := session.create(ctx, responseRequest{Input: "hello"}, progress)
			if err == nil || len(*seen) != 1 || session.transientRetryUsed || len(progress.logs) != 0 {
				t.Fatalf("err=%v requests=%d retried=%t logs=%v", err, len(*seen), session.transientRetryUsed, progress.logs)
			}
		})
	}
}

func TestTransientRetryLeavesSuccessAndOtherClientCallersUnchanged(t *testing.T) {
	for _, small := range []bool{false, true} {
		session, seen := retryScript(t, retryReply{status: 200, body: finishes("done")})
		session.prefetched = small
		response, err := session.create(context.Background(), responseRequest{Input: "hello"}, &recordingProgress{})
		text, _ := response.text()
		if err != nil || text != "done" || len(*seen) != 1 || session.transientRetryUsed {
			t.Fatalf("small=%t err=%v text=%q count=%d", small, err, text, len(*seen))
		}
	}
	for _, status := range []int{502, 524} {
		session, seen := retryScript(t, retryReply{status: status, body: "error"})
		_, err := session.runner.client.create(context.Background(), responseRequest{Input: "not a coding session"})
		if err == nil || len(*seen) != 1 {
			t.Fatalf("non-session caller unexpectedly retried: err=%v requests=%d", err, len(*seen))
		}
	}
}

func TestTransientRetrySharesTokenThroughCompatibilityRecursion(t *testing.T) {
	for _, mode := range []string{"dialect", "tools", "schema"} {
		for _, before := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retry-before=%t", mode, before), func(t *testing.T) {
				compat := retryReply{status: 400, body: `{"error":{"message":"Failed to generate tool call but tool_choice = required"}}`}
				if mode == "dialect" {
					compat = retryReply{status: 404}
				}
				if mode == "schema" {
					compat.body = `{"error":{"message":"response_format incompatible with tools"}}`
				}
				failure := retryReply{status: 524, body: "error code: 524"}
				success := retryReply{status: 200, body: finishes("done")}
				if mode != "tools" {
					success.body = `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`
				}
				otherFailure := retryReply{status: 502, body: "gateway error"}
				replies := []retryReply{compat, failure, success, otherFailure}
				if before {
					replies = []retryReply{otherFailure, compat, failure}
				}
				session, seen := retryScript(t, replies...)
				client := session.runner.client
				client.autoDialect = mode == "dialect"
				client.chat = mode == "schema"
				if client.chat {
					client.endpoint = chatEndpoint(client.baseURL)
				}
				request := responseRequest{Input: "hello", Tools: repositoryTools(false), ToolChoice: "auto"}
				if mode == "schema" {
					request.Text = strictSchema("file_rewrite", rewriteSchema())
				}
				_, err := session.create(context.Background(), request, &recordingProgress{})
				if before {
					if err == nil || len(*seen) != 3 {
						t.Fatalf("err=%v requests=%d", err, len(*seen))
					}
					if (*seen)[0].body != (*seen)[1].body {
						t.Fatal("gateway replay changed bytes")
					}
				} else {
					if err != nil || len(*seen) != 3 {
						t.Fatalf("err=%v requests=%d", err, len(*seen))
					}
					if (*seen)[1].body != (*seen)[2].body {
						t.Fatal("post-adaptation replay changed bytes")
					}
					_, err = session.create(context.Background(), responseRequest{Input: "next round"}, &recordingProgress{})
					if err == nil || len(*seen) != 4 {
						t.Fatal("compatibility recursion reset turn retry")
					}
				}
			})
		}
	}
}

func TestTransientRetryStillRunsCheckAndRepair(t *testing.T) {
	for _, status := range []int{502, 524} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			session, seen := retryScript(t,
				retryReply{status: status, body: "error code: 524"},
				retryReply{status: 200, body: editsAndFinishes("bad", "@a.txt\n-old\n+bad\n", "done")},
				retryReply{status: 200, body: editsAndFinishes("repair", "@a.txt\n-bad\n+good\n", "fixed")},
			)
			root := session.runner.repository.Root
			check := `{"scripts":{"test":"node -e \"const f=require('fs');f.appendFileSync('checks','x');process.exit(f.readFileSync('a.txt','utf8').includes('good')?0:1)\""}}`
			if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(check), 0600); err != nil {
				t.Fatal(err)
			}
			outcome, err := session.work(context.Background(), &recordingProgress{})
			checks, _ := os.ReadFile(filepath.Join(root, "checks"))
			if err != nil || !outcome.Applied || outcome.Attempts != 2 || string(checks) != "xx" || session.used["apply_diff"] != 2 || len(*seen) != 3 {
				t.Fatalf("outcome=%+v err=%v checks=%q quota=%v count=%d", outcome, err, checks, session.used, len(*seen))
			}
			if !strings.Contains((*seen)[2].body, "project's check failed") {
				t.Fatal("failed check evidence was not returned for repair")
			}
		})
	}
}

func TestTransientRetryDoesNotResetEditOrRoundBudgets(t *testing.T) {
	t.Run("four charged edits", func(t *testing.T) {
		replies := []retryReply{{status: 524, body: "error"}}
		for n := 0; n < 5; n++ {
			replies = append(replies, retryReply{status: 200, body: edits(fmt.Sprint(n), "@a.txt\n-absent\n+bad\n")})
		}
		replies = append(replies, retryReply{status: 200, body: finishes("unable to complete")})
		session, seen := retryScript(t, replies...)
		outcome, err := session.work(context.Background(), &recordingProgress{})
		if err != nil || outcome.Applied || session.used["apply_diff"] != 4 || len(*seen) != 7 || !strings.Contains((*seen)[6].body, "limit") {
			t.Fatalf("outcome=%+v err=%v quota=%v count=%d final=%s", outcome, err, session.used, len(*seen), (*seen)[len(*seen)-1].body)
		}
	})
	t.Run("twenty rounds", func(t *testing.T) {
		replies := []retryReply{{status: 524, body: "error"}}
		for n := 0; n < 20; n++ {
			replies = append(replies, retryReply{status: 200, body: reads(fmt.Sprint(n), "a.txt")})
		}
		session, seen := retryScript(t, replies...)
		_, err := session.work(context.Background(), &recordingProgress{})
		if !errors.Is(err, errOutOfRounds) || len(*seen) != 21 || session.used["read_files"] != 8 {
			t.Fatalf("err=%v count=%d quota=%v", err, len(*seen), session.used)
		}
	})
	t.Run("recorder refusal after last forwarded slot", func(t *testing.T) {
		// Equivalent response sequence when the first request uses slot 30;
		// the existing external recorder refuses forwarding the replay.
		session, seen := retryScript(t, retryReply{status: 502, body: "error"}, retryReply{status: 429, body: "Evaluation request limit reached"})
		_, err := session.work(context.Background(), &recordingProgress{})
		if err == nil || !strings.Contains(err.Error(), "429") || len(*seen) != 2 {
			t.Fatalf("err=%v count=%d", err, len(*seen))
		}
	})
}

func TestTransientRetryTokenResetsForNewTurn(t *testing.T) {
	session, seen := retryScript(t,
		retryReply{status: 524, body: "error"}, retryReply{status: 200, body: finishes("done")},
		retryReply{status: 524, body: "error"}, retryReply{status: 200, body: finishes("done")},
	)
	for i := 0; i < 2; i++ {
		if _, err := session.work(context.Background(), &recordingProgress{}); err != nil {
			t.Fatal(err)
		}
		session = session.runner.newChangeSession("new turn", "a.txt", nil, nil)
	}
	if len(*seen) != 4 {
		t.Fatalf("requests=%d", len(*seen))
	}
}

type retryCancelProgress struct{ cancel context.CancelFunc }

func (p retryCancelProgress) Status(string) {}
func (p retryCancelProgress) Log(string)    { p.cancel() }

func TestTransientRetryCancellationAtDispatchBoundary(t *testing.T) {
	for _, status := range []int{502, 524} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			session, seen := retryScript(t, retryReply{status: status, body: "error"})
			_, err := session.create(ctx, responseRequest{Input: "hello"}, retryCancelProgress{cancel})
			if !errors.Is(err, context.Canceled) || len(*seen) != 1 {
				t.Fatalf("err=%v count=%d", err, len(*seen))
			}
		})
	}
}

func TestTransientRetryMixedStatusesShareOneAllowance(t *testing.T) {
	for _, first := range []int{502, 524} {
		for _, second := range []int{502, 524} {
			t.Run(fmt.Sprintf("%d-then-%d", first, second), func(t *testing.T) {
				session, seen := retryScript(t,
					retryReply{status: first, body: "first gateway failure"},
					retryReply{status: second, body: "second gateway failure"},
				)
				progress := &recordingProgress{}
				_, err := session.create(context.Background(), responseRequest{Input: "same request"}, progress)
				if err == nil || !strings.Contains(err.Error(), fmt.Sprint(second)) || len(*seen) != 2 || !session.transientRetryUsed {
					t.Fatalf("err=%v requests=%d used=%t", err, len(*seen), session.transientRetryUsed)
				}
				if (*seen)[0].body != (*seen)[1].body || len(progress.logs) != 1 || !strings.Contains(progress.logs[0], fmt.Sprintf("status=%d", first)) {
					t.Fatalf("replay or marker mismatch: %v", progress.logs)
				}
			})
		}
	}
}
