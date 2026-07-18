#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Pulse cloud setup requires $1 in the Codex universal image." >&2
    exit 1
  fi
}

require_command git
require_command go
require_command node
require_command npm

node_major="$(node -p 'process.versions.node.split(".")[0]')"
if [ "$node_major" -lt 20 ]; then
  echo "Pulse cloud setup requires Node 20 or newer; found $(node --version)." >&2
  exit 1
fi

npm --prefix mcp ci --no-audit --no-fund
npm --prefix pulse-app/cli ci --no-audit --no-fund

(
  cd pulse-app
  go mod download
  go build ./...
)

npm --prefix mcp run --silent build
node pulse-app/cli/src/cli.js --help >/dev/null

echo "Pulse Codex Cloud workspace ready. No Pulse user data or secrets were created."
