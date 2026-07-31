# Pulse managed embedder runtime

This directory is the auditable source and provenance contract for the
Personal Pulse local embedder. End-user installation never builds it and never
uses system Python. The release job builds one arm64 macOS 13.5+ DMG containing
CPython 3.12.9, MLX/MLX Metal 0.29.3, Tokenizers 0.21.1, the fixed XLM-R
architecture, the JSON-line helper, tokenizer/config sidecars, licenses, an
SBOM, and an exact internal file manifest. The 1.1 GB data-only safetensors
weights remain a separate resumable artifact.

Release build:

```sh
PULSE_PRODUCTION_RELEASE=1 \
PULSE_NOTARYTOOL_PROFILE=<authorized-keychain-profile> \
PULSE_FLAGEMBEDDING_PYTHON=<pinned-FlagEmbedding-1.4.0-python> \
PULSE_BGE_M3_REFERENCE_MODEL=<BAAI-snapshot-path-containing-5617a9f...> \
PULSE_BGE_M3_MLX_MODEL=<verified-model.safetensors> \
npm run build:embedder-runtime
```

Production mode fails closed without the authorized Developer ID and notary
profile and without the full pinned quality-reference inputs. The quality gate
uses RU/EN/ZH query/document cases and requires exact top-1 retrieval, an
nDCG@10 delta no greater than 0.005, minimum vector cosine 0.999, cold start at
most 10 seconds, warm query p95 at most 250 ms, and peak RSS at most 2.5 GB.
It signs every Mach-O file, creates and signs the DMG, submits it to
Apple, staples the ticket, and validates Gatekeeper acceptance. Publishing the
DMG and signed release manifest is a separate authorized release ceremony.
The canonical `QUALITY.json` evidence is included in the signed carrier and
hashed by `pulse-artifact-tree.json`; the adjacent receipt is the same bytes.

`quality_gate.py` without a reference model proves only the deterministic CLS
pooling fixture and prints `quality_claimed: false`. A model-quality/parity
claim requires the optional `--reference-model` path, the official
`FlagEmbedding.BGEM3FlagModel`, and the managed runtime/model paths.
