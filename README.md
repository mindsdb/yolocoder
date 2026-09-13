# [^_^] YoloCoder

A minimal boilerplate for the YoloCoder CLI.

```sh
yolocoder "Add expiration support to user sessions"
```

At startup, YoloCoder prints the current folder it will work in. No Git
repository is required: YoloCoder maps and patches the folder directly. If
the folder already has its own `.git` (not an ancestor directory's), it's
used opportunistically for a `.gitignore`-aware map and faster patch checks.

Passing a task runs it once and exits. Running `yolocoder` with no task starts
an interactive session instead: it prompts, works, reports what it did, then
prompts again, all in one continuous transcript. Leave with `/exit` or Ctrl+C.

At the prompt, paste multiline instructions normally, use arrow keys to
reposition the cursor, press Enter to send, and press Shift+Enter for a new
line (falls back to Enter-only in terminals that don't report Shift+Enter
separately).

While it works, YoloCoder prints what it is actually doing — the files it
reads, the searches it runs, the plan, the patch, and the test result — so
the finished session leaves a readable trail rather than a single status
line that overwrites itself. Under the reply, a dim line reports what the
turn cost — total tokens, input (and how much of that was served from
cache, when the provider reports it), and output — summed across every
call the turn took, not just the last one. Silent when the provider
doesn't report usage at all.

## Web UI

```sh
yolocoder --web
yolocoder --web "build a todo app"   # empty folder: scaffold, then run this first
yolocoder --web --port 8080
```

Serves a local page with chat on one side and a live preview of the app in
an iframe on the other, backed by the same agent loop the terminal uses.
It needs Node.js; if it isn't on your PATH, you're offered an install
before anything else runs.

For now, `--web` only works in two kinds of folder: an empty one, which it
scaffolds a small starter into, or one it already scaffolded (marked by a
`yolocoder.json` at its root). Anything else is refused rather than
guessed at. The starter is `frontend/` (Vite + React + Tailwind v4) and
`backend/` (Express + Drizzle + better-sqlite3) as two dev servers in one
`package.json` — see the scaffold's own `ARCHITECTURE.md` for the exact
layout. It's shadcn-ready (`components.json`, the right path aliases, the
theme already in `frontend/src/index.css`) but ships no UI primitives:
`npx shadcn@latest add <component>` installs whatever a component needs
the first time one is actually added, rather than a `Button` sitting
unused in every fresh project. `scripts/start.sh`, `scripts/restart.sh`
and `scripts/stop.sh` are safe to run by hand from a terminal; `start.sh`
launches both dev servers backgrounded and detached (they start
automatically when `--web` does), and reports the frontend's port and
combined log through `.yolocoder/web/`. If those scripts ever go missing
from a yolocoder project, `--web` restores just them, leaving the rest of
the project untouched. Type `exit` or `quit` into the terminal `--web` is
running in (or Ctrl+C) to stop it.

The preview is reverse-proxied rather than pointed straight at the dev
server — on its own dedicated port, at the root of its own origin, since a
dev server's own absolute asset paths (Vite's `/src/main.tsx`, for example)
are written assuming they own the whole origin and resolve wrongly if
mounted under a path prefix instead. Being proxied is also what lets a
small script be injected into the page that reports uncaught errors and
unhandled promise rejections back to the chat. Combined with the dev
server's own log, a server-side or browser-side error while the app is
running is turned into a task and fixed automatically, then the dev server
is restarted — up to two attempts per distinct error, after which it's left
for you instead of retried forever. Whenever the dev server can't be
reached at all — starting up, restarting, or the health check catching an
actual crash — the preview shows a small page that polls itself and
reloads the moment it's back, so the iframe recovers on its own instead of
sitting on a stale error until the browser is refreshed by hand.

Each run's chat pane starts fresh rather than replaying this folder's whole
recorded history: that history exists to give the agent context to reason
from, not to be shown verbatim as a transcript, and a folder used across
many separate runs (or from the plain terminal too) accumulates turns that
read as a confusing backlog rather than one conversation.

Every chat message goes through the same build → load → review loop:
**build** is the agent making the change, streamed live to the chat and
console; **load** just tells the iframe to reload once a change is
applied — the dev server has already picked it up on its own by then
(Vite's HMR, `tsx watch`'s own restart), so this never restarts a process
itself, only makes sure the browser actually shows it; **review** is the
error watcher, which runs continuously regardless of any one task and
turns a server or browser error into another build → load → review cycle
automatically. Long messages in chat (a stack trace, in particular)
collapse behind a one-line summary — click to expand. Each message that
starts a turn gets its own "Agent activity" section right under it —
the agent's step-by-step trail and the dev server's log for that turn,
open and live while it runs, collapsing once the reply lands — rather
than one shared console for the whole conversation. A reply that cost
something the provider reported gets a small, muted caption of its own
underneath it — total tokens, input (cached, if reported), output —
summed across every call the turn took; silent otherwise.

