-- 012_graph_snapshots.sql
-- Reversible log of every graph mutation caused by extraction. Enables rewind
-- of a specific observation's effects without global backup/restore.
CREATE TABLE IF NOT EXISTS graph_snapshots (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    observation_id INTEGER NOT NULL,
    op             TEXT NOT NULL,      -- 'insert_entity', 'update_entity', 'insert_relation', 'update_relation', 'insert_event', 'insert_event_entity', 'insert_fact', 'insert_evidence', 'insert_entity_merge_proposal', 'insert_open_question'
    table_name     TEXT NOT NULL,      -- target table
    row_id         INTEGER,            -- the PK of the affected row (NULL for event_entities junction which has no single pk)
    before_json    TEXT,               -- NULL for insert ops, JSON of row-before-update for update ops
    after_json     TEXT NOT NULL,      -- JSON of what was inserted or updated to
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE INDEX IF NOT EXISTS idx_graph_snapshots_obs ON graph_snapshots(observation_id);
CREATE INDEX IF NOT EXISTS idx_graph_snapshots_created ON graph_snapshots(created_at);
