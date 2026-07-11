package teamauth

import "testing"

func activeActor(kind PrincipalKind, role Role) Actor {
	actor := Actor{
		TeamID:           "team-1",
		PrincipalID:      "human-1",
		Kind:             kind,
		Role:             role,
		PrincipalActive:  true,
		MembershipActive: true,
		BindingActive:    true,
		AuthorizedEpoch:  EpochSnapshot{Global: 7, Policy: 3, Principal: 2, Membership: 4, Binding: 5},
		CurrentEpoch:     EpochSnapshot{Global: 7, Policy: 3, Principal: 2, Membership: 4, Binding: 5},
	}
	if kind == PrincipalAgent {
		actor.PrincipalID = "agent-1"
		actor.HumanPrincipalID = "human-1"
	}
	if kind == PrincipalService {
		actor.PrincipalID = "service-1"
		actor.HumanPrincipalID = ""
	}
	return actor
}

func scope(scopeType ScopeType, id, owner string) *CanonicalScope {
	return &CanonicalScope{
		TeamID: "team-1", Type: scopeType, ID: id, OwnerPrincipalID: owner,
		Lifecycle: LifecycleActive, Generation: 1, PrivacyTier: "normal", Retention: "long_term",
	}
}

func TestPolicyRoleAndActionMatrix(t *testing.T) {
	personal := scope(ScopePersonal, "human-1", "human-1")
	project := scope(ScopeProject, "project-1", "human-1")
	team := scope(ScopeTeam, "team-1", "")

	tests := []struct {
		name    string
		request AuthorizationRequest
		allowed bool
	}{
		{name: "owner reads own personal", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalHuman, RoleOwner), Target: personal}, allowed: true},
		{name: "member agent reads own personal", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalAgent, RoleMember), Target: personal}, allowed: true},
		{name: "reviewer agent reads own personal", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalAgent, RoleReviewer), Target: personal}, allowed: true},
		{name: "service reads personal only by grant", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalService, RoleMember), Target: personal, Grants: GrantFacts{ServiceObjectGrant: true}}, allowed: true},
		{name: "service cannot infer a personal grant", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalService, RoleMember), Target: personal}, allowed: false},

		{name: "owner reads owned project", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalHuman, RoleOwner), Target: project, Grants: GrantFacts{ProjectOwned: true}}, allowed: true},
		{name: "member agent reads assigned project", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalAgent, RoleMember), Target: project, Grants: GrantFacts{ProjectAccess: AccessRead}}, allowed: true},
		{name: "reviewer agent reads assigned project", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalAgent, RoleReviewer), Target: project, Grants: GrantFacts{ProjectAccess: AccessRead}}, allowed: true},
		{name: "service project read intersects project and object grants", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalService, RoleMember), Target: project, Grants: GrantFacts{ProjectAccess: AccessRead, ServiceObjectGrant: true}}, allowed: true},
		{name: "service project object grant alone is insufficient", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalService, RoleMember), Target: project, Grants: GrantFacts{ServiceObjectGrant: true}}, allowed: false},
		{name: "owner reads active team scope", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalHuman, RoleOwner), Target: team}, allowed: true},
		{name: "member agent reads active team scope", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalAgent, RoleMember), Target: team}, allowed: true},
		{name: "reviewer agent reads active team scope", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalAgent, RoleReviewer), Target: team}, allowed: true},
		{name: "service reads team only by grant", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalService, RoleMember), Target: team, Grants: GrantFacts{ServiceObjectGrant: true}}, allowed: true},
		{name: "service cannot infer team read", request: AuthorizationRequest{Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalService, RoleMember), Target: team}, allowed: false},

		{name: "owner writes personal", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalHuman, RoleOwner), Target: personal}, allowed: true},
		{name: "member agent writes personal", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalAgent, RoleMember), Target: personal}, allowed: true},
		{name: "reviewer agent writes personal", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalAgent, RoleReviewer), Target: personal}, allowed: true},
		{name: "service never writes personal", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalService, RoleMember), Target: personal, Grants: GrantFacts{ServiceObjectGrant: true}}, allowed: false},

		{name: "owner writes owned project", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalHuman, RoleOwner), Target: project, Grants: GrantFacts{ProjectOwned: true}}, allowed: true},
		{name: "member agent writes assigned project", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalAgent, RoleMember), Target: project, Grants: GrantFacts{ProjectAccess: AccessWrite}}, allowed: true},
		{name: "reviewer agent writes assigned project", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalAgent, RoleReviewer), Target: project, Grants: GrantFacts{ProjectAccess: AccessWrite}}, allowed: true},
		{name: "service project write intersects project and object grants", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalService, RoleMember), Target: &CanonicalScope{TeamID: "team-1", Type: ScopeProject, ID: "project-1", Lifecycle: LifecycleActive, Generation: 1}, Grants: GrantFacts{ProjectAccess: AccessWrite, ServiceObjectGrant: true}}, allowed: true},

		{name: "owner direct team write disabled", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalHuman, RoleOwner), Target: team}, allowed: false},
		{name: "member agent direct team write disabled", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalAgent, RoleMember), Target: team}, allowed: false},
		{name: "reviewer agent direct team write disabled", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalAgent, RoleReviewer), Target: team}, allowed: false},
		{name: "service direct team write disabled despite grants", request: AuthorizationRequest{Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalService, RoleMember), Target: team, Grants: GrantFacts{ProjectAccess: AccessAdmin, ServiceObjectGrant: true}}, allowed: false},

		{name: "owner deletes own personal object", request: AuthorizationRequest{Action: ActionDelete, Capabilities: []Capability{CapabilityDelete}, Actor: activeActor(PrincipalHuman, RoleOwner), Target: personal}, allowed: true},
		{name: "member agent deletes own project object", request: AuthorizationRequest{Action: ActionDelete, Capabilities: []Capability{CapabilityDelete}, Actor: activeActor(PrincipalAgent, RoleMember), Target: project, Grants: GrantFacts{ProjectAccess: AccessWrite}}, allowed: true},
		{name: "reviewer agent deletes own project object", request: AuthorizationRequest{Action: ActionDelete, Capabilities: []Capability{CapabilityDelete}, Actor: activeActor(PrincipalAgent, RoleReviewer), Target: project, Grants: GrantFacts{ProjectAccess: AccessWrite}}, allowed: true},
		{name: "service never deletes", request: AuthorizationRequest{Action: ActionDelete, Capabilities: []Capability{CapabilityDelete}, Actor: activeActor(PrincipalService, RoleMember), Target: personal, Grants: GrantFacts{ServiceObjectGrant: true}}, allowed: false},
		{name: "owner human deletes shared only with approval", request: AuthorizationRequest{Action: ActionDelete, Capabilities: []Capability{CapabilityDelete}, Actor: activeActor(PrincipalHuman, RoleOwner), Target: team, Grants: GrantFacts{OwnerApproval: true}}, allowed: true},
		{name: "owner human deletes reviewed non-owned project with approval", request: AuthorizationRequest{Action: ActionDelete, Capabilities: []Capability{CapabilityDelete}, Actor: activeActor(PrincipalHuman, RoleOwner), Target: scope(ScopeProject, "project-1", "human-other"), Grants: GrantFacts{OwnerApproval: true}}, allowed: true},
		{name: "owner agent cannot use owner approval", request: AuthorizationRequest{Action: ActionDelete, Capabilities: []Capability{CapabilityDelete}, Actor: activeActor(PrincipalAgent, RoleOwner), Target: team, Grants: GrantFacts{OwnerApproval: true}}, allowed: false},

		{name: "owner human inspects permitted audit", request: AuthorizationRequest{Action: ActionAudit, Capabilities: []Capability{CapabilityAudit}, Actor: activeActor(PrincipalHuman, RoleOwner)}, allowed: true},
		{name: "member agent inspects own actions", request: AuthorizationRequest{Action: ActionAudit, Capabilities: []Capability{CapabilityAudit}, Actor: activeActor(PrincipalAgent, RoleMember), Grants: GrantFacts{OwnAudit: true}}, allowed: true},
		{name: "reviewer agent has no elevated audit", request: AuthorizationRequest{Action: ActionAudit, Capabilities: []Capability{CapabilityAudit}, Actor: activeActor(PrincipalAgent, RoleReviewer)}, allowed: false},
		{name: "service has no audit", request: AuthorizationRequest{Action: ActionAudit, Capabilities: []Capability{CapabilityAudit}, Actor: activeActor(PrincipalService, RoleMember), Grants: GrantFacts{OwnAudit: true}}, allowed: false},

		{name: "owner human manages with action approval", request: AuthorizationRequest{Action: ActionManage, Actor: activeActor(PrincipalHuman, RoleOwner), Grants: GrantFacts{OwnerApproval: true}}, allowed: true},
		{name: "owner agent cannot manage", request: AuthorizationRequest{Action: ActionManage, Actor: activeActor(PrincipalAgent, RoleOwner), Grants: GrantFacts{OwnerApproval: true}}, allowed: false},
		{name: "team wipe remains disabled", request: AuthorizationRequest{Action: ActionTeamWipe, Actor: activeActor(PrincipalHuman, RoleOwner), Grants: GrantFacts{OwnerApproval: true}}, allowed: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := Authorize(test.request)
			if decision.Allowed != test.allowed {
				t.Fatalf("Authorize() = %+v, allowed want %v", decision, test.allowed)
			}
		})
	}
}

