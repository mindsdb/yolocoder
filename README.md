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
for you instead of retried forever. A page that keeps throwing the same
error on every render while a fix is already in flight doesn't queue a
fresh attempt for each repeat: reports arriving while one is already
running are dropped, the same way a burst of log lines from one error is
coalesced into a single report rather than one per line. Whenever the
dev server can't be
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
something the provider reported gets a small, muted footer of its own
underneath it — `in: 1,234 (120 cached) | out: 344` — summed across
every call the turn took; silent otherwise.

The chat panel — titled with the same `[^_^] YoloCoder` the terminal
prints at startup, with the folder it's running in shown just below —
can be collapsed to give the preview the full window; a small button in
its header brings it back. Enter sends the message; Shift+Enter starts a
new line instead, noted in a small hint on the other side of the input
footer. A model picker sits there too: it lists what the endpoint's
`/v1/models` offers, the same way `yolocoder model` does on the
terminal, grouped by provider when the endpoint's own listing says who
owns each model (a multi-vendor gateway like Groq or Together, typically)
and left flat when it doesn't (MindsHub included, whose own models have
nothing to group by). Switching it takes effect immediately and is
saved, unless the provider came from `--llm-from-env-vars`, which — like
the terminal's `/model` — it can't change.

Pasting a screenshot into the message box attaches it to that turn — a
small thumbnail appears above the input, click its × to drop it before
sending. It's downscaled in the browser first, so a retina screenshot
doesn't turn into an oversized request. Sent images show up inline in
that message's own bubble. This only actually helps when the connected
model is multimodal; most aren't, and there's no detection of that up
front — an attached screenshot a model can't see is simply ignored the
way any other vision content would be.

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

1. Builds a compact map of the current folder (`.gitignore`-aware when it
   already has its own Git repository, a plain walk otherwise).
2. Opens one conversation with that map and the message, and works in it
   until there is something to say. The model reads with `read_files` and
   `search`, edits with `apply_diff`, and finishes by replying in plain
   markdown with no tool call — which is the reply you read. Deciding
   whether the message was a task, a question or ordinary conversation
   happens there too, by the call that can act on the answer. A separate
   routing call used to come first, but it had no tools and so couldn't
   settle "question or change?" for anything needing the files to answer;
   it said "change" and deferred, costing a round trip every turn.
3. Places each edit by matching its text against the file. Nothing is
   written unless every edit in the patch can be placed, and a rejection
   comes back as that tool's result — naming each edit it could not find
   and the closest line to it — so the repair continues the same
   conversation rather than starting a fresh attempt.
4. Runs the repository's detected check command when the model tries to
   finish, and sends it back to work if that fails. It cannot declare
   victory over a build it just broke.
5. Falls back to writing whole files when it runs out of edits or rounds
   without landing anything.

Each tool has its own budget for a turn rather than sharing one ceiling,
because they go wrong differently: a model that can't place a hunk will
spend every round retrying the edit, where one that's merely exploring
reads a few files and stops.

Edits use a compact patch format, not a unified diff — no `@@` markers,
no line numbers, no counts, since none of it is read anyway:

```
@path            modify this file
@+path           create it; every following line is its literal content

 context before
-old line
+new line
 context after
```

A blank line separates one edit from the next, and context is optional.
That leaves the model nothing to get right except the text itself, which
is the only part that matters: a line differing by one character can't be
found. Unified diffs and the `*** Begin Patch` format still parse, for
models that reach for them anyway.

The request carries tools and no JSON schema. Asking for both in one
request is what produced the provider workarounds in `client.go` — some
refuse the combination outright, and at least one pins `tool_choice` to
`required` and then fails when the model would rather reply than call a
tool. Tools plus a plain-message ending is the one shape every
OpenAI-compatible provider implements the same way.

The last three turns in the folder ride along in that opening message.
They're small — around 1.5 KB on a real folder — and they cover the
follow-ups that make up most of a session ("make it bigger", "undo that",
"now the other one"), so fetching them separately would cost a round trip
to save almost nothing. Choosing which earlier turns were relevant used to
be its own model call, shown every recorded turn to pick out a few; that
cost a round trip on the coding model every turn to narrow a few kilobytes.

Anything older than those three is not sent at all. `/recall` in a session
turns on a `recall` tool that reads the ten turns below them on request,
for a message that reaches back further than what it was shown. It's off
by default: an unused tool is still a definition on every request, and the
recent turns answer nearly everything on their own. A turn in that history
is what was asked and what came of it, nothing else — around 280 bytes
each on a real folder.

The model never receives a shell tool. Local code exposes only bounded
`read_files`, `search` and `apply_diff`, plus `recall` when it's switched
on. Placing edits and running the check are deterministic local
operations.

Some OpenAI-compatible providers pin `tool_choice` to `required` whenever
tools are offered — YoloCoder only ever sends `auto` — and then fail the
request outright when the model would rather reply than call a tool, which
is exactly what a message needing no files does. That failure is caught and
the request is retried once with no tools, which is what the model was
trying to do anyway.

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
