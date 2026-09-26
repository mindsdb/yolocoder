#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p .yolocoder/web
if [ -f .yolocoder/web/server.pid ]; then
  first=$(head -n 1 .yolocoder/web/server.pid)
  if kill -0 "$first" 2>/dev/null; then exit 0; fi
fi
: > .yolocoder/web/server.log
node_modules/.bin/tsx watch backend/index.ts >> .yolocoder/web/server.log 2>&1 &
backend_pid=$!
node_modules/.bin/vite --config frontend/vite.config.ts >> .yolocoder/web/server.log 2>&1 &
frontend_pid=$!
printf '%s\n%s\n' "$frontend_pid" "$backend_pid" > .yolocoder/web/server.pid
printf '%s\n' "$FRONTEND_PORT" > .yolocoder/web/port
