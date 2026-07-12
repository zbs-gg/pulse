package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

var (
	ErrOwnerApprovalInvalid         = errors.New("owner_approval_invalid")
	ErrOwnerApprovalRequired        = errors.New("owner_approval_required")
	ErrOwnerApprovalExpired         = errors.New("owner_approval_expired")
	ErrOwnerApprovalReplay          = errors.New("owner_approval_replayed")
	ErrOwnerApprovalBindingMismatch = errors.New("owner_approval_binding_mismatch")
	ErrOwnerStepUpStale             = errors.New("owner_step_up_stale")
	ErrHumanOwnerRequired           = errors.New("active_human_owner_required")
	ErrTeamRemoteInactive           = errors.New("team_remote_inactive")
	ErrTeamAlreadyActivated         = errors.New("team_remote_already_activated")
)

const (
	OwnerActionTeamBootstrap          = "team.bootstrap"
	OwnerActionMembershipCreate       = "membership.create"
	OwnerActionMembershipRevoke       = "membership.revoke"
	OwnerActionAgentBindingCreate     = "agent_binding.create"
	OwnerActionAgentBindingRevoke     = "agent_binding.revoke"
	OwnerActionServicePrincipalCreate = "service_principal.create"
	OwnerActionServicePrincipalRevoke = "service_principal.revoke"
	OwnerActionProjectCreate          = "project.create"
	OwnerActionProjectGrantCreate     = "project_grant.create"
	OwnerActionProjectGrantRevoke     = "project_grant.revoke"
	OwnerActionSharedDelete           = "team.object.delete.shared"
	OwnerActionTeamAuditInspect       = "team.audit.inspect"
	OwnerActionDeletionStatus         = "team.deletion.status"
	OwnerActionSyntheticActivate      = "team.activation.synthetic"

	TeamActivationInactive = "inactive"
	TeamActivationActive   = "active"
	TeamContentSynthetic   = "synthetic"

	ownerApprovalMaxStepUpAge = 5 * time.Minute
	ownerApprovalMaxTTL       = 5 * time.Minute
	ownerAssertionMaxTTL      = 30 * time.Second
)

var ownerApprovalActions = map[string]struct{}{
	OwnerActionTeamBootstrap: {}, OwnerActionMembershipCreate: {},
	OwnerActionMembershipRevoke: {}, OwnerActionAgentBindingCreate: {},
	OwnerActionAgentBindingRevoke: {}, OwnerActionServicePrincipalCreate: {},
	OwnerActionServicePrincipalRevoke: {}, OwnerActionProjectCreate: {},
	OwnerActionProjectGrantCreate: {}, OwnerActionProjectGrantRevoke: {},
	OwnerActionSharedDelete: {}, OwnerActionTeamAuditInspect: {},
	OwnerActionDeletionStatus: {}, OwnerActionSyntheticActivate: {},
}

// OwnerStepUpIdentity is returned only after the deployment-pinned issuer,
// human subject, and admin client triple matches. External identity values are
// not included in this result and never need to enter an admin request body.
type OwnerStepUpIdentity struct {
	StoreID          string
	TeamID           string
	OwnerPrincipalID string
	ClientKey        string
	Bootstrap        bool
}

type TeamBootstrapIntent struct {
	StoreID           string
	TeamID            string
	OwnerPrincipalID  string
	OwnerMembershipID string
}

type ApprovedBootstrapTeamRequest struct {
	TeamName      string
	PresentedRoot teamauth.BootstrapRoot
	Intent        TeamBootstrapIntent
	ApprovalNonce string
	RequestID     string
	ClientKey     string
}

type OwnerApprovalIssueRequest struct {
	OwnerPrincipalID   string
	StoreID            string
	TeamID             string
	ClientKey          string
	Action             string
	TargetKind         string
	TargetID           string
	TargetDigest       string
	StepUpAt           time.Time
	ExpiresAt          time.Time
	AssertionKID       string
	AssertionJTI       string
	AssertionExpiresAt time.Time
	Writer             TeamWriterLeaseIdentity
}

type OwnerApprovalChallenge struct {
	Nonce     string
	ExpiresAt time.Time
}

type OwnerApprovalConsumeRequest struct {
	// OwnerPrincipalID is optional compatibility metadata. Authority is always
	// derived from the nonce row; a non-empty mismatch fails closed.
	OwnerPrincipalID string
	StoreID          string
	TeamID           string
	Action           string
	TargetKind       string
	TargetID         string
	TargetDigest     string
	Nonce            string
	RequestID        string
	ClientKey        string
	Writer           TeamWriterLeaseIdentity
}

type OwnerApprovalConsumption struct {
	OwnerPrincipalID string
	AuditEventID     string
	ConsumedAt       time.Time
}

type TeamActivationState struct {
	StoreID         string
	TeamID          string
	ActivationState string
	ContentBoundary string
	PublicEnabled   bool
	GateDigest      string
	ActivatedBy     string
	AuditEventID    string
	ActivatedAt     *time.Time
}

