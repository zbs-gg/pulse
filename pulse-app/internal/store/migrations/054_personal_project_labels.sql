-- Keep a content-free, human-readable label for each repository represented in
-- the one-vault Personal store. Absolute workspace paths are never persisted.

CREATE TABLE personal_project_labels (
    repository_id  TEXT PRIMARY KEY CHECK(
        length(repository_id) BETWEEN 1 AND 256
        AND substr(repository_id, 1, 1) GLOB '[A-Za-z0-9]'
        AND repository_id NOT GLOB '*[^A-Za-z0-9._:-]*'
    ),
    label          TEXT NOT NULL CHECK(length(label) BETWEEN 1 AND 96),
    updated_at     TEXT NOT NULL
);

UPDATE store_identity
   SET min_reader_version = 54,
       min_writer_version = 54
 WHERE singleton = 1;
