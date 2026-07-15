# Garden Pulse — top-level developer Makefile
#
# Common entry points for build / test / verify after the pulse-app/
# restructure. Adapted from the pre-restructure Makefile that survives
# in .worktrees/main-integrate-pr25/ (paths prefixed with pulse-app/,
# Python targets dropped — main has no Python; mcp/ TS targets added).
#
# Usage:
#   make help        # list all targets
#   make build       # compile the Go server -> pulse-app/bin/pulse
#   make test        # run Go test suite (pulse-app/)
#   make verify      # ONE gate: Go, MCP, negative smoke, and CLI checks
#   make run         # start the server on 127.0.0.1:18789
#   make lint        # go vet + gofmt check
#   make clean       # remove build artifacts

GO            ?= go
NPM           ?= npm
APP_DIR       := pulse-app
MCP_DIR       := mcp
CLI_DIR       := $(APP_DIR)/cli
BIN_DIR       := $(APP_DIR)/bin
PULSE_BIN     := $(BIN_DIR)/pulse
PULSE_DATA    ?= $(HOME)/.pulse
PULSE_ADDR    ?= 127.0.0.1:18789
VERIFY_LOG    ?= $(HOME)/.claude/verify-log.jsonl

.DEFAULT_GOAL := help
.PHONY: help build test run run-server clean lint fmt mcp-test mcp-build cli-test verify team-remote-daemon-store-acceptance team-deploy-static-verify team-race-release release-verify

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Compile the Go server -> pulse-app/bin/pulse
	@mkdir -p $(BIN_DIR)
	cd $(APP_DIR) && $(GO) build -o bin/pulse ./cmd/pulse
	@echo "built $(PULSE_BIN)"

test: ## Run Go test suite (pulse-app/)
	cd $(APP_DIR) && $(GO) test ./...

run: ## Run the server in-process (go run ./cmd/pulse)
	cd $(APP_DIR) && $(GO) run ./cmd/pulse -addr $(PULSE_ADDR) -data-dir $(PULSE_DATA)

run-server: build ## Build then start pulse-app/bin/pulse
	$(PULSE_BIN) -addr $(PULSE_ADDR) -data-dir $(PULSE_DATA)

lint: ## go vet + gofmt -l (fail if any file needs gofmt)
	cd $(APP_DIR) && $(GO) vet ./...
	@unformatted=$$(cd $(APP_DIR) && gofmt -l cmd internal cli); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt would change these files (in $(APP_DIR)/):"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@echo "lint clean"

fmt: ## Apply gofmt to pulse-app Go files
	cd $(APP_DIR) && gofmt -w cmd internal cli

mcp-test: ## Run MCP server TS tests (mcp/)
	cd $(MCP_DIR) && $(NPM) test

mcp-build: ## Build MCP server (mcp/)
	cd $(MCP_DIR) && $(NPM) run build

cli-test: ## Run published CLI contract tests (pulse-app/cli/)
	cd $(CLI_DIR) && $(NPM) test

team-remote-daemon-store-acceptance: mcp-build ## Exercise Go/SQLite plus a synthetic Owner HTTP/OIDC/DPoP/CLI chain; external TLS/live IdP stays separate
	cd $(MCP_DIR) && $(NPM) run acceptance:team-remote-daemon-store

team-deploy-static-verify: ## Validate default-off Team deployment templates on any development host
	./deploy/team/verify-templates.mjs

team-race-release: ## Run the Team Go release suites under the race detector exactly once
	cd $(APP_DIR) && $(GO) test -race -count=1 -timeout 20m ./internal/store ./internal/server ./internal/teamread ./internal/teamjobs ./internal/teamauth ./cmd/pulse

release-verify: verify team-race-release team-deploy-static-verify ## Full release gate; live Linux systemd/Caddy validation remains separate

verify: ## ONE gate: Go + MCP + negative smoke + CLI; appends ~/.claude/verify-log.jsonl
	@verify_data=$$(mktemp -d "$${TMPDIR:-/tmp}/pulse-verify.XXXXXX") || exit 1; \
	trap 'rm -rf "$$verify_data"' EXIT HUP INT TERM; \
	case "$$verify_data" in \
	  "$(HOME)/.pulse"|"$(HOME)/.pulse/"*) echo "refusing to verify against the real ~/.pulse"; exit 1;; \
	esac; \
	export PULSE_DATA_DIR="$$verify_data"; \
	status=pass; \
	( cd $(APP_DIR) \
	  && $(GO) build ./... \
	  && $(GO) vet ./... \
	  && unformatted=$$(gofmt -l cmd internal cli) \
	  && { if [ -n "$$unformatted" ]; then \
	         echo "gofmt would change these files (in $(APP_DIR)/):"; \
	         echo "$$unformatted"; exit 1; \
	       fi; } \
	  && $(GO) test ./... ) \
	&& ( if [ -f $(MCP_DIR)/package.json ]; then \
	       cd $(MCP_DIR) \
	       && { [ -d node_modules ] || $(NPM) ci --silent; } \
	       && $(NPM) test --silent && $(NPM) run --silent build \
	       && $(NPM) run --silent acceptance:team-remote-daemon-store \
	       && $(NPM) run --silent smoke:standalone-negative \
	       && $(NPM) run --silent smoke:team-remote-negative; \
	     else \
	       echo "$(MCP_DIR)/package.json not found, skipping mcp checks"; \
	     fi ) \
	&& ( if [ -f $(CLI_DIR)/package.json ]; then \
	       cd $(CLI_DIR) \
	       && $(NPM) test --silent \
	       && $(NPM) run --silent test:codex-product \
	       && $(NPM) run --silent test:claude-product \
	       && $(NPM) run --silent test:codex-team-packaging-contract; \
	     else \
	       echo "$(CLI_DIR)/package.json not found, skipping CLI checks"; \
	     fi ) \
	&& ./deploy/team/verify-templates.mjs \
	|| status=fail; \
	ts=$$(date -u +%Y-%m-%dT%H:%M:%SZ); \
	ref=$$(git rev-parse HEAD 2>/dev/null || echo "$(CURDIR)"); \
	printf '{"ts":"%s","workspace":"%s","repo":"pulse","loop":"verify","kind":"make-verify","outcome":"%s","ref":"%s"}\n' \
		"$$ts" "$(CURDIR)" "$$status" "$$ref" >> $(VERIFY_LOG); \
	echo "verify: $$status (ledger: $(VERIFY_LOG))"; \
	test "$$status" = pass

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
	rm -f $(APP_DIR)/*.test $(APP_DIR)/*.out
	@echo "cleaned"
