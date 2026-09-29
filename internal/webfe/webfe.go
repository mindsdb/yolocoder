// Package webfe serves --web-fe: yolocoder as a hosted service for
// frontend-only apps. The browser holds the project and runs it, in a
// Sandpack preview; the server's only job is the agent loop, over a
// repository in memory that lives exactly as long as one request.
// Nothing is written to disk, nothing is executed, and nothing is kept
// between turns — which is what makes it safe to put in front of
// strangers, and cheap to run on a Lambda.
package webfe

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mindsdb/yolocoder/internal/agent"
	"github.com/mindsdb/yolocoder/internal/config"
	"github.com/mindsdb/yolocoder/internal/repo"
)

//go:embed static
var staticFiles embed.FS

//go:embed all:starters
var starterFiles embed.FS

// The limits on what one turn may carry. They are what bounds a turn's
// cost, since the model's input is the project plus the message, and
// they are sized for the simple apps this is for: a project past them
// is one to take to the real yolocoder, on a real machine.
//
// The first three are the agent's own limits for sending a project in
// memory to the model whole, so every project accepted here goes up
// front in full and no turn spends a round trip reading.
const (
	maxFiles        = agent.MemoryWholeMaxFiles
	maxFileBytes    = agent.MemoryWholeMaxFileBytes
	maxProjectBytes = agent.MemoryWholeMaxBytes
	maxMessageBytes = 4000
	maxHistory      = 10
	maxSummaryBytes = 2000
	// A Lambda Function URL refuses a request body past 6 MB, so the
	// images a turn can carry are sized to stay well under it, alongside
	// a full project.
	maxImages     = 3
	maxImageBytes = 1500 << 10
	maxBodyBytes  = maxProjectBytes + maxImages*maxImageBytes + 256<<10

	turnTimeout = 5 * time.Minute
)

// Config is everything the server is told from outside: the provider
// every turn runs against, the key that signs projects, and the origins
// a browser may call it from.
type Config struct {
	Provider   config.LLM
	SigningKey []byte
	// Origins are the pages allowed to call the API cross-origin — the
	// GitHub Pages site, when the client is hosted there. Empty allows
	// only the client this server serves itself.
	Origins []string
}

// DefaultModel is what every turn runs on when OPENAI_MODEL is not set:
// MindsHub's own fast model, which is what the demo is for.
const DefaultModel = "mindshub_air"

// ConfigFromEnvironment reads Config the way a deployment sets it: the
// provider from the OPENAI_* variables --llm-from-env-vars already
// reads (with OPENAI_MODEL defaulting to DefaultModel), and the rest
// from YOLOCODER_WEB_FE_*. A missing signing key is made up for this
// process, with a warning, since that only costs local development its
// projects on a restart.
func ConfigFromEnvironment(getenv func(string) string) (Config, error) {
	provider, err := config.FromEnvironment(func(name string) string {
		if value := getenv(name); name != "OPENAI_MODEL" || strings.TrimSpace(value) != "" {
			return value
		}
		return DefaultModel
	})
	if err != nil {
		return Config{}, err
	}
	key := []byte(getenv("YOLOCODER_WEB_FE_SIGNING_KEY"))
	switch {
	case len(key) == 0:
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return Config{}, err
		}
		fmt.Fprintln(os.Stderr, "YOLOCODER_WEB_FE_SIGNING_KEY is not set; projects will not survive a restart.")
	case len(key) < 32:
		return Config{}, fmt.Errorf("YOLOCODER_WEB_FE_SIGNING_KEY must be at least 32 bytes")
	}
	var origins []string
	for _, origin := range strings.Split(getenv("YOLOCODER_WEB_FE_ORIGINS"), ",") {
		if origin = strings.TrimRight(strings.TrimSpace(origin), "/"); origin != "" {
			origins = append(origins, origin)
		}
	}
	return Config{Provider: provider, SigningKey: key, Origins: origins}, nil
}

