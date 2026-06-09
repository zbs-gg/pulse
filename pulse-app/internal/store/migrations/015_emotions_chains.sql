-- 015_emotions_chains.sql
-- Emotion tags + causal chains for Pulse v3 conditional retrieval.
--
-- Rationale: the conditional emotion/state/chain boosts gate on these signals;
-- on an empathic-memory benchmark the conditional v3 stack improved stateful and
-- chain retrieval over an always-on baseline without regressing factual recall.
--
-- Two new tables enable v3's three conditional boosts:
--   1. event_emotions: 10-dim Plutchik vector per event (joy/sadness/anger/fear/
--      trust/disgust/anticipation/surprise/shame/guilt) for the emotion_alignment
--      boost — active only when query has dominant emotion (max ≥ 0.5).
--   2. event_chains: directed edges for causal/temporal chain retrieval —
--      active only for chain queries, traversed via BFS in retrieval_v3.go.
--
-- Conditional gating is the core lesson from Phase D negative result (2026-04-20):
-- always-on multiplicative emotion term HURT retrieval monotonically. v3 fixes
-- this by gating each boost on its own signal being genuine.

-- Plutchik-10 emotion vector per event.
-- Populated by emotion_classifier.py (Qwen Max tagger) or manual curation.
-- Zero-vector is the default; a zero-vector event gets no emotion boost (safe).
CREATE TABLE IF NOT EXISTS event_emotions (
    event_id       INTEGER PRIMARY KEY,
    joy            REAL NOT NULL DEFAULT 0,
    sadness        REAL NOT NULL DEFAULT 0,
    anger          REAL NOT NULL DEFAULT 0,
    fear           REAL NOT NULL DEFAULT 0,
    trust          REAL NOT NULL DEFAULT 0,
    disgust        REAL NOT NULL DEFAULT 0,
    anticipation   REAL NOT NULL DEFAULT 0,
    surprise       REAL NOT NULL DEFAULT 0,
    shame          REAL NOT NULL DEFAULT 0,
    guilt          REAL NOT NULL DEFAULT 0,
    tagger         TEXT NOT NULL,       -- "qwen-k3-max" | "manual" | "kimi-k2.6"
    tagger_version TEXT,                 -- model version for reproducibility
    confidence     REAL NOT NULL DEFAULT 1.0,
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
    CHECK (joy          BETWEEN 0 AND 1),
    CHECK (sadness      BETWEEN 0 AND 1),
    CHECK (anger        BETWEEN 0 AND 1),
    CHECK (fear         BETWEEN 0 AND 1),
    CHECK (trust        BETWEEN 0 AND 1),
    CHECK (disgust      BETWEEN 0 AND 1),
    CHECK (anticipation BETWEEN 0 AND 1),
    CHECK (surprise     BETWEEN 0 AND 1),
    CHECK (shame        BETWEEN 0 AND 1),
    CHECK (guilt        BETWEEN 0 AND 1)
);

-- Direct edges for causal/temporal chains.
-- parent_id is the causal ancestor; child_id is the descendant.
-- strength captures edge confidence (0-1); used for future weighted BFS.
-- Example: early_setback (22) → coping_pattern (31) → present_behavior (32).
CREATE TABLE IF NOT EXISTS event_chains (
    parent_id  INTEGER NOT NULL,
    child_id   INTEGER NOT NULL,
    strength   REAL NOT NULL DEFAULT 1.0,
    kind       TEXT NOT NULL DEFAULT 'causal' CHECK(kind IN ('causal','temporal','thematic')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    PRIMARY KEY (parent_id, child_id),
    FOREIGN KEY (parent_id) REFERENCES events(id) ON DELETE CASCADE,
    FOREIGN KEY (child_id)  REFERENCES events(id) ON DELETE CASCADE,
    CHECK (strength BETWEEN 0 AND 1),
    CHECK (parent_id != child_id)
);
CREATE INDEX IF NOT EXISTS idx_chains_parent ON event_chains(parent_id);
CREATE INDEX IF NOT EXISTS idx_chains_child  ON event_chains(child_id);
CREATE INDEX IF NOT EXISTS idx_chains_kind   ON event_chains(kind);

-- Precomputed query-emotion cache for hot queries (optional, populated by retrieval_v3).
-- Keyed by SHA256 of the query text (first 16 hex chars) to avoid unbounded growth.
CREATE TABLE IF NOT EXISTS query_emotion_cache (
    query_hash   TEXT PRIMARY KEY,
    query_text   TEXT NOT NULL,
    joy          REAL NOT NULL DEFAULT 0,
    sadness      REAL NOT NULL DEFAULT 0,
    anger        REAL NOT NULL DEFAULT 0,
    fear         REAL NOT NULL DEFAULT 0,
    trust        REAL NOT NULL DEFAULT 0,
    disgust      REAL NOT NULL DEFAULT 0,
    anticipation REAL NOT NULL DEFAULT 0,
    surprise     REAL NOT NULL DEFAULT 0,
    shame        REAL NOT NULL DEFAULT 0,
    guilt        REAL NOT NULL DEFAULT 0,
    inferred_by  TEXT NOT NULL,          -- "qwen-k3-max" | "keyword_fallback"
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_qemo_created ON query_emotion_cache(created_at);
