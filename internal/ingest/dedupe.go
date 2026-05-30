package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nkkmnk/pulse/internal/capture"
)

// applySensitivePolicy checks whether any actor in obs matches a sensitive_actors
// record and mutates obs.ContentText / obs.MediaRefs / obs.Metadata according to
// the policy. obs.Redacted is set to true when any policy fires.
func applySensitivePolicy(ctx context.Context, db *sql.DB, obs *capture.Observation) error {
	if len(obs.Actors) == 0 {
		return nil
	}
	for _, a := range obs.Actors {
		var policy string
		err := db.QueryRowContext(ctx, `
			SELECT sa.policy
			FROM sensitive_actors sa
			JOIN entity_identities ei ON ei.entity_id = sa.entity_id
			WHERE ei.source_kind = ? AND ei.identifier = ?`,
			obs.SourceKind, a.ID,
		).Scan(&policy)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}

		switch policy {
		case "no_capture":
			obs.ContentText = ""
			obs.MediaRefs = nil
			obs.Metadata = map[string]any{"sensitive": "no_capture"}
		case "redact_content":
			obs.ContentText = "[redacted]"
		case "summary_only":
			obs.ContentText = "[summary_only]"
		}
		obs.Redacted = true
		return nil
	}
	return nil
}

// upsert decides whether an observation is a new insert, duplicate, or revision.
// SELECT-then-INSERT has a race: two concurrent ingests of the same
// (source_kind, source_id) can both miss and both attempt INSERT. The UNIQUE
// constraint will reject the loser — acceptable for M1; transactionalize later.
func (h *Handler) upsert(ctx context.Context, obs *capture.Observation) (op, int64, error) {
	db := h.store.DB()
	var existingID int64
	var existingHash string
	var existingVersion int

	err := db.QueryRowContext(ctx, `
		SELECT id, content_hash, version FROM observations
		WHERE source_kind=? AND source_id=?
		ORDER BY version DESC LIMIT 1`,
		obs.SourceKind, obs.SourceID,
	).Scan(&existingID, &existingHash, &existingVersion)

	if err == sql.ErrNoRows {
		if err := applySensitivePolicy(ctx, db, obs); err != nil {
			return 0, 0, fmt.Errorf("sensitive policy: %w", err)
		}
		id, err := insertObservation(ctx, db, obs)
		if err != nil {
			return 0, 0, err
		}
		if err := enqueueExtractionJob(ctx, db, id); err != nil {
			return 0, 0, fmt.Errorf("enqueue: %w", err)
		}
		return opInsert, id, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if existingHash == obs.ContentHash {
		return opDuplicate, existingID, nil
	}

	// Revision: new version row + revision audit record.
	newVersion := existingVersion + 1
	newObs := *obs
	newObs.Version = newVersion
	if err := applySensitivePolicy(ctx, db, &newObs); err != nil {
		return 0, 0, fmt.Errorf("sensitive policy: %w", err)
	}
	newID, err := insertObservation(ctx, db, &newObs)
	if err != nil {
		return 0, 0, err
	}
	diff, err := computeDiff(ctx, db, existingID, obs.ContentText)
	if err != nil {
		return 0, 0, fmt.Errorf("compute diff: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO observation_revisions (observation_id, version, prev_hash, diff, changed_at)
		VALUES (?, ?, ?, ?, ?)`,
		newID, newVersion, existingHash,
		diff,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return 0, 0, fmt.Errorf("record revision: %w", err)
	}
	if err := enqueueExtractionJob(ctx, db, newID); err != nil {
		return 0, 0, fmt.Errorf("enqueue: %w", err)
	}
	return opRevision, newID, nil
}

func enqueueExtractionJob(ctx context.Context, db *sql.DB, obsID int64) error {
	ids, _ := json.Marshal([]int64{obsID})
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.ExecContext(ctx, `
		INSERT INTO extraction_jobs (observation_ids, state, attempts, created_at, updated_at)
		VALUES (?, 'pending', 0, ?, ?)`,
		string(ids), now, now,
	)
	return err
}

func insertObservation(ctx context.Context, db *sql.DB, obs *capture.Observation) (int64, error) {
	actors, err := json.Marshal(obs.Actors)
	if err != nil {
		return 0, fmt.Errorf("marshal actors: %w", err)
	}
	meta, err := json.Marshal(obs.Metadata)
	if err != nil {
		return 0, fmt.Errorf("marshal metadata: %w", err)
	}
	media, err := json.Marshal(obs.MediaRefs)
	if err != nil {
		return 0, fmt.Errorf("marshal media_refs: %w", err)
	}
	redactedInt := 0
	if obs.Redacted {
		redactedInt = 1
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO observations
		  (source_kind, source_id, content_hash, version, scope,
		   captured_at, observed_at, actors, content_text, media_refs, metadata, raw_json, redacted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		obs.SourceKind, obs.SourceID, obs.ContentHash, obs.Version, obs.Scope,
		obs.CapturedAt.Format(time.RFC3339), obs.ObservedAt.Format(time.RFC3339),
		string(actors), obs.ContentText, string(media), string(meta), string(obs.RawJSON),
		redactedInt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// computeDiff produces a compact human-readable marker for the revision audit log.
// We record quoted prefixes of the previous and new content_text — not a real diff.
func computeDiff(ctx context.Context, db *sql.DB, prevID int64, newContent string) (string, error) {
	var prev string
	if err := db.QueryRowContext(ctx, `SELECT content_text FROM observations WHERE id=?`, prevID).Scan(&prev); err != nil {
		return "", fmt.Errorf("fetch prev content (id=%d): %w", prevID, err)
	}
	return fmt.Sprintf("-%.100q\n+%.100q", prev, newContent), nil
}
