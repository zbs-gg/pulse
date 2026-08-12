# Pulse 0.8 real Personal memory baseline

Date: 2026-08-12

## The first large Personal baseline did not pass

The exact published `@zbs-gg/pulse@0.8.0` archive retrieved the expected memory
in 34 of 40 private cases. The practical minimum was 32, so useful recall passed.
It stayed silent in 7 of 10 unrelated controls instead of the required 9, and
two queries returned an internal search error. The combined practical bar was
therefore not met.

The live answer phase was not run. One of the ten cases selected before the
retrieval run missed, and replacing it after seeing the result would make the
answer score post-selected. Model application remains unevaluated for this
baseline.

## The run used the published product on a large safe copy

The runner downloaded version `0.8.0` from npm, verified the npm integrity and
the installed signed release identities, then used the shipped prompt
compositor with the installed daemon and BGE-M3 embedder. SQLite online backup
created a separate snapshot containing 76,795 events, 373 active capsules,
5,989 emotional marks, and 76,795 embeddings. The active Personal store was not
queried directly or changed.

The 50 cases were frozen before the run: 25 were labelled Codex, 25 Claude Code;
40 expected one real Personal memory and 10 expected silence. Gold answers were
stored only in an owner-private file outside Git and were never sent to Pulse.
The exact query strings remained absent from the copied vault before and after
the run.

## This is real-corpus retrieval, not yet a raw-chat replay

Each positive case uses an active Personal capsule as its gold source and a new
semantic question written without copying the source wording. This tests the
published product against the real migrated archive rather than a 16-item toy
store. It does not yet prove that a fact can be reconstructed directly from an
earlier raw Codex or Claude Code turn: many migrated capsules have no reliable
source-message pointer.

That limitation is recorded in the aggregate as
`raw_history_temporal_replay: false`. The raw histories remain useful for a
later provenance-aware temporal dataset, but they must not be presented as the
source of this score.

## Noise and reliability are now the next product defects

Two unrelated controls received archive events and one received an old migrated
Claude capsule. The migrated Claude material is unusually long: 298 of 300
active `claude-mem` summaries exceed the 400-character delivery limit, 264
exceed 1,000 characters, and their average length is 1,132 characters. This is
a concrete data-quality risk because delivery clips those records before the
model sees them.

Two context queries returned HTTP 500. One was a positive decision case; the
other was the only project-isolation control. The latter is not evidence of a
project leak, but it makes project isolation inconclusive in this run.

## Speed and context size passed comfortably

Cold retrieval took 463 ms. Warm p50 was 368 ms and warm p95 was 547 ms, below
the one-second limit. The largest injected context was 736 bytes, approximately
184 tokens, below the 600-token limit. Query persistence checks passed.

The next change should address only the three false-positive controls and the
two internal query errors, then rerun the same frozen private cases once. The
runner, thresholds, and gold set should not be rewritten around this result.

Aggregate result:
[`results/2026-08-12-real-personal-memory-0.8.0.json`](./results/2026-08-12-real-personal-memory-0.8.0.json)
