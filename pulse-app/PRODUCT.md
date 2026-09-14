# Pulse Personal

The 0.8.3 installer downloads signed runtime and model assets from the public
GitHub release in `zbs-gg/pulse`. GitHub may redirect downloads to
`release-assets.githubusercontent.com`; signatures, exact sizes and SHA-256
digests are still checked before activation. No paid release storage service
is required. Personal memory remains on the local machine.


## Current product

Pulse Personal is memory for AI tools. It gives Codex and Claude Code the
knowledge they need at the moment they need it, stays silent when nothing
relevant is found, and lets the owner inspect, correct, or delete memories in
Memory Home.

Stable remains 0.7.2 for Apple Silicon Macs. The opt-in 0.8.3 preview adds
complete-moment writing and OpenCode 1.18.x to Codex, Claude Code and Cursor.
The BB adapter uses the same local engine. There is no Team server, cloud
synchronization or shared-memory command; ordinary ChatGPT chat is not a
Personal connection.

All meaningful requested parts are accepted together and individually
receipted. Multiple feelings retain their names, causes and user-stated versus
inferred provenance. A small automatic context budget never limits what can be
stored or read explicitly as a complete moment.

The release flow binds one signed artifact set, npm archive, documentation and
GitHub prerelease. It installs that archive on a clean Apple Silicon runner and
requires a real semantic query. One-day owner use and production readiness are
separate acceptance boundaries.

## Users

Pulse Personal is for people who work across coding AI programs and do not want
to explain the same project decisions, constraints, and open questions again in
every new session. The published line supports Codex, Claude Code, and Cursor;
the 0.8.3 preview also supports OpenCode under the narrower target above.
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

The model sees one write tool, `pulse_memory`, and saves the complete meaningful moment during the ordinary working turn.
There is no three-item quota. Multiple emotions, human feeling names, causes and
explicit versus inferred provenance remain distinct. There is no second model pass,
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

The one-day owner-machine dogfood is therefore not complete. The exact selected preview
bytes must be activated on that Mac and pass a cold semantic query, a
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

The 0.8.3 preview requires exact-archive host checks after publication. Earlier
measurements above describe their dated builds, not acceptance of this release.

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

Status: stable remains 0.7.2; 0.8.3 is an opt-in preview. Installation and
semantic retrieval checks are required for the exact published version.
One-day acceptance and production readiness are not implied by passing tests.

## Complete moments in 0.8.3

Explicit memory requests preserve all meaningful parts and coexisting feelings.
Pulse validates the full structured set before accepting it locally, then
materializes individual parts with durable receipts. A `stored` result covers
all submitted parts; `pending` or `partial` does not. Canonical storage and
explicit moment reading do not wait for the background search index. Each part
keeps its own pending index receipt until indexing actually finishes. A lost response permits one
bounded replay of the identical operation. A validation refusal before admission
permits correcting the input. Stop does not create an automatic continuation.

A moment reference lets the agent read all currently eligible linked parts with
pagination, independently of the short automatic context budget. Deleted items
are excluded; current corrections and personal/project boundaries still apply.
The write transport envelope is 16 MiB, not a limit on the number of feelings.
Oversized input is explicitly refused before admission, never truncated.
Structured summaries longer than one storage row are split without dropping text.
Historical feelings remain remembered even after their current-state influence
fades; inferred emotions are identified as hypotheses about that moment.

Native Codex, Claude Code, Cursor and OpenCode adapters and the BB adapter share
this behavior. Raw transcripts, secrets, old-chat import and backend model calls
remain off by default. Build/tests are not an installed-runtime or public-release
claim; publication and owner-machine checks must use the exact signed archive.
