package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mindsdb/yolocoder/internal/agent"
	"github.com/mindsdb/yolocoder/internal/app"
	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/session"
)

//go:embed static
var staticFiles embed.FS

// DefaultPort is used when no --port is given and it isn't already taken;
// otherwise the OS picks a free one.
const DefaultPort = 8420

// chatMessage is one line in the chat pane. Role is "user" for what was
// typed (or an auto-detected error, tagged "auto-fix"), "assistant" for
// the agent's reply, or "system" for status the tool itself reports.
type chatMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Server holds everything one --web session needs: the project it's
// driving, the agent loop it shares with the terminal, the SSE hub
// connected browser tabs listen on, and the dev server it supervises.
type Server struct {
	root     string
	provider config.LLM
	history  *session.Log

	hub     *hub
	proc    *process
	watcher *errorWatcher
	guard   autoFixGuard

	// turnMutex serializes agent runs: Runner.Run patches and tests the
	// working tree, which is not safe to do from two tasks at once (a
	// chat message arriving mid auto-fix, for instance).
	turnMutex sync.Mutex
}

// Serve starts the --web UI for the current folder and blocks until ctx is
// cancelled, at which point it stops the dev server and shuts the HTTP
// server down cleanly. initialTask, if non-empty, is submitted as the
// first chat message right away.
func Serve(ctx context.Context, provider config.LLM, port int, initialTask string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	history, historyErr := session.Open(root, session.Terminal(os.Getenv))
	if historyErr != nil {
		fmt.Fprintln(os.Stderr, "session log:", historyErr)
	}

	server := &Server{root: root, provider: provider, history: history, hub: newHub()}
	server.watcher = newErrorWatcher(func(text string) { server.onError("server", text) })
	server.proc = newProcess(root, server.hub, server.watcher)

	if err := server.prepareProject(ctx); err != nil {
		return err
	}

	listener, actualPort, err := listen(port)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	mux := http.NewServeMux()
	server.routes(mux)
	httpServer := &http.Server{Handler: mux}

	fmt.Printf("\x1b[36m[^_^] YoloCoder web\x1b[0m listening on \x1b[1mhttp://localhost:%d\x1b[0m\n", actualPort)

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	if err := server.proc.Start(ctx); err != nil {
		server.hub.publish("chat", chatMessage{Role: "system", Text: "Could not start the dev server: " + err.Error()})
	}

	if task := strings.TrimSpace(initialTask); task != "" {
		go server.runTask(context.Background(), task, "user")
	}

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.proc.Stop(shutdownCtx)
	return httpServer.Shutdown(shutdownCtx)
}

// prepareProject makes sure there is something for the UI to run: a fresh
// scaffold on an empty folder, or scripts/{start,restart,stop}.sh written
// by the agent itself on an existing one.
func (server *Server) prepareProject(ctx context.Context) error {
	if looksEmpty(server.root) {
		server.hub.publish("chat", chatMessage{Role: "system",
			Text: "Empty folder: scaffolding a Node/React/Tailwind/Drizzle+SQLite starter."})
		if err := scaffoldProject(server.root); err != nil {
			return fmt.Errorf("scaffold project: %w", err)
		}
		if err := server.npmInstall(ctx); err != nil {
			server.hub.publish("chat", chatMessage{Role: "system", Text: "npm install failed: " + err.Error()})
		}
		return nil
	}
	if !scriptsExist(server.root) {
		server.hub.publish("chat", chatMessage{Role: "system",
			Text: "Writing scripts/start.sh, restart.sh and stop.sh for this project..."})
		if _, err := server.runTask(ctx, existingProjectScriptsTask, "system"); err != nil {
			return fmt.Errorf("write start/restart/stop scripts: %w", err)
		}
	}
	return nil
}

