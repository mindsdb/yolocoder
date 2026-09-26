package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestInitialContextStarterDecisions(t *testing.T) {
	files := map[string]string{
		"web/src/App.tsx":      "export default function App() { return <main>Starter</main> }\n",
		"web/src/main.tsx":     "import App from './App';\nexport const app = App;\n",
		"web/src/styles.css":   "body { margin: 0; font-family: sans-serif; }\n",
		"web/src/widgets.tsx":  "export const Empty = () => <p>No data yet</p>;\n",
		"server/index.ts":      "import { health } from './routes';\nexport const routes = { health };\n",
		"server/routes.ts":     "export const health = () => ({ ok: true });\n",
		"web/vite.config.ts":   "export default { server: { port: 3000 } };\n",
		"web/src/large.ts":     strings.Repeat("// source\n", 1300),
		"server/large.ts":      strings.Repeat("// source\n", 1300),
		"package.json":         `{"private":true,"workspaces":["web","server"]}`,
		"tsconfig.json":        `{"compilerOptions":{"strict":true}}`,
		"web/package.json":     `{"name":"web","dependencies":{"react":"*"}}`,
		"web/tsconfig.json":    `{"extends":"../tsconfig.json","compilerOptions":{"jsx":"react-jsx"}}`,
		"server/package.json":  `{"name":"server","type":"module"}`,
		"server/tsconfig.json": `{"extends":"../tsconfig.json"}`,
	}
	var safePaths []string
	for path := range files {
		if filepath.Ext(path) != ".json" {
			safePaths = append(safePaths, path)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(safePaths)))
	for _, tc := range []struct {
		name, route, reason string
		probability         map[string]float64
		want                []string
	}{
		{"normal entry points", "normal", "accepted=3 selected=3 reason=confident_selection",
			map[string]float64{"web/src/App.tsx": .98, "server/index.ts": .99, "web/src/styles.css": .97},
			[]string{"server/index.ts", "web/src/App.tsx", "web/src/styles.css"}},
		{"normal overflow ranks", "normal", "accepted=6 selected=4 reason=bounded_selection",
			map[string]float64{"web/src/App.tsx": .98, "server/index.ts": .99, "web/src/styles.css": .97, "server/routes.ts": .96, "web/src/main.tsx": .95, "web/vite.config.ts": .81},
			[]string{"server/index.ts", "web/src/App.tsx", "web/src/styles.css", "server/routes.ts"}},
		{"normal tie order", "normal", "accepted=5 selected=4 reason=bounded_selection",
			map[string]float64{"web/src/App.tsx": .9, "server/index.ts": .9, "web/src/styles.css": .9, "server/routes.ts": .9, "web/src/main.tsx": .9},
			[]string{"server/index.ts", "server/routes.ts", "web/src/App.tsx", "web/src/main.tsx"}},
		{"normal byte bound", "normal", "accepted=4 selected=3 reason=bounded_selection",
			map[string]float64{"web/src/large.ts": .99, "server/large.ts": .98, "server/index.ts": .97, "web/src/styles.css": .96},
			[]string{"web/src/large.ts", "server/index.ts", "web/src/styles.css"}},
		{"normal no confident files", "normal", "accepted=0 selected=0 reason=no_confident_files",
			map[string]float64{"web/src/App.tsx": .79}, nil},
		{"small UI preserves selection", "small_ui", "accepted=2 selected=2 reason=confident_selection",
			map[string]float64{"web/src/App.tsx": .99, "web/src/styles.css": .98},
			[]string{"web/src/styles.css", "web/src/App.tsx"}},
		{"small UI overflow falls back", "small_ui", "accepted=5 selected=0 reason=small_ui_overflow",
			map[string]float64{"web/src/App.tsx": .99, "server/index.ts": .99, "web/src/styles.css": .99, "server/routes.ts": .99, "web/src/main.tsx": .99}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := folder(t, files)
			outside := filepath.Join(t.TempDir(), "outside.ts")
			if err := os.WriteFile(outside, []byte("private outside content"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(repository.Root, "linked.ts")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(repository.Root, "directory.ts"), 0700); err != nil {
				t.Fatal(err)
			}
			mapped := append([]string(nil), safePaths...)
			for path := range files {
				if filepath.Ext(path) == ".json" {
					mapped = append(mapped, path)
				}
			}
			mapped = append(mapped, "linked.ts", "directory.ts", outside, "../outside.ts")
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request struct {
					Questions map[string]any `json:"questions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if len(request.Questions) != len(safePaths)+1 {
					t.Errorf("unsafe or non-source candidates: questions=%d", len(request.Questions))
				}
				answers := map[string]editAnswer{"route": {Type: "choice", Choice: tc.route, Probabilities: map[string]float64{tc.route: .99}}}
				for i, path := range safePaths {
					answers[fmt.Sprintf("file_%d", i)] = editAnswer{Type: "choice", Choice: "read", Probabilities: map[string]float64{"read": tc.probability[path]}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-test", "answers": answers})
			}))
			defer server.Close()
			runner := NewRunner(&Client{baseURL: server.URL, http: server.Client()}, repository)
			runner.editRouterModel = "jev-test"
			mapping, err := repository.Map()
			if err != nil {
				t.Fatal(err)
			}
			task := "Build a searchable inventory editor using the existing frontend and API scaffold."
			if tc.route == "small_ui" {
				task = "Change the existing heading from Starter to Inventory and the body font to monospace."
			}
			session := runner.newChangeSession(task, mapping, nil, nil)
			progress := &recordingProgress{}
			runner.prefetchEdit(context.Background(), session, mapped, progress)
			logs := strings.Join(progress.logs, "\n")
			if requests != 1 || !strings.Contains(logs, tc.reason) {
				t.Fatalf("requests=%d logs=%s", requests, logs)
			}
			if len(tc.want) == 0 {
				if len(session.transcript) != 1 || session.used["read_files"] != 0 || len(runner.served) != 0 {
					t.Fatal("fallback changed read state")
				}
			} else {
				if len(session.readPaths) < len(tc.want) || !reflect.DeepEqual(session.readPaths[:len(tc.want)], tc.want) {
					t.Fatalf("read=%v want initial=%v", session.readPaths, tc.want)
				}
				if session.used["read_files"] != 1 || len(session.readPaths) > 8 || len(session.transcript) != 3 {
					t.Fatal("read quota or transcript bounds changed")
				}
				output := session.transcript[2].(toolOutput).Output
				if len(output) > prefetchMaxBytes || strings.Contains(output, "private outside content") {
					t.Fatal("unsafe or oversized context")
				}
			}
			if session.prefetched != (tc.route == "small_ui" && len(tc.want) > 0) {
				t.Fatal("normal prefetch changed small-UI routing")
			}
			t.Logf("initial sources=%v; %s", tc.want, logs)
		})
	}
}
