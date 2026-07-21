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
It also installs and enables the native Codex Compound Engineering plugin at
the reviewed `3.19.0` revision, so new Cloud tasks have the same `ce-plan`,
`ce-work`, `ce-code-review`, and `lfg` skills as the desktop task. You do not
need to add a marketplace or install those skills manually in the Cloud UI.

The setup does not install Pulse, start a daemon, read `~/.pulse`, import chats,
or run production signing and publication. It needs no environment variables
or secrets. Codex Cloud gives the setup phase internet access and caches the
result; using the same command as the maintenance script keeps dependency and
plugin checks repeatable when the cached environment resumes.

After the first setup, start a **new Cloud task** so Codex discovers the newly
installed plugin skills. In that task, `ce-setup`, `ce-plan`, `ce-work`, and
`lfg` are available by name. Compound Engineering's optional browser and AST
helper binaries are not product prerequisites and are intentionally not bulk
installed here.

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

References: [Codex Cloud environments](https://developers.openai.com/codex/cloud/environments)
and [Codex plugins](https://learn.chatgpt.com/codex/plugins).
