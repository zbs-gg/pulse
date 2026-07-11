package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

var (
	ErrConcealedNotFound       = errors.New("concealed_not_found")
	ErrTeamPolicyDenied        = errors.New("team policy denied")
	ErrTeamPolicyNotReady      = errors.New("team policy metadata is not ready")
	ErrTeamPolicyEpochChanged  = errors.New("team policy epoch changed")
	ErrTeamWriterLeaseHeld     = errors.New("team writer lease is held")
	ErrTeamWriterLeaseMismatch = errors.New("team writer lease does not match the active daemon")
)

type CandidateFilterRequest struct {
	PrincipalID    string
	Capabilities   []teamauth.Capability
	Context        teamauth.ActiveContext
	PrivacyCeiling string
	Retention      string
}

// AuthorizedCandidateFilter is a compiled, fixed scope predicate. It stores
// principal and scope-partition facts, never a materialized list of object IDs.
// Callers apply SQLPredicate before lexical/vector search or graph traversal,
// then recheck PolicyEpoch immediately before response or side effects.
type AuthorizedCandidateFilter struct {
	teamID           string
	principalID      string
	humanPrincipalID string
	bindingID        string
	kind             teamauth.PrincipalKind
	context          teamauth.ActiveContext
	privacyCeiling   string
	retention        string
	epoch            teamauth.EpochSnapshot
}

func (filter AuthorizedCandidateFilter) PolicyEpoch() teamauth.EpochSnapshot {
	return filter.epoch
}

var sqlAliasPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (filter AuthorizedCandidateFilter) SQLPredicate(alias string) (string, []any, error) {
	if !sqlAliasPattern.MatchString(alias) {
		return "", nil, fmt.Errorf("invalid SQL alias %q", alias)
	}
	column := func(name string) string { return alias + "." + name }
	clauses := []string{
		column("team_id") + " = ?",
		column("lifecycle") + " = 'active'",
		`EXISTS (
			SELECT 1 FROM team_policy_metadata policy
			 WHERE policy.team_id = ` + column("team_id") + `
			   AND policy.policy_version = ?
			   AND policy.schema_version = ?
			   AND policy.policy_epoch = ?
			   AND policy.global_epoch = ?
		)`,
	}
	args := []any{
		filter.teamID, teamauth.PolicyVersion, teamauth.SchemaVersion,
		filter.epoch.Policy, filter.epoch.Global,
	}

	identitySQL, identityArgs := filter.identityPredicate()
	clauses = append(clauses, identitySQL)
	args = append(args, identityArgs...)

	scopeSQL, scopeArgs := filter.scopePredicate(alias)
	clauses = append(clauses, scopeSQL)
	args = append(args, scopeArgs...)

	if filter.context.ProjectID != "" {
		clauses = append(clauses, "("+column("scope_type")+" <> 'project' OR "+column("scope_id")+" = ?)")
		args = append(args, filter.context.ProjectID)
	}
	if filter.context.RepoID != "" {
		clauses = append(clauses, "("+column("scope_type")+" <> 'repo' OR "+column("scope_id")+" = ?)")
		args = append(args, filter.context.RepoID)
	}
	if filter.context.AgentID != "" {
		clauses = append(clauses, "("+column("scope_type")+" <> 'agent' OR "+column("scope_id")+" = ?)")
		args = append(args, filter.context.AgentID)
	}
	if filter.context.SessionID != "" {
		clauses = append(clauses, "("+column("scope_type")+" <> 'session' OR "+column("scope_id")+" = ?)")
		args = append(args, filter.context.SessionID)
	}
	switch filter.privacyCeiling {
	case "normal":
		clauses = append(clauses, column("privacy_tier")+" = 'normal'")
	case "sensitive":
		clauses = append(clauses, column("privacy_tier")+" IN ('normal', 'sensitive')")
	case "private":
		// The broadest handling ceiling adds no grant. Scope policy above still
		// controls every candidate.
	}
	if filter.retention != "" {
		clauses = append(clauses, column("retention")+" = ?")
		args = append(args, filter.retention)
	}
	return strings.Join(clauses, " AND "), args, nil
}

