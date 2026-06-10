-- 011_embeddings.sql
-- Dense vector embeddings for entities. One row per entity that has been embedded.
-- vector stored as JSON array of floats (simple, portable, no sqlite-vec dependency).
-- At small scale (<10k entities) naive cosine in Python is fast enough; migrate to
-- sqlite-vec when N grows.
CREATE TABLE IF NOT EXISTS entity_embeddings (
    entity_id    INTEGER PRIMARY KEY,
    model        TEXT NOT NULL,
    dim          INTEGER NOT NULL,
    vector_json  TEXT NOT NULL,
    text_source  TEXT NOT NULL,
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    FOREIGN KEY (entity_id) REFERENCES entities(id) ON DELETE CASCADE
);
