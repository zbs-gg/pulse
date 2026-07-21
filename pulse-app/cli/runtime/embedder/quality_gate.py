#!/usr/bin/env python3
"""Release quality gate against pinned FlagEmbedding 1.4.0.

Without --reference-model this proves only the deterministic pooling fixture
and emits quality_claimed=false. Production must run the full multilingual
parity, retrieval, and resource gate and require quality_claimed=true.
"""

from __future__ import annotations

import argparse
import importlib.metadata
import json
import math
import resource
import subprocess
import sys
import time
from pathlib import Path

sys.dont_write_bytecode = True

from pulse_embedder.protocol import DIMENSIONS, cls_pool_and_normalize, validate_embeddings

REFERENCE_PACKAGE_VERSION = "1.4.0"
REFERENCE_PACKAGE_SHA256 = "fb1856b312851591341cf4533187350e9ce43f66bbf195c66f25a73266ff7db9"
REFERENCE_MODEL_REVISION = "5617a9f61b028005a4858fdac845db406aefb181"
REFERENCE_MODEL_MANIFEST_SHA256 = "fa4361447341e16d2a95095ce369e67eafad53cfb93eac741418d722dac5f5f8"
THRESHOLDS = {
    "cold_start_ms_max": 10_000.0,
    "minimum_cosine": 0.999,
    "ndcg_at_10_delta_max": 0.005,
    "peak_rss_bytes_max": 2_500_000_000,
    "top1_min": 1.0,
    "warm_query_p95_ms_max": 250.0,
}

DOCUMENTS = [
    ("en_install", "Pulse installs local project memory with one command and no compiler or API key."),
    ("en_privacy", "Personal memories stay in a private local vault and are never shared with a team automatically."),
    ("en_tokens", "The dashboard reports measured input tokens avoided by reusing a compact continuity pack."),
    ("en_receipt", "Every memory candidate is shown before save and receives an editable storage receipt."),
    ("ru_install", "Pulse устанавливает локальную память проекта одной командой без компилятора и API ключа."),
    ("ru_privacy", "Личные воспоминания остаются в приватном локальном хранилище и не попадают команде автоматически."),
    ("ru_tokens", "Дашборд показывает измеренное количество сэкономленных токенов благодаря сжатому контексту."),
    ("ru_receipt", "Каждая запись памяти показывается до сохранения и получает редактируемую квитанцию."),
    ("zh_install", "Pulse 用一条命令安装本地项目记忆，不需要编译器或 API 密钥。"),
    ("zh_privacy", "个人记忆保存在私有本地存储中，绝不会自动共享给团队。"),
    ("zh_tokens", "仪表板显示通过复用压缩连续性上下文而实际节省的输入令牌数量。"),
    ("zh_receipt", "每条候选记忆在保存前都会展示，并提供可编辑的存储回执。"),
]

QUERIES = [
    ("Can I install without Go, Python, or an API key?", "en_install"),
    ("Can my private memories leak into team storage automatically?", "en_privacy"),
    ("Where can I see measured token savings?", "en_tokens"),
    ("Can I inspect and edit a memory before it is stored?", "en_receipt"),
    ("Нужны ли Go, Python или API ключ для установки?", "ru_install"),
    ("Может ли личная память автоматически утечь в командную?", "ru_privacy"),
    ("Где посмотреть реальную экономию токенов?", "ru_tokens"),
    ("Можно ли проверить и изменить воспоминание перед записью?", "ru_receipt"),
    ("安装时需要 Go、Python 或 API 密钥吗？", "zh_install"),
    ("个人记忆会自动泄露到团队空间吗？", "zh_privacy"),
    ("在哪里查看实际节省的令牌数量？", "zh_tokens"),
    ("保存前可以检查和编辑记忆吗？", "zh_receipt"),
]


def deterministic_fixture(root: Path) -> None:
    value = json.loads((root / "fixture-contract.json").read_text(encoding="utf-8"))
    case = value["case"]
    cls = case["cls_prefix"] + [0.0] * case["zero_tail"]
    distractor = case["non_cls_prefix"] + [0.0] * case["zero_tail"]
    actual = cls_pool_and_normalize([[cls, distractor]])[0]
    if len(actual) != case["dimension"] or any(
        abs(actual[index] - expected) > 1e-12 for index, expected in enumerate(case["expected_prefix"])
    ):
        raise RuntimeError("deterministic_cls_fixture_failed")