type ActivateSyntheticTeamRequest struct {
	ApprovalNonce string
	GateDigest    string
	RequestID     string
	ClientKey     string
	Writer        TeamWriterLeaseIdentity
}

type TeamActivationReadiness struct {
	Store      TeamStoreInfo
	Activation TeamActivationState
}

type ownerApprovalRow struct {
	NonceHash        string
	StoreID          string
	TeamID           string
	OwnerPrincipalID string
	ClientKey        string
	WriterID         string
	WriterTokenHash  string
	Action           string
	TargetKind       string
	TargetID         string
	TargetDigest     string
	StepUpAt         time.Time
	IssuedAt         time.Time
	ExpiresAt        time.Time
	ConsumedAt       *time.Time
	ConsumeAuditID   string
}

// ResolveOwnerStepUpIdentity pins the complete configured browser-admin
// identity, including the admin client. Browser assertion signature, replay,
// method/path/body binding, and auth_time verification remain server duties.
func (s *Store) ResolveOwnerStepUpIdentity(ctx context.Context, presented teamauth.BootstrapRoot) (OwnerStepUpIdentity, error) {
	if s.expectedBootstrapRoot == nil || !s.expectedBootstrapRoot.Matches(presented) {
		return OwnerStepUpIdentity{}, ErrHumanOwnerRequired
	}
	clientKey := teamauth.OAuthClientKey(presented.Issuer, presented.AdminClientID)
	info, err := readTeamStoreInfo(ctx, s.db)
	if errors.Is(err, ErrTeamStoreUninitialized) {
		var candidate int
		if scanErr := s.db.QueryRowContext(ctx, `
			SELECT count(*) FROM team_bootstrap_candidates WHERE singleton = 1`).Scan(&candidate); scanErr != nil {
			return OwnerStepUpIdentity{}, scanErr
		}
		if candidate != 1 {
			return OwnerStepUpIdentity{}, ErrTeamBootstrapCandidateRequired
		}
		return OwnerStepUpIdentity{ClientKey: clientKey, Bootstrap: true}, nil
	}
	if err != nil {
		return OwnerStepUpIdentity{}, err
	}
	principal, err := s.ResolveHumanIdentity(ctx, presented.Issuer, presented.Subject)
	if err != nil || principal.Kind != string(teamauth.PrincipalHuman) || principal.MembershipRole != "owner" {
		return OwnerStepUpIdentity{}, ErrHumanOwnerRequired
	}
	return OwnerStepUpIdentity{
		StoreID: info.StoreID, TeamID: info.TeamID,
		OwnerPrincipalID: principal.PrincipalID, ClientKey: clientKey,
	}, nil
}

// PrepareTeamBootstrap preassigns every identity to which the browser-approved
// bootstrap nonce will be bound. It does not mark or activate the store.
func (s *Store) PrepareTeamBootstrap(ctx context.Context) (TeamBootstrapIntent, error) {
	var markers, candidates int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM team_stores`).Scan(&markers); err != nil {
		return TeamBootstrapIntent{}, err
	}
	if markers != 0 {
		return TeamBootstrapIntent{}, ErrBootstrapConsumed
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM team_bootstrap_candidates WHERE singleton = 1`).Scan(&candidates); err != nil {
		return TeamBootstrapIntent{}, err
	}
	if candidates != 1 {
		return TeamBootstrapIntent{}, ErrTeamBootstrapCandidateRequired
	}
	values := make([]string, 4)
	for index, prefix := range []string{"store", "team", "principal", "membership"} {
		value, err := newOpaqueID(prefix)
		if err != nil {
			return TeamBootstrapIntent{}, err
		}
		values[index] = value
	}
	return TeamBootstrapIntent{
		StoreID: values[0], TeamID: values[1], OwnerPrincipalID: values[2], OwnerMembershipID: values[3],
	}, nil
}

func TeamBootstrapApprovalTargetDigest(intent TeamBootstrapIntent, teamName string) string {
	return ownerApprovalDigest(OwnerActionTeamBootstrap, intent.StoreID, intent.TeamID,
		intent.OwnerPrincipalID, intent.OwnerMembershipID, strings.TrimSpace(teamName))
}

