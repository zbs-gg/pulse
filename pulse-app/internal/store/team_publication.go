package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nkkmnk/pulse/internal/capture"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrTeamPublicationInvalid             = errors.New("invalid_team_publication")
	ErrTeamPublicationDeskRequired        = errors.New("team_publication_requires_desk")
	ErrTeamPublicationSourceMismatch      = errors.New("team_publication_source_mismatch")
	ErrTeamPublicationIdempotencyConflict = errors.New("team_publication_idempotency_conflict")
	ErrTeamPublicationDeletionBarrier     = errors.New("team_publication_deletion_barrier")
)

const (
	TeamPublicationEnvelopeSchema              = "pulse.team.airlock_envelope.v1"
	TeamPublicationPrepared                    = "prepared"
	TeamPublicationApproved                    = "approved"
	TeamPublicationInFlight                    = "in_flight"
	TeamPublicationRemoteCommittedLocalPending = "remote_committed_local_pending"
	TeamPublicationReconciled                  = "reconciled"
	TeamPublicationCanceled                    = "canceled"
	TeamPublicationExpired                     = "expired"
	TeamPublicationFailed                      = "failed"
	TeamPublicationFailureRemoteAbsent         = "remote_absence_verified"
	teamPublicationAction                      = "team.commons.publish"
	teamPublicationMaxBytes                    = 64 << 10
	teamPublicationMaxTTL                      = 5 * time.Minute
)

var teamPublicationTopLevel = []string{
	"schema", "action", "deployment_id", "store_id", "team_id",
	"target_kind", "target_id", "publication_key", "policy_epoch", "writer_principal_id",
	"client_key", "writer_id", "source_timestamp", "content", "metadata",
}

