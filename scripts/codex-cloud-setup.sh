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
require_command codex

node_major="$(node -p 'process.versions.node.split(".")[0]')"
if [ "$node_major" -lt 20 ]; then
  echo "Pulse cloud setup requires Node 20 or newer; found $(node --version)." >&2
  exit 1
fi

# Keep Cloud tasks on the same reviewed Compound Engineering pipeline as the
# desktop task. Pin the marketplace commit instead of following a moving main
# branch; both commands are safe to repeat when a cached container resumes.
compound_engineering_ref="32fae6c546704b3befb7e5eba30fc6bed931fba9"
codex plugin marketplace add EveryInc/compound-engineering-plugin --ref "$compound_engineering_ref"
codex plugin add compound-engineering@compound-engineering-plugin
if ! codex plugin list | grep -Eq '^compound-engineering@compound-engineering-plugin[[:space:]]+installed, enabled[[:space:]]+3\.19\.0([[:space:]]|$)'; then
  echo "Pulse cloud setup could not activate Compound Engineering 3.19.0." >&2
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

echo "Pulse Codex Cloud workspace ready with Compound Engineering 3.19.0. No Pulse user data or secrets were created."
