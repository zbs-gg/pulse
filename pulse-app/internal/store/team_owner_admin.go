package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

// OwnerAdminMutation is a closed metadata-only union. Actor/store/team are
// intentionally absent: ExecuteApprovedOwnerAdminMutation derives them from
// the one-time approval and the marked store.
type OwnerAdminMutation struct {
	Action            string
	Issuer            string
	Subject           string
	ClientID          string
	Role              string
	Name              string
	TargetID          string
	ProjectID         string
	TargetPrincipalID string
	AccessLevel       string
}

type ApprovedOwnerAdminMutationRequest struct {
	Mutation      OwnerAdminMutation
	ApprovalNonce string
	RequestID     string
	ClientKey     string
	Writer        TeamWriterLeaseIdentity
}

type OwnerAdminMutationResult struct {
	Action       string
	AuditEventID string
	AuthEpoch    int64
	Member       *TeamMember
	Binding      *AgentBinding
	Service      *ServicePrincipal
	Project      *TeamProject
	Grant        *ProjectGrant
}

// OwnerAdminMutationTarget canonicalizes the exact body that a browser
// approval authorizes. Raw external identity strings contribute only through
// existing domain-separated identity keys.
func OwnerAdminMutationTarget(mutation OwnerAdminMutation) (string, string, string, error) {
	mutation.Name = strings.TrimSpace(mutation.Name)
	switch mutation.Action {
	case OwnerActionMembershipCreate:
		if !validExactIdentityValue(mutation.Issuer) || !validExactIdentityValue(mutation.Subject) ||
			(mutation.Role != "owner" && mutation.Role != "member" && mutation.Role != "reviewer") {
			return "", "", "", ErrInvalidTeamIdentityMutation
		}
		identityKey := teamauth.HumanIdentityKey(mutation.Issuer, mutation.Subject)
		return "membership", identityKey,
			ownerApprovalDigest(mutation.Action, identityKey, mutation.Role), nil
	case OwnerActionMembershipRevoke:
		if !validOwnerOpaque(mutation.TargetID, 255) {
			return "", "", "", ErrInvalidTeamIdentityMutation
		}
		return "membership", mutation.TargetID,
			ownerApprovalDigest(mutation.Action, mutation.TargetID), nil
	case OwnerActionAgentBindingCreate:
		if !validExactIdentityValue(mutation.Issuer) || !validExactIdentityValue(mutation.Subject) ||
			!validExactIdentityValue(mutation.ClientID) {
			return "", "", "", ErrInvalidTeamIdentityMutation
		}
		bindingKey := teamauth.AgentBindingKey(mutation.Issuer, mutation.Subject, mutation.ClientID)
		return "agent_binding", bindingKey,
			ownerApprovalDigest(mutation.Action, bindingKey), nil
	case OwnerActionAgentBindingRevoke:
		if !validOwnerOpaque(mutation.TargetID, 255) {
			return "", "", "", ErrInvalidTeamIdentityMutation
		}
		return "agent_binding", mutation.TargetID,
			ownerApprovalDigest(mutation.Action, mutation.TargetID), nil
	case OwnerActionServicePrincipalCreate:
		if !validExactIdentityValue(mutation.Issuer) || !validExactIdentityValue(mutation.ClientID) {
			return "", "", "", ErrInvalidTeamIdentityMutation
		}
		serviceKey := teamauth.ServiceIdentityKey(mutation.Issuer, mutation.ClientID)
		return "service_principal", serviceKey,
			ownerApprovalDigest(mutation.Action, serviceKey), nil
	case OwnerActionServicePrincipalRevoke:
		if !validOwnerOpaque(mutation.TargetID, 255) {
			return "", "", "", ErrInvalidTeamIdentityMutation
		}
		return "service_principal", mutation.TargetID,
			ownerApprovalDigest(mutation.Action, mutation.TargetID), nil
	case OwnerActionProjectCreate:
		if mutation.Name == "" {
			return "", "", "", ErrInvalidTeamIdentityMutation
		}
		nameKey := ownerApprovalDigest("project-name-v1", mutation.Name)
		return "project", nameKey, ownerApprovalDigest(mutation.Action, nameKey), nil
	case OwnerActionProjectGrantCreate:
		if !validOwnerOpaque(mutation.ProjectID, 255) ||
			!validOwnerOpaque(mutation.TargetPrincipalID, 255) ||
			(mutation.AccessLevel != "read" && mutation.AccessLevel != "write" && mutation.AccessLevel != "admin") {
			return "", "", "", ErrInvalidTeamIdentityMutation
		}
		grantKey := ownerApprovalDigest("project-grant-v1", mutation.ProjectID, mutation.TargetPrincipalID)
		return "project_grant", grantKey,
			ownerApprovalDigest(mutation.Action, mutation.ProjectID, mutation.TargetPrincipalID, mutation.AccessLevel), nil
	case OwnerActionProjectGrantRevoke:
		if !validOwnerOpaque(mutation.TargetID, 255) {
			return "", "", "", ErrInvalidTeamIdentityMutation
		}
		return "project_grant", mutation.TargetID,
			ownerApprovalDigest(mutation.Action, mutation.TargetID), nil
	default:
		return "", "", "", ErrInvalidTeamIdentityMutation
	}
}

