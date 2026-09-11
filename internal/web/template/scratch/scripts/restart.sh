#!/bin/sh
set -e
cd "$(dirname "$0")/.."
sh scripts/stop.sh
sh scripts/start.sh