// BootstrapTeamWithApproval is the only bootstrap entrypoint for the Owner
// administration server. Store marker, initial Owner identity, approval
// consumption, privileged audit, and bootstrap-candidate removal commit in one
// transaction, so a crash cannot burn the nonce without the team identity.
func (s *Store) BootstrapTeamWithApproval(ctx context.Context, request ApprovedBootstrapTeamRequest) (BootstrapResult, error) {
	request.TeamName = strings.TrimSpace(request.TeamName)
	if request.TeamName == "" || !validBootstrapIntent(request.Intent) ||
		!validOwnerNonce(request.ApprovalNonce) || !validOwnerOpaque(request.RequestID, 255) ||
		!validOwnerClientKey(request.ClientKey) {
		return BootstrapResult{}, ErrOwnerApprovalInvalid
	}
	if s.expectedBootstrapRoot == nil || !s.expectedBootstrapRoot.Matches(request.PresentedRoot) {
		return BootstrapResult{}, ErrBootstrapRootMismatch
	}
	expectedClientKey := teamauth.OAuthClientKey(
		s.expectedBootstrapRoot.Issuer, s.expectedBootstrapRoot.AdminClientID,
	)
	if request.ClientKey != "" && !hmac.Equal([]byte(request.ClientKey), []byte(expectedClientKey)) {
		return BootstrapResult{}, ErrOwnerApprovalBindingMismatch
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer tx.Rollback()
	var markers, candidates int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM team_stores`).Scan(&markers); err != nil {
		return BootstrapResult{}, err
	}
	if markers != 0 {
		return BootstrapResult{}, ErrBootstrapConsumed
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM team_bootstrap_candidates WHERE singleton = 1`).Scan(&candidates); err != nil {
		return BootstrapResult{}, err
	}
	if candidates != 1 {
		return BootstrapResult{}, ErrTeamBootstrapCandidateRequired
	}
	legacy, err := countLegacyRows(ctx, tx)
	if err != nil {
		return BootstrapResult{}, err
	}
	if legacy != 0 {
		return BootstrapResult{}, ErrLegacyLocalData
	}

	now := s.clock().UTC().Format(time.RFC3339Nano)
	rootFingerprint, _ := s.expectedBootstrapRoot.Fingerprint()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_stores(
			singleton, store_id, team_id, team_name, min_reader_version,
			min_writer_version, durability_profile, auth_epoch,
			bootstrap_root_fingerprint, bootstrap_consumed_at, created_at)
		VALUES (1, ?, ?, ?, ?, ?, 'wal-full-fk', 1, ?, ?, ?)`,
		request.Intent.StoreID, request.Intent.TeamID, request.TeamName,
		teamauth.SchemaVersion, teamauth.SchemaVersion, rootFingerprint, now, now); err != nil {
		if isConstraintError(err) {
			return BootstrapResult{}, ErrBootstrapConsumed
		}
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_principals(principal_id, store_id, kind, status, auth_epoch, created_at)
		VALUES (?, ?, 'human', 'active', 1, ?)`,
		request.Intent.OwnerPrincipalID, request.Intent.StoreID, now); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_human_identities(identity_key, human_principal_id, created_at)
		VALUES (?, ?, ?)`, teamauth.HumanIdentityKey(
		s.expectedBootstrapRoot.Issuer, s.expectedBootstrapRoot.Subject,
	), request.Intent.OwnerPrincipalID, now); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_memberships(
			membership_id, team_id, principal_id, role, status, auth_epoch, created_at)
		VALUES (?, ?, ?, 'owner', 'active', 1, ?)`,
		request.Intent.OwnerMembershipID, request.Intent.TeamID,
		request.Intent.OwnerPrincipalID, now); err != nil {
		return BootstrapResult{}, err
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return BootstrapResult{}, err
	}
	_, err = s.consumeOwnerApprovalTx(ctx, tx, info, OwnerApprovalConsumeRequest{
		StoreID: request.Intent.StoreID, TeamID: request.Intent.TeamID,
		Action: OwnerActionTeamBootstrap, TargetKind: "team", TargetID: request.Intent.TeamID,
		TargetDigest: TeamBootstrapApprovalTargetDigest(request.Intent, request.TeamName),
		Nonce:        request.ApprovalNonce, RequestID: request.RequestID, ClientKey: expectedClientKey,
	})
	if err != nil {
		return BootstrapResult{}, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM team_bootstrap_candidates WHERE singleton = 1`)
	if err != nil {
		return BootstrapResult{}, err
	}
	if consumed, rowsErr := result.RowsAffected(); rowsErr != nil || consumed != 1 {
		return BootstrapResult{}, ErrTeamBootstrapCandidateRequired
	}
	if err := tx.Commit(); err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{
		TeamStoreInfo: TeamStoreInfo{
			StoreID: request.Intent.StoreID, TeamID: request.Intent.TeamID,
			TeamName: request.TeamName, MinReaderVersion: teamauth.SchemaVersion,
			MinWriterVersion: teamauth.SchemaVersion, AuthEpoch: 1,
		},
		OwnerPrincipalID:  request.Intent.OwnerPrincipalID,
		OwnerMembershipID: request.Intent.OwnerMembershipID,
	}, nil
}

func SyntheticActivationTargetDigest(storeID, teamID, gateDigest string) string {
	return ownerApprovalDigest(OwnerActionSyntheticActivate, storeID, teamID, gateDigest)
}

func SharedDeletionApprovalTargetDigest(objectID string) string {
	return ownerApprovalDigest(OwnerActionSharedDelete, objectID)
}

func sharedDeletionOwnerApprovalRequest(info TeamStoreInfo, request TeamSharedDeletionStartRequest) OwnerApprovalConsumeRequest {
	return OwnerApprovalConsumeRequest{
		StoreID: info.StoreID, TeamID: info.TeamID,
		Action: OwnerActionSharedDelete, TargetKind: "team_object", TargetID: request.ObjectID,
		TargetDigest: SharedDeletionApprovalTargetDigest(request.ObjectID),
		Nonce:        request.ApprovalNonce, RequestID: request.RequestID, ClientKey: request.ClientKey,
		Writer: request.Writer,
	}
}

func (s *Store) readBoundSharedDeletionApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	info TeamStoreInfo,
	request OwnerApprovalConsumeRequest,
) (ownerApprovalRow, error) {
	if !validOwnerApprovalConsume(request) {
		return ownerApprovalRow{}, ErrOwnerApprovalInvalid
	}
	row, err := readOwnerApproval(ctx, tx, request.Nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return ownerApprovalRow{}, ErrOwnerApprovalRequired
	}
	if err != nil {
		return ownerApprovalRow{}, err
	}
	if row.StoreID != info.StoreID || row.TeamID != info.TeamID ||
		row.Action != request.Action || row.TargetKind != request.TargetKind ||
		row.TargetID != request.TargetID ||
		!hmac.Equal([]byte(row.ClientKey), []byte(request.ClientKey)) ||
		!ownerApprovalWriterMatches(row, request.Writer) ||
		!hmac.Equal([]byte(row.TargetDigest), []byte(request.TargetDigest)) {
		return ownerApprovalRow{}, ErrOwnerApprovalBindingMismatch
	}
	if row.ConsumedAt == nil && !row.ExpiresAt.After(s.clock().UTC()) {
		return ownerApprovalRow{}, ErrOwnerApprovalExpired
	}
	if err := requireHumanOwnerTx(ctx, tx, info.TeamID, row.OwnerPrincipalID); err != nil {
		return ownerApprovalRow{}, err
	}
	return row, nil
}

func (s *Store) IssueOwnerApproval(ctx context.Context, request OwnerApprovalIssueRequest) (OwnerApprovalChallenge, error) {
	now := s.clock().UTC()
	if err := validateOwnerApprovalIssue(request, now); err != nil {
		return OwnerApprovalChallenge{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OwnerApprovalChallenge{}, err
	}
	defer tx.Rollback()
	if s.expectedBootstrapRoot == nil {
		return OwnerApprovalChallenge{}, ErrHumanOwnerRequired
	}
	expectedClientKey := teamauth.OAuthClientKey(
		s.expectedBootstrapRoot.Issuer, s.expectedBootstrapRoot.AdminClientID,
	)
	if !hmac.Equal([]byte(request.ClientKey), []byte(expectedClientKey)) {
		return OwnerApprovalChallenge{}, ErrOwnerApprovalBindingMismatch
	}
	if request.Action == OwnerActionTeamBootstrap {
		var markers, candidates int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM team_stores`).Scan(&markers); err != nil {
			return OwnerApprovalChallenge{}, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM team_bootstrap_candidates WHERE singleton = 1`).Scan(&candidates); err != nil {
			return OwnerApprovalChallenge{}, err
		}
		if markers != 0 || candidates != 1 || s.expectedBootstrapRoot == nil {
			return OwnerApprovalChallenge{}, ErrTeamBootstrapCandidateRequired
		}
	} else {
		info, err := readTeamStoreInfo(ctx, tx)
		if err != nil {
			return OwnerApprovalChallenge{}, err
		}
		if request.StoreID != info.StoreID || request.TeamID != info.TeamID {
			return OwnerApprovalChallenge{}, ErrOwnerApprovalBindingMismatch
		}
		if err := requireHumanOwnerTx(ctx, tx, info.TeamID, request.OwnerPrincipalID); err != nil {
			return OwnerApprovalChallenge{}, err
		}
		if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
			return OwnerApprovalChallenge{}, err
		}
	}

	for attempts := 0; attempts < 3; attempts++ {
		nonce, err := newOwnerApprovalNonce()
		if err != nil {
			return OwnerApprovalChallenge{}, err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO team_owner_approvals(
				nonce_hash, store_id, team_id, owner_principal_id, client_key,
				writer_id, writer_lease_token_hash, action,
				target_kind, target_id, target_digest,
				assertion_kid_hash, assertion_jti_hash, assertion_expires_at,
				step_up_at, issued_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ownerApprovalNonceHash(nonce), request.StoreID, request.TeamID,
			request.OwnerPrincipalID, request.ClientKey, request.Writer.WriterID,
			ownerApprovalWriterTokenHash(request), request.Action, request.TargetKind, request.TargetID,
			request.TargetDigest, assertionIdentifier("owner-kid", request.AssertionKID),
			assertionIdentifier("owner-jti", request.AssertionJTI),
			request.AssertionExpiresAt.UTC().Format(time.RFC3339Nano),
			request.StepUpAt.UTC().Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano), request.ExpiresAt.UTC().Format(time.RFC3339Nano))
		if err == nil {
			if request.Action != OwnerActionTeamBootstrap {
				if leaseErr := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); leaseErr != nil {
					return OwnerApprovalChallenge{}, leaseErr
				}
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return OwnerApprovalChallenge{}, commitErr
			}
			return OwnerApprovalChallenge{Nonce: nonce, ExpiresAt: request.ExpiresAt.UTC()}, nil
		}
		if !isConstraintError(err) {
			return OwnerApprovalChallenge{}, err
		}
		var replay int
		if replayErr := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM team_owner_approvals
				 WHERE assertion_kid_hash = ? AND assertion_jti_hash = ?
			)`, assertionIdentifier("owner-kid", request.AssertionKID),
			assertionIdentifier("owner-jti", request.AssertionJTI)).Scan(&replay); replayErr != nil {
			return OwnerApprovalChallenge{}, replayErr
		}
		if replay == 1 {
			return OwnerApprovalChallenge{}, ErrOwnerApprovalReplay
		}
	}
	return OwnerApprovalChallenge{}, ErrOwnerApprovalInvalid
}

