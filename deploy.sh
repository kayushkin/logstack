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

echo "==> Smoke test..."
if ! curl -fsS --max-time 10 "http://localhost:$PORT/api/v1/health" >/dev/null 2>&1; then
  echo "ERROR: $SERVICE not responding on :$PORT/api/v1/health"
  journalctl --user -u "$SERVICE" -n 30 --no-pager 2>&1
  exit 1
fi
echo "    health OK"

# The group-by field list is served from store.GroupFields. A binary built before
# the Source -> Orchestrator rename landed answers 400 here and 200 on
# /group/source (bucketing every log under "unknown"), so this pins the deployed
# binary to the fixed one rather than just to "a binary that starts".
if ! curl -fsS --max-time 10 "http://localhost:$PORT/api/v1/logs/group/orchestrator?limit=1" >/dev/null 2>&1; then
  echo "ERROR: /logs/group/orchestrator rejected - deployed binary predates the Orchestrator rename"
  exit 1
fi
echo "    group/orchestrator OK"
echo "    smoke test OK"

echo "==> Done."
