#!/usr/bin/env bash
# Rebuilds and restarts the locally-running worker + controlplane
# processes — the stop/rebuild/start cycle every Go change needs, since
# neither process picks up a rebuilt binary on its own. cmd/controlplane's
# static assets are go:embed'd into the binary, so even a frontend-only
# change (app.js/app.css/images) needs this too, not just a browser
# refresh.
#
# Builds first, before stopping anything: a broken build then leaves the
# previously-running processes untouched rather than killing a working
# service for nothing.
#
# Usage: scripts/restart.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

WORKER_BIN=bin/worker
CONTROLPLANE_BIN=bin/controlplane
WORKER_LOG=/tmp/factory-worker.log
CONTROLPLANE_LOG=/tmp/factory-controlplane.log

echo "==> Building"
go build -o "$WORKER_BIN" ./cmd/worker
go build -o "$CONTROLPLANE_BIN" ./cmd/controlplane

# stop sends SIGTERM, waits up to 10s for the process to actually exit,
# then falls back to SIGKILL — must be sure it's actually gone before the
# next start, or that start becomes a duplicate process still holding
# the same port instead of a clean replacement.
stop() {
	local pattern="$1"
	local pids
	pids=$(pgrep -f "$pattern" || true)
	[ -z "$pids" ] && return 0

	echo "==> Stopping $pattern ($pids)"
	kill $pids 2>/dev/null || true
	for _ in $(seq 1 20); do
		pgrep -f "$pattern" >/dev/null || return 0
		sleep 0.5
	done
	echo "==> $pattern still running after SIGTERM, sending SIGKILL"
	kill -9 $(pgrep -f "$pattern") 2>/dev/null || true
}

stop "$WORKER_BIN"
stop "$CONTROLPLANE_BIN"

echo "==> Starting worker (log: $WORKER_LOG)"
nohup "./$WORKER_BIN" >"$WORKER_LOG" 2>&1 &
disown

echo "==> Starting controlplane (log: $CONTROLPLANE_LOG)"
nohup "./$CONTROLPLANE_BIN" >"$CONTROLPLANE_LOG" 2>&1 &
disown

sleep 1

echo "==> Status"
pgrep -af "$WORKER_BIN" || echo "worker: NOT RUNNING (see $WORKER_LOG)"
pgrep -af "$CONTROLPLANE_BIN" || echo "controlplane: NOT RUNNING (see $CONTROLPLANE_LOG)"

port="${CONTROLPLANE_ADDR:-:8082}"
port="${port##*:}"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:${port}/" || echo "unreachable")
echo "controlplane health: $code"
