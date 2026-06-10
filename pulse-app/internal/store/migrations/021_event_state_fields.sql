-- 021_event_state_fields.sql
-- Adds the per-event fields the v3 conditional boosts read, so the production
-- Go engine can run state_fit / anchor / date boosts (previously these lived
-- only in the Python bench reference retrieval_v3.py).
--
--   user_flag        — load-bearing anchor (user-pinned, emotionally salient, or
--                      milestone events). Drives boost_anchor + slower anchor-decay.
--   sentiment_label  — coarse label used by depletion/restoration heuristics
--                      ('body_signal','restoration','milestone','setback'…).
--   biometric_json   — JSON snapshot {hrv, sleep_quality, stress_proxy, hr_trend,
--                      hrv_trend, workout} for state_fit depletion/restoration.
--
-- All nullable / defaulted → backward-compatible. Existing rows keep user_flag=0
-- (no anchor), NULL label/biometric (state boost simply stays 1.0 → v2_pure).
-- Conditional gating means absent fields = no boost, never a regression.

ALTER TABLE events ADD COLUMN user_flag INTEGER NOT NULL DEFAULT 0 CHECK (user_flag IN (0, 1));
ALTER TABLE events ADD COLUMN sentiment_label TEXT;
ALTER TABLE events ADD COLUMN biometric_json TEXT;

CREATE INDEX IF NOT EXISTS idx_events_user_flag ON events(user_flag) WHERE user_flag = 1;
