package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps a *sql.DB. Use DB() to access the underlying handle.
type Store struct {
	db *sql.DB
}

// DB returns the underlying *sql.DB for use by other packages.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// FeedSignal — proactive feed signal computed by pulse_consolidate.py.
type FeedSignal struct {
	ID              int64   `json:"id"`
	SignalKind      string  `json:"signal_kind"`
	SubjectEntityID *int64  `json:"subject_entity_id,omitempty"`
	EvidenceObsIDs  []any   `json:"evidence_obs_ids"`
	Salience        float64 `json:"salience"`
	ComputedAt      string  `json:"computed_at"`
	ConsumedAt      *string `json:"consumed_at,omitempty"`
}

// RecentFeedSignals returns up to `limit` most-salient unconsumed signals.
// If markConsumed is true, marks them consumed in the same transaction.
func (s *Store) RecentFeedSignals(limit int, markConsumed bool) ([]FeedSignal, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
        SELECT id, signal_kind, subject_entity_id, evidence_obs_ids, salience, computed_at, consumed_at
          FROM feed_signals
         WHERE consumed_at IS NULL
         ORDER BY salience DESC, computed_at DESC
         LIMIT ?
    `, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FeedSignal
	var ids []int64
	for rows.Next() {
		var fs FeedSignal
		var evidenceJSON string
		var subj sql.NullInt64
		var consumed sql.NullString
		if err := rows.Scan(&fs.ID, &fs.SignalKind, &subj, &evidenceJSON, &fs.Salience, &fs.ComputedAt, &consumed); err != nil {
			return nil, err
		}
		if subj.Valid {
			v := subj.Int64
			fs.SubjectEntityID = &v
		}
		if consumed.Valid {
			v := consumed.String
			fs.ConsumedAt = &v
		}
		if err := json.Unmarshal([]byte(evidenceJSON), &fs.EvidenceObsIDs); err != nil {
			fs.EvidenceObsIDs = []any{}
		}
		out = append(out, fs)
		ids = append(ids, fs.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if markConsumed && len(ids) > 0 {
		now := time.Now().UTC().Format(time.RFC3339)
		for _, id := range ids {
			if _, err := tx.Exec("UPDATE feed_signals SET consumed_at=? WHERE id=?", now, id); err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// Open opens the SQLite database, enables WAL, runs migrations.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
