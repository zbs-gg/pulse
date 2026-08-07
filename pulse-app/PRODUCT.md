# Pulse Personal

## Current product

Pulse Personal keeps approved working memory for Codex, Claude Code, and Cursor
in a local project-bound vault. It restores relevant memories in later work and
lets the owner inspect, correct, or delete them in Memory Home.

The current public release is 0.7.2 for Apple Silicon Macs. The published npm
tags `latest` and `preview` both point to this version. It has no Team server,
cloud synchronization, or shared-memory command. Ordinary ChatGPT chat is not
connected to Pulse. Emotional memory and local-store migration are unfinished
Personal 0.8 work in a separate draft and are not part of 0.7.2.

## Users

Pulse Personal is for people who work across coding AI programs and do not want
to explain the same project decisions, constraints, and open questions again in
every new session.

## Product principles

- Keep memory local, inspectable, correctable, and removable.
- Show what installation changes before asking for confirmation.
- Store compact structured memories, not raw conversations.
- Reject secrets and local paths before writing them.
- Keep optional memory fail-open: its failure must not block the AI program or
  create a new turn by itself.
- Treat Team as a separate private product boundary.
- Do not claim support for a program or operating system without a real install.

## Interface

Memory Home should feel like a trustworthy local operator console: readable,
keyboard-accessible, responsive, and clear about what is stored and why. Avoid
decorative AI-dashboard styling, hidden import state, and transcript dumps.

Status: developer preview, not production ready.