func (filter AuthorizedCandidateFilter) identityPredicate() (string, []any) {
	if filter.kind == teamauth.PrincipalAgent {
		return `EXISTS (
			SELECT 1
			  FROM team_principals principal
			  JOIN team_agent_bindings binding
			    ON binding.agent_principal_id = principal.principal_id
			   AND binding.binding_id = ?
			   AND binding.status = 'active'
			   AND binding.auth_epoch = ?
			  JOIN team_principals human
			    ON human.principal_id = binding.human_principal_id
			   AND human.status = 'active'
			  JOIN team_memberships membership
			    ON membership.principal_id = human.principal_id
			   AND membership.team_id = binding.team_id
			   AND membership.status = 'active'
			   AND membership.auth_epoch = ?
			 WHERE principal.principal_id = ?
			   AND principal.status = 'active'
			   AND principal.auth_epoch = ?
			   AND binding.team_id = ?
			   AND binding.human_principal_id = ?
		)`, []any{
				filter.bindingID, filter.epoch.Binding, filter.epoch.Membership,
				filter.principalID, filter.epoch.Principal, filter.teamID, filter.humanPrincipalID,
			}
	}
	return `EXISTS (
		SELECT 1
		  FROM team_principals principal
		  JOIN team_memberships membership
		    ON membership.principal_id = principal.principal_id
		   AND membership.team_id = ?
		   AND membership.status = 'active'
		   AND membership.auth_epoch = ?
		 WHERE principal.principal_id = ?
		   AND principal.status = 'active'
		   AND principal.auth_epoch = ?
	)`, []any{filter.teamID, filter.epoch.Membership, filter.principalID, filter.epoch.Principal}
}

func (filter AuthorizedCandidateFilter) scopePredicate(alias string) (string, []any) {
	typeColumn := alias + ".scope_type"
	idColumn := alias + ".scope_id"
	ownerColumn := alias + ".owner_principal_id"
	kindColumn := alias + ".object_kind"
	if filter.kind == teamauth.PrincipalService {
		return `EXISTS (
			SELECT 1 FROM team_service_object_grants service_grant
			 WHERE service_grant.team_id = ` + alias + `.team_id
			   AND service_grant.service_principal_id = ?
			   AND service_grant.action = 'read'
			   AND service_grant.status = 'active'
			   AND service_grant.scope_type = ` + typeColumn + `
			   AND service_grant.scope_id = ` + idColumn + `
			   AND (service_grant.object_kind = '*' OR service_grant.object_kind = ` + kindColumn + `)
		) AND (
			` + typeColumn + ` <> 'project' OR EXISTS (
				SELECT 1 FROM team_project_grants project_grant
				 WHERE project_grant.project_id = ` + idColumn + `
				   AND project_grant.principal_id = ?
				   AND project_grant.status = 'active'
				   AND project_grant.access_level IN ('read', 'write', 'admin')
			)
		)`, []any{filter.principalID, filter.principalID}
	}

	projectAccess := `EXISTS (
		SELECT 1 FROM team_project_grants project_grant
		 WHERE project_grant.project_id = ` + idColumn + `
		   AND project_grant.principal_id = ?
		   AND project_grant.status = 'active'
		   AND project_grant.access_level IN ('read', 'write', 'admin')
	)`
	projectArgs := []any{filter.principalID}
	if filter.kind == teamauth.PrincipalHuman {
		projectAccess = `(
			EXISTS (
				SELECT 1 FROM team_projects project
				 WHERE project.project_id = ` + idColumn + `
				   AND project.team_id = ` + alias + `.team_id
				   AND project.owner_principal_id = ?
			) OR ` + projectAccess + `
		)`
		projectArgs = []any{filter.humanPrincipalID, filter.principalID}
	}
	return `(
		(` + typeColumn + ` = 'personal' AND ` + ownerColumn + ` = ?)
		OR ` + typeColumn + ` = 'team'
		OR (` + typeColumn + ` = 'project' AND ` + projectAccess + `)
		OR (` + typeColumn + ` IN ('repo', 'agent', 'session') AND ` + ownerColumn + ` = ?)
	)`, append([]any{filter.humanPrincipalID}, append(projectArgs, filter.humanPrincipalID)...)
}

