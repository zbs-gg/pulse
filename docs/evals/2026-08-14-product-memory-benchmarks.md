# Pulse 0.8 product-memory benchmarks

Date: 2026-08-14

## Pulse is quiet and fast on compact memories, but weak on raw long histories

The current candidate fixes one concrete false-positive path in automatic
context selection. On the small own-data suite it recalled 12 of 15 expected
memories, stayed silent on all 5 unrelated questions, returned no query errors,
and kept warm p95 at 51 ms. The published 0.8.0 compositor recalled one more
expected item but injected memory into 3 of the 5 unrelated questions. The
candidate therefore makes the intended precision trade-off: a weak direct
capsule is omitted instead of being accepted through the lower archive
threshold.

The result is not yet a general memory-quality victory. On the full LoCoMo raw
conversation corpus, the same automatic path returned gold evidence for 561 of
1,535 eligible questions (36.5%). Pulse is good at retrieving compact durable
memories; it does not yet reliably turn thousands of raw dialogue turns into
the right compact memory.

## Every Pulse number below used the installable product path

The runner downloads the exact published `@zbs-gg/pulse@0.8.0` archive from
npm, verifies npm integrity and the installed signed release, creates an empty
temporary Personal vault, writes through `pulse_memory`, and queries through
the same automatic prompt compositor used by Codex and Claude Code. The source
candidate changes only the compositor under test; its MCP server, daemon,
SQLite schema, BGE-M3 embedder, and signed runtime remain the published 0.8.0
ones.

Queries are scanned before and after each run to verify that their exact text
was not persisted. Results contain IDs, digests, scores, timings, and counts,
not the benchmark prompts or memories. Each benchmark case gets its own signed
synthetic repository binding so project memory cannot cross cases.

## The own-data suite exposed and verified the false-positive fix

| Product path | Needed memory | Correct silence | Errors | Warm p95 | Largest context |
| --- | ---: | ---: | ---: | ---: | ---: |
| Published 0.8.0 | 13 / 15 | 2 / 5 | 0 | 117 ms | 131 estimated tokens |
| 0.8.1 candidate compositor | 12 / 15 | 5 / 5 | 0 | 51 ms | 83 estimated tokens |

One multi-fragment dossier question was excluded because the automatic hot path
is intentionally limited to one direct capsule or two archive events. The
three remaining misses show the next retrieval problem: a short stored summary
can be semantically adjacent to the question without containing the particular
fact the question asks for. Lowering the threshold again would restore noise,
not solve that ranking problem.

The npm archive was 3.3 MB and its installed dependency tree was 25.6 MB. The
already installed signed runtime, including the daemon, local embedding
runtime, BGE-M3 model, and plugin runtime, occupied 1.03 GB. A temporary vault
with 16 memories occupied 6.5 MB. This runner verified npm installation and the
active signed runtime identity; the release gate separately owns a clean
one-command product installation.

## The old EmoBench gave Pulse information it would never receive from a user

The old Python adapter received gold emotion labels, hidden user state,
biometrics, predecessor links, and hand-written query hints. It also expanded
causal chains itself. Those scores measured an assisted research pipeline, not
Pulse as installed in Codex or Claude Code.

Only 5 of its 35 questions can be asked honestly through the current product
contract. Replayed as ordinary saved emotional moments, Pulse found acceptable
evidence for 3 of 5 and stayed silent on 2 of 5 unrelated controls. The other
30 cases are excluded: 10 require hidden user state, 10 require hidden
biometrics, and 10 require relationship traversal that belongs to Atlas. One of
60 source events was rejected by the normal product write policy and was not
bypassed.

EmoBench should therefore be rebuilt, not tuned. Gold labels may judge an
answer but must not be passed to retrieval. Emotional questions must be based
on words visible to a normal host or on an emotional moment Pulse actually
stored. The revised corpus needs separate development and frozen holdout sets.

## LongMemEval showed a ceiling, while LoCoMo showed the real weakness

The bounded LongMemEval-S run selected 5 questions from each of its 6
categories. For every question it stored all official answer sessions plus 4
deterministic distractor sessions. Pulse returned at least one official answer
turn for all 30 questions, with no errors and 77 ms warm p95. This is an oracle
extraction ceiling on a small stratified sample, not official LongMemEval
answer accuracy and not a competitor-comparable score.

The LoCoMo run was deliberately harder and more useful. It stored 5,873 normal
dialogue turns from all 10 conversations, then asked every eligible question
from the published four-category set. Pulse found official evidence in 561 of
1,535 cases, with no query errors, 78 ms warm p95, and at most 131 estimated
tokens of context. Recall by the dataset's numeric category was 88/282,
141/320, 19/92, and 313/841. Nine turns rejected by normal product policy were
left rejected.

This means the next product work is not a graph or a larger context dump. Pulse
needs a better way to extract and rank compact memories from raw history before
large chat exports are imported. Atlas can later consume Pulse memory to model
people, projects, events, and relationships; it should not be smuggled into the
memory benchmark.

## Competitor headline scores are not comparable to the Pulse retrieval score

Mem0 reports 94.4% LongMemEval and 92.5% LoCoMo answer accuracy with mean
contexts of 6,787 and 6,956 tokens. Zep reports 90.2% and 94.7% with median
contexts of 4,408 and 5,760 tokens. Both use an answer model and an LLM judge;
their product teams report the figures. Pulse currently reports deterministic
evidence retrieval before an answer model and returns at most about 131 tokens
in these runs. Putting the percentages in one leaderboard would be dishonest.

The useful comparison is architectural. Mem0 and Zep spend thousands of
retrieved tokens and perform extraction or graph construction to maximize
end-to-end answers. Pulse spends tens to roughly one hundred tokens and stays
fully local, but its current raw-history recall is much lower. Claude Mem's
official repository describes automatic capture and semantic summaries but
does not publish a directly comparable LoCoMo or LongMemEval run. Plain text
rules remain the simplest reliable place for a handful of stable instructions,
but they are not a searchable long-term-memory benchmark contestant.

Primary external sources:

- [LongMemEval official repository](https://github.com/xiaowu0162/longmemeval)
- [LoCoMo official repository](https://github.com/snap-research/locomo)
- [Mem0 benchmark repository](https://github.com/mem0ai/memory-benchmarks)
- [Mem0 research results](https://mem0.ai/research)
- [Zep research results](https://www.getzep.com/research/)
- [Claude Mem official repository](https://github.com/thedotmack/claude-mem)

## The next benchmark work is narrow

Release the false-positive fix, then freeze the runner and inputs. The next
iteration should add a real import/extraction path and rerun full LongMemEval
and LoCoMo with an answer model and the official judges. EmoBench should be
revised independently around user-visible emotional evidence. Thresholds must
be tuned only on development data and checked once on a holdout; the five
unrelated controls in this report are evidence, not a tuning set.

Raw result receipts:

- [`published own-data`](./results/2026-08-13-product-own-memory-0.8.0-published.json)
- [`candidate own-data`](./results/2026-08-13-product-own-memory-0.8.0-candidate.json)
- [`published EmoBench product subset`](./results/2026-08-13-product-emobench-v3-0.8.0-published.json)
- [`candidate EmoBench product subset`](./results/2026-08-13-product-emobench-v3-0.8.0-candidate.json)
- [`candidate LongMemEval-S sample`](./results/2026-08-13-product-longmemeval-s-retrieval-30-0.8.0-candidate.json)
- [`candidate LoCoMo retrieval`](./results/2026-08-13-product-locomo-retrieval-0.8.0-candidate.json)
