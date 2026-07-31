-- Keep the binding-scoped Memory Home pending-card query ordered and bounded
-- without sorting every pending candidate. Personal stores only.

CREATE INDEX idx_memory_tray_pending_home
    ON memory_tray_candidates(state, updated_at DESC, candidate_id);

UPDATE store_identity
   SET min_reader_version = 37,
       min_writer_version = 37
 WHERE singleton = 1;