// Serve listens on port until ctx is cancelled.
func Serve(ctx context.Context, cfg Config, port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: Handler(cfg), ErrorLog: log.New(os.Stderr, "[http] ", log.LstdFlags)}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Printf("[^_^] YoloCoder web-fe on http://localhost:%d\n", port)
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler is the whole service: the client, the starters, and turns.
func Handler(cfg Config) http.Handler {
	service := &service{cfg: cfg, signer: signer{key: cfg.SigningKey}}
	staticRoot, _ := fs.Sub(staticFiles, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(staticRoot)))
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(response, "ok")
	})
	mux.HandleFunc("GET /api/starter", service.handleStarter)
	mux.HandleFunc("POST /api/turn", service.handleTurn)
	mux.HandleFunc("OPTIONS /api/", func(http.ResponseWriter, *http.Request) {})
	return service.cors(mux)
}

type service struct {
	cfg    Config
	signer signer
}

// cors lets the configured origins call the API. It is not the security
// boundary — anything can call a public URL — only what lets the hosted
// client, on another origin, read the answers.
func (service *service) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		origin := request.Header.Get("Origin")
		for _, allowed := range service.cfg.Origins {
			if origin == allowed {
				response.Header().Set("Access-Control-Allow-Origin", origin)
				response.Header().Set("Access-Control-Allow-Methods", "GET, POST")
				response.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				response.Header().Set("Access-Control-Max-Age", "86400")
				response.Header().Add("Vary", "Origin")
				break
			}
		}
		next.ServeHTTP(response, request)
	})
}

// project is what the browser holds and sends back: the files, the
// history, and the signature over both.
type project struct {
	Files   map[string]string `json:"files"`
	History []Turn            `json:"history"`
	State   string            `json:"state"`
}

var starterKinds = []string{"app", "game"}

func (service *service) handleStarter(response http.ResponseWriter, request *http.Request) {
	kind := request.URL.Query().Get("kind")
	if kind == "" {
		kind = starterKinds[0]
	}
	files, err := starter(kind)
	if err != nil {
		http.Error(response, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(response, project{Files: files, History: []Turn{}, State: service.signer.sign(files, nil)})
}

func starter(kind string) (map[string]string, error) {
	known := false
	for _, name := range starterKinds {
		known = known || name == kind
	}
	if !known {
		return nil, fmt.Errorf("unknown starter %q", kind)
	}
	root, err := fs.Sub(starterFiles, "starters/"+kind)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	err = fs.WalkDir(root, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := fs.ReadFile(root, name)
		files[name] = string(content)
		return err
	})
	return files, err
}

type turnRequest struct {
	project
	Message string   `json:"message"`
	Images  []string `json:"images,omitempty"`
}

// handleTurn runs one turn and streams it back as Server-Sent Events:
// the same status and log lines the terminal prints while it works,
// then one "done" carrying the files that changed and the new signed
// state, or one "failed" saying why nothing did.
func (service *service) handleTurn(response http.ResponseWriter, request *http.Request) {
	var turn turnRequest
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, maxBodyBytes))
	if err := decoder.Decode(&turn); err != nil {
		http.Error(response, "unreadable request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := validate(turn); err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	if !service.signer.verify(turn.Files, turn.History, turn.State) {
		http.Error(response, "this project was not produced here, or the server's key has changed; start a new one", http.StatusConflict)
		return
	}
	stream, err := newEventStream(response)
	if err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), turnTimeout)
	defer cancel()
	repository := repo.NewMemory(turn.Files)
	outcome, err := service.run(ctx, repository, turn, stream)
	if err != nil {
		stream.send("failed", map[string]string{"error": err.Error()})
		return
	}

	// A turn may create files, so the project it leaves is checked
	// against the same limits the next turn will be; one that grew past
	// them is refused now, rather than becoming a project that can no
	// longer be sent at all.
	changed := repository.Changed()
	files := make(map[string]string, len(turn.Files)+len(changed))
	for name, content := range turn.Files {
		files[name] = content
	}
	for name, content := range changed {
		files[name] = content
	}
	if err := validateFiles(files); err != nil {
		stream.send("failed", map[string]string{"error": "the change would make the project too large for the demo: " + err.Error()})
		return
	}
	reply := strings.TrimSpace(outcome.Reply)
	if reply == "" {
		reply = "Done."
	}
	history := append(append([]Turn{}, turn.History...), Turn{Message: turn.Message, Summary: clip(reply, maxSummaryBytes), Files: outcome.Files})
	if len(history) > maxHistory {
		history = history[len(history)-maxHistory:]
	}
	stream.send("done", map[string]any{
		"reply":   reply,
		"changed": changed,
		"history": history,
		"state":   service.signer.sign(files, history),
		"usage":   outcome.Usage,
		"profile": outcome.Profile.Summary(),
	})
}

