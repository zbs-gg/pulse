package outbox

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const MaxAttempts = 10

// Message is the input to Enqueue. ReplyTo may be nil.
type Message struct {
	ChatID   int64
	Text     string
	ReplyTo  *int64
	Media    string
	Priority string // "normal" or "urgent"; empty = normal
}

// Record is a row returned by Claim.
type Record struct {
	ID       int64
	ChatID   int64
	Text     string
	ReplyTo  *int64
	Media    string
	Priority string
	Attempts int
}

type Outbox struct {
	db            *sql.DB
	leaseDuration time.Duration
}

func New(db *sql.DB, lease time.Duration) *Outbox {
	return &Outbox{db: db, leaseDuration: lease}
}

func dedupeKey(m Message) string {
	h := sha256.New()
	replyTo := "nil"
	if m.ReplyTo != nil {
		replyTo = fmt.Sprintf("%d", *m.ReplyTo)
	}
	fmt.Fprintf(h, "%d|%s|%s", m.ChatID, replyTo, m.Text)
	return hex.EncodeToString(h.Sum(nil))
}

// Enqueue inserts the message. If an identical dedupe_key already exists,
// returns the existing row id (idempotent).
func (o *Outbox) Enqueue(m Message) (int64, error) {
	key := dedupeKey(m)
	priority := m.Priority
	if priority == "" {
		priority = "normal"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	tx, err := o.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("outbox enqueue: %w", err)
	}
	defer tx.Rollback()

	var existingID int64
	err = tx.QueryRow("SELECT id FROM outbox WHERE dedupe_key = ?", key).Scan(&existingID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("outbox enqueue commit: %w", err)
		}
		return existingID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("outbox enqueue lookup: %w", err)
	}

	res, err := tx.Exec(`
        INSERT INTO outbox (dedupe_key, chat_id, text, reply_to, media, priority, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
    `, key, m.ChatID, m.Text, m.ReplyTo, nullIfEmpty(m.Media), priority, now)
	if err != nil {
		return 0, fmt.Errorf("outbox enqueue insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("outbox enqueue last insert id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("outbox enqueue commit: %w", err)
	}
	return id, nil
}

// Claim atomically marks up to `limit` pending rows as 'sending' and returns them.
// Rows whose next_retry is in the future are skipped.
func (o *Outbox) Claim(limit int) ([]Record, error) {
	now := time.Now().UTC()
	lease := now.Add(o.leaseDuration).Format(time.RFC3339Nano)
	nowStr := now.Format(time.RFC3339Nano)

	rows, err := o.db.Query(`
        UPDATE outbox
        SET status = 'sending', sending_until = ?
        WHERE id IN (
            SELECT id FROM outbox
            WHERE status = 'pending' AND (next_retry IS NULL OR next_retry <= ?)
            ORDER BY created_at ASC
            LIMIT ?
        )
        RETURNING id, chat_id, text, reply_to, media, priority, attempts
    `, lease, nowStr, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox claim: %w", err)
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		var r Record
		var replyTo sql.NullInt64
		var media sql.NullString
		if err := rows.Scan(&r.ID, &r.ChatID, &r.Text, &replyTo, &media, &r.Priority, &r.Attempts); err != nil {
			return nil, fmt.Errorf("outbox claim scan: %w", err)
		}
		if replyTo.Valid {
			v := replyTo.Int64
			r.ReplyTo = &v
		}
		if media.Valid {
			r.Media = media.String
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox claim iterate: %w", err)
	}
	return records, nil
}

// Ack marks a claimed row as 'sent' on success, or increments attempts and
// schedules a retry on failure.
func (o *Outbox) Ack(id int64, success bool, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if success {
		_, err := o.db.Exec(`
            UPDATE outbox SET status = 'sent', sent_at = ?, sending_until = NULL, error = NULL
            WHERE id = ?
        `, now, id)
		if err != nil {
			return fmt.Errorf("outbox ack success: %w", err)
		}
		return nil
	}

	// Atomic failure path: increment attempts, compute next state.
	// We compute next_retry for attempts+1; if attempts+1 >= MaxAttempts the
	// status becomes 'failed' and next_retry is ignored.
	tx, err := o.db.Begin()
	if err != nil {
		return fmt.Errorf("outbox ack begin: %w", err)
	}
	defer tx.Rollback()

	var attempts int
	if err := tx.QueryRow("SELECT attempts FROM outbox WHERE id = ?", id).Scan(&attempts); err != nil {
		return fmt.Errorf("outbox ack read attempts: %w", err)
	}
	newAttempts := attempts + 1

	if newAttempts >= MaxAttempts {
		if _, err := tx.Exec(`
            UPDATE outbox SET status = 'failed', attempts = ?, sending_until = NULL, error = ?
            WHERE id = ?
        `, newAttempts, errMsg, id); err != nil {
			return fmt.Errorf("outbox ack fail: %w", err)
		}
		return tx.Commit()
	}

	backoff := retryBackoff(newAttempts)
	nextRetry := time.Now().UTC().Add(backoff).Format(time.RFC3339Nano)
	if _, err := tx.Exec(`
        UPDATE outbox SET status = 'pending', attempts = ?, next_retry = ?, sending_until = NULL, error = ?
        WHERE id = ?
    `, newAttempts, nextRetry, errMsg, id); err != nil {
		return fmt.Errorf("outbox ack retry: %w", err)
	}
	return tx.Commit()
}

// Reap returns stale 'sending' rows (lease expired) to 'pending'. Runs periodically.
func (o *Outbox) Reap() (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := o.db.Exec(`
        UPDATE outbox SET status = 'pending', sending_until = NULL
        WHERE status = 'sending' AND (sending_until IS NULL OR sending_until < ?)
    `, now)
	if err != nil {
		return 0, fmt.Errorf("outbox reap: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("outbox reap rows affected: %w", err)
	}
	return n, nil
}

func retryBackoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 5 * time.Second
	case 2:
		return 30 * time.Second
	case 3:
		return 2 * time.Minute
	case 4:
		return 10 * time.Minute
	default:
		return time.Hour
	}
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
