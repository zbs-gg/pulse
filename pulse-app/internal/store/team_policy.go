package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
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
	authorizedAt     time.Time
	epoch            teamauth.EpochSnapshot
}

func (filter AuthorizedCandidateFilter) PolicyEpoch() teamauth.EpochSnapshot {
	return filter.epoch
}

// FilterFingerprint is a stable, content-free cache partition key for this
// exact authorization snapshot. It deliberately excludes evaluation time and
// every object/content identifier: cached candidates still require a fresh
// root-access recheck before they can influence a response.
func (filter AuthorizedCandidateFilter) FilterFingerprint() string {
	return teamGraphOpaqueDigestID(
		"candidate_filter", "pulse-team-authorized-candidate-filter-v1",
		filter.teamID, filter.principalID, filter.humanPrincipalID, filter.bindingID,
		string(filter.kind), filter.context.TeamID, filter.context.ProjectID,
		filter.context.RepoID, filter.context.AgentID, filter.context.SessionID,
		filter.privacyCeiling, filter.retention,
		strconv.FormatInt(filter.epoch.Global, 10),
		strconv.FormatInt(filter.epoch.Policy, 10),
		strconv.FormatInt(filter.epoch.Principal, 10),
		strconv.FormatInt(filter.epoch.Membership, 10),
		strconv.FormatInt(filter.epoch.Binding, 10),
		strconv.Itoa(teamauth.PolicyVersion), strconv.Itoa(teamauth.SchemaVersion),
	)
}

var sqlAliasPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (filter AuthorizedCandidateFilter) SQLPredicate(alias string) (string, []any, error) {
	return filter.sqlPredicateAt(alias, filter.authorizedAt)
}

