package web

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
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

// httpErrorLog labels net/http's own internal diagnostics — a body copy
// that fails mid-stream because the dev server just restarted, a
// panic recovered from a handler, and the like — which bypass every
// error path yolocoder defines and print straight to stderr through
// log.Default() otherwise (an "Unsolicited response ... 400 Bad
// Request", a bare "context deadline exceeded", once each already seen
// in practice). Neither ends the run; both looked like a crash next to
// yolocoder's own unprefixed banner lines without something marking
// whose message this actually is.
var httpErrorLog = log.New(os.Stderr, "[http] ", log.LstdFlags)

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
	root    string
	history *session.Log

	// fromEnvironment marks a provider sourced from OPENAI_* environment
	// variables, which --model (like the terminal's /model) can't change:
	// there is nothing on disk for it to save to, and it's meant to be
	// fixed by whatever set those variables, not switched mid-session.
	fromEnvironment bool

	hub     *hub
	proc    *process
	watcher *errorWatcher
	guard   autoFixGuard

	// providerMutex guards provider: read by every runTask call, written
	// by a model change from the UI while one may be in flight.
	providerMutex sync.Mutex
	provider      config.LLM

	// appProxyPort is the app proxy's own listener port (see
	// newAppProxy), set once before the HTTP servers start accepting
	// connections and read-only after — the "go" statement that starts
	// serving is itself the happens-before edge that makes this safe
	// without a mutex.
	appProxyPort int

	// turnMutex serializes agent runs: Runner.Run patches and tests the
	// working tree, which is not safe to do from two tasks at once (a
	// chat message arriving mid auto-fix, for instance).
	turnMutex sync.Mutex

	// stateMutex guards busy and phase, the ground truth behind the
	// "busy"/"phase" SSE events (see setBusy/setPhase). A one-shot event
	// is exactly that: a browser tab that misses one — a reconnect during
	// a long build, a dropped event, a tab opened mid-task — has no way
	// to catch up on its own. /state exposes this so the client can
	// resync itself whenever its SSE connection (re)opens, rather than
	// trusting every event to arrive exactly once.
	stateMutex sync.Mutex
	busy       bool
	phase      string
}

// Serve starts the --web UI for the current folder and blocks until ctx is
// cancelled, at which point it stops the dev server and shuts the HTTP
// server down cleanly. initialTask, if non-empty, is submitted as the
// first chat message right away.
func Serve(ctx context.Context, provider config.LLM, port int, initialTask string, fromEnvironment bool) error {
	if err := ensureNode(); err != nil {
		return err
	}
	// A child context so typing "exit" can stop the run the same way the
	// caller cancelling ctx (Ctrl+C) already does, without either needing
	// to know about the other.
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	go watchForExit(stop)

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	history, historyErr := session.Open(root, session.Terminal(os.Getenv))
	if historyErr != nil {
		fmt.Fprintln(os.Stderr, "session log:", historyErr)
	}

	server := &Server{root: root, provider: provider, fromEnvironment: fromEnvironment, history: history, hub: newHub()}
	server.watcher = newErrorWatcher(func(text string) { server.onError("server", text) })
	server.proc = newProcess(root, server.hub, server.watcher)

	if err := server.prepareProject(ctx); err != nil {
		return err
	}

	// The app being built gets its own dedicated listener, on its own
	// port chosen fresh each run: see the comment on newAppProxy for why
	// it can't share a path prefix on the main UI's own server.
	appListener, appPort, err := listenAny()
	if err != nil {
		return fmt.Errorf("listen (app proxy): %w", err)
	}
	server.appProxyPort = appPort
	appProxyServer := &http.Server{Handler: newAppProxy(server.proc.Port), ErrorLog: httpErrorLog}
	go appProxyServer.Serve(appListener)

	listener, actualPort, err := listen(port)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	mux := http.NewServeMux()
	server.routes(mux)
	httpServer := &http.Server{Handler: mux, ErrorLog: httpErrorLog}

	fmt.Printf("\x1b[36m[^_^] YoloCoder web\x1b[0m listening on \x1b[1mhttp://localhost:%d\x1b[0m\n", actualPort)
	fmt.Println("\x1b[2mType exit here, or Ctrl+C, to stop.\x1b[0m")

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

	fmt.Println("[^_^] Bye.")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.proc.Stop(shutdownCtx)
	_ = appProxyServer.Shutdown(shutdownCtx)
	return httpServer.Shutdown(shutdownCtx)
}

