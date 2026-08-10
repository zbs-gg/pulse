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
every new session. The current supported programs are Codex, Claude Code, and
Cursor. Ordinary ChatGPT chat is not a supported Pulse connection yet.

## Product principles

- Keep memory local, inspectable, correctable, and removable.
- Show what installation changes before asking for confirmation.
- Store compact structured memories, not raw conversations.
- Reject secrets and local paths before writing them.
- Keep optional memory fail-open: its failure must not block the AI program or
  create a new turn by itself.
- Treat Team as a separate private product boundary.
- Do not claim support for a program or operating system without a real install.

## Unfinished 0.8 contract

Pulse stays out of the normal workflow. On each user question it performs one
temporary local relevance search and offers at most four memories within about
600 tokens; weak matches add nothing. It does not save the question or preload
a general context package at session start.

The model sees one write tool, `pulse_memory`, and may save at most three short
durable items during the ordinary working turn. There is no second model pass,
Stop interception, automatic continuation, or Pulse check before unrelated
tools. Stable operating rules belong in host instruction files, not in Pulse.

Personal facts can follow the owner between projects. Project decisions remain
inside their project. Emotional observations describe a single moment, mark
inference as inference, and decay under the existing influence rules.

## Current Evidence

The owner-machine migration passed on 2026-08-09. The existing local-store merge
path produced and atomically activated a reviewed Personal database while the
old Pulse database, Claude Mem database, migration sources, and recovery copy
remained preserved.

A local epoch-22 candidate corrected the advertised write schema so an emotion
must be a separate item and fixed the bounded semantic selection path. On
2026-08-10, fresh Codex and Claude Code turns automatically applied the exact
stored rule that an emotional moment is a separate `pulse_memory` item without
the question repeating the stored wording. The earlier Claude write proof also
remains valid: one ordinary call stored a project decision and a separate
emotional moment without an error or retry, and the live index received both
immediately.

The owner-machine one-day dogfood is now running with Pulse enabled in Codex and
Claude Code. Cursor live recall and the cross-project boundary remain pending,
so this is evidence for two local hosts rather than full three-host acceptance.

Personal 0.8 is still neither published nor production ready. The public
package remains 0.7.2, Team was not enabled, and the unfinished code stays on
the development branch while the bounded dogfood continues.

## Interface

Memory Home should feel like a trustworthy local operator console: readable,
keyboard-accessible, responsive, and clear about what is stored and why. Avoid
decorative AI-dashboard styling, hidden import state, and transcript dumps.

## Anti-References

Do not look like parchment, fake editorial notebooks, generic AI purple glass, beige dashboard cards, decorative grid paper, or over-designed AI surfaces. Avoid hiding import state behind terminal logs or dumping raw transcripts.

## Design Principles

- Make consent and trust visible before installation, migration, or deletion.
- Show memories and emotional observations as inspectable, editable objects.
- Distinguish what the person said from what Pulse inferred.
- Never turn repeated emotions into a personality trait without confirmation.
- Keep Personal memory local; Team is a separate private pilot.
- Prefer task clarity and dense readable product surfaces over decorative personality.
- Never imply evaluated paper claims from product migration UX.

## Accessibility

Default to accessible product UI: readable contrast, keyboard-focusable controls, reduced-motion-safe interactions, responsive layout, and no reliance on color alone for status.

Status: Personal 0.7.2 is public; Personal 0.8 is installed locally for a
one-day Codex and Claude Code dogfood, remains a developer preview, and is not
production ready.