func (filter AuthorizedCandidateFilter) sqlPredicateAt(alias string, at time.Time) (string, []any, error) {
	if !sqlAliasPattern.MatchString(alias) {
		return "", nil, fmt.Errorf("invalid SQL alias %q", alias)
	}
	if at.IsZero() {
		return "", nil, fmt.Errorf("authorized candidate filter has no evaluation time")
	}
	column := func(name string) string { return alias + "." + name }
	clauses := []string{
		column("team_id") + " = ?",
		column("lifecycle") + " = 'active'",
		"(" + column("expires_at") + " IS NULL OR julianday(" + column("expires_at") + ") > julianday(?))",
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
		filter.teamID, at.UTC().Format(time.RFC3339Nano),
		teamauth.PolicyVersion, teamauth.SchemaVersion,
		filter.epoch.Policy, filter.epoch.Global,
	}

	identitySQL, identityArgs := filter.identityPredicate()
	clauses = append(clauses, identitySQL)
	args = append(args, identityArgs...)

	scopeSQL, scopeArgs := filter.scopePredicate(alias)
	clauses = append(clauses, scopeSQL)
	args = append(args, scopeArgs...)

	for _, active := range []struct {
		scopeType string
		scopeID   string
	}{
		{scopeType: "project", scopeID: filter.context.ProjectID},
		{scopeType: "repo", scopeID: filter.context.RepoID},
		{scopeType: "agent", scopeID: filter.context.AgentID},
		{scopeType: "session", scopeID: filter.context.SessionID},
	} {
		if active.scopeID == "" {
			clauses = append(clauses, column("scope_type")+" <> '"+active.scopeType+"'")
			continue
		}
		clauses = append(clauses,
			"("+column("scope_type")+" <> '"+active.scopeType+"' OR "+column("scope_id")+" = ?)")
		args = append(args, active.scopeID)
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
	if principalErr != nil {
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
	if actor.Kind == teamauth.PrincipalAgent {
		if principal.BindingID == "" ||
			(request.Context.AgentID != "" && request.Context.AgentID != principal.BindingID) {
			return AuthorizedCandidateFilter{}, fmt.Errorf(
				"%w: agent context conflicts with authenticated binding", ErrTeamPolicyDenied,
			)
		}
		request.Context.AgentID = principal.BindingID
	}
	humanID := actor.EffectiveHumanPrincipalID()
	return AuthorizedCandidateFilter{
		teamID: principal.TeamID, principalID: principal.PrincipalID,
		humanPrincipalID: humanID, bindingID: principal.BindingID,
		kind: teamauth.PrincipalKind(principal.Kind), context: request.Context,
		privacyCeiling: request.PrivacyCeiling, retention: request.Retention,
		authorizedAt: s.clock().UTC(), epoch: epoch,
	}, nil
}

// RecheckAuthorizedCandidateFilter is the fresh empty-result boundary. It
// proves that the identity and epoch snapshot which produced an empty candidate
// set is still active, without probing or counting any hidden object.
func (s *Store) RecheckAuthorizedCandidateFilter(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
) error {
	if filter.teamID == "" || filter.principalID == "" || filter.authorizedAt.IsZero() {
		return ErrTeamPolicyDenied
	}
	if err := s.recheckAuthorizedCandidateIdentity(ctx, filter); err != nil {
		return err
	}
	if err := s.RecheckTeamPolicyEpoch(ctx, filter.epoch); err != nil {
		return s.classifyAuthorizedCandidateEpochError(ctx, filter, err)
	}
	identitySQL, identityArgs := filter.identityPredicate()
	var present int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 WHERE `+identitySQL, identityArgs...).Scan(&present); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if identityErr := s.recheckAuthorizedCandidateIdentity(ctx, filter); identityErr != nil {
				return identityErr
			}
			return ErrPrincipalRevoked
		}
		return err
	}
	if err := s.RecheckTeamPolicyEpoch(ctx, filter.epoch); err != nil {
		return s.classifyAuthorizedCandidateEpochError(ctx, filter, err)
	}
	return nil
}

// recheckAuthorizedCandidateIdentity distinguishes revocation of the exact
// authenticated principal/binding from unrelated policy or grant churn. The
// team-wide epoch is intentionally checked separately: an active principal
// surviving someone else's mutation must report epoch_changed, not revoked.
func (s *Store) recheckAuthorizedCandidateIdentity(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
) error {
	principal, err := s.ResolveTeamPrincipal(ctx, filter.principalID)
	if err != nil {
		return err
	}
	currentKind := teamauth.PrincipalKind(principal.Kind)
	currentHumanID := ""
	switch currentKind {
	case teamauth.PrincipalHuman:
		currentHumanID = principal.PrincipalID
	case teamauth.PrincipalAgent:
		currentHumanID = principal.HumanPrincipalID
	case teamauth.PrincipalService:
	default:
		return ErrPrincipalRevoked
	}
	if principal.TeamID != filter.teamID || principal.PrincipalID != filter.principalID ||
		currentKind != filter.kind || currentHumanID != filter.humanPrincipalID ||
		principal.BindingID != filter.bindingID {
		return ErrPrincipalRevoked
	}
	if principal.PrincipalEpoch != filter.epoch.Principal ||
		principal.MembershipEpoch != filter.epoch.Membership ||
		principal.BindingEpoch != filter.epoch.Binding {
		return ErrTeamPolicyEpochChanged
	}
	return nil
}

func (s *Store) classifyAuthorizedCandidateEpochError(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
	epochErr error,
) error {
	if !errors.Is(epochErr, ErrTeamPolicyEpochChanged) {
		return epochErr
	}
	identityErr := s.recheckAuthorizedCandidateIdentity(ctx, filter)
	if errors.Is(identityErr, ErrPrincipalRevoked) {
		return identityErr
	}
	if identityErr != nil && !errors.Is(identityErr, ErrTeamPolicyEpochChanged) {
		return identityErr
	}
	return epochErr
}

// RecheckAuthorizedCandidateRoots repeats current root access for every opaque
// parent returned by a repository. Empty sets use the indistinguishable empty
// recheck above. A final identity/epoch check closes revocation races between
// the last root lookup and the caller's response boundary.
func (s *Store) RecheckAuthorizedCandidateRoots(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
	rootObjectIDs []string,
) error {
	if len(rootObjectIDs) == 0 {
		return s.RecheckAuthorizedCandidateFilter(ctx, filter)
	}
	seen := make(map[string]struct{}, len(rootObjectIDs))
	uniqueRootIDs := make([]string, 0, len(rootObjectIDs))
	for _, rootObjectID := range rootObjectIDs {
		if !validProjectionOpaque(rootObjectID, 255) {
			return ErrConcealedNotFound
		}
		if _, duplicate := seen[rootObjectID]; duplicate {
			continue
		}
		seen[rootObjectID] = struct{}{}
		uniqueRootIDs = append(uniqueRootIDs, rootObjectID)
	}
	if len(uniqueRootIDs) > 1000 {
		return ErrTeamPolicyDenied
	}
	if err := s.RecheckAuthorizedCandidateFilter(ctx, filter); err != nil {
		return err
	}

	boundaryTime := s.clock().UTC()
	predicate, predicateArgs, err := filter.sqlPredicateAt("object", boundaryTime)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	placeholders := make([]string, len(uniqueRootIDs))
	args := make([]any, 0, len(uniqueRootIDs)+len(predicateArgs))
	for index, rootObjectID := range uniqueRootIDs {
		placeholders[index] = "?"
		args = append(args, rootObjectID)
	}
	args = append(args, predicateArgs...)
	var authorizedRoots int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		  FROM team_object_registry object
		 WHERE object.object_id IN (`+strings.Join(placeholders, ",")+`)
		   AND `+predicate, args...).Scan(&authorizedRoots); err != nil {
		return err
	}
	if authorizedRoots != len(uniqueRootIDs) {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return err
		}
		if err := s.RecheckAuthorizedCandidateFilter(ctx, filter); err != nil {
			return err
		}
		return ErrConcealedNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.RecheckAuthorizedCandidateFilter(ctx, filter)
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
	if err := s.RecheckAuthorizedTeamObjectAccess(ctx, filter, objectID); err != nil {
		return TeamObject{}, err
	}
	return object, nil
}

// RecheckAuthorizedTeamObjectAccess repeats the fixed policy predicate with a
// fresh clock value immediately before a response or object-specific side
// effect. The exact object lookup remains bounded by the registry primary key.
func (s *Store) RecheckAuthorizedTeamObjectAccess(ctx context.Context, filter AuthorizedCandidateFilter, objectID string) error {
	if objectID == "" {
		return ErrConcealedNotFound
	}
	if err := s.RecheckTeamPolicyEpoch(ctx, filter.epoch); err != nil {
		return err
	}
	predicate, args, err := filter.sqlPredicateAt("object", s.clock().UTC())
	if err != nil {
		return err
	}
	args = append([]any{objectID}, args...)
	var exists int
	if err := s.db.QueryRowContext(ctx, `
		SELECT 1
		  FROM team_object_registry object
		 WHERE object.object_id = ? AND `+predicate, args...).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConcealedNotFound
		}
		return err
	}
	return s.RecheckTeamPolicyEpoch(ctx, filter.epoch)
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
		     OR ((object.scope_type = 'session' OR object.retention = 'session') AND (
		         object.expires_at IS NULL
		         OR julianday(object.created_at) IS NULL
		         OR julianday(object.expires_at) IS NULL
		         OR julianday(object.expires_at) <= julianday(object.created_at)
		         OR julianday(object.expires_at) > julianday(object.created_at, '+24 hours')
		         OR substr(object.expires_at, 1, 19) NOT GLOB
		            '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]'
		         OR NOT (
		            (length(object.expires_at) = 20 AND substr(object.expires_at, 20, 1) = 'Z')
		            OR (length(object.expires_at) BETWEEN 22 AND 30
		                AND substr(object.expires_at, 20, 1) = '.'
		                AND substr(object.expires_at, -1, 1) = 'Z'
		                AND substr(object.expires_at, 21, length(object.expires_at) - 21)
		                    NOT GLOB '*[^0-9]*')
		         )
		     ))
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
		   FROM team_memory_capsules memory
		   LEFT JOIN team_object_registry root ON root.object_id = memory.root_object_id
		  WHERE root.object_id IS NULL
		     OR memory.team_id <> root.team_id
		     OR memory.scope_type <> root.scope_type
		     OR memory.scope_id <> root.scope_id
		     OR memory.root_generation > root.generation
		     OR (root.lifecycle = 'active' AND memory.root_generation <> root.generation)
		     OR root.object_kind <> 'memory'
		     OR length(memory.capsule_id) NOT BETWEEN 1 AND 255
		     OR memory.capsule_id GLOB '*[^A-Za-z0-9._:-]*'
		     OR memory.schema_version <> 'pulse.team.memory.v1'
		     OR memory.item_ordinal NOT BETWEEN 0 AND 19
		     OR memory.source_host NOT IN (
		        'chatgpt', 'claude', 'codex', 'claude-code', 'gemini-cli',
		        'cursor', 'langchain', 'crewai', 'pulse-cli')
		     OR memory.conversation_scope NOT IN (
		        'current_turn', 'user_selected_excerpt', 'project_context', 'install_event')
		     OR memory.kind NOT IN (
		        'fact', 'decision', 'preference', 'project_state', 'open_loop',
		        'correction', 'relationship_note', 'do_not_repeat', 'system_event', 'state_signal')
		     OR memory.evidence_hint NOT IN (
		        'user_selected', 'current_turn', 'assistant_inferred', 'tool_result', 'user_confirmed')
		     OR memory.confidence < 0.0 OR memory.confidence > 1.0
		     OR length(memory.redacted_summary) NOT BETWEEN 1 AND 1200
		     OR length(memory.source_timestamp) <> 24
		     OR substr(memory.source_timestamp, 1, 19) NOT GLOB
		        '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]'
		     OR substr(memory.source_timestamp, 20, 1) <> '.'
		     OR substr(memory.source_timestamp, 21, 3) GLOB '*[^0-9]*'
		     OR substr(memory.source_timestamp, 24, 1) <> 'Z'
		     OR julianday(memory.source_timestamp) IS NULL
		     OR json_valid(memory.tags_json) <> 1
		     OR CASE WHEN json_valid(memory.tags_json) = 1 THEN
		          json_type(memory.tags_json) <> 'array'
		          OR json_array_length(memory.tags_json) > 32
		          OR EXISTS (
		             SELECT 1 FROM json_each(memory.tags_json) tag
		              WHERE tag.type <> 'text' OR length(tag.value) NOT BETWEEN 1 AND 64
		          )
		        ELSE 0 END
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_graph_delta_inputs input
		   LEFT JOIN team_object_registry root ON root.object_id = input.root_object_id
		  WHERE root.object_id IS NULL
		     OR input.store_id <> root.store_id
		     OR input.team_id <> root.team_id
		     OR input.scope_type <> root.scope_type
		     OR input.scope_id <> root.scope_id
		     OR input.root_generation > root.generation
		     OR (root.lifecycle = 'active' AND input.root_generation <> root.generation)
		     OR root.object_kind <> 'graph_delta'
		     OR input.schema_version <> 'pulse.team.graph_delta.v1'
		     OR input.source_host NOT IN (
		        'chatgpt', 'claude', 'codex', 'claude-code', 'gemini-cli',
		        'cursor', 'langchain', 'crewai', 'pulse-cli')
		     OR input.conversation_scope NOT IN (
		        'current_turn', 'user_selected_excerpt', 'project_context', 'install_event')
		     OR length(input.source_timestamp) <> 24
		     OR substr(input.source_timestamp, 1, 19) NOT GLOB
		        '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]'
		     OR substr(input.source_timestamp, 20, 1) <> '.'
		     OR substr(input.source_timestamp, 21, 3) GLOB '*[^0-9]*'
		     OR substr(input.source_timestamp, 24, 1) <> 'Z'
		     OR julianday(input.source_timestamp) IS NULL
		     OR length(input.content_digest) <> 64
		     OR input.content_digest GLOB '*[^0-9a-f]*'
		     OR json_valid(input.canonical_json) <> 1
		     OR CASE WHEN json_valid(input.canonical_json) = 1 THEN
		          json_type(input.canonical_json) <> 'object'
		          OR length(CAST(input.canonical_json AS BLOB)) NOT BETWEEN 2 AND 262144
		          OR COALESCE(json_extract(input.canonical_json, '$.schema'), '') <> 'pulse.team.graph_delta.v1'
		          OR COALESCE(json_type(input.canonical_json, '$.raw_input_included'), '') <> 'false'
		          OR COALESCE(json_extract(input.canonical_json, '$.source.host'), '') <> input.source_host
		          OR COALESCE(json_extract(input.canonical_json, '$.source.conversation_scope'), '') <> input.conversation_scope
		          OR COALESCE(json_extract(input.canonical_json, '$.source.timestamp'), '') <> input.source_timestamp
		          OR json_type(input.canonical_json, '$.idempotency_key') IS NOT NULL
		          OR json_type(input.canonical_json, '$.actor') IS NOT NULL
		          OR json_type(input.canonical_json, '$.principal_id') IS NOT NULL
		          OR json_type(input.canonical_json, '$.owner_principal_id') IS NOT NULL
		          OR json_type(input.canonical_json, '$.team_id') IS NOT NULL
		        ELSE 0 END
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_object_registry root
		   LEFT JOIN team_graph_delta_inputs input
		     ON input.root_object_id = root.object_id
		    AND input.store_id = root.store_id
		    AND input.team_id = root.team_id
		    AND input.scope_type = root.scope_type
		    AND input.scope_id = root.scope_id
		    AND input.root_generation = root.generation
		  WHERE root.object_kind = 'graph_delta'
		    AND root.lifecycle = 'active'
		    AND input.root_object_id IS NULL
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_semantic_projection_intents intent
		   LEFT JOIN team_object_registry root ON root.object_id = intent.root_object_id
		   LEFT JOIN team_graph_delta_inputs input
		     ON input.root_object_id = intent.root_object_id
		    AND input.root_generation = intent.root_generation
		  WHERE root.object_id IS NULL OR input.root_object_id IS NULL
		     OR intent.store_id <> root.store_id OR intent.store_id <> input.store_id
		     OR intent.team_id <> root.team_id OR intent.team_id <> input.team_id
		     OR intent.scope_type <> root.scope_type OR intent.scope_type <> input.scope_type
		     OR intent.scope_id <> root.scope_id OR intent.scope_id <> input.scope_id
		     OR intent.root_generation > root.generation
		     OR (root.lifecycle = 'active' AND intent.root_generation <> root.generation)
		     OR root.object_kind <> 'graph_delta'
		     OR length(intent.intent_id) NOT BETWEEN 1 AND 255
		     OR intent.intent_id GLOB '*[^A-Za-z0-9._:-]*'
		     OR length(intent.derivative_object_id) NOT BETWEEN 1 AND 255
		     OR intent.derivative_object_id GLOB '*[^A-Za-z0-9._:-]*'
		     OR intent.source_ordinal NOT BETWEEN 0 AND 49
		     OR length(intent.semantic_key_digest) <> 64
		     OR intent.semantic_key_digest GLOB '*[^0-9a-f]*'
		     OR length(intent.policy_digest) <> 64
		     OR intent.policy_digest GLOB '*[^0-9a-f]*'
		     OR length(intent.payload_digest) <> 64
		     OR intent.payload_digest GLOB '*[^0-9a-f]*'
		     OR NOT (
		        (intent.projection_kind = 'claim' AND intent.source_kind = 'fact'
		         AND intent.derivative_kind = 'assertion')
		        OR (intent.projection_kind = 'continuity' AND intent.source_kind = 'continuity'
		            AND intent.source_ordinal = 0
		            AND intent.derivative_kind = 'continuity_checkpoint')
		        OR (intent.projection_kind = 'embedding'
		            AND intent.source_kind IN ('node', 'edge', 'fact', 'event')
		            AND intent.derivative_kind = 'embedding')
		        OR (intent.projection_kind = 'graph' AND (
		               (intent.source_kind = 'node' AND intent.derivative_kind = 'graph_entity')
		            OR (intent.source_kind = 'edge' AND intent.derivative_kind = 'graph_relation')
		            OR (intent.source_kind = 'fact' AND intent.derivative_kind = 'graph_fact')
		            OR (intent.source_kind = 'event' AND intent.derivative_kind = 'graph_event')
		        ))
		     )
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_projection_jobs job
		   JOIN team_object_registry root ON root.object_id = job.root_object_id
		  WHERE root.object_kind = 'graph_delta'
		    AND root.lifecycle = 'active'
		    AND root.generation = job.root_generation
		    AND (
		      job.projection_kind NOT IN ('claim', 'continuity', 'embedding', 'graph')
		      OR NOT EXISTS (
		        SELECT 1 FROM team_semantic_projection_intents intent
		         WHERE intent.root_object_id = job.root_object_id
		           AND intent.root_generation = job.root_generation
		           AND intent.store_id = job.store_id
		           AND intent.team_id = job.team_id
		           AND intent.scope_type = job.scope_type
		           AND intent.scope_id = job.scope_id
		           AND intent.projection_kind = job.projection_kind
		      )
		    )
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_semantic_projection_intents intent
		   JOIN team_object_registry root ON root.object_id = intent.root_object_id
		  WHERE root.object_kind = 'graph_delta'
		    AND root.lifecycle = 'active'
		    AND root.generation = intent.root_generation
		    AND NOT EXISTS (
		      SELECT 1 FROM team_projection_jobs job
		       WHERE job.root_object_id = intent.root_object_id
		         AND job.root_generation = intent.root_generation
		         AND job.store_id = intent.store_id
		         AND job.team_id = intent.team_id
		         AND job.scope_type = intent.scope_type
		         AND job.scope_id = intent.scope_id
		         AND job.projection_kind = intent.projection_kind
		    )
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_memory_events event
		   LEFT JOIN team_object_registry root ON root.object_id = event.root_object_id
		   LEFT JOIN team_memory_capsules capsule ON capsule.capsule_id = event.capsule_id
		   LEFT JOIN team_object_registry derivative ON derivative.object_id = event.derivative_object_id
		   LEFT JOIN team_projection_jobs job ON job.job_id = event.job_id
		   LEFT JOIN team_projection_outputs output
		     ON output.job_id = event.job_id AND output.derivative_object_id = event.derivative_object_id
		   LEFT JOIN team_object_contributions contribution
		     ON contribution.parent_object_id = event.root_object_id
		    AND contribution.derivative_object_id = event.derivative_object_id
		   LEFT JOIN team_object_storage_map storage
		     ON storage.object_id = event.derivative_object_id
		    AND storage.representation_kind = 'memory_event' AND storage.storage_key = event.event_id
		  WHERE root.object_id IS NULL OR capsule.capsule_id IS NULL OR derivative.object_id IS NULL
		     OR job.job_id IS NULL OR output.job_id IS NULL OR contribution.parent_object_id IS NULL
		     OR storage.object_id IS NULL OR root.object_kind <> 'memory'
		     OR event.team_id <> root.team_id OR event.scope_type <> root.scope_type
		     OR event.scope_id <> root.scope_id OR event.root_generation > root.generation
		     OR capsule.root_object_id <> event.root_object_id
		     OR capsule.root_generation <> event.root_generation
		     OR capsule.team_id <> event.team_id OR capsule.scope_type <> event.scope_type
		     OR capsule.scope_id <> event.scope_id
		     OR event.kind <> capsule.kind OR event.redacted_summary <> capsule.redacted_summary
		     OR event.source_timestamp <> capsule.source_timestamp OR event.tags_json <> capsule.tags_json
		     OR derivative.team_id <> event.team_id OR derivative.scope_type <> event.scope_type
		     OR derivative.scope_id <> event.scope_id OR derivative.object_kind <> 'event'
		     OR job.root_object_id <> event.root_object_id
		     OR job.root_generation <> event.root_generation OR job.projection_kind <> 'event'
		     OR job.state <> 'ready' OR length(event.content_digest) <> 64
		     OR event.content_digest GLOB '*[^0-9a-f]*'
		     OR length(event.source_timestamp) <> 24
		     OR substr(event.source_timestamp, 1, 19) NOT GLOB
		        '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]'
		     OR substr(event.source_timestamp, 20, 1) <> '.'
		     OR substr(event.source_timestamp, 21, 3) GLOB '*[^0-9]*'
		     OR substr(event.source_timestamp, 24, 1) <> 'Z'
		     OR julianday(event.source_timestamp) IS NULL
		     OR json_valid(event.tags_json) <> 1
		     OR CASE WHEN json_valid(event.tags_json) = 1 THEN json_type(event.tags_json) <> 'array' ELSE 0 END
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_memory_embeddings embedding
		   LEFT JOIN team_object_registry root ON root.object_id = embedding.root_object_id
		   LEFT JOIN team_memory_capsules capsule ON capsule.capsule_id = embedding.capsule_id
		   LEFT JOIN team_object_registry derivative ON derivative.object_id = embedding.derivative_object_id
		   LEFT JOIN team_projection_jobs job ON job.job_id = embedding.job_id
		   LEFT JOIN team_projection_outputs output
		     ON output.job_id = embedding.job_id AND output.derivative_object_id = embedding.derivative_object_id
		   LEFT JOIN team_object_contributions contribution
		     ON contribution.parent_object_id = embedding.root_object_id
		    AND contribution.derivative_object_id = embedding.derivative_object_id
		   LEFT JOIN team_object_storage_map storage
		     ON storage.object_id = embedding.derivative_object_id
		    AND storage.representation_kind = 'memory_embedding' AND storage.storage_key = embedding.embedding_id
		  WHERE root.object_id IS NULL OR capsule.capsule_id IS NULL OR derivative.object_id IS NULL
		     OR job.job_id IS NULL OR output.job_id IS NULL OR contribution.parent_object_id IS NULL
		     OR storage.object_id IS NULL OR root.object_kind <> 'memory'
		     OR embedding.team_id <> root.team_id OR embedding.scope_type <> root.scope_type
		     OR embedding.scope_id <> root.scope_id OR embedding.root_generation > root.generation
		     OR capsule.root_object_id <> embedding.root_object_id
		     OR capsule.root_generation <> embedding.root_generation
		     OR capsule.team_id <> embedding.team_id OR capsule.scope_type <> embedding.scope_type
		     OR capsule.scope_id <> embedding.scope_id
		     OR derivative.team_id <> embedding.team_id OR derivative.scope_type <> embedding.scope_type
		     OR derivative.scope_id <> embedding.scope_id OR derivative.object_kind <> 'embedding'
		     OR job.root_object_id <> embedding.root_object_id
		     OR job.root_generation <> embedding.root_generation OR job.projection_kind <> 'embedding'
		     OR job.state <> 'ready' OR embedding.dimensions NOT BETWEEN 1 AND 4096
		     OR length(embedding.model) NOT BETWEEN 1 AND 64
		     OR embedding.model GLOB '*[^a-z0-9._:-]*'
		     OR length(embedding.vector_digest) <> 64 OR embedding.vector_digest GLOB '*[^0-9a-f]*'
		     OR length(embedding.content_digest) <> 64 OR embedding.content_digest GLOB '*[^0-9a-f]*'
		     OR json_valid(embedding.vector_json) <> 1
		     OR CASE WHEN json_valid(embedding.vector_json) = 1 THEN
		          json_type(embedding.vector_json) <> 'array'
		          OR json_array_length(embedding.vector_json) <> embedding.dimensions
		          OR EXISTS (
		             SELECT 1 FROM json_each(embedding.vector_json) value
		              WHERE value.type NOT IN ('integer', 'real')
		          )
		        ELSE 0 END
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_projection_jobs job
		   JOIN team_object_registry root ON root.object_id = job.root_object_id
		  WHERE root.object_kind = 'memory' AND root.lifecycle = 'active'
		     AND root.generation = job.root_generation
		     AND job.projection_kind = 'event' AND job.state = 'ready'
		     AND (
		       SELECT count(*) FROM team_memory_events event
		        WHERE event.job_id = job.job_id
		          AND event.root_object_id = job.root_object_id
		          AND event.root_generation = job.root_generation
		     ) <> (
		       SELECT count(*) FROM team_memory_capsules capsule
		        WHERE capsule.root_object_id = job.root_object_id
		          AND capsule.root_generation = job.root_generation
		     )
		 LIMIT 1`},
		{query: `SELECT 1
		   FROM team_projection_jobs job
		   JOIN team_object_registry root ON root.object_id = job.root_object_id
		  WHERE root.object_kind = 'memory' AND root.lifecycle = 'active'
		     AND root.generation = job.root_generation
		     AND job.projection_kind = 'embedding' AND job.state = 'ready'
		     AND (
		       SELECT count(*) FROM team_memory_embeddings embedding
		        WHERE embedding.job_id = job.job_id
		          AND embedding.root_object_id = job.root_object_id
		          AND embedding.root_generation = job.root_generation
		     ) <> (
		       SELECT count(*) FROM team_memory_capsules capsule
		        WHERE capsule.root_object_id = job.root_object_id
		          AND capsule.root_generation = job.root_generation
		     )
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

