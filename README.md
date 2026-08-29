# YoloCoder

A minimal boilerplate for the YoloCoder CLI.

```text
Welcome to YoloCode CLI
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

Release builds check the rolling `latest` GitHub release once per day when
the CLI starts. If a newer build is available, YoloCoder verifies its SHA-256
checksum and replaces the current binary. Set `YOLOCODER_NO_AUTOUPDATE=1` to
disable this behavior.

Version tags matching `v*` create permanent GitHub releases. Every push to
`main` refreshes the rolling `latest` release used by the self-updater.
