# YoloCoder

A minimal boilerplate for the YoloCoder CLI.

```sh
yolocoder "Add expiration support to user sessions"
```

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

Requires Go 1.22 or newer.

```sh
go run ./cmd/yolocoder
go test ./...
```

## Connect an LLM

On first launch, YoloCoder asks you to connect either:

- MindsHub using browser sign-in
- Any other OpenAI-compatible endpoint using a base URL and API key

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
`yolocoder config reset` to manage the saved provider.

## How it works

YoloCoder keeps the loop deliberately small:

1. Builds a compact, `.gitignore`-aware repository map.
2. Lets the model read likely files or search only when needed.
3. Requests a strict JSON implementation plan.
4. Requests a strict JSON response containing a unified diff.
5. Checks and applies the diff with `git apply`.
6. Runs the repository's detected test command.
7. Retries at most twice using only patch or test failure evidence.

The model never receives a shell tool. Local code exposes only bounded
`read_files` and `search` tools during context gathering. Patch application
and testing are deterministic local operations.

Release builds check the rolling `latest` GitHub release whenever the CLI
starts. If a newer build is available, YoloCoder verifies its SHA-256 checksum
and replaces the current binary. The new build runs on the next invocation.
Set `YOLOCODER_NO_AUTOUPDATE=1` to disable this behavior. Normal startup also
prints the embedded version and short commit, for example `YoloCoder main
(68542b0)`.

Run `yolocoder update` to force an immediate check and bypass any saved check
interval from an older installation.

Interactive startup uses a small `[*_*]` robot animation while YoloCoder checks
for and installs updates. Non-interactive output remains animation-free.

Version tags matching `v*` create permanent GitHub releases. Every push to
`main` refreshes the rolling `latest` release used by the self-updater.
