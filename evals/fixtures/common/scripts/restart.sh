#!/bin/sh
set -eu
cd "$(dirname "$0")"
sh stop.sh
sh start.sh
