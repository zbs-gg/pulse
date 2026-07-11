package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

var (
	ErrTeamObjectInvalid                   = errors.New("team_object_invalid")
	ErrTeamObjectCommitFailed              = errors.New("team_object_commit_failed")
	ErrTeamIdempotencyConflict             = errors.New("idempotency_conflict")
	ErrTeamIdempotencyInProgress           = errors.New("idempotency_in_progress")
	ErrTeamIdempotencyFailed               = errors.New("idempotency_failed")
	errTeamObjectExtensionStatementInvalid = errors.New("team_object_extension_statement_invalid")
)

const (
	TeamObjectStatusStored     = "stored"
	TeamProjectionStatePending = "pending"
	maxTeamObjectBodyBytes     = 4 << 20
	maxTeamProjectionKinds     = 16
	maxTeamIdempotencyKeyBytes = 255
)

// TeamWriterLeaseIdentity is the minimum writer identity a mutation needs.
// The raw token is checked inside the mutation transaction and is never
// persisted; team_writer_leases stores only its domain-separated hash.
type TeamWriterLeaseIdentity struct {
	WriterID string
	Token    string
}

// TeamObjectPolicy contains handling policy that is persisted on the
// canonical root. Scope and ownership never come from this request: those are
// copied only from the sealed TeamMutationPermit.
type TeamObjectPolicy struct {
	PrivacyTier string
	Retention   string
	ExpiresAt   *time.Time
}

type TeamObjectWriteRequest struct {
	Permit          TeamMutationPermit
	Writer          TeamWriterLeaseIdentity
	RequestID       string
	OAuthClientKey  string
	IdempotencyKey  string
	Body            []byte
	BodyDigest      string
	Policy          TeamObjectPolicy
	ProjectionKinds []string
}

type TeamProjectionJobResult struct {
	Kind  string
	JobID string
	State string
}

// TeamObjectWriteResult deliberately separates durable storage from
// asynchronous projection completion. This write path can report only stored
// plus pending projection work; ready is owned by the worker/completion path.
type TeamObjectWriteResult struct {
	ObjectID        string
	AuditEventID    string
	Status          string
	ProjectionState string
	ProjectionJobs  []TeamProjectionJobResult
	FullyProjected  bool
	Replayed        bool
}

type normalizedTeamObjectWrite struct {
	permit            TeamMutationPermit
	attribution       TeamMutationAttribution
	writer            TeamWriterLeaseIdentity
	requestID         string
	clientKey         string
	idempotencyHash   string
	bodyDigest        string
	operationDigest   string
	objectKind        string
	target            teamauth.CanonicalScope
	privacyTier       string
	retention         string
	expiresAt         *time.Time
	expiryDigestValue string
	sessionBound      bool
	projectionKinds   []string
}

// teamObjectWriteTransaction is package-private on purpose. U9/U10 domain
// writers can attach their content-bearing rows to the same transaction by
// calling storeTeamObjectWithExtension. The closure-backed facade accepts only
// one validated DML/SELECT statement and exposes no raw or commit-capable tx.
type teamObjectWriteTransaction struct {
	execDML          func(context.Context, string, ...any) (sql.Result, error)
	queryRow         func(context.Context, string, ...any) (*sql.Row, error)
	mapStorage       func(context.Context, string, string) error
	insertTeamMemory func(context.Context, teamMemoryCapsuleStorageItem) error
	ObjectID         string
	StoreID          string
	TeamID           string
	AuthorID         string
	OAuthClientKey   string
	BodyDigest       string
	Scope            teamauth.CanonicalScope
}

type teamObjectWriteExtension func(context.Context, *teamObjectWriteTransaction) error

