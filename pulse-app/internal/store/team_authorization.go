package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

// TeamMutationAuthorizationRequest contains only request-local facts already
// authenticated by the gateway plus the requested mutation target. Principal,
// client, role, ownership, grant, lifecycle, and epoch facts are always loaded
// again from the team store.
type TeamMutationAuthorizationRequest struct {
	PrincipalID      string
	OAuthClientKey   string
	Action           teamauth.Action
	Capabilities     []teamauth.Capability
	Context          teamauth.ActiveContext
	ObjectKind       string
	ExistingObjectID string
	RequestedScope   *teamauth.CanonicalScope
}

// TeamMutationAttribution is a copy of the store-derived identity attached to
// a permit. Mutating this returned value cannot mutate the permit itself.
type TeamMutationAttribution struct {
	StoreID          string
	TeamID           string
	ActorPrincipalID string
	HumanPrincipalID string
	OAuthClientKey   string
	PrincipalKind    teamauth.PrincipalKind
	MembershipRole   teamauth.Role
}

// TeamMutationPermit is sealed by this package. Its fields are deliberately
// private so a caller can copy a permit but cannot widen capabilities, context,
// target ownership, or epochs before the transaction recheck.
type TeamMutationPermit struct {
	attribution      TeamMutationAttribution
	action           teamauth.Action
	objectKind       string
	existingObjectID string
	effectiveTarget  teamauth.CanonicalScope
	epoch            teamauth.EpochSnapshot
	membershipID     string
	bindingID        string
	capabilities     []teamauth.Capability
	context          teamauth.ActiveContext
	requestedScope   *teamauth.CanonicalScope
}

func (permit TeamMutationPermit) Attribution() TeamMutationAttribution {
	return permit.attribution
}

func (permit TeamMutationPermit) Action() teamauth.Action {
	return permit.action
}

func (permit TeamMutationPermit) ObjectKind() string {
	return permit.objectKind
}

func (permit TeamMutationPermit) ExistingObjectID() string {
	return permit.existingObjectID
}

func (permit TeamMutationPermit) EffectiveTarget() teamauth.CanonicalScope {
	return permit.effectiveTarget
}

func (permit TeamMutationPermit) PolicyEpoch() teamauth.EpochSnapshot {
	return permit.epoch
}

