CREATE TABLE outbox (
    id            INTEGER PRIMARY KEY,
    dedupe_key    TEXT UNIQUE NOT NULL,
    chat_id       INTEGER NOT NULL,
    text          TEXT NOT NULL,
    reply_to      INTEGER,
    media         TEXT,
    priority      TEXT NOT NULL DEFAULT 'normal',
    status        TEXT NOT NULL DEFAULT 'pending',
    attempts      INTEGER NOT NULL DEFAULT 0,
    sending_until TEXT,
    next_retry    TEXT,
    created_at    TEXT NOT NULL,
    sent_at       TEXT,
    error         TEXT
);

CREATE INDEX idx_outbox_status ON outbox(status, next_retry);
