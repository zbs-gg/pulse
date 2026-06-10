-- 020_fts5_lexical: FTS5 indexes for hybrid retrieval (BM25 + cosine).
--
-- bge-m3 cosine alone misses on abstract queries — e.g. a paraphrased
-- query can map to a semantically-near event instead of the actual
-- canonical event that shares the exact phrase. Lexical BM25 catches the
-- exact-string match that the embedding rounds off.
--
-- Three independent FTS5 tables (events / facts / entities). Triggers
-- keep them in sync with the source tables. Tokenizer = unicode61
-- with diacritic removal — works for Russian word boundaries; we lose
-- morphology (рана vs раны), but that's good enough for v1 — the
-- query expander (Qwen3 local) will fan out variants.

-- ---------- events ----------
CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
    title, description,
    content='events', content_rowid='id',
    tokenize="unicode61 remove_diacritics 2"
);

CREATE TRIGGER IF NOT EXISTS events_fts_ai AFTER INSERT ON events BEGIN
    INSERT INTO events_fts(rowid, title, description)
    VALUES (new.id, new.title, COALESCE(new.description, ''));
END;
CREATE TRIGGER IF NOT EXISTS events_fts_ad AFTER DELETE ON events BEGIN
    INSERT INTO events_fts(events_fts, rowid, title, description)
    VALUES ('delete', old.id, old.title, COALESCE(old.description, ''));
END;
CREATE TRIGGER IF NOT EXISTS events_fts_au AFTER UPDATE ON events BEGIN
    INSERT INTO events_fts(events_fts, rowid, title, description)
    VALUES ('delete', old.id, old.title, COALESCE(old.description, ''));
    INSERT INTO events_fts(rowid, title, description)
    VALUES (new.id, new.title, COALESCE(new.description, ''));
END;

-- ---------- facts ----------
CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(
    text,
    content='facts', content_rowid='id',
    tokenize="unicode61 remove_diacritics 2"
);

CREATE TRIGGER IF NOT EXISTS facts_fts_ai AFTER INSERT ON facts BEGIN
    INSERT INTO facts_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS facts_fts_ad AFTER DELETE ON facts BEGIN
    INSERT INTO facts_fts(facts_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER IF NOT EXISTS facts_fts_au AFTER UPDATE ON facts BEGIN
    INSERT INTO facts_fts(facts_fts, rowid, text) VALUES ('delete', old.id, old.text);
    INSERT INTO facts_fts(rowid, text) VALUES (new.id, new.text);
END;

-- ---------- entities ----------
CREATE VIRTUAL TABLE IF NOT EXISTS entities_fts USING fts5(
    canonical_name, aliases,
    content='entities', content_rowid='id',
    tokenize="unicode61 remove_diacritics 2"
);

CREATE TRIGGER IF NOT EXISTS entities_fts_ai AFTER INSERT ON entities BEGIN
    INSERT INTO entities_fts(rowid, canonical_name, aliases)
    VALUES (new.id, new.canonical_name, COALESCE(new.aliases, ''));
END;
CREATE TRIGGER IF NOT EXISTS entities_fts_ad AFTER DELETE ON entities BEGIN
    INSERT INTO entities_fts(entities_fts, rowid, canonical_name, aliases)
    VALUES ('delete', old.id, old.canonical_name, COALESCE(old.aliases, ''));
END;
CREATE TRIGGER IF NOT EXISTS entities_fts_au AFTER UPDATE ON entities BEGIN
    INSERT INTO entities_fts(entities_fts, rowid, canonical_name, aliases)
    VALUES ('delete', old.id, old.canonical_name, COALESCE(old.aliases, ''));
    INSERT INTO entities_fts(rowid, canonical_name, aliases)
    VALUES (new.id, new.canonical_name, COALESCE(new.aliases, ''));
END;

-- ---------- backfill existing rows ----------
-- External-content FTS5 ignores direct INSERTs and reads from the
-- source table on 'rebuild'. So we just trigger a rebuild — it scans
-- events / facts / entities and (re)builds the index. Idempotent.
INSERT INTO events_fts(events_fts) VALUES ('rebuild');
INSERT INTO facts_fts(facts_fts) VALUES ('rebuild');
INSERT INTO entities_fts(entities_fts) VALUES ('rebuild');