func (server *Server) npmInstall(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(server.root, "package.json")); err != nil {
		return nil
	}
	server.hub.publish("status", map[string]string{"text": "Installing dependencies (npm install)..."})
	defer server.hub.publish("status", map[string]string{"text": ""})
	command := exec.CommandContext(ctx, "npm", "install")
	command.Dir = server.root
	output, err := command.CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		if line != "" {
			server.hub.publish("log", map[string]string{"text": line})
		}
	}
	if err != nil {
		return fmt.Errorf("%w", err)
	}
	return nil
}

func (server *Server) routes(mux *http.ServeMux) {
	staticRoot, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err) // embedded at build time; a missing "static" dir is a build-time bug
	}
	mux.Handle("/", http.FileServer(http.FS(staticRoot)))
	mux.HandleFunc("/history", server.handleHistory)
	mux.Handle("/events", server.hub)
	mux.HandleFunc("/chat", server.handleChat)
	mux.HandleFunc("/client-error", server.handleClientError)
	mux.HandleFunc("/process/start", server.handleProcess("start"))
	mux.HandleFunc("/process/restart", server.handleProcess("restart"))
	mux.HandleFunc("/process/stop", server.handleProcess("stop"))
	mux.Handle("/app/", newAppProxy(server.proc.Port))
}

// handleHistory replays what this folder was already asked, so a browser
// tab opened after work has started still shows the conversation so far.
// Live progress (the Status/Log trail of a run in flight) is not replayed;
// only /events carries that, same trade-off as the terminal's own history.
func (server *Server) handleHistory(response http.ResponseWriter, request *http.Request) {
	turns, _ := session.Recent(server.root)
	messages := make([]chatMessage, 0, len(turns)*2)
	for _, turn := range turns {
		if turn.Message != "" {
			messages = append(messages, chatMessage{Role: "user", Text: turn.Message})
		}
		if turn.Summary != "" {
			messages = append(messages, chatMessage{Role: "assistant", Text: turn.Summary})
		}
	}
	writeJSON(response, messages)
}

type chatRequest struct {
	Message string `json:"message"`
}

func (server *Server) handleChat(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body chatRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(body.Message)
	if message == "" {
		http.Error(response, "message is required", http.StatusBadRequest)
		return
	}
	// A person sending a message of their own is a sign they're driving
	// again, so the next auto-detected error earns a fresh set of attempts.
	server.guard.reset()
	go server.runTask(context.Background(), message, "user")
	response.WriteHeader(http.StatusAccepted)
}

type clientErrorReport struct {
	Error struct {
		Message  string `json:"message"`
		Filename string `json:"filename"`
		Lineno   int    `json:"lineno"`
		Colno    int    `json:"colno"`
		Stack    string `json:"stack"`
	} `json:"error"`
}

// handleClientError receives what the injected browser shim posted to the
// parent page and forwards it to app.js, which relays it here from the
// window "message" listener.
func (server *Server) handleClientError(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var report clientErrorReport
	if err := json.NewDecoder(request.Body).Decode(&report); err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	var text strings.Builder
	fmt.Fprintln(&text, report.Error.Message)
	if report.Error.Filename != "" {
		fmt.Fprintf(&text, "at %s:%d:%d\n", report.Error.Filename, report.Error.Lineno, report.Error.Colno)
	}
	if report.Error.Stack != "" {
		fmt.Fprintln(&text, report.Error.Stack)
	}
	go server.onError("browser", text.String())
	response.WriteHeader(http.StatusAccepted)
}

// onError turns a detected server or browser error into an auto-fix task,
// unless the loop guard says this exact error has already had its fair
// share of automatic attempts.
func (server *Server) onError(source, text string) {
	if !server.guard.allow(signature(text)) {
		server.hub.publish("chat", chatMessage{Role: "system", Text: fmt.Sprintf(
			"Still seeing the same %s error after %d fix attempts — leaving it for you:\n\n%s",
			source, maxAutoFixAttempts, text)})
		return
	}
	task := fmt.Sprintf("A %s error occurred while the app was running:\n\n%s\n\nDiagnose and fix it.", source, text)
	outcome, err := server.runTask(context.Background(), task, "auto-fix")
	if err == nil && outcome.Applied {
		_ = server.proc.Restart(context.Background())
	}
}

