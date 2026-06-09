-- 013_event_embeddings.sql
-- Event-level dense vector embeddings — the retrieval primary index.
--
-- Rationale (bench):
--   - v1 entity-level keyword-BFS retrieval fell through to a static salience
--     fallback on the large majority of conversational bursts (near-identical
--     top-3 anchors regardless of query).
--   - v2_pure (event-level cosine + light recency, α=0) outperformed the
--     entity-graph baseline on an empathic-retrieval subset.
--   - Winning path = embed events directly, cosine retrieve, light recency decay
--     (λ=0.001, half-life ~700d), no sentiment amplifier, no anchor boost.
--
-- vector stored as JSON array of floats — same pattern as entity_embeddings (011).
-- At small scale (<100k events) naive cosine in Python is fast enough.
CREATE TABLE IF NOT EXISTS event_embeddings (
    event_id     INTEGER PRIMARY KEY,
    model        TEXT NOT NULL,
    dim          INTEGER NOT NULL,
    vector_json  TEXT NOT NULL,
    text_source  TEXT NOT NULL,       -- the exact text that was embedded (title+description)
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_event_embeddings_model ON event_embeddings(model);