func TestWriteTargetDefaultsOnlyForHumanDelegation(t *testing.T) {
	for _, kind := range []PrincipalKind{PrincipalHuman, PrincipalAgent} {
		decision := Authorize(AuthorizationRequest{
			Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(kind, RoleMember),
		})
		if !decision.Allowed || decision.EffectiveTarget.Type != ScopePersonal ||
			decision.EffectiveTarget.ID != "human-1" || decision.EffectiveTarget.OwnerPrincipalID != "human-1" {
			t.Fatalf("%s missing target decision = %+v", kind, decision)
		}
	}

	service := Authorize(AuthorizationRequest{
		Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalService, RoleMember),
	})
	if service.Allowed || service.Reason != ReasonExplicitTargetRequired {
		t.Fatalf("service missing target decision = %+v", service)
	}
}

func TestWriteOwnershipIsDerivedAndSpoofedOwnersAreRejected(t *testing.T) {
	for _, kind := range []PrincipalKind{PrincipalHuman, PrincipalAgent} {
		request := AuthorizationRequest{
			Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(kind, RoleMember),
			Target: &CanonicalScope{TeamID: "team-1", Type: ScopeProject, ID: "project-1", Lifecycle: LifecycleActive, Generation: 1},
			Grants: GrantFacts{ProjectAccess: AccessWrite},
		}
		decision := Authorize(request)
		if !decision.Allowed || decision.EffectiveTarget.OwnerPrincipalID != "human-1" {
			t.Fatalf("%s server-derived owner decision = %+v", kind, decision)
		}
		request.Target.OwnerPrincipalID = "spoofed-owner"
		if spoofed := Authorize(request); spoofed.Allowed || spoofed.Reason != ReasonOwnershipRequired {
			t.Fatalf("%s spoofed owner decision = %+v", kind, spoofed)
		}
	}

	service := AuthorizationRequest{
		Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalService, RoleMember),
		Target: &CanonicalScope{TeamID: "team-1", Type: ScopeProject, ID: "project-1", Lifecycle: LifecycleActive, Generation: 1},
		Grants: GrantFacts{ProjectAccess: AccessWrite, ServiceObjectGrant: true},
	}
	decision := Authorize(service)
	if !decision.Allowed || decision.EffectiveTarget.OwnerPrincipalID != "service-1" {
		t.Fatalf("service attribution decision = %+v", decision)
	}
	service.Target.OwnerPrincipalID = "human-1"
	if spoofed := Authorize(service); spoofed.Allowed || spoofed.Reason != ReasonOwnershipRequired {
		t.Fatalf("service spoofed owner decision = %+v", spoofed)
	}
}

