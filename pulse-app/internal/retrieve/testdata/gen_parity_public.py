#!/usr/bin/env python3
"""Generate the PUBLIC Go==reference v3 parity fixture.

This is the public, committable sibling of the PRIVATE dump_parity_golden.py
(which lives in the emo-bench repo and reads a private 60-event personal
corpus + real MLX bge-m3 embeddings). Neither the private corpus nor the
private embedder can run in a clean public CI checkout, so the private golden
stays gitignored and the Go parity test SKIPs.

This script instead:

  1. Defines a SMALL, FULLY SYNTHETIC, PUBLIC corpus (fabricated events +
     fabricated user states — ZERO personal/private data). It is hand-built to
     exercise every v3 boost term: emotion, state(depletion/restoration),
     anchor(user_flag), date(temporal-keyword + explicit snapshot), chain
     expansion, and the v2_pure-collapse property (neutral core query).

  2. Injects DETERMINISTIC SYNTHETIC EMBEDDINGS by monkeypatching the
     reference engine's embed_cohere_or_alt() to a fixed hash-seeded unit
     vector per text. The parity test consumes frozen vectors as opaque
     inputs (it never re-embeds), so a fabricated-but-deterministic embedder
     is faithful to what the gate actually checks: the SCORING formula.

  3. Runs the REAL reference engine (RetrievalV3 from retrieval_v3.py) on this
     public corpus and dumps the golden the same way the private dumper does.
     The golden is therefore produced by the genuine reference scorer, not a
     re-implementation — so the Go test asserting against it is a real
     Go==reference parity check, just on public data.

Run (from a checkout that can see the reference engine):

    REF_DIR=/path/to/emo-bench/bench/external-evals/scripts \\
        python3 gen_parity_public.py

REF_DIR defaults to the local emo-bench path. The output (parity_golden_public.json
and parity_corpus_public.json) is written next to this script and IS committed.
Regenerating requires the reference engine; the committed fixture does not.
"""
from __future__ import annotations

import hashlib
import json
import os
import sys
from pathlib import Path

import numpy as np

HERE = Path(__file__).resolve().parent

# Locate the reference engine (private repo). Override with REF_DIR.
REF_DIR = Path(
    os.environ.get(
        "REF_DIR",
        "/Users/nikshilov/dev/ai/Garden/emo-bench/bench/external-evals/scripts",
    )
)

EMBED_DIM = 16  # tiny synthetic embedding; dim is irrelevant to the formula.


def synth_vec(text: str) -> list[float]:
    """Deterministic unit vector from text. Stable across runs and machines:
    seeded purely by a sha256 of the text, normalized. No MLX, no network."""
    h = hashlib.sha256(text.encode("utf-8")).digest()
    rng = np.random.RandomState(int.from_bytes(h[:4], "big"))
    v = rng.standard_normal(EMBED_DIM).astype(np.float32)
    n = float(np.linalg.norm(v))
    if n < 1e-9:
        v[0] = 1.0
        n = 1.0
    return [float(x) for x in (v / n)]