// AuthorizeTeamMutation resolves every authority-bearing fact from one store
// snapshot. Existing objects are loaded authoritatively from the registry;
// requested scopes are used only for new writes and may only narrow policy.
func (s *Store) AuthorizeTeamMutation(ctx context.Context, request TeamMutationAuthorizationRequest) (TeamMutationPermit, error) {
	normalized, err := normalizeTeamMutationRequest(request)
	if err != nil {
		return TeamMutationPermit{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TeamMutationPermit{}, err
	}
	defer tx.Rollback()
	permit, err := authorizeTeamMutation(ctx, tx, normalized)
	if err != nil {
		return TeamMutationPermit{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamMutationPermit{}, err
	}
	return permit, nil
}

// RecheckTeamMutationPermitTx must be called inside the mutation transaction,
// immediately before the first write. It checks the global/policy/principal/
// membership/binding epochs, re-resolves the OAuth client and exact grants,
// and re-runs teamauth.Authorize against the current canonical object.
func (s *Store) RecheckTeamMutationPermitTx(ctx context.Context, tx *sql.Tx, permit TeamMutationPermit) error {
	if tx == nil || !validTeamMutationPermit(permit) {
		return ErrTeamPolicyEpochChanged
	}
	if err := recheckTeamMutationEpochLayers(ctx, tx, permit); err != nil {
		return err
	}
	request := TeamMutationAuthorizationRequest{
		PrincipalID:      permit.attribution.ActorPrincipalID,
		OAuthClientKey:   permit.attribution.OAuthClientKey,
		Action:           permit.action,
		Capabilities:     append([]teamauth.Capability(nil), permit.capabilities...),
		Context:          permit.context,
		ObjectKind:       permit.objectKind,
		ExistingObjectID: permit.existingObjectID,
		RequestedScope:   cloneCanonicalScope(permit.requestedScope),
	}
	current, err := authorizeTeamMutation(ctx, tx, request)
	if err != nil {
		if errors.Is(err, ErrConcealedNotFound) {
			return ErrConcealedNotFound
		}
		if errors.Is(err, ErrTeamPolicyDenied) || errors.Is(err, ErrPrincipalRevoked) {
			return ErrTeamPolicyEpochChanged
		}
		return err
	}
	if current.attribution != permit.attribution || !permit.epoch.Matches(current.epoch) ||
		current.membershipID != permit.membershipID || current.bindingID != permit.bindingID ||
		current.action != permit.action || current.objectKind != permit.objectKind ||
		current.existingObjectID != permit.existingObjectID {
		return ErrTeamPolicyEpochChanged
	}
	if current.effectiveTarget != permit.effectiveTarget {
		if permit.existingObjectID != "" {
			return ErrConcealedNotFound
		}
		return ErrTeamPolicyEpochChanged
	}
	return nil
}

func normalizeTeamMutationRequest(request TeamMutationAuthorizationRequest) (TeamMutationAuthorizationRequest, error) {
	request.Capabilities = append([]teamauth.Capability(nil), request.Capabilities...)
	request.RequestedScope = cloneCanonicalScope(request.RequestedScope)
	if !exactMutationValue(request.PrincipalID, 255) || !exactMutationValue(request.Context.TeamID, 255) ||
		(request.OAuthClientKey != "" && !lowerHexDigest(request.OAuthClientKey)) ||
		!validMutationAction(request.Action) || !validMutationCapabilities(request.Capabilities) ||
		!validMutationContext(request.Context) {
		return TeamMutationAuthorizationRequest{}, teamMutationDenied(teamauth.ReasonInvalidRequest)
	}
	if request.ExistingObjectID != "" {
		if !exactMutationValue(request.ExistingObjectID, 255) || request.RequestedScope != nil || request.ObjectKind != "" {
			return TeamMutationAuthorizationRequest{}, teamMutationDenied(teamauth.ReasonInvalidRequest)
		}
		return request, nil
	}
	if request.Action != teamauth.ActionWrite || !exactMutationValue(request.ObjectKind, 64) {
		return TeamMutationAuthorizationRequest{}, teamMutationDenied(teamauth.ReasonInvalidRequest)
	}
	if request.RequestedScope != nil {
		target := request.RequestedScope
		if !optionalMutationValue(target.TeamID, 255) || !optionalMutationValue(target.ID, 255) ||
			!optionalMutationValue(target.OwnerPrincipalID, 255) ||
			(target.Lifecycle != "" && target.Lifecycle != teamauth.LifecycleActive) ||
			(target.Generation != 0 && target.Generation != 1) ||
			!validOptionalPrivacy(target.PrivacyTier) || !validOptionalRetention(target.Retention) {
			return TeamMutationAuthorizationRequest{}, teamMutationDenied(teamauth.ReasonInvalidRequest)
		}
		target.Lifecycle = teamauth.LifecycleActive
		target.Generation = 1
	}
	return request, nil
}

func authorizeTeamMutation(ctx context.Context, q queryer, request TeamMutationAuthorizationRequest) (TeamMutationPermit, error) {
	info, err := readTeamStoreInfo(ctx, q)
	if err != nil {
		return TeamMutationPermit{}, err
	}
	policy, err := readTeamPolicyState(ctx, q)
	if err != nil {
		return TeamMutationPermit{}, err
	}
	if policy.StoreID != info.StoreID || policy.TeamID != info.TeamID || policy.GlobalEpoch != info.AuthEpoch {
		return TeamMutationPermit{}, ErrTeamPolicyNotReady
	}
	principal, err := resolveTeamPrincipal(ctx, q, info, request.PrincipalID)
	if err != nil {
		if errors.Is(err, ErrPrincipalRevoked) {
			return TeamMutationPermit{}, teamMutationDenied(teamauth.ReasonPrincipalRevoked)
		}
		return TeamMutationPermit{}, err
	}
	clientMatches, err := teamMutationClientMatches(ctx, q, principal, request.OAuthClientKey)
	if err != nil {
		return TeamMutationPermit{}, err
	}
	if !clientMatches {
		return TeamMutationPermit{}, teamMutationDenied(teamauth.ReasonPrincipalRevoked)
	}
	epoch := teamauth.EpochSnapshot{
		Global: info.AuthEpoch, Policy: policy.PolicyEpoch,
		Principal: principal.PrincipalEpoch, Membership: principal.MembershipEpoch,
		Binding: principal.BindingEpoch,
	}
	actor := teamauth.Actor{
		TeamID: info.TeamID, PrincipalID: principal.PrincipalID,
		HumanPrincipalID: principal.HumanPrincipalID,
		Kind:             teamauth.PrincipalKind(principal.Kind),
		Role:             teamauth.Role(principal.MembershipRole),
		PrincipalActive:  principal.PrincipalStatus == "active",
		MembershipActive: principal.MembershipStatus == "active",
		BindingActive:    principal.Kind != string(teamauth.PrincipalAgent) || principal.BindingStatus == "active",
		AuthorizedEpoch:  epoch, CurrentEpoch: epoch,
	}
	if base := teamauth.ValidatePrincipal(request.Action, request.Capabilities, actor); !base.Allowed {
		return TeamMutationPermit{}, teamMutationDenied(base.Reason)
	}

	objectKind := request.ObjectKind
	var target *teamauth.CanonicalScope
	targetAuthoritative := request.ExistingObjectID != ""
	if targetAuthoritative {
		object, err := loadTeamMutationObject(ctx, q, request.ExistingObjectID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return TeamMutationPermit{}, ErrConcealedNotFound
			}
			return TeamMutationPermit{}, err
		}
		if object.StoreID != info.StoreID || object.TeamID != info.TeamID {
			return TeamMutationPermit{}, ErrConcealedNotFound
		}
		objectKind = object.ObjectKind
		target = &object.Scope
	} else {
		target = cloneCanonicalScope(request.RequestedScope)
	}
	grants := teamauth.GrantFacts{}
	if target != nil {
		grants, err = loadTeamMutationGrantFacts(ctx, q, actor, *target, objectKind, request.Action)
		if err != nil {
			return TeamMutationPermit{}, err
		}
	}
	decision := teamauth.Authorize(teamauth.AuthorizationRequest{
		Action: request.Action, Capabilities: request.Capabilities,
		Actor: actor, Context: request.Context, Target: target,
		TargetAuthoritative: targetAuthoritative, Grants: grants,
	})
	if !decision.Allowed {
		if targetAuthoritative {
			return TeamMutationPermit{}, ErrConcealedNotFound
		}
		return TeamMutationPermit{}, teamMutationDenied(decision.Reason)
	}
	if decision.EffectiveTarget.TeamID != info.TeamID {
		if targetAuthoritative {
			return TeamMutationPermit{}, ErrConcealedNotFound
		}
		return TeamMutationPermit{}, teamMutationDenied(teamauth.ReasonContextMismatch)
	}
	humanID := actor.EffectiveHumanPrincipalID()
	return TeamMutationPermit{
		attribution: TeamMutationAttribution{
			StoreID: info.StoreID, TeamID: info.TeamID,
			ActorPrincipalID: principal.PrincipalID, HumanPrincipalID: humanID,
			OAuthClientKey: request.OAuthClientKey,
			PrincipalKind:  teamauth.PrincipalKind(principal.Kind),
			MembershipRole: teamauth.Role(principal.MembershipRole),
		},
		action: request.Action, objectKind: objectKind,
		existingObjectID: request.ExistingObjectID,
		effectiveTarget:  decision.EffectiveTarget, epoch: epoch,
		membershipID: principal.MembershipID, bindingID: principal.BindingID,
		capabilities: append([]teamauth.Capability(nil), request.Capabilities...),
		context:      request.Context, requestedScope: cloneCanonicalScope(request.RequestedScope),
	}, nil
}

func teamMutationClientMatches(ctx context.Context, q queryer, principal ResolvedTeamPrincipal, clientKey string) (bool, error) {
	if principal.Kind == string(teamauth.PrincipalHuman) {
		return clientKey == "", nil
	}
	if !lowerHexDigest(clientKey) {
		return false, nil
	}
	var teamID, kind, principalID, bindingID string
	if err := q.QueryRowContext(ctx, `
		SELECT team_id, kind, principal_id, COALESCE(binding_id, '')
		  FROM team_oauth_clients WHERE oauth_client_key = ?`, clientKey).
		Scan(&teamID, &kind, &principalID, &bindingID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return teamID == principal.TeamID && kind == principal.Kind && principalID == principal.PrincipalID &&
		((kind == string(teamauth.PrincipalAgent) && bindingID == principal.BindingID) ||
			(kind == string(teamauth.PrincipalService) && bindingID == "")), nil
}

func loadTeamMutationObject(ctx context.Context, q queryer, objectID string) (TeamObject, error) {
	return scanTeamObject(q.QueryRowContext(ctx, `
		SELECT object_id, store_id, team_id, object_kind,
		       scope_type, scope_id, COALESCE(owner_principal_id, ''),
		       lifecycle, generation, privacy_tier, retention, author_principal_id
		  FROM team_object_registry WHERE object_id = ?`, objectID))
}

func loadTeamMutationGrantFacts(ctx context.Context, q queryer, actor teamauth.Actor, target teamauth.CanonicalScope, objectKind string, action teamauth.Action) (teamauth.GrantFacts, error) {
	var facts teamauth.GrantFacts
	if target.Type == teamauth.ScopeProject {
		var projectOwner string
		err := q.QueryRowContext(ctx, `
			SELECT owner_principal_id FROM team_projects
			 WHERE project_id = ? AND team_id = ?`, target.ID, actor.TeamID).Scan(&projectOwner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return facts, err
		}
		if err == nil && actor.Kind != teamauth.PrincipalService {
			facts.ProjectOwned = projectOwner == actor.EffectiveHumanPrincipalID()
		}
		var access string
		err = q.QueryRowContext(ctx, `
			SELECT grant.access_level
			  FROM team_project_grants grant
			  JOIN team_projects project ON project.project_id = grant.project_id
			 WHERE grant.project_id = ? AND project.team_id = ?
			   AND grant.principal_id = ? AND grant.status = 'active'`,
			target.ID, actor.TeamID, actor.PrincipalID).Scan(&access)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return facts, err
		}
		if err == nil {
			facts.ProjectAccess = teamauth.AccessLevel(access)
		}
	}
	if actor.Kind == teamauth.PrincipalService && (action == teamauth.ActionRead || action == teamauth.ActionWrite) {
		var count int
		if err := q.QueryRowContext(ctx, `
			SELECT count(*) FROM team_service_object_grants
			 WHERE team_id = ? AND service_principal_id = ? AND action = ?
			   AND scope_type = ? AND scope_id = ? AND status = 'active'
			   AND (object_kind = ? OR object_kind = '*')`,
			actor.TeamID, actor.PrincipalID, string(action), string(target.Type), target.ID, objectKind).Scan(&count); err != nil {
			return facts, err
		}
		facts.ServiceObjectGrant = count > 0
	}
	return facts, nil
}

func recheckTeamMutationEpochLayers(ctx context.Context, tx *sql.Tx, permit TeamMutationPermit) error {
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return err
	}
	policy, err := readTeamPolicyState(ctx, tx)
	if err != nil {
		return err
	}
	if info.StoreID != permit.attribution.StoreID || info.TeamID != permit.attribution.TeamID ||
		info.AuthEpoch != permit.epoch.Global || policy.StoreID != info.StoreID || policy.TeamID != info.TeamID ||
		policy.GlobalEpoch != permit.epoch.Global || policy.PolicyEpoch != permit.epoch.Policy {
		return ErrTeamPolicyEpochChanged
	}
	principal, err := resolveTeamPrincipal(ctx, tx, info, permit.attribution.ActorPrincipalID)
	if err != nil {
		if errors.Is(err, ErrPrincipalRevoked) {
			return ErrTeamPolicyEpochChanged
		}
		return err
	}
	humanID := (teamauth.Actor{
		PrincipalID: principal.PrincipalID, HumanPrincipalID: principal.HumanPrincipalID,
		Kind: teamauth.PrincipalKind(principal.Kind),
	}).EffectiveHumanPrincipalID()
	clientMatches, err := teamMutationClientMatches(ctx, tx, principal, permit.attribution.OAuthClientKey)
	if err != nil {
		return err
	}
	if principal.TeamID != permit.attribution.TeamID || principal.StoreID != permit.attribution.StoreID ||
		principal.PrincipalID != permit.attribution.ActorPrincipalID || humanID != permit.attribution.HumanPrincipalID ||
		teamauth.PrincipalKind(principal.Kind) != permit.attribution.PrincipalKind ||
		teamauth.Role(principal.MembershipRole) != permit.attribution.MembershipRole ||
		principal.MembershipID != permit.membershipID || principal.BindingID != permit.bindingID ||
		principal.PrincipalEpoch != permit.epoch.Principal || principal.MembershipEpoch != permit.epoch.Membership ||
		principal.BindingEpoch != permit.epoch.Binding || !clientMatches {
		return ErrTeamPolicyEpochChanged
	}
	return nil
}