func newTeamObjectWriteTransaction(tx *sql.Tx, now time.Time, write teamObjectWriteTransaction) *teamObjectWriteTransaction {
	write.execDML = func(ctx context.Context, statement string, args ...any) (sql.Result, error) {
		if !validTeamObjectExtensionStatement(statement, false) {
			return nil, errTeamObjectExtensionStatementInvalid
		}
		return tx.ExecContext(ctx, statement, args...)
	}
	write.queryRow = func(ctx context.Context, statement string, args ...any) (*sql.Row, error) {
		if !validTeamObjectExtensionStatement(statement, true) {
			return nil, errTeamObjectExtensionStatementInvalid
		}
		return tx.QueryRowContext(ctx, statement, args...), nil
	}
	write.mapStorage = func(ctx context.Context, representationKind, storageKey string) error {
		if !validTeamClass(representationKind, 64) || isProjectionStateName(representationKind) ||
			!validTeamOpaque(storageKey, 1, 255) {
			return errTeamObjectExtensionStatementInvalid
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_object_storage_map(
				object_id, team_id, scope_type, scope_id, generation,
				representation_kind, storage_key, created_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
			write.ObjectID, write.TeamID, write.Scope.Type, write.Scope.ID,
			representationKind, storageKey, now.UTC().Format(time.RFC3339Nano),
		)
		return err
	}
	write.insertTeamMemory = func(ctx context.Context, item teamMemoryCapsuleStorageItem) error {
		if !validTeamMemoryCapsuleStorageItem(item) {
			return ErrTeamMemoryInvalid
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_memory_capsules(
				capsule_id, root_object_id, team_id, scope_type, scope_id,
				root_generation, item_ordinal, schema_version, source_host,
				conversation_scope, source_timestamp, kind, redacted_summary,
				confidence, evidence_hint, tags_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.CapsuleID, write.ObjectID, write.TeamID, write.Scope.Type, write.Scope.ID,
			write.Scope.Generation, item.Ordinal, TeamMemorySchema, item.Source.Host,
			item.Source.ConversationScope, item.Source.Timestamp, item.Item.Kind,
			item.Item.RedactedSummary, item.Item.Confidence, item.Item.EvidenceHint,
			item.TagsJSON, now.UTC().Format(time.RFC3339Nano),
		)
		return err
	}
	return &write
}

func (write *teamObjectWriteTransaction) ExecContext(
	ctx context.Context,
	statement string,
	args ...any,
) (sql.Result, error) {
	if write == nil || write.execDML == nil {
		return nil, errTeamObjectExtensionStatementInvalid
	}
	return write.execDML(ctx, statement, args...)
}

func (write *teamObjectWriteTransaction) QueryRowContext(
	ctx context.Context,
	statement string,
	args ...any,
) (*sql.Row, error) {
	if write == nil || write.queryRow == nil {
		return nil, errTeamObjectExtensionStatementInvalid
	}
	return write.queryRow(ctx, statement, args...)
}

func (write *teamObjectWriteTransaction) MapStorage(
	ctx context.Context,
	representationKind, storageKey string,
) error {
	if write == nil || write.mapStorage == nil {
		return errTeamObjectExtensionStatementInvalid
	}
	return write.mapStorage(ctx, representationKind, storageKey)
}

func (write *teamObjectWriteTransaction) InsertTeamMemoryCapsuleItem(
	ctx context.Context,
	item teamMemoryCapsuleStorageItem,
) error {
	if write == nil || write.insertTeamMemory == nil {
		return ErrTeamMemoryInvalid
	}
	return write.insertTeamMemory(ctx, item)
}

func (s *Store) StoreTeamObject(ctx context.Context, request TeamObjectWriteRequest) (TeamObjectWriteResult, error) {
	return s.storeTeamObjectWithExtension(ctx, request, nil)
}

func (s *Store) storeTeamObjectWithExtension(
	ctx context.Context,
	request TeamObjectWriteRequest,
	extension teamObjectWriteExtension,
) (TeamObjectWriteResult, error) {
	normalized, err := s.normalizeTeamObjectWrite(request)
	if err != nil {
		return TeamObjectWriteResult{}, err
	}

	// Team Open adds _txlock=immediate to every connection, so BeginTx takes
	// the single SQLite writer reservation before any idempotency observation.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamObjectWriteResult{}, teamObjectCommitError(err)
	}
	defer tx.Rollback()

	// Both authority-bearing snapshots are rechecked before replay or writes.
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, normalized.writer.WriterID, normalized.writer.Token); err != nil {
		return TeamObjectWriteResult{}, err
	}
	if err := s.RecheckTeamMutationPermitTx(ctx, tx, normalized.permit); err != nil {
		return TeamObjectWriteResult{}, err
	}

	replayed, found, err := s.loadTeamObjectIdempotency(ctx, tx, normalized)
	if err != nil {
		return TeamObjectWriteResult{}, err
	}
	if found {
		if err := tx.Commit(); err != nil {
			return TeamObjectWriteResult{}, teamObjectCommitError(err)
		}
		return replayed, nil
	}

	now := s.clock().UTC()
	if normalized.expiresAt != nil && (!normalized.expiresAt.After(now) ||
		(normalized.sessionBound && normalized.expiresAt.After(now.Add(24*time.Hour)))) {
		return TeamObjectWriteResult{}, ErrTeamObjectInvalid
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_idempotency_records(
			team_id, principal_id, client_key, action, idempotency_key_hash,
			body_digest, state, object_id, audit_event_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', NULL, NULL, ?, ?)`,
		normalized.attribution.TeamID, normalized.attribution.ActorPrincipalID,
		normalized.clientKey, teamObjectWriteAction, normalized.idempotencyHash,
		normalized.operationDigest, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	); err != nil {
		return TeamObjectWriteResult{}, teamObjectCommitError(err)
	}

	objectID, err := newOpaqueID("object")
	if err != nil {
		return TeamObjectWriteResult{}, teamObjectCommitError(err)
	}
	var expiresAt any
	if normalized.expiresAt != nil {
		expiresAt = normalized.expiresAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, 'active', 1, ?, ?, ?)`,
		objectID, normalized.attribution.StoreID, normalized.attribution.TeamID,
		normalized.objectKind, normalized.target.Type, normalized.target.ID,
		normalized.target.OwnerPrincipalID, normalized.attribution.ActorPrincipalID,
		normalized.privacyTier, normalized.retention, expiresAt,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	); err != nil {
		return TeamObjectWriteResult{}, teamObjectCommitError(err)
	}

	if extension != nil {
		write := newTeamObjectWriteTransaction(tx, now, teamObjectWriteTransaction{
			ObjectID: objectID,
			StoreID:  normalized.attribution.StoreID, TeamID: normalized.attribution.TeamID,
			AuthorID:       normalized.attribution.ActorPrincipalID,
			OAuthClientKey: normalized.clientKey, BodyDigest: normalized.bodyDigest,
			Scope: normalized.target,
		})
		if err := extension(ctx, write); err != nil {
			return TeamObjectWriteResult{}, teamObjectCommitError(err)
		}
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, normalized.writer.WriterID, normalized.writer.Token); err != nil {
		return TeamObjectWriteResult{}, err
	}

	projectID := normalized.permit.context.ProjectID
	if projectID == "" && normalized.target.Type == teamauth.ScopeProject {
		projectID = normalized.target.ID
	}
	auditEventID, err := appendTeamDomainAudit(ctx, tx, teamDomainAuditEvent{
		StoreID: normalized.attribution.StoreID, TeamID: normalized.attribution.TeamID,
		ProjectID: projectID, ActorPrincipal: normalized.attribution.ActorPrincipalID,
		OAuthClientKey: normalized.clientKey, RequestID: normalized.requestID,
		Action: teamObjectWriteAction, TargetKind: normalized.objectKind, TargetID: objectID,
		PolicyVersion: teamauth.PolicyVersion, AuthorizationAt: normalized.permit.PolicyEpoch().Global,
		ReasonCode: teamObjectStoredReason, OccurredAt: now,
	})
	if err != nil {
		return TeamObjectWriteResult{}, teamObjectCommitError(err)
	}

	jobs := make([]TeamProjectionJobResult, 0, len(normalized.projectionKinds))
	for _, kind := range normalized.projectionKinds {
		jobID, err := newOpaqueID("projection_job")
		if err != nil {
			return TeamObjectWriteResult{}, teamObjectCommitError(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_projection_jobs(
				job_id, store_id, team_id, root_object_id, root_generation,
				scope_type, scope_id, projection_kind, state, attempt_count,
				lease_token_hash, lease_expires_at, next_attempt_at, last_error_code,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, ?, 'pending', 0, NULL, NULL, ?, NULL, ?, ?)`,
			jobID, normalized.attribution.StoreID, normalized.attribution.TeamID,
			objectID, normalized.target.Type, normalized.target.ID, kind,
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		); err != nil {
			return TeamObjectWriteResult{}, teamObjectCommitError(err)
		}
		jobs = append(jobs, TeamProjectionJobResult{Kind: kind, JobID: jobID, State: TeamProjectionStatePending})
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, normalized.writer.WriterID, normalized.writer.Token); err != nil {
		return TeamObjectWriteResult{}, err
	}

	updated, err := tx.ExecContext(ctx, `
		UPDATE team_idempotency_records
		   SET state = 'stored', object_id = ?, audit_event_id = ?, updated_at = ?
		 WHERE team_id = ? AND principal_id = ? AND client_key = ? AND action = ?
		   AND idempotency_key_hash = ? AND body_digest = ? AND state = 'pending'`,
		objectID, auditEventID, now.Format(time.RFC3339Nano),
		normalized.attribution.TeamID, normalized.attribution.ActorPrincipalID,
		normalized.clientKey, teamObjectWriteAction, normalized.idempotencyHash,
		normalized.operationDigest,
	)
	if err != nil {
		return TeamObjectWriteResult{}, teamObjectCommitError(err)
	}
	rows, err := updated.RowsAffected()
	if err != nil || rows != 1 {
		return TeamObjectWriteResult{}, teamObjectCommitError(err)
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, normalized.writer.WriterID, normalized.writer.Token); err != nil {
		return TeamObjectWriteResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamObjectWriteResult{}, teamObjectCommitError(err)
	}
	return TeamObjectWriteResult{
		ObjectID: objectID, AuditEventID: auditEventID,
		Status: TeamObjectStatusStored, ProjectionState: TeamProjectionStatePending,
		ProjectionJobs: jobs, FullyProjected: false, Replayed: false,
	}, nil
}

