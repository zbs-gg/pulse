-- 017_atomic_facts.sql
-- Phase G — Atomic facts extracted per-event for hybrid retrieval.
--
-- Rationale (2026-04-26 bench v3 result):
--   Pulse v3 sessions-only beat Mem0+custom on stateful (×2.25) and multi-signal
--   (×1.43), but lost on factual recall (core 26.7% vs Mem0+custom 53.3%).
--   Mem0's atomic fact extraction collapses long events into crisp claims that
--   match cosine similarity better on factual queries.
--
--   Phase G adds Mem0-style atomic_facts table to Pulse, indexed separately
--   from event embeddings, used in `factual` retrieval mode. Pulse keeps BOTH
--   full session text (for empathic) AND atomic facts (for factual recall) —
--   this dual-index is the differentiator vs Mem0 which loses session context.
--
--   Bench result: Pulse hybrid R@3 24.8% (vs v3 21.9% +2.9pp, vs Mem0+custom
--   18.1% +6.7pp) with core climbing 26.7→40.0% while stateful held 30→36.7%.
--
-- DISTINCT from migration 005's `facts` table (entity-scoped, salience-driven).
-- This table is event-scoped, retrieval-driven, with denormalized emotion+anchor
-- inheritance from the parent event for query-time speed.

-- Atomic facts — one row per extracted fact, linked to a parent event.
-- Inheritance fields (joy..guilt, is_anchor, biometric_json) are denormalized
-- from event_emotions / events.user_flag for index-free retrieval lookup.
CREATE TABLE IF NOT EXISTS atomic_facts (
    id              INTEGER PRIMARY KEY,
    event_id        INTEGER NOT NULL,
    text            TEXT NOT NULL,
    text_hash       TEXT NOT NULL,           -- MD5 first 16 hex; per-event dedup
    attributed_to   TEXT,                    -- "user", entity name, or "self"
    is_anchor       INTEGER NOT NULL DEFAULT 0 CHECK(is_anchor IN (0,1)),
    -- Plutchik-10 vector (denormalized from event_emotions for query speed)
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
    -- Biometric snapshot at extraction time (JSON; nullable)
    biometric_json TEXT,
    -- Provenance
    extractor       TEXT NOT NULL,           -- "gpt-4o-mini" | "claude-haiku-4-5" | "manual"
    extractor_version TEXT,
    confidence      REAL NOT NULL DEFAULT 1.0 CHECK(confidence BETWEEN 0 AND 1),
    extracted_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE,
    UNIQUE(event_id, text_hash),             -- per-event dedup; same fact text
                                              -- across distinct events is allowed
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
CREATE INDEX IF NOT EXISTS idx_atomic_facts_event ON atomic_facts(event_id);
CREATE INDEX IF NOT EXISTS idx_atomic_facts_anchor ON atomic_facts(is_anchor)
    WHERE is_anchor = 1;
CREATE INDEX IF NOT EXISTS idx_atomic_facts_attr ON atomic_facts(attributed_to);

-- Embeddings stored separately (mirrors event_embeddings pattern from 013).
-- Lets us re-embed facts without touching extraction provenance.
CREATE TABLE IF NOT EXISTS atomic_fact_embeddings (
    fact_id      INTEGER PRIMARY KEY,
    model        TEXT NOT NULL,                -- "embed-v4.0" | future model
    dim          INTEGER NOT NULL,
    vector_json  TEXT NOT NULL,                -- JSON array of floats
    text_source  TEXT NOT NULL,                -- which fact.text variant was embedded
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    FOREIGN KEY (fact_id) REFERENCES atomic_facts(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_atomic_fact_embeddings_model
    ON atomic_fact_embeddings(model);

-- Query route cache (heuristic + LLM verdicts, mirrors query_emotion_cache from 015).
-- Used by retrieval/router.go to avoid re-classifying identical queries.
CREATE TABLE IF NOT EXISTS query_route_cache (
    query_hash   TEXT PRIMARY KEY,             -- SHA256 first 16 hex
    query_text   TEXT NOT NULL,
    mode         TEXT NOT NULL CHECK(mode IN ('factual','empathic','chain')),
    confidence   REAL NOT NULL CHECK(confidence BETWEEN 0 AND 1),
    classifier   TEXT NOT NULL,                -- "heuristic" | "claude-haiku-4-5" | "qwen3-max"
    reasoning    TEXT,                          -- short trace for debugging
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_query_route_cache_created
    ON query_route_cache(created_at);
