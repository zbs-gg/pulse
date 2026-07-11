package teamauth

// PolicyVersion is the executable role/action/scope contract stored alongside
// every dedicated team database. It is independent from the SQL schema
// version so either contract can fail readiness without guessing.
const PolicyVersion = 1

type Capability string

const (
	CapabilityConnect Capability = "pulse:connect"
	CapabilityStatus  Capability = "pulse:status"
	CapabilityRead    Capability = "pulse:read"
	CapabilityWrite   Capability = "pulse:write"
	CapabilityAudit   Capability = "pulse:audit"
	CapabilityDelete  Capability = "pulse:delete"
)

type PrincipalKind string

const (
	PrincipalHuman   PrincipalKind = "human"
	PrincipalAgent   PrincipalKind = "agent"
	PrincipalService PrincipalKind = "service"
)

type Role string

const (
	RoleOwner    Role = "owner"
	RoleMember   Role = "member"
	RoleReviewer Role = "reviewer"
)

type Action string

const (
	ActionConnect  Action = "connect"
	ActionStatus   Action = "status"
	ActionRead     Action = "read"
	ActionWrite    Action = "write"
	ActionDelete   Action = "delete"
	ActionAudit    Action = "audit"
	ActionPromote  Action = "promote"
	ActionManage   Action = "manage"
	ActionRevoke   Action = "revoke"
	ActionTeamWipe Action = "team_wipe"
)

type ScopeType string

const (
	ScopePersonal ScopeType = "personal"
	ScopeTeam     ScopeType = "team"
	ScopeProject  ScopeType = "project"
	ScopeRepo     ScopeType = "repo"
	ScopeAgent    ScopeType = "agent"
	ScopeSession  ScopeType = "session"
)

type LifecycleState string

const (
	LifecycleActive        LifecycleState = "active"
	LifecycleTombstoned    LifecycleState = "tombstoned"
	LifecycleCleaning      LifecycleState = "cleaning"
	LifecycleCleanupFailed LifecycleState = "cleanup_failed"
	LifecycleComplete      LifecycleState = "complete"
)

type AccessLevel string

const (
	AccessNone  AccessLevel = ""
	AccessRead  AccessLevel = "read"
	AccessWrite AccessLevel = "write"
	AccessAdmin AccessLevel = "admin"
)

type Reason string

const (
	ReasonAllowed                Reason = "allowed"
	ReasonInvalidRequest         Reason = "invalid_request"
	ReasonInsufficientCapability Reason = "insufficient_capability"
	ReasonPrincipalRevoked       Reason = "principal_revoked"
	ReasonEpochChanged           Reason = "policy_epoch_changed"
	ReasonContextMismatch        Reason = "context_mismatch"
	ReasonExplicitTargetRequired Reason = "explicit_target_required"
	ReasonScopePromotionDisabled Reason = "scope_promotion_disabled"
	ReasonProjectGrantRequired   Reason = "project_grant_required"
	ReasonServiceGrantRequired   Reason = "service_grant_required"
	ReasonOwnershipRequired      Reason = "ownership_required"
	ReasonOwnerApprovalRequired  Reason = "owner_approval_required"
	ReasonConcealedNotFound      Reason = "concealed_not_found"
)

// EpochSnapshot is captured at authorization time and compared with current
// state before a write commits and before a read response or side effect.
// Binding is zero for non-agent principals.
type EpochSnapshot struct {
	Global     int64
	Policy     int64
	Principal  int64
	Membership int64
	Binding    int64
}

func (snapshot EpochSnapshot) Matches(current EpochSnapshot) bool {
	return snapshot == current
}

type Actor struct {
	TeamID           string
	PrincipalID      string
	HumanPrincipalID string
	Kind             PrincipalKind
	Role             Role
	PrincipalActive  bool
	MembershipActive bool
	BindingActive    bool
	AuthorizedEpoch  EpochSnapshot
	CurrentEpoch     EpochSnapshot
}

func (actor Actor) EffectiveHumanPrincipalID() string {
	switch actor.Kind {
	case PrincipalHuman:
		return actor.PrincipalID
	case PrincipalAgent:
		return actor.HumanPrincipalID
	default:
		return ""
	}
}

