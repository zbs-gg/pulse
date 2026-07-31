#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CALLER_DIR="$(pwd)"
APP_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PULSE_DIR="$(cd "$APP_DIR/.." && pwd)"
MCP_DIR="$PULSE_DIR/mcp"
CLI_DIR="$APP_DIR/cli"
DATA_DIR="${PULSE_DATA_DIR:-$HOME/.pulse}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "[pulse] missing required command: $1" >&2
    exit 1
  fi
}

need node
need npm
need go
need claude

echo "[pulse] Building the local product daemon and pinned MCP runtime..."
(cd "$MCP_DIR" && npm ci --silent && npm run --silent build)
(cd "$CLI_DIR" && npm ci --silent && npm run --silent prepack)

echo "[pulse] Installing Claude Code on the same bound Personal/Desk vault as Codex..."
(
  cd "$CALLER_DIR"
  PULSE_DATA_DIR="$DATA_DIR" \
    node "$CLI_DIR/src/cli.js" connect claude-code
)

echo
echo "[pulse] Start one real Claude Code prompt in this repository, then verify with:"
echo "  PULSE_DATA_DIR='$DATA_DIR' node '$CLI_DIR/src/cli.js' doctor claude-code"
