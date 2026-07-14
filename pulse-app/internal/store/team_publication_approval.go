package store

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

var (
	ErrTeamPublicationApprovalInvalid         = errors.New("team_publication_approval_invalid")
	ErrTeamPublicationApprovalRequired        = errors.New("team_publication_approval_required")
	ErrTeamPublicationApprovalExpired         = errors.New("team_publication_approval_expired")
	ErrTeamPublicationApprovalReplay          = errors.New("team_publication_approval_replayed")
	ErrTeamPublicationApprovalBindingMismatch = errors.New("team_publication_approval_binding_mismatch")
)

const (
	teamPublicationApprovalAction = "team.commons.publish.approval"
	teamPublicationApprovalReason = "publication_approval_consumed"
)

// TeamPublicationApprovalIssueRequest is the complete authority envelope for
// one exact Desk-to-Commons publication. The owner UI constructs this value
// only after verifying the canonical outbound bytes. It deliberately does not
// contain a Desk source ID, path, transcript, or any other private lineage.
type TeamPublicationApprovalIssueRequest struct {
	DeploymentID              string
	StoreID                   string
	TeamID                    string
	SharedProjectID           string
	EnvelopeDigest            string
	IdempotencyKeyHash        string
	OperationDigest           string
	PublisherPrincipalID      string
	PublisherMembershipID     string
	PublisherClientKey        string
	PublisherBindingID        string
	Writer                    TeamWriterLeaseIdentity
	ApprovingOwnerPrincipalID string
	ApprovingClientKey        string
	PolicyEpoch               int64
	GlobalEpoch               int64
	AssertionKID              string
	AssertionJTI              string
	AssertionExpiresAt        time.Time
	StepUpAt                  time.Time
	ExpiresAt                 time.Time
}

type TeamPublicationApprovalChallenge struct {
	Nonce     string
	ExpiresAt time.Time
}

// TeamPublicationApprovalConsumeRequest must be rebuilt from trusted remote
// commit inputs. Supplying only the nonce is intentionally insufficient: every
// authorization and publication binding is compared again in the commit tx.
type TeamPublicationApprovalConsumeRequest struct {
	Nonce                     string
	RequestID                 string
	DeploymentID              string
	StoreID                   string
	TeamID                    string
	SharedProjectID           string
	EnvelopeDigest            string
	IdempotencyKeyHash        string
	OperationDigest           string
	PublisherPrincipalID      string
	PublisherMembershipID     string
	PublisherClientKey        string
	PublisherBindingID        string
	Writer                    TeamWriterLeaseIdentity
	ApprovingOwnerPrincipalID string
	ApprovingClientKey        string
	PolicyEpoch               int64
	GlobalEpoch               int64
}

type TeamPublicationApprovalConsumption struct {
	NonceHash        string
	OwnerPrincipalID string
	AuditEventID     string
	ConsumedAt       time.Time
}

type teamPublicationApprovalRow struct {
	NonceHash                 string
	StoreID                   string
	TeamID                    string
	DeploymentID              string
	SharedProjectID           string
	EnvelopeDigest            string
	IdempotencyKeyHash        string
	OperationDigest           string
	PublisherPrincipalID      string
	PublisherMembershipID     string
	PublisherClientKey        string
	PublisherBindingID        string
	RuntimeWriterID           string
	WriterLeaseTokenHash      string
	ApprovingOwnerPrincipalID string
	ApprovingClientKey        string
	PolicyEpoch               int64
	GlobalEpoch               int64
	AssertionKIDHash          string
	AssertionJTIHash          string
	AssertionExpiresAt        time.Time
	StepUpAt                  time.Time
	IssuedAt                  time.Time
	ExpiresAt                 time.Time
	ConsumedAt                *time.Time
	ConsumeAuditEventID       string
}

