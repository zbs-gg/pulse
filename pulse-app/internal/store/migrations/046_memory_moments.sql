-- A write operation may contain every meaningful part of one moment. Keep the
-- real host turn separate from its internal operation ledger identity.
ALTER TABLE turn_ledgers ADD COLUMN moment_id TEXT;
ALTER TABLE turn_ledgers ADD COLUMN source_turn_ref TEXT;
ALTER TABLE turn_ledgers ADD COLUMN moment_repository_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_turn_ledgers_moment ON turn_ledgers(moment_id) WHERE moment_id IS NOT NULL;
UPDATE store_identity SET min_reader_version=46, min_writer_version=46 WHERE singleton=1;
