-- First-class temporal assertions: bitemporal claim identity + typed scope.
--
-- The free-text facts table (005) stays as a projection target. This is the
-- canonical claim store: it carries a stable claim_key for supersession, a
-- separate valid-time (when true in the world) and system-time (when Pulse
-- believed it) so we can distinguish "the world changed" from "we recorded it
-- wrong", and a typed scope so cross-harness recall is deliberate, not bleed.
--
-- Does NOT touch v3 emotional scoring. Additive only.
CREATE TABLE assertions (
    id                INTEGER PRIMARY KEY,
    claim_key         TEXT NOT NULL,            -- normalized subject+predicate: the supersession key
    subject_entity_id INTEGER REFERENCES entities(id),
    predicate         TEXT NOT NULL,
    object_text       TEXT NOT NULL,
    object_entity_id  INTEGER REFERENCES entities(id),
    qualifiers        TEXT,                     -- JSON: location/degree/conditions

    confidence        REAL NOT NULL DEFAULT 1.0,

    -- valid-time (event-time): when the claim is true in the world.
    -- valid_to IS NULL  => still true.
    valid_from        TEXT,
    valid_to          TEXT,

    -- system-time (transaction-time): when Pulse believed it.
    -- system_to IS NULL => currently believed.
    system_from       TEXT NOT NULL,
    system_to         TEXT,

    -- lifecycle: active | superseded (world changed) | retracted (we were wrong)
    status            TEXT NOT NULL DEFAULT 'active'
                        CHECK(status IN ('active','superseded','retracted')),
    superseded_by     INTEGER REFERENCES assertions(id),

    -- provenance
    source_event_ids  TEXT,                     -- JSON array of event ids
    extractor_version TEXT,

    -- typed scope (§10.C): cross-harness recall is deliberate, not accidental.
    scope_type        TEXT NOT NULL DEFAULT 'personal'
                        CHECK(scope_type IN ('personal','project','repo','agent','session')),
    scope_id          TEXT NOT NULL DEFAULT '',
    visibility        TEXT NOT NULL DEFAULT 'private'
                        CHECK(visibility IN ('private','shared')),

    created_at        TEXT NOT NULL
);

CREATE INDEX idx_assertions_claim   ON assertions(claim_key, status);
CREATE INDEX idx_assertions_subject ON assertions(subject_entity_id);
CREATE INDEX idx_assertions_scope   ON assertions(scope_type, scope_id);
-- Fast "what does Pulse currently believe" lookups.
CREATE INDEX idx_assertions_current ON assertions(claim_key) WHERE system_to IS NULL;
