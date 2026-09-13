package web

import (
	"fmt"
	"strings"

	"github.com/mindsdb/yolocoder/internal/debug"
)

// debugClip keeps a traced body readable in the terminal; the file log
// (started with YOLOCODER_DEBUG_LOG) gets it untruncated regardless.
const debugClip = 2000

// enableDebugToTerminal makes every request/reply and patch/test result
// visible in the terminal --web was launched from — the same trace the
// terminal session's own /debug command shows — without any of it
// reaching the browser: debug.SetSink is entirely separate from the SSE
// hub the chat/activity panes are built on, so this prints straight to
// stdout and nothing shows up in the web UI.
func enableDebugToTerminal() {
	debug.SetSink(func(title, body string) {
		text := "  \x1b[2m[debug] " + title + "\x1b[0m"
		if body = strings.TrimSpace(body); body != "" {
			text += "\n" + indentDebugBody(clipDebugBody(body, debugClip))
		}
		fmt.Println(text)
	})
	fmt.Println("[^_^] Debug output on: every request and reply will print here.")
	if path := debug.Path(); path != "" {
		fmt.Printf("[^_^] Full trace is also being written to %s\n", path)
	} else {
		fmt.Printf("[^_^] For a full untruncated trace, restart with %s=1\n", debug.PathEnv)
	}
}

func clipDebugBody(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf("\n... %d more bytes (see the debug log file)", len(text)-limit)
}

func indentDebugBody(text string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = "  \x1b[2m" + line + "\x1b[0m"
	}
	return strings.Join(lines, "\n")
}
