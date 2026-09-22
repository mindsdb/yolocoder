package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mindsdb/yolocoder/internal/debug"
)

// Choosing the files a task needs, before the model is asked anything.
//
// Every coding turn otherwise opens the same way: one call that reads the
// map and comes back asking for files, then a second that does the work.
// The first exists only to pick, and a decision model picks in about a
// second where that call takes three.
//
// Measured over fourteen real turns from a session log, against the files
// those turns actually changed: 968ms at the median, and the whole file
// list costs the same as one question — the call is priced per request,
// not per question. At the threshold below it named every file that
// mattered in thirteen of fourteen, carrying about 1.6 files that did not.
//
// Being wrong is survivable and being slow is not, which is what the
// timeout and the silent fall-through below are for: a miss costs the
// round trip this was saving, and nothing else, because the model still
// has read_files.

const (
	// decideThreshold is where a probability becomes a file worth
	// reading. Swept over the same fourteen turns: 0.5 misses a file in
	// three of them, 0.35 misses one, and going lower buys the last one
	// at four extra files a turn, which is a worse trade than the round
	// trip it saves.
	decideThreshold = 0.35

	// decideTimeout is short because this is an optimization. Past this
	// the turn is better off doing what it always did.
	decideTimeout = 6 * time.Second

	// maxDecideFiles bounds what is worth asking about. Latency does not
	// grow with the question count but the prompt does — nineteen files
	// cost about 900 input tokens — and past this the map is better left
	// to the model, which can narrow it by name far more cheaply.
	maxDecideFiles = 80

	// maxDecidePicks stops a shrug from turning into the whole repository
	// being read. If this many come back above the line, the answer was
	// not a selection.
	maxDecidePicks = 8
)

type decisionRequest struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]question `json:"questions"`
}

type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type decisionReply struct {
	Answers map[string]struct {
		Noul float64 `json:"noul"`
	} `json:"answers"`
}

// decisionsEndpoint is where the decision model answers, beside the
// completions route the rest of this package speaks to.
func decisionsEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/decisions"
	}
	return baseURL + "/v1/decisions"
}

// chooseFiles are the files a task probably needs, or nil.
//
// nil is the ordinary answer to anything going wrong — a timeout, a 504,
// an endpoint that has never heard of decisions — and the turn carries on
// exactly as it did before. Nothing here is allowed to fail a turn.
func (runner *Runner) chooseFiles(ctx context.Context, task string, paths []string) []string {
	if len(paths) == 0 || len(paths) > maxDecideFiles {
		return nil
	}
	questions := make(map[string]question, len(paths))
	for index, path := range paths {
		questions["f"+strconv.Itoa(index)] = question{
			Type:         "noul",
			Instructions: "Is the file " + path + " one that must be read or edited to do this task?",
		}
	}
	payload, err := json.Marshal(decisionRequest{
		Model:     "jev",
		State:     map[string]any{"task": task, "files": paths},
		Questions: questions,
	})
	if err != nil {
		return nil
	}

	callCtx, cancel := context.WithTimeout(ctx, decideTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		decisionsEndpoint(runner.client.baseURL), bytes.NewReader(payload))
	if err != nil {
		return nil
	}
	request.Header.Set("Authorization", "Bearer "+runner.client.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := runner.client.http.Do(request)
	if err != nil {
		debug.Logf("DECIDE", "%v", err)
		return nil
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		debug.Logf("DECIDE", "%s: %s", response.Status, snippet(string(body)))
		return nil
	}
	var reply decisionReply
	if err := json.Unmarshal(body, &reply); err != nil {
		debug.Logf("DECIDE", "decode: %v", err)
		return nil
	}

	type scored struct {
		path string
		odds float64
	}
	var above []scored
	for name, answer := range reply.Answers {
		index, err := strconv.Atoi(strings.TrimPrefix(name, "f"))
		if err != nil || index < 0 || index >= len(paths) || answer.Noul < decideThreshold {
			continue
		}
		above = append(above, scored{paths[index], answer.Noul})
	}
	if len(above) == 0 || len(above) > maxDecidePicks {
		return nil
	}
	// Most confident first, so a read that hits the byte ceiling drops
	// the file it was least sure about.
	sort.Slice(above, func(i, j int) bool { return above[i].odds > above[j].odds })
	chosen := make([]string, 0, len(above))
	for _, one := range above {
		chosen = append(chosen, one.path)
	}
	return chosen
}

// preread puts the chosen files into the conversation as though the model
// had asked for them, which is what makes the round trip disappear: it
// opens already holding what it would otherwise have spent a call
// requesting. Reported honestly on the trail — these are files nobody
// asked for, and a turn that goes wrong afterwards should not leave
// anyone wondering where they came from.
func (session *changeSession) preread(paths []string, progress Progress) {
	text, err := session.runner.readFiles(paths)
	if err != nil {
		return
	}
	session.readPaths = append(session.readPaths, paths...)
	session.transcript = append(session.transcript, inputMessage{
		Role: "user",
		Content: fmt.Sprintf("These look like the files this needs, so here they are already. "+
			"Read anything else you want with read_files.\n\n%s", text),
	})
	progress.Log("  picked " + strings.Join(paths, ", "))
}