func (s *Store) normalizeTeamObjectWrite(request TeamObjectWriteRequest) (normalizedTeamObjectWrite, error) {
	invalid := func() (normalizedTeamObjectWrite, error) {
		return normalizedTeamObjectWrite{}, ErrTeamObjectInvalid
	}
	if !validTeamMutationPermit(request.Permit) || request.Permit.Action() != teamauth.ActionWrite ||
		request.Permit.ExistingObjectID() != "" || !validTeamClass(request.Permit.ObjectKind(), 64) {
		return invalid()
	}
	attribution := request.Permit.Attribution()
	if !lowerHexDigest(request.OAuthClientKey) ||
		subtle.ConstantTimeCompare([]byte(request.OAuthClientKey), []byte(attribution.OAuthClientKey)) != 1 ||
		(attribution.PrincipalKind != teamauth.PrincipalAgent && attribution.PrincipalKind != teamauth.PrincipalService) ||
		!validTeamOpaque(request.Writer.WriterID, 1, 255) ||
		!validTeamOpaque(request.Writer.Token, 1, 255) ||
		!validTeamOpaque(request.RequestID, 8, 64) ||
		!validRawTeamIdempotencyKey(request.IdempotencyKey) ||
		len(request.Body) > maxTeamObjectBodyBytes || !lowerHexDigest(request.BodyDigest) {
		return invalid()
	}
	computedDigest := sha256.Sum256(request.Body)
	computedHex := hex.EncodeToString(computedDigest[:])
	if subtle.ConstantTimeCompare([]byte(computedHex), []byte(request.BodyDigest)) != 1 {
		return invalid()
	}

	target := request.Permit.EffectiveTarget()
	if target.TeamID != attribution.TeamID || target.Lifecycle != teamauth.LifecycleActive || target.Generation != 1 ||
		!validTeamObjectScope(target) || !validTeamPrivacy(request.Policy.PrivacyTier) ||
		!validTeamRetention(request.Policy.Retention) ||
		(target.PrivacyTier != "" && target.PrivacyTier != request.Policy.PrivacyTier) ||
		(target.Retention != "" && target.Retention != request.Policy.Retention) {
		return invalid()
	}

	var expiresAt *time.Time
	expiryDigestValue := "none"
	sessionBound := target.Type == teamauth.ScopeSession || request.Policy.Retention == "session"
	if request.Policy.ExpiresAt != nil {
		expires := request.Policy.ExpiresAt.UTC()
		if expires.IsZero() {
			return invalid()
		}
		expiresAt = &expires
		expiryDigestValue = "at:" + expires.Format(time.RFC3339Nano)
	}
	if sessionBound && expiresAt == nil {
		expires := s.clock().UTC().Add(24 * time.Hour)
		expiresAt = &expires
		// Bind retries to the caller's stable default policy rather than the
		// newly derived wall-clock timestamp. The first commit stores the exact
		// resolved expiry and subsequent retries replay that durable result.
		expiryDigestValue = "default_24h"
	}

	if len(request.ProjectionKinds) == 0 || len(request.ProjectionKinds) > maxTeamProjectionKinds {
		return invalid()
	}
	projectionKinds := append([]string(nil), request.ProjectionKinds...)
	for _, kind := range projectionKinds {
		if !validTeamClass(kind, 64) || isProjectionStateName(kind) {
			return invalid()
		}
	}
	sort.Strings(projectionKinds)
	for index := 1; index < len(projectionKinds); index++ {
		if projectionKinds[index] == projectionKinds[index-1] {
			return invalid()
		}
	}

	keyDigest := sha256.Sum256([]byte("pulse-team-idempotency-key-v1\x00" + request.IdempotencyKey))
	normalized := normalizedTeamObjectWrite{
		permit: request.Permit, attribution: attribution, writer: request.Writer,
		requestID: request.RequestID, clientKey: request.OAuthClientKey,
		idempotencyHash: hex.EncodeToString(keyDigest[:]), bodyDigest: request.BodyDigest,
		objectKind: request.Permit.ObjectKind(), target: target,
		privacyTier: request.Policy.PrivacyTier, retention: request.Policy.Retention,
		expiresAt: expiresAt, expiryDigestValue: expiryDigestValue,
		sessionBound: sessionBound, projectionKinds: projectionKinds,
	}
	normalized.operationDigest = canonicalTeamObjectOperationDigest(normalized)
	return normalized, nil
}

