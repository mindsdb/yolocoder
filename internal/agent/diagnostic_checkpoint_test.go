//go:build darwin || linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func diagnosticScript(t *testing.T, session *changeSession, script string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"scripts": map[string]string{"test": "node diagnostic.cjs"}})
	for name, body := range map[string]string{"package.json": string(data), "diagnostic.cjs": script} {
		if err := session.runner.repository.Write(name, body); err != nil {
			t.Fatal(err)
		}
	}
}

func diagnosticInput(t *testing.T, request retryRequest) string {
	t.Helper()
	var decoded responseRequest
	if err := json.Unmarshal([]byte(request.body), &decoded); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(decoded.Input)
	return string(b)
}

func TestDiagnosticCheckpointDrainsBatchOnceAndStillChecksFinal(t *testing.T) {
	session, seen := retryScript(t,
		retryReply{status: 200, body: batchReplies(t, edits("a", "@a.txt\n-old\n+first\n"), edits("b", "@b.txt\n-old\n+first\n"))},
		retryReply{status: 200, body: edits("again", "@a.txt\n-first\n+second\n")},
		retryReply{status: 200, body: editsAndFinishes("done", "@a.txt\n-second\n+final\n", "Finished.")})
	session.runner.repository.Write("b.txt", "old\n")
	diagnosticScript(t, session, `const fs=require('fs');const a=fs.readFileSync('a.txt','utf8').trim();const b=fs.readFileSync('b.txt','utf8').trim();fs.appendFileSync('checked',a+':'+b+'\n');console.log('both='+a+':'+b);`)
	progress := &recordingProgress{}
	out, err := session.work(context.Background(), progress)
	checked, _ := os.ReadFile(filepath.Join(session.runner.repository.Root, "checked"))
	if err != nil || out.Reply != "Finished." || len(*seen) != 3 || out.Attempts != 4 || string(checked) != "first:first\nfinal:first\n" {
		t.Fatalf("out=%+v err=%v requests=%d checked=%q", out, err, len(*seen), checked)
	}
	input := diagnosticInput(t, (*seen)[1])
	for _, want := range []string{"Diagnostic checkpoint passed", "does not establish task completion", "both=first:first", `"call_id":"a"`, `"call_id":"b"`} {
		if !strings.Contains(input, want) {
			t.Fatalf("missing %q in %s", want, input)
		}
	}
	if strings.Count(strings.Join(progress.logs, "\n"), "diagnostic checkpoint:") != 1 || strings.Count(strings.Join(progress.logs, "\n"), "  check passed") != 1 {
		t.Fatal(progress.logs)
	}
}

func TestDiagnosticCheckpointFailureDoesNotReplaceFinalRepair(t *testing.T) {
	session, seen := retryScript(t,
		retryReply{status: 200, body: edits("bad", "@a.txt\n-old\n+bad\n")},
		retryReply{status: 200, body: editsAndFinishes("still", "@a.txt\n-bad\n+stillbad\n", "Wrong.")},
		retryReply{status: 200, body: editsAndFinishes("fix", "@a.txt\n-stillbad\n+good\n", "Fixed.")})
	diagnosticScript(t, session, `const fs=require('fs');const value=fs.readFileSync('a.txt','utf8').trim();fs.appendFileSync('checked',value+'\n');if(value!=='good'){console.error('EXPECTED_GOOD');process.exit(1)}`)
	progress := &recordingProgress{}
	out, err := session.work(context.Background(), progress)
	checked, _ := os.ReadFile(filepath.Join(session.runner.repository.Root, "checked"))
	if err != nil || out.Reply != "Fixed." || out.Attempts != 3 || len(*seen) != 3 || string(checked) != "bad\nstillbad\ngood\n" {
		t.Fatalf("out=%+v err=%v checked=%q", out, err, checked)
	}
	if input := diagnosticInput(t, (*seen)[1]); !strings.Contains(input, "Diagnostic checkpoint failed") || !strings.Contains(input, "EXPECTED_GOOD") {
		t.Fatal(input)
	}
	if input := diagnosticInput(t, (*seen)[2]); !strings.Contains(input, "project's check failed") {
		t.Fatal(input)
	}
	if strings.Count(strings.Join(progress.logs, "\n"), "diagnostic checkpoint:") != 1 {
		t.Fatal(progress.logs)
	}
}

