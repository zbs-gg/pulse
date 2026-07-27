#!/usr/bin/env python3

"""Build Pulse's data-only, cross-platform BGE-M3 ONNX model artifact."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
from importlib.metadata import version

os.environ.setdefault("HF_HUB_OFFLINE", "1")
os.environ.setdefault("TRANSFORMERS_OFFLINE", "1")
os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")

import numpy as np
import onnx
import onnxruntime as ort
from onnxruntime.quantization import QuantType, quantize_dynamic
import torch
from transformers import AutoModel, AutoTokenizer


MODEL_REVISION = "5617a9f61b028005a4858fdac845db406aefb181"
SOURCE_REVISION = "a37eddded9a6a1273a87fb8b0da0d1cdbd98aeec"
EXPECTED_PACKAGES = {
    "numpy": "2.3.1",
    "onnx": "1.19.1",
    "onnxruntime": "1.23.2",
    "safetensors": "0.7.0",
    "tokenizers": "0.22.2",
    "torch": "2.11.0",
    "transformers": "4.57.3",
}
SOURCE_FILES = {
    "config.json": {
        "bytes": 715,
        "sha256": "2f0b6a5c00cf8653f67d93dd611b119f57e9199f876e137de63090dab4dfb426",
    },
    "model.safetensors": {
        "bytes": 1_135_554_209,
        "sha256": "e8028acb2e77e0010d35ef75832d823edb5851c95682deef2bde86a45bd4441d",
    },
    "special_tokens_map.json": {
        "bytes": 964,
        "sha256": "8c785abebea9ae3257b61681b4e6fd8365ceafde980c21970d001e834cf10835",
    },
    "tokenizer.json": {
        "bytes": 17_098_085,
        "sha256": "5df1f55d60c9705a501ab9a75550728625740741fe4be308dac4806c16b7d51d",
    },
    "tokenizer_config.json": {
        "bytes": 379,
        "sha256": "c2ef99124628ae6f79a847ad67e5d3f5b016d0387de90c9fbaf17c45a98933af",
    },
}
SUPPORT_FILES = (
    "config.json",
    "special_tokens_map.json",
    "tokenizer.json",
    "tokenizer_config.json",
)
VECTOR_CONTRACT = {
    "dimensions": 1024,
    "model": "bge-m3",
    "normalized": True,
    "opset": 17,
    "pooling": "cls",
    "quantization": "dynamic-int8",
    "revision": MODEL_REVISION,
    "source": "BAAI/bge-m3",
}
QUALITY_TEXTS = (
    "Store the approved personal memory for the next task.",
    "Preserve approved personal memory across a newly opened task.",
    "Do not import old chats without explicit approval.",
    "The weather in Phuket is humid today.",
    "Сохрани одобренную личную память для следующей задачи.",
    "Передай одобренную память в новую задачу автоматически.",
    "Не импортируй старые чаты без явного согласия.",
    "Сегодня на Пхукете влажная погода.",
    "Memory receipt id must be durable and inspectable.",
    "A visible Home card requires my approval.",
    "Windows, Linux, and macOS must share the same ONNX model.",
    "fn retrieve(query: string): Promise<Memory[]>",
    "หน่วยความจำส่วนตัวต้องทำงานในเครื่อง",
)
QUALITY_THRESHOLDS = {
    "maximum_pairwise_cosine_drift_ppm": 35_000,
    "minimum_mean_self_cosine_ppm": 985_000,
    "minimum_self_cosine_ppm": 980_000,
    "minimum_top1_agreement_ppm": 1_000_000,
}


def canonical_json(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while chunk := handle.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_json(value: object) -> str:
    return hashlib.sha256(canonical_json(value).encode("utf-8")).hexdigest()


def exact_directory(path: str, label: str) -> Path:
    candidate = Path(path)
    if not candidate.is_absolute() or candidate.resolve() != candidate:
        raise ValueError(f"{label} must be an absolute clean path")
    if not candidate.is_dir() or candidate.is_symlink():
        raise ValueError(f"{label} must be a real directory")
    return candidate


def exact_file(path: str, label: str) -> Path:
    candidate = Path(path)
    if not candidate.is_absolute() or candidate.resolve() != candidate:
        raise ValueError(f"{label} must be an absolute clean path")
    if not candidate.is_file() or candidate.is_symlink():
        raise ValueError(f"{label} must be a regular file")
    return candidate


def verify_packages() -> dict[str, str]:
    actual = {name: version(name) for name in EXPECTED_PACKAGES}
    if actual != EXPECTED_PACKAGES:
        raise RuntimeError(
            f"portable model exporter package mismatch: expected {EXPECTED_PACKAGES}, got {actual}"
        )
    return actual


def verify_source(root: Path) -> dict[str, dict[str, object]]:
    verified: dict[str, dict[str, object]] = {}
    for name, expected in SOURCE_FILES.items():
        path = root / name
        if not path.is_file() or path.is_symlink():
            raise RuntimeError(f"portable model source is missing {name}")
        actual = {"bytes": path.stat().st_size, "sha256": sha256_file(path)}
        if actual != expected:
            raise RuntimeError(
                f"portable model source mismatch for {name}: expected {expected}, got {actual}"
            )
        verified[name] = actual
    return verified


class DenseEncoder(torch.nn.Module):
    def __init__(self, inner: torch.nn.Module):
        super().__init__()
        self.inner = inner

    def forward(
        self, input_ids: torch.Tensor, attention_mask: torch.Tensor
    ) -> torch.Tensor:
        return self.inner(
            input_ids=input_ids,
            attention_mask=attention_mask,
            return_dict=False,
        )[0]


def normalized_cls(hidden: np.ndarray) -> np.ndarray:
    values = hidden[:, 0, :].astype(np.float64)
    norms = np.linalg.norm(values, axis=1, keepdims=True)
    if not np.isfinite(values).all() or not np.isfinite(norms).all() or np.any(norms <= 1e-12):
        raise RuntimeError("portable model emitted an invalid CLS vector")
    return values / norms


def quality_evidence(
    reference: np.ndarray,
    candidate: np.ndarray,
    model_digest: str,
    packages: dict[str, str],
) -> dict[str, object]:
    reference_cls = normalized_cls(reference)
    candidate_cls = normalized_cls(candidate)
    self_cosines = np.sum(reference_cls * candidate_cls, axis=1)
    reference_pairs = reference_cls @ reference_cls.T
    candidate_pairs = candidate_cls @ candidate_cls.T
    pairwise_drift = np.abs(reference_pairs - candidate_pairs)
    np.fill_diagonal(reference_pairs, -2)
    np.fill_diagonal(candidate_pairs, -2)
    top1_agreement = np.mean(
        np.argmax(reference_pairs, axis=1) == np.argmax(candidate_pairs, axis=1)
    )
    metrics = {
        "maximum_pairwise_cosine_drift_ppm": round(float(pairwise_drift.max()) * 1_000_000),
        "mean_self_cosine_ppm": round(float(self_cosines.mean()) * 1_000_000),
        "minimum_self_cosine_ppm": round(float(self_cosines.min()) * 1_000_000),
        "top1_agreement_ppm": round(float(top1_agreement) * 1_000_000),
    }
    passed = (
        metrics["maximum_pairwise_cosine_drift_ppm"]
        <= QUALITY_THRESHOLDS["maximum_pairwise_cosine_drift_ppm"]
        and metrics["mean_self_cosine_ppm"]
        >= QUALITY_THRESHOLDS["minimum_mean_self_cosine_ppm"]
        and metrics["minimum_self_cosine_ppm"]
        >= QUALITY_THRESHOLDS["minimum_self_cosine_ppm"]
        and metrics["top1_agreement_ppm"]
        >= QUALITY_THRESHOLDS["minimum_top1_agreement_ppm"]
    )
    evidence = {
        "corpus": {
            "count": len(QUALITY_TEXTS),
            "id": "pulse-portable-model-multilingual-v1",
            "sha256": sha256_json(list(QUALITY_TEXTS)),
        },
        "export": {
            "opset": 17,
            "packages": packages,
            "quantization": "dynamic-int8-per-channel",
        },
        "metrics": metrics,
        "model_sha256": model_digest,
        "passed": passed,
        "schema": "pulse.portable_embedder.quality_gate.v1",
        "thresholds": QUALITY_THRESHOLDS,
    }
    if not passed:
        raise RuntimeError(f"portable model quality gate failed: {canonical_json(evidence)}")
    return evidence


def assert_onnx_contract(path: Path) -> None:
    onnx.checker.check_model(str(path))
    model = onnx.load(path, load_external_data=False)
    if [(entry.domain, entry.version) for entry in model.opset_import] != [("", 17)]:
        raise RuntimeError("portable model ONNX opset mismatch")
    if [entry.name for entry in model.graph.input] != ["input_ids", "attention_mask"]:
        raise RuntimeError("portable model ONNX input contract mismatch")
    if [entry.name for entry in model.graph.output] != ["last_hidden_state"]:
        raise RuntimeError("portable model ONNX output contract mismatch")
    if any(
        initializer.data_location == onnx.TensorProto.EXTERNAL
        for initializer in model.graph.initializer
    ):
        raise RuntimeError("portable model must be one self-contained ONNX file")


def build(source: Path, license_path: Path, output: Path) -> dict[str, object]:
    if output.exists():
        raise RuntimeError("portable model output already exists")
    if not output.parent.is_dir() or output.parent.is_symlink():
        raise RuntimeError("portable model output parent is invalid")
    packages = verify_packages()
    source_files = verify_source(source)
    stage = Path(tempfile.mkdtemp(prefix=".pulse-portable-model-", dir=output.parent))
    export_root = Path(tempfile.mkdtemp(prefix="pulse-portable-model-export-"))
    try:
        model = AutoModel.from_pretrained(
            source,
            local_files_only=True,
            trust_remote_code=False,
        )
        model.eval()
        if (
            model.config.model_type != "xlm-roberta"
            or model.config.hidden_size != 1024
            or model.config.num_hidden_layers != 24
        ):
            raise RuntimeError("portable model source architecture mismatch")
        tokenizer = AutoTokenizer.from_pretrained(
            source,
            local_files_only=True,
            trust_remote_code=False,
        )
        tokenized = tokenizer(
            list(QUALITY_TEXTS),
            padding=True,
            truncation=True,
            max_length=8192,
            return_tensors="pt",
        )
        if sorted(tokenized.keys()) != ["attention_mask", "input_ids"]:
            raise RuntimeError("portable model tokenizer contract mismatch")
        encoder = DenseEncoder(model)
        with torch.inference_mode():
            reference = encoder(
                tokenized["input_ids"], tokenized["attention_mask"]
            ).detach().cpu().numpy()
        float_model = export_root / "model_fp32.onnx"
        output_model = stage / "model_int8.onnx"
        torch.onnx.export(
            encoder,
            (tokenized["input_ids"], tokenized["attention_mask"]),
            str(float_model),
            input_names=["input_ids", "attention_mask"],
            output_names=["last_hidden_state"],
            dynamic_axes={
                "input_ids": {0: "batch", 1: "sequence"},
                "attention_mask": {0: "batch", 1: "sequence"},
                "last_hidden_state": {0: "batch", 1: "sequence"},
            },
            opset_version=17,
            do_constant_folding=True,
            dynamo=False,
            external_data=True,
        )
        quantize_dynamic(
            float_model,
            output_model,
            per_channel=True,
            reduce_range=False,
            weight_type=QuantType.QInt8,
            use_external_data_format=False,
        )
        assert_onnx_contract(output_model)
        session = ort.InferenceSession(
            str(output_model), providers=["CPUExecutionProvider"]
        )
        if (
            [entry.name for entry in session.get_inputs()]
            != ["input_ids", "attention_mask"]
            or [entry.name for entry in session.get_outputs()]
            != ["last_hidden_state"]
        ):
            raise RuntimeError("portable model runtime contract mismatch")
        candidate = session.run(
            ["last_hidden_state"],
            {
                "input_ids": tokenized["input_ids"].numpy().astype(np.int64),
                "attention_mask": tokenized["attention_mask"].numpy().astype(np.int64),
            },
        )[0]
        if candidate.shape != reference.shape or candidate.shape[2] != 1024:
            raise RuntimeError("portable model runtime output shape mismatch")
        model_digest = sha256_file(output_model)
        quality = quality_evidence(reference, candidate, model_digest, packages)
        evidence_digest = sha256_json(quality)
        support = stage / "support"
        support.mkdir(mode=0o700)
        for name in SUPPORT_FILES:
            shutil.copyfile(source / name, support / name)
        licenses = stage / "LICENSES"
        licenses.mkdir(mode=0o700)
        shutil.copyfile(license_path, licenses / "BGE-M3-MIT.txt")
        provenance = {
            "conversion": {
                "engine": "transformers-js-onnx",
                "opset": 17,
                "quantization": "dynamic-int8-per-channel",
            },
            "quality_evidence": quality,
            "quality_evidence_digest": evidence_digest,
            "schema": "pulse.portable_embedder.provenance.v1",
            "source": {
                "files": source_files,
                "model": "mlx-community/bge-m3-mlx-fp16",
                "revision": SOURCE_REVISION,
                "upstream_model": "BAAI/bge-m3",
                "upstream_revision": MODEL_REVISION,
            },
        }
        (stage / "PROVENANCE.json").write_text(
            f"{canonical_json(provenance)}\n", encoding="utf-8"
        )
        contract = {
            "engine": "transformers-js-onnx",
            "model_file": "model_int8.onnx",
            "quality": {
                "evidence_digest": evidence_digest,
                "kind": "production",
            },
            "schema": "pulse.portable_embedder.model.v1",
            "support_files": list(SUPPORT_FILES),
            "vector_contract": VECTOR_CONTRACT,
        }
        (stage / "pulse-model-contract.json").write_text(
            f"{canonical_json(contract)}\n", encoding="utf-8"
        )
        stage.rename(output)
        return {
            "model_bytes": (output / "model_int8.onnx").stat().st_size,
            "model_sha256": model_digest,
            "production_ready": True,
            "quality_evidence_digest": evidence_digest,
            "quality_metrics": quality["metrics"],
            "schema": "pulse.portable_model_conversion.v1",
            "vector_contract": VECTOR_CONTRACT,
        }
    finally:
        shutil.rmtree(export_root, ignore_errors=True)
        if stage.exists():
            shutil.rmtree(stage, ignore_errors=True)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--license", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--source-model", required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    source = exact_directory(args.source_model, "source model")
    license_path = exact_file(args.license, "model license")
    output = Path(args.output)
    if not output.is_absolute() or output.resolve() != output:
        raise ValueError("output must be an absolute clean path")
    result = build(source, license_path, output)
    sys.stdout.write(f"{canonical_json(result)}\n")


if __name__ == "__main__":
    main()
