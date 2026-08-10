# Product

## Register

product

## Users

Pulse Personal is for people who work across Codex, Claude Code, and Cursor and
do not want to explain the same project decisions again in every new session.
Ordinary ChatGPT chat is not a supported Pulse connection yet.

## Product Purpose

Pulse stores small approved memories in a local SQLite database, restores the
relevant ones in later work, and lets the owner inspect, correct, or delete them
in Memory Home. Personal 0.8 adds momentary emotional observations and a safe
preview for combining old local stores without changing their source files.
The 0.8 work is not released yet.

## Invisible Memory Contract

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

The later invisible-memory dogfood still did not pass. A local epoch-17
candidate corrected the advertised write schema so an emotion must be a
separate item. Claude Code automatically received an old acceptance criterion
and stored a new project decision plus emotional moment in one `pulse_memory`
call without an error or retry. The live index contained both the new capsule
and its embedding immediately.

Codex then exposed the remaining product failure. Its prompt hook retrieved the
new capsule for one semantic wording, but two fresh Codex answers did not apply
the exact stored rule reliably: one reported that the rule was absent and one
replaced it with a broader interpretation. Cursor and the live project boundary
across one shared Personal process remain unaccepted. Under the bounded
acceptance rule, local 0.8 connections were disabled instead of adding another
mechanism.

Personal 0.8 is therefore neither published nor accepted for ordinary work.
The public package remains 0.7.2, Team was not enabled, and the unfinished code
stays on the development branch for a later, narrowly scoped correction.

## Brand Personality

Clear, exacting, calm. Pulse should feel like a trustworthy local operator console, not a marketing page or another memory app demanding attention.

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
