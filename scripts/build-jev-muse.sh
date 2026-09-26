#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
mkdir -p dist
commit=$(git rev-parse --short HEAD)
"${GO:-go}" build -ldflags "-X github.com/mindsdb/yolocoder/internal/agent.editRouterModel=jev-1.13.0 -X github.com/mindsdb/yolocoder/internal/agent.smallEditModel=muse-spark-1-3 -X github.com/mindsdb/yolocoder/internal/version.Version=jev-muse-review -X github.com/mindsdb/yolocoder/internal/version.Commit=$commit" -o dist/yolocoder-jev-muse ./cmd/yolocoder
printf '%s\n' "Built $root/dist/yolocoder-jev-muse"