func TestAuthoritativeExistingProjectWritePreservesCanonicalOwner(t *testing.T) {
	request := AuthorizationRequest{
		Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(PrincipalAgent, RoleMember),
		Target:              scope(ScopeProject, "project-1", "human-other"),
		TargetAuthoritative: true,
		Grants:              GrantFacts{ProjectAccess: AccessWrite},
	}
	decision := Authorize(request)
	if !decision.Allowed || decision.EffectiveTarget.OwnerPrincipalID != "human-other" {
		t.Fatalf("authoritative project target decision = %+v", decision)
	}

	request.TargetAuthoritative = false
	if spoofed := Authorize(request); spoofed.Allowed || spoofed.Reason != ReasonOwnershipRequired {
		t.Fatalf("caller-proposed foreign owner decision = %+v", spoofed)
	}
}

func TestProjectGrantNeverWidensUnrelatedOwnerScopes(t *testing.T) {
	actor := activeActor(PrincipalAgent, RoleMember)
	foreignRepo := scope(ScopeRepo, "repo-foreign", "human-other")
	for _, action := range []Action{ActionRead, ActionWrite, ActionDelete} {
		decision := Authorize(AuthorizationRequest{
			Action: action, Capabilities: []Capability{RequiredCapability(action)}, Actor: actor,
			Target: foreignRepo, Grants: GrantFacts{ProjectAccess: AccessAdmin},
		})
		if decision.Allowed {
			t.Fatalf("project grant widened %s to foreign repo: %+v", action, decision)
		}
	}

	ownRepo := scope(ScopeRepo, "repo-own", "human-1")
	decision := Authorize(AuthorizationRequest{
		Action: ActionWrite, Capabilities: []Capability{CapabilityWrite}, Actor: actor,
		Target: ownRepo, Grants: GrantFacts{ProjectAccess: AccessRead},
	})
	if !decision.Allowed {
		t.Fatalf("unrelated read-only project grant narrowed owned repo write: %+v", decision)
	}
}