type ActiveContext struct {
	TeamID    string
	ProjectID string
	RepoID    string
	AgentID   string
	SessionID string
}

// CanonicalScope is server-derived for an existing object. On a new write,
// callers may request Type and ID, but TeamID and ownership are overwritten or
// checked against the authenticated actor before this value is persisted.
type CanonicalScope struct {
	TeamID           string
	Type             ScopeType
	ID               string
	OwnerPrincipalID string
	Lifecycle        LifecycleState
	Generation       int64
	PrivacyTier      string
	Retention        string
}

type GrantFacts struct {
	ProjectAccess      AccessLevel
	ProjectOwned       bool
	ServiceObjectGrant bool
	OwnAudit           bool
	OwnerApproval      bool
}

type AuthorizationRequest struct {
	Action       Action
	Capabilities []Capability
	Actor        Actor
	Context      ActiveContext
	Target       *CanonicalScope
	// TargetAuthoritative is true only when Target was loaded from the
	// canonical registry. Caller-proposed write targets must leave it false so
	// team identity and ownership are derived instead of trusted.
	TargetAuthoritative bool
	Grants              GrantFacts
}

type Decision struct {
	Allowed         bool
	Reason          Reason
	EffectiveTarget CanonicalScope
	Epoch           EpochSnapshot
}

func RequiredCapability(action Action) Capability {
	switch action {
	case ActionConnect:
		return CapabilityConnect
	case ActionStatus:
		return CapabilityStatus
	case ActionRead:
		return CapabilityRead
	case ActionWrite, ActionPromote:
		return CapabilityWrite
	case ActionDelete:
		return CapabilityDelete
	case ActionAudit:
		return CapabilityAudit
	default:
		return ""
	}
}

// ValidatePrincipal applies the capability, active-identity, role, and epoch
// layers that are common to object decisions and candidate-filter creation.
func ValidatePrincipal(action Action, capabilities []Capability, actor Actor) Decision {
	decision := Decision{Reason: ReasonInvalidRequest, Epoch: actor.AuthorizedEpoch}
	if actor.TeamID == "" || actor.PrincipalID == "" || !validRole(actor.Role) || !validPrincipalKind(actor.Kind) {
		return decision
	}
	required := RequiredCapability(action)
	if required != "" && !hasCapability(capabilities, required) {
		decision.Reason = ReasonInsufficientCapability
		return decision
	}
	if !actor.PrincipalActive || !actor.MembershipActive || (actor.Kind == PrincipalAgent && (!actor.BindingActive || actor.HumanPrincipalID == "")) {
		decision.Reason = ReasonPrincipalRevoked
		return decision
	}
	if !actor.AuthorizedEpoch.Matches(actor.CurrentEpoch) {
		decision.Reason = ReasonEpochChanged
		return decision
	}
	decision.Allowed = true
	decision.Reason = ReasonAllowed
	return decision
}

