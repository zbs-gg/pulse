-- Session lineage tree
CREATE TABLE sessions (
    id                   INTEGER PRIMARY KEY,
    parent_session_id    INTEGER REFERENCES sessions(id),
    lineage_root_id      INTEGER REFERENCES sessions(id),
    name                 TEXT NOT NULL,
    status               TEXT NOT NULL DEFAULT 'active',
    summary_markdown     TEXT,
    summary_json         TEXT,
    memory_snapshot_hash TEXT NOT NULL,
    capsule_snapshot     TEXT,
    token_count          INTEGER,
    compaction_depth     INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL,
    compacted_at         TEXT,
    ended_at             TEXT
);
CREATE INDEX idx_sessions_parent ON sessions(parent_session_id);
CREATE INDEX idx_sessions_root   ON sessions(lineage_root_id, created_at);
CREATE INDEX idx_sessions_status ON sessions(status);

-- Conversation messages (bound to session from day 1)
CREATE TABLE messages (
    id                   INTEGER PRIMARY KEY,
    session_id           INTEGER REFERENCES sessions(id),
    chat_id              INTEGER NOT NULL,
    telegram_message_id  INTEGER,
    role                 TEXT NOT NULL CHECK(role IN ('user','assistant','system','compaction_marker')),
    text                 TEXT NOT NULL,
    kind                 TEXT NOT NULL DEFAULT 'text',  -- text | voice_transcript | photo_desc | tool_output
    token_count          INTEGER,
    is_compaction_marker INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL
);
CREATE INDEX idx_messages_session ON messages(session_id, created_at);

-- Frozen MEMORY.md snapshots (hash-deduped)
CREATE TABLE memory_snapshots (
    hash           TEXT PRIMARY KEY,
    content        TEXT NOT NULL,
    char_count     INTEGER NOT NULL,
    capacity_bytes INTEGER NOT NULL DEFAULT 2200,
    created_at     TEXT NOT NULL
);

-- Audit trail for compaction events (empty in M1, used in M2)
CREATE TABLE compaction_events (
    id                  INTEGER PRIMARY KEY,
    session_id          INTEGER NOT NULL REFERENCES sessions(id),
    child_session_id    INTEGER REFERENCES sessions(id),
    tokens_before       INTEGER NOT NULL,
    tokens_after        INTEGER NOT NULL,
    trigger             TEXT NOT NULL,
    previous_summary    TEXT,
    haiku_tokens_in     INTEGER,
    haiku_tokens_out    INTEGER,
    haiku_cost_usd      REAL,
    promoted_memory_ids TEXT,
    created_at          TEXT NOT NULL
);

-- Pending memory promotions from compactions (empty in M1)
CREATE TABLE pending_promotions (
    id            INTEGER PRIMARY KEY,
    session_id    INTEGER NOT NULL REFERENCES sessions(id),
    proposed_text TEXT NOT NULL,
    proposed_kind TEXT NOT NULL,
    importance    REAL NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    created_at    TEXT NOT NULL,
    resolved_at   TEXT
);

-- FTS5 virtual table for session-level keyword recall
CREATE VIRTUAL TABLE sessions_fts USING fts5(
    name,
    summary_markdown,
    content='sessions',
    content_rowid='id'
);

CREATE TRIGGER sessions_fts_ai AFTER INSERT ON sessions BEGIN
  INSERT INTO sessions_fts(rowid, name, summary_markdown)
    VALUES (new.id, new.name, COALESCE(new.summary_markdown, ''));
END;
CREATE TRIGGER sessions_fts_ad AFTER DELETE ON sessions BEGIN
  INSERT INTO sessions_fts(sessions_fts, rowid, name, summary_markdown)
    VALUES ('delete', old.id, old.name, COALESCE(old.summary_markdown, ''));
END;
CREATE TRIGGER sessions_fts_au AFTER UPDATE ON sessions BEGIN
  INSERT INTO sessions_fts(sessions_fts, rowid, name, summary_markdown)
    VALUES ('delete', old.id, old.name, COALESCE(old.summary_markdown, ''));
  INSERT INTO sessions_fts(rowid, name, summary_markdown)
    VALUES (new.id, new.name, COALESCE(new.summary_markdown, ''));
END;