class ManagedSession:
    def __init__(self, arguments):
        started = time.perf_counter()
        self.process = subprocess.Popen(
            [arguments.managed_python, arguments.managed_helper, "--model-file", arguments.model_file,
             "--support-dir", arguments.support_directory],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
        )
        ready = json.loads(self.process.stdout.readline())
        self.cold_start_ms = (time.perf_counter() - started) * 1000
        expected = {
            "dimensions": DIMENSIONS, "id": "__startup__", "model": "bge-m3", "normalized": True,
            "ok": True, "pooling": "cls", "protocol": 1, "schema": "pulse.embedder.ready.v1",
        }
        if ready != expected:
            self.close()
            raise RuntimeError("managed_startup_contract_failed")
        self.request_id = 0

    def embed(self, texts: list[str]) -> tuple[list[list[float]], float]:
        self.request_id += 1
        request = {"id": f"r{self.request_id}", "schema": "pulse.embedder.request.v1", "texts": texts}
        started = time.perf_counter()
        self.process.stdin.write(json.dumps(request, separators=(",", ":")) + "\n")
        self.process.stdin.flush()
        response = json.loads(self.process.stdout.readline())
        elapsed = (time.perf_counter() - started) * 1000
        if response.get("id") != request["id"] or response.get("schema") != "pulse.embedder.response.v1" or set(response) != {"embeddings", "id", "schema"}:
            raise RuntimeError("managed_response_contract_failed")
        return validate_embeddings(response["embeddings"], len(texts)), elapsed

    def close(self) -> int:
        if self.process.poll() is None:
            self.process.kill()
        self.process.wait()
        return resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss


def managed_evidence(arguments, texts: list[str]) -> tuple[list[list[float]], dict]:
    session = ManagedSession(arguments)
    latencies = []
    try:
        session.embed(["Pulse managed embedder warmup."])
        vectors, _ = session.embed(texts)
        for query, _ in QUERIES:
            _, elapsed = session.embed([query])
            latencies.append(elapsed)
        cold = session.cold_start_ms
    finally:
        peak_rss = session.close()
    ordered = sorted(latencies)
    p95 = ordered[max(0, math.ceil(0.95 * len(ordered)) - 1)]
    return vectors, {"cold_start_ms": cold, "peak_rss_bytes": peak_rss, "warm_query_p95_ms": p95}


def official_vectors(reference_model: str, reference_package_root: str, texts: list[str]) -> list[list[float]]:
    package_root = Path(reference_package_root).resolve()
    distributions = [
        distribution for distribution in importlib.metadata.distributions(path=[str(package_root)])
        if distribution.metadata.get("Name", "").lower() == "flagembedding"
    ]
    if len(distributions) != 1 or distributions[0].version != REFERENCE_PACKAGE_VERSION:
        raise RuntimeError("flagembedding_version_mismatch")
    sys.path.insert(0, str(package_root))
    from FlagEmbedding import BGEM3FlagModel
    imported_package = Path(sys.modules["FlagEmbedding"].__file__).resolve()
    if not imported_package.is_relative_to(package_root):
        raise RuntimeError("flagembedding_package_not_isolated")
    model = BGEM3FlagModel(reference_model, use_fp16=True)
    encoded = model.encode(texts, batch_size=8, max_length=8192, return_dense=True,
                           return_sparse=False, return_colbert_vecs=False)
    return validate_embeddings(encoded["dense_vecs"].tolist(), len(texts))


def cosine(left, right) -> float:
    return sum(a * b for a, b in zip(left, right)) / (
        math.sqrt(sum(value * value for value in left)) * math.sqrt(sum(value * value for value in right))
    )


def retrieval_metrics(vectors: list[list[float]]) -> dict:
    document_count = len(DOCUMENTS)
    documents = vectors[:document_count]
    queries = vectors[document_count:]
    top1 = 0
    ndcg = 0.0
    ids = [identifier for identifier, _ in DOCUMENTS]
    for query_vector, (_, relevant) in zip(queries, QUERIES):
        ranking = sorted(range(document_count), key=lambda index: cosine(query_vector, documents[index]), reverse=True)
        rank = ranking.index(ids.index(relevant))
        if rank == 0:
            top1 += 1
        if rank < 10:
            ndcg += 1.0 / math.log2(rank + 2)
    return {"ndcg_at_10": ndcg / len(QUERIES), "top1": top1 / len(QUERIES)}


