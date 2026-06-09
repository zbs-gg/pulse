# Security Install Checklist

Use this checklist before installing Pulse MCP Preview v0.4.2 for a user.

## Required Checks

- [ ] No backend OpenAI, Anthropic, or Cohere key is required by default.
- [ ] Raw transcript capture is off by default.
- [ ] Old chat import is not run by default.
- [ ] The install source is real: npm preview package is published, or the user
      explicitly provided a source bundle, tarball, or local checkout.
- [ ] Local storage path is shown.
- [ ] Claude Code MCP config location is shown.
- [ ] Claude Code hooks location is shown.
- [ ] Local viewer URL is shown.
- [ ] Wipe command is shown:
      `pulse wipe --confirm "wipe pulse memory"`.
- [ ] Disconnect command is shown:
      `pulse disconnect claude-code`.
- [ ] Stop command is shown:
      `pulse stop`.
- [ ] No secrets are printed.
- [ ] Install requires explicit user confirmation.

## Expected Install Plan

```bash
pulse install-plan claude-code --json
```

The plan should say Pulse will install:

- local Pulse daemon;
- Pulse MCP server;
- Claude Code MCP config;
- Claude Code lifecycle hooks;
- local viewer;
- private first memory proof.

The plan should say Pulse will not:

- import old chats;
- store raw transcripts by default;
- call backend OpenAI/Anthropic/Cohere model APIs by default;
- print secrets;
- claim production readiness.

## Stop Conditions

Stop and explain if:

- a command asks for model API keys for the default path;
- npm returns 404 for `@zbs-gg/pulse@preview` and no source bundle/local
  checkout was provided;
- a command tries to import archives before the first proof;
- a command prints a real secret;
- a command writes project `.mcp.json` without explicit user approval;
- the user has not confirmed installation.