// IssueTeamPublicationApproval persists a single-use, exact-envelope approval
// after rechecking both the human Owner authority and the currently enrolled
// publisher/runtime identities. It is separate from generic owner approvals so
// no generic admin action can be confused for Commons publication authority.
func (s *Store) IssueTeamPublicationApproval(
	ctx context.Context,
	request TeamPublicationApprovalIssueRequest,
) (TeamPublicationApprovalChallenge, error) {
	now := s.clock().UTC()
	if err := validateTeamPublicationApprovalIssue(request, now); err != nil {
		return TeamPublicationApprovalChallenge{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamPublicationApprovalChallenge{}, err
	}
	defer tx.Rollback()

	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return TeamPublicationApprovalChallenge{}, err
	}
	if request.StoreID != info.StoreID || request.TeamID != info.TeamID {
		return TeamPublicationApprovalChallenge{}, ErrTeamPublicationApprovalBindingMismatch
	}
	if err := s.recheckTeamPublicationApprovalAuthorityTx(ctx, tx, info, publicationApprovalBindingFromIssue(request)); err != nil {
		return TeamPublicationApprovalChallenge{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return TeamPublicationApprovalChallenge{}, err
	}

	for attempts := 0; attempts < 3; attempts++ {
		nonce, nonceErr := newOwnerApprovalNonce()
		if nonceErr != nil {
			return TeamPublicationApprovalChallenge{}, nonceErr
		}
		_, insertErr := tx.ExecContext(ctx, `
			INSERT INTO team_publication_approvals(
				nonce_hash, store_id, team_id, deployment_id, shared_project_id,
				envelope_digest, idempotency_key_hash, operation_digest,
				publisher_principal_id, publisher_membership_id,
				publisher_client_key, publisher_binding_id,
				runtime_writer_id, writer_lease_token_hash,
				approving_owner_principal_id, approving_client_key,
				policy_epoch, global_epoch, assertion_kid_hash, assertion_jti_hash,
				assertion_expires_at, step_up_at, issued_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			teamPublicationApprovalNonceHash(nonce), request.StoreID, request.TeamID,
			request.DeploymentID, request.SharedProjectID, request.EnvelopeDigest,
			request.IdempotencyKeyHash, request.OperationDigest,
			request.PublisherPrincipalID, request.PublisherMembershipID,
			request.PublisherClientKey, request.PublisherBindingID,
			request.Writer.WriterID, writerLeaseTokenHash(request.Writer.Token),
			request.ApprovingOwnerPrincipalID, request.ApprovingClientKey,
			request.PolicyEpoch, request.GlobalEpoch,
			assertionIdentifier("publication-kid", request.AssertionKID),
			assertionIdentifier("publication-jti", request.AssertionJTI),
			request.AssertionExpiresAt.UTC().Format(time.RFC3339Nano),
			request.StepUpAt.UTC().Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano), request.ExpiresAt.UTC().Format(time.RFC3339Nano),
		)
		if insertErr == nil {
			if err := s.recheckTeamPublicationApprovalAuthorityTx(ctx, tx, info, publicationApprovalBindingFromIssue(request)); err != nil {
				return TeamPublicationApprovalChallenge{}, err
			}
			if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
				return TeamPublicationApprovalChallenge{}, err
			}
			if err := tx.Commit(); err != nil {
				if isConstraintError(err) {
					return TeamPublicationApprovalChallenge{}, ErrTeamPublicationApprovalReplay
				}
				return TeamPublicationApprovalChallenge{}, err
			}
			return TeamPublicationApprovalChallenge{Nonce: nonce, ExpiresAt: request.ExpiresAt.UTC()}, nil
		}
		if !isConstraintError(insertErr) {
			return TeamPublicationApprovalChallenge{}, insertErr
		}
		var assertionReplay int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM team_publication_approvals
				 WHERE assertion_kid_hash = ? AND assertion_jti_hash = ?
			)`,
			assertionIdentifier("publication-kid", request.AssertionKID),
			assertionIdentifier("publication-jti", request.AssertionJTI),
		).Scan(&assertionReplay); err != nil {
			return TeamPublicationApprovalChallenge{}, err
		}
		if assertionReplay == 1 {
			return TeamPublicationApprovalChallenge{}, ErrTeamPublicationApprovalReplay
		}
	}
	return TeamPublicationApprovalChallenge{}, ErrTeamPublicationApprovalInvalid
}

// peekTeamPublicationApprovalTx verifies the complete immutable row and all
// live authority without consuming it. This is package-private so the remote
// commit path can plan IDs, then consume and write the receipt in the same tx.
func (s *Store) peekTeamPublicationApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	info TeamStoreInfo,
	request TeamPublicationApprovalConsumeRequest,
) (teamPublicationApprovalRow, error) {
	if tx == nil || !validTeamPublicationApprovalConsume(request) {
		return teamPublicationApprovalRow{}, ErrTeamPublicationApprovalInvalid
	}
	row, err := readTeamPublicationApproval(ctx, tx, request.Nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return teamPublicationApprovalRow{}, ErrTeamPublicationApprovalRequired
	}
	if err != nil {
		return teamPublicationApprovalRow{}, err
	}
	if row.ConsumedAt != nil {
		return teamPublicationApprovalRow{}, ErrTeamPublicationApprovalReplay
	}
	if !row.ExpiresAt.After(s.clock().UTC()) {
		return teamPublicationApprovalRow{}, ErrTeamPublicationApprovalExpired
	}
	if !teamPublicationApprovalMatches(row, info, request) {
		return teamPublicationApprovalRow{}, ErrTeamPublicationApprovalBindingMismatch
	}
	if err := s.recheckTeamPublicationApprovalAuthorityTx(ctx, tx, info, publicationApprovalBindingFromConsume(request)); err != nil {
		return teamPublicationApprovalRow{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return teamPublicationApprovalRow{}, err
	}
	return row, nil
}

// consumeTeamPublicationApprovalTx burns the approval and appends its durable,
// content-free owner/publication audit. The caller must create the Commons
// object and paired receipt before committing this same transaction.
func (s *Store) consumeTeamPublicationApprovalTx(
	ctx context.Context,
	tx *sql.Tx,
	info TeamStoreInfo,
	request TeamPublicationApprovalConsumeRequest,
) (TeamPublicationApprovalConsumption, error) {
	row, err := s.peekTeamPublicationApprovalTx(ctx, tx, info, request)
	if err != nil {
		return TeamPublicationApprovalConsumption{}, err
	}
	now := s.clock().UTC()
	auditID, err := appendTeamPublicationApprovalAudit(ctx, tx, info, row, request, now)
	if err != nil {
		return TeamPublicationApprovalConsumption{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE team_publication_approvals
		   SET consumed_at = ?, consume_audit_event_id = ?
		 WHERE nonce_hash = ? AND consumed_at IS NULL AND expires_at > ?`,
		now.Format(time.RFC3339Nano), auditID, row.NonceHash, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isConstraintError(err) {
			return TeamPublicationApprovalConsumption{}, ErrTeamPublicationApprovalReplay
		}
		return TeamPublicationApprovalConsumption{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return TeamPublicationApprovalConsumption{}, err
	}
	if affected != 1 {
		return TeamPublicationApprovalConsumption{}, ErrTeamPublicationApprovalReplay
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return TeamPublicationApprovalConsumption{}, err
	}
	return TeamPublicationApprovalConsumption{
		NonceHash: row.NonceHash, OwnerPrincipalID: row.ApprovingOwnerPrincipalID,
		AuditEventID: auditID, ConsumedAt: now,
	}, nil
}

type teamPublicationApprovalBinding struct {
	SharedProjectID           string
	PublisherPrincipalID      string
	PublisherMembershipID     string
	PublisherClientKey        string
	PublisherBindingID        string
	ApprovingOwnerPrincipalID string
	ApprovingClientKey        string
	PolicyEpoch               int64
	GlobalEpoch               int64
}

func publicationApprovalBindingFromIssue(request TeamPublicationApprovalIssueRequest) teamPublicationApprovalBinding {
	return teamPublicationApprovalBinding{
		SharedProjectID:           request.SharedProjectID,
		PublisherPrincipalID:      request.PublisherPrincipalID,
		PublisherMembershipID:     request.PublisherMembershipID,
		PublisherClientKey:        request.PublisherClientKey,
		PublisherBindingID:        request.PublisherBindingID,
		ApprovingOwnerPrincipalID: request.ApprovingOwnerPrincipalID,
		ApprovingClientKey:        request.ApprovingClientKey,
		PolicyEpoch:               request.PolicyEpoch, GlobalEpoch: request.GlobalEpoch,
	}
}

func publicationApprovalBindingFromConsume(request TeamPublicationApprovalConsumeRequest) teamPublicationApprovalBinding {
	return teamPublicationApprovalBinding{
		SharedProjectID:           request.SharedProjectID,
		PublisherPrincipalID:      request.PublisherPrincipalID,
		PublisherMembershipID:     request.PublisherMembershipID,
		PublisherClientKey:        request.PublisherClientKey,
		PublisherBindingID:        request.PublisherBindingID,
		ApprovingOwnerPrincipalID: request.ApprovingOwnerPrincipalID,
		ApprovingClientKey:        request.ApprovingClientKey,
		PolicyEpoch:               request.PolicyEpoch, GlobalEpoch: request.GlobalEpoch,
	}
}

func (s *Store) recheckTeamPublicationApprovalAuthorityTx(
	ctx context.Context,
	tx *sql.Tx,
	info TeamStoreInfo,
	binding teamPublicationApprovalBinding,
) error {
	if s.expectedBootstrapRoot == nil {
		return ErrHumanOwnerRequired
	}
	expectedAdminClient := teamauth.OAuthClientKey(
		s.expectedBootstrapRoot.Issuer, s.expectedBootstrapRoot.AdminClientID,
	)
	if !hmac.Equal([]byte(binding.ApprovingClientKey), []byte(expectedAdminClient)) {
		return ErrTeamPublicationApprovalBindingMismatch
	}
	if err := requireHumanOwnerTx(ctx, tx, info.TeamID, binding.ApprovingOwnerPrincipalID); err != nil {
		return err
	}

	var publisherBindings int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		  FROM team_principals publisher
		  JOIN team_agent_bindings binding
		    ON binding.binding_id = ?
		   AND binding.team_id = ?
		   AND binding.agent_principal_id = publisher.principal_id
		   AND binding.client_key = ?
		   AND binding.status = 'active'
		  JOIN team_memberships membership
		    ON membership.membership_id = ?
		   AND membership.team_id = ?
		   AND membership.principal_id = binding.human_principal_id
		   AND membership.status = 'active'
		  JOIN team_principals human
		    ON human.principal_id = membership.principal_id
		   AND human.store_id = ?
		   AND human.kind = 'human'
		   AND human.status = 'active'
		  JOIN team_oauth_clients client
		    ON client.oauth_client_key = binding.client_key
		   AND client.team_id = ?
		   AND client.kind = 'agent'
		   AND client.principal_id = publisher.principal_id
		   AND client.binding_id = binding.binding_id
		 WHERE publisher.principal_id = ?
		   AND publisher.store_id = ?
		   AND publisher.kind = 'agent'
		   AND publisher.status = 'active'`,
		binding.PublisherBindingID, info.TeamID, binding.PublisherClientKey,
		binding.PublisherMembershipID, info.TeamID, info.StoreID, info.TeamID,
		binding.PublisherPrincipalID, info.StoreID,
	).Scan(&publisherBindings); err != nil {
		return err
	}
	if publisherBindings != 1 {
		return ErrTeamPublicationApprovalBindingMismatch
	}

	var projects int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM team_projects
		 WHERE project_id = ? AND team_id = ? AND owner_principal_id = ?`,
		binding.SharedProjectID, info.TeamID, binding.ApprovingOwnerPrincipalID,
	).Scan(&projects); err != nil {
		return err
	}
	if projects != 1 {
		return ErrTeamPublicationApprovalBindingMismatch
	}
	var policyEpoch, globalEpoch int64
	if err := tx.QueryRowContext(ctx, `
		SELECT policy_epoch, global_epoch FROM team_policy_metadata
		 WHERE store_id = ? AND team_id = ?`, info.StoreID, info.TeamID,
	).Scan(&policyEpoch, &globalEpoch); err != nil {
		return err
	}
	if policyEpoch != binding.PolicyEpoch || globalEpoch != binding.GlobalEpoch {
		return ErrTeamPublicationApprovalBindingMismatch
	}
	return nil
}

func validateTeamPublicationApprovalIssue(request TeamPublicationApprovalIssueRequest, now time.Time) error {
	if !validPublicationApprovalIdentityFields(
		request.DeploymentID, request.StoreID, request.TeamID, request.SharedProjectID,
		request.PublisherPrincipalID, request.PublisherMembershipID,
		request.PublisherBindingID, request.ApprovingOwnerPrincipalID,
	) || !validHexDigest(request.EnvelopeDigest) ||
		!validHexDigest(request.IdempotencyKeyHash) || !validHexDigest(request.OperationDigest) ||
		!validHexDigest(request.PublisherClientKey) || !validHexDigest(request.ApprovingClientKey) ||
		!validTeamOpaque(request.Writer.WriterID, 1, 255) ||
		!validTeamOpaque(request.Writer.Token, 1, 255) ||
		request.PolicyEpoch < 1 || request.GlobalEpoch < 1 ||
		!validPublicationAssertionIdentifier(request.AssertionKID) ||
		!validPublicationAssertionIdentifier(request.AssertionJTI) ||
		request.AssertionExpiresAt.IsZero() || request.StepUpAt.IsZero() || request.ExpiresAt.IsZero() {
		return ErrTeamPublicationApprovalInvalid
	}
	stepUp := request.StepUpAt.UTC()
	expires := request.ExpiresAt.UTC()
	assertionExpires := request.AssertionExpiresAt.UTC()
	if stepUp.After(now.Add(30*time.Second)) || now.Sub(stepUp) > ownerApprovalMaxStepUpAge {
		return ErrOwnerStepUpStale
	}
	if !expires.After(now) || expires.Sub(now) > ownerApprovalMaxTTL || expires.Sub(stepUp) > ownerApprovalMaxStepUpAge {
		return ErrTeamPublicationApprovalExpired
	}
	if !assertionExpires.After(now) || assertionExpires.Sub(now) > ownerAssertionMaxTTL {
		return ErrTeamPublicationApprovalExpired
	}
	return nil
}

func validTeamPublicationApprovalConsume(request TeamPublicationApprovalConsumeRequest) bool {
	return validOwnerNonce(request.Nonce) && validTeamOpaque(request.RequestID, 8, 64) &&
		validPublicationApprovalIdentityFields(
			request.DeploymentID, request.StoreID, request.TeamID, request.SharedProjectID,
			request.PublisherPrincipalID, request.PublisherMembershipID,
			request.PublisherBindingID, request.ApprovingOwnerPrincipalID,
		) && validHexDigest(request.EnvelopeDigest) && validHexDigest(request.IdempotencyKeyHash) &&
		validHexDigest(request.OperationDigest) && validHexDigest(request.PublisherClientKey) &&
		validHexDigest(request.ApprovingClientKey) && validTeamOpaque(request.Writer.WriterID, 1, 255) &&
		validTeamOpaque(request.Writer.Token, 1, 255) && request.PolicyEpoch >= 1 && request.GlobalEpoch >= 1
}

func validPublicationApprovalIdentityFields(values ...string) bool {
	for _, value := range values {
		if !validTeamOpaque(value, 1, 255) {
			return false
		}
	}
	return true
}

func validPublicationAssertionIdentifier(value string) bool {
	return value != "" && len(value) <= 1024 && strings.TrimSpace(value) == value
}

func teamPublicationApprovalMatches(
	row teamPublicationApprovalRow,
	info TeamStoreInfo,
	request TeamPublicationApprovalConsumeRequest,
) bool {
	return row.StoreID == info.StoreID && row.TeamID == info.TeamID &&
		row.StoreID == request.StoreID && row.TeamID == request.TeamID &&
		row.DeploymentID == request.DeploymentID && row.SharedProjectID == request.SharedProjectID &&
		hmac.Equal([]byte(row.EnvelopeDigest), []byte(request.EnvelopeDigest)) &&
		hmac.Equal([]byte(row.IdempotencyKeyHash), []byte(request.IdempotencyKeyHash)) &&
		hmac.Equal([]byte(row.OperationDigest), []byte(request.OperationDigest)) &&
		row.PublisherPrincipalID == request.PublisherPrincipalID &&
		row.PublisherMembershipID == request.PublisherMembershipID &&
		hmac.Equal([]byte(row.PublisherClientKey), []byte(request.PublisherClientKey)) &&
		row.PublisherBindingID == request.PublisherBindingID &&
		row.RuntimeWriterID == request.Writer.WriterID &&
		writerLeaseTokenMatches(row.WriterLeaseTokenHash, request.Writer.Token) &&
		row.ApprovingOwnerPrincipalID == request.ApprovingOwnerPrincipalID &&
		hmac.Equal([]byte(row.ApprovingClientKey), []byte(request.ApprovingClientKey)) &&
		row.PolicyEpoch == request.PolicyEpoch && row.GlobalEpoch == request.GlobalEpoch
}

func readTeamPublicationApproval(ctx context.Context, q queryer, nonce string) (teamPublicationApprovalRow, error) {
	var row teamPublicationApprovalRow
	var assertionExpires, stepUp, issued, expires string
	var consumed sql.NullString
	if err := q.QueryRowContext(ctx, `
		SELECT nonce_hash, store_id, team_id, deployment_id, shared_project_id,
		       envelope_digest, idempotency_key_hash, operation_digest,
		       publisher_principal_id, publisher_membership_id,
		       publisher_client_key, publisher_binding_id,
		       runtime_writer_id, writer_lease_token_hash,
		       approving_owner_principal_id, approving_client_key,
		       policy_epoch, global_epoch, assertion_kid_hash, assertion_jti_hash,
		       assertion_expires_at, step_up_at, issued_at, expires_at,
		       consumed_at, COALESCE(consume_audit_event_id, '')
		  FROM team_publication_approvals WHERE nonce_hash = ?`,
		teamPublicationApprovalNonceHash(nonce),
	).Scan(
		&row.NonceHash, &row.StoreID, &row.TeamID, &row.DeploymentID, &row.SharedProjectID,
		&row.EnvelopeDigest, &row.IdempotencyKeyHash, &row.OperationDigest,
		&row.PublisherPrincipalID, &row.PublisherMembershipID,
		&row.PublisherClientKey, &row.PublisherBindingID,
		&row.RuntimeWriterID, &row.WriterLeaseTokenHash,
		&row.ApprovingOwnerPrincipalID, &row.ApprovingClientKey,
		&row.PolicyEpoch, &row.GlobalEpoch, &row.AssertionKIDHash, &row.AssertionJTIHash,
		&assertionExpires, &stepUp, &issued, &expires, &consumed, &row.ConsumeAuditEventID,
	); err != nil {
		return teamPublicationApprovalRow{}, err
	}
	var err error
	if row.AssertionExpiresAt, err = time.Parse(time.RFC3339Nano, assertionExpires); err != nil {
		return teamPublicationApprovalRow{}, ErrTeamPublicationApprovalInvalid
	}
	if row.StepUpAt, err = time.Parse(time.RFC3339Nano, stepUp); err != nil {
		return teamPublicationApprovalRow{}, ErrTeamPublicationApprovalInvalid
	}
	if row.IssuedAt, err = time.Parse(time.RFC3339Nano, issued); err != nil {
		return teamPublicationApprovalRow{}, ErrTeamPublicationApprovalInvalid
	}
	if row.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires); err != nil {
		return teamPublicationApprovalRow{}, ErrTeamPublicationApprovalInvalid
	}
	if consumed.Valid {
		consumedAt, parseErr := time.Parse(time.RFC3339Nano, consumed.String)
		if parseErr != nil {
			return teamPublicationApprovalRow{}, ErrTeamPublicationApprovalInvalid
		}
		row.ConsumedAt = &consumedAt
	}
	return row, nil
}

func appendTeamPublicationApprovalAudit(
	ctx context.Context,
	tx *sql.Tx,
	info TeamStoreInfo,
	row teamPublicationApprovalRow,
	request TeamPublicationApprovalConsumeRequest,
	now time.Time,
) (string, error) {
	var policyVersion int
	var globalEpoch int64
	if err := tx.QueryRowContext(ctx, `
		SELECT policy_version, global_epoch FROM team_policy_metadata
		 WHERE store_id = ? AND team_id = ?`, info.StoreID, info.TeamID,
	).Scan(&policyVersion, &globalEpoch); err != nil {
		return "", err
	}
	if globalEpoch != row.GlobalEpoch {
		return "", ErrTeamPublicationApprovalBindingMismatch
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
		VALUES (?, ?, ?, ?, 'allowed', ?, ?, ?, ?, 'publication_envelope', ?, ?,
		        ?, 'team-remote', ?, ?, '{}')`,
		eventID, info.StoreID, now.Format(time.RFC3339Nano),
		teamPublicationApprovalAction, row.ApprovingOwnerPrincipalID,
		row.ApprovingClientKey, info.TeamID, row.SharedProjectID,
		row.EnvelopeDigest, request.RequestID, policyVersion, globalEpoch,
		teamPublicationApprovalReason,
	)
	if err != nil {
		return "", err
	}
	return eventID, nil
}

func teamPublicationApprovalNonceHash(nonce string) string {
	return ownerApprovalDigest("pulse-team-publication-approval-nonce-v1", nonce)
}