type TeamObject struct {
	ObjectID          string
	StoreID           string
	TeamID            string
	ObjectKind        string
	Scope             teamauth.CanonicalScope
	AuthorPrincipalID string
}

func (s *Store) BuildAuthorizedCandidateFilter(ctx context.Context, request CandidateFilterRequest) (AuthorizedCandidateFilter, error) {
	if request.Context.TeamID == "" || request.PrincipalID == "" {
		return AuthorizedCandidateFilter{}, fmt.Errorf("%w: missing team context or principal", ErrTeamPolicyDenied)
	}
	if request.PrivacyCeiling == "" {
		request.PrivacyCeiling = "normal"
	}
	if !validPrivacyTier(request.PrivacyCeiling) {
		return AuthorizedCandidateFilter{}, fmt.Errorf("%w: invalid privacy ceiling", ErrTeamPolicyDenied)
	}
	if request.Retention != "" && !validRetention(request.Retention) {
		return AuthorizedCandidateFilter{}, fmt.Errorf("%w: invalid retention", ErrTeamPolicyDenied)
	}

	principal, principalErr := s.ResolveTeamPrincipal(ctx, request.PrincipalID)
	if principalErr != nil && !errors.Is(principalErr, ErrPrincipalRevoked) {
		return AuthorizedCandidateFilter{}, principalErr
	}
	policy, err := readTeamPolicyState(ctx, s.db)
	if err != nil {
		return AuthorizedCandidateFilter{}, err
	}
	if request.Context.TeamID != principal.TeamID || policy.TeamID != principal.TeamID {
		return AuthorizedCandidateFilter{}, fmt.Errorf("%w: context team mismatch", ErrTeamPolicyDenied)
	}
	epoch := teamauth.EpochSnapshot{
		Global: principal.TeamEpoch, Policy: policy.PolicyEpoch,
		Principal: principal.PrincipalEpoch, Membership: principal.MembershipEpoch,
		Binding: principal.BindingEpoch,
	}
	current := epoch
	current.Global = policy.GlobalEpoch
	actor := teamauth.Actor{
		TeamID: principal.TeamID, PrincipalID: principal.PrincipalID,
		HumanPrincipalID: principal.HumanPrincipalID,
		Kind:             teamauth.PrincipalKind(principal.Kind), Role: teamauth.Role(principal.MembershipRole),
		PrincipalActive:  principal.PrincipalStatus == "active",
		MembershipActive: principal.MembershipStatus == "active",
		BindingActive:    principal.Kind != string(teamauth.PrincipalAgent) || principal.BindingStatus == "active",
		AuthorizedEpoch:  epoch, CurrentEpoch: current,
	}
	if principalErr != nil {
		actor.PrincipalActive = false
	}
	decision := teamauth.ValidatePrincipal(teamauth.ActionRead, request.Capabilities, actor)
	if !decision.Allowed {
		return AuthorizedCandidateFilter{}, fmt.Errorf("%w: %s", ErrTeamPolicyDenied, decision.Reason)
	}
	humanID := actor.EffectiveHumanPrincipalID()
	return AuthorizedCandidateFilter{
		teamID: principal.TeamID, principalID: principal.PrincipalID,
		humanPrincipalID: humanID, bindingID: principal.BindingID,
		kind: teamauth.PrincipalKind(principal.Kind), context: request.Context,
		privacyCeiling: request.PrivacyCeiling, retention: request.Retention,
		epoch: epoch,
	}, nil
}

