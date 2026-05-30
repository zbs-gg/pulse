package erase

import (
	"context"
	"fmt"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

// Eraser performs content erasure operations against the store.
type Eraser struct {
	store *store.Store
}

// NewEraser returns an Eraser backed by the given store.
func NewEraser(s *store.Store) *Eraser {
	return &Eraser{store: s}
}

// SoftErase marks all observations linked (via evidence) to the entity as
// redacted=1 and sets content_text=NULL. Evidence rows and graph structure
// are preserved. Reversible at policy level — the audit log records op.
func (e *Eraser) SoftErase(ctx context.Context, entityID int64, initiatedBy, note string) error {
	db := e.store.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	logRes, err := tx.ExecContext(ctx, `
        INSERT INTO erasure_log (op_kind, subject_kind, subject_id, initiated_by, initiated_at, note)
        VALUES ('soft','entity',?,?,?,?)`,
		entityID, initiatedBy, now, note,
	)
	if err != nil {
		return fmt.Errorf("erasure_log: %w", err)
	}
	logID, err := logRes.LastInsertId()
	if err != nil {
		return fmt.Errorf("log last id: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
        UPDATE observations
        SET redacted=1, content_text=NULL
        WHERE id IN (
            SELECT observation_id FROM evidence WHERE subject_kind='entity' AND subject_id=?
        )`, entityID,
	)
	if err != nil {
		return fmt.Errorf("redact observations: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE erasure_log SET completed_at=? WHERE id=?`, now, logID)
	if err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	return tx.Commit()
}

// HardErase deletes observations linked to the entity. Evidence rows cascade
// via FK ON DELETE CASCADE. Entity row is preserved (empty shell) unless
// caller also issues a follow-up delete. Non-reversible at DB level.
func (e *Eraser) HardErase(ctx context.Context, entityID int64, initiatedBy, note string) error {
	db := e.store.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)
	logRes, err := tx.ExecContext(ctx, `
        INSERT INTO erasure_log (op_kind, subject_kind, subject_id, initiated_by, initiated_at, note)
        VALUES ('hard','entity',?,?,?,?)`,
		entityID, initiatedBy, now, note,
	)
	if err != nil {
		return fmt.Errorf("erasure_log: %w", err)
	}
	logID, err := logRes.LastInsertId()
	if err != nil {
		return fmt.Errorf("log last id: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
        DELETE FROM observations
        WHERE id IN (
            SELECT observation_id FROM evidence WHERE subject_kind='entity' AND subject_id=?
        )`, entityID,
	)
	if err != nil {
		return fmt.Errorf("delete observations: %w", err)
	}

	_, err = tx.ExecContext(ctx, `UPDATE erasure_log SET completed_at=? WHERE id=?`, now, logID)
	if err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	return tx.Commit()
}
