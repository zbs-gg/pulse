# GPT-5.6-Luna extractor gate

Date: 2026-07-21  
Scope: Codex host extraction only  
Status: synthetic compatibility passed; private quality gate not authorized and not run

## Decision

Keep `codex:gpt-5.5:low` as the extractor recommendation for now.

`gpt-5.6-luna` at low effort is available through the Codex subscription and
can return the required compact JSON shape. That proves compatibility, not
quality. The frozen 12-session and 50-session private corpora have not been
sent to the provider, so this run cannot justify changing the default.

BGE-M3 remains the fixed local embedder and scorer. Claude Code and Cursor
extractor policies are unchanged.

## Synthetic compatibility receipt

| Field | Result |
|---|---|
| Model | `gpt-5.6-luna` |
| Reasoning effort | `low` |
| Codex CLI | `0.144.6` |
| Input | one fictional session with no private or project data |
| Isolation | temporary `HOME` and `CODEX_HOME`; user config and rules ignored; plugins, memories, apps, browser, computer use, multi-agent, and Chronicle disabled |
| Sandbox | read-only |
| Session persistence | ephemeral |
| Tool calls | 0 |
| Output | strict JSON with `summary` and typed `items` |
| Exit | 0 |
| Wall time | 11.36 seconds |
| Reported tokens | 10,049 |
| Residue | only temporary Codex state under the isolated home; the complete temporary home was deleted after inventory |

The first attempt with Codex CLI `0.136.0` failed closed because Luna required
a newer client. The stable CLI was upgraded to `0.144.6`; the identical
synthetic probe then passed. The token count includes Codex harness overhead
and is not a throughput or cost comparison.

## Frozen private baseline identity

Only content-free identities and aggregate thresholds are recorded here. The
private manifests, prompts, facts, provider outputs, and transcripts remain
outside this repository.

| Artifact role | SHA-256 |
|---|---|
| 12-session Codex runner | `37d81d2def35214c7179c5e64ce00df3825aed18c4c076b1a11b46ab6983965d` |
| 12-session shared prompt and stamp logic | `8510fa583d2917428c57d9358aa1a2e048d689e690c92d17f5d940d8329b76b2` |
| 12-session manifest | `096c0356ea9ebe2e6d4baca6d079446bd0e5e70c388ef94d45da54ccee24bea7` |
| 12-session aggregate baseline | `53de8a4cfa63805ae7ed25a7a455c52c2c4549a776013f9ce0000a0c1d8003a6` |
| 50-session ingestion runner | `fe158a7dcce6495fb35e11185f187f204a0ed9e2e102f578bf00b15955d734ec` |
| 50-session BGE-M3 scorer | `a487b5b2d374c64eb51f270b9fa3a78db4cdbff7ff79b776312d0887ab33b295` |
| 50-session manifest | `e0d7ced9c11834979fa197d2ce55cef7a469e07f768e4d104ee4dfe7ce11fabf` |
| deterministic scrubber | `c60d79d4c99015035d27e0c94446628efd4b7c286515b269ba0cc991dadb766d` |
| 50-session aggregate baseline | `f1ec195c08298da48ef8e2f9d89926079c75eb9949c69ff6174f951c025faf01` |

The frozen GPT-5.5-low 50-session gate remains:

- chat recall at 5: `1.000`;
- source reference correctness at 1: `0.867`;
- fact recall at 5: `0.733`;
- strict parse success: `1.000`;
- stored leak markers after the deterministic scrubber: `0`;
- measured throughput: `110.5` sessions per hour.

## Why the private gate stopped

Codex extraction is a provider call even when it is covered by a flat
subscription. Running the frozen evaluator would send selected private session
text to OpenAI. Plan acceptance and approval of a local consolidation dry run
do not authorize that corpus egress.

Before changing the extractor default, a human must explicitly approve sending
the frozen 12-session probe and 50-session gate corpus through Codex. The run
must then keep the manifest, prompt, schema, scrubber, BGE-M3 scorer, probes,
and metric definitions fixed. Luna-medium is allowed only once if Luna-low
misses a quality threshold. No silent model fallback is permitted.