var (
	teamPublicationOpaque           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$`)
	teamPublicationHTML             = regexp.MustCompile(`(?i)<\s*/?\s*[A-Za-z][^>]*>`)
	teamPublicationActive           = regexp.MustCompile(`(?i)(javascript|vbscript|data)\s*:`)
	teamPublicationSecret           = regexp.MustCompile(`(?i)(authorization\s*:\s*bearer|api[_ -]?key|password|private[_ -]?key|begin\s+(rsa\s+)?private\s+key|\bsk-[A-Za-z0-9_-]{12,}\b|\bghp_[A-Za-z0-9_]{12,}\b|\bxoxb-[A-Za-z0-9-]{12,}\b|\bAKIA[0-9A-Z]{12,}\b)`)
	teamPublicationPath             = regexp.MustCompile(`(?i)(/(users|home|etc|var|private|volumes|tmp|opt|workspace)/|file://|(^|\s)~/|(^|\s)[a-z]:\\|\\\\[^\\\s]+\\)`)
	teamPublicationPrivateReference = regexp.MustCompile(`(?i)\b(memory|candidate|session|thread|desk|personal|tray|vault)_[A-Za-z0-9][A-Za-z0-9._:-]{7,}\b`)
)

type TeamPublicationPrepareRequest struct {
	SourceObjectID      string
	SourceContentDigest string
	DeploymentID        string
	RemoteStoreID       string
	TeamID              string
	PolicyEpoch         int64
	WriterPrincipalID   string
	ClientKey           string
	WriterID            string
	CanonicalEnvelope   []byte
	EnvelopeDigest      string
	IdempotencyKey      string
	ExpiresAt           time.Time
}

type TeamPublicationIntent struct {
	IntentID            string
	SourceObjectID      string
	SourceContentDigest string
	DeploymentID        string
	RemoteStoreID       string
	TeamID              string
	PolicyEpoch         int64
	WriterPrincipalID   string
	ClientKey           string
	WriterID            string
	State               string
	EnvelopeDigest      string
	CanonicalEnvelope   []byte
	IdempotencyKey      string
	ExpiresAt           time.Time
	CreatedAt           time.Time
	ApprovalID          string
	ApprovalDigest      string
	ApprovedAt          *time.Time
	RemoteObjectID      string
	RemoteReceiptID     string
	RemoteAuditEventID  string
	RemoteContentDigest string
	FailureCode         string
	TerminalAt          *time.Time
	Replayed            bool
}

type TeamPublicationLocalApprovalReceipt struct {
	IntentID       string
	ApprovalID     string
	EnvelopeDigest string
	ApprovedAt     time.Time
}

type TeamPublicationRemoteReceipt struct {
	IdempotencyKey string
	EnvelopeDigest string
	ObjectID       string
	ReceiptID      string
	AuditEventID   string
	RecordedAt     time.Time
}

type TeamPublicationEnvelopeBindings struct {
	DeploymentID      string
	StoreID           string
	TeamID            string
	PolicyEpoch       int64
	WriterPrincipalID string
	ClientKey         string
	WriterID          string
	PublicationKey    string
	EnvelopeDigest    string
}

type teamPublicationEnvelope struct {
	Schema            string                   `json:"schema"`
	Action            string                   `json:"action"`
	DeploymentID      string                   `json:"deployment_id"`
	StoreID           string                   `json:"store_id"`
	TeamID            string                   `json:"team_id"`
	TargetKind        string                   `json:"target_kind"`
	TargetID          string                   `json:"target_id"`
	PublicationKey    string                   `json:"publication_key"`
	PolicyEpoch       int64                    `json:"policy_epoch"`
	WriterPrincipalID string                   `json:"writer_principal_id"`
	ClientKey         string                   `json:"client_key"`
	WriterID          string                   `json:"writer_id"`
	SourceTimestamp   string                   `json:"source_timestamp"`
	Content           string                   `json:"content"`
	Metadata          *teamPublicationMetadata `json:"metadata"`
}

type teamPublicationMetadata struct {
	Kind string    `json:"kind"`
	Tags *[]string `json:"tags"`
}

// PrepareTeamPublication creates an immutable local disclosure intent. It has
// no approval or publication side effect; those require the separate human
// Airlock boundary. Private source identifiers stay in this Desk row and are
// deliberately absent from the canonical outbound envelope.
func (s *Store) PrepareTeamPublication(ctx context.Context, request TeamPublicationPrepareRequest) (TeamPublicationIntent, error) {
	if s.storeKind != StoreKindDesk {
		return TeamPublicationIntent{}, ErrTeamPublicationDeskRequired
	}
	envelope, canonical, digest, err := normalizeTeamPublicationEnvelope(request)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	now := s.clock().UTC()
	if request.ExpiresAt.IsZero() || !request.ExpiresAt.After(now) || request.ExpiresAt.After(now.Add(teamPublicationMaxTTL)) {
		return TeamPublicationIntent{}, ErrTeamPublicationInvalid
	}
	if !validTrayIdentifier(request.SourceObjectID) || !validDigest(request.SourceContentDigest) ||
		!validTrayIdentifier(request.IdempotencyKey) {
		return TeamPublicationIntent{}, ErrTeamPublicationInvalid
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	defer tx.Rollback()
	var storedDigest, lifecycle string
	if err := tx.QueryRowContext(ctx, `
		SELECT content_digest, lifecycle FROM private_memory_objects WHERE object_id=?`,
		request.SourceObjectID,
	).Scan(&storedDigest, &lifecycle); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TeamPublicationIntent{}, ErrTeamPublicationSourceMismatch
		}
		return TeamPublicationIntent{}, err
	}
	if lifecycle != "active" || storedDigest != request.SourceContentDigest {
		return TeamPublicationIntent{}, ErrTeamPublicationSourceMismatch
	}

	if existing, found, err := loadTeamPublicationIntentTx(ctx, tx, request.IdempotencyKey); err != nil {
		return TeamPublicationIntent{}, err
	} else if found {
		if !sameTeamPublicationIntent(existing, request, canonical, digest) {
			return TeamPublicationIntent{}, ErrTeamPublicationIdempotencyConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	intentID, err := newOpaqueID("airlock")
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	createdAt := now.Format(time.RFC3339Nano)
	expiresAt := request.ExpiresAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_publication_intents(
			intent_id, source_object_id, source_content_digest,
			deployment_id, store_id, team_id, policy_epoch,
			writer_principal_id, client_key, writer_id,
			envelope_schema, envelope_json, envelope_digest,
			idempotency_key, state, expires_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'prepared', ?, ?, ?)`,
		intentID, request.SourceObjectID, request.SourceContentDigest,
		envelope.DeploymentID, envelope.StoreID, envelope.TeamID, envelope.PolicyEpoch,
		envelope.WriterPrincipalID, envelope.ClientKey, envelope.WriterID,
		envelope.Schema, string(canonical), digest, request.IdempotencyKey,
		expiresAt, createdAt, createdAt,
	)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamPublicationIntent{}, err
	}
	return TeamPublicationIntent{
		IntentID: intentID, SourceObjectID: request.SourceObjectID,
		SourceContentDigest: request.SourceContentDigest,
		DeploymentID:        envelope.DeploymentID, RemoteStoreID: envelope.StoreID, TeamID: envelope.TeamID,
		PolicyEpoch: envelope.PolicyEpoch, WriterPrincipalID: envelope.WriterPrincipalID,
		ClientKey: envelope.ClientKey, WriterID: envelope.WriterID,
		State: TeamPublicationPrepared, EnvelopeDigest: digest,
		CanonicalEnvelope: append([]byte(nil), canonical...), IdempotencyKey: request.IdempotencyKey,
		ExpiresAt: request.ExpiresAt.UTC(), CreatedAt: now,
	}, nil
}

// ParseCanonicalTeamPublicationEnvelope is the shared Go boundary for every
// Airlock preview and commit path. It accepts only the exact canonical bytes
// that normalizeTeamPublicationEnvelope will later persist and returns public
// authority bindings without any private Desk source identifiers.
func ParseCanonicalTeamPublicationEnvelope(
	canonical []byte,
) (TeamPublicationEnvelopeBindings, error) {
	digestBytes := sha256.Sum256(canonical)
	digest := hex.EncodeToString(digestBytes[:])
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var preliminary teamPublicationEnvelope
	if err := decoder.Decode(&preliminary); err != nil {
		return TeamPublicationEnvelopeBindings{}, ErrTeamPublicationInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return TeamPublicationEnvelopeBindings{}, ErrTeamPublicationInvalid
	}
	envelope, exact, exactDigest, err := normalizeTeamPublicationEnvelope(TeamPublicationPrepareRequest{
		DeploymentID: preliminary.DeploymentID, RemoteStoreID: preliminary.StoreID,
		TeamID: preliminary.TeamID, PolicyEpoch: preliminary.PolicyEpoch,
		WriterPrincipalID: preliminary.WriterPrincipalID, ClientKey: preliminary.ClientKey,
		WriterID: preliminary.WriterID, CanonicalEnvelope: canonical,
		EnvelopeDigest: digest, IdempotencyKey: preliminary.PublicationKey,
	})
	if err != nil || !bytes.Equal(exact, canonical) || exactDigest != digest {
		return TeamPublicationEnvelopeBindings{}, ErrTeamPublicationInvalid
	}
	return TeamPublicationEnvelopeBindings{
		DeploymentID: envelope.DeploymentID, StoreID: envelope.StoreID,
		TeamID: envelope.TeamID, PolicyEpoch: envelope.PolicyEpoch,
		WriterPrincipalID: envelope.WriterPrincipalID, ClientKey: envelope.ClientKey,
		WriterID: envelope.WriterID, PublicationKey: envelope.PublicationKey,
		EnvelopeDigest: exactDigest,
	}, nil
}

// RecordTeamPublicationApprovalReceipt mirrors an approval already issued by
// the out-of-band human Airlock. It cannot grant remote authority; it only
// advances this exact immutable Desk intent after the remote approval exists.
func (s *Store) RecordTeamPublicationApprovalReceipt(
	ctx context.Context,
	receipt TeamPublicationLocalApprovalReceipt,
) (TeamPublicationIntent, error) {
	if s.storeKind != StoreKindDesk || !validTrayIdentifier(receipt.IntentID) ||
		!validTeamOpaque(receipt.ApprovalID, 8, 255) || !validDigest(receipt.EnvelopeDigest) ||
		receipt.ApprovedAt.IsZero() {
		return TeamPublicationIntent{}, ErrTeamPublicationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	defer tx.Rollback()
	intent, found, err := loadTeamPublicationIntentByIDTx(ctx, tx, receipt.IntentID)
	if err != nil || !found {
		if err != nil {
			return TeamPublicationIntent{}, err
		}
		return TeamPublicationIntent{}, ErrTeamPublicationSourceMismatch
	}
	if intent.EnvelopeDigest != receipt.EnvelopeDigest {
		return TeamPublicationIntent{}, ErrTeamPublicationIdempotencyConflict
	}
	if intent.ApprovalID != "" {
		if intent.ApprovalID != receipt.ApprovalID || intent.ApprovalDigest != receipt.EnvelopeDigest ||
			intent.ApprovedAt == nil || !intent.ApprovedAt.Equal(receipt.ApprovedAt.UTC()) {
			return TeamPublicationIntent{}, ErrTeamPublicationIdempotencyConflict
		}
		intent.Replayed = true
		return intent, nil
	}
	approvedAt := receipt.ApprovedAt.UTC()
	if intent.State != TeamPublicationPrepared || !intent.ExpiresAt.After(approvedAt) ||
		approvedAt.After(s.clock().UTC().Add(30*time.Second)) {
		return TeamPublicationIntent{}, ErrTeamPublicationInvalid
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE team_publication_intents
		   SET state='approved', approval_id=?, approval_digest=?, approved_at=?, updated_at=?
		 WHERE intent_id=? AND state='prepared' AND envelope_digest=?`,
		receipt.ApprovalID, receipt.EnvelopeDigest, approvedAt.Format(time.RFC3339Nano),
		approvedAt.Format(time.RFC3339Nano), receipt.IntentID, receipt.EnvelopeDigest,
	)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return TeamPublicationIntent{}, ErrTeamPublicationIdempotencyConflict
	}
	if err := tx.Commit(); err != nil {
		return TeamPublicationIntent{}, err
	}
	return s.TeamPublicationIntentByKey(ctx, intent.IdempotencyKey)
}