# ── Public synthetic corpus (fabricated; zero personal data) ─────────────────
# Plutchik-10 keys: joy sadness anger fear trust disgust anticipation surprise
# shame guilt. days_ago drives recency + anchor decay. user_flag=True => anchor.
# predecessor_ids => chain edges. biometric_snapshot => state_fit on stressed/
# restored queries.
CORPUS_EVENTS = [
    {
        "id": 1,
        "text": "shipped the v1 release after months of work, felt proud and complete",
        "sentiment_label": "milestone",
        "user_flag": True,  # structural anchor
        "days_ago": 120,
        "emotion_tags": {"joy": 0.8, "anticipation": 0.4, "trust": 0.3},
        "predecessor_ids": [],
        "biometric_snapshot": {"hrv": 82, "sleep_quality": 0.8, "workout": True},
    },
    {
        "id": 2,
        "text": "long overload week, anxious sleep and declining recovery, body worn down",
        "sentiment_label": "burden",
        "user_flag": False,
        "days_ago": 5,
        "emotion_tags": {"sadness": 0.6, "fear": 0.5},
        "predecessor_ids": [],
        "biometric_snapshot": {"hrv": 48, "sleep_quality": 0.3, "stress_proxy": 0.7,
                               "hrv_trend": "declining_3d"},
    },
    {
        "id": 3,
        "text": "calm morning, rested and steady, post-workout glow",
        "sentiment_label": "repair",
        "user_flag": False,
        "days_ago": 2,
        "emotion_tags": {"joy": 0.5, "trust": 0.6},
        "predecessor_ids": [],
        "biometric_snapshot": {"hrv": 88, "sleep_quality": 0.9, "stress_proxy": 0.2,
                               "workout": True},
    },
    {
        "id": 4,
        "text": "a quiet neutral note about the weather and groceries",
        "sentiment_label": "neutral",
        "user_flag": False,
        "days_ago": 30,
        "emotion_tags": {},
        "predecessor_ids": [],
        "biometric_snapshot": {},
    },
    {
        "id": 5,
        "text": "started the project, full of hope and anticipation for what it could become",
        "sentiment_label": "origin",
        "user_flag": True,  # anchor, chain root
        "days_ago": 200,
        "emotion_tags": {"anticipation": 0.7, "joy": 0.4},
        "predecessor_ids": [],
        "biometric_snapshot": {},
    },
    {
        "id": 6,
        "text": "hit a hard wall mid-project, frustrated and stuck for days",
        "sentiment_label": "wound",
        "user_flag": False,
        "days_ago": 150,
        "emotion_tags": {"anger": 0.6, "sadness": 0.4},
        "predecessor_ids": [5],
        "biometric_snapshot": {},
    },
    {
        "id": 7,
        "text": "pushed through the wall and finally found a path forward, relieved",
        "sentiment_label": "milestone",
        "user_flag": False,
        "days_ago": 130,
        "emotion_tags": {"joy": 0.6, "trust": 0.5, "surprise": 0.3},
        "predecessor_ids": [6],
        "biometric_snapshot": {},
    },
    {
        "id": 8,
        "text": "a recent small win this week, quietly satisfying",
        "sentiment_label": "milestone",
        "user_flag": False,
        "days_ago": 3,
        "emotion_tags": {"joy": 0.5},
        "predecessor_ids": [],
        "biometric_snapshot": {},
    },
    {
        "id": 9,
        "text": "felt scared and anxious before the big talk, fear of failing publicly",
        "sentiment_label": "burden",
        "user_flag": False,
        "days_ago": 20,
        "emotion_tags": {"fear": 0.8, "sadness": 0.3},
        "predecessor_ids": [],
        "biometric_snapshot": {},
    },
    {
        "id": 10,
        "text": "old structural truth about how the system is meant to work, foundational",
        "sentiment_label": "origin",
        "user_flag": True,  # anchor
        "days_ago": 300,
        "emotion_tags": {"trust": 0.6},
        "predecessor_ids": [],
        "biometric_snapshot": {},
    },
]

# ── Public synthetic tests (fabricated states) ───────────────────────────────
# user_state mirrors the bench test schema (user_state dict + biometric_snapshot
# overlay). build_user_state in the reference merges them.
CORPUS_TESTS = [
    {
        "id": "P1",
        "name": "core neutral query (v2_pure collapse)",
        "test_type": "core",
        "user_query": "tell me about the project work",
        # no state, no temporal keyword -> every boost collapses to 1.0
    },
    {
        "id": "P2",
        "name": "core query with temporal keyword (date boost only)",
        "test_type": "core",
        "user_query": "what happened this week",
        # 'this week' -> implicit date_ref=3d; emotion/state/anchor stay 1.0
    },
    {
        "id": "P3",
        "name": "stateful: body stressed -> depletion events boosted",
        "test_type": "stateful",
        "user_query": "how am I doing",
        "user_state": {
            "mood_vector": {"sadness": 0.6, "fear": 0.5},
            "stress_proxy": 0.75,
            "sleep_quality": 0.3,
            "hrv_trend": "declining_3d",
        },
    },
    {
        "id": "P4",
        "name": "stateful: body restored -> restoration events boosted",
        "test_type": "stateful",
        "user_query": "how am I doing",
        "user_state": {
            "mood_vector": {"joy": 0.6, "trust": 0.5},
            "stress_proxy": 0.2,
            "sleep_quality": 0.85,
        },
    },
    {
        "id": "P5",
        "name": "multi_signal: dominant fear emotion + explicit snapshot date",
        "test_type": "multi_signal",
        "user_query": "remind me about the hard moment",
        "user_state": {
            "mood_vector": {"fear": 0.8, "sadness": 0.3},
            "snapshot_days_ago": 20,
        },
    },
    {
        "id": "P6",
        "name": "multi_signal: dominant joy emotion, no date",
        "test_type": "multi_signal",
        "user_query": "show me a good memory",
        "user_state": {
            "mood_vector": {"joy": 0.8, "trust": 0.4},
        },
    },
    {
        "id": "P7",
        "name": "chain: expand the project causal chain",
        "test_type": "chain",
        "user_query": "walk me through the project story",
    },
    {
        "id": "P8",
        "name": "stateful no-emotion: stressed body, neutral mood",
        "test_type": "stateful",
        "user_query": "any context on the rough patch",
        "user_state": {
            "stress_proxy": 0.7,
        },
    },
]

