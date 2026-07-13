#!/usr/bin/env bash
# Boot-and-answer smoke test for logstack.
#
# Builds cmd/logstack from THIS checkout, boots it against a throwaway data
# directory on a throwaway port, writes a handful of log entries through the
# real HTTP API, and reads them back. Asserts on parsed content, not on 200s.
#
# The point is to catch a binary that compiles green and is still dead — a
# duplicate/conflicting route registration panics gin at startup, and no
# compiler sees that. `go build` proves nothing about whether the thing boots.
#
# Hermetic: temp data dir, temp port, NATS pointed at a closed port. It never
# reads or writes the live corpus ($LOGSTACK_DATA_DIR, ~/.inber/logs), never
# touches the live port (8088), and never joins the live JetStream consumer.
# No credentials, no external network, no other service required.
#
# Exits 0 on success, non-zero on the FIRST failing assertion, dumping the
# server log to stderr.
#
# Tunables:
#   E2E_PORT  — listen port (default 19102)
#   E2E_KEEP  — set to "1" to keep $TMP_DIR after the run

set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
PORT="${E2E_PORT:-19102}"
BASE="http://127.0.0.1:$PORT"

# Every curl is bounded. logstack's query path scans and sorts the corpus in
# process; a performance regression there (there has been one: an O(n^2) sort
# that made an unscoped group query take ~2 minutes) must surface as a smoke
# FAILURE, not as a nightly job that hangs until someone notices.
CURL_MAX_TIME=10

for bin in go curl jq; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "ERROR: required tool '$bin' not found on PATH" >&2
    exit 2
  fi
done

TMP_DIR="$(mktemp -d -t logstack-e2e.XXXXXX)"
BIN_DIR="$TMP_DIR/bin"
DATA_DIR="$TMP_DIR/data"
SERVER_LOG="$TMP_DIR/server.log"
mkdir -p "$BIN_DIR" "$DATA_DIR"

SERVER_PID=""

dump_log() {
  echo "----- server.log ($SERVER_LOG) -----" >&2
  cat "$SERVER_LOG" >&2 2>/dev/null || echo "(no server log)" >&2
  echo "----- end server.log -----" >&2
}

cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [ "${E2E_KEEP:-}" = "1" ]; then
    echo "[e2e] keeping $TMP_DIR"
  else
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT INT TERM

step() { printf '\n==> %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; dump_log; exit 1; }

# ---------------------------------------------------------------------------
# Fixtures
#
# Timestamps are pinned and explicit rather than left to the server's
# time.Now(). FileStore buckets entries into $DATA_DIR/<date>/<orchestrator>.jsonl
# using the entry's own timestamp, and getDirsToScan turns a `from=` into the
# same date strings — so pinning one UTC instant makes the write path and the
# read path agree regardless of the host's timezone (and regardless of the run
# straddling midnight).
# ---------------------------------------------------------------------------
NOW_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
DAY="${NOW_UTC%%T*}"
# Scope every query. `from=` is the bound that actually bounds: limit is applied
# after the scan+sort, so `?limit=1` is not a cheap probe (deploy.sh learned
# this the hard way). Our corpus is three entries in a temp dir, so this costs
# nothing here — it is here so a regression shows up as a timeout, not a stall.
FROM="${DAY}T00:00:00Z"
ORCH="e2e-smoke"
AGENT="e2e-agent"
SESSION="e2e-session-$$"
MARKER="e2e-marker-$$-${RANDOM}"

start_server() {
  # cwd is the temp dir, not the repo: if LOGSTACK_DATA_DIR were ever ignored,
  # the store's "./logs" default must not land in a real tree.
  #
  # NATS_URL points at a closed port on purpose. With the default
  # nats://localhost:4222 this smoke would attach to the live bus, subscribe to
  # logs.> and chat.inbound.*, and — worse — bind the durable JetStream consumer
  # named "logstack" on chat.outbound, stealing messages from the real service.
  # bus.Connect fails fast on a refused connection and main() logs-and-continues.
  # `exec` in the subshell so $! is the server itself and cleanup's kill/wait
  # act on a direct child, not on an orphaned grandchild.
  (
    cd "$TMP_DIR" && exec env \
      LOGSTACK_PORT="$PORT" \
      LOGSTACK_DATA_DIR="$DATA_DIR" \
      GIN_MODE=release \
      NATS_URL="nats://127.0.0.1:1" \
      "$BIN_DIR/logstack"
  ) >>"$SERVER_LOG" 2>&1 &
  SERVER_PID=$!
  echo "    pid: $SERVER_PID"
}

# Poll, never sleep-and-hope. logstack rebuilds its whole in-memory id index by
# parsing every .jsonl line under LOGSTACK_DATA_DIR *before* it binds the port.
# A fixed `sleep N` raced exactly that and made deploy.sh report every deploy as
# broken. Bounded attempts, then fail loudly.
wait_ready() {
  local attempts=60 i status
  for i in $(seq 1 "$attempts"); do
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
      fail "server exited during startup (it did not even reach the listener)"
    fi
    status="$(curl -fsS --max-time "$CURL_MAX_TIME" "$BASE/api/v1/health" 2>/dev/null | jq -r '.status // empty' || true)"
    if [ "$status" = "healthy" ]; then
      echo "    health OK (ready after $i poll(s), ~0.5s apart)"
      return 0
    fi
    sleep 0.5
  done
  fail "server did not answer $BASE/api/v1/health within $((attempts / 2))s"
}

stop_server() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  SERVER_PID=""
}