func (s *Store) LookupAuthorizedTeamObject(ctx context.Context, filter AuthorizedCandidateFilter, objectID string) (TeamObject, error) {
	predicate, args, err := filter.SQLPredicate("object")
	if err != nil {
		return TeamObject{}, err
	}
	args = append([]any{objectID}, args...)
	object, err := scanTeamObject(s.db.QueryRowContext(ctx, `
		SELECT object.object_id, object.store_id, object.team_id, object.object_kind,
		       object.scope_type, object.scope_id, COALESCE(object.owner_principal_id, ''),
		       object.lifecycle, object.generation, object.privacy_tier, object.retention,
		       object.author_principal_id
		  FROM team_object_registry object
		 WHERE object.object_id = ? AND `+predicate, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return TeamObject{}, ErrConcealedNotFound
	}
	if err != nil {
		return TeamObject{}, err
	}
	if err := s.RecheckTeamPolicyEpoch(ctx, filter.epoch); err != nil {
		return TeamObject{}, err
	}
	return object, nil
}

type teamObjectScanner interface {
	Scan(...any) error
}

func scanTeamObject(row teamObjectScanner) (TeamObject, error) {
	var object TeamObject
	if err := row.Scan(
		&object.ObjectID, &object.StoreID, &object.TeamID, &object.ObjectKind,
		&object.Scope.Type, &object.Scope.ID, &object.Scope.OwnerPrincipalID,
		&object.Scope.Lifecycle, &object.Scope.Generation, &object.Scope.PrivacyTier,
		&object.Scope.Retention, &object.AuthorPrincipalID,
	); err != nil {
		return TeamObject{}, err
	}
	object.Scope.TeamID = object.TeamID
	return object, nil
}

type teamPolicyState struct {
	StoreID          string
	TeamID           string
	PolicyVersion    int
	SchemaVersion    int
	PolicyEpoch      int64
	GlobalEpoch      int64
	RealContentState string
}

func readTeamPolicyState(ctx context.Context, q queryer) (teamPolicyState, error) {
	var state teamPolicyState
	if err := q.QueryRowContext(ctx, `
		SELECT store_id, team_id, policy_version, schema_version,
		       policy_epoch, global_epoch, real_content_state
		  FROM team_policy_metadata`).Scan(
		&state.StoreID, &state.TeamID, &state.PolicyVersion, &state.SchemaVersion,
		&state.PolicyEpoch, &state.GlobalEpoch, &state.RealContentState,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return state, ErrTeamPolicyNotReady
		}
		return state, err
	}
	if state.PolicyVersion != teamauth.PolicyVersion || state.SchemaVersion != teamauth.SchemaVersion {
		return state, ErrTeamPolicyNotReady
	}
	return state, nil
}

func (s *Store) RecheckTeamPolicyEpoch(ctx context.Context, snapshot teamauth.EpochSnapshot) error {
	return recheckTeamPolicyEpoch(ctx, s.db, snapshot)
}

func (s *Store) RecheckTeamPolicyEpochTx(ctx context.Context, tx *sql.Tx, snapshot teamauth.EpochSnapshot) error {
	if tx == nil {
		return ErrTeamPolicyEpochChanged
	}
	return recheckTeamPolicyEpoch(ctx, tx, snapshot)
}

func recheckTeamPolicyEpoch(ctx context.Context, q queryer, snapshot teamauth.EpochSnapshot) error {
	state, err := readTeamPolicyState(ctx, q)
	if err != nil {
		return err
	}
	if state.GlobalEpoch != snapshot.Global || state.PolicyEpoch != snapshot.Policy {
		return ErrTeamPolicyEpochChanged
	}
	return nil
}

type TeamWriterLeaseRequest struct {
	WriterID      string
	WriterVersion int
	Token         string
	TTL           time.Duration
}