func (service *service) run(ctx context.Context, repository *repo.Repository, turn turnRequest, progress agent.Progress) (agent.Outcome, error) {
	client, err := agent.NewClient(service.cfg.Provider)
	if err != nil {
		return agent.Outcome{}, err
	}
	history := make([]agent.Recollection, len(turn.History))
	for i, earlier := range turn.History {
		history[i] = agent.Recollection{Number: i + 1, Message: earlier.Message, Summary: earlier.Summary, Files: earlier.Files}
	}
	return agent.NewRunner(client, repository).Run(ctx, turn.Message, turn.Images, history, progress)
}

func validate(turn turnRequest) error {
	message := strings.TrimSpace(turn.Message)
	switch {
	case message == "":
		return fmt.Errorf("a message is required")
	case len(message) > maxMessageBytes:
		return fmt.Errorf("the message is over %d bytes", maxMessageBytes)
	case len(turn.History) > maxHistory:
		return fmt.Errorf("history is over %d turns", maxHistory)
	case len(turn.Images) > maxImages:
		return fmt.Errorf("at most %d images per message", maxImages)
	}
	for _, image := range turn.Images {
		if !strings.HasPrefix(image, "data:image/") || len(image) > maxImageBytes {
			return fmt.Errorf("images must be data URLs of at most %d KB", maxImageBytes>>10)
		}
	}
	return validateFiles(turn.Files)
}

// validateFiles holds a project to the limits, and to being plain text
// at plain paths. Most of this is already implied by the signature —
// a signed project was checked when it was signed — but it is checked
// again before the signature, which then only has to be computed over
// something of a known, bounded size.
func validateFiles(files map[string]string) error {
	if len(files) == 0 {
		return fmt.Errorf("the project has no files")
	}
	if len(files) > maxFiles {
		return fmt.Errorf("the project has over %d files", maxFiles)
	}
	total := 0
	for name, content := range files {
		if !utf8.ValidString(name) || name != path.Clean(name) || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") {
			return fmt.Errorf("%q is not a plain relative path", name)
		}
		if len(content) > maxFileBytes {
			return fmt.Errorf("%s is over %d KB", name, maxFileBytes>>10)
		}
		if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
			return fmt.Errorf("%s is not text", name)
		}
		total += len(content)
	}
	if total > maxProjectBytes {
		return fmt.Errorf("the project is over %d KB", maxProjectBytes>>10)
	}
	return nil
}

// eventStream is agent.Progress written as Server-Sent Events to one
// response, flushed as each line happens so the browser shows the work
// while it is still going.
type eventStream struct {
	mutex    sync.Mutex
	response http.ResponseWriter
	flusher  http.Flusher
}

func newEventStream(response http.ResponseWriter) (*eventStream, error) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming unsupported")
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &eventStream{response: response, flusher: flusher}, nil
}

func (stream *eventStream) send(name string, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		return
	}
	stream.mutex.Lock()
	defer stream.mutex.Unlock()
	fmt.Fprintf(stream.response, "event: %s\ndata: %s\n\n", name, body)
	stream.flusher.Flush()
}

func (stream *eventStream) Status(text string) {
	stream.send("status", map[string]string{"text": text})
}
func (stream *eventStream) Log(text string) { stream.send("log", map[string]string{"text": text}) }

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(value)
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}