func Authorize(request AuthorizationRequest) Decision {
	decision := ValidatePrincipal(request.Action, request.Capabilities, request.Actor)
	if !decision.Allowed {
		return decision
	}
	deny := func(reason Reason) Decision {
		return Decision{Reason: reason, Epoch: request.Actor.AuthorizedEpoch}
	}

	switch request.Action {
	case ActionConnect, ActionStatus:
		return decision
	case ActionTeamWipe:
		return deny(ReasonScopePromotionDisabled)
	case ActionManage, ActionRevoke:
		if request.Actor.Kind == PrincipalHuman && request.Actor.Role == RoleOwner && request.Grants.OwnerApproval {
			return decision
		}
		return deny(ReasonOwnerApprovalRequired)
	case ActionAudit:
		if request.Actor.Kind == PrincipalService {
			return deny(ReasonServiceGrantRequired)
		}
		if request.Actor.Kind == PrincipalHuman && request.Actor.Role == RoleOwner {
			return decision
		}
		if request.Grants.OwnAudit {
			return decision
		}
		return deny(ReasonOwnershipRequired)
	}

	if request.Action == ActionPromote {
		return deny(ReasonScopePromotionDisabled)
	}

	target, ok, targetReason := resolveTarget(request)
	if !ok {
		return deny(targetReason)
	}
	decision.EffectiveTarget = target
	if target.TeamID != request.Actor.TeamID ||
		(request.Context.TeamID != "" && request.Context.TeamID != request.Actor.TeamID) ||
		!contextContainsTarget(request.Context, target) {
		return deny(ReasonContextMismatch)
	}
	if target.Lifecycle != LifecycleActive {
		return deny(ReasonConcealedNotFound)
	}
	if !validCanonicalScope(target) {
		return deny(ReasonInvalidRequest)
	}

	switch request.Action {
	case ActionRead:
		if authorizeRead(request.Actor, target, request.Grants) {
			return decision
		}
	case ActionWrite:
		if target.Type == ScopeTeam {
			return deny(ReasonScopePromotionDisabled)
		}
		if authorizeWrite(request.Actor, target, request.Grants) {
			return decision
		}
	case ActionDelete:
		if target.Type == ScopeTeam {
			if request.Actor.Kind == PrincipalHuman && request.Actor.Role == RoleOwner && request.Grants.OwnerApproval {
				return decision
			}
			return deny(ReasonOwnerApprovalRequired)
		}
		if target.Type != ScopePersonal && target.OwnerPrincipalID != request.Actor.EffectiveHumanPrincipalID() &&
			request.Actor.Kind == PrincipalHuman && request.Actor.Role == RoleOwner && request.Grants.OwnerApproval {
			return decision
		}
		if authorizeDelete(request.Actor, target, request.Grants) {
			return decision
		}
	default:
		return deny(ReasonInvalidRequest)
	}

	if request.Actor.Kind == PrincipalService {
		return deny(ReasonServiceGrantRequired)
	}
	if target.Type == ScopeProject {
		return deny(ReasonProjectGrantRequired)
	}
	return deny(ReasonOwnershipRequired)
}

func resolveTarget(request AuthorizationRequest) (CanonicalScope, bool, Reason) {
	if request.Target == nil {
		if request.Action != ActionWrite {
			return CanonicalScope{}, false, ReasonConcealedNotFound
		}
		ownerID := request.Actor.EffectiveHumanPrincipalID()
		if ownerID == "" {
			return CanonicalScope{}, false, ReasonExplicitTargetRequired
		}
		return CanonicalScope{
			TeamID: request.Actor.TeamID, Type: ScopePersonal, ID: ownerID,
			OwnerPrincipalID: ownerID, Lifecycle: LifecycleActive, Generation: 1,
		}, true, ReasonAllowed
	}
	target := *request.Target
	if target.TeamID == "" {
		if request.Action != ActionWrite || request.TargetAuthoritative {
			return CanonicalScope{}, false, ReasonConcealedNotFound
		}
		target.TeamID = request.Actor.TeamID
	}
	if request.Action == ActionWrite {
		if request.TargetAuthoritative {
			if target.Lifecycle == "" || target.Generation < 1 {
				return CanonicalScope{}, false, ReasonInvalidRequest
			}
			return target, true, ReasonAllowed
		}
		if target.Lifecycle == "" {
			target.Lifecycle = LifecycleActive
		}
		if target.Generation == 0 {
			target.Generation = 1
		}
		if target.Type != ScopeTeam {
			ownerID := request.Actor.EffectiveHumanPrincipalID()
			if request.Actor.Kind == PrincipalService {
				ownerID = request.Actor.PrincipalID
			}
			if ownerID != "" {
				if target.OwnerPrincipalID != "" && target.OwnerPrincipalID != ownerID {
					return CanonicalScope{}, false, ReasonOwnershipRequired
				}
				target.OwnerPrincipalID = ownerID
			}
			if target.Type == ScopePersonal && request.Actor.Kind != PrincipalService {
				if target.ID != "" && target.ID != ownerID {
					return CanonicalScope{}, false, ReasonOwnershipRequired
				}
				target.ID = ownerID
			}
		}
	}
	return target, true, ReasonAllowed
}