The chat panel — titled with the same `[^_^] YoloCoder` the terminal
prints at startup, with the folder it's running in shown just below —
can be collapsed to give the preview the full window; a small button in
its header brings it back. A model picker sits under the message input:
it lists what the endpoint's `/v1/models` offers, the same way
`yolocoder model` does on the terminal, and switching it there takes
effect immediately and is saved, unless the provider came from
`--llm-from-env-vars`, which — like the terminal's `/model` — it can't
change.

The dev server is watched for actually being alive, not just for error
text reaching its log: if it stops answering on its own (a crash, an OS
resource limit) it's restarted automatically, without any task involved,
up to a few attempts within a couple of minutes before giving up and
showing a Restart button — the only control surface `--web` has, and only
once automatic recovery has actually stopped trying.

## Debugging

When a provider returns something unexpected, `/debug` in a session shows
every request and reply as they happen. `--debug` does the same from the
command line — `yolocoder --web --debug` in particular, since there's no
`/debug` command in the browser: the trace always prints to the terminal
`--web` was launched from, never to the web UI itself. For a full
untruncated trace on disk, including each patch and what Git said about it:

```sh
YOLOCODER_DEBUG_LOG=1 yolocoder          # ~/.config/yolocoder/debug.log
YOLOCODER_DEBUG_LOG=/tmp/trace.log yolocoder
```

The trace holds the contents of the files being worked on, so it is written
with user-only permissions. It never contains the API key, which travels in
a header rather than the request body.

## Install

macOS and Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/mindsdb/yolocoder/main/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/mindsdb/yolocoder/main/install.ps1 | iex
```

## Develop

Requires Go 1.24 or newer.

```sh
go run ./cmd/yolocoder
go test ./...
```

## Connect an LLM

On first launch, YoloCoder asks you to connect either:

- MindsHub using browser sign-in
- Custom: any OpenAI-compatible endpoint using a base URL and API key

"OpenAI-compatible" covers two different APIs. YoloCoder speaks both: the
Responses API, and the `/v1/chat/completions` that most providers (Cerebras,
Groq, Together, Ollama, vLLM and others) offer instead. Connecting a custom
endpoint checks which one it has and saves that alongside it, so there is
nothing to configure by hand.

The endpoint configuration is saved to `~/.config/yolocoder/config.json`.
The API key is stored separately in `~/.config/yolocoder/credentials.json`
with user-only permissions.

For a one-off environment-based connection that is not persisted:

```sh
OPENAI_BASE_URL=https://api.example.com/v1 \
OPENAI_API_KEY=sk-example \
OPENAI_MODEL=gpt-5.2-codex \
yolocoder --llm-from-env-vars "Add expiration support to user sessions"
```

Use `yolocoder config show`, `yolocoder config connect`, or
`yolocoder config reset` to manage the saved provider. Inside a session,
`/setup` runs the same connect flow and the session picks up the new
provider straight away.

Run `yolocoder model` to switch models on the saved provider: it lists what
the endpoint's `/v1/models` offers (MindsHub exposes several) and lets you
pick one, or pass a name directly with `yolocoder model <name>`.

## How it works

YoloCoder keeps the loop deliberately small:

1. Routes the message: a plain conversational message gets the model's
   direct reply and stops there; only a coding task continues below.
2. Builds a compact map of the current folder (`.gitignore`-aware when it
   already has its own Git repository, a plain walk otherwise).
3. Opens one conversation that reads the files it needs (or searches, when
   the map isn't enough) and then answers with a summary, the files it
   touches, and a unified diff. Planning and patching are the same request:
   the files are already in the conversation from the tool calls, so asking
   separately would resend all of them to learn nothing new.
4. Applies the diff with `git apply`, which works directly against the
   folder without requiring a Git repository. If Git rejects it, the hunks
   are placed by matching their content instead, since a model reliably
   gets the content right and the line numbers and counts wrong.
5. Runs the repository's detected test command.
6. Retries at most twice, continuing the same conversation so a repair
   costs only the failure evidence rather than the whole context again.
7. Falls back to writing whole files when no diff will apply at all.

The model never receives a shell tool. Local code exposes only bounded
`read_files` and `search` tools during context gathering. Patch application
and testing are deterministic local operations.

Release builds check the rolling `latest` GitHub release whenever the CLI
starts. If a newer build is available, YoloCoder verifies its SHA-256 checksum,
replaces the current binary, says so, and restarts into it, so the run you
just started continues on the new build rather than the one it launched with.
Set `YOLOCODER_NO_AUTOUPDATE=1` to disable this behavior. Normal startup also
prints the embedded version and short commit, for example `YoloCoder main
(68542b0)`.

Run `yolocoder update` to force an immediate check and bypass any saved check
interval from an older installation.

Interactive startup uses a small `[*_*]` robot animation while YoloCoder checks
for and installs updates. Non-interactive output remains animation-free.

Version tags matching `v*` create permanent GitHub releases. Every push to
`main` refreshes the rolling `latest` release used by the self-updater.
