package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

var (
	ErrTeamStoreUninitialized           = errors.New("team store is not bootstrapped")
	ErrBootstrapRootMismatch            = errors.New("presented bootstrap root does not match deployment-pinned root")
	ErrBootstrapConsumed                = errors.New("team store bootstrap has already been consumed")
	ErrLegacyLocalData                  = errors.New("unscoped local data cannot be adopted into a team store")
	ErrTeamStoreRequiresTeamOpen        = errors.New("marked team store requires OpenTeam")
	ErrTeamStoreIdentityMismatch        = errors.New("team store identity does not match deployment configuration")
	ErrUnsupportedTeamSchema            = errors.New("binary does not satisfy team store reader/writer floor")
	ErrMembershipRequired               = errors.New("active team membership is required")
	ErrPrincipalRevoked                 = errors.New("principal or binding is revoked")
	ErrLastOwner                        = errors.New("cannot revoke the last active owner")
	ErrInvalidTeamIdentityMutation      = errors.New("invalid team identity mutation")
	ErrAssertionReplay                  = errors.New("assertion identifier has already been consumed")
	ErrAssertionExpired                 = errors.New("assertion is expired")
	ErrProjectGrantRequired             = errors.New("active project grant is required")
	ErrTeamBootstrapCandidateRequired   = errors.New("team bootstrap requires a fresh team-store candidate")
	ErrProjectOwnershipTransferRequired = errors.New("project ownership must be transferred before revocation")
	ErrHumanPrincipalRequired           = errors.New("human principal is required")
)

type TeamStoreInfo struct {
	StoreID          string
	TeamID           string
	TeamName         string
	MinReaderVersion int
	MinWriterVersion int
	AuthEpoch        int64
}

type TeamReadinessOptions struct {
	ExpectedStoreID string
	ExpectedTeamID  string
	ReaderVersion   int
	WriterVersion   int
}

type BootstrapTeamRequest struct {
	TeamName      string
	PresentedRoot teamauth.BootstrapRoot
}

type BootstrapResult struct {
	TeamStoreInfo
	OwnerPrincipalID  string
	OwnerMembershipID string
}

type RegisterAgentBindingRequest struct {
	ActorPrincipalID string
	Issuer           string
	Subject          string
	ClientID         string
}

type AgentBinding struct {
	BindingID        string
	HumanPrincipalID string
	AgentPrincipalID string
	AuthEpoch        int64
}

type AddTeamMemberRequest struct {
	ActorPrincipalID string
	Issuer           string
	Subject          string
	Role             string
}

type TeamMember struct {
	PrincipalID  string
	MembershipID string
	Role         string
	AuthEpoch    int64
}

type RegisterServicePrincipalRequest struct {
	ActorPrincipalID string
	Issuer           string
	ClientID         string
}

type ServicePrincipal struct {
	PrincipalID  string
	MembershipID string
	AuthEpoch    int64
}

type TeamProject struct {
	ProjectID            string
	TeamID               string
	Name                 string
	OwnerPrincipalID     string
	CreatedByPrincipalID string
}

type GrantProjectAccessRequest struct {
	ActorPrincipalID  string
	ProjectID         string
	TargetPrincipalID string
	AccessLevel       string
}

type ProjectGrant struct {
	GrantID     string
	ProjectID   string
	PrincipalID string
	AccessLevel string
	AuthEpoch   int64
}

type ResolvedTeamPrincipal struct {
	StoreID          string
	TeamID           string
	PrincipalID      string
	Kind             string
	HumanPrincipalID string
	BindingID        string
	MembershipID     string
	MembershipRole   string
	PrincipalStatus  string
	BindingStatus    string
	MembershipStatus string
	PrincipalEpoch   int64
	BindingEpoch     int64
	MembershipEpoch  int64
	TeamEpoch        int64
}

type ResolvedOAuthClient struct {
	OAuthClientKey string
	Kind           string
	PrincipalID    string
	BindingID      string
}

func (s *Store) CheckTeamReadiness(ctx context.Context, options TeamReadinessOptions) (TeamStoreInfo, error) {
	info, err := readTeamStoreInfo(ctx, s.db)
	if err != nil {
		return TeamStoreInfo{}, err
	}
	if err := validateTeamReadinessInfo(info, options); err != nil {
		return TeamStoreInfo{}, err
	}
	if err := verifyTeamPragmas(s.db); err != nil {
		return TeamStoreInfo{}, err
	}
	legacy, err := countLegacyRows(ctx, s.db)
	if err != nil {
		return TeamStoreInfo{}, err
	}
	if legacy != 0 {
		return TeamStoreInfo{}, ErrLegacyLocalData
	}
	return info, nil
}

func validateTeamReadinessInfo(info TeamStoreInfo, options TeamReadinessOptions) error {
	if (options.ExpectedStoreID != "" && options.ExpectedStoreID != info.StoreID) ||
		(options.ExpectedTeamID != "" && options.ExpectedTeamID != info.TeamID) {
		return ErrTeamStoreIdentityMismatch
	}
	readerVersion := options.ReaderVersion
	if readerVersion == 0 {
		readerVersion = teamauth.SchemaVersion
	}
	writerVersion := options.WriterVersion
	if writerVersion == 0 {
		writerVersion = teamauth.SchemaVersion
	}
	if readerVersion < info.MinReaderVersion || writerVersion < info.MinWriterVersion {
		return fmt.Errorf("%w: store requires reader %d and writer %d", ErrUnsupportedTeamSchema, info.MinReaderVersion, info.MinWriterVersion)
	}
	return nil
}

func (s *Store) CurrentTeamAuthEpoch(ctx context.Context) (int64, error) {
	info, err := readTeamStoreInfo(ctx, s.db)
	if err != nil {
		return 0, err
	}
	return info.AuthEpoch, nil
}

// ResolveTeamPrincipal reads current principal, membership, and (for agents)
// binding state in one store call. It returns the populated state alongside
// ErrPrincipalRevoked when any required layer is inactive so policy callers
// can fail closed without losing metadata needed for reasoned audit.
func (s *Store) ResolveTeamPrincipal(ctx context.Context, principalID string) (ResolvedTeamPrincipal, error) {
	info, err := readTeamStoreInfo(ctx, s.db)
	if err != nil {
		return ResolvedTeamPrincipal{}, err
	}
	return resolveTeamPrincipal(ctx, s.db, info, principalID)
}

