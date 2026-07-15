#!/usr/bin/env python3
"""Pulse managed bge-m3 JSON-line helper. No network or dynamic model code."""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

sys.dont_write_bytecode = True

from pulse_embedder.protocol import (
    DIMENSIONS,
    MAX_LINE_BYTES,
    PROTOCOL,
    READY_SCHEMA,
    RESPONSE_SCHEMA,
    ProtocolError,
    validate_embeddings,
    validate_request,
)


def _regular_file(path: Path, suffix: str | None = None) -> Path:
    if not path.is_absolute() or path.is_symlink() or not path.is_file():
        raise RuntimeError("runtime_path_invalid")
    if suffix is not None and path.suffix != suffix:
        raise RuntimeError("runtime_file_type_invalid")
    return path


def _load(model_file: Path, support_directory: Path):
    # Imports are intentionally delayed until after argument/path validation.
    import mlx.core as mx
    from tokenizers import Tokenizer
    from pulse_embedder.xlm_roberta import Model, ModelArgs

    config_file = _regular_file(support_directory / "config.json", ".json")
    tokenizer_file = _regular_file(support_directory / "tokenizer.json", ".json")
    with config_file.open("r", encoding="utf-8") as stream:
        config = json.load(stream)
    expected = {
        "model_type": "xlm-roberta",
        "hidden_size": DIMENSIONS,
        "num_hidden_layers": 24,
        "intermediate_size": 4096,
        "num_attention_heads": 16,
        "max_position_embeddings": 8194,
        "vocab_size": 250002,
        "pad_token_id": 1,
    }
    if any(config.get(key) != value for key, value in expected.items()):
        raise RuntimeError("model_config_contract_mismatch")
    model = Model(ModelArgs.from_dict(config))
    weights = model.sanitize(dict(mx.load(str(_regular_file(model_file, ".safetensors")))))
    model.load_weights(list(weights.items()))
    mx.eval(model.parameters())
    model.eval()
    tokenizer = Tokenizer.from_file(str(tokenizer_file))
    tokenizer.enable_truncation(max_length=8192)
    tokenizer.enable_padding(pad_id=1, pad_token="<pad>")
    return mx, model, tokenizer


def _embed(mx, model, tokenizer, texts: list[str]) -> list[list[float]]:
    encoded = tokenizer.encode_batch(texts, add_special_tokens=True)
    input_ids = mx.array([item.ids for item in encoded])
    attention_mask = mx.array([item.attention_mask for item in encoded])
    hidden = model(input_ids, attention_mask)
    # Official BGE-M3 dense embeddings use first-token (CLS) pooling.
    dense = hidden[:, 0, :]
    dense = dense / mx.maximum(mx.linalg.norm(dense, axis=-1, keepdims=True), 1e-12)
    mx.eval(dense)
    return validate_embeddings(dense.tolist(), len(texts))


def _emit(value: dict) -> None:
    sys.stdout.write(json.dumps(value, allow_nan=False, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def main() -> int:
    parser = argparse.ArgumentParser(allow_abbrev=False)
    parser.add_argument("--model-file", type=Path, required=True)
    parser.add_argument("--support-dir", type=Path, required=True)
    arguments = parser.parse_args()
    if not arguments.support_dir.is_absolute() or arguments.support_dir.is_symlink() or not arguments.support_dir.is_dir():
        raise RuntimeError("support_directory_invalid")
    mx, model, tokenizer = _load(arguments.model_file, arguments.support_dir)
    _emit({
        "dimensions": DIMENSIONS,
        "id": "__startup__",
        "model": "bge-m3",
        "normalized": True,
        "ok": True,
        "pooling": "cls",
        "protocol": PROTOCOL,
        "schema": READY_SCHEMA,
    })
    while True:
        raw = sys.stdin.buffer.readline(MAX_LINE_BYTES + 1)
        if raw == b"":
            return 0
        if len(raw) > MAX_LINE_BYTES or not raw.endswith(b"\n"):
            raise ProtocolError("request_line_invalid")
        request = json.loads(raw)
        request_id, texts = validate_request(request)
        vectors = _embed(mx, model, tokenizer, texts)
        _emit({"embeddings": vectors, "id": request_id, "schema": RESPONSE_SCHEMA})


if __name__ == "__main__":
    # Hash randomization cannot affect protocol order or model execution.
    os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
    raise SystemExit(main())