func TestDiagnosticCheckpointExclusions(t *testing.T) {
	for _, which := range []string{"failed sibling", "malformed metadata", "rejected", "cutoff", "typed complete", "small UI", "last read", "last round"} {
		t.Run(which, func(t *testing.T) {
			first := edits("a", "@a.txt\n-old\n+new\n")
			tail := []retryReply{{status: 200, body: finishes("Finished after review.")}}
			switch which {
			case "failed sibling":
				first = batchReplies(t, reads("bad", "absent.txt"), first)
			case "malformed metadata":
				first = calls("apply_diff", "bad", `{"patch":"@a.txt\n-old\n+new\n","task_complete":"false"}`)
			case "rejected":
				first = edits("bad", "@a.txt\n-absent\n+new\n")
			case "cutoff":
				first = incompleteReply(t, first, "max_output_tokens")
			case "typed complete":
				first = editsAndFinishes("a", "@a.txt\n-old\n+new\n", "Finished.")
				tail = nil
			case "last read":
				first = batchReplies(t, first, reads("last", "a.txt"))
			}
			replies := append([]retryReply{{status: 200, body: first}}, tail...)
			if which == "last round" {
				replies = nil
				for i := 0; i < maxRounds-1; i++ {
					replies = append(replies, retryReply{status: 200, body: cutOff()})
				}
				replies = append(replies, retryReply{status: 200, body: first})
			}
			session, seen := retryScript(t, replies...)
			session.prefetched = which == "small UI"
			diagnosticScript(t, session, `require('fs').appendFileSync('checked','x')`)
			progress := &recordingProgress{}
			_, err := session.work(context.Background(), progress)
			if which == "last round" {
				if !errors.Is(err, errOutOfRounds) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if session.checkpointUsed || strings.Contains(strings.Join(progress.logs, "\n"), "diagnostic checkpoint:") {
				t.Fatalf("ineligible checkpoint: %v", progress.logs)
			}
			for _, req := range *seen {
				if strings.Contains(diagnosticInput(t, req), "Diagnostic checkpoint ") {
					t.Fatal("ineligible diagnostic in request")
				}
			}
		})
	}
}

func TestDiagnosticCheckpointSkippedAndBoundedOutput(t *testing.T) {
	t.Run("no command", func(t *testing.T) {
		session, seen := retryScript(t, retryReply{status: 200, body: edits("a", "@a.txt\n-old\n+new\n")}, retryReply{status: 200, body: finishes("Still incomplete.")})
		out, err := session.work(context.Background(), &recordingProgress{})
		if err != nil || out.Reply != "Still incomplete." || len(*seen) != 2 || !strings.Contains(diagnosticInput(t, (*seen)[1]), "Diagnostic checkpoint skipped") {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	})
	t.Run("output", func(t *testing.T) {
		session, _ := retryScript(t)
		diagnosticScript(t, session, `process.stdout.write(Buffer.alloc(65536,0xff));process.stderr.write('x'.repeat(65536));`)
		status, text := diagnosticCheckpoint(context.Background(), session.runner.repository.Root)
		if status != "passed" || len(text) > maxCheckpointBytes || !utf8.ValidString(text) || !strings.Contains(text, "truncated") {
			t.Fatalf("status=%s bytes=%d text=%q", status, len(text), text)
		}
	})
	t.Run("spawn unavailable", func(t *testing.T) {
		status, _ := runCheckpoint(context.Background(), t.TempDir(), "/missing/checkpoint-executable", nil)
		if status != "unfinished" {
			t.Fatal(status)
		}
	})
}

// All descendants remain in the isolated group, as npm/node/check tools do.
// Each writes its PID before waiting; no external service or provider is used.
const diagnosticTree = `const fs=require('fs'),cp=require('child_process');process.on('SIGTERM',()=>{});fs.writeFileSync('parent.pid',''+process.pid);cp.spawn(process.execPath,['-e',"const fs=require('fs'),cp=require('child_process');process.on('SIGTERM',()=>{});fs.writeFileSync('child.pid',''+process.pid);cp.spawn(process.execPath,['-e',\"process.on('SIGTERM',()=>{});require('fs').writeFileSync('grandchild.pid',''+process.pid);setInterval(()=>{},1000)\"],{stdio:'inherit'});setInterval(()=>{},1000)"],{stdio:'inherit'});setInterval(()=>{},1000);`

func waitDiagnosticFile(t *testing.T, path string) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("process did not become ready:", path)
}

func assertDiagnosticTreeStopped(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"parent.pid", "child.pid", "grandchild.pid"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(string(b))
		if err != nil {
			t.Fatal(err)
		}
		until := time.Now().Add(2 * time.Second)
		for time.Now().Before(until) {
			err = syscall.Kill(pid, 0)
			if errors.Is(err, syscall.ESRCH) {
				break
			}
			// A killed orphan may await init's reaping; it can no longer run.
			state, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
			if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
				err = syscall.ESRCH
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !errors.Is(err, syscall.ESRCH) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("descendant still running: %s pid%d", name, pid)
		}
	}
}

func TestDiagnosticCheckpointTimeoutAndParentCancellationCleanTree(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "parent cancellation"}[cancelParent], func(t *testing.T) {
			session, seen := retryScript(t, retryReply{status: 200, body: edits("a", "@a.txt\n-old\n+new\n")}, retryReply{status: 200, body: finishes("Not finished.")})
			diagnosticScript(t, session, diagnosticTree)
			root := session.runner.repository.Root
			if !cancelParent {
				start := time.Now()
				status, text := diagnosticCheckpoint(context.Background(), root)
				if status != "unfinished" || !strings.Contains(text, "not a failed check") || time.Since(start) > 4*time.Second {
					t.Fatalf("status=%s elapsed=%v text=%q", status, time.Since(start), text)
				}
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := session.work(ctx, &recordingProgress{}); done <- err }()
				waitDiagnosticFile(t, filepath.Join(root, "grandchild.pid"))
				start := time.Now()
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("parent cancellation blocked")
				}
				if time.Since(start) > 2*time.Second || len(*seen) != 1 || strings.Contains(string(mustJSON(session.transcript)), "Diagnostic checkpoint ") {
					t.Fatalf("requests=%d transcript=%v", len(*seen), session.transcript)
				}
			}
			assertDiagnosticTreeStopped(t, root)
		})
	}
}

func mustJSON(value any) []byte { b, _ := json.Marshal(value); return b }

func TestDiagnosticCheckpointCleansBackgroundChildrenOnExit(t *testing.T) {
	root := t.TempDir()
	script := strings.Replace(diagnosticTree, "setInterval(()=>{},1000);", "setTimeout(()=>process.exit(0),500);", 1)
	// Replace only the outer interval: nested intervals have no trailing semicolon.
	status, _ := runCheckpoint(context.Background(), root, "node", []string{"-e", script})
	if status != "passed" {
		t.Fatalf("completed parent check should pass after child cleanup, got%s", status)
	}
	assertDiagnosticTreeStopped(t, root)
}