BETA = 0.15
GAMMA = 0.15
USE_LLM_QUERY_EMO = False
TOP_K_DUMP = 5


def main() -> None:
    if not (REF_DIR / "retrieval_v3.py").exists():
        raise SystemExit(
            f"reference engine not found at {REF_DIR}/retrieval_v3.py — set REF_DIR. "
            f"(The committed fixture does not need this; only regeneration does.)"
        )
    sys.path.insert(0, str(REF_DIR))

    import retrieval_v3 as rv3  # noqa: E402

    # ── Inject deterministic synthetic embeddings into the REAL engine ───────
    # The engine calls embed_cohere_or_alt(texts, input_type) for both events
    # and queries. We replace it with the synthetic, deterministic embedder so
    # no MLX/Cohere/network is needed and the golden is reproducible anywhere.
    def fake_embed(texts, input_type):  # noqa: ARG001
        return np.array([synth_vec(t) for t in texts], dtype=np.float32)

    rv3.embed_cohere_or_alt = fake_embed
    # RetrievalV3 captured the name at import time inside methods? It calls the
    # module-global, so patching the module attr is sufficient. Verify:
    assert rv3.embed_cohere_or_alt is fake_embed

    UserState = rv3.UserState
    EMOTION_QUERY_HINTS = rv3.EMOTION_QUERY_HINTS

    def build_user_state(user_state, biometric_snapshot):
        """Replicate the private dumper's merge (PulseV3Adapter.retrieve)."""
        merged = {}
        if user_state:
            merged.update(user_state)
        if biometric_snapshot:
            for k, v in biometric_snapshot.items():
                if k in ("hrv", "sleep_quality", "sleep_hours", "hr_trend",
                         "hrv_trend", "stress_proxy", "time_of_day"):
                    merged[k] = v
        mood_vector = merged.pop("mood_vector", {})
        has_signal = mood_vector or any(
            merged.get(k) is not None
            for k in ("sleep_quality", "hrv", "hr_trend", "hrv_trend", "stress_proxy")
        )
        if not has_signal:
            return None
        return UserState(
            mood_vector=mood_vector,
            sleep_quality=merged.get("sleep_quality"),
            sleep_hours=merged.get("sleep_hours"),
            hrv=merged.get("hrv"),
            hr_trend=merged.get("hr_trend"),
            hrv_trend=merged.get("hrv_trend"),
            stress_proxy=merged.get("stress_proxy"),
            recent_life_events_7d=merged.get("recent_life_events_7d", []),
            time_of_day=merged.get("time_of_day"),
            snapshot_days_ago=merged.get("snapshot_days_ago"),
        )

    def compute_effective_query(query, state):
        eq = query
        if state and state.mood_vector:
            dom_ok, _, dom_key = state.has_dominant_emotion(0.5)
            if dom_ok:
                hint = EMOTION_QUERY_HINTS.get(dom_key)
                if hint:
                    eq = f"{query} {hint}"
        return eq

    engine = rv3.RetrievalV3(
        CORPUS_EVENTS,
        beta=BETA,
        gamma=GAMMA,
        use_llm_query_emo=USE_LLM_QUERY_EMO,
    )

    event_embeddings = {}
    for eid, vec in zip(engine._ids, engine._event_vecs):
        event_embeddings[str(eid)] = [float(x) for x in vec]

    n_events = len(CORPUS_EVENTS)
    all_ids = list(engine._ids)

    dumped_tests = []
    for t in CORPUS_TESTS:
        tid = t["id"]
        query = t["user_query"]
        user_state = t.get("user_state")
        biometric = t.get("biometric_snapshot")
        is_chain = t.get("test_type") == "chain"

        state = build_user_state(user_state, biometric)
        effective_query = compute_effective_query(query, state)
        # Query vector = the SAME synthetic embedding the engine used internally
        # (engine.retrieve re-embeds effective_query via the monkeypatched fn).
        query_vec = synth_vec(effective_query)

        scored = engine.retrieve(query, user_state=state, top_k=n_events,
                                 return_scores=True)
        score_by_id = {str(eid): float(s) for eid, s in scored}
        for eid in all_ids:
            score_by_id.setdefault(str(eid), 0.0)

        if is_chain:
            top5 = engine.retrieve(query, user_state=state, top_k=TOP_K_DUMP,
                                   expand_chain=True)
        else:
            top5 = engine.retrieve(query, user_state=state, top_k=TOP_K_DUMP)

        if state is None:
            state_dump = None
        else:
            state_dump = {
                "mood_vector": dict(state.mood_vector),
                "sleep_quality": state.sleep_quality,
                "sleep_hours": state.sleep_hours,
                "hrv": state.hrv,
                "hr_trend": state.hr_trend,
                "hrv_trend": state.hrv_trend,
                "stress_proxy": state.stress_proxy,
                "recent_life_events_7d": list(state.recent_life_events_7d),
                "time_of_day": state.time_of_day,
                "snapshot_days_ago": state.snapshot_days_ago,
            }

        dumped_tests.append({
            "test_id": tid,
            "test_name": t.get("name"),
            "test_type": t.get("test_type"),
            "query": query,
            "effective_query": effective_query,
            "hint_applied": effective_query != query,
            "query_vector": [float(x) for x in query_vec],
            "user_state": state_dump,
            "biometric": biometric,
            "expand_chain": is_chain,
            "scores": score_by_id,
            "top5": [int(x) for x in top5],
        })

    corpus_out = HERE / "parity_corpus_public.json"
    golden_out = HERE / "parity_golden_public.json"

    # Corpus file: the Go test's loadCorpus reads _meta.source_corpus. We write
    # a relative path so it resolves inside the repo regardless of machine.
    corpus_out.write_text(
        json.dumps({"events": CORPUS_EVENTS}, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )

    golden = {
        "_meta": {
            "source_corpus": "testdata/parity_corpus_public.json",
            "embedding_provider": "synthetic-sha256-unit",
            "embedding_dim": EMBED_DIM,
            "beta": BETA,
            "gamma": GAMMA,
            "use_llm_query_emo": USE_LLM_QUERY_EMO,
            "delta_anchor": engine.delta_anchor,
            "delta_date": engine.delta_date,
            "anchor_top_n": engine.anchor_top_n,
            "decay_lambda": engine.decay_lambda,
            "decay_lambda_anchor": engine.decay_lambda_anchor,
            "n_events": n_events,
            "n_tests": len(CORPUS_TESTS),
            "top_k_dump": TOP_K_DUMP,
            "public": True,
            "note": (
                "PUBLIC synthetic Go==reference v3 parity fixture. Fabricated "
                "events + states (zero personal data). Embeddings are "
                "deterministic sha256-seeded unit vectors (no MLX/Cohere). "
                "Scores produced by the REAL reference engine retrieval_v3.py "
                "(embed_cohere_or_alt monkeypatched to the synthetic embedder). "
                "Regenerate with testdata/gen_parity_public.py + REF_DIR."
            ),
        },
        "event_embeddings": event_embeddings,
        "tests": dumped_tests,
    }
    golden_out.write_text(
        json.dumps(golden, ensure_ascii=False, indent=2), encoding="utf-8"
    )

    print(f"WROTE {golden_out}  ({golden_out.stat().st_size} bytes)")
    print(f"WROTE {corpus_out}  ({corpus_out.stat().st_size} bytes)")
    print(f"  n_events={n_events}  n_tests={len(CORPUS_TESTS)}  dim={EMBED_DIM}")
    for s in dumped_tests:
        top5_scores = [(eid, round(s["scores"][str(eid)], 6)) for eid in s["top5"]]
        print(f"  {s['test_id']:>3} [{s['test_type']:<12}] hint={s['hint_applied']!s:<5} "
              f"top5={s['top5']} scores={top5_scores}")


if __name__ == "__main__":
    main()
