-- Persistent claim-resolution decision ledger (Pro NO-GO #8): shadow mode must
-- decide+LOG, not just flip to insert with aggregate counters. Every resolution
-- decision is recorded here with the candidate it acted on, cosine, change-cue,
-- the human-readable reason, and whether it was actually APPLIED (shadow=0).
CREATE TABLE IF NOT EXISTS claim_decisions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    claim_key   TEXT NOT NULL,
    scope_type  TEXT,
    scope_id    TEXT,
    visibility  TEXT,
    action      TEXT NOT NULL,            -- proposed action: supersede | insert | noop
    applied     INTEGER NOT NULL DEFAULT 0, -- 1 = applied; 0 = shadow (would-have)
    target_id   INTEGER,
    cosine      REAL,
    change_cue  INTEGER NOT NULL DEFAULT 0,
    reason      TEXT,
    decided_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_claim_decisions_key ON claim_decisions(claim_key);