// watchForExit lets a person still type "exit" or "quit" into the
// terminal --web was launched from to stop it, the same words the
// interactive terminal session already accepts, rather than needing to
// remember Ctrl+C works here too. On a non-interactive stdin (piped,
// redirected, /dev/null) Scan simply hits EOF right away and this
// goroutine ends without ever calling stop.
func watchForExit(stop context.CancelFunc) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "exit", "quit", "/exit", "/quit":
			stop()
			return
		}
	}
}

// prepareProject makes sure there is something for the UI to run. For
// now, --web only knows how to work with two kinds of folder: an empty
// one, which it scaffolds from scratch, or one it scaffolded earlier,
// identified by yolocoder.json. Anything else is refused rather than
// guessed at: finding an arbitrary existing project's dev command is a
// judgment call an agent can get wrong, and this is exactly the wrong
// place for that to surface as a confusing failure.
func (server *Server) prepareProject(ctx context.Context) error {
	switch {
	case looksEmpty(server.root):
		server.announce("Setting up a new project in " + server.root)
		server.announce("Empty folder: scaffolding a Node/React/Tailwind/Drizzle+SQLite starter...")
		if err := scaffoldProject(server.root); err != nil {
			return fmt.Errorf("scaffold project: %w", err)
		}
		server.announce("Project files written.")
		if err := server.npmInstall(ctx); err != nil {
			server.announce("npm install failed: " + err.Error())
		}
		return nil
	case isYolocoderProject(server.root):
		if !scriptsExist(server.root) {
			server.announce("Setting up this project: scripts/start.sh, restart.sh and stop.sh are missing, restoring them...")
			if err := restoreScripts(server.root); err != nil {
				return fmt.Errorf("restore scripts: %w", err)
			}
			server.announce("Scripts restored.")
		}
		return nil
	default:
		return fmt.Errorf("this folder isn't empty and wasn't created by yolocoder --web\n\n" +
			"For now, yolocoder --web only works in an empty folder (to scaffold a new project) or a " +
			"folder yolocoder already scaffolded. Run it somewhere empty, or point it at a project it created.")
	}
}

// announce reports a first-time setup step to whatever is watching. The
// terminal is the only thing listening at this point in Serve — no
// browser tab has connected yet, and setup can take a while (an npm
// install especially) — so this prints there directly rather than relying
// solely on the SSE hub, while still publishing to it for a tab that
// happens to connect while setup is still running.
func (server *Server) announce(text string) {
	fmt.Println("[^_^] " + text)
	server.hub.publish("chat", chatMessage{Role: "system", Text: text})
}

// lineStreamer turns a writer of arbitrary byte chunks (an exec.Cmd's
// Stdout/Stderr, in particular) into a callback per complete line, so
// long-running output like npm install's can be shown as it happens
// instead of dumped all at once when the command finally exits.
type lineStreamer struct {
	onLine func(string)
	buffer []byte
}

func (streamer *lineStreamer) Write(chunk []byte) (int, error) {
	streamer.buffer = append(streamer.buffer, chunk...)
	for {
		index := bytes.IndexByte(streamer.buffer, '\n')
		if index < 0 {
			break
		}
		streamer.onLine(string(bytes.TrimRight(streamer.buffer[:index], "\r")))
		streamer.buffer = streamer.buffer[index+1:]
	}
	return len(chunk), nil
}

// flush reports whatever's left unterminated once the command has
// exited, so a final line without a trailing newline isn't dropped.
func (streamer *lineStreamer) flush() {
	if len(streamer.buffer) > 0 {
		streamer.onLine(string(streamer.buffer))
		streamer.buffer = nil
	}
}

