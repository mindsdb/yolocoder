#!/bin/sh
# Starts the API (Express, tsx watch) and the frontend (Vite) backgrounded
# and detached, so both keep running after this script returns. Re-run
# safely: if the pidfile already names a live process, this is a no-op.
set -e
cd "$(dirname "$0")/.."
mkdir -p .yolocoder/web

if [ -f .yolocoder/web/server.pid ]; then
  existing="$(head -n1 .yolocoder/web/server.pid 2>/dev/null || true)"
  if [ -n "$existing" ] && kill -0 "$existing" 2>/dev/null; then
    echo "already running"
    exit 0
  fi
fi

: > .yolocoder/web/server.log

nohup node_modules/.bin/tsx watch server/index.ts >> .yolocoder/web/server.log 2>&1 &
api_pid=$!

nohup node_modules/.bin/vite --port 5173 --strictPort >> .yolocoder/web/server.log 2>&1 &
web_pid=$!

printf '%s\n%s\n' "$web_pid" "$api_pid" > .yolocoder/web/server.pid
echo 5173 > .yolocoder/web/port

echo "started (web pid $web_pid, api pid $api_pid, port 5173)"
