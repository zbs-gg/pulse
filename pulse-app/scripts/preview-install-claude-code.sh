#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CALLER_DIR="$(pwd)"
APP_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PULSE_DIR="$(cd "$APP_DIR/.." && pwd)"
MCP_DIR="$PULSE_DIR/mcp"
CLI_DIR="$APP_DIR/cli"

DATA_DIR="${PULSE_DATA_DIR:-$HOME/.pulse}"
BASE_URL="${PULSE_BASE_URL:-http://127.0.0.1:18789}"
ADDR="${BASE_URL#http://}"
BIN_DIR="$DATA_DIR/bin"
LOG_DIR="$DATA_DIR/logs"
DAEMON_BIN="$BIN_DIR/pulse-preview-daemon"
DAEMON_LOG="$LOG_DIR/pulse-preview-daemon.log"
PID_FILE="$DATA_DIR/pulse-preview-daemon.pid"
THREAD_ID="${PULSE_THREAD_ID:-$(basename "$CALLER_DIR")}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "[pulse] missing required command: $1" >&2
    exit 1
  fi
}

wait_for_daemon() {
  local secret_path="$DATA_DIR/secret.key"
  for _ in $(seq 1 80); do
    if [[ -f "$secret_path" ]]; then
      local key
      key="$(tr -d '\n' < "$secret_path")"
      if curl -fsS -H "X-Pulse-Key: $key" "$BASE_URL/memory/status" >/dev/null 2>&1; then
        return 0
      fi
    fi
    sleep 0.15
  done
  echo "[pulse] daemon did not become ready. Log: $DAEMON_LOG" >&2
  exit 1
}

pulse_ready() {
  local secret_path="$DATA_DIR/secret.key"
  if [[ ! -f "$secret_path" ]]; then
    return 1
  fi
  local key
  key="$(tr -d '\n' < "$secret_path")"
  curl -fsS -H "X-Pulse-Key: $key" "$BASE_URL/memory/status" >/dev/null 2>&1
}

echo "[pulse] Pulse MCP Preview v0.4.2"
echo "[pulse] Claude Code-first local preview"
echo

need go
need node
need npm
need curl

if ! command -v claude >/dev/null 2>&1; then
  echo "[pulse] Claude Code CLI was not found on PATH." >&2
  echo "[pulse] Install Claude Code first, then rerun this preview installer." >&2
  echo "[pulse] This script will not write a project .mcp.json fallback with a secret." >&2
  exit 1
fi

mkdir -p "$BIN_DIR" "$LOG_DIR"

echo "[pulse] Building local Pulse daemon..."
(cd "$APP_DIR" && go build -o "$DAEMON_BIN" ./cmd/pulse)

echo "[pulse] Building local Pulse MCP package..."
(cd "$MCP_DIR" && npm ci >/dev/null && npm run build >/dev/null)

if pulse_ready; then
  echo "[pulse] Existing Pulse daemon detected at $BASE_URL"
else
  echo "[pulse] Starting Pulse daemon at $BASE_URL"
  PULSE_MODE=local-auto \
  ANTHROPIC_API_KEY= \
  OPENAI_API_KEY= \
  COHERE_API_KEY= \
  nohup "$DAEMON_BIN" -addr "$ADDR" -data-dir "$DATA_DIR" > "$DAEMON_LOG" 2>&1 &
  echo "$!" > "$PID_FILE"
  wait_for_daemon
fi

export PULSE_BASE_URL="$BASE_URL"
export PULSE_DATA_DIR="$DATA_DIR"
export PULSE_MCP_ENTRYPOINT="$MCP_DIR/dist/index.js"

echo "[pulse] Connecting Claude Code..."
(cd "$CALLER_DIR" && node "$CLI_DIR/src/cli.js" init claude-code)

SECRET="$(tr -d '\n' < "$DATA_DIR/secret.key")"

echo
echo "[pulse] Preview ready."
echo "[pulse] Dashboard:"
echo "  $BASE_URL/viewer?key=$SECRET&thread_id=$THREAD_ID&first_run=1&host=claude-code"
echo
echo "[pulse] Try first memory in Claude Code:"
echo '  Remember this in Pulse: Atlas must not own the People Graph; Pulse owns portable continuity memory.'
echo
echo "[pulse] Then open a fresh Claude Code session and ask:"
echo '  What did we decide about Atlas and the People Graph?'
echo
echo "[pulse] Wipe later:"
echo '  pulse wipe --confirm "wipe pulse memory"'
