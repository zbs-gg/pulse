# Security Install Checklist

Use this checklist before installing stable Pulse Personal 0.7.2 or the
explicit 0.8.2 preview for a user.

## Required Checks

- [ ] No backend OpenAI, Anthropic, or Cohere key is required by default.
- [ ] Raw transcript capture is off by default.
- [ ] Emotional marks contain a short event description and metadata, not the
      exact user quote or full conversation.
- [ ] Emotional marks remain in Personal SQLite and cannot be published to
      Pulse Team.
- [ ] Old chat import is not run by default.
- [ ] The install source is real: npm reports `@zbs-gg/pulse@0.7.2` for stable
      or `@zbs-gg/pulse@0.8.2` for the selected preview, or the user explicitly
      provided an exact source bundle, tarball, or local checkout.
- [ ] The exact OS, architecture, and harness are green in
      `docs/release/NATIVE_SUPPORT_LEDGER.md`; PR fixture evidence is not
      presented as public support.
- [ ] Local storage path is shown.
- [ ] The selected AI program's connection files are shown.
- [ ] Local viewer URL is shown.
- [ ] Wipe command is shown:
      `pulse wipe --confirm "wipe pulse memory"`.
- [ ] Disconnect command is shown:
      `pulse disconnect claude-code`.
- [ ] Stop command is shown:
      `pulse stop`.
- [ ] No secrets are printed.
- [ ] Install requires explicit user confirmation.
- [ ] The host advertises only `pulse_memory`; legacy write names are hidden.
- [ ] Prompt recall is temporary, capped at four memories / about 600 tokens,
      and does not persist the user's question.
- [ ] No session-start memory package, Stop interception, automatic
      continuation, or second model pass is installed.

## Expected Install Plan

```bash
pulse init codex --dry-run
```

The plan should say Pulse will install:

- local Pulse daemon;
- Pulse MCP server;
- connections for every selected compatible AI program;
- local viewer;
- private first memory proof.

The plan should say Pulse will not:

- import old chats;
- store raw transcripts by default;
- publish emotional marks or their causes to another person;
- call backend OpenAI/Anthropic/Cohere model APIs by default;
- print secrets;
- claim production readiness.

## Stop Conditions

Stop and explain if:

- a command asks for model API keys for the default path;
- npm returns 404 for the exact selected Pulse version and no source bundle/local
  checkout was provided;
- a command tries to import archives before the first proof;
- a command offers to send an emotional mark to Team or another user;
- a command prints a real secret;
- a command writes project `.mcp.json` without explicit user approval;
- the user has not confirmed installation.