func (server *Server) npmInstall(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(server.root, "package.json")); err != nil {
		return nil
	}
	server.announce("Installing dependencies (npm install)...")
	server.hub.publish("status", map[string]string{"text": "Installing dependencies (npm install)..."})
	defer server.hub.publish("status", map[string]string{"text": ""})

	streamer := &lineStreamer{onLine: func(line string) {
		if line == "" {
			return
		}
		fmt.Println("  " + line)
		server.hub.publish("log", map[string]string{"text": line})
	}}
	command := exec.CommandContext(ctx, "npm", "install")
	command.Dir = server.root
	command.Stdout = streamer
	command.Stderr = streamer
	err := command.Run()
	streamer.flush()
	if err != nil {
		return err
	}
	server.announce("Dependencies installed.")
	return nil
}

func (server *Server) routes(mux *http.ServeMux) {
	staticRoot, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err) // embedded at build time; a missing "static" dir is a build-time bug
	}
	mux.Handle("/", http.FileServer(http.FS(staticRoot)))
	mux.HandleFunc("/config", server.handleConfig)
	mux.HandleFunc("/state", server.handleState)
	mux.HandleFunc("/models", server.handleModels)
	mux.HandleFunc("/model", server.handleModel)
	mux.Handle("/events", server.hub)
	mux.HandleFunc("/chat", server.handleChat)
	mux.HandleFunc("/client-error", server.handleClientError)
	mux.HandleFunc("/process/start", server.handleProcess("start"))
	mux.HandleFunc("/process/restart", server.handleProcess("restart"))
	mux.HandleFunc("/process/stop", server.handleProcess("stop"))
}

// handleConfig tells the page which port the app proxy ended up on, so
// app.js can point the iframe at it — it's chosen fresh each run (see
// listenAny), so it can't just be hardcoded into the static HTML.
//
// The chat pane deliberately does *not* replay this folder's whole
// recorded history on load: session.Recent exists to give the agent
// context to reason from, not to be replayed verbatim as a transcript,
// and a folder used across many separate --web runs (or from the plain
// terminal too) accumulates turns that read as a confusing, unrelated
// backlog rather than one conversation. Each run starts its visible chat
// pane fresh; only what happens in *this* run streams in over /events.
func (server *Server) handleConfig(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, map[string]any{"appProxyPort": server.appProxyPort, "folder": app.Folder()})
}

// handleState is the ground truth behind the "busy"/"phase"/"process"
// SSE events, so a client can resync itself instead of trusting every
// one-shot event to arrive: an EventSource reconnect during a long build
// (or a tab that simply missed one) would otherwise leave the UI showing
// something stale forever, with nothing to correct it. The client fetches
// this whenever its SSE connection (re)opens.
func (server *Server) handleState(response http.ResponseWriter, request *http.Request) {
	server.stateMutex.Lock()
	busy, phase := server.busy, server.phase
	server.stateMutex.Unlock()
	writeJSON(response, map[string]any{
		"busy":    busy,
		"phase":   phase,
		"process": server.proc.State(),
		"port":    server.proc.Port(),
	})
}

const listModelsTimeout = 10 * time.Second

// handleModels lists what the endpoint's /v1/models offers, the same way
// the terminal's `yolocoder model` does, plus the one currently in use —
// best-effort: an endpoint that doesn't support listing still gets a
// usable response, just with an empty list and only its current model.
func (server *Server) handleModels(response http.ResponseWriter, request *http.Request) {
	provider := server.currentProvider()
	ctx, cancel := context.WithTimeout(request.Context(), listModelsTimeout)
	defer cancel()
	models, _ := agent.ListModels(ctx, provider.BaseURL, provider.APIKey)
	writeJSON(response, map[string]any{
		"models":  models,
		"current": provider.Model,
		"locked":  server.fromEnvironment,
	})
}

type modelRequest struct {
	Model string `json:"model"`
}

// handleModel changes the model in use from here on, exactly like the
// terminal's /model.
func (server *Server) handleModel(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if server.fromEnvironment {
		http.Error(response, "can't change an OPENAI_* environment provider; restart with a different OPENAI_MODEL", http.StatusBadRequest)
		return
	}
	var body modelRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		http.Error(response, "model is required", http.StatusBadRequest)
		return
	}
	server.setModel(model)
	response.WriteHeader(http.StatusAccepted)
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
	if err == nil && shouldRestartAfterAutoFix(source, outcome.Applied) {
		_ = server.proc.Restart(context.Background())
	}
}