# get <path> / post <path> <json> → response body on stdout.
#
# -f so any HTTP error is an error, --max-time so a hang is an error. Both route
# failures through fail(), which dumps the server log; they run inside command
# substitution, so their exit 1 propagates out through set -e and stops the run
# at the FIRST failing assertion rather than cascading.
get() {
  local body
  if ! body="$(curl -fsS --max-time "$CURL_MAX_TIME" "$BASE$1" 2>&1)"; then
    fail "GET $1 failed: $body"
  fi
  printf '%s' "$body"
}

post() {
  local body
  if ! body="$(curl -fsS --max-time "$CURL_MAX_TIME" -X POST "$BASE$1" \
      -H 'Content-Type: application/json' -d "$2" 2>&1)"; then
    fail "POST $1 failed: $body"
  fi
  printf '%s' "$body"
}

# status_of <path> → bare HTTP status code (for the assertions that expect 4xx).
status_of() {
  curl -s -o /dev/null -w '%{http_code}' --max-time "$CURL_MAX_TIME" "$BASE$1"
}

step "build cmd/logstack from $REPO_DIR"
cd "$REPO_DIR"
go build -o "$BIN_DIR/logstack" ./cmd/logstack
echo "    built: $(stat -c %s "$BIN_DIR/logstack") bytes"

step "boot on :$PORT (data dir: $DATA_DIR)"
start_server
wait_ready

step "POST /api/v1/logs"
CREATE="$(post /api/v1/logs "{
    \"timestamp\": \"$NOW_UTC\",
    \"orchestrator\": \"$ORCH\",
    \"agent\": \"$AGENT\",
    \"channel\": \"e2e\",
    \"session_id\": \"$SESSION\",
    \"model\": \"claude-sonnet-4-5\",
    \"level\": \"info\",
    \"type\": \"outbound\",
    \"content\": {\"text\": \"$MARKER\", \"author\": \"$AGENT\"},
    \"stats\": {\"input_tokens\": 11, \"output_tokens\": 22, \"duration_ms\": 33, \"model\": \"claude-sonnet-4-5\"}
  }")"

LOG_ID="$(jq -r '.id // empty' <<<"$CREATE")"
[ -n "$LOG_ID" ] || fail "POST /api/v1/logs returned no id: $CREATE"
[ "$(jq -r '.status // empty' <<<"$CREATE")" = "created" ] || fail "POST /api/v1/logs status != created: $CREATE"
echo "    id: $LOG_ID"

# The store is the reason this service exists. Assert it wrote where we told it
# to — this is also what proves the run never went near the live corpus.
EXPECT_FILE="$DATA_DIR/$DAY/$ORCH.jsonl"
[ -f "$EXPECT_FILE" ] || fail "expected JSONL at $EXPECT_FILE — LOGSTACK_DATA_DIR not honoured?"
echo "    wrote: $EXPECT_FILE"