func resolveTeamPrincipal(ctx context.Context, q queryer, info TeamStoreInfo, principalID string) (ResolvedTeamPrincipal, error) {
	resolved := ResolvedTeamPrincipal{
		StoreID: info.StoreID, TeamID: info.TeamID, PrincipalID: principalID, TeamEpoch: info.AuthEpoch,
	}
	if err := q.QueryRowContext(ctx, `
		SELECT kind, status, auth_epoch FROM team_principals
		 WHERE principal_id = ? AND store_id = ?`, principalID, info.StoreID).
		Scan(&resolved.Kind, &resolved.PrincipalStatus, &resolved.PrincipalEpoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return resolved, ErrPrincipalRevoked
		}
		return resolved, err
	}
	var err error
	switch resolved.Kind {
	case string(teamauth.PrincipalHuman), string(teamauth.PrincipalService):
		err = q.QueryRowContext(ctx, `
			SELECT membership_id, role, status, auth_epoch FROM team_memberships
			 WHERE team_id = ? AND principal_id = ?`, info.TeamID, principalID).
			Scan(&resolved.MembershipID, &resolved.MembershipRole, &resolved.MembershipStatus, &resolved.MembershipEpoch)
	case string(teamauth.PrincipalAgent):
		err = q.QueryRowContext(ctx, `
			SELECT b.binding_id, b.human_principal_id, b.status, b.auth_epoch,
			       m.membership_id, m.role, m.status, m.auth_epoch
			  FROM team_agent_bindings b
			  JOIN team_principals hp
			    ON hp.principal_id = b.human_principal_id
			   AND hp.store_id = ? AND hp.status = 'active'
			  JOIN team_memberships m ON m.team_id = b.team_id AND m.principal_id = b.human_principal_id
			 WHERE b.team_id = ? AND b.agent_principal_id = ?`, info.StoreID, info.TeamID, principalID).
			Scan(&resolved.BindingID, &resolved.HumanPrincipalID, &resolved.BindingStatus, &resolved.BindingEpoch,
				&resolved.MembershipID, &resolved.MembershipRole, &resolved.MembershipStatus, &resolved.MembershipEpoch)
	default:
		return resolved, ErrPrincipalRevoked
	}
	if errors.Is(err, sql.ErrNoRows) {
		return resolved, ErrPrincipalRevoked
	}
	if err != nil {
		return resolved, err
	}
	if resolved.PrincipalStatus != "active" || resolved.MembershipStatus != "active" ||
		(resolved.Kind == string(teamauth.PrincipalAgent) && resolved.BindingStatus != "active") {
		return resolved, ErrPrincipalRevoked
	}
	return resolved, nil
}

func (s *Store) ResolveHumanIdentity(ctx context.Context, issuer, subject string) (ResolvedTeamPrincipal, error) {
	var principalID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT human_principal_id FROM team_human_identities WHERE identity_key = ?`,
		teamauth.HumanIdentityKey(issuer, subject)).Scan(&principalID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ResolvedTeamPrincipal{}, ErrPrincipalRevoked
		}
		return ResolvedTeamPrincipal{}, err
	}
	return s.ResolveTeamPrincipal(ctx, principalID)
}

func (s *Store) ResolveServiceIdentity(ctx context.Context, issuer, clientID string) (ResolvedTeamPrincipal, error) {
	var principalID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT service_principal_id FROM team_service_identities WHERE service_key = ?`,
		teamauth.ServiceIdentityKey(issuer, clientID)).Scan(&principalID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ResolvedTeamPrincipal{}, ErrPrincipalRevoked
		}
		return ResolvedTeamPrincipal{}, err
	}
	return s.ResolveTeamPrincipal(ctx, principalID)
}

func (s *Store) ResolveOAuthClient(ctx context.Context, issuer, clientID string) (ResolvedOAuthClient, error) {
	key := teamauth.OAuthClientKey(issuer, clientID)
	var resolved ResolvedOAuthClient
	resolved.OAuthClientKey = key
	if err := s.db.QueryRowContext(ctx, `
		SELECT kind, principal_id, COALESCE(binding_id, '')
		  FROM team_oauth_clients WHERE oauth_client_key = ?`, key).
		Scan(&resolved.Kind, &resolved.PrincipalID, &resolved.BindingID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ResolvedOAuthClient{}, ErrPrincipalRevoked
		}
		return ResolvedOAuthClient{}, err
	}
	principal, err := s.ResolveTeamPrincipal(ctx, resolved.PrincipalID)
	if err != nil {
		return resolved, err
	}
	if principal.Kind != resolved.Kind || (resolved.Kind == "agent" && principal.BindingID != resolved.BindingID) {
		return resolved, ErrInvalidTeamIdentityMutation
	}
	return resolved, nil
}

