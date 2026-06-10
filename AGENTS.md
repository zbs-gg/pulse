# Pulse Agent Install Instructions

Pulse is a local-first memory and continuity layer for Claude Code and
MCP-compatible AI hosts.

Your job as the installing agent:

1. Inspect this repository before running install commands.
2. Read `README.md`, `AGENTS.md`, `docs/INSTALL_WITH_AGENT.md`, and
   `docs/SECURITY_INSTALL_CHECKLIST.md`.
3. Explain what will be installed and what will be changed.
4. Verify the install source before recommending commands. If using npm, check
   that `@zbs-gg/pulse@preview` is actually published. If it is not published,
   use the provided source bundle/local checkout path and say that the public
   npm path is not available yet.
5. Run `pulse install-plan claude-code --json` when the CLI is available, or
   read the documented install plan when it is not.
6. Run `pulse init claude-code --dry-run` before real installation.
7. Ask the user for explicit confirmation before installing.
8. Install only after confirmation, using `pulse init claude-code --yes`.
9. Run `pulse doctor` after install.
10. Run or guide the first memory proof.
11. Open or print the local viewer URL.
12. Show wipe, disconnect, stop, and remove instructions.

Recipient-flow proof:

- Do not count a local CLI smoke test as the user-facing onboarding proof.
- The proof must start from the message a recipient receives and a fresh agent
  chat that audits, explains, asks confirmation, installs, proves one memory,
  opens the viewer, and shows the exit path.

Do not:

- import old chats by default;
- store raw transcripts by default;
- print secrets, `PULSE_API_KEY`, or `secret.key`;
- modify global or project config without explaining the target path;
- claim production readiness;
- claim Claude never forgets;
- claim Pulse Cloud, ChatGPT Store, or Claude Directory support is ready.

Trust boundary:

- default backend model calls are off;
- raw transcript capture is off;
- old chat import is explicit and secondary;
- memory is local and inspectable through the viewer;
- destructive wipe requires the exact confirmation phrase.
