# YoloCoder

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
line that overwrites itself.

## Debugging

When a provider returns something unexpected, `/debug` in a session shows
every request and reply as they happen. For a full untruncated trace on
disk, including each patch and what Git said about it:

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