func contextContainsTarget(context ActiveContext, target CanonicalScope) bool {
	switch target.Type {
	case ScopeProject:
		return context.ProjectID == "" || context.ProjectID == target.ID
	case ScopeRepo:
		return context.RepoID == "" || context.RepoID == target.ID
	case ScopeAgent:
		return context.AgentID == "" || context.AgentID == target.ID
	case ScopeSession:
		return context.SessionID == "" || context.SessionID == target.ID
	default:
		return true
	}
}

func authorizeRead(actor Actor, target CanonicalScope, grants GrantFacts) bool {
	if actor.Kind == PrincipalService {
		if !grants.ServiceObjectGrant {
			return false
		}
		return target.Type != ScopeProject || hasProjectAccess(grants.ProjectAccess, AccessRead)
	}
	switch target.Type {
	case ScopePersonal:
		return target.OwnerPrincipalID == actor.EffectiveHumanPrincipalID()
	case ScopeProject:
		return grants.ProjectOwned || hasProjectAccess(grants.ProjectAccess, AccessRead)
	case ScopeTeam:
		return true
	case ScopeRepo, ScopeAgent, ScopeSession:
		return target.OwnerPrincipalID == actor.EffectiveHumanPrincipalID()
	default:
		return false
	}
}

func authorizeWrite(actor Actor, target CanonicalScope, grants GrantFacts) bool {
	if actor.Kind == PrincipalService {
		if target.Type == ScopePersonal || target.Type == ScopeTeam || !grants.ServiceObjectGrant {
			return false
		}
		return target.Type != ScopeProject || hasProjectAccess(grants.ProjectAccess, AccessWrite)
	}
	switch target.Type {
	case ScopePersonal:
		return target.OwnerPrincipalID == actor.EffectiveHumanPrincipalID()
	case ScopeProject:
		return grants.ProjectOwned || hasProjectAccess(grants.ProjectAccess, AccessWrite)
	case ScopeTeam:
		return false
	case ScopeRepo, ScopeAgent, ScopeSession:
		return target.OwnerPrincipalID == actor.EffectiveHumanPrincipalID()
	default:
		return false
	}
}

func authorizeDelete(actor Actor, target CanonicalScope, grants GrantFacts) bool {
	if actor.Kind == PrincipalService || target.OwnerPrincipalID != actor.EffectiveHumanPrincipalID() {
		return false
	}
	if target.Type == ScopeProject {
		return grants.ProjectOwned || hasProjectAccess(grants.ProjectAccess, AccessWrite)
	}
	return target.Type == ScopePersonal || target.Type == ScopeRepo || target.Type == ScopeAgent || target.Type == ScopeSession
}

func hasProjectAccess(actual, required AccessLevel) bool {
	return accessRank(actual) >= accessRank(required)
}

func accessRank(level AccessLevel) int {
	switch level {
	case AccessRead:
		return 1
	case AccessWrite:
		return 2
	case AccessAdmin:
		return 3
	default:
		return 0
	}
}

func hasCapability(capabilities []Capability, required Capability) bool {
	for _, capability := range capabilities {
		if capability == required {
			return true
		}
	}
	return false
}

func validRole(role Role) bool {
	return role == RoleOwner || role == RoleMember || role == RoleReviewer
}

func validPrincipalKind(kind PrincipalKind) bool {
	return kind == PrincipalHuman || kind == PrincipalAgent || kind == PrincipalService
}

func validCanonicalScope(scope CanonicalScope) bool {
	if scope.TeamID == "" || scope.ID == "" || scope.Generation < 1 || scope.Lifecycle == "" {
		return false
	}
	switch scope.Type {
	case ScopePersonal:
		return scope.OwnerPrincipalID != "" && scope.ID == scope.OwnerPrincipalID
	case ScopeTeam:
		return scope.ID == scope.TeamID
	case ScopeProject, ScopeRepo, ScopeAgent, ScopeSession:
		return scope.OwnerPrincipalID != ""
	default:
		return false
	}
}
