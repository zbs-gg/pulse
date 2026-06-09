CREATE TABLE memory_capsules (
    id                   TEXT PRIMARY KEY,
    schema_version       TEXT NOT NULL,
    source_host          TEXT NOT NULL,
    conversation_scope   TEXT NOT NULL,
    source_timestamp     TEXT NOT NULL,
    kind                 TEXT NOT NULL,
    redacted_summary     TEXT NOT NULL,
    confidence           REAL NOT NULL,
    evidence_hint        TEXT NOT NULL,
    privacy_tier         TEXT NOT NULL,
    retention            TEXT NOT NULL,
    tags                 TEXT NOT NULL DEFAULT '[]',
    created_at           TEXT NOT NULL
);

CREATE INDEX idx_memory_capsules_kind ON memory_capsules(kind);
CREATE INDEX idx_memory_capsules_privacy ON memory_capsules(privacy_tier);
CREATE INDEX idx_memory_capsules_retention ON memory_capsules(retention);
CREATE INDEX idx_memory_capsules_created ON memory_capsules(created_at);

CREATE VIRTUAL TABLE memory_capsules_fts USING fts5(
    redacted_summary,
    tags,
    content='memory_capsules',
    content_rowid='rowid',
    tokenize="unicode61 remove_diacritics 2"
);

CREATE TRIGGER memory_capsules_fts_ai AFTER INSERT ON memory_capsules BEGIN
    INSERT INTO memory_capsules_fts(rowid, redacted_summary, tags)
    VALUES (new.rowid, new.redacted_summary, new.tags);
END;

CREATE TRIGGER memory_capsules_fts_ad AFTER DELETE ON memory_capsules BEGIN
    INSERT INTO memory_capsules_fts(memory_capsules_fts, rowid, redacted_summary, tags)
    VALUES ('delete', old.rowid, old.redacted_summary, old.tags);
END;

CREATE TRIGGER memory_capsules_fts_au AFTER UPDATE ON memory_capsules BEGIN
    INSERT INTO memory_capsules_fts(memory_capsules_fts, rowid, redacted_summary, tags)
    VALUES ('delete', old.rowid, old.redacted_summary, old.tags);
    INSERT INTO memory_capsules_fts(rowid, redacted_summary, tags)
    VALUES (new.rowid, new.redacted_summary, new.tags);
END;
