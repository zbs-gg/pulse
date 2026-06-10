-- 019_feed_signals.sql
-- Phase M9.4 — proactive feed signals computed by daily consolidation.
--
-- Rationale: do not capture feed *content* — capture *signals* (e.g. likes,
--   dwell on content domains). Consolidation computes 5 pattern types from
--   those observations:
--     like_burst       — 5+ likes within a 60-min window
--     dwell_spike      — sum dwell on a platform > 2x rolling-7d-baseline
--     topic_cluster    — >=3 observations sharing extracted topic entity / 24h
--     post_event_shift — high emotional_weight event followed by feed activity
--     hrv_x_topic      — biometric HRV below baseline + topic_cluster
--                        on stress-pattern topics
--
--   A client queries unconsumed signals at session start; surfaces at most one.

CREATE TABLE feed_signals (
    id                INTEGER PRIMARY KEY,
    signal_kind       TEXT NOT NULL CHECK (signal_kind IN (
                        'like_burst',
                        'dwell_spike',
                        'topic_cluster',
                        'post_event_shift',
                        'hrv_x_topic'
                      )),
    subject_entity_id INTEGER REFERENCES entities(id),
    evidence_obs_ids  TEXT NOT NULL,           -- JSON array of observation ids
    salience          REAL NOT NULL DEFAULT 0,
    computed_at       TEXT NOT NULL,
    consumed_at       TEXT,
    UNIQUE(signal_kind, subject_entity_id, computed_at)
);

CREATE INDEX idx_feed_signals_unconsumed ON feed_signals(consumed_at, salience DESC)
    WHERE consumed_at IS NULL;
CREATE INDEX idx_feed_signals_subject     ON feed_signals(subject_entity_id);