type TeamWriterLease struct {
	StoreID       string
	TeamID        string
	WriterID      string
	WriterVersion int
	Token         string
	ExpiresAt     time.Time
}

func (s *Store) AcquireTeamWriterLease(ctx context.Context, request TeamWriterLeaseRequest) (TeamWriterLease, error) {
	request.WriterID = strings.TrimSpace(request.WriterID)
	if request.WriterID == "" || request.WriterVersion < teamauth.SchemaVersion || request.TTL <= 0 || request.TTL > 5*time.Minute {
		return TeamWriterLease{}, ErrTeamWriterLeaseMismatch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamWriterLease{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return TeamWriterLease{}, err
	}
	if _, err := readTeamPolicyState(ctx, tx); err != nil {
		return TeamWriterLease{}, err
	}
	now := s.clock().UTC()
	var current TeamWriterLease
	var runtimeMode, currentTokenHash, expiresAt string
	err = tx.QueryRowContext(ctx, `
		SELECT writer_id, runtime_mode, writer_version, lease_token_hash, expires_at
		  FROM team_writer_leases WHERE store_id = ?`, info.StoreID).Scan(
		&current.WriterID, &runtimeMode, &current.WriterVersion, &currentTokenHash, &expiresAt,
	)
	if err == nil {
		currentExpiry, parseErr := time.Parse(time.RFC3339Nano, expiresAt)
		if parseErr != nil {
			return TeamWriterLease{}, ErrTeamWriterLeaseMismatch
		}
		if currentExpiry.After(now) {
			if runtimeMode != "team-remote" || current.WriterID != request.WriterID || !writerLeaseTokenMatches(currentTokenHash, request.Token) {
				return TeamWriterLease{}, ErrTeamWriterLeaseHeld
			}
			newExpiry := now.Add(request.TTL)
			if _, err := tx.ExecContext(ctx, `
				UPDATE team_writer_leases
				   SET writer_version = ?, heartbeat_at = ?, expires_at = ?
				 WHERE store_id = ? AND lease_token_hash = ?`,
				request.WriterVersion, now.Format(time.RFC3339Nano), newExpiry.Format(time.RFC3339Nano),
				info.StoreID, currentTokenHash); err != nil {
				return TeamWriterLease{}, err
			}
			if err := tx.Commit(); err != nil {
				return TeamWriterLease{}, err
			}
			current.StoreID, current.TeamID, current.WriterVersion, current.Token, current.ExpiresAt = info.StoreID, info.TeamID, request.WriterVersion, request.Token, newExpiry
			return current, nil
		}
		// A heartbeat may only extend the exact active lease it was issued.
		// Once expired it must fail terminally; silently rotating here would
		// leave the running server and its request guards holding a stale token.
		if request.Token != "" {
			return TeamWriterLease{}, ErrTeamWriterLeaseMismatch
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM team_writer_leases WHERE store_id = ?`, info.StoreID); err != nil {
			return TeamWriterLease{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return TeamWriterLease{}, err
	}
	if request.Token != "" {
		return TeamWriterLease{}, ErrTeamWriterLeaseMismatch
	}
	token, err := newOpaqueID("writer_lease")
	if err != nil {
		return TeamWriterLease{}, err
	}
	tokenHash := writerLeaseTokenHash(token)
	expires := now.Add(request.TTL)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_writer_leases(
			store_id, team_id, writer_id, runtime_mode, writer_version,
			lease_token_hash, acquired_at, heartbeat_at, expires_at)
		VALUES (?, ?, ?, 'team-remote', ?, ?, ?, ?, ?)`,
		info.StoreID, info.TeamID, request.WriterID, request.WriterVersion, tokenHash,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano)); err != nil {
		return TeamWriterLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamWriterLease{}, err
	}
	return TeamWriterLease{
		StoreID: info.StoreID, TeamID: info.TeamID, WriterID: request.WriterID,
		WriterVersion: request.WriterVersion, Token: token, ExpiresAt: expires,
	}, nil
}

func (s *Store) ReleaseTeamWriterLease(ctx context.Context, writerID, token string) error {
	var storeID, tokenHash string
	if err := s.db.QueryRowContext(ctx, `
		SELECT store_id, lease_token_hash FROM team_writer_leases
		 WHERE writer_id = ? AND runtime_mode = 'team-remote'`, writerID).Scan(&storeID, &tokenHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTeamWriterLeaseMismatch
		}
		return err
	}
	if !writerLeaseTokenMatches(tokenHash, token) {
		return ErrTeamWriterLeaseMismatch
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM team_writer_leases
		 WHERE store_id = ? AND writer_id = ? AND lease_token_hash = ? AND runtime_mode = 'team-remote'`,
		storeID, writerID, tokenHash)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrTeamWriterLeaseMismatch
	}
	return nil
}

type TeamPolicyReadinessOptions struct {
	TeamReadinessOptions
	WriterID    string
	WriterToken string
}

type TeamPolicyReadiness struct {
	TeamStoreInfo
	PolicyVersion    int
	SchemaVersion    int
	PolicyEpoch      int64
	GlobalEpoch      int64
	RealContentState string
	WriterID         string
	WriterVersion    int
}

func (s *Store) CheckTeamPolicyReadiness(ctx context.Context, options TeamPolicyReadinessOptions) (TeamPolicyReadiness, error) {
	info, err := s.CheckTeamReadiness(ctx, options.TeamReadinessOptions)
	if err != nil {
		return TeamPolicyReadiness{}, err
	}
	policy, err := readTeamPolicyState(ctx, s.db)
	if err != nil {
		return TeamPolicyReadiness{}, err
	}
	if policy.StoreID != info.StoreID || policy.TeamID != info.TeamID || policy.GlobalEpoch != info.AuthEpoch {
		return TeamPolicyReadiness{}, ErrTeamPolicyNotReady
	}
	if err := validateTeamPolicyIntegrity(ctx, s.db, policy); err != nil {
		return TeamPolicyReadiness{}, err
	}
	writer, err := validateTeamWriterLease(
		ctx, s.db, info.StoreID, options.WriterID, options.WriterToken, s.clock().UTC(),
	)
	if err != nil {
		return TeamPolicyReadiness{}, err
	}
	return TeamPolicyReadiness{
		TeamStoreInfo: info, PolicyVersion: policy.PolicyVersion,
		SchemaVersion: policy.SchemaVersion, PolicyEpoch: policy.PolicyEpoch,
		GlobalEpoch: policy.GlobalEpoch, RealContentState: policy.RealContentState,
		WriterID: writer.WriterID, WriterVersion: writer.WriterVersion,
	}, nil
}

func validateTeamPolicyIntegrity(ctx context.Context, q queryer, policy teamPolicyState) error {
	type integrityCheck struct {
		query string
		args  []any
	}
	checks := []integrityCheck{
		{query: `SELECT 1
		   FROM team_object_registry object
		   LEFT JOIN team_principals author
		     ON author.principal_id = object.author_principal_id
		    AND author.store_id = object.store_id
		   LEFT JOIN team_principals owner
		     ON owner.principal_id = object.owner_principal_id
		    AND owner.store_id = object.store_id
		   LEFT JOIN team_projects project
		     ON project.project_id = object.scope_id
		    AND project.team_id = object.team_id
		  WHERE object.store_id <> ?
		     OR object.team_id <> ?
		     OR author.principal_id IS NULL
		     OR (object.owner_principal_id IS NOT NULL AND owner.principal_id IS NULL)
		     OR object.scope_type NOT IN ('personal', 'team', 'project', 'repo', 'agent', 'session')
		     OR object.scope_id = ''
		     OR (object.scope_type = 'personal' AND (
		         object.owner_principal_id IS NULL OR object.scope_id <> object.owner_principal_id))
		     OR (object.scope_type = 'team' AND (
		         object.owner_principal_id IS NOT NULL OR object.scope_id <> object.team_id))
		     OR (object.scope_type = 'project' AND project.project_id IS NULL)
		     OR (object.scope_type IN ('project', 'repo', 'agent', 'session')
		         AND object.owner_principal_id IS NULL)
		     OR object.lifecycle NOT IN ('active', 'tombstoned', 'cleaning', 'cleanup_failed', 'complete')
		     OR object.generation < 1
		 LIMIT 1`, args: []any{policy.StoreID, policy.TeamID}},
		{query: `SELECT 1
		   FROM team_object_storage_map storage
		   LEFT JOIN team_object_registry object ON object.object_id = storage.object_id
		  WHERE object.object_id IS NULL
		     OR storage.team_id <> object.team_id
		     OR storage.scope_type <> object.scope_type
		     OR storage.scope_id <> object.scope_id
		     OR storage.generation > object.generation
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_object_contributions contribution
		   LEFT JOIN team_object_registry parent ON parent.object_id = contribution.parent_object_id
		   LEFT JOIN team_object_registry derivative ON derivative.object_id = contribution.derivative_object_id
		  WHERE parent.object_id IS NULL OR derivative.object_id IS NULL
		     OR contribution.team_id <> parent.team_id
		     OR contribution.team_id <> derivative.team_id
		     OR contribution.scope_type <> parent.scope_type
		     OR contribution.scope_type <> derivative.scope_type
		     OR contribution.scope_id <> parent.scope_id
		     OR contribution.scope_id <> derivative.scope_id
		     OR contribution.parent_generation > parent.generation
		     OR contribution.derivative_generation > derivative.generation
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_projection_jobs job
		   LEFT JOIN team_object_registry root ON root.object_id = job.root_object_id
		  WHERE root.object_id IS NULL
		     OR job.store_id <> root.store_id
		     OR job.team_id <> root.team_id
		     OR job.scope_type <> root.scope_type
		     OR job.scope_id <> root.scope_id
		     OR job.root_generation > root.generation
		     OR (job.state IN ('pending', 'leased') AND (
		         root.lifecycle <> 'active' OR job.root_generation <> root.generation
		     ))
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_projection_outputs output
		   LEFT JOIN team_projection_jobs job ON job.job_id = output.job_id
		   LEFT JOIN team_object_registry derivative ON derivative.object_id = output.derivative_object_id
		  WHERE job.job_id IS NULL OR derivative.object_id IS NULL
		     OR job.state <> 'ready'
		     OR derivative.team_id <> job.team_id
		     OR derivative.scope_type <> job.scope_type
		     OR derivative.scope_id <> job.scope_id
		     OR output.derivative_generation > derivative.generation
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_audit_events audit
		   LEFT JOIN team_audit_event_order ordered
		     ON ordered.event_id = audit.event_id AND ordered.store_id = audit.store_id
		  WHERE ordered.event_id IS NULL
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_audit_event_order ordered
		   LEFT JOIN team_audit_events audit
		     ON audit.event_id = ordered.event_id AND audit.store_id = ordered.store_id
		  WHERE audit.event_id IS NULL
		 LIMIT 1`},
	}
	for _, check := range checks {
		var invalid int64
		if err := q.QueryRowContext(ctx, check.query, check.args...).Scan(&invalid); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return err
		}
		return ErrTeamPolicyNotReady
	}
	return nil
}

func writerLeaseTokenHash(token string) string {
	digest := sha256.Sum256([]byte("pulse-writer-lease-token-v1\x00" + token))
	return hex.EncodeToString(digest[:])
}

func writerLeaseTokenMatches(storedHash, token string) bool {
	if token == "" {
		return false
	}
	want := writerLeaseTokenHash(token)
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(want)) == 1
}
