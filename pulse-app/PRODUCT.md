# Pulse Personal

## Current product

Pulse Personal is memory for AI tools. It gives Codex and Claude Code the
knowledge they need at the moment they need it, stays silent when nothing
relevant is found, and lets the owner inspect, correct, or delete memories in
Memory Home.

The stable public release is 0.7.2 for Apple Silicon Macs. Personal 0.8.0 is
published under the npm `preview` tag for the same target; it is not production
ready. It has no Team server, cloud synchronization, or shared-memory command.
Ordinary ChatGPT chat is not connected to Pulse. Emotional moments and the
reviewed local-store migration are part of the 0.8 preview, not 0.7.2.

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

## 0.8 preview contract

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
path produced and atomically activated a reviewed Personal database. The old
Pulse database, Claude Mem database, migration sources, and recovery files are
preserved in a verified encrypted external archive.

On 2026-08-12, installed local epoch 34 passed fresh Codex and Claude Code
acceptance. Both hosts automatically recalled old memory without its exact
wording, wrote durable changes with one `pulse_memory` call, and recalled each
other's new records. A deliberately cancelled search returned control
immediately, while the following question still used full BGE-M3 semantic
retrieval. Personal memory followed the owner into another project; the Pulse
Personal project decision stayed inside its repository. An unrelated question
received no memory. With Pulse deliberately unavailable, terminal and file
work, cancellation, and normal session completion remained usable in both
hosts.

The owner-machine one-day dogfood is now running with Pulse enabled in Codex and
Claude Code. One broad, ambiguous Claude question surfaced an unrelated old
memory before a more specific semantic question found the intended decision.
That ambiguity remains under observation during dogfood. Cursor live recall
remains pending, so this is evidence for two local hosts rather than full
three-host acceptance.

On 2026-08-11, a separate Claude Chat experiment automatically recalled the
exact `LUNA-724-PURPLE` marker from an isolated hosted store of 21 memories.
Claude also called Pulse once for an unrelated question without mixing memory
into the answer. This required an account-level Claude instruction and the
connector in Always available mode; MCP server instructions alone were ignored.
The resulting visible tool call and additional tool round trip happen on every
ordinary message. The hosted store is not synchronized with the local Personal
0.8 vault, so this is remote-connector evidence rather than Personal support.

Personal 0.8.0 is now a public preview but is not production ready. The stable
`latest` package remains 0.7.2, Team was not enabled, and the bounded dogfood
continues in Codex and Claude Code.

## Interface

Memory Home should feel like a trustworthy local operator console: readable,
keyboard-accessible, responsive, and clear about what is stored and why. Avoid
decorative AI-dashboard styling, hidden import state, and transcript dumps.

The 0.8 preview operator surface reports each supported host's last recall
and write receipt without retaining the question or memory text. It also shows
protected storage and safe generated cleanup. The CLI keeps the same boundary:
`pulse storage clean` preserves the active release, one rollback, the vault,
keys, source data, and unknown files.

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

Status: Personal 0.7.2 remains the stable public release; Personal 0.8.0 is a
public npm preview and epoch 34 is installed locally for a one-day Codex and
Claude Code dogfood that started on 2026-08-12. It is not production ready.
Separate Claude Chat remote
recall passed with an account-level instruction and a visible per-message tool
call; it is not part of the Personal installation.
