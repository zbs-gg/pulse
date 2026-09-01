# Pulse Personal

## Current product

Pulse Personal is memory for AI tools. It gives Codex and Claude Code the
knowledge they need at the moment they need it, stays silent when nothing
relevant is found, and lets the owner inspect, correct, or delete memories in
Memory Home.

The stable public release is 0.7.2 for Apple Silicon Macs. Personal 0.8.2 is
published under the npm `preview` tag for the same target; it is not production
ready. It has no Team server, cloud synchronization, or shared-memory command.
Ordinary ChatGPT chat is not connected to Pulse. Emotional moments and the
reviewed local-store migration are part of the 0.8 preview, not 0.7.2.

The source tree is now the unpublished 0.8.3 candidate. It adds OpenCode
1.18.x support on Apple Silicon macOS and has been contract-tested against the
installed 1.18.15. Stable 0.7.2 and public preview 0.8.2 do not contain this
adapter. No npm or GitHub publication is authorized by this candidate work.

The 0.8.2 preview expands historical import, retrieves up to four short,
distinct memories within the existing context budget, preserves the embedder
protocol after caller cancellation, and makes Doctor prove a real semantic
query. Its publication flow installs the exact archive on a clean Apple Silicon
runner and blocks release unless semantic retrieval answers.

## Users

Pulse Personal is for people who work across coding AI programs and do not want
to explain the same project decisions, constraints, and open questions again in
every new session. The published line supports Codex, Claude Code, and Cursor;
the 0.8.3 candidate also supports OpenCode under the narrower target above.
Ordinary ChatGPT chat is not a supported Pulse connection yet.

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

In OpenCode, one inert global loader is registered in the user's config and
activates only inside a project with a signed Pulse binding. `chat.message`
runs local recall and `experimental.chat.system.transform` supplies it before
the first response. OpenCode sees exactly one administrative surface:
`pulse_memory`; daemon failure, memory errors, cancel, and idle remain fail-open.

OpenCode fun facts are opt-in. Once per session, at most six approved local
candidates may be offered to the configured `small_model`, or to a provably
cheaper active text model from the same provider. The service session receives
no user request or other memory, has tools disabled and a 32-token output cap,
and may return only one candidate ID or `none`. Its content-free receipt records
model, latency, optional usage, and candidate digest. Failure uses the local
deterministic choice and does not break the main response.

## Current Evidence

The owner-machine migration passed on 2026-08-09. The existing local-store merge
path produced and atomically activated a reviewed Personal database. The old
Pulse database, Claude Mem database, migration sources, and recovery files are
preserved in a verified encrypted external archive.

On 2026-08-12, fresh Codex and Claude Code checks demonstrated automatic recall,
one-call writing, cross-host memory, project isolation, silence on an unrelated
question, and fail-open host work. The signed epoch 34 daemon did not contain
the later cancellation repair, however. On 2026-08-16 it still reported full
BGE-M3 retrieval while ordinary queries timed out against an unloaded model. A
five-second semantic probe loaded the intact model; the same Hermes query then
returned relevant candidates in 496 ms. The data and model were healthy, but
the readiness claim was not.

The one-day owner-machine dogfood is therefore not complete. Exact public 0.8.2
bytes must still be activated on that Mac and pass a cold semantic query, a
warm query, and normal recall and writing in Codex and Claude Code before
daily-use status can be restored.
Cursor live recall remains pending.

A frozen 50-case baseline on a copy of the active Personal vault did not pass
the combined practical bar. Published `0.8.0` retrieved the expected memory in
34 of 40 cases, stayed silent in 7 of 10 unrelated controls, and returned two
internal query errors. Warm p95 and context size passed. The live answer score
was not run after retrieval failed, because replacing a preselected missed case
would be post-selection. Product work should fix false-positive context and
query reliability before expanding the benchmark or importing more archives.

The current benchmark uses an isolated installation of the exact signed
runtime and the real `pulse_memory` plus prompt-time context path. The 0.8.1
fix recalled 12 of 15 useful memories and stayed silent on all 5 unrelated
controls, compared with 13 of 15 and 2 of 5 for published 0.8.0. Full raw-history
LoCoMo evidence retrieval reached 561 of 1,535 eligible questions. A bounded
30-question LongMemEval-S run recalled an official answer turn in every case,
but does not measure official answer accuracy. The existing EmoBench is not a
valid product score: 30 of its 35 questions depend on hidden state, biometrics,
or Atlas-like graph traversal that Pulse does not receive. It must be rebuilt
before it can be used for product comparison.

The 0.8.2 preview candidate completed the full 1,535-question LoCoMo path
with GPT-5.4 low used for extraction, answering, and judging. It scored 1,230
correct answers (80.13%), compared with 1,231 (80.20%) for equal-model Mem0 and
959 (62.48%) for the previous Pulse baseline. Median context was 120 estimated
tokens, maximum context 198, and warm retrieval p95 70.8 ms. The candidate
improved Pulse substantially but did not meet its predeclared 82% and
beat-Mem0 acceptance target. This is development evidence rather than a
production-readiness claim. Details are in
`docs/evals/2026-08-16-full-locomo-candidate.md`.

On 2026-08-11, a separate Claude Chat experiment automatically recalled the
exact `LUNA-724-PURPLE` marker from an isolated hosted store of 21 memories.
Claude also called Pulse once for an unrelated question without mixing memory
into the answer. This required an account-level Claude instruction and the
connector in Always available mode; MCP server instructions alone were ignored.
The resulting visible tool call and additional tool round trip happen on every
ordinary message. The hosted store is not synchronized with the local Personal
0.8 vault, so this is remote-connector evidence rather than Personal support.

Personal 0.8.2 is the public preview. The stable `latest` package remains
0.7.2, Team was not enabled, and daily-use acceptance in Codex and Claude Code
is paused until exact public 0.8.2 bytes pass the owner-machine recall and write
cycle.
The 0.8.3 OpenCode adapter remains an unpublished candidate until new signed
artifacts, a new descriptor, and physical owner-machine acceptance exist.

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

Status: Personal 0.7.2 remains the stable public release; Personal 0.8.2 is the
public npm preview, whose publication gate includes a real semantic query.
Epoch 34 from 0.8.0 remains installed locally, so the Codex and Claude Code day
is not accepted as complete. Exact public 0.8.2 bytes must pass cold and warm
semantic search plus real host recall and writing on the owner Mac. It is not
production ready.
OpenCode support is present only in candidate 0.8.3 and still awaits the
separately approved physical OpenCode 1.18.15 qualification.
Separate Claude Chat remote
recall passed with an account-level instruction and a visible per-message tool
call; it is not part of the Personal installation.
