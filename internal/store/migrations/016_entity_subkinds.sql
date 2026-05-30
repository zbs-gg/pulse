-- 016_entity_subkinds.sql
-- Expand entity.kind for AI/persona and fiction-boundary modeling.
--
-- Why:
--   Pulse needs to distinguish real people, AI companions/personas,
--   fictional characters, fictionalized selves, narrative devices, and safety
--   boundaries. Canonical names alone are too fragile for this: a character
--   in a book, a same-named companion persona, and a real person of the same
--   name must not collapse into one graph node.
--
-- SQLite cannot ALTER a CHECK constraint directly. Rebuilding `entities` is
-- unsafe here because many tables reference it and migrations run inside a
-- transaction with foreign_keys enabled. This updates the table definition text
-- in sqlite_schema and bumps the schema cookie so the connection reloads the
-- constraint. The new CHECK is a strict superset of the old one.

PRAGMA writable_schema = ON;

UPDATE sqlite_schema
SET sql = replace(
    sql,
    '''person'',''place'',''project'',''org'',''product'',
        ''community'',''skill'',''concept'',''thing'',''event_series''',
    '''person'',''place'',''project'',''org'',''product'',
        ''community'',''skill'',''concept'',''thing'',''event_series'',
        ''ai_entity'',''ai_persona'',''fictional_character'',''fictionalized_self'',
        ''narrative_device'',''safety_boundary'''
)
WHERE type = 'table'
  AND name = 'entities'
  AND sql LIKE '%kind              TEXT NOT NULL CHECK(kind IN (%'
  AND sql NOT LIKE '%ai_entity%';

-- Force SQLite to reload the edited sqlite_schema definition on this connection.
-- Date-derived cookie; unrelated to Pulse's schema_meta migration version.
PRAGMA schema_version = 20260424;

PRAGMA writable_schema = OFF;