func (s *Store) BootstrapTeam(ctx context.Context, request BootstrapTeamRequest) (BootstrapResult, error) {
	request.TeamName = strings.TrimSpace(request.TeamName)
	if request.TeamName == "" {
		return BootstrapResult{}, fmt.Errorf("%w: empty team name", ErrInvalidTeamIdentityMutation)
	}
	if s.expectedBootstrapRoot == nil {
		return BootstrapResult{}, ErrBootstrapRootMismatch
	}
	if !s.expectedBootstrapRoot.Matches(request.PresentedRoot) {
		return BootstrapResult{}, ErrBootstrapRootMismatch
	}
	rootFingerprint, _ := s.expectedBootstrapRoot.Fingerprint()
	identityKey := teamauth.HumanIdentityKey(s.expectedBootstrapRoot.Issuer, s.expectedBootstrapRoot.Subject)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer tx.Rollback()
	var markers int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM team_stores`).Scan(&markers); err != nil {
		return BootstrapResult{}, err
	}
	if markers != 0 {
		return BootstrapResult{}, ErrBootstrapConsumed
	}
	var candidates int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM team_bootstrap_candidates WHERE singleton = 1`).Scan(&candidates); err != nil {
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

	storeID, err := newOpaqueID("store")
	if err != nil {
		return BootstrapResult{}, err
	}
	teamID, err := newOpaqueID("team")
	if err != nil {
		return BootstrapResult{}, err
	}
	ownerID, err := newOpaqueID("principal")
	if err != nil {
		return BootstrapResult{}, err
	}
	membershipID, err := newOpaqueID("membership")
	if err != nil {
		return BootstrapResult{}, err
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_stores(
			singleton, store_id, team_id, team_name, min_reader_version,
			min_writer_version, durability_profile, auth_epoch,
			bootstrap_root_fingerprint, bootstrap_consumed_at, created_at)
		VALUES (1, ?, ?, ?, ?, ?, 'wal-full-fk', 1, ?, ?, ?)`,
		storeID, teamID, request.TeamName, teamauth.SchemaVersion, teamauth.SchemaVersion,
		rootFingerprint, now, now); err != nil {
		if isConstraintError(err) {
			return BootstrapResult{}, ErrBootstrapConsumed
		}
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_principals(principal_id, store_id, kind, status, auth_epoch, created_at)
		VALUES (?, ?, 'human', 'active', 1, ?)`, ownerID, storeID, now); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_human_identities(identity_key, human_principal_id, created_at)
		VALUES (?, ?, ?)`, identityKey, ownerID, now); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_memberships(membership_id, team_id, principal_id, role, status, auth_epoch, created_at)
		VALUES (?, ?, ?, 'owner', 'active', 1, ?)`, membershipID, teamID, ownerID, now); err != nil {
		return BootstrapResult{}, err
	}
	if err := appendAudit(ctx, tx, storeID, 1, ownerID, "team.bootstrap", "allowed", "team", teamID, "bootstrap_root_matched", auditContext{
		TeamID: teamID, ClientKey: teamauth.OAuthClientKey(s.expectedBootstrapRoot.Issuer, s.expectedBootstrapRoot.AdminClientID),
	}); err != nil {
		return BootstrapResult{}, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM team_bootstrap_candidates WHERE singleton = 1`)
	if err != nil {
		return BootstrapResult{}, err
	}
	if consumed, err := result.RowsAffected(); err != nil || consumed != 1 {
		return BootstrapResult{}, ErrTeamBootstrapCandidateRequired
	}
	if err := tx.Commit(); err != nil {
		if isConstraintError(err) {
			return BootstrapResult{}, ErrBootstrapConsumed
		}
		return BootstrapResult{}, err
	}
	return BootstrapResult{
		TeamStoreInfo: TeamStoreInfo{
			StoreID: storeID, TeamID: teamID, TeamName: request.TeamName,
			MinReaderVersion: teamauth.SchemaVersion, MinWriterVersion: teamauth.SchemaVersion, AuthEpoch: 1,
		},
		OwnerPrincipalID:  ownerID,
		OwnerMembershipID: membershipID,
	}, nil
}

func (s *Store) AddTeamMember(ctx context.Context, request AddTeamMemberRequest) (TeamMember, error) {
	if !validExactIdentityValue(request.Issuer) || !validExactIdentityValue(request.Subject) ||
		(request.Role != "owner" && request.Role != "member" && request.Role != "reviewer") {
		return TeamMember{}, ErrInvalidTeamIdentityMutation
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamMember{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return TeamMember{}, err
	}
	if err := requireOwner(ctx, tx, info.TeamID, request.ActorPrincipalID); err != nil {
		return TeamMember{}, err
	}
	identityKey := teamauth.HumanIdentityKey(request.Issuer, request.Subject)
	var existing TeamMember
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT hi.human_principal_id, m.membership_id, m.role, m.auth_epoch, m.status
		  FROM team_human_identities hi
		  JOIN team_memberships m ON m.principal_id = hi.human_principal_id AND m.team_id = ?
		 WHERE hi.identity_key = ?`, info.TeamID, identityKey).
		Scan(&existing.PrincipalID, &existing.MembershipID, &existing.Role, &existing.AuthEpoch, &status)
	if err == nil {
		if status != "active" {
			return TeamMember{}, ErrPrincipalRevoked
		}
		if existing.Role != request.Role {
			return TeamMember{}, ErrInvalidTeamIdentityMutation
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TeamMember{}, err
	}

	principalID, err := newOpaqueID("principal")
	if err != nil {
		return TeamMember{}, err
	}
	membershipID, err := newOpaqueID("membership")
	if err != nil {
		return TeamMember{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_principals(principal_id, store_id, kind, status, auth_epoch, created_at)
		VALUES (?, ?, 'human', 'active', 1, ?)`, principalID, info.StoreID, now); err != nil {
		return TeamMember{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_human_identities(identity_key, human_principal_id, created_at)
		VALUES (?, ?, ?)`, identityKey, principalID, now); err != nil {
		return TeamMember{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_memberships(
			membership_id, team_id, principal_id, role, status, auth_epoch, created_at)
		VALUES (?, ?, ?, ?, 'active', 1, ?)`, membershipID, info.TeamID, principalID, request.Role, now); err != nil {
		return TeamMember{}, err
	}
	epoch, err := bumpTeamEpoch(ctx, tx)
	if err != nil {
		return TeamMember{}, err
	}
	if err := appendAudit(ctx, tx, info.StoreID, epoch, request.ActorPrincipalID, "membership.create", "allowed", "membership", membershipID, "human_identity_added"); err != nil {
		return TeamMember{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamMember{}, err
	}
	return TeamMember{PrincipalID: principalID, MembershipID: membershipID, Role: request.Role, AuthEpoch: 1}, nil
}

func (s *Store) RegisterAgentBinding(ctx context.Context, request RegisterAgentBindingRequest) (AgentBinding, error) {
	if !validExactIdentityValue(request.Issuer) || !validExactIdentityValue(request.Subject) || !validExactIdentityValue(request.ClientID) {
		return AgentBinding{}, ErrInvalidTeamIdentityMutation
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentBinding{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return AgentBinding{}, err
	}
	if err := requireOwner(ctx, tx, info.TeamID, request.ActorPrincipalID); err != nil {
		return AgentBinding{}, err
	}
	identityKey := teamauth.HumanIdentityKey(request.Issuer, request.Subject)
	var humanID string
	if err := tx.QueryRowContext(ctx, `
		SELECT hi.human_principal_id
		  FROM team_human_identities hi
		  JOIN team_principals hp ON hp.principal_id = hi.human_principal_id AND hp.status = 'active'
		  JOIN team_memberships m ON m.principal_id = hp.principal_id AND m.team_id = ? AND m.status = 'active'
		 WHERE hi.identity_key = ?`, info.TeamID, identityKey).Scan(&humanID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AgentBinding{}, ErrMembershipRequired
		}
		return AgentBinding{}, err
	}
	oauthClientKey := teamauth.OAuthClientKey(request.Issuer, request.ClientID)
	var registeredKind, registeredPrincipalID, registeredBindingID string
	err = tx.QueryRowContext(ctx, `
		SELECT kind, principal_id, COALESCE(binding_id, '')
		  FROM team_oauth_clients WHERE oauth_client_key = ?`, oauthClientKey).
		Scan(&registeredKind, &registeredPrincipalID, &registeredBindingID)
	if err == nil {
		if registeredKind != "agent" {
			return AgentBinding{}, ErrInvalidTeamIdentityMutation
		}
		var existing AgentBinding
		var status string
		if err := tx.QueryRowContext(ctx, `
			SELECT binding_id, human_principal_id, agent_principal_id, auth_epoch, status
			  FROM team_agent_bindings WHERE binding_id = ?`, registeredBindingID).
			Scan(&existing.BindingID, &existing.HumanPrincipalID, &existing.AgentPrincipalID, &existing.AuthEpoch, &status); err != nil {
			return AgentBinding{}, ErrInvalidTeamIdentityMutation
		}
		if existing.AgentPrincipalID != registeredPrincipalID || existing.HumanPrincipalID != humanID {
			return AgentBinding{}, ErrInvalidTeamIdentityMutation
		}
		if status != "active" {
			return AgentBinding{}, ErrPrincipalRevoked
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AgentBinding{}, err
	}
	bindingKey := teamauth.AgentBindingKey(request.Issuer, request.Subject, request.ClientID)
	var existing AgentBinding
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT binding_id, human_principal_id, agent_principal_id, auth_epoch, status
		  FROM team_agent_bindings WHERE binding_key = ?`, bindingKey).
		Scan(&existing.BindingID, &existing.HumanPrincipalID, &existing.AgentPrincipalID, &existing.AuthEpoch, &status)
	if err == nil {
		if existing.HumanPrincipalID != humanID {
			return AgentBinding{}, ErrInvalidTeamIdentityMutation
		}
		if status != "active" {
			return AgentBinding{}, ErrPrincipalRevoked
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AgentBinding{}, err
	}

	agentID, err := newOpaqueID("principal")
	if err != nil {
		return AgentBinding{}, err
	}
	bindingID, err := newOpaqueID("binding")
	if err != nil {
		return AgentBinding{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_principals(principal_id, store_id, kind, status, auth_epoch, created_at)
		VALUES (?, ?, 'agent', 'active', 1, ?)`, agentID, info.StoreID, now); err != nil {
		return AgentBinding{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_agent_bindings(
			binding_id, team_id, human_principal_id, agent_principal_id,
			binding_key, client_key, status, auth_epoch, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'active', 1, ?)`, bindingID, info.TeamID, humanID, agentID,
		bindingKey, oauthClientKey, now); err != nil {
		return AgentBinding{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_oauth_clients(
			oauth_client_key, team_id, kind, principal_id, binding_id, created_at)
		VALUES (?, ?, 'agent', ?, ?, ?)`, oauthClientKey, info.TeamID, agentID, bindingID, now); err != nil {
		return AgentBinding{}, err
	}
	epoch, err := bumpTeamEpoch(ctx, tx)
	if err != nil {
		return AgentBinding{}, err
	}
	if err := appendAudit(ctx, tx, info.StoreID, epoch, request.ActorPrincipalID, "agent_binding.create", "allowed", "agent_binding", bindingID, "member_client_bound", auditContext{
		TeamID: info.TeamID, ClientKey: oauthClientKey,
	}); err != nil {
		return AgentBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentBinding{}, err
	}
	return AgentBinding{BindingID: bindingID, HumanPrincipalID: humanID, AgentPrincipalID: agentID, AuthEpoch: 1}, nil
}

func (s *Store) ResolveAgentBinding(ctx context.Context, issuer, subject, clientID string) (AgentBinding, error) {
	bindingKey := teamauth.AgentBindingKey(issuer, subject, clientID)
	var binding AgentBinding
	err := s.db.QueryRowContext(ctx, `
		SELECT b.binding_id, b.human_principal_id, b.agent_principal_id, b.auth_epoch
		  FROM team_agent_bindings b
		  JOIN team_principals ap ON ap.principal_id = b.agent_principal_id AND ap.status = 'active'
		  JOIN team_principals hp ON hp.principal_id = b.human_principal_id AND hp.status = 'active'
		  JOIN team_memberships m ON m.principal_id = hp.principal_id AND m.team_id = b.team_id AND m.status = 'active'
		 WHERE b.binding_key = ? AND b.status = 'active'`, bindingKey).
		Scan(&binding.BindingID, &binding.HumanPrincipalID, &binding.AgentPrincipalID, &binding.AuthEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentBinding{}, ErrPrincipalRevoked
	}
	return binding, err
}

func (s *Store) RevokeAgentBinding(ctx context.Context, actorPrincipalID, bindingID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return err
	}
	if err := requireOwner(ctx, tx, info.TeamID, actorPrincipalID); err != nil {
		return err
	}
	var agentID, clientKey string
	if err := tx.QueryRowContext(ctx, `
		SELECT agent_principal_id, client_key FROM team_agent_bindings
		 WHERE binding_id = ? AND team_id = ? AND status = 'active'`, bindingID, info.TeamID).Scan(&agentID, &clientKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPrincipalRevoked
		}
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
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
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_project_grants SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE principal_id = ? AND status = 'active'`, now, agentID); err != nil {
		return err
	}
	epoch, err := bumpTeamEpoch(ctx, tx)
	if err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, info.StoreID, epoch, actorPrincipalID, "agent_binding.revoke", "allowed", "agent_binding", bindingID, "binding_revoked", auditContext{
		TeamID: info.TeamID, ClientKey: clientKey,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeMembership(ctx context.Context, actorPrincipalID, targetHumanPrincipalID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return err
	}
	if err := requireOwner(ctx, tx, info.TeamID, actorPrincipalID); err != nil {
		return err
	}
	var targetKind string
	if err := tx.QueryRowContext(ctx, `
		SELECT kind FROM team_principals WHERE principal_id = ? AND store_id = ?`,
		targetHumanPrincipalID, info.StoreID).Scan(&targetKind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMembershipRequired
		}
		return err
	}
	if targetKind != "human" {
		return ErrHumanPrincipalRequired
	}
	var membershipID, role string
	if err := tx.QueryRowContext(ctx, `
		SELECT membership_id, role FROM team_memberships
		 WHERE team_id = ? AND principal_id = ? AND status = 'active'`, info.TeamID, targetHumanPrincipalID).
		Scan(&membershipID, &role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMembershipRequired
		}
		return err
	}
	if role == "owner" {
		var ownerCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM team_memberships
			 WHERE team_id = ? AND role = 'owner' AND status = 'active'`, info.TeamID).Scan(&ownerCount); err != nil {
			return err
		}
		if ownerCount <= 1 {
			return ErrLastOwner
		}
	}
	var ownedProjects int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM team_projects
		 WHERE team_id = ? AND owner_principal_id = ?`, info.TeamID, targetHumanPrincipalID).Scan(&ownedProjects); err != nil {
		return err
	}
	if ownedProjects != 0 {
		return ErrProjectOwnershipTransferRequired
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_project_grants SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE status = 'active' AND (
			principal_id = ? OR principal_id IN (
				SELECT agent_principal_id FROM team_agent_bindings
				 WHERE human_principal_id = ? AND status = 'active'
			)
		)`, now, targetHumanPrincipalID, targetHumanPrincipalID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_memberships SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE membership_id = ?`, now, membershipID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_principals SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE principal_id = ? OR principal_id IN (
			SELECT agent_principal_id FROM team_agent_bindings WHERE human_principal_id = ? AND status = 'active'
		)`, now, targetHumanPrincipalID, targetHumanPrincipalID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_agent_bindings SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE human_principal_id = ? AND status = 'active'`, now, targetHumanPrincipalID); err != nil {
		return err
	}
	epoch, err := bumpTeamEpoch(ctx, tx)
	if err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, info.StoreID, epoch, actorPrincipalID, "membership.revoke", "allowed", "membership", membershipID, "membership_and_bindings_revoked"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RegisterServicePrincipal(ctx context.Context, request RegisterServicePrincipalRequest) (ServicePrincipal, error) {
	if !validExactIdentityValue(request.Issuer) || !validExactIdentityValue(request.ClientID) {
		return ServicePrincipal{}, ErrInvalidTeamIdentityMutation
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ServicePrincipal{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return ServicePrincipal{}, err
	}
	if err := requireOwner(ctx, tx, info.TeamID, request.ActorPrincipalID); err != nil {
		return ServicePrincipal{}, err
	}
	oauthClientKey := teamauth.OAuthClientKey(request.Issuer, request.ClientID)
	var registeredKind, registeredPrincipalID string
	err = tx.QueryRowContext(ctx, `
		SELECT kind, principal_id FROM team_oauth_clients WHERE oauth_client_key = ?`, oauthClientKey).
		Scan(&registeredKind, &registeredPrincipalID)
	if err == nil {
		if registeredKind != "service" {
			return ServicePrincipal{}, ErrInvalidTeamIdentityMutation
		}
		var existing ServicePrincipal
		var status string
		if err := tx.QueryRowContext(ctx, `
			SELECT m.membership_id, p.auth_epoch, p.status
			  FROM team_principals p
			  JOIN team_memberships m ON m.principal_id = p.principal_id AND m.team_id = ?
			 WHERE p.principal_id = ? AND p.kind = 'service'`, info.TeamID, registeredPrincipalID).
			Scan(&existing.MembershipID, &existing.AuthEpoch, &status); err != nil {
			return ServicePrincipal{}, ErrInvalidTeamIdentityMutation
		}
		existing.PrincipalID = registeredPrincipalID
		if status != "active" {
			return ServicePrincipal{}, ErrPrincipalRevoked
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ServicePrincipal{}, err
	}
	serviceKey := teamauth.ServiceIdentityKey(request.Issuer, request.ClientID)
	var existing ServicePrincipal
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT si.service_principal_id, m.membership_id, p.auth_epoch, p.status
		  FROM team_service_identities si
		  JOIN team_principals p ON p.principal_id = si.service_principal_id
		  JOIN team_memberships m ON m.principal_id = p.principal_id AND m.team_id = ?
		 WHERE si.service_key = ?`, info.TeamID, serviceKey).
		Scan(&existing.PrincipalID, &existing.MembershipID, &existing.AuthEpoch, &status)
	if err == nil {
		if status != "active" {
			return ServicePrincipal{}, ErrPrincipalRevoked
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ServicePrincipal{}, err
	}
	principalID, err := newOpaqueID("principal")
	if err != nil {
		return ServicePrincipal{}, err
	}
	membershipID, err := newOpaqueID("membership")
	if err != nil {
		return ServicePrincipal{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_principals(principal_id, store_id, kind, status, auth_epoch, created_at)
		VALUES (?, ?, 'service', 'active', 1, ?)`, principalID, info.StoreID, now); err != nil {
		return ServicePrincipal{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_service_identities(service_key, client_key, service_principal_id, created_at)
		VALUES (?, ?, ?, ?)`, serviceKey, oauthClientKey, principalID, now); err != nil {
		return ServicePrincipal{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_oauth_clients(
			oauth_client_key, team_id, kind, principal_id, binding_id, created_at)
		VALUES (?, ?, 'service', ?, NULL, ?)`, oauthClientKey, info.TeamID, principalID, now); err != nil {
		return ServicePrincipal{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_memberships(membership_id, team_id, principal_id, role, status, auth_epoch, created_at)
		VALUES (?, ?, ?, 'member', 'active', 1, ?)`, membershipID, info.TeamID, principalID, now); err != nil {
		return ServicePrincipal{}, err
	}
	epoch, err := bumpTeamEpoch(ctx, tx)
	if err != nil {
		return ServicePrincipal{}, err
	}
	if err := appendAudit(ctx, tx, info.StoreID, epoch, request.ActorPrincipalID, "service_principal.create", "allowed", "service_principal", principalID, "service_identity_registered", auditContext{
		TeamID: info.TeamID, ClientKey: oauthClientKey,
	}); err != nil {
		return ServicePrincipal{}, err
	}
	if err := tx.Commit(); err != nil {
		return ServicePrincipal{}, err
	}
	return ServicePrincipal{PrincipalID: principalID, MembershipID: membershipID, AuthEpoch: 1}, nil
}

func (s *Store) RevokeServicePrincipal(ctx context.Context, actorPrincipalID, servicePrincipalID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return err
	}
	if err := requireOwner(ctx, tx, info.TeamID, actorPrincipalID); err != nil {
		return err
	}
	var membershipID, clientKey string
	if err := tx.QueryRowContext(ctx, `
		SELECT m.membership_id, si.client_key
		  FROM team_principals p
		  JOIN team_service_identities si ON si.service_principal_id = p.principal_id
		  JOIN team_memberships m ON m.principal_id = p.principal_id AND m.team_id = ?
		 WHERE p.principal_id = ? AND p.kind = 'service'
		   AND p.status = 'active' AND m.status = 'active'`, info.TeamID, servicePrincipalID).
		Scan(&membershipID, &clientKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPrincipalRevoked
		}
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_principals SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE principal_id = ?`, now, servicePrincipalID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_memberships SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE membership_id = ?`, now, membershipID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_project_grants SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE principal_id = ? AND status = 'active'`, now, servicePrincipalID); err != nil {
		return err
	}
	epoch, err := bumpTeamEpoch(ctx, tx)
	if err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, info.StoreID, epoch, actorPrincipalID, "service_principal.revoke", "allowed", "service_principal", servicePrincipalID, "service_and_grants_revoked", auditContext{
		TeamID: info.TeamID, ClientKey: clientKey,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateTeamProject(ctx context.Context, actorPrincipalID, name string) (TeamProject, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return TeamProject{}, ErrInvalidTeamIdentityMutation
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamProject{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return TeamProject{}, err
	}
	if err := requireOwner(ctx, tx, info.TeamID, actorPrincipalID); err != nil {
		return TeamProject{}, err
	}
	var ownerKind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM team_principals WHERE principal_id = ?`, actorPrincipalID).Scan(&ownerKind); err != nil {
		return TeamProject{}, err
	}
	if ownerKind != "human" {
		return TeamProject{}, ErrHumanPrincipalRequired
	}
	projectID, err := newOpaqueID("project")
	if err != nil {
		return TeamProject{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_projects(
			project_id, team_id, name, owner_principal_id, created_by_principal_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, info.TeamID, name, actorPrincipalID, actorPrincipalID, now); err != nil {
		return TeamProject{}, err
	}
	epoch, err := bumpTeamEpoch(ctx, tx)
	if err != nil {
		return TeamProject{}, err
	}
	if err := appendAudit(ctx, tx, info.StoreID, epoch, actorPrincipalID, "project.create", "allowed", "project", projectID, "project_created", auditContext{
		TeamID: info.TeamID, ProjectID: projectID,
	}); err != nil {
		return TeamProject{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamProject{}, err
	}
	return TeamProject{
		ProjectID: projectID, TeamID: info.TeamID, Name: name,
		OwnerPrincipalID: actorPrincipalID, CreatedByPrincipalID: actorPrincipalID,
	}, nil
}

func (s *Store) GrantProjectAccess(ctx context.Context, request GrantProjectAccessRequest) (ProjectGrant, error) {
	if request.AccessLevel != "read" && request.AccessLevel != "write" && request.AccessLevel != "admin" {
		return ProjectGrant{}, ErrInvalidTeamIdentityMutation
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectGrant{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return ProjectGrant{}, err
	}
	if err := requireOwner(ctx, tx, info.TeamID, request.ActorPrincipalID); err != nil {
		return ProjectGrant{}, err
	}
	var projectTeamID string
	if err := tx.QueryRowContext(ctx, `SELECT team_id FROM team_projects WHERE project_id = ?`, request.ProjectID).Scan(&projectTeamID); err != nil || projectTeamID != info.TeamID {
		return ProjectGrant{}, ErrMembershipRequired
	}
	if err := requireGrantablePrincipal(ctx, tx, info.TeamID, request.TargetPrincipalID); err != nil {
		return ProjectGrant{}, err
	}
	var existing ProjectGrant
	var existingStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT grant_id, project_id, principal_id, access_level, auth_epoch, status
		  FROM team_project_grants WHERE project_id = ? AND principal_id = ?`,
		request.ProjectID, request.TargetPrincipalID).
		Scan(&existing.GrantID, &existing.ProjectID, &existing.PrincipalID, &existing.AccessLevel, &existing.AuthEpoch, &existingStatus)
	if err == nil {
		if existingStatus != "active" {
			return ProjectGrant{}, ErrProjectGrantRequired
		}
		if existing.AccessLevel != request.AccessLevel {
			return ProjectGrant{}, ErrInvalidTeamIdentityMutation
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProjectGrant{}, err
	}
	grantID, err := newOpaqueID("grant")
	if err != nil {
		return ProjectGrant{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_project_grants(
			grant_id, project_id, principal_id, access_level, status, auth_epoch, created_at)
		VALUES (?, ?, ?, ?, 'active', 1, ?)`, grantID, request.ProjectID, request.TargetPrincipalID, request.AccessLevel, now); err != nil {
		return ProjectGrant{}, err
	}
	epoch, err := bumpTeamEpoch(ctx, tx)
	if err != nil {
		return ProjectGrant{}, err
	}
	if err := appendAudit(ctx, tx, info.StoreID, epoch, request.ActorPrincipalID, "project_grant.create", "allowed", "project_grant", grantID, "active_principal_granted", auditContext{
		TeamID: info.TeamID, ProjectID: request.ProjectID,
	}); err != nil {
		return ProjectGrant{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectGrant{}, err
	}
	return ProjectGrant{GrantID: grantID, ProjectID: request.ProjectID, PrincipalID: request.TargetPrincipalID, AccessLevel: request.AccessLevel, AuthEpoch: 1}, nil
}

func (s *Store) ResolveProjectGrant(ctx context.Context, projectID, principalID string) (ProjectGrant, error) {
	var grant ProjectGrant
	var status string
	err := s.db.QueryRowContext(ctx, `
		SELECT grant_id, project_id, principal_id, access_level, auth_epoch, status
		  FROM team_project_grants
		 WHERE project_id = ? AND principal_id = ?`, projectID, principalID).
		Scan(&grant.GrantID, &grant.ProjectID, &grant.PrincipalID, &grant.AccessLevel, &grant.AuthEpoch, &status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "active") {
		return grant, ErrProjectGrantRequired
	}
	if err != nil {
		return grant, err
	}
	if _, err := s.ResolveTeamPrincipal(ctx, principalID); err != nil {
		return grant, ErrProjectGrantRequired
	}
	return grant, nil
}

func (s *Store) RevokeProjectGrant(ctx context.Context, actorPrincipalID, grantID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return err
	}
	if err := requireOwner(ctx, tx, info.TeamID, actorPrincipalID); err != nil {
		return err
	}
	var projectID string
	if err := tx.QueryRowContext(ctx, `
		SELECT g.project_id
		  FROM team_project_grants g
		  JOIN team_projects p ON p.project_id = g.project_id
		 WHERE g.grant_id = ? AND g.status = 'active' AND p.team_id = ?`, grantID, info.TeamID).
		Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProjectGrantRequired
		}
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_project_grants SET status = 'revoked', revoked_at = ?, auth_epoch = auth_epoch + 1
		 WHERE grant_id = ?`, now, grantID); err != nil {
		return err
	}
	epoch, err := bumpTeamEpoch(ctx, tx)
	if err != nil {
		return err
	}
	if err := appendAudit(ctx, tx, info.StoreID, epoch, actorPrincipalID, "project_grant.revoke", "allowed", "project_grant", grantID, "grant_revoked", auditContext{
		TeamID: info.TeamID, ProjectID: projectID,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// ConsumeAssertionID atomically records an assertion's (kid,jti) pair. The
// persisted values are domain-separated hashes, and the unique key makes
// concurrent and post-restart replay fail closed.
func (s *Store) ConsumeAssertionID(ctx context.Context, kid, jti string, expiresAt time.Time) error {
	if strings.TrimSpace(kid) == "" || strings.TrimSpace(jti) == "" {
		return ErrInvalidTeamIdentityMutation
	}
	now := s.clock().UTC()
	if !expiresAt.After(now) {
		return ErrAssertionExpired
	}
	info, err := readTeamStoreInfo(ctx, s.db)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO team_assertion_replay(store_id, kid, jti, expires_at, consumed_at)
		VALUES (?, ?, ?, ?, ?)`, info.StoreID, assertionIdentifier("kid", kid), assertionIdentifier("jti", jti),
		expiresAt.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	if isConstraintError(err) {
		return ErrAssertionReplay
	}
	return err
}

func (s *Store) PruneExpiredAssertionIDs(ctx context.Context) (int64, error) {
	info, err := readTeamStoreInfo(ctx, s.db)
	if err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM team_assertion_replay WHERE store_id = ? AND expires_at <= ?`,
		info.StoreID, s.clock().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readTeamStoreInfo(ctx context.Context, q queryer) (TeamStoreInfo, error) {
	var info TeamStoreInfo
	var durability string
	err := q.QueryRowContext(ctx, `
		SELECT store_id, team_id, team_name, min_reader_version, min_writer_version, auth_epoch, durability_profile
		  FROM team_stores WHERE singleton = 1`).
		Scan(&info.StoreID, &info.TeamID, &info.TeamName, &info.MinReaderVersion, &info.MinWriterVersion, &info.AuthEpoch, &durability)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamStoreInfo{}, ErrTeamStoreUninitialized
	}
	if err != nil {
		return TeamStoreInfo{}, err
	}
	if durability != "wal-full-fk" {
		return TeamStoreInfo{}, fmt.Errorf("invalid team durability profile %q", durability)
	}
	return info, nil
}

func verifyTeamPragmas(db *sql.DB) error {
	var journal string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		return err
	}
	var synchronous, foreignKeys int
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		return err
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if strings.ToLower(journal) != "wal" || synchronous != 2 || foreignKeys != 1 {
		return fmt.Errorf("team durability mismatch: journal=%s synchronous=%d foreign_keys=%d", journal, synchronous, foreignKeys)
	}
	return nil
}

type legacyCounter interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func countLegacyRows(ctx context.Context, q legacyCounter) (int64, error) {
	// Before the canonical object registry exists, explicit bootstrap only
	// accepts a database with no pre-team application state at all. Discovering
	// the catalog avoids silently missing a newly added content-bearing table;
	// FTS virtual/shadow tables are excluded because their config rows are not
	// user domain data. U4 replaces this pre-registry gate with scope-registry
	// checks once legitimate team content can exist.
	rows, err := q.QueryContext(ctx, `
		SELECT name, COALESCE(sql, '')
		  FROM sqlite_master
		 WHERE type = 'table'
		 ORDER BY name`)
	if err != nil {
		return 0, fmt.Errorf("inspect legacy table catalog: %w", err)
	}
	type catalogTable struct {
		name string
		sql  string
	}
	var catalog []catalogTable
	for rows.Next() {
		var table catalogTable
		if err := rows.Scan(&table.name, &table.sql); err != nil {
			rows.Close()
			return 0, err
		}
		catalog = append(catalog, table)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	ftsRoots := make(map[string]struct{})
	for _, table := range catalog {
		sqlText := strings.ToLower(table.sql)
		if strings.Contains(sqlText, "create virtual table") && strings.Contains(sqlText, "using fts5") {
			ftsRoots[table.name] = struct{}{}
		}
	}
	allowed := map[string]struct{}{
		"schema_meta": {}, "schema_migration_manifest": {},
		"team_bootstrap_candidates": {}, "team_stores": {}, "team_principals": {},
		"team_human_identities": {}, "team_service_identities": {}, "team_memberships": {},
		"team_projects": {}, "team_agent_bindings": {}, "team_oauth_clients": {},
		"team_project_grants": {}, "team_audit_events": {}, "team_security_events": {},
		"team_assertion_replay": {}, "team_policy_metadata": {}, "team_object_registry": {},
		"team_object_storage_map": {}, "team_memory_capsules": {}, "team_memory_events": {},
		"team_memory_embeddings": {}, "team_graph_delta_inputs": {},
		"team_semantic_projection_intents": {}, "team_object_contributions": {},
		"team_semantic_materializations": {}, "team_graph_materializations": {},
		"team_assertion_materializations": {}, "team_continuity_materializations": {},
		"team_semantic_embeddings":   {},
		"team_service_object_grants": {}, "team_writer_leases": {},
		"team_idempotency_records": {}, "team_projection_jobs": {},
		"team_projection_outputs": {}, "team_audit_event_order": {},
	}
	for _, table := range catalog {
		if strings.HasPrefix(table.name, "sqlite_") {
			continue
		}
		if _, ok := allowed[table.name]; ok {
			continue
		}
		if isFTS5CatalogTable(table.name, ftsRoots) {
			continue
		}
		var exists int
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+quoteSQLiteIdentifier(table.name)+` LIMIT 1)`).Scan(&exists); err != nil {
			return 0, fmt.Errorf("inspect legacy table %s: %w", table.name, err)
		}
		if exists != 0 {
			return 1, nil
		}
	}
	return 0, nil
}

func isFTS5CatalogTable(name string, roots map[string]struct{}) bool {
	if _, ok := roots[name]; ok {
		return true
	}
	for root := range roots {
		for _, suffix := range []string{"_data", "_idx", "_content", "_docsize", "_config"} {
			if name == root+suffix {
				return true
			}
		}
	}
	return false
}

func quoteSQLiteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func requireMembership(ctx context.Context, tx *sql.Tx, teamID, principalID string) error {
	var count int
	err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		  FROM team_memberships m
		  JOIN team_principals p ON p.principal_id = m.principal_id
		 WHERE m.team_id = ? AND m.principal_id = ?
		   AND m.status = 'active' AND p.status = 'active'`, teamID, principalID).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrMembershipRequired
	}
	return nil
}

func requireOwner(ctx context.Context, tx *sql.Tx, teamID, principalID string) error {
	var count int
	err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		  FROM team_memberships m
		  JOIN team_principals p ON p.principal_id = m.principal_id
		 WHERE m.team_id = ? AND m.principal_id = ? AND m.role = 'owner'
		   AND m.status = 'active' AND p.status = 'active'`, teamID, principalID).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrMembershipRequired
	}
	return nil
}

func requireGrantablePrincipal(ctx context.Context, tx *sql.Tx, teamID, principalID string) error {
	var kind, status string
	if err := tx.QueryRowContext(ctx, `SELECT kind, status FROM team_principals WHERE principal_id = ?`, principalID).Scan(&kind, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMembershipRequired
		}
		return err
	}
	if status != "active" {
		return ErrPrincipalRevoked
	}
	switch kind {
	case "human":
		return requireMembership(ctx, tx, teamID, principalID)
	case "agent":
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM team_agent_bindings b
			JOIN team_memberships m ON m.principal_id = b.human_principal_id
			WHERE b.team_id = ? AND b.agent_principal_id = ? AND b.status = 'active' AND m.status = 'active'`,
			teamID, principalID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return ErrMembershipRequired
		}
		return nil
	case "service":
		return requireMembership(ctx, tx, teamID, principalID)
	default:
		return ErrMembershipRequired
	}
}

func bumpTeamEpoch(ctx context.Context, tx *sql.Tx) (int64, error) {
	var epoch int64
	if err := tx.QueryRowContext(ctx, `
		UPDATE team_stores SET auth_epoch = auth_epoch + 1 WHERE singleton = 1
		RETURNING auth_epoch`).Scan(&epoch); err != nil {
		return 0, err
	}
	return epoch, nil
}

type auditContext struct {
	TeamID    string
	ProjectID string
	ClientKey string
}

func appendAudit(ctx context.Context, tx *sql.Tx, storeID string, epoch int64, actor, action, outcome, targetKind, targetID, reason string, contexts ...auditContext) error {
	eventID, err := newOpaqueID("audit")
	if err != nil {
		return err
	}
	var details auditContext
	if len(contexts) != 0 {
		details = contexts[0]
	}
	if details.TeamID == "" {
		if err := tx.QueryRowContext(ctx, `SELECT team_id FROM team_stores WHERE store_id = ?`, storeID).Scan(&details.TeamID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_audit_events(
			event_id, store_id, occurred_at, action, outcome, actor_principal_id,
			client_key, team_id, project_id, target_kind, target_id,
			policy_version, mode, auth_epoch, reason_code, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, 1, 'team-remote', ?, ?, '{}')`,
		eventID, storeID, time.Now().UTC().Format(time.RFC3339Nano), action, outcome,
		actor, details.ClientKey, details.TeamID, details.ProjectID, targetKind, targetID, epoch, reason)
	return err
}

func newOpaqueID(prefix string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf), nil
}

func isConstraintError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "constraint")
}

func validExactIdentityValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func assertionIdentifier(kind, value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("pulse-assertion-"+kind+"-v1\x00"+value)))
}
