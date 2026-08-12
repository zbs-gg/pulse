# Pulse Personal — repository instructions

## Product boundary

- This public repository and npm package contain Pulse Personal only. Memory
  stays in a local project-bound vault; there is no Team server, shared memory,
  cloud synchronization, or `pulse team` command here.
- The supported AI programs are Codex, Claude Code, and Cursor. Ordinary
  ChatGPT chat is not a supported Pulse connection.
- Personal memory is optional. A broken daemon, activation, or memory request
  must not block terminal or file tools, Stop/Cancel, goal control, or normal
  session completion, and must not create an automatic continuation.
- Raw conversations, secrets, and local computer paths are not memory. Keep
  raw transcript capture, old-chat import, and backend model calls off by
  default.
- Pulse Team is a separate private pilot. Do not add Team code, cloud settings,
  device keys, shared database migrations, or Team claims to this repository.

## Installation and repository work

- The stable public release is Personal 0.7.2 for Apple Silicon Macs. Personal
  0.8.0 is a public npm preview for the same target; it is not the stable
  default or a production-ready release. Do not present Intel Mac, Windows, or
  Linux as publicly supported from fixture evidence alone.
- Before installation, show the exact files and settings that will change and
  ask the person to confirm. `--yes` does not bypass macOS protected actions.
- Do not change global AI-program settings, system trust, or an existing Pulse
  vault during repository work unless the person explicitly asked for that
  installation or repair.
- Never use the developer's real home directory, Personal vault, keys, or AI
  settings in automated tests. Use a temporary home and data directory.
- Start each task from clean `main` in a branch named for the result, such as
  `feat/<clear-result>`. Do not reuse broad branches such as `clean-ui`.
- Preserve unrelated work. Run the relevant focused tests while editing and
  `make verify` before merging code.

## Product documentation

After changing capabilities, installation, user flow, limitations, privacy,
readiness, or positioning, review and update the related product documents in
the same work. If no documentation change is needed, say so explicitly.

The required Pulse Personal documents are:

- `README.md`
- `llms.txt`
- `pulse-app/PRODUCT.md`
- `docs/INSTALL_WITH_AGENT.md`
- `docs/PERSONAL_PULSE_ONBOARDING.md`
- `docs/SECURITY_INSTALL_CHECKLIST.md`