def main() -> int:
    parser = argparse.ArgumentParser(allow_abbrev=False)
    parser.add_argument("--managed-python")
    parser.add_argument("--managed-helper")
    parser.add_argument("--model-file")
    parser.add_argument("--support-directory")
    parser.add_argument("--reference-model")
    parser.add_argument("--reference-model-manifest-sha256")
    parser.add_argument("--reference-package-root")
    parser.add_argument("--reference-package-sha256")
    arguments = parser.parse_args()
    root = Path(__file__).resolve().parent
    deterministic_fixture(root)
    if arguments.reference_model is None:
        print(json.dumps({
            "fixture": "pass", "quality_claimed": False, "schema": "pulse.embedder.quality_gate.v1",
        }, sort_keys=True))
        return 0
    required = [
        arguments.managed_python, arguments.managed_helper, arguments.model_file,
        arguments.reference_model_manifest_sha256, arguments.reference_package_root,
        arguments.reference_package_sha256, arguments.support_directory,
    ]
    if any(not value for value in required):
        raise RuntimeError("official_reference_gate_requires_managed_runtime_paths")
    reference_path = Path(arguments.reference_model).resolve()
    if not reference_path.is_dir():
        raise RuntimeError("official_reference_model_missing")
    if arguments.reference_model_manifest_sha256 != REFERENCE_MODEL_MANIFEST_SHA256:
        raise RuntimeError("official_reference_model_manifest_mismatch")
    if arguments.reference_package_sha256 != REFERENCE_PACKAGE_SHA256:
        raise RuntimeError("official_reference_package_digest_mismatch")
    texts = [text for _, text in DOCUMENTS] + [query for query, _ in QUERIES]
    managed, resources = managed_evidence(arguments, texts)
    official = official_vectors(str(reference_path), arguments.reference_package_root, texts)
    similarities = [cosine(left, right) for left, right in zip(managed, official)]
    managed_retrieval = retrieval_metrics(managed)
    official_retrieval = retrieval_metrics(official)
    ndcg_delta = abs(managed_retrieval["ndcg_at_10"] - official_retrieval["ndcg_at_10"])
    failures = []
    if min(similarities) < THRESHOLDS["minimum_cosine"]:
        failures.append("cosine")
    if managed_retrieval["top1"] < THRESHOLDS["top1_min"] or official_retrieval["top1"] < THRESHOLDS["top1_min"]:
        failures.append("top1")
    if ndcg_delta > THRESHOLDS["ndcg_at_10_delta_max"]:
        failures.append("ndcg")
    if resources["cold_start_ms"] > THRESHOLDS["cold_start_ms_max"]:
        failures.append("cold_start")
    if resources["warm_query_p95_ms"] > THRESHOLDS["warm_query_p95_ms_max"]:
        failures.append("warm_latency")
    if resources["peak_rss_bytes"] > THRESHOLDS["peak_rss_bytes_max"]:
        failures.append("peak_rss")
    if failures:
        raise RuntimeError(f"official_flagembedding_quality_gate_failed:{','.join(failures)}")
    print(json.dumps({
        "corpus": {"documents": len(DOCUMENTS), "languages": ["en", "ru", "zh"], "queries": len(QUERIES)},
        "fixture": "pass",
        "managed_retrieval": managed_retrieval,
        "minimum_cosine": min(similarities),
        "ndcg_at_10_delta": ndcg_delta,
        "official_retrieval": official_retrieval,
        "quality_claimed": True,
        "reference": {
            "model_manifest_sha256": REFERENCE_MODEL_MANIFEST_SHA256,
            "model_revision": REFERENCE_MODEL_REVISION,
            "package": "FlagEmbedding",
            "package_sha256": REFERENCE_PACKAGE_SHA256,
            "version": REFERENCE_PACKAGE_VERSION,
        },
        "resources": resources,
        "schema": "pulse.embedder.quality_gate.v1",
        "thresholds": THRESHOLDS,
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
