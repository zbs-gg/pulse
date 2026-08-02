-- Stable semantic identity for truthful Local Preview write receipts. Personal
-- keeps its existing governed private_memory_objects deduplication path; the
-- nullable column does not alter old or Personal rows.

ALTER TABLE memory_capsules ADD COLUMN content_digest TEXT
    CHECK(content_digest IS NULL OR (
        length(content_digest) = 64
        AND content_digest NOT GLOB '*[^a-f0-9]*'
    ));

CREATE UNIQUE INDEX idx_memory_capsules_active_content_digest
    ON memory_capsules(content_digest)
    WHERE content_digest IS NOT NULL AND status = 'active';

UPDATE store_identity
   SET min_reader_version = 42,
       min_writer_version = 42
 WHERE singleton = 1;
