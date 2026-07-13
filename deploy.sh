#!/usr/bin/env bash
set -euo pipefail

# Mirrors agent-store/deploy.sh: builds the cmd/logstack binary, drops it into
# ~/bin/logstack, and bounces the user systemd unit. Same DBus env shim so the
# script works from non-login shells (Claude, automation).

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HOME/bin"
SERVICE="logstack.service"
# Must match ExecStart in ~/.config/systemd/user/logstack.service. A sibling repo
# once shipped a deploy.sh whose BIN_NAME still named the repo it was copied from
# and would have overwritten that other service's binary.
BINARY="logstack"
PORT="8088"

cd "$REPO_DIR"

export PATH="$HOME/.local/share/mise/shims:$PATH"
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
export DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-unix:path=${XDG_RUNTIME_DIR}/bus}"

echo "==> Building $BINARY..."
go build -o "$BINARY" ./cmd/logstack
echo "    built: $(ls -lh "$BINARY" | awk '{print $5}')"

echo "==> Stopping $SERVICE..."
systemctl --user stop "$SERVICE" 2>/dev/null || true
sleep 1

echo "==> Installing binary to $BIN_DIR..."
mkdir -p "$BIN_DIR"
cp "$BINARY" "$BIN_DIR/$BINARY"

echo "==> Starting $SERVICE..."
systemctl --user daemon-reload
systemctl --user start "$SERVICE"

echo "==> Verifying..."
sleep 2
if systemctl --user is-active --quiet "$SERVICE"; then
  echo "    $SERVICE is running"
  journalctl --user -u "$SERVICE" -n 5 --no-pager 2>&1 | grep -v '^--' || true
else
  echo "ERROR: $SERVICE failed to start"
  journalctl --user -u "$SERVICE" -n 20 --no-pager 2>&1
  exit 1
fi

# Wait for the port, don't guess at it. logstack rebuilds its in-memory id index
# by parsing every line of every .jsonl under LOGSTACK_DATA_DIR before it binds --
# 221MB and ~4-5s here, and it grows with the corpus. The old `sleep 2` + one curl
# raced that: the socket was not open yet, curl got connection refused (instantly,
# so --max-time never even applied), and deploy.sh exited 1 having already
# installed and started a perfectly good binary. It reported every deploy as
# broken, which is indistinguishable from reporting none of them.
echo "==> Waiting for :$PORT to accept connections..."
READY_TIMEOUT=60
for i in $(seq 1 "$READY_TIMEOUT"); do
  if curl -fsS --max-time 5 "http://localhost:$PORT/api/v1/health" >/dev/null 2>&1; then
    echo "    health OK (ready after ${i}s)"
    break
  fi
  if ! systemctl --user is-active --quiet "$SERVICE"; then
    echo "ERROR: $SERVICE died while starting up"
    journalctl --user -u "$SERVICE" -n 30 --no-pager 2>&1
    exit 1
  fi
  if [ "$i" -eq "$READY_TIMEOUT" ]; then
    echo "ERROR: $SERVICE still not answering :$PORT/api/v1/health after ${READY_TIMEOUT}s"
    journalctl --user -u "$SERVICE" -n 30 --no-pager 2>&1
    exit 1
  fi
  sleep 1
done

echo "==> Smoke test..."

# The group-by field list is served from store.GroupFields. A binary built before
# the Source -> Orchestrator rename landed answers 400 here and 200 on
# /group/source (bucketing every log under "unknown"), so this pins the deployed
# binary to the fixed one rather than just to "a binary that starts".
#
# Scoped to today deliberately. This probe used to be `?limit=1`, which reads as
# cheap and is not: limit is applied after the scan, so it still swept the default
# 30-day window -- ~2 minutes, well past --max-time 10. The smoke test could
# therefore never pass, and deploy.sh exited 1 on a perfectly good deploy, after
# it had already installed and started the binary. `from=` is the bound that
# actually bounds (0.003s), and it costs the probe nothing: a pre-rename binary
# still answers 400 here whatever the window.
SMOKE_FROM="$(date -u +%Y-%m-%dT00:00:00Z)"
if ! curl -fsS --max-time 10 "http://localhost:$PORT/api/v1/logs/group/orchestrator?from=$SMOKE_FROM" >/dev/null 2>&1; then
  echo "ERROR: /logs/group/orchestrator rejected - deployed binary predates the Orchestrator rename"
  exit 1
fi
echo "    group/orchestrator OK"
echo "    smoke test OK"

echo "==> Done."
