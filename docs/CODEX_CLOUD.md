# Continue Pulse in Codex Cloud

This repository is ready to use in a Codex Cloud environment without copying
local Pulse data, signing keys, API keys, or a developer `~/.codex/config.toml`.

## One-time environment setup

1. Open Codex settings and create a Cloud Environment for the private
   `zbs-gg/pulse` GitHub repository.
2. Use the default universal image and pin Node 20 or newer.
3. Set the setup script to:

   ```bash
   ./scripts/codex-cloud-setup.sh
   ```

4. Use the same command as the maintenance script so a cached environment
   refreshes dependencies after checking out another branch.
5. Do not add Pulse runtime secrets. Source, fixture, and unit work needs none.

The setup installs the two locked npm dependency trees, downloads locked Go
modules, builds the Go packages and MCP package, and checks the CLI entrypoint.
It does not install Pulse, start a daemon, read `~/.pulse`, import chats, or run
production signing and publication.

## Start on this branch

Select `codex/pulse-memory-architecture-reset` in the Codex Cloud UI, or use the
environment identifier shown by Codex settings:

```bash
codex cloud exec \
  --env <pulse-environment-id> \
  --branch codex/pulse-memory-architecture-reset \
  "Continue the LFG plan in docs/plans/2026-07-17-001-feat-host-neutral-one-command-install-plan.md from the first incomplete unit. Read AGENTS.md first, inspect the branch, and preserve the Personal-versus-Team boundary."
```

Cloud source and fixture checks are useful development evidence. They do not
replace the native macOS, Windows, and Linux product gates required by the plan.
