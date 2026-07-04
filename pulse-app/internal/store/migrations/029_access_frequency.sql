-- 028_access_frequency.sql
-- Access-frequency salience: instrumentation layer only (Phase A).
-- Adds a bare per-event usage counter so that a memory recalled many times can
-- (in a later, deferred phase) be scored as more salient than one never used.
--
--   access_count      — integer bumped by id each time the event is returned by
--                       Retrieve (only when PULSE_ACCESS_FREQ is enabled). Keyed
--                       by event.id ONLY: no content, transcript, or text is read
--                       or written here, so the daemon-never-sees-raw invariant
--                       is untouched.
--   last_accessed_at  — ISO8601 timestamp of the most recent recall (NULL = never).
--
-- Additive / backward-compatible (mirrors migration 021's ALTER pattern):
-- existing rows get access_count=0 and NULL last_accessed_at. Flat / all-zero
-- counts mean the (future, deferred) freq boost stays exactly 1.0 for every
-- event, so retrieval scoring and Go==Python parity are unaffected. This
-- migration changes NO scoring path — it only makes the signal available.

ALTER TABLE events ADD COLUMN access_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN last_accessed_at TEXT;
