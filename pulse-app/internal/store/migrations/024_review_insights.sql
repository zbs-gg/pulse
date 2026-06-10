ALTER TABLE continuity_checkpoints ADD COLUMN active_threads_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE continuity_checkpoints ADD COLUMN review_insights_json TEXT NOT NULL DEFAULT '[]';
