"""Minimal MLX XLM-R encoder for Pulse bge-m3 dense embeddings.

This is an independent MLX port of the Apache-2.0 Hugging Face Transformers
XLM-R encoder identified in ../source-manifest.json. It intentionally omits
task heads, decoders, remote code, and dynamic model dispatch.
"""

from __future__ import annotations

import inspect
import math
from dataclasses import dataclass

import mlx.core as mx
import mlx.nn as nn


@dataclass
class ModelArgs:
    model_type: str
    hidden_size: int
    num_hidden_layers: int
    intermediate_size: int
    num_attention_heads: int
    max_position_embeddings: int
    vocab_size: int
    layer_norm_eps: float = 1e-5
    add_pooling_layer: bool = True
    attention_probs_dropout_prob: float = 0.1
    hidden_dropout_prob: float = 0.1
    type_vocab_size: int = 1
    pad_token_id: int = 1

    @classmethod
    def from_dict(cls, value: dict) -> "ModelArgs":
        allowed = inspect.signature(cls).parameters
        return cls(**{key: item for key, item in value.items() if key in allowed})


class Embeddings(nn.Module):
    def __init__(self, config: ModelArgs):
        super().__init__()
        self.word_embeddings = nn.Embedding(config.vocab_size, config.hidden_size)
        self.position_embeddings = nn.Embedding(config.max_position_embeddings, config.hidden_size)
        self.token_type_embeddings = nn.Embedding(config.type_vocab_size, config.hidden_size)
        self.LayerNorm = nn.LayerNorm(config.hidden_size, eps=config.layer_norm_eps)
        self.dropout = nn.Dropout(config.hidden_dropout_prob)
        self.padding_idx = config.pad_token_id

    def __call__(self, input_ids, token_type_ids=None, position_ids=None):
        if position_ids is None:
            mask = (input_ids != self.padding_idx).astype(mx.int32)
            position_ids = mx.cumsum(mask, axis=1) * mask + self.padding_idx
        if token_type_ids is None:
            token_type_ids = mx.zeros(input_ids.shape, dtype=mx.int32)
        value = self.word_embeddings(input_ids)
        value = value + self.position_embeddings(position_ids)
        value = value + self.token_type_embeddings(token_type_ids)
        return self.dropout(self.LayerNorm(value))


class SelfAttention(nn.Module):
    def __init__(self, config: ModelArgs):
        super().__init__()
        self.num_attention_heads = config.num_attention_heads
        self.head_size = config.hidden_size // config.num_attention_heads
        self.all_head_size = self.num_attention_heads * self.head_size
        self.query = nn.Linear(config.hidden_size, self.all_head_size)
        self.key = nn.Linear(config.hidden_size, self.all_head_size)
        self.value = nn.Linear(config.hidden_size, self.all_head_size)
        self.dropout = nn.Dropout(config.attention_probs_dropout_prob)

    def _heads(self, value):
        shape = value.shape[:-1] + (self.num_attention_heads, self.head_size)
        return value.reshape(shape).transpose(0, 2, 1, 3)

    def __call__(self, hidden_states, attention_mask=None):
        query = self._heads(self.query(hidden_states))
        key = self._heads(self.key(hidden_states))
        value = self._heads(self.value(hidden_states))
        scores = (query @ key.swapaxes(-1, -2)) / math.sqrt(self.head_size)
        if attention_mask is not None:
            scores = scores + attention_mask
        probability = nn.softmax(scores.astype(mx.float32), axis=-1).astype(scores.dtype)
        context = self.dropout(probability) @ value
        context = context.transpose(0, 2, 1, 3)
        return context.reshape(context.shape[:-2] + (self.all_head_size,))


class SelfOutput(nn.Module):
    def __init__(self, config: ModelArgs):
        super().__init__()
        self.dense = nn.Linear(config.hidden_size, config.hidden_size)
        self.LayerNorm = nn.LayerNorm(config.hidden_size, eps=config.layer_norm_eps)
        self.dropout = nn.Dropout(config.hidden_dropout_prob)

    def __call__(self, value, residual):
        return self.LayerNorm(self.dropout(self.dense(value)) + residual)


class Attention(nn.Module):
    def __init__(self, config: ModelArgs):
        super().__init__()
        # Attribute names intentionally match XLM-R safetensors keys.
        self.self = SelfAttention(config)
        self.output = SelfOutput(config)

    def __call__(self, value, attention_mask=None):
        return self.output(self.self(value, attention_mask), value)


class Intermediate(nn.Module):
    def __init__(self, config: ModelArgs):
        super().__init__()
        self.dense = nn.Linear(config.hidden_size, config.intermediate_size)

    def __call__(self, value):
        return nn.gelu(self.dense(value))


class Output(nn.Module):
    def __init__(self, config: ModelArgs):
        super().__init__()
        self.dense = nn.Linear(config.intermediate_size, config.hidden_size)
        self.LayerNorm = nn.LayerNorm(config.hidden_size, eps=config.layer_norm_eps)
        self.dropout = nn.Dropout(config.hidden_dropout_prob)

    def __call__(self, value, residual):
        return self.LayerNorm(self.dropout(self.dense(value)) + residual)


class Layer(nn.Module):
    def __init__(self, config: ModelArgs):
        super().__init__()
        self.attention = Attention(config)
        self.intermediate = Intermediate(config)
        self.output = Output(config)

    def __call__(self, value, attention_mask=None):
        attended = self.attention(value, attention_mask)
        return self.output(self.intermediate(attended), attended)


class Encoder(nn.Module):
    def __init__(self, config: ModelArgs):
        super().__init__()
        self.layer = [Layer(config) for _ in range(config.num_hidden_layers)]

    def __call__(self, value, attention_mask=None):
        for block in self.layer:
            value = block(value, attention_mask)
        return value


class Pooler(nn.Module):
    def __init__(self, config: ModelArgs):
        super().__init__()
        self.dense = nn.Linear(config.hidden_size, config.hidden_size)

    def __call__(self, value):
        return mx.tanh(self.dense(value[:, 0]))


class Model(nn.Module):
    def __init__(self, config: ModelArgs):
        super().__init__()
        self.config = config
        self.embeddings = Embeddings(config)
        self.encoder = Encoder(config)
        self.pooler = Pooler(config) if config.add_pooling_layer else None

    def __call__(self, input_ids, attention_mask):
        extended = (1.0 - attention_mask[:, None, None, :]) * -10000.0
        return self.encoder(self.embeddings(input_ids), extended)

    def sanitize(self, weights):
        return {key: value for key, value in weights.items() if "position_ids" not in key}