func (s *Store) ExecuteApprovedOwnerAdminMutation(ctx context.Context, request ApprovedOwnerAdminMutationRequest) (OwnerAdminMutationResult, error) {
	targetKind, approvalTargetID, targetDigest, err := OwnerAdminMutationTarget(request.Mutation)
	if err != nil || !validOwnerNonce(request.ApprovalNonce) ||
		!validOwnerOpaque(request.RequestID, 255) || !validOwnerClientKey(request.ClientKey) ||
		!validTeamOpaque(request.Writer.WriterID, 1, 255) || !validTeamOpaque(request.Writer.Token, 1, 255) {
		return OwnerAdminMutationResult{}, ErrOwnerApprovalInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OwnerAdminMutationResult{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return OwnerAdminMutationResult{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return OwnerAdminMutationResult{}, err
	}
	consumeRequest := OwnerApprovalConsumeRequest{
		StoreID: info.StoreID, TeamID: info.TeamID, Action: request.Mutation.Action,
		TargetKind: targetKind, TargetID: approvalTargetID, TargetDigest: targetDigest,
		Nonce: request.ApprovalNonce, RequestID: request.RequestID, ClientKey: request.ClientKey,
		Writer: request.Writer,
	}
	approval, err := s.peekOwnerApprovalTx(ctx, tx, info, consumeRequest)
	if err != nil {
		return OwnerAdminMutationResult{}, err
	}
	result := OwnerAdminMutationResult{Action: request.Mutation.Action, AuthEpoch: info.AuthEpoch}
	actualKind, actualID, changed, err := s.executeOwnerAdminMutationTx(
		ctx, tx, info, approval.OwnerPrincipalID, request.Mutation, &result,
	)
	if err != nil {
		return OwnerAdminMutationResult{}, err
	}
	if changed {
		result.AuthEpoch, err = bumpTeamEpoch(ctx, tx)
		if err != nil {
			return OwnerAdminMutationResult{}, err
		}
	}
	consumed, err := s.consumeOwnerApprovalTxWithAuditTarget(
		ctx, tx, info, consumeRequest, actualKind, actualID,
	)
	if err != nil {
		return OwnerAdminMutationResult{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return OwnerAdminMutationResult{}, err
	}
	result.AuditEventID = consumed.AuditEventID
	if err := tx.Commit(); err != nil {
		if isConstraintError(err) {
			return OwnerAdminMutationResult{}, ErrOwnerApprovalReplay
		}
		return OwnerAdminMutationResult{}, err
	}
	return result, nil
}

func (s *Store) peekOwnerApprovalTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, request OwnerApprovalConsumeRequest) (ownerApprovalRow, error) {
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
	if row.ConsumedAt != nil {
		return ownerApprovalRow{}, ErrOwnerApprovalReplay
	}
	if !row.ExpiresAt.After(s.clock().UTC()) {
		return ownerApprovalRow{}, ErrOwnerApprovalExpired
	}
	if row.StoreID != info.StoreID || row.TeamID != info.TeamID ||
		row.Action != request.Action || row.TargetKind != request.TargetKind ||
		row.TargetID != request.TargetID || row.TargetDigest != request.TargetDigest ||
		row.ClientKey != request.ClientKey || !ownerApprovalWriterMatches(row, request.Writer) {
		return ownerApprovalRow{}, ErrOwnerApprovalBindingMismatch
	}
	if err := requireHumanOwnerTx(ctx, tx, info.TeamID, row.OwnerPrincipalID); err != nil {
		return ownerApprovalRow{}, err
	}
	return row, nil
}

func (s *Store) executeOwnerAdminMutationTx(
	ctx context.Context,
	tx *sql.Tx,
	info TeamStoreInfo,
	ownerID string,
	mutation OwnerAdminMutation,
	result *OwnerAdminMutationResult,
) (string, string, bool, error) {
	switch mutation.Action {
	case OwnerActionMembershipCreate:
		value, changed, err := s.addApprovedMemberTx(ctx, tx, info, mutation)
		result.Member = &value
		return "membership", value.MembershipID, changed, err
	case OwnerActionMembershipRevoke:
		membershipID, err := s.revokeApprovedMembershipTx(ctx, tx, info, mutation.TargetID)
		return "membership", membershipID, err == nil, err
	case OwnerActionAgentBindingCreate:
		value, changed, err := s.addApprovedAgentBindingTx(ctx, tx, info, mutation)
		result.Binding = &value
		return "agent_binding", value.BindingID, changed, err
	case OwnerActionAgentBindingRevoke:
		err := s.revokeApprovedAgentBindingTx(ctx, tx, info, mutation.TargetID)
		return "agent_binding", mutation.TargetID, err == nil, err
	case OwnerActionServicePrincipalCreate:
		value, changed, err := s.addApprovedServiceTx(ctx, tx, info, mutation)
		result.Service = &value
		return "service_principal", value.PrincipalID, changed, err
	case OwnerActionServicePrincipalRevoke:
		err := s.revokeApprovedServiceTx(ctx, tx, info, mutation.TargetID)
		return "service_principal", mutation.TargetID, err == nil, err
	case OwnerActionProjectCreate:
		value, changed, err := s.addApprovedProjectTx(ctx, tx, info, ownerID, mutation.Name)
		result.Project = &value
		return "project", value.ProjectID, changed, err
	case OwnerActionProjectGrantCreate:
		value, changed, err := s.addApprovedProjectGrantTx(ctx, tx, info, mutation)
		result.Grant = &value
		return "project_grant", value.GrantID, changed, err
	case OwnerActionProjectGrantRevoke:
		err := s.revokeApprovedProjectGrantTx(ctx, tx, info, mutation.TargetID)
		return "project_grant", mutation.TargetID, err == nil, err
	default:
		return "", "", false, ErrInvalidTeamIdentityMutation
	}
}

func (s *Store) addApprovedMemberTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, mutation OwnerAdminMutation) (TeamMember, bool, error) {
	identityKey := teamauth.HumanIdentityKey(mutation.Issuer, mutation.Subject)
	var existing TeamMember
	var status string
	err := tx.QueryRowContext(ctx, `
		SELECT hi.human_principal_id, membership.membership_id, membership.role,
		       membership.auth_epoch, membership.status
		  FROM team_human_identities hi
		  JOIN team_memberships membership
		    ON membership.principal_id = hi.human_principal_id AND membership.team_id = ?
		 WHERE hi.identity_key = ?`, info.TeamID, identityKey).Scan(
		&existing.PrincipalID, &existing.MembershipID, &existing.Role, &existing.AuthEpoch, &status,
	)
	if err == nil {
		if status != "active" {
			return TeamMember{}, false, ErrPrincipalRevoked
		}
		if existing.Role != mutation.Role {
			return TeamMember{}, false, ErrInvalidTeamIdentityMutation
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TeamMember{}, false, err
	}
	principalID, err := newOpaqueID("principal")
	if err != nil {
		return TeamMember{}, false, err
	}
	membershipID, err := newOpaqueID("membership")
	if err != nil {
		return TeamMember{}, false, err
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_principals(principal_id, store_id, kind, status, auth_epoch, created_at)
		VALUES (?, ?, 'human', 'active', 1, ?)`, principalID, info.StoreID, now); err != nil {
		return TeamMember{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_human_identities(identity_key, human_principal_id, created_at)
		VALUES (?, ?, ?)`, identityKey, principalID, now); err != nil {
		return TeamMember{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_memberships(
			membership_id, team_id, principal_id, role, status, auth_epoch, created_at)
		VALUES (?, ?, ?, ?, 'active', 1, ?)`,
		membershipID, info.TeamID, principalID, mutation.Role, now); err != nil {
		return TeamMember{}, false, err
	}
	return TeamMember{PrincipalID: principalID, MembershipID: membershipID, Role: mutation.Role, AuthEpoch: 1}, true, nil
}

func (s *Store) revokeApprovedMembershipTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, targetID string) (string, error) {
	var membershipID, role, kind string
	if err := tx.QueryRowContext(ctx, `
		SELECT membership.membership_id, membership.role, principal.kind
		  FROM team_memberships membership
		  JOIN team_principals principal ON principal.principal_id = membership.principal_id
		 WHERE membership.team_id = ? AND membership.principal_id = ?
		   AND membership.status = 'active' AND principal.status = 'active'`,
		info.TeamID, targetID).Scan(&membershipID, &role, &kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrMembershipRequired
		}
		return "", err
	}
	if kind != "human" {
		return "", ErrHumanPrincipalRequired
	}
	if role == "owner" {
		var owners int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM team_memberships
			 WHERE team_id = ? AND role = 'owner' AND status = 'active'`, info.TeamID).Scan(&owners); err != nil {
			return "", err
		}
		if owners <= 1 {
			return "", ErrLastOwner
		}
	}
	var projects int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM team_projects
		 WHERE team_id = ? AND owner_principal_id = ?`, info.TeamID, targetID).Scan(&projects); err != nil {
		return "", err
	}
	if projects != 0 {
		return "", ErrProjectOwnershipTransferRequired
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_project_grants SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE status = 'active' AND (
			principal_id = ? OR principal_id IN (
				SELECT agent_principal_id FROM team_agent_bindings
				 WHERE human_principal_id = ? AND status = 'active'
			)
		)`, now, targetID, targetID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_memberships SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE membership_id = ?`, now, membershipID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_principals SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE principal_id = ? OR principal_id IN (
			SELECT agent_principal_id FROM team_agent_bindings
			 WHERE human_principal_id = ? AND status = 'active'
		)`, now, targetID, targetID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_agent_bindings SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE human_principal_id = ? AND status = 'active'`, now, targetID); err != nil {
		return "", err
	}
	return membershipID, nil
}

func (s *Store) addApprovedAgentBindingTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, mutation OwnerAdminMutation) (AgentBinding, bool, error) {
	identityKey := teamauth.HumanIdentityKey(mutation.Issuer, mutation.Subject)
	var humanID string
	if err := tx.QueryRowContext(ctx, `
		SELECT identity.human_principal_id
		  FROM team_human_identities identity
		  JOIN team_principals human ON human.principal_id = identity.human_principal_id AND human.status = 'active'
		  JOIN team_memberships membership
		    ON membership.principal_id = human.principal_id AND membership.team_id = ? AND membership.status = 'active'
		 WHERE identity.identity_key = ?`, info.TeamID, identityKey).Scan(&humanID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AgentBinding{}, false, ErrMembershipRequired
		}
		return AgentBinding{}, false, err
	}
	oauthKey := teamauth.OAuthClientKey(mutation.Issuer, mutation.ClientID)
	var existing AgentBinding
	var status, kind, registeredPrincipal string
	err := tx.QueryRowContext(ctx, `
		SELECT client.kind, client.principal_id, binding.binding_id,
		       binding.human_principal_id, binding.agent_principal_id,
		       binding.auth_epoch, binding.status
		  FROM team_oauth_clients client
		  JOIN team_agent_bindings binding ON binding.binding_id = client.binding_id
		 WHERE client.oauth_client_key = ?`, oauthKey).Scan(
		&kind, &registeredPrincipal, &existing.BindingID, &existing.HumanPrincipalID,
		&existing.AgentPrincipalID, &existing.AuthEpoch, &status,
	)
	if err == nil {
		if kind != "agent" || registeredPrincipal != existing.AgentPrincipalID ||
			existing.HumanPrincipalID != humanID {
			return AgentBinding{}, false, ErrInvalidTeamIdentityMutation
		}
		if status != "active" {
			return AgentBinding{}, false, ErrPrincipalRevoked
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AgentBinding{}, false, err
	}
	agentID, err := newOpaqueID("principal")
	if err != nil {
		return AgentBinding{}, false, err
	}
	bindingID, err := newOpaqueID("binding")
	if err != nil {
		return AgentBinding{}, false, err
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_principals(principal_id, store_id, kind, status, auth_epoch, created_at)
		VALUES (?, ?, 'agent', 'active', 1, ?)`, agentID, info.StoreID, now); err != nil {
		return AgentBinding{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_agent_bindings(
			binding_id, team_id, human_principal_id, agent_principal_id,
			binding_key, client_key, status, auth_epoch, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'active', 1, ?)`, bindingID, info.TeamID,
		humanID, agentID, teamauth.AgentBindingKey(mutation.Issuer, mutation.Subject, mutation.ClientID),
		oauthKey, now); err != nil {
		return AgentBinding{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_oauth_clients(
			oauth_client_key, team_id, kind, principal_id, binding_id, created_at)
		VALUES (?, ?, 'agent', ?, ?, ?)`, oauthKey, info.TeamID, agentID, bindingID, now); err != nil {
		return AgentBinding{}, false, err
	}
	return AgentBinding{BindingID: bindingID, HumanPrincipalID: humanID, AgentPrincipalID: agentID, AuthEpoch: 1}, true, nil
}

func (s *Store) revokeApprovedAgentBindingTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, bindingID string) error {
	var agentID string
	if err := tx.QueryRowContext(ctx, `
		SELECT agent_principal_id FROM team_agent_bindings
		 WHERE binding_id = ? AND team_id = ? AND status = 'active'`, bindingID, info.TeamID).Scan(&agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPrincipalRevoked
		}
		return err
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_agent_bindings SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE binding_id = ?`, now, bindingID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_principals SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE principal_id = ?`, now, agentID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE team_project_grants SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE principal_id = ? AND status = 'active'`, now, agentID)
	return err
}

func (s *Store) addApprovedServiceTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, mutation OwnerAdminMutation) (ServicePrincipal, bool, error) {
	oauthKey := teamauth.OAuthClientKey(mutation.Issuer, mutation.ClientID)
	var existing ServicePrincipal
	var kind, status string
	err := tx.QueryRowContext(ctx, `
		SELECT client.kind, client.principal_id, membership.membership_id,
		       principal.auth_epoch, principal.status
		  FROM team_oauth_clients client
		  JOIN team_principals principal ON principal.principal_id = client.principal_id
		  JOIN team_memberships membership
		    ON membership.principal_id = principal.principal_id AND membership.team_id = ?
		 WHERE client.oauth_client_key = ?`, info.TeamID, oauthKey).Scan(
		&kind, &existing.PrincipalID, &existing.MembershipID, &existing.AuthEpoch, &status,
	)
	if err == nil {
		if kind != "service" {
			return ServicePrincipal{}, false, ErrInvalidTeamIdentityMutation
		}
		if status != "active" {
			return ServicePrincipal{}, false, ErrPrincipalRevoked
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ServicePrincipal{}, false, err
	}
	principalID, err := newOpaqueID("principal")
	if err != nil {
		return ServicePrincipal{}, false, err
	}
	membershipID, err := newOpaqueID("membership")
	if err != nil {
		return ServicePrincipal{}, false, err
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_principals(principal_id, store_id, kind, status, auth_epoch, created_at)
		VALUES (?, ?, 'service', 'active', 1, ?)`, principalID, info.StoreID, now); err != nil {
		return ServicePrincipal{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_service_identities(service_key, client_key, service_principal_id, created_at)
		VALUES (?, ?, ?, ?)`, teamauth.ServiceIdentityKey(mutation.Issuer, mutation.ClientID),
		oauthKey, principalID, now); err != nil {
		return ServicePrincipal{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_oauth_clients(oauth_client_key, team_id, kind, principal_id, binding_id, created_at)
		VALUES (?, ?, 'service', ?, NULL, ?)`, oauthKey, info.TeamID, principalID, now); err != nil {
		return ServicePrincipal{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_memberships(membership_id, team_id, principal_id, role, status, auth_epoch, created_at)
		VALUES (?, ?, ?, 'member', 'active', 1, ?)`, membershipID, info.TeamID, principalID, now); err != nil {
		return ServicePrincipal{}, false, err
	}
	return ServicePrincipal{PrincipalID: principalID, MembershipID: membershipID, AuthEpoch: 1}, true, nil
}

func (s *Store) revokeApprovedServiceTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, principalID string) error {
	var membershipID string
	if err := tx.QueryRowContext(ctx, `
		SELECT membership.membership_id
		  FROM team_principals principal
		  JOIN team_memberships membership
		    ON membership.principal_id = principal.principal_id AND membership.team_id = ?
		 WHERE principal.principal_id = ? AND principal.kind = 'service'
		   AND principal.status = 'active' AND membership.status = 'active'`,
		info.TeamID, principalID).Scan(&membershipID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPrincipalRevoked
		}
		return err
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_principals SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE principal_id = ?`, now, principalID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_memberships SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE membership_id = ?`, now, membershipID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE team_project_grants SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE principal_id = ? AND status = 'active'`, now, principalID)
	return err
}

func (s *Store) addApprovedProjectTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, ownerID, name string) (TeamProject, bool, error) {
	name = strings.TrimSpace(name)
	var existing TeamProject
	err := tx.QueryRowContext(ctx, `
		SELECT project_id, team_id, name, owner_principal_id, created_by_principal_id
		  FROM team_projects WHERE team_id = ? AND name = ?`, info.TeamID, name).Scan(
		&existing.ProjectID, &existing.TeamID, &existing.Name,
		&existing.OwnerPrincipalID, &existing.CreatedByPrincipalID,
	)
	if err == nil {
		if existing.OwnerPrincipalID != ownerID || existing.CreatedByPrincipalID != ownerID {
			return TeamProject{}, false, ErrInvalidTeamIdentityMutation
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TeamProject{}, false, err
	}
	projectID, err := newOpaqueID("project")
	if err != nil {
		return TeamProject{}, false, err
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_projects(
			project_id, team_id, name, owner_principal_id, created_by_principal_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, projectID, info.TeamID, name, ownerID, ownerID, now); err != nil {
		return TeamProject{}, false, err
	}
	return TeamProject{
		ProjectID: projectID, TeamID: info.TeamID, Name: name,
		OwnerPrincipalID: ownerID, CreatedByPrincipalID: ownerID,
	}, true, nil
}

func (s *Store) addApprovedProjectGrantTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, mutation OwnerAdminMutation) (ProjectGrant, bool, error) {
	var projectTeam string
	if err := tx.QueryRowContext(ctx, `SELECT team_id FROM team_projects WHERE project_id = ?`, mutation.ProjectID).Scan(&projectTeam); err != nil || projectTeam != info.TeamID {
		return ProjectGrant{}, false, ErrMembershipRequired
	}
	if err := requireGrantablePrincipal(ctx, tx, info.TeamID, mutation.TargetPrincipalID); err != nil {
		return ProjectGrant{}, false, err
	}
	var existing ProjectGrant
	var status string
	err := tx.QueryRowContext(ctx, `
		SELECT grant_id, project_id, principal_id, access_level, auth_epoch, status
		  FROM team_project_grants WHERE project_id = ? AND principal_id = ?`,
		mutation.ProjectID, mutation.TargetPrincipalID).Scan(
		&existing.GrantID, &existing.ProjectID, &existing.PrincipalID,
		&existing.AccessLevel, &existing.AuthEpoch, &status,
	)
	if err == nil {
		if status != "active" {
			return ProjectGrant{}, false, ErrProjectGrantRequired
		}
		if existing.AccessLevel != mutation.AccessLevel {
			return ProjectGrant{}, false, ErrInvalidTeamIdentityMutation
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProjectGrant{}, false, err
	}
	grantID, err := newOpaqueID("grant")
	if err != nil {
		return ProjectGrant{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_project_grants(
			grant_id, project_id, principal_id, access_level, status, auth_epoch, created_at)
		VALUES (?, ?, ?, ?, 'active', 1, ?)`, grantID, mutation.ProjectID,
		mutation.TargetPrincipalID, mutation.AccessLevel,
		s.clock().UTC().Format(time.RFC3339Nano)); err != nil {
		return ProjectGrant{}, false, err
	}
	return ProjectGrant{
		GrantID: grantID, ProjectID: mutation.ProjectID,
		PrincipalID: mutation.TargetPrincipalID, AccessLevel: mutation.AccessLevel, AuthEpoch: 1,
	}, true, nil
}

func (s *Store) revokeApprovedProjectGrantTx(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, grantID string) error {
	var projectID string
	if err := tx.QueryRowContext(ctx, `
		SELECT grant.project_id
		  FROM team_project_grants grant
		  JOIN team_projects project ON project.project_id = grant.project_id
		 WHERE grant.grant_id = ? AND grant.status = 'active' AND project.team_id = ?`,
		grantID, info.TeamID).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProjectGrantRequired
		}
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE team_project_grants SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE grant_id = ?`, s.clock().UTC().Format(time.RFC3339Nano), grantID)
	return err
}