// shouldRestartAfterAutoFix reports whether an applied auto-fix should
// also restart the dev server process. Only true for a server-sourced
// error: a browser-side one can't have touched the dev server processes
// at all — they're a separate process from the tab that threw it — so
// HMR/tsx watch have already picked up the fix on their own by the time
// the task returns, the same as any other applied change (see reload).
// Restarting anyway would just be an unnecessary few seconds of
// "connection refused" for no benefit. A server-side error might mean
// the process itself is in a bad state (or actually dead), which an
// explicit restart is worth doing for.
func shouldRestartAfterAutoFix(source string, applied bool) bool {
	return applied && source == "server"
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

	// busy brackets the turn so the UI can show something is happening the
	// moment a message is sent, not just once Status/Log lines start
	// arriving (routing a plain message costs one silent round trip before
	// the first of those).
	server.setBusy(true)
	server.setPhase("build")
	defer server.setBusy(false)

	server.hub.publish("chat", chatMessage{Role: role, Text: task})

	turns, _ := session.Recent(server.root)
	outcome, err := app.RunTask(ctx, task, server.currentProvider(), app.Recollections(turns), hubProgress{server.hub})
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
	if outcome.Applied {
		server.reload()
	}
	return outcome, nil
}

// reload is the "load" step of build → load → review. The dev server
// already picks up filesystem changes on its own the moment the patch
// lands — Vite's HMR for the frontend, tsx watch's own restart for the
// backend — so this doesn't restart anything itself; restarting the
// whole process on every edit would be slower and would throw away
// exactly the state HMR exists to preserve. It just tells the page to
// reload the iframe, as the fastest way to guarantee a change that HMR
// can't patch in place (an edit to index.html, a newly added dependency,
// a full-reload signal Vite already decided to send) actually shows up
// rather than silently waiting on it. "Review" isn't a step this
// function waits on: the error watcher tails server.log continuously
// regardless of any particular task, and fires its own auto-fix run —
// another build → load → review cycle — if the change just applied
// broke something.
func (server *Server) reload() {
	server.setPhase("load")
	server.hub.publish("reload", map[string]bool{"reload": true})
}

// setBusy and setPhase are the only way busy/phase change: they update
// the ground truth (read back by handleState) and publish the matching
// SSE event in the same place, so the two can never drift apart.
func (server *Server) setBusy(busy bool) {
	server.stateMutex.Lock()
	server.busy = busy
	server.stateMutex.Unlock()
	server.hub.publish("busy", map[string]bool{"busy": busy})
}

func (server *Server) setPhase(phase string) {
	server.stateMutex.Lock()
	server.phase = phase
	server.stateMutex.Unlock()
	server.hub.publish("phase", map[string]string{"phase": phase})
}

// currentProvider is what every task actually runs against; read through
// this rather than the field directly, since a model change from the UI
// can land between one task and the next.
func (server *Server) currentProvider() config.LLM {
	server.providerMutex.Lock()
	defer server.providerMutex.Unlock()
	return server.provider
}

// setModel changes the model in use from here on and saves it, exactly
// like the terminal's /model — except an environment-sourced provider,
// which the caller must not pass here (there is nothing on disk for it
// to save to; see handleModel).
func (server *Server) setModel(model string) {
	server.providerMutex.Lock()
	server.provider.Model = model
	provider := server.provider
	server.providerMutex.Unlock()
	if err := config.Save(provider); err != nil {
		server.hub.publish("chat", chatMessage{Role: "system", Text: "Could not save the model choice: " + err.Error()})
	}
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
		Attempts: outcome.Attempts, Rewrote: outcome.Rewrote,
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
	if listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		return listener, port, nil
	}
	return listenAny()
}

// listenAny always picks an OS-assigned free port, for a listener (the
// app proxy's) that has no fixed default worth trying first.
func listenAny() (net.Listener, int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	return listener, listener.Addr().(*net.TCPAddr).Port, nil
}
