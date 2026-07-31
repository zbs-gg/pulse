# Pulse managed embedder attribution

The runtime is built only from the immutable URLs and SHA-256 values in
`source-manifest.json`. It contains no `mlx-embeddings` code or dependency.

- `pulse_embedder/xlm_roberta.py` is an independent MLX port of the XLM-R
  encoder in Hugging Face Transformers 4.49.0, commit
  `a22a4378d97d06b7a1d9abad6e0086d30fdea199`, licensed Apache-2.0. The exact
  upstream file digest is pinned in the source manifest.
- MLX and MLX Metal 0.29.3 are Copyright 2023 Apple Inc., MIT licensed.
- Hugging Face Tokenizers 0.21.1 is Apache-2.0 licensed.
- CPython 3.12.9 and python-build-standalone retain the PSF and bundled
  third-party license files from the pinned distribution.
- BAAI bge-m3 and the pinned MLX data-only conversion declare the MIT license.
  Model code is disabled; only safetensors weights and pinned JSON sidecars are
  accepted.

`quality_gate.py` distinguishes a deterministic pooling contract check from an
optional quality/parity result against pinned `FlagEmbedding` 1.4.0 and the
official BAAI model revision recorded in the source manifest. Production also
requires the multilingual retrieval and resource thresholds. The fixture alone
must never be presented as a model-quality claim.