func validTeamMutationPermit(permit TeamMutationPermit) bool {
	return permit.attribution.StoreID != "" && permit.attribution.TeamID != "" &&
		permit.attribution.ActorPrincipalID != "" && permit.membershipID != "" &&
		validMutationAction(permit.action) && permit.objectKind != "" &&
		permit.effectiveTarget.TeamID == permit.attribution.TeamID && permit.epoch.Global > 0 &&
		permit.epoch.Policy > 0 && permit.epoch.Principal > 0 && permit.epoch.Membership > 0
}

func cloneCanonicalScope(scope *teamauth.CanonicalScope) *teamauth.CanonicalScope {
	if scope == nil {
		return nil
	}
	copy := *scope
	return &copy
}

func teamMutationDenied(reason teamauth.Reason) error {
	return fmt.Errorf("%w: %s", ErrTeamPolicyDenied, reason)
}

func validMutationAction(action teamauth.Action) bool {
	return action == teamauth.ActionWrite || action == teamauth.ActionDelete
}

func validMutationCapabilities(capabilities []teamauth.Capability) bool {
	for _, capability := range capabilities {
		switch capability {
		case teamauth.CapabilityConnect, teamauth.CapabilityStatus, teamauth.CapabilityRead,
			teamauth.CapabilityWrite, teamauth.CapabilityAudit, teamauth.CapabilityDelete:
		default:
			return false
		}
	}
	return true
}

func validMutationContext(context teamauth.ActiveContext) bool {
	return exactMutationValue(context.TeamID, 255) && optionalMutationValue(context.ProjectID, 255) &&
		optionalMutationValue(context.RepoID, 255) && optionalMutationValue(context.AgentID, 255) &&
		optionalMutationValue(context.SessionID, 255)
}

func exactMutationValue(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value
}

func optionalMutationValue(value string, maximum int) bool {
	return value == "" || exactMutationValue(value, maximum)
}

func lowerHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validOptionalPrivacy(value string) bool {
	return value == "" || validPrivacyTier(value)
}

func validOptionalRetention(value string) bool {
	return value == "" || validRetention(value)
}