func (s *Store) loadTeamObjectIdempotency(
	ctx context.Context,
	tx *sql.Tx,
	write normalizedTeamObjectWrite,
) (TeamObjectWriteResult, bool, error) {
	var bodyDigest, state, objectID, auditEventID string
	err := tx.QueryRowContext(ctx, `
		SELECT body_digest, state, COALESCE(object_id, ''), COALESCE(audit_event_id, '')
		  FROM team_idempotency_records
		 WHERE team_id = ? AND principal_id = ? AND client_key = ? AND action = ?
		   AND idempotency_key_hash = ?`,
		write.attribution.TeamID, write.attribution.ActorPrincipalID,
		write.clientKey, teamObjectWriteAction, write.idempotencyHash,
	).Scan(&bodyDigest, &state, &objectID, &auditEventID)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamObjectWriteResult{}, false, nil
	}
	if err != nil {
		return TeamObjectWriteResult{}, false, teamObjectCommitError(err)
	}
	if subtle.ConstantTimeCompare([]byte(bodyDigest), []byte(write.operationDigest)) != 1 {
		return TeamObjectWriteResult{}, true, ErrTeamIdempotencyConflict
	}
	switch state {
	case "pending":
		return TeamObjectWriteResult{}, true, ErrTeamIdempotencyInProgress
	case "failed":
		return TeamObjectWriteResult{}, true, ErrTeamIdempotencyFailed
	case "stored":
		if objectID == "" || auditEventID == "" {
			return TeamObjectWriteResult{}, true, teamObjectCommitError(errors.New("stored idempotency result is incomplete"))
		}
	default:
		return TeamObjectWriteResult{}, true, teamObjectCommitError(errors.New("unknown idempotency state"))
	}

	var lifecycle string
	var expiresAt sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT lifecycle, expires_at
		  FROM team_object_registry
		 WHERE object_id = ? AND store_id = ? AND team_id = ?`,
		objectID, write.attribution.StoreID, write.attribution.TeamID,
	).Scan(&lifecycle, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamObjectWriteResult{}, true, ErrConcealedNotFound
	}
	if err != nil {
		return TeamObjectWriteResult{}, true, teamObjectCommitError(err)
	}
	if lifecycle != string(teamauth.LifecycleActive) {
		return TeamObjectWriteResult{}, true, ErrConcealedNotFound
	}
	if expiresAt.Valid {
		expires, parseErr := time.Parse(time.RFC3339Nano, expiresAt.String)
		if parseErr != nil {
			return TeamObjectWriteResult{}, true, teamObjectCommitError(parseErr)
		}
		if !expires.After(s.clock().UTC()) {
			return TeamObjectWriteResult{}, true, ErrConcealedNotFound
		}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT projection_kind, job_id
		  FROM team_projection_jobs
		 WHERE root_object_id = ? AND root_generation = 1
		 ORDER BY projection_kind, job_id`, objectID)
	if err != nil {
		return TeamObjectWriteResult{}, true, teamObjectCommitError(err)
	}
	defer rows.Close()
	jobs := make([]TeamProjectionJobResult, 0, len(write.projectionKinds))
	for rows.Next() {
		var job TeamProjectionJobResult
		if err := rows.Scan(&job.Kind, &job.JobID); err != nil {
			return TeamObjectWriteResult{}, true, teamObjectCommitError(err)
		}
		job.State = TeamProjectionStatePending
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return TeamObjectWriteResult{}, true, teamObjectCommitError(err)
	}
	if len(jobs) == 0 {
		return TeamObjectWriteResult{}, true, teamObjectCommitError(errors.New("stored object has no projection jobs"))
	}
	return TeamObjectWriteResult{
		ObjectID: objectID, AuditEventID: auditEventID,
		Status: TeamObjectStatusStored, ProjectionState: TeamProjectionStatePending,
		ProjectionJobs: jobs, FullyProjected: false, Replayed: true,
	}, true, nil
}

