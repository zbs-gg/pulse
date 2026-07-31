"""Pure protocol and pooling invariants shared by runtime and fixture gates."""

from __future__ import annotations

import math
import re

DIMENSIONS = 1024
MAX_BATCH = 96
MAX_LINE_BYTES = 8 * 1024 * 1024
MAX_TEXT_BYTES = 32 * 1024
PROTOCOL = 1
REQUEST_SCHEMA = "pulse.embedder.request.v1"
RESPONSE_SCHEMA = "pulse.embedder.response.v1"
READY_SCHEMA = "pulse.embedder.ready.v1"
_REQUEST_ID = re.compile(r"r[1-9][0-9]{0,19}\Z")


class ProtocolError(ValueError):
    pass


def validate_request(value: object) -> tuple[str, list[str]]:
    if not isinstance(value, dict) or set(value) != {"id", "schema", "texts"}:
        raise ProtocolError("request_fields_invalid")
    request_id = value["id"]
    if not isinstance(request_id, str) or _REQUEST_ID.fullmatch(request_id) is None:
        raise ProtocolError("request_id_invalid")
    if value["schema"] != REQUEST_SCHEMA:
        raise ProtocolError("request_schema_invalid")
    texts = value["texts"]
    if not isinstance(texts, list) or not 1 <= len(texts) <= MAX_BATCH:
        raise ProtocolError("request_batch_invalid")
    for text in texts:
        if not isinstance(text, str):
            raise ProtocolError("request_text_invalid")
        size = len(text.encode("utf-8", errors="strict"))
        if size < 1 or size > MAX_TEXT_BYTES:
            raise ProtocolError("request_text_invalid")
    return request_id, texts


def cls_pool_and_normalize(hidden_states: list[list[list[float]]]) -> list[list[float]]:
    """Reference fixture implementation: first-token (CLS), then L2 norm."""
    output: list[list[float]] = []
    for sequence in hidden_states:
        if not sequence:
            raise ProtocolError("pooling_sequence_empty")
        cls = sequence[0]
        if len(cls) != DIMENSIONS or any(not math.isfinite(value) for value in cls):
            raise ProtocolError("pooling_vector_invalid")
        norm = math.sqrt(sum(value * value for value in cls))
        if not math.isfinite(norm) or norm <= 1e-12:
            raise ProtocolError("pooling_norm_invalid")
        output.append([value / norm for value in cls])
    return output


def validate_embeddings(vectors: object, expected: int) -> list[list[float]]:
    if not isinstance(vectors, list) or len(vectors) != expected:
        raise ProtocolError("embedding_count_invalid")
    for vector in vectors:
        if not isinstance(vector, list) or len(vector) != DIMENSIONS:
            raise ProtocolError("embedding_dimension_invalid")
        if any(not isinstance(value, (int, float)) or not math.isfinite(value) for value in vector):
            raise ProtocolError("embedding_value_invalid")
        norm = math.sqrt(sum(float(value) * float(value) for value in vector))
        if abs(norm - 1.0) > 0.005:
            raise ProtocolError("embedding_norm_invalid")
    return vectors
