#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ -f .yolocoder/web/server.pid ]; then
  while read -r pid; do kill "$pid" 2>/dev/null || true; done < .yolocoder/web/server.pid
  rm -f .yolocoder/web/server.pid
fi