func TestReadsAndDeletesNeverAdoptMissingTeamIdentity(t *testing.T) {
	for _, action := range []Action{ActionRead, ActionDelete} {
		target := scope(ScopePersonal, "human-1", "human-1")
		target.TeamID = ""
		decision := Authorize(AuthorizationRequest{
			Action: action, Capabilities: []Capability{RequiredCapability(action)},
			Actor: activeActor(PrincipalHuman, RoleOwner), Target: target,
		})
		if decision.Allowed || decision.Reason != ReasonConcealedNotFound {
			t.Fatalf("action=%s missing team decision=%+v", action, decision)
		}
	}
}

func TestContextCanNarrowButNeverGrant(t *testing.T) {
	request := AuthorizationRequest{
		Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalAgent, RoleMember),
		Target:  scope(ScopeProject, "project-1", "human-1"),
		Context: ActiveContext{TeamID: "team-1", ProjectID: "project-1"},
	}
	if decision := Authorize(request); decision.Allowed || decision.Reason != ReasonProjectGrantRequired {
		t.Fatalf("matching caller context widened an absent grant: %+v", decision)
	}

	request.Grants.ProjectAccess = AccessRead
	if decision := Authorize(request); !decision.Allowed {
		t.Fatalf("assigned project denied: %+v", decision)
	}
	request.Context.ProjectID = "project-spoofed"
	if decision := Authorize(request); decision.Allowed || decision.Reason != ReasonContextMismatch {
		t.Fatalf("mismatched context did not narrow: %+v", decision)
	}
}

