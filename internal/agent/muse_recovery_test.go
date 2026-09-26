package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mindsdb/yolocoder/internal/repo"
)

const musePolicyMessage = "The model policy is temporarily unavailable. Please retry shortly."

func museRecoveryScript(t *testing.T, replies ...retryReply) (*changeSession, *[]retryRequest) {
	t.Helper()
	session, seen := retryScript(t, replies...)
	session.runner.client.model = "muse-spark-1-3"
	session.runner.smallEditModel = "muse-spark-1-3"
	return session, seen
}

func TestMuseRecoveryReplaysBeforeToolsAndPreservesFinalCheck(t *testing.T) {
	for _, small := range []bool{false, true} {
		t.Run(fmt.Sprintf("small=%t", small), func(t *testing.T) {
			bad := editsAndFinishes("unaccepted", "@a.txt\n-old\n+wrong\n", "wrong")
			var errorReply map[string]any
			_ = json.Unmarshal([]byte(bad), &errorReply)
			errorReply["error"] = map[string]string{"message": musePolicyMessage}
			body, _ := json.Marshal(errorReply)
			success := editsAndFinishes("accepted", "@a.txt\n-old\n+new\n", "done")
			if small {
				call := replacementCall([]repo.Replacement{{Path: "a.txt", Old: "old", New: "new"}})
				success = calls(call.Name, call.CallID, call.Arguments)
			}
			session, seen := museRecoveryScript(t, retryReply{status: 503, body: string(body)}, retryReply{status: 200, body: success})
			session.prefetched = small
			root := session.runner.repository.Root
			if small {
				if _, err := session.runner.readFiles([]string{"a.txt"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"test":"node -e \"require('fs').appendFileSync('checks','x')\""}}`), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			progress := &recordingProgress{}
			outcome, err := session.work(ctx, progress)
			content, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			checks, _ := os.ReadFile(filepath.Join(root, "checks"))
			if err != nil || !outcome.Applied || outcome.Attempts != 1 || session.used["apply_diff"] != 1 || string(content) != "new\n" || string(checks) != "x" || len(*seen) != 2 {
				t.Fatalf("outcome=%+v err=%v quota=%v content=%q checks=%q requests=%d", outcome, err, session.used, content, checks, len(*seen))
			}
			first, second := (*seen)[0], (*seen)[1]
			if first.body != second.body || first.url != second.url || !reflect.DeepEqual(first.header, second.header) || first.ctx != second.ctx || deadlineOf(second.ctx) != deadlineOf(ctx) {
				t.Fatal("replay changed bytes, headers, URL, context or deadline")
			}
			if strings.Count(strings.Join(progress.logs, "\n"), "transient retry: dispatch status=503 attempt=1") != 1 {
				t.Fatalf("wrong replay marker: %v", progress.logs)
			}
		})
	}
}

func TestMuseRecoveryExactErrorAndDialect(t *testing.T) {
	for _, chat := range []bool{false, true} {
		for _, body := range []string{musePolicyMessage, " \n" + musePolicyMessage + "\t", `{"error":{"message":"` + musePolicyMessage + `"}}`} {
			t.Run(fmt.Sprintf("chat=%t/%s", chat, body), func(t *testing.T) {
				success := finishes("done")
				if chat {
					success = `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`
				}
				session, seen := museRecoveryScript(t, retryReply{status: 503, body: body}, retryReply{status: 200, body: success})
				session.runner.client.chat = chat
				if chat {
					session.runner.client.endpoint = chatEndpoint(session.runner.client.baseURL)
				}
				request := responseRequest{Instructions: "keep all input", Input: []any{inputMessage{Role: "user", Content: "hello", Images: []string{"data:image/png;base64,AA=="}}}, Tools: repositoryTools(false), ToolChoice: "auto", Text: strictSchema("file_rewrite", rewriteSchema())}
				response, err := session.create(context.Background(), request, &recordingProgress{})
				text, _ := response.text()
				if err != nil || text != "done" || len(*seen) != 2 || (*seen)[0].body != (*seen)[1].body || !strings.Contains((*seen)[1].body, "data:image/png;base64,AA==") || !strings.Contains((*seen)[1].body, "file_rewrite") {
					t.Fatalf("err=%v text=%q requests=%v", err, text, *seen)
				}
			})
		}
	}
}

func TestMuseRecoveryRejectsOtherOrIncompleteErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply retryReply
		model string
	}{
		{"generic 503", retryReply{status: 503, body: "unavailable"}, ""},
		{"prefix", retryReply{status: 503, body: "Error: " + musePolicyMessage}, ""},
		{"suffix", retryReply{status: 503, body: musePolicyMessage + " Later."}, ""},
		{"case", retryReply{status: 503, body: strings.ToLower(musePolicyMessage)}, ""},
		{"JSON message whitespace", retryReply{status: 503, body: `{"error":{"message":" ` + musePolicyMessage + `"}}`}, ""},
		{"different JSON shape", retryReply{status: 503, body: `{"message":"` + musePolicyMessage + `"}`}, ""},
		{"trailing JSON", retryReply{status: 503, body: `{"error":{"message":"` + musePolicyMessage + `"}}{}`}, ""},
		{"partial body", retryReply{status: 503, body: musePolicyMessage, readErr: io.ErrUnexpectedEOF}, ""},
		{"capped body", retryReply{status: 503, body: musePolicyMessage + strings.Repeat(" ", (8<<20)-len(musePolicyMessage))}, ""},
		{"transport", retryReply{err: io.ErrUnexpectedEOF}, ""},
		{"auth", retryReply{status: 401, body: musePolicyMessage}, ""},
		{"billing", retryReply{status: 402, body: musePolicyMessage}, ""},
		{"forbidden", retryReply{status: 403, body: musePolicyMessage}, ""},
		{"cap refusal", retryReply{status: 429, body: musePolicyMessage}, ""},
		{"other model", retryReply{status: 503, body: musePolicyMessage}, "jev-1.13.0"},
		{"model prefix", retryReply{status: 503, body: musePolicyMessage}, "muse-spark-1-3-preview"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, seen := museRecoveryScript(t, tc.reply)
			if tc.model != "" {
				session.runner.client.model = tc.model
			}
			_, err := session.create(context.Background(), responseRequest{Input: "hello"}, &recordingProgress{})
			if err == nil || len(*seen) != 1 || session.transientRetryUsed {
				t.Fatalf("err=%v requests=%d used=%t", err, len(*seen), session.transientRetryUsed)
			}
		})
	}
}

func TestMuseRecoverySharesTurnTokenAcrossRoundsAndStatuses(t *testing.T) {
	for _, pair := range [][2]int{{503, 503}, {503, 502}, {503, 524}, {502, 503}, {524, 503}} {
		t.Run(fmt.Sprint(pair), func(t *testing.T) {
			session, seen := museRecoveryScript(t,
				retryReply{status: pair[0], body: musePolicyMessage}, retryReply{status: 200, body: reads("read", "a.txt")},
				retryReply{status: pair[1], body: musePolicyMessage})
			_, err := session.work(context.Background(), &recordingProgress{})
			if err == nil || len(*seen) != 3 || session.used["read_files"] != 1 || !session.transientRetryUsed {
				t.Fatalf("err=%v requests=%d quota=%v", err, len(*seen), session.used)
			}
		})
	}
}

func TestMuseRecoverySmallWriterFallbackKeepsToken(t *testing.T) {
	for _, first := range []retryReply{{status: 500, body: "writer failed"}, {status: 503, body: musePolicyMessage}} {
		t.Run(fmt.Sprint(first.status), func(t *testing.T) {
			replies := []retryReply{first, {status: 503, body: musePolicyMessage}, {status: 200, body: finishes("done")}, {status: 503, body: musePolicyMessage}}
			if first.status == 503 {
				// First Muse writer uses the token, fails again, then falls back
				// to the primary Muse client; fallback must not create a new token.
				replies = []retryReply{first, first, first}
			}
			session, seen := museRecoveryScript(t, replies...)
			session.prefetched = true
			writer := *session.runner.client
			if first.status == 500 {
				writer.model = "other-writer"
			}
			session.writer = &writer
			_, err := session.create(context.Background(), responseRequest{Input: "hello"}, &recordingProgress{})
			if first.status == 500 {
				if err != nil || len(*seen) != 3 || (*seen)[1].body != (*seen)[2].body {
					t.Fatalf("fallback replay err=%v requests=%d", err, len(*seen))
				}
				_, err = session.create(context.Background(), responseRequest{Input: "next round"}, &recordingProgress{})
			}
			if err == nil || len(*seen) != len(replies) || !session.transientRetryUsed || session.writer != session.runner.client {
				t.Fatalf("err=%v requests=%d used=%t", err, len(*seen), session.transientRetryUsed)
			}
		})
	}
}

func TestMuseRecoveryCancellationAndNonSessionScope(t *testing.T) {
	for _, small := range []bool{false, true} {
		for _, atDispatch := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			reply := retryReply{status: 503, body: musePolicyMessage}
			var progress Progress = &recordingProgress{}
			if atDispatch {
				progress = retryCancelProgress{cancel}
			} else {
				reply.onClose = cancel
			}
			session, seen := museRecoveryScript(t, reply)
			session.prefetched = small
			_, err := session.create(ctx, responseRequest{Input: "hello"}, progress)
			cancel()
			if err == nil || len(*seen) != 1 || ctx.Err() == nil {
				t.Fatalf("small=%t dispatch=%t err=%v requests=%d", small, atDispatch, err, len(*seen))
			}
		}
	}
	session, seen := museRecoveryScript(t, retryReply{status: 503, body: musePolicyMessage})
	_, err := session.runner.client.create(context.Background(), responseRequest{Input: "outside coding session"})
	if err == nil || len(*seen) != 1 {
		t.Fatalf("non-session call retried: err=%v requests=%d", err, len(*seen))
	}
}