// consumeOwnerApproval is intentionally package-private. Burning an approval
// in one transaction and performing a privileged mutation in another is not a
// safe public contract; exported entrypoints below consume inside their domain
// transaction. This helper exists for direct invariant tests only.
func (s *Store) consumeOwnerApproval(ctx context.Context, request OwnerApprovalConsumeRequest) (OwnerApprovalConsumption, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OwnerApprovalConsumption{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return OwnerApprovalConsumption{}, err
	}
	if !validTeamOpaque(request.Writer.WriterID, 1, 255) ||
		!validTeamOpaque(request.Writer.Token, 1, 255) {
		return OwnerApprovalConsumption{}, ErrOwnerApprovalInvalid
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return OwnerApprovalConsumption{}, err
	}
	consumed, err := s.consumeOwnerApprovalTx(ctx, tx, info, request)
	if err != nil {
		return OwnerApprovalConsumption{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return OwnerApprovalConsumption{}, err
	}
	if err := tx.Commit(); err != nil {
		if isConstraintError(err) {
			return OwnerApprovalConsumption{}, ErrOwnerApprovalReplay
		}
		return OwnerApprovalConsumption{}, err
	}
	return consumed, nil
}

func (s *Store) consumeOwnerApprovalTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, request OwnerApprovalConsumeRequest) (OwnerApprovalConsumption, error) {
	return s.consumeOwnerApprovalTxWithAuditTarget(ctx, tx, info, request, request.TargetKind, request.TargetID)
}