func (s *Store) MarkTeamPublicationInFlight(
	ctx context.Context,
	intentID, approvalID string,
	at time.Time,
) (TeamPublicationIntent, error) {
	if s.storeKind != StoreKindDesk || !validTrayIdentifier(intentID) ||
		!validTeamOpaque(approvalID, 8, 255) || at.IsZero() {
		return TeamPublicationIntent{}, ErrTeamPublicationInvalid
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE team_publication_intents
		   SET state='in_flight', failure_code=NULL, terminal_at=NULL, updated_at=?
		 WHERE intent_id=? AND approval_id=?
		   AND (state='approved' OR (state='failed' AND failure_code=?))
		   AND disclosure_purged_at IS NULL
		   AND expires_at > ?`,
		at.UTC().Format(time.RFC3339Nano), intentID, approvalID,
		TeamPublicationFailureRemoteAbsent,
		at.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		intent, loadErr := s.teamPublicationIntentByID(ctx, intentID)
		if loadErr == nil && intent.State == TeamPublicationInFlight && intent.ApprovalID == approvalID {
			intent.Replayed = true
			return intent, nil
		}
		return TeamPublicationIntent{}, ErrTeamPublicationIdempotencyConflict
	}
	return s.teamPublicationIntentByID(ctx, intentID)
}

// CancelTeamPublication ends a disclosure before any remote commit can begin.
// The exact envelope digest is required so a stale UI cannot cancel a newer
// intent that happens to reuse an opaque identifier.
func (s *Store) CancelTeamPublication(
	ctx context.Context,
	intentID, envelopeDigest string,
	at time.Time,
) (TeamPublicationIntent, error) {
	return s.terminateTeamPublicationIntent(
		ctx, intentID, envelopeDigest, TeamPublicationCanceled, "", at, false,
	)
}

// ExpireTeamPublication advances only an actually expired prepared/approved
// disclosure. In-flight and remotely committed publications must be resolved
// through receipt lookup instead of being hidden as expired.
func (s *Store) ExpireTeamPublication(
	ctx context.Context,
	intentID, envelopeDigest string,
	at time.Time,
) (TeamPublicationIntent, error) {
	return s.terminateTeamPublicationIntent(
		ctx, intentID, envelopeDigest, TeamPublicationExpired, "", at, true,
	)
}

// FailTeamPublicationRemoteAbsent records a retryable failure only after the
// remote reconciliation path has established that no publication receipt
// exists for the original key. Ambiguous response loss must remain in-flight
// and be reconciled; it must never create a fresh publication key.
func (s *Store) FailTeamPublicationRemoteAbsent(
	ctx context.Context,
	intentID, approvalID, envelopeDigest string,
	at time.Time,
) (TeamPublicationIntent, error) {
	if s.storeKind != StoreKindDesk || !validTrayIdentifier(intentID) ||
		!validTeamOpaque(approvalID, 8, 255) || !validDigest(envelopeDigest) || at.IsZero() {
		return TeamPublicationIntent{}, ErrTeamPublicationInvalid
	}
	atText := at.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `
		UPDATE team_publication_intents
		   SET state='failed', failure_code=?, updated_at=?, terminal_at=?
		 WHERE intent_id=? AND approval_id=? AND envelope_digest=? AND state='in_flight'
		   AND remote_receipt_id IS NULL`,
		TeamPublicationFailureRemoteAbsent, atText, atText,
		intentID, approvalID, envelopeDigest,
	)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		intent, loadErr := s.teamPublicationIntentByID(ctx, intentID)
		if loadErr == nil && intent.State == TeamPublicationFailed &&
			intent.ApprovalID == approvalID && intent.EnvelopeDigest == envelopeDigest {
			intent.Replayed = true
			return intent, nil
		}
		return TeamPublicationIntent{}, ErrTeamPublicationIdempotencyConflict
	}
	return s.teamPublicationIntentByID(ctx, intentID)
}

func (s *Store) terminateTeamPublicationIntent(
	ctx context.Context,
	intentID, envelopeDigest, state, failureCode string,
	at time.Time,
	requireExpired bool,
) (TeamPublicationIntent, error) {
	if s.storeKind != StoreKindDesk || !validTrayIdentifier(intentID) ||
		!validDigest(envelopeDigest) || at.IsZero() ||
		(state != TeamPublicationCanceled && state != TeamPublicationExpired) {
		return TeamPublicationIntent{}, ErrTeamPublicationInvalid
	}
	atText := at.UTC().Format(time.RFC3339Nano)
	expiryPredicate := ""
	if requireExpired {
		expiryPredicate = " AND expires_at <= ?"
	}
	query := `
		UPDATE team_publication_intents
		   SET state=?, failure_code=?, updated_at=?, terminal_at=?
		 WHERE intent_id=? AND envelope_digest=? AND state IN ('prepared','approved')` + expiryPredicate
	arguments := []any{state, nullablePublicationFailure(failureCode), atText, atText, intentID, envelopeDigest}
	if requireExpired {
		arguments = append(arguments, atText)
	}
	result, err := s.db.ExecContext(ctx, query, arguments...)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		intent, loadErr := s.teamPublicationIntentByID(ctx, intentID)
		if loadErr == nil && intent.State == state && intent.EnvelopeDigest == envelopeDigest {
			intent.Replayed = true
			return intent, nil
		}
		return TeamPublicationIntent{}, ErrTeamPublicationIdempotencyConflict
	}
	return s.teamPublicationIntentByID(ctx, intentID)
}

func nullablePublicationFailure(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// purgeDeskPublicationIntentsTx removes the disclosure payload before its Desk
// source is deleted. Ambiguous remote work remains a hard barrier: losing the
// exact envelope while a remote commit may have happened would make the saga
// impossible to reconcile safely.
func (s *Store) purgeDeskPublicationIntentsTx(
	tx *sql.Tx,
	sourceObjectID string,
	now time.Time,
	removeEvidence bool,
) error {
	if s.storeKind != StoreKindDesk {
		return nil
	}
	predicate := ""
	arguments := make([]any, 0, 4)
	if sourceObjectID != "" {
		predicate = " AND source_object_id = ?"
		arguments = append(arguments, sourceObjectID)
	}
	var ambiguous int
	if err := tx.QueryRow(`
		SELECT count(*) FROM team_publication_intents
		 WHERE state IN ('in_flight','remote_committed_local_pending')`+predicate,
		arguments...,
	).Scan(&ambiguous); err != nil {
		return err
	}
	if ambiguous != 0 {
		return ErrTeamPublicationDeletionBarrier
	}

	nowText := now.UTC().Format(time.RFC3339Nano)
	cancelArguments := []any{nowText, nowText, nowText}
	cancelArguments = append(cancelArguments, arguments...)
	if _, err := tx.Exec(`
		UPDATE team_publication_intents
		   SET state='canceled', envelope_json='{}', disclosure_purged_at=?,
		       updated_at=?, terminal_at=?
		 WHERE disclosure_purged_at IS NULL
		   AND state IN ('prepared','approved')`+predicate,
		cancelArguments...,
	); err != nil {
		return err
	}
	terminalArguments := []any{nowText, nowText}
	terminalArguments = append(terminalArguments, arguments...)
	if _, err := tx.Exec(`
		UPDATE team_publication_intents
		   SET envelope_json='{}', disclosure_purged_at=?, updated_at=?
		 WHERE disclosure_purged_at IS NULL
		   AND state IN ('reconciled','failed','canceled','expired')`+predicate,
		terminalArguments...,
	); err != nil {
		return err
	}
	if !removeEvidence {
		return nil
	}
	_, err := tx.Exec(`DELETE FROM team_publication_intents WHERE 1=1`+predicate, arguments...)
	return err
}

func (s *Store) RecordTeamPublicationRemoteCommit(
	ctx context.Context,
	receipt TeamPublicationRemoteReceipt,
) (TeamPublicationIntent, error) {
	return s.recordTeamPublicationRemoteReceipt(ctx, receipt, false)
}

func (s *Store) ReconcileTeamPublication(
	ctx context.Context,
	receipt TeamPublicationRemoteReceipt,
) (TeamPublicationIntent, error) {
	return s.recordTeamPublicationRemoteReceipt(ctx, receipt, true)
}

func (s *Store) recordTeamPublicationRemoteReceipt(
	ctx context.Context,
	receipt TeamPublicationRemoteReceipt,
	reconcile bool,
) (TeamPublicationIntent, error) {
	if s.storeKind != StoreKindDesk || !validTrayIdentifier(receipt.IdempotencyKey) ||
		!validDigest(receipt.EnvelopeDigest) || !validTeamOpaque(receipt.ObjectID, 8, 255) ||
		!validTeamOpaque(receipt.ReceiptID, 8, 255) || !validTeamOpaque(receipt.AuditEventID, 8, 255) ||
		receipt.RecordedAt.IsZero() {
		return TeamPublicationIntent{}, ErrTeamPublicationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	defer tx.Rollback()
	intent, found, err := loadTeamPublicationIntentTx(ctx, tx, receipt.IdempotencyKey)
	if err != nil || !found {
		if err != nil {
			return TeamPublicationIntent{}, err
		}
		return TeamPublicationIntent{}, ErrTeamPublicationSourceMismatch
	}
	if intent.EnvelopeDigest != receipt.EnvelopeDigest {
		return TeamPublicationIntent{}, ErrTeamPublicationIdempotencyConflict
	}
	if intent.ApprovedAt == nil || receipt.RecordedAt.UTC().Before(intent.ApprovedAt.UTC()) {
		return TeamPublicationIntent{}, ErrTeamPublicationInvalid
	}
	if intent.RemoteReceiptID != "" {
		if !sameTeamPublicationRemoteReceipt(intent, receipt) {
			return TeamPublicationIntent{}, ErrTeamPublicationIdempotencyConflict
		}
		if reconcile && intent.State == TeamPublicationRemoteCommittedLocalPending {
			at := receipt.RecordedAt.UTC().Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `
				UPDATE team_publication_intents
				   SET state='reconciled', updated_at=?, terminal_at=?
				 WHERE intent_id=? AND state='remote_committed_local_pending'`,
				at, at, intent.IntentID,
			); err != nil {
				return TeamPublicationIntent{}, err
			}
			if err := tx.Commit(); err != nil {
				return TeamPublicationIntent{}, err
			}
			return s.TeamPublicationIntentByKey(ctx, receipt.IdempotencyKey)
		}
		intent.Replayed = true
		return intent, nil
	}
	if intent.State != TeamPublicationInFlight {
		return TeamPublicationIntent{}, ErrTeamPublicationIdempotencyConflict
	}
	state := TeamPublicationRemoteCommittedLocalPending
	var terminal any
	if reconcile {
		state = TeamPublicationReconciled
		terminal = receipt.RecordedAt.UTC().Format(time.RFC3339Nano)
	}
	at := receipt.RecordedAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `
		UPDATE team_publication_intents
		   SET state=?, remote_object_id=?, remote_receipt_id=?, remote_audit_event_id=?,
		       remote_content_digest=?, updated_at=?, terminal_at=?
		 WHERE intent_id=? AND state='in_flight'`,
		state, receipt.ObjectID, receipt.ReceiptID, receipt.AuditEventID,
		receipt.EnvelopeDigest, at, terminal, intent.IntentID,
	)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamPublicationIntent{}, err
	}
	return s.TeamPublicationIntentByKey(ctx, receipt.IdempotencyKey)
}

func (s *Store) TeamPublicationIntentByKey(ctx context.Context, idempotencyKey string) (TeamPublicationIntent, error) {
	if s.storeKind != StoreKindDesk || !validTrayIdentifier(idempotencyKey) {
		return TeamPublicationIntent{}, ErrTeamPublicationInvalid
	}
	intent, found, err := loadTeamPublicationIntentTx(ctx, s.db, idempotencyKey)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	if !found {
		return TeamPublicationIntent{}, ErrTeamPublicationSourceMismatch
	}
	return intent, nil
}

func (s *Store) teamPublicationIntentByID(ctx context.Context, intentID string) (TeamPublicationIntent, error) {
	intent, found, err := loadTeamPublicationIntentByIDTx(ctx, s.db, intentID)
	if err != nil {
		return TeamPublicationIntent{}, err
	}
	if !found {
		return TeamPublicationIntent{}, ErrTeamPublicationSourceMismatch
	}
	return intent, nil
}

func normalizeTeamPublicationEnvelope(request TeamPublicationPrepareRequest) (teamPublicationEnvelope, []byte, string, error) {
	invalid := func() (teamPublicationEnvelope, []byte, string, error) {
		return teamPublicationEnvelope{}, nil, "", ErrTeamPublicationInvalid
	}
	if len(request.CanonicalEnvelope) < 2 || len(request.CanonicalEnvelope) > teamPublicationMaxBytes ||
		!validDigest(request.EnvelopeDigest) {
		return invalid()
	}
	canonical, prefixedDigest, err := capture.CanonicalizeEnvelopeJSON(request.CanonicalEnvelope, teamPublicationTopLevel)
	if err != nil || !bytes.Equal(canonical, request.CanonicalEnvelope) {
		return invalid()
	}
	digest := strings.TrimPrefix(prefixedDigest, "sha256:")
	if digest != request.EnvelopeDigest {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var envelope teamPublicationEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return invalid()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return invalid()
	}
	if envelope.Schema != TeamPublicationEnvelopeSchema || envelope.Action != teamPublicationAction ||
		envelope.TargetKind != "commons" || envelope.TargetID != envelope.TeamID ||
		envelope.PublicationKey != request.IdempotencyKey ||
		envelope.PolicyEpoch < 1 || envelope.Metadata == nil || envelope.Metadata.Tags == nil ||
		request.DeploymentID != envelope.DeploymentID || request.RemoteStoreID != envelope.StoreID ||
		request.TeamID != envelope.TeamID || request.PolicyEpoch != envelope.PolicyEpoch ||
		request.WriterPrincipalID != envelope.WriterPrincipalID || request.ClientKey != envelope.ClientKey ||
		request.WriterID != envelope.WriterID || !validPublicationID(envelope.DeploymentID, "deployment_") ||
		!validPublicationID(envelope.StoreID, "store_") || !validPublicationID(envelope.TeamID, "team_") ||
		!validPublicationID(envelope.WriterPrincipalID, "principal_") || !teamPublicationOpaque.MatchString(envelope.WriterID) ||
		!validDigest(envelope.ClientKey) || !validTrayIdentifier(envelope.PublicationKey) ||
		teamPublicationPrivateReference.MatchString(envelope.PublicationKey) ||
		!validTeamPublicationTimestamp(envelope.SourceTimestamp) ||
		!validTeamPublicationText(envelope.Content, 1200) ||
		!validKind(envelope.Metadata.Kind) || len(*envelope.Metadata.Tags) > 20 {
		return invalid()
	}
	tags := *envelope.Metadata.Tags
	seenTags := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if !validTeamMemoryTag(tag) || !validTeamPublicationText(tag, 64) {
			return invalid()
		}
		if _, duplicate := seenTags[tag]; duplicate {
			return invalid()
		}
		seenTags[tag] = struct{}{}
	}
	for index := 1; index < len(tags); index++ {
		if tags[index-1] >= tags[index] {
			return invalid()
		}
	}
	return envelope, canonical, digest, nil
}

func validTeamPublicationTimestamp(value string) bool {
	canonical, _, ok := canonicalTeamMemoryOptionalTime(value)
	return ok && canonical == value
}

func validDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validPublicationID(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && teamPublicationOpaque.MatchString(value)
}

func validTeamPublicationText(value string, maximum int) bool {
	if value == "" || strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > maximum ||
		strings.ContainsAny(value, "\r\n\t") ||
		!norm.NFKC.IsNormalString(value) || looksUnsafeTeamMemoryText(value) ||
		teamPublicationHTML.MatchString(value) || teamPublicationActive.MatchString(value) ||
		teamPublicationSecret.MatchString(value) || teamPublicationPath.MatchString(value) ||
		teamPublicationPrivateReference.MatchString(value) {
		return false
	}
	latin, cyrillic, greek := false, false, false
	reset := func() bool {
		mixed := 0
		for _, present := range []bool{latin, cyrillic, greek} {
			if present {
				mixed++
			}
		}
		latin, cyrillic, greek = false, false, false
		return mixed <= 1
	}
	for _, r := range value {
		if unicode.In(r, unicode.Cf) {
			return false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == ':' || r == '-' {
			latin = latin || unicode.In(r, unicode.Latin)
			cyrillic = cyrillic || unicode.In(r, unicode.Cyrillic)
			greek = greek || unicode.In(r, unicode.Greek)
			continue
		}
		if !reset() {
			return false
		}
	}
	return reset()
}

func loadTeamPublicationIntentTx(ctx context.Context, q queryer, idempotencyKey string) (TeamPublicationIntent, bool, error) {
	return loadTeamPublicationIntentRow(q.QueryRowContext(ctx, `
		SELECT intent_id, source_object_id, source_content_digest,
		       deployment_id, store_id, team_id, policy_epoch,
		       writer_principal_id, client_key, writer_id, state,
		       envelope_digest, envelope_json, idempotency_key,
		       expires_at, created_at,
		       COALESCE(approval_id, ''), COALESCE(approval_digest, ''), approved_at,
		       COALESCE(remote_object_id, ''), COALESCE(remote_receipt_id, ''),
		       COALESCE(remote_audit_event_id, ''), COALESCE(remote_content_digest, ''),
		       COALESCE(failure_code, ''), terminal_at
		  FROM team_publication_intents WHERE idempotency_key=?`, idempotencyKey))
}

func loadTeamPublicationIntentByIDTx(ctx context.Context, q queryer, intentID string) (TeamPublicationIntent, bool, error) {
	return loadTeamPublicationIntentRow(q.QueryRowContext(ctx, `
		SELECT intent_id, source_object_id, source_content_digest,
		       deployment_id, store_id, team_id, policy_epoch,
		       writer_principal_id, client_key, writer_id, state,
		       envelope_digest, envelope_json, idempotency_key,
		       expires_at, created_at,
		       COALESCE(approval_id, ''), COALESCE(approval_digest, ''), approved_at,
		       COALESCE(remote_object_id, ''), COALESCE(remote_receipt_id, ''),
		       COALESCE(remote_audit_event_id, ''), COALESCE(remote_content_digest, ''),
		       COALESCE(failure_code, ''), terminal_at
		  FROM team_publication_intents WHERE intent_id=?`, intentID))
}

func loadTeamPublicationIntentRow(row *sql.Row) (TeamPublicationIntent, bool, error) {
	var result TeamPublicationIntent
	var envelope string
	var expiresAt, createdAt string
	var approvedAt, terminalAt sql.NullString
	err := row.Scan(
		&result.IntentID, &result.SourceObjectID, &result.SourceContentDigest,
		&result.DeploymentID, &result.RemoteStoreID, &result.TeamID, &result.PolicyEpoch,
		&result.WriterPrincipalID, &result.ClientKey, &result.WriterID, &result.State,
		&result.EnvelopeDigest, &envelope, &result.IdempotencyKey, &expiresAt, &createdAt,
		&result.ApprovalID, &result.ApprovalDigest, &approvedAt,
		&result.RemoteObjectID, &result.RemoteReceiptID,
		&result.RemoteAuditEventID, &result.RemoteContentDigest,
		&result.FailureCode, &terminalAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamPublicationIntent{}, false, nil
	}
	if err != nil {
		return TeamPublicationIntent{}, false, err
	}
	result.CanonicalEnvelope = []byte(envelope)
	result.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return TeamPublicationIntent{}, false, err
	}
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err == nil && approvedAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, approvedAt.String)
		if parseErr != nil {
			return TeamPublicationIntent{}, false, parseErr
		}
		result.ApprovedAt = &parsed
	}
	if err == nil && terminalAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, terminalAt.String)
		if parseErr != nil {
			return TeamPublicationIntent{}, false, parseErr
		}
		result.TerminalAt = &parsed
	}
	return result, true, err
}

func sameTeamPublicationRemoteReceipt(intent TeamPublicationIntent, receipt TeamPublicationRemoteReceipt) bool {
	return intent.EnvelopeDigest == receipt.EnvelopeDigest &&
		intent.RemoteObjectID == receipt.ObjectID && intent.RemoteReceiptID == receipt.ReceiptID &&
		intent.RemoteAuditEventID == receipt.AuditEventID &&
		intent.RemoteContentDigest == receipt.EnvelopeDigest
}

func sameTeamPublicationIntent(existing TeamPublicationIntent, request TeamPublicationPrepareRequest, canonical []byte, digest string) bool {
	return existing.SourceObjectID == request.SourceObjectID &&
		existing.SourceContentDigest == request.SourceContentDigest &&
		existing.DeploymentID == request.DeploymentID && existing.RemoteStoreID == request.RemoteStoreID &&
		existing.TeamID == request.TeamID && existing.PolicyEpoch == request.PolicyEpoch &&
		existing.WriterPrincipalID == request.WriterPrincipalID && existing.ClientKey == request.ClientKey &&
		existing.WriterID == request.WriterID && existing.EnvelopeDigest == digest &&
		bytes.Equal(existing.CanonicalEnvelope, canonical) && existing.ExpiresAt.Equal(request.ExpiresAt.UTC())
}