func (server *Server) handleProcess(action string) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var err error
		switch action {
		case "start":
			err = server.proc.Start(request.Context())
		case "restart":
			err = server.proc.Restart(request.Context())
		case "stop":
			err = server.proc.Stop(request.Context())
		}
		if err != nil {
			http.Error(response, err.Error(), http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusAccepted)
	}
}

// runTask is the one path every task takes, whatever triggered it: a
// user's chat message, the one-off "write me start/restart/stop scripts"
// task, or an auto-fix. It is exactly what runTask in cmd/yolocoder does
// for the terminal, aimed at the SSE hub instead of the robot line.
func (server *Server) runTask(ctx context.Context, task, role string) (agent.Outcome, error) {
	server.turnMutex.Lock()
	defer server.turnMutex.Unlock()

	server.hub.publish("chat", chatMessage{Role: role, Text: task})

	turns, _ := session.Recent(server.root)
	outcome, err := app.RunTask(ctx, task, server.provider, app.Recollections(turns), hubProgress{server.hub})
	if err != nil {
		server.hub.publish("chat", chatMessage{Role: "system", Text: "Error: " + err.Error()})
		return outcome, err
	}
	recordTurn(server.history, task, outcome)
	reply := outcome.Reply
	if reply == "" {
		reply = "Done."
	}
	server.hub.publish("chat", chatMessage{Role: "assistant", Text: reply})
	return outcome, nil
}

// recordTurn mirrors record in cmd/yolocoder/main.go, so a folder's
// history reads as one continuous log whether it was asked from the
// terminal or the web UI.
func recordTurn(history *session.Log, task string, outcome agent.Outcome) {
	if history == nil {
		return
	}
	kind := "chat"
	if outcome.Coding {
		kind = "code"
	}
	_ = history.Append(session.Turn{
		Message: task, Kind: kind, Summary: outcome.Reply, Files: outcome.Files, Applied: outcome.Applied,
	})
}

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(value)
}

// listen binds the requested port, falling back to an OS-assigned free
// one if it's taken, and reports which one it actually got.
func listen(port int) (net.Listener, int, error) {
	if port == 0 {
		port = DefaultPort
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, 0, err
		}
	}
	return listener, listener.Addr().(*net.TCPAddr).Port, nil
}

// existingProjectScriptsTask is fed through the normal agent loop, so the
// scripts it writes land as a reviewable diff like any other change,
// rather than through a special-cased file-writing path in this package.
const existingProjectScriptsTask = `This project has no scripts/start.sh, scripts/restart.sh or ` +
	`scripts/stop.sh yet. Inspect package.json (or this project's equivalent) to find its dev command, ` +
	`then create all three so yolocoder --web can drive them. Follow this contract exactly:

A ".yolocoder/web" directory holds the running dev server's state; create it if missing
(mkdir -p .yolocoder/web).

scripts/start.sh: if .yolocoder/web/server.pid names a process that is still running, do nothing.
Otherwise launch the project's dev server backgrounded and detached so it outlives this script, for
example:
  mkdir -p .yolocoder/web
  nohup npm run dev > .yolocoder/web/server.log 2>&1 &
  echo $! > .yolocoder/web/server.pid
Then write the port the dev server ends up listening on to .yolocoder/web/port, just the number. If
the port is fixed by the project's own config, write it directly; if it's only known once the server
has actually bound it, wait briefly and read it back out of server.log.

scripts/stop.sh: read the pid from .yolocoder/web/server.pid, and if it names a running process,
signal its process group (kill -TERM -$pid, falling back to kill $pid) so anything it spawned stops
too, then remove server.pid. Do nothing if there is no pid file or the process is already gone.

scripts/restart.sh: call stop.sh then start.sh.

All three must be POSIX sh, marked executable, and safe to run more than once and by hand from a
plain terminal, not only from this tool.`
