# Pulse Personal

## Current product

Pulse Personal keeps approved working memory for Codex, Claude Code, and Cursor
in a local project-bound vault. It restores relevant memories in later work and
lets the owner inspect, correct, or delete them in Memory Home.

The public release is 0.7.2 for Apple Silicon Macs. This branch is the
unfinished Personal 0.8 draft. It adds momentary emotional observations and a
safe preview for combining old local stores without changing their source
files. Personal 0.8 is not published and is not ready to merge.

There is no Team server, cloud synchronization, or shared-memory command in
Personal. Ordinary ChatGPT chat is not connected to Pulse.

## Users

Pulse Personal is for people who work across Codex, Claude Code, and Cursor and
do not want to explain the same project decisions, constraints, and open
questions again in every new session.

## Product principles

- Keep memory local, inspectable, correctable, and removable.
- Show what installation, migration, or deletion changes before confirmation.
- Store compact structured memories, not raw conversations.
- Reject secrets and local paths before writing them.
- Keep optional memory fail-open: its failure must not block the AI program or
  create a new turn by itself.
- Show emotional observations as editable objects and distinguish what the
  person said from what Pulse inferred.
- Never turn repeated emotions into a personality trait without confirmation.
- Treat Team as a separate private product boundary.
- Do not claim support for a program or operating system without a real install.

## Interface

Memory Home should feel like a trustworthy local operator console: readable,
keyboard-accessible, responsive, and clear about what is stored and why. Avoid
decorative AI-dashboard styling, hidden import state, and transcript dumps.

Status: unfinished Personal 0.8 draft, not production ready.