func TestCapabilityIdentityAndEpochAreAllRequired(t *testing.T) {
	base := AuthorizationRequest{
		Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalAgent, RoleMember),
		Target: scope(ScopePersonal, "human-1", "human-1"),
	}
	if decision := Authorize(base); !decision.Allowed {
		t.Fatalf("baseline denied: %+v", decision)
	}

	withoutCapability := base
	withoutCapability.Capabilities = nil
	if decision := Authorize(withoutCapability); decision.Reason != ReasonInsufficientCapability {
		t.Fatalf("missing capability decision = %+v", decision)
	}

	revoked := base
	revoked.Actor.BindingActive = false
	if decision := Authorize(revoked); decision.Reason != ReasonPrincipalRevoked {
		t.Fatalf("revoked binding decision = %+v", decision)
	}

	for _, component := range []struct {
		name string
		bump func(*EpochSnapshot)
	}{
		{name: "global", bump: func(epoch *EpochSnapshot) { epoch.Global++ }},
		{name: "policy", bump: func(epoch *EpochSnapshot) { epoch.Policy++ }},
		{name: "principal", bump: func(epoch *EpochSnapshot) { epoch.Principal++ }},
		{name: "membership", bump: func(epoch *EpochSnapshot) { epoch.Membership++ }},
		{name: "binding", bump: func(epoch *EpochSnapshot) { epoch.Binding++ }},
	} {
		t.Run(component.name, func(t *testing.T) {
			stale := base
			component.bump(&stale.Actor.CurrentEpoch)
			if decision := Authorize(stale); decision.Reason != ReasonEpochChanged {
				t.Fatalf("stale %s epoch decision = %+v", component.name, decision)
			}
		})
	}
}

func TestCrossTeamAndInactiveLifecycleFailClosed(t *testing.T) {
	base := AuthorizationRequest{
		Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalAgent, RoleMember),
		Target: scope(ScopePersonal, "human-1", "human-1"), Context: ActiveContext{TeamID: "team-1"},
	}

	crossTarget := base
	crossTarget.Target = scope(ScopePersonal, "human-1", "human-1")
	crossTarget.Target.TeamID = "team-other"
	if decision := Authorize(crossTarget); decision.Allowed || decision.Reason != ReasonContextMismatch {
		t.Fatalf("cross-team target decision = %+v", decision)
	}

	crossContext := base
	crossContext.Context.TeamID = "team-other"
	if decision := Authorize(crossContext); decision.Allowed || decision.Reason != ReasonContextMismatch {
		t.Fatalf("cross-team context decision = %+v", decision)
	}

	for _, lifecycle := range []LifecycleState{LifecycleTombstoned, LifecycleCleaning, LifecycleCleanupFailed, LifecycleComplete} {
		inactive := base
		copy := *base.Target
		copy.Lifecycle = lifecycle
		copy.Generation = 2
		inactive.Target = &copy
		if decision := Authorize(inactive); decision.Allowed || decision.Reason != ReasonConcealedNotFound {
			t.Fatalf("lifecycle=%s decision=%+v", lifecycle, decision)
		}
	}
}

func TestPrivacyAndRetentionNeverWidenVisibility(t *testing.T) {
	request := AuthorizationRequest{
		Action: ActionRead, Capabilities: []Capability{CapabilityRead}, Actor: activeActor(PrincipalAgent, RoleMember),
		Target: scope(ScopePersonal, "someone-else", "someone-else"),
	}
	for _, privacy := range []string{"normal", "sensitive", "private"} {
		for _, retention := range []string{"session", "project", "long_term"} {
			copy := *request.Target
			copy.PrivacyTier = privacy
			copy.Retention = retention
			request.Target = &copy
			if decision := Authorize(request); decision.Allowed {
				t.Fatalf("privacy=%s retention=%s widened visibility: %+v", privacy, retention, decision)
			}
		}
	}
}

func TestAllPromotionAndDirectTeamWritesAreDisabled(t *testing.T) {
	for _, kind := range []PrincipalKind{PrincipalHuman, PrincipalAgent, PrincipalService} {
		for _, action := range []Action{ActionWrite, ActionPromote} {
			decision := Authorize(AuthorizationRequest{
				Action: action, Capabilities: []Capability{CapabilityWrite}, Actor: activeActor(kind, RoleOwner),
				Target: scope(ScopeTeam, "team-1", ""), Grants: GrantFacts{ProjectAccess: AccessAdmin, ServiceObjectGrant: true, OwnerApproval: true},
			})
			if decision.Allowed || decision.Reason != ReasonScopePromotionDisabled {
				t.Fatalf("kind=%s action=%s decision=%+v", kind, action, decision)
			}
		}
	}
}