func (s *Store) consumeOwnerApprovalTxWithAuditTarget(
	ctx context.Context,
	tx *sql.Tx,
	info TeamStoreInfo,
	request OwnerApprovalConsumeRequest,
	auditTargetKind string,
	auditTargetID string,
) (OwnerApprovalConsumption, error) {
	if !validOwnerApprovalConsume(request) {
		return OwnerApprovalConsumption{}, ErrOwnerApprovalInvalid
	}
	row, err := readOwnerApproval(ctx, tx, request.Nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerApprovalConsumption{}, ErrOwnerApprovalRequired
	}
	if err != nil {
		return OwnerApprovalConsumption{}, err
	}
	if row.ConsumedAt != nil {
		return OwnerApprovalConsumption{}, ErrOwnerApprovalReplay
	}
	now := s.clock().UTC()
	if !row.ExpiresAt.After(now) {
		return OwnerApprovalConsumption{}, ErrOwnerApprovalExpired
	}
	if row.StoreID != info.StoreID || row.TeamID != info.TeamID ||
		request.StoreID != "" && request.StoreID != row.StoreID ||
		request.TeamID != "" && request.TeamID != row.TeamID ||
		request.OwnerPrincipalID != "" && request.OwnerPrincipalID != row.OwnerPrincipalID ||
		!hmac.Equal([]byte(request.ClientKey), []byte(row.ClientKey)) ||
		!ownerApprovalWriterMatches(row, request.Writer) ||
		row.Action != request.Action || row.TargetKind != request.TargetKind || row.TargetID != request.TargetID ||
		!hmac.Equal([]byte(row.TargetDigest), []byte(request.TargetDigest)) {
		return OwnerApprovalConsumption{}, ErrOwnerApprovalBindingMismatch
	}
	if err := requireHumanOwnerTx(ctx, tx, info.TeamID, row.OwnerPrincipalID); err != nil {
		return OwnerApprovalConsumption{}, err
	}
	if !validOwnerOpaque(auditTargetKind, 64) || !validOwnerOpaque(auditTargetID, 255) {
		return OwnerApprovalConsumption{}, ErrOwnerApprovalInvalid
	}
	auditID, err := appendOwnerApprovalAudit(ctx, tx, info, row, request, auditTargetKind, auditTargetID, now)
	if err != nil {
		return OwnerApprovalConsumption{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE team_owner_approvals
		   SET consumed_at = ?, consume_audit_event_id = ?
		 WHERE nonce_hash = ? AND consumed_at IS NULL AND expires_at > ?`,
		now.Format(time.RFC3339Nano), auditID, row.NonceHash,
		now.Format(time.RFC3339Nano))
	if err != nil {
		if isConstraintError(err) {
			return OwnerApprovalConsumption{}, ErrOwnerApprovalReplay
		}
		return OwnerApprovalConsumption{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return OwnerApprovalConsumption{}, err
	}
	if affected != 1 {
		return OwnerApprovalConsumption{}, ErrOwnerApprovalReplay
	}
	return OwnerApprovalConsumption{
		OwnerPrincipalID: row.OwnerPrincipalID, AuditEventID: auditID, ConsumedAt: now,
	}, nil
}

func (s *Store) ActivateSyntheticTeamRemote(ctx context.Context, request ActivateSyntheticTeamRequest) (TeamActivationState, error) {
	if !validHexDigest(request.GateDigest) || !validOwnerOpaque(request.RequestID, 255) ||
		!validOwnerClientKey(request.ClientKey) ||
		!validTeamOpaque(request.Writer.WriterID, 1, 255) ||
		!validTeamOpaque(request.Writer.Token, 1, 255) {
		return TeamActivationState{}, ErrOwnerApprovalInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamActivationState{}, err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return TeamActivationState{}, err
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return TeamActivationState{}, err
	}
	consume := OwnerApprovalConsumeRequest{
		StoreID: info.StoreID, TeamID: info.TeamID,
		Action: OwnerActionSyntheticActivate, TargetKind: "team_activation", TargetID: info.TeamID,
		TargetDigest: SyntheticActivationTargetDigest(info.StoreID, info.TeamID, request.GateDigest),
		Nonce:        request.ApprovalNonce, RequestID: request.RequestID, ClientKey: request.ClientKey,
		Writer: request.Writer,
	}
	consumed, err := s.consumeOwnerApprovalTx(ctx, tx, info, consume)
	if err != nil {
		return TeamActivationState{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE team_remote_activation
		   SET activation_state = 'active', public_enabled = 1,
		       synthetic_gate_digest = ?, activated_by_principal_id = ?,
		       activation_audit_event_id = ?, activated_at = ?
		 WHERE singleton = 1 AND store_id = ? AND team_id = ?
		   AND activation_state = 'inactive' AND public_enabled = 0`,
		request.GateDigest, consumed.OwnerPrincipalID, consumed.AuditEventID,
		consumed.ConsumedAt.Format(time.RFC3339Nano), info.StoreID, info.TeamID)
	if err != nil {
		return TeamActivationState{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return TeamActivationState{}, err
	}
	if changed != 1 {
		return TeamActivationState{}, ErrTeamAlreadyActivated
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_policy_metadata
		   SET real_content_state = 'synthetic', updated_at = ?
		 WHERE store_id = ? AND team_id = ? AND real_content_state = 'blocked'`,
		consumed.ConsumedAt.Format(time.RFC3339Nano), info.StoreID, info.TeamID); err != nil {
		return TeamActivationState{}, err
	}
	state, err := readTeamActivationState(ctx, tx)
	if err != nil {
		return TeamActivationState{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return TeamActivationState{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamActivationState{}, err
	}
	return state, nil
}

func (s *Store) ReadTeamActivationState(ctx context.Context) (TeamActivationState, error) {
	return readTeamActivationState(ctx, s.db)
}

func (s *Store) CheckSyntheticTeamReadiness(ctx context.Context, options TeamReadinessOptions) (TeamActivationReadiness, error) {
	info, err := s.CheckTeamReadiness(ctx, options)
	if err != nil {
		return TeamActivationReadiness{}, err
	}
	activation, err := s.ReadTeamActivationState(ctx)
	if err != nil {
		return TeamActivationReadiness{}, err
	}
	var realContentState string
	if err := s.db.QueryRowContext(ctx, `
		SELECT real_content_state FROM team_policy_metadata
		 WHERE store_id = ? AND team_id = ?`, info.StoreID, info.TeamID).Scan(&realContentState); err != nil {
		return TeamActivationReadiness{}, err
	}
	if activation.StoreID != info.StoreID || activation.TeamID != info.TeamID ||
		activation.ActivationState != TeamActivationActive || !activation.PublicEnabled ||
		activation.ContentBoundary != TeamContentSynthetic || !validHexDigest(activation.GateDigest) ||
		realContentState != TeamContentSynthetic {
		return TeamActivationReadiness{}, ErrTeamRemoteInactive
	}
	return TeamActivationReadiness{Store: info, Activation: activation}, nil
}

func validateOwnerApprovalIssue(request OwnerApprovalIssueRequest, now time.Time) error {
	if !validOwnerOpaque(request.OwnerPrincipalID, 255) || !validOwnerOpaque(request.StoreID, 255) ||
		!validOwnerOpaque(request.TeamID, 255) || !validOwnerAction(request.Action) ||
		!validOwnerOpaque(request.TargetKind, 64) || !validOwnerOpaque(request.TargetID, 255) ||
		!validHexDigest(request.TargetDigest) || !validOwnerClientKey(request.ClientKey) || request.ClientKey == "" ||
		request.StepUpAt.IsZero() || request.ExpiresAt.IsZero() ||
		strings.TrimSpace(request.AssertionKID) == "" || strings.TrimSpace(request.AssertionJTI) == "" ||
		request.AssertionExpiresAt.IsZero() {
		return ErrOwnerApprovalInvalid
	}
	stepUp := request.StepUpAt.UTC()
	expires := request.ExpiresAt.UTC()
	if stepUp.After(now.Add(30*time.Second)) || now.Sub(stepUp) > ownerApprovalMaxStepUpAge {
		return ErrOwnerStepUpStale
	}
	if !expires.After(now) || expires.Sub(now) > ownerApprovalMaxTTL || expires.Sub(stepUp) > ownerApprovalMaxStepUpAge {
		return ErrOwnerApprovalExpired
	}
	assertionExpires := request.AssertionExpiresAt.UTC()
	if !assertionExpires.After(now) || assertionExpires.Sub(now) > ownerAssertionMaxTTL {
		return ErrOwnerApprovalExpired
	}
	if request.Action == OwnerActionTeamBootstrap {
		if request.Writer.WriterID != "" || request.Writer.Token != "" {
			return ErrOwnerApprovalInvalid
		}
	} else if !validTeamOpaque(request.Writer.WriterID, 1, 255) ||
		!validTeamOpaque(request.Writer.Token, 1, 255) {
		return ErrOwnerApprovalInvalid
	}
	return nil
}

func validOwnerApprovalConsume(request OwnerApprovalConsumeRequest) bool {
	base := validOwnerAction(request.Action) && validOwnerOpaque(request.TargetKind, 64) &&
		validOwnerOpaque(request.TargetID, 255) && validHexDigest(request.TargetDigest) &&
		validOwnerNonce(request.Nonce) && validOwnerOpaque(request.RequestID, 255) &&
		validOwnerClientKey(request.ClientKey) && request.ClientKey != "" &&
		(request.StoreID == "" || validOwnerOpaque(request.StoreID, 255)) &&
		(request.TeamID == "" || validOwnerOpaque(request.TeamID, 255)) &&
		(request.OwnerPrincipalID == "" || validOwnerOpaque(request.OwnerPrincipalID, 255))
	if !base {
		return false
	}
	if request.Action == OwnerActionTeamBootstrap {
		return request.Writer.WriterID == "" && request.Writer.Token == ""
	}
	return validTeamOpaque(request.Writer.WriterID, 1, 255) &&
		validTeamOpaque(request.Writer.Token, 1, 255)
}

func validBootstrapIntent(intent TeamBootstrapIntent) bool {
	return validOwnerOpaque(intent.StoreID, 255) && strings.HasPrefix(intent.StoreID, "store_") &&
		validOwnerOpaque(intent.TeamID, 255) && strings.HasPrefix(intent.TeamID, "team_") &&
		validOwnerOpaque(intent.OwnerPrincipalID, 255) && strings.HasPrefix(intent.OwnerPrincipalID, "principal_") &&
		validOwnerOpaque(intent.OwnerMembershipID, 255) && strings.HasPrefix(intent.OwnerMembershipID, "membership_")
}

func validOwnerAction(action string) bool {
	_, ok := ownerApprovalActions[action]
	return ok
}

func validOwnerOpaque(value string, max int) bool {
	if len(value) == 0 || len(value) > max || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func validHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validOwnerNonce(value string) bool { return validHexDigest(value) }

func validOwnerClientKey(value string) bool { return value == "" || validHexDigest(value) }

func ownerApprovalWriterTokenHash(request OwnerApprovalIssueRequest) string {
	if request.Action == OwnerActionTeamBootstrap {
		return ""
	}
	return writerLeaseTokenHash(request.Writer.Token)
}

func ownerApprovalWriterMatches(row ownerApprovalRow, writer TeamWriterLeaseIdentity) bool {
	if row.Action == OwnerActionTeamBootstrap {
		return row.WriterID == "" && row.WriterTokenHash == "" &&
			writer.WriterID == "" && writer.Token == ""
	}
	return row.WriterID == writer.WriterID && writerLeaseTokenMatches(row.WriterTokenHash, writer.Token)
}

func requireHumanOwnerTx(ctx context.Context, tx *sql.Tx, teamID, principalID string) error {
	return requireHumanOwnerQuery(ctx, tx, teamID, principalID)
}

func requireHumanOwnerQuery(ctx context.Context, q queryer, teamID, principalID string) error {
	var count int
	if err := q.QueryRowContext(ctx, `
		SELECT count(*)
		  FROM team_principals principal
		  JOIN team_memberships membership
		    ON membership.principal_id = principal.principal_id
		   AND membership.team_id = ?
		 WHERE principal.principal_id = ? AND principal.kind = 'human'
		   AND principal.status = 'active' AND membership.status = 'active'
		   AND membership.role = 'owner'`, teamID, principalID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrHumanOwnerRequired
	}
	return nil
}

func readOwnerApproval(ctx context.Context, q queryer, nonce string) (ownerApprovalRow, error) {
	var row ownerApprovalRow
	var stepUpText, issuedText, expiresText string
	var consumedText sql.NullString
	if err := q.QueryRowContext(ctx, `
		SELECT nonce_hash, store_id, team_id, owner_principal_id, client_key,
		       writer_id, writer_lease_token_hash, action,
		       target_kind, target_id, target_digest, step_up_at, issued_at,
		       expires_at, consumed_at, COALESCE(consume_audit_event_id, '')
		  FROM team_owner_approvals WHERE nonce_hash = ?`, ownerApprovalNonceHash(nonce)).Scan(
		&row.NonceHash, &row.StoreID, &row.TeamID, &row.OwnerPrincipalID, &row.ClientKey,
		&row.WriterID, &row.WriterTokenHash, &row.Action,
		&row.TargetKind, &row.TargetID, &row.TargetDigest, &stepUpText, &issuedText,
		&expiresText, &consumedText, &row.ConsumeAuditID,
	); err != nil {
		return ownerApprovalRow{}, err
	}
	var err error
	if row.StepUpAt, err = time.Parse(time.RFC3339Nano, stepUpText); err != nil {
		return ownerApprovalRow{}, ErrOwnerApprovalInvalid
	}
	if row.IssuedAt, err = time.Parse(time.RFC3339Nano, issuedText); err != nil {
		return ownerApprovalRow{}, ErrOwnerApprovalInvalid
	}
	if row.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresText); err != nil {
		return ownerApprovalRow{}, ErrOwnerApprovalInvalid
	}
	if consumedText.Valid {
		consumed, parseErr := time.Parse(time.RFC3339Nano, consumedText.String)
		if parseErr != nil {
			return ownerApprovalRow{}, ErrOwnerApprovalInvalid
		}
		row.ConsumedAt = &consumed
	}
	return row, nil
}

func appendOwnerApprovalAudit(
	ctx context.Context,
	tx *sql.Tx,
	info TeamStoreInfo,
	approval ownerApprovalRow,
	request OwnerApprovalConsumeRequest,
	auditTargetKind string,
	auditTargetID string,
	now time.Time,
) (string, error) {
	var policyVersion int
	var globalEpoch int64
	if err := tx.QueryRowContext(ctx, `
		SELECT policy_version, global_epoch FROM team_policy_metadata
		 WHERE store_id = ? AND team_id = ?`, info.StoreID, info.TeamID).Scan(&policyVersion, &globalEpoch); err != nil {
		return "", err
	}
	eventID, err := newOpaqueID("audit")
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_audit_events(
			event_id, store_id, occurred_at, action, outcome, actor_principal_id,
			client_key, team_id, project_id, target_kind, target_id, request_id,
			policy_version, mode, auth_epoch, reason_code, metadata_json)
		VALUES (?, ?, ?, ?, 'allowed', ?, NULLIF(?, ''), ?, NULL, ?, ?, ?,
		        ?, 'team-remote', ?, 'owner_approval_consumed', '{}')`,
		eventID, info.StoreID, now.Format(time.RFC3339Nano), approval.Action,
		approval.OwnerPrincipalID, approval.ClientKey, info.TeamID, auditTargetKind,
		auditTargetID, request.RequestID, policyVersion, globalEpoch)
	if err != nil {
		return "", err
	}
	return eventID, nil
}

func readTeamActivationState(ctx context.Context, q queryer) (TeamActivationState, error) {
	var state TeamActivationState
	var public int
	var activatedAt sql.NullString
	if err := q.QueryRowContext(ctx, `
		SELECT store_id, team_id, activation_state, content_boundary, public_enabled,
		       COALESCE(synthetic_gate_digest, ''),
		       COALESCE(activated_by_principal_id, ''),
		       COALESCE(activation_audit_event_id, ''), activated_at
		  FROM team_remote_activation WHERE singleton = 1`).Scan(
		&state.StoreID, &state.TeamID, &state.ActivationState, &state.ContentBoundary,
		&public, &state.GateDigest, &state.ActivatedBy, &state.AuditEventID, &activatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TeamActivationState{}, ErrTeamRemoteInactive
		}
		return TeamActivationState{}, err
	}
	state.PublicEnabled = public == 1
	if activatedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, activatedAt.String)
		if err != nil {
			return TeamActivationState{}, ErrTeamRemoteInactive
		}
		state.ActivatedAt = &parsed
	}
	return state, nil
}

func newOwnerApprovalNonce() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func ownerApprovalNonceHash(nonce string) string {
	return ownerApprovalDigest("pulse-owner-approval-nonce-v1", nonce)
}

func ownerApprovalDigest(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