type teamGraphIntegrityQueryer interface {
	queryer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type teamGraphIntegrityRoot struct {
	objectID, storeID, teamID  string
	scopeType                  teamauth.ScopeType
	scopeID, ownerID, authorID string
	privacyTier, retention     string
	generation                 int64
	expiresAt, canonicalJSON   string
	contentDigest              string
}

func validateTeamGraphIngressDescriptorIntegrity(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	policy teamPolicyState,
) error {
	rows, err := q.QueryContext(ctx, `
		SELECT root.object_id FROM team_object_registry root
		 WHERE root.object_kind = 'graph_delta' AND root.lifecycle = 'active'
		 ORDER BY root.object_id`)
	if err != nil {
		return err
	}
	var rootIDs []string
	for rows.Next() {
		var rootID string
		if err := rows.Scan(&rootID); err != nil {
			rows.Close()
			return err
		}
		rootIDs = append(rootIDs, rootID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, rootID := range rootIDs {
		root, err := loadTeamGraphIntegrityRoot(ctx, q, rootID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTeamPolicyNotReady
		}
		if err != nil {
			return err
		}
		if root.storeID != policy.StoreID || root.teamID != policy.TeamID {
			return ErrTeamPolicyNotReady
		}
		if err := validateOneTeamGraphIngressDescriptorSet(ctx, q, root); err != nil {
			return err
		}
	}
	return nil
}

func loadTeamGraphIntegrityRoot(
	ctx context.Context,
	q queryer,
	rootID string,
) (teamGraphIntegrityRoot, error) {
	var root teamGraphIntegrityRoot
	var scopeType string
	err := q.QueryRowContext(ctx, `
		SELECT root.object_id, root.store_id, root.team_id, root.scope_type,
		       root.scope_id, COALESCE(root.owner_principal_id, ''),
		       root.author_principal_id, root.privacy_tier, root.retention,
		       root.generation, COALESCE(root.expires_at, ''),
		       input.canonical_json, input.content_digest
		  FROM team_object_registry root
		  JOIN team_graph_delta_inputs input
		    ON input.root_object_id = root.object_id
		   AND input.store_id = root.store_id
		   AND input.team_id = root.team_id
		   AND input.scope_type = root.scope_type
		   AND input.scope_id = root.scope_id
		   AND input.root_generation = root.generation
		 WHERE root.object_id = ? AND root.object_kind = 'graph_delta'
		   AND root.lifecycle = 'active'`, rootID).Scan(
		&root.objectID, &root.storeID, &root.teamID, &scopeType,
		&root.scopeID, &root.ownerID, &root.authorID,
		&root.privacyTier, &root.retention, &root.generation,
		&root.expiresAt, &root.canonicalJSON, &root.contentDigest,
	)
	root.scopeType = teamauth.ScopeType(scopeType)
	return root, err
}

func validateOneTeamGraphIngressDescriptorSet(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	root teamGraphIntegrityRoot,
) error {
	_, err := loadValidatedTeamGraphIngressDescriptorSet(ctx, q, root)
	return err
}

// loadValidatedTeamGraphIngressDescriptorSet is the single reconstruction
// boundary shared by readiness and semantic projection workers. It derives
// the original sealed permit from durable attribution, re-canonicalizes the
// stored body, and checks the exact intent and job sets before returning any
// content to a projector.
func loadValidatedTeamGraphIngressDescriptorSet(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	root teamGraphIntegrityRoot,
) (normalizedTeamGraphDeltaWrite, error) {
	write, err := decodeCanonicalTeamGraphReadinessBody([]byte(root.canonicalJSON))
	if err != nil {
		return normalizedTeamGraphDeltaWrite{}, ErrTeamPolicyNotReady
	}
	write.IdempotencyKey = "readiness-placeholder"

	var actorID, clientKey, idempotencyKeyHash, principalKind, humanPrincipalID string
	var idempotencyRows int
	err = q.QueryRowContext(ctx, `
		SELECT idem.principal_id, idem.client_key, idem.idempotency_key_hash,
		       principal.kind, COALESCE(binding.human_principal_id, ''),
		       (SELECT count(*) FROM team_idempotency_records counted
		         WHERE counted.object_id = ? AND counted.state = 'stored')
		  FROM team_idempotency_records idem
		  JOIN team_principals principal ON principal.principal_id = idem.principal_id
		  LEFT JOIN team_agent_bindings binding
		    ON binding.agent_principal_id = idem.principal_id
		   AND binding.client_key = idem.client_key
		 WHERE idem.object_id = ? AND idem.state = 'stored'
		 LIMIT 1`, root.objectID, root.objectID).Scan(
		&actorID, &clientKey, &idempotencyKeyHash,
		&principalKind, &humanPrincipalID, &idempotencyRows,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return normalizedTeamGraphDeltaWrite{}, ErrTeamPolicyNotReady
	}
	if err != nil {
		return normalizedTeamGraphDeltaWrite{}, err
	}
	if idempotencyRows != 1 || actorID != root.authorID || !lowerHexDigest(clientKey) ||
		!lowerHexDigest(idempotencyKeyHash) {
		return normalizedTeamGraphDeltaWrite{}, ErrTeamPolicyNotReady
	}
	kind := teamauth.PrincipalKind(principalKind)
	if (kind != teamauth.PrincipalAgent && kind != teamauth.PrincipalService) ||
		(kind == teamauth.PrincipalAgent && humanPrincipalID == "") ||
		(kind == teamauth.PrincipalService && humanPrincipalID != "") {
		return normalizedTeamGraphDeltaWrite{}, ErrTeamPolicyNotReady
	}

	var requestedScope *teamauth.CanonicalScope
	if write.TargetScope != nil {
		requestedScope = &teamauth.CanonicalScope{
			Type: write.TargetScope.Type,
			ID:   write.TargetScope.ID,
		}
	}
	permit := TeamMutationPermit{
		attribution: TeamMutationAttribution{
			StoreID: root.storeID, TeamID: root.teamID,
			ActorPrincipalID: actorID, HumanPrincipalID: humanPrincipalID,
			OAuthClientKey: clientKey, PrincipalKind: kind,
		},
		action: teamauth.ActionWrite, objectKind: "graph_delta",
		effectiveTarget: teamauth.CanonicalScope{
			TeamID: root.teamID, Type: root.scopeType, ID: root.scopeID,
			OwnerPrincipalID: root.ownerID, Lifecycle: teamauth.LifecycleActive,
			Generation: root.generation, PrivacyTier: root.privacyTier,
			Retention: root.retention,
		},
		context:        teamGraphActiveContextToAuth(root.teamID, write.ActiveContext),
		requestedScope: requestedScope,
	}
	normalized, err := normalizeTeamGraphDeltaWriteWithIdempotencyHash(
		permit, write, idempotencyKeyHash,
	)
	if err != nil || !bytes.Equal(normalized.canonicalBody, []byte(root.canonicalJSON)) ||
		normalized.bodyDigest != root.contentDigest ||
		normalized.body.PrivacyTier != root.privacyTier ||
		normalized.body.Retention != root.retention ||
		!teamGraphReadinessExpiryMatches(root, normalized.expiresAt) {
		return normalizedTeamGraphDeltaWrite{}, ErrTeamPolicyNotReady
	}
	if err := validateTeamGraphIntentRows(ctx, q, root, normalized.intentDescriptors); err != nil {
		return normalizedTeamGraphDeltaWrite{}, err
	}
	if err := validateTeamGraphJobRows(ctx, q, root, normalized.projectionKinds); err != nil {
		return normalizedTeamGraphDeltaWrite{}, err
	}
	return normalized, nil
}

func teamGraphActiveContextToAuth(teamID string, active TeamGraphActiveContext) teamauth.ActiveContext {
	return teamauth.ActiveContext{
		TeamID: teamID, ProjectID: active.ProjectID, RepoID: active.RepoID,
		AgentID: active.AgentID, SessionID: active.SessionID,
	}
}

func decodeCanonicalTeamGraphReadinessBody(raw []byte) (TeamGraphDeltaWrite, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var canonical teamGraphCanonicalBody
	if err := decoder.Decode(&canonical); err != nil {
		return TeamGraphDeltaWrite{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("team graph canonical body has trailing JSON")
		}
		return TeamGraphDeltaWrite{}, err
	}
	reencoded, err := marshalTeamGraphCanonical(canonical)
	if err != nil || !bytes.Equal(reencoded, raw) {
		return TeamGraphDeltaWrite{}, ErrTeamGraphDeltaInvalid
	}
	var write TeamGraphDeltaWrite
	if err := json.Unmarshal(raw, &write); err != nil {
		return TeamGraphDeltaWrite{}, err
	}
	return write, nil
}

func teamGraphReadinessExpiryMatches(
	root teamGraphIntegrityRoot,
	explicit *time.Time,
) bool {
	if explicit != nil {
		stored, err := time.Parse(time.RFC3339Nano, root.expiresAt)
		return err == nil && stored.Equal(explicit.UTC())
	}
	if root.scopeType == teamauth.ScopeSession || root.retention == "session" {
		_, err := time.Parse(time.RFC3339Nano, root.expiresAt)
		return err == nil
	}
	return root.expiresAt == ""
}

func validateTeamGraphIntentRows(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	root teamGraphIntegrityRoot,
	descriptors []teamSemanticIntentDescriptor,
) error {
	expected := make(map[string]teamSemanticProjectionIntent, len(descriptors))
	for _, descriptor := range descriptors {
		key := teamGraphIntentSetKey(
			descriptor.ProjectionKind, descriptor.SourceKind, descriptor.SourceOrdinal,
		)
		expected[key] = teamSemanticProjectionIntent{
			IntentID: teamGraphOpaqueDigestID(
				"semantic_intent", "pulse-team-semantic-intent-v1", root.objectID,
				strconv.FormatInt(root.generation, 10), descriptor.ProjectionKind,
				descriptor.SourceKind, strconv.Itoa(descriptor.SourceOrdinal),
				descriptor.DerivativeObjectID, descriptor.PayloadDigest,
			),
			ProjectionKind: descriptor.ProjectionKind, SourceKind: descriptor.SourceKind,
			SourceOrdinal:      descriptor.SourceOrdinal,
			DerivativeObjectID: descriptor.DerivativeObjectID,
			DerivativeKind:     descriptor.DerivativeKind,
			SemanticKeyDigest:  descriptor.SemanticKeyDigest,
			PolicyDigest:       descriptor.PolicyDigest, PayloadDigest: descriptor.PayloadDigest,
		}
	}
	rows, err := q.QueryContext(ctx, `
		SELECT intent_id, projection_kind, source_kind, source_ordinal,
		       derivative_object_id, derivative_kind, semantic_key_digest,
		       policy_digest, payload_digest
		  FROM team_semantic_projection_intents
		 WHERE root_object_id = ? AND root_generation = ?
		 ORDER BY projection_kind, source_kind, source_ordinal`,
		root.objectID, root.generation)
	if err != nil {
		return err
	}
	seen := 0
	for rows.Next() {
		var actual teamSemanticProjectionIntent
		if err := rows.Scan(
			&actual.IntentID, &actual.ProjectionKind, &actual.SourceKind,
			&actual.SourceOrdinal, &actual.DerivativeObjectID, &actual.DerivativeKind,
			&actual.SemanticKeyDigest, &actual.PolicyDigest, &actual.PayloadDigest,
		); err != nil {
			rows.Close()
			return err
		}
		key := teamGraphIntentSetKey(actual.ProjectionKind, actual.SourceKind, actual.SourceOrdinal)
		want, ok := expected[key]
		if !ok || actual != want {
			rows.Close()
			return ErrTeamPolicyNotReady
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if seen != len(expected) {
		return ErrTeamPolicyNotReady
	}
	return nil
}

func validateTeamGraphJobRows(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	root teamGraphIntegrityRoot,
	expectedKinds []string,
) error {
	rows, err := q.QueryContext(ctx, `
		SELECT projection_kind, store_id, team_id, scope_type, scope_id, root_generation
		  FROM team_projection_jobs WHERE root_object_id = ?
		 ORDER BY projection_kind`, root.objectID)
	if err != nil {
		return err
	}
	var actualKinds []string
	for rows.Next() {
		var kind, storeID, teamID, scopeType, scopeID string
		var generation int64
		if err := rows.Scan(&kind, &storeID, &teamID, &scopeType, &scopeID, &generation); err != nil {
			rows.Close()
			return err
		}
		if storeID != root.storeID || teamID != root.teamID ||
			scopeType != string(root.scopeType) || scopeID != root.scopeID ||
			generation != root.generation {
			rows.Close()
			return ErrTeamPolicyNotReady
		}
		actualKinds = append(actualKinds, kind)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	sort.Strings(expectedKinds)
	if len(actualKinds) != len(expectedKinds) {
		return ErrTeamPolicyNotReady
	}
	for index := range expectedKinds {
		if actualKinds[index] != expectedKinds[index] {
			return ErrTeamPolicyNotReady
		}
	}
	return nil
}

func teamGraphIntentSetKey(projectionKind, sourceKind string, ordinal int) string {
	return projectionKind + "\x00" + sourceKind + "\x00" + strconv.Itoa(ordinal)
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
