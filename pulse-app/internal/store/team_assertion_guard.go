package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

// RecheckTeamWriterLeaseTx fences a team mutation to the active daemon lease.
// Team stores use BEGIN IMMEDIATE, so once this succeeds no competing writer
// can replace the lease before the caller commits or rolls back this tx.
func (s *Store) RecheckTeamWriterLeaseTx(ctx context.Context, tx *sql.Tx, writerID, token string) error {
	if tx == nil || writerID == "" || token == "" {
		return ErrTeamWriterLeaseMismatch
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return err
	}
	_, err = validateTeamWriterLease(ctx, tx, info.StoreID, writerID, token, s.clock().UTC())
	return err
}

type teamWriterLeaseState struct {
	WriterID      string
	WriterVersion int
}

func validateTeamWriterLease(
	ctx context.Context,
	q queryer,
	storeID, writerID, token string,
	now time.Time,
) (teamWriterLeaseState, error) {
	var state teamWriterLeaseState
	var runtimeMode, tokenHash, expiresAt string
	if err := q.QueryRowContext(ctx, `
		SELECT writer_id, runtime_mode, writer_version, lease_token_hash, expires_at
		  FROM team_writer_leases WHERE store_id = ?`, storeID).Scan(
		&state.WriterID, &runtimeMode, &state.WriterVersion, &tokenHash, &expiresAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return teamWriterLeaseState{}, ErrTeamWriterLeaseMismatch
		}
		return teamWriterLeaseState{}, err
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(now) || runtimeMode != "team-remote" ||
		state.WriterVersion < teamauth.SchemaVersion || state.WriterID != writerID || !writerLeaseTokenMatches(tokenHash, token) {
		return teamWriterLeaseState{}, ErrTeamWriterLeaseMismatch
	}
	return state, nil
}

// ConsumeAssertionIDWithWriterLease records the replay fence only while the
// caller still owns the active team writer lease.
func (s *Store) ConsumeAssertionIDWithWriterLease(
	ctx context.Context,
	kid, jti string,
	expiresAt time.Time,
	writerID, token string,
) error {
	if kid == "" || jti == "" {
		return ErrInvalidTeamIdentityMutation
	}
	now := s.clock().UTC()
	if !expiresAt.After(now) {
		return ErrAssertionExpired
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, writerID, token); err != nil {
		return err
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_assertion_replay(store_id, kid, jti, expires_at, consumed_at)
		VALUES (?, ?, ?, ?, ?)`, info.StoreID, assertionIdentifier("kid", kid), assertionIdentifier("jti", jti),
		expiresAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		if isConstraintError(err) {
			return ErrAssertionReplay
		}
		return err
	}
	return tx.Commit()
}

// PruneExpiredAssertionIDsWithWriterLease prevents an expired daemon from
// deleting replay fences after a replacement writer has taken over.
func (s *Store) PruneExpiredAssertionIDsWithWriterLease(ctx context.Context, writerID, token string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, writerID, token); err != nil {
		return 0, err
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `
		DELETE FROM team_assertion_replay WHERE store_id = ? AND expires_at <= ?`,
		info.StoreID, s.clock().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}
