-- Exact-digest proof that a human-visible, authenticated Memory Home surface
-- rendered a private memory candidate. Receipts contain only identifiers,
-- digests, surface metadata, binding, and timestamps; never candidate content.
-- This migration applies only to Personal stores.

CREATE TABLE memory_presentation_receipts (
    receipt_id              TEXT PRIMARY KEY,
    candidate_id            TEXT NOT NULL REFERENCES memory_tray_candidates(candidate_id) ON DELETE CASCADE,
    candidate_version       INTEGER NOT NULL CHECK(candidate_version >= 1),
    content_digest          TEXT NOT NULL CHECK(
                                length(content_digest) = 64
                                AND content_digest NOT GLOB '*[^a-f0-9]*'
                            ),
    trusted_surface_kind    TEXT NOT NULL CHECK(trusted_surface_kind = 'memory_home'),
    trusted_surface_instance TEXT NOT NULL CHECK(
                                length(trusted_surface_instance) BETWEEN 1 AND 255
                                AND trusted_surface_instance NOT GLOB '*[^A-Za-z0-9._:-]*'
                            ),
    binding_digest          TEXT NOT NULL CHECK(
                                length(binding_digest) = 64
                                AND binding_digest NOT GLOB '*[^a-f0-9]*'
                            ),
    presented_at            TEXT NOT NULL,
    grace_expires_at        TEXT NOT NULL,
    UNIQUE(candidate_id, candidate_version, content_digest,
           trusted_surface_kind, trusted_surface_instance, binding_digest)
);

CREATE INDEX idx_memory_presentation_candidate
    ON memory_presentation_receipts(candidate_id, candidate_version, presented_at, receipt_id);

CREATE TRIGGER memory_presentation_receipts_immutable
BEFORE UPDATE ON memory_presentation_receipts
BEGIN SELECT RAISE(ABORT, 'memory presentation receipts are immutable'); END;

CREATE TRIGGER memory_presentation_receipts_no_delete
BEFORE DELETE ON memory_presentation_receipts
WHEN EXISTS (
    SELECT 1 FROM memory_tray_candidates
     WHERE candidate_id = OLD.candidate_id
)
BEGIN SELECT RAISE(ABORT, 'memory presentation receipts are append-only'); END;

-- Older pending rows have no trusted exact-card presentation proof. Clearing
-- their old timer is the only safe upgrade: they remain visible and editable,
-- but cannot auto-commit until Memory Home presents the current digest.
UPDATE memory_tray_candidates
   SET grace_expires_at = ''
 WHERE state = 'pending';

UPDATE store_identity
   SET min_reader_version = 35,
       min_writer_version = 35
 WHERE singleton = 1;