step "POST /api/v1/logs/batch (2 entries)"
BATCH="$(post /api/v1/logs/batch "[
    {\"timestamp\":\"$NOW_UTC\",\"orchestrator\":\"$ORCH\",\"agent\":\"$AGENT\",\"session_id\":\"$SESSION\",\"level\":\"error\",\"type\":\"error\",\"content\":{\"message\":\"$MARKER-batch-1\"}},
    {\"timestamp\":\"$NOW_UTC\",\"orchestrator\":\"$ORCH\",\"agent\":\"$AGENT\",\"session_id\":\"$SESSION\",\"level\":\"info\",\"type\":\"inbound\",\"content\":{\"text\":\"$MARKER-batch-2\"}}
  ]")"
CREATED="$(jq -r '.created // 0' <<<"$BATCH")"
FAILED="$(jq -r '.failed // 0' <<<"$BATCH")"
[ "$CREATED" = "2" ] || fail "batch created=$CREATED, want 2: $BATCH"
[ "$FAILED" = "0" ] || fail "batch failed=$FAILED, want 0: $BATCH"
echo "    created: $CREATED"

step "GET /api/v1/logs/:id — read back what we wrote"
ENTRY="$(get "/api/v1/logs/$LOG_ID")"
GOT_TEXT="$(jq -r '.content.text // empty' <<<"$ENTRY")"
GOT_ORCH="$(jq -r '.orchestrator // empty' <<<"$ENTRY")"
[ "$GOT_TEXT" = "$MARKER" ] || fail "GET /logs/$LOG_ID content.text='$GOT_TEXT', want '$MARKER'"
[ "$GOT_ORCH" = "$ORCH" ] || fail "GET /logs/$LOG_ID orchestrator='$GOT_ORCH', want '$ORCH'"
echo "    content.text: $GOT_TEXT"

step "GET /api/v1/logs?orchestrator=$ORCH&from=$FROM"
QUERY="$(get "/api/v1/logs?orchestrator=$ORCH&session_id=$SESSION&from=$FROM")"
COUNT="$(jq -r '.count // 0' <<<"$QUERY")"
[ "$COUNT" = "3" ] || fail "query count=$COUNT, want 3 (the 1 single + 2 batch entries): $QUERY"
jq -e --arg m "$MARKER" 'any(.logs[]; .content.text == $m)' <<<"$QUERY" >/dev/null \
  || fail "query result did not contain the entry we POSTed (marker $MARKER)"
jq -e --arg m "$MARKER-batch-1" 'any(.logs[]; .content.message == $m)' <<<"$QUERY" >/dev/null \
  || fail "query result did not contain batch entry 1 (marker $MARKER-batch-1)"
echo "    count: $COUNT (marker + both batch entries present)"

step "GET /api/v1/logs/group/orchestrator?from=$FROM"
# Pins the deployed binary to the post-rename store.GroupFields. A binary built
# before the Source -> Orchestrator rename answers 400 here and 200 on
# /group/source, silently bucketing every log under "unknown".
GROUP="$(get "/api/v1/logs/group/orchestrator?orchestrator=$ORCH&from=$FROM")"
[ "$(jq -r '.group_by // empty' <<<"$GROUP")" = "orchestrator" ] || fail "group_by wrong: $GROUP"
GROUP_COUNT="$(jq -r --arg o "$ORCH" '.groups[] | select(.group_key == $o) | .count' <<<"$GROUP")"
[ "$GROUP_COUNT" = "3" ] || fail "group '$ORCH' count=$GROUP_COUNT, want 3: $GROUP"
echo "    group $ORCH: $GROUP_COUNT entries"

step "GET /api/v1/logs/group/source — must be rejected (field no longer exists)"
SRC_STATUS="$(status_of "/api/v1/logs/group/source?from=$FROM")"
[ "$SRC_STATUS" = "400" ] || fail "group/source returned $SRC_STATUS, want 400 — binary predates the Orchestrator rename"
echo "    400 as expected"

step "GET /api/v1/stats?from=$FROM"
STATS="$(get "/api/v1/stats?orchestrator=$ORCH&session_id=$SESSION&from=$FROM")"
TOTAL="$(jq -r '.total_entries // 0' <<<"$STATS")"
BY_ORCH="$(jq -r --arg o "$ORCH" '.by_orchestrator[$o] // 0' <<<"$STATS")"
BY_ERROR="$(jq -r '.by_level.error // 0' <<<"$STATS")"
[ "$TOTAL" = "3" ]    || fail "stats total_entries=$TOTAL, want 3: $STATS"
[ "$BY_ORCH" = "3" ]  || fail "stats by_orchestrator[$ORCH]=$BY_ORCH, want 3: $STATS"
[ "$BY_ERROR" = "1" ] || fail "stats by_level.error=$BY_ERROR, want 1: $STATS"
echo "    total_entries: $TOTAL (by_level.error: $BY_ERROR)"

step "GET /api/v1/usage — token aggregation over the outbound entry"
USAGE="$(get "/api/v1/usage")"
USAGE_IN="$(jq -r --arg a "$AGENT" '.day[] | select(.agent == $a) | .input_tokens' <<<"$USAGE")"
USAGE_TOTAL="$(jq -r --arg a "$AGENT" '.day[] | select(.agent == $a) | .total_tokens' <<<"$USAGE")"
[ "$USAGE_IN" = "11" ]    || fail "usage input_tokens=$USAGE_IN, want 11: $USAGE"
[ "$USAGE_TOTAL" = "33" ] || fail "usage total_tokens=$USAGE_TOTAL, want 33 (11 in + 22 out): $USAGE"
echo "    $AGENT: ${USAGE_IN} in / ${USAGE_TOTAL} total"

step "restart — the id index must survive a reboot"
# This is the boot path that actually bites: NewFileStore replays every .jsonl
# line into the in-memory id index before Run() binds the port. A fresh process
# over a non-empty corpus must still resolve the id we wrote in the last one.
stop_server
start_server
wait_ready
REREAD="$(get "/api/v1/logs/$LOG_ID")"
[ "$(jq -r '.content.text // empty' <<<"$REREAD")" = "$MARKER" ] \
  || fail "after restart, GET /logs/$LOG_ID did not return the entry — index not rebuilt from disk: $REREAD"
echo "    id $LOG_ID still resolves after restart"

step "SUCCESS — logstack boots, ingests, queries, groups, aggregates, and reindexes"
