#!/bin/sh
# Stops whatever start.sh started, using the pids it recorded. Safe to run
# even if nothing is running.
cd "$(dirname "$0")/.."

if [ ! -f .yolocoder/web/server.pid ]; then
  exit 0
fi

while read -r pid; do
  [ -n "$pid" ] || continue
  kill -0 "$pid" 2>/dev/null && kill "$pid" 2>/dev/null || true
done < .yolocoder/web/server.pid

rm -f .yolocoder/web/server.pid .yolocoder/web/port
echo "stopped"
