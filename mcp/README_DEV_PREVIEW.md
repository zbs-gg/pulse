# Pulse MCP Preview v0.4.2

Pulse MCP Preview v0.4.2 is a Claude Code-first partner preview.

It is meant for technical friends, AI-heavy users, partners, and
investor-adjacent reviewers who can run a local developer preview and give
feedback on the core promise:

```text
Pulse keeps the thread across AI chats.
```

This is not full production, not a consumer installer, not Pulse Cloud, and not
a ChatGPT or Claude Store connector.

## What v0.4.2 Proves

The preview should let a technical user complete one clean path:

1. Install/start local Pulse.
2. Connect Claude Code.
3. Run `pulse doctor`.
4. Save one structured memory.
5. Open a fresh Claude Code session.
6. Recover that memory without re-explaining.
7. Open the viewer and inspect what Pulse will tell Claude next.
8. Delete or wipe memory.

## Canonical Install Path

Preferred v0.4.2 path is agent-first:

1. Give your AI agent the prompt in
   [`../docs/INSTALL_WITH_AGENT.md`](../docs/INSTALL_WITH_AGENT.md).
2. The agent reads `pulse/AGENTS.md` and the security checklist.
3. The agent runs `pulse install-plan claude-code --json`.
4. The agent runs `pulse init claude-code --dry-run`.
5. The agent asks for confirmation.
6. After confirmation, the agent runs `pulse init claude-code --yes`,
   `pulse doctor`, `pulse demo`, and `pulse viewer`.

Manual preview command after `@zbs-gg/pulse` is published with the preview tag:

```bash
npx @zbs-gg/pulse@preview init claude-code
```

Verify the npm path before using it:

```bash
npm view @zbs-gg/pulse dist-tags
```

If npm returns 404, use the source-bundle fallback or packed tarball and state
that the public npm path is not ready yet.

This configures Claude Code MCP + hooks in the directory where it is run.

Source-bundle fallback:

```bash
cd pulse/pulse-app
./scripts/preview-install-claude-code.sh
```

This quick proof uses the extracted `pulse/pulse-app` directory as the Claude
Code project. After running it, start Claude Code from that same directory.

To connect Pulse to a real project, run the installer from the target project
directory instead:

```bash
cd /path/to/your/project
/path/to/pulse-mcp-preview-v0.4.2-source-20260608/pulse/pulse-app/scripts/preview-install-claude-code.sh
```

The installer configures Claude Code MCP + hooks in the directory you run it
from. If you run the installer in one directory and then open Claude Code in a
different project, that project will not have the preview hooks/config.

This script builds the local Go daemon, builds the local MCP package, starts
Pulse on `127.0.0.1`, configures Claude Code MCP + hooks, saves the first
install memory when the daemon is reachable, and prints the local viewer URL.

Future stable public shape:

```bash
npx @zbs-gg/pulse init claude-code
```

Do not market that future command as the broad consumer path until daemon
distribution is solved.

## Safe Product Promise

```text
Pulse MCP Preview helps Claude Code remember structured context across sessions.
It stores host-extracted memory capsules locally, shows what will be resumed
next, and does not require backend OpenAI, Anthropic, or Cohere keys by default.
```

Short version:

```text
Stop re-explaining your project to Claude Code.
Pulse keeps the thread.
```

## Do Not Claim

- Production ready.
- Claude never forgets.
- Works for everyone.
- One-click install for nontechnical users.
- Automatic all-chat migration.
- ChatGPT or Claude public app connector ready.
- Pulse Cloud ready.

## Package Boundary

`@zbs-gg/pulse` is the CLI installer/local preview wrapper for v0.4.2.
`@zbs-gg/pulse-mcp` is only the MCP server package. It is a thin stdio/HTTP
bridge to a running local Pulse daemon, not the whole product and not a
standalone memory database.

## Required Reading

- [INSTALL_WITH_AGENT.md](../docs/INSTALL_WITH_AGENT.md)
- [SECURITY_INSTALL_CHECKLIST.md](../docs/SECURITY_INSTALL_CHECKLIST.md)
- [60S_DEMO.md](docs/developer-preview/60S_DEMO.md)
- [INSTALL_DEV_SETUP.md](docs/developer-preview/INSTALL_DEV_SETUP.md)
- [PROOF_INDEX.md](docs/developer-preview/PROOF_INDEX.md)
- [UNINSTALL_AND_WIPE.md](docs/developer-preview/UNINSTALL_AND_WIPE.md)
- [KNOWN_LIMITATIONS.md](docs/developer-preview/KNOWN_LIMITATIONS.md)
- [SAFE_CLAIMS.md](docs/developer-preview/SAFE_CLAIMS.md)

## Current Release Gate

v0.4.2 is shareable only if these checks pass freshly:

```bash
cd pulse/pulse-app
env -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u COHERE_API_KEY go test ./...

cd pulse/pulse-app/cli
npm test
npm pack --dry-run --json

cd pulse/mcp
npm test
npm run build
npm pack --dry-run --json
```

The real Claude Code two-session proof is documented in
`docs/developer-preview/PROOF_INDEX.md`; treat it as preview evidence, not a
production claim.