func validTeamObjectScope(scope teamauth.CanonicalScope) bool {
	if !validExactIdentityValue(scope.TeamID) || !validExactIdentityValue(scope.ID) {
		return false
	}
	switch scope.Type {
	case teamauth.ScopePersonal:
		return scope.OwnerPrincipalID != "" && scope.ID == scope.OwnerPrincipalID
	case teamauth.ScopeTeam:
		return scope.OwnerPrincipalID == "" && scope.ID == scope.TeamID
	case teamauth.ScopeProject, teamauth.ScopeRepo, teamauth.ScopeAgent, teamauth.ScopeSession:
		return validExactIdentityValue(scope.OwnerPrincipalID)
	default:
		return false
	}
}

func validTeamPrivacy(value string) bool {
	return value == "normal" || value == "sensitive" || value == "private"
}

func validTeamRetention(value string) bool {
	return value == "session" || value == "project" || value == "long_term"
}

func validTeamOpaque(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for index := range len(value) {
		char := value[index]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func validRawTeamIdempotencyKey(value string) bool {
	if value == "" || len(value) > maxTeamIdempotencyKeyBytes || strings.TrimSpace(value) != value {
		return false
	}
	for index := range len(value) {
		if value[index] < 0x20 || value[index] == 0x7f {
			return false
		}
	}
	return true
}

func validTeamClass(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for index := range len(value) {
		char := value[index]
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '.' || char == ':' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func isProjectionStateName(value string) bool {
	switch value {
	case "pending", "leased", "ready", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func canonicalTeamObjectOperationDigest(write normalizedTeamObjectWrite) string {
	fields := []string{
		"pulse-team-object-operation-v1",
		teamObjectWriteAction,
		write.attribution.StoreID,
		write.attribution.TeamID,
		write.attribution.ActorPrincipalID,
		write.attribution.HumanPrincipalID,
		string(write.attribution.PrincipalKind),
		write.clientKey,
		write.objectKind,
		write.target.TeamID,
		string(write.target.Type),
		write.target.ID,
		write.target.OwnerPrincipalID,
		string(write.target.Lifecycle),
		strconv.FormatInt(write.target.Generation, 10),
		write.target.PrivacyTier,
		write.target.Retention,
		write.privacyTier,
		write.retention,
		write.expiryDigestValue,
		write.bodyDigest,
		strconv.Itoa(len(write.projectionKinds)),
	}
	fields = append(fields, write.projectionKinds...)
	hash := sha256.New()
	var size [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validTeamObjectExtensionStatement(statement string, query bool) bool {
	trimmed := strings.TrimSpace(statement)
	if trimmed == "" || strings.ContainsRune(trimmed, ';') || strings.ContainsRune(trimmed, '\x00') ||
		strings.Contains(trimmed, "--") || strings.Contains(trimmed, "/*") || strings.Contains(trimmed, "*/") {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	first := strings.ToUpper(fields[0])
	if query {
		if first != "SELECT" {
			return false
		}
	} else if first != "INSERT" && first != "UPDATE" && first != "DELETE" {
		return false
	}
	words := teamObjectSQLWords(strings.ToUpper(trimmed))
	for _, word := range words {
		if reservedTeamObjectExtensionTable(word) {
			return false
		}
		switch word {
		case "BEGIN", "COMMIT", "END", "ROLLBACK", "SAVEPOINT", "RELEASE",
			"ATTACH", "DETACH", "PRAGMA", "VACUUM", "ALTER", "CREATE", "DROP":
			return false
		}
	}
	if query {
		for index, word := range words[:len(words)-1] {
			if (word == "FROM" || word == "JOIN") && reservedTeamObjectExtensionTable(words[index+1]) {
				return false
			}
		}
		return true
	}
	table := ""
	switch first {
	case "INSERT":
		if len(words) < 3 || words[1] != "INTO" {
			return false
		}
		table = words[2]
	case "UPDATE":
		if len(words) < 2 {
			return false
		}
		table = words[1]
	case "DELETE":
		if len(words) < 3 || words[1] != "FROM" {
			return false
		}
		table = words[2]
	}
	if reservedTeamObjectExtensionTable(table) {
		return false
	}
	return true
}

func teamObjectSQLWords(statement string) []string {
	return strings.FieldsFunc(statement, func(char rune) bool {
		return !((char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_')
	})
}

func reservedTeamObjectExtensionTable(table string) bool {
	lower := strings.ToLower(table)
	return strings.HasPrefix(lower, "team_") || strings.HasPrefix(lower, "schema_") ||
		strings.HasPrefix(lower, "sqlite_")
}

func teamObjectCommitError(_ error) error {
	return ErrTeamObjectCommitFailed
}
