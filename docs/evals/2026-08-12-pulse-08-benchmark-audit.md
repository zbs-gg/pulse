# Pulse 0.8 benchmark audit

Date: 2026-08-12

## The existing scores do not measure the published Pulse 0.8 product

The old own-data run used the former MCP recall surface. Its saved Pulse result
contains 12 memories and 15 queries, while the current task file contains 16 of
each. Query latency was not recorded. Those numbers remain historical evidence
for that exact run, but they cannot be used as a Pulse 0.8 result.

The current Empathic Memory Bench calls a Python `PulseV3Adapter`, not the
installed npm package or its prompt-time recall path. The adapter receives
`user_state`, biometric snapshots, `user_flag`, emotion tags, and predecessor
links directly from the benchmark. It also augments queries with hand-written
emotion hints and explicitly expands causal chains. Ordinary Pulse 0.8 recall
does not receive these oracle fields from Codex or Claude Code.

The LongMemEval and LoCoMo scripts likewise implement their own cosine, BM25,
RRF, optional model reranking, and answer generation in Python. Their published
scores describe those experimental pipelines, not the released product.

## The valuable benchmark data should be kept

The own-data set contains useful correction, preference, negation, stale-fact,
and distractor cases. The Empathic Memory Bench contains valuable emotional
moments, difficult distractors, and human-written explanations of what a useful
memory would be. LongMemEval and LoCoMo remain useful public datasets.

The adapters and product claims need replacement; the corpora do not need to be
discarded. Causal traversal over people, projects, and event relationships
belongs in an Atlas benchmark rather than the Pulse memory score.

## The first honest runner exercised the product boundary

The new runner downloaded the published `@zbs-gg/pulse@0.8.0`, verified its
release identity, copied the active Personal SQLite database with online backup,
and queried through the shipped prompt-time product compositor. It did not call
the Python reference retriever or provide hidden state, graph, or gold-answer
fields to Pulse.

Retrieval and model answering are scored separately. The deterministic layer
records whether the right memory was offered, whether an unrelated memory was
injected, whether a weak query stayed silent, latency, and returned bytes. A
separate answer check may then measure whether the host preserved corrections
and negation after receiving the context.

The frozen private run used 40 semantic questions grounded in active Personal
capsules plus 10 unrelated controls. It found the expected memory in 34 cases,
stayed silent in 7 controls, and returned two internal query errors. Useful
recall, latency, and context size passed their initial limits; silence and
reliability did not. The full method and aggregate are recorded in
[`2026-08-12-real-personal-memory-baseline.md`](./2026-08-12-real-personal-memory-baseline.md).

The old single-memory own-data set remains a technical smoke corpus. Its former
MCP result remains historical and is not relabelled as a 0.8 product score.

## EmoBench needs a product-facing revision

Emotional cases remain in scope when the signal exists in the information a
real host can provide: the user's current wording or a previously saved
emotional moment. Hidden biometric snapshots should either be expressed in the
user-visible prompt or tested later through a real sensor integration. Gold
emotion tags may be used for judging but not passed to the retriever.

Before tuning any threshold, the revised cases must be split into a development
set and a frozen holdout. The release-facing result will report semantic recall,
irrelevant silence, correction and supersession, fresh-write visibility,
personal/project isolation, cancellation recovery, cold and warm p50/p95, and
returned context size. It will not combine these with the answer model into one
opaque score.

CodeGraph is installed but the dirty EmoBench checkout is not initialized. This
audit therefore used exact source inspection and did not create `.codegraph` or
change any benchmark files.
