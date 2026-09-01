# Pulse Personal — repository instructions

## Product boundary

- This public repository and npm package contain Pulse Personal only. Memory
  stays in a local project-bound vault; there is no Team server, shared memory,
  cloud synchronization, or `pulse team` command here.
- The published AI programs are Codex, Claude Code, and Cursor. The unpublished
  0.8.3 candidate also supports OpenCode 1.18.x on Apple Silicon macOS.
  Ordinary ChatGPT chat is not a supported Pulse connection.
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
  0.8.2 is a public npm preview for the same target; it is not the stable
  default or a production-ready release. Personal 0.8.3 is an unpublished
  candidate and must not reuse the 0.8.2 publication descriptor or artifacts.
  Do not present Intel Mac, Windows, or Linux as publicly supported from fixture
  evidence alone.
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

## Preview release completion

- A preview release is complete only when one reviewed publication descriptor
  binds the package version, signed epoch, exact npm archive, product documents,
  changelog, and GitHub prerelease notes. Validate it with
  `npm run verify:preview-publication` from `pulse-app/cli`.
- It is valid for `main` to be ahead of the published preview. In that state
  `package.json` names the unreleased candidate, the descriptor continues to
  record the last public preview, and `release-verify` must fail until new
  signed artifacts and a new descriptor exist. Never reuse a published version.
- Use the existing `.github/workflows/publish-npm.yml` for both npm preview and
  the matching GitHub prerelease. Do not publish npm and GitHub through separate
  ad-hoc commands for a new version.
- After public bytes exist, install that exact version outside the source
  checkout and run real `pulse doctor codex` and `pulse doctor claude-code`
  plus one semantic recall in each host. A hosted clean-Mac install does not
  replace the owner-machine host check.

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
