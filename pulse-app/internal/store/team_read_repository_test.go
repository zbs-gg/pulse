package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

func TestAuthorizedTeamMemoryRepositoryFiltersParentBeforeMatchOrderAndLimit(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "read-member", Role: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "read-member", ClientID: "read-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	visibleProject, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Visible project")
	if err != nil {
		t.Fatal(err)
	}
	hiddenProject, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Hidden project")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID, ProjectID: visibleProject.ProjectID,
		TargetPrincipalID: binding.AgentPrincipalID, AccessLevel: "read",
	})
	if err != nil {
		t.Fatal(err)
	}

	insertTeamReadMemoryFixture(t, s, bootstrap, teamReadMemoryFixture{
		rootID: "read-visible", capsuleID: "capsule-visible", ownerID: member.PrincipalID,
		scopeType: "project", scopeID: visibleProject.ProjectID,
		summary: "needle visible authorized", confidence: 0.10,
	})
	insertTeamReadMemoryFixture(t, s, bootstrap, teamReadMemoryFixture{
		rootID: "read-hidden", capsuleID: "capsule-hidden", ownerID: bootstrap.OwnerPrincipalID,
		scopeType: "project", scopeID: hiddenProject.ProjectID,
		summary: "needle hidden highest match", confidence: 1.0,
	})
	insertTeamReadMemoryFixture(t, s, bootstrap, teamReadMemoryFixture{
		rootID: "read-other-personal", capsuleID: "capsule-other", ownerID: member.PrincipalID,
		scopeType: "personal", scopeID: member.PrincipalID,
		summary: "unrelated authorized", confidence: 0.9,
	})
	var localBefore int
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&localBefore); err != nil {
		t.Fatal(err)
	}

	filter, err := s.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
		PrincipalID:  binding.AgentPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context: teamauth.ActiveContext{
			TeamID: bootstrap.TeamID, ProjectID: visibleProject.ProjectID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryAuthorizedTeamMemoryCapsules(ctx, filter, TeamTextReadQuery{
		Match: "needle", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RootObjectID != "read-visible" ||
		rows[0].CapsuleID != "capsule-visible" || rows[0].RedactedSummary != "needle visible authorized" ||
		rows[0].PartitionKey == "" || rows[0].PrivacyTier != "normal" || rows[0].Retention != "project" ||
		!reflect.DeepEqual(rows[0].Tags, []string{"read-fixture"}) {
		t.Fatalf("authorized memory rows = %+v", rows)
	}
	var localAfter int
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&localAfter); err != nil {
		t.Fatal(err)
	}
	if localAfter != localBefore {
		t.Fatalf("team read touched local memory table: %d -> %d", localBefore, localAfter)
	}

	if err := s.RevokeProjectGrant(ctx, bootstrap.OwnerPrincipalID, grant.GrantID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecheckAuthorizedCandidateRoots(ctx, filter, []string{"read-visible"}); !errors.Is(err, ErrTeamPolicyEpochChanged) && !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("project revoke after candidate load = %v, want stale authorization", err)
	}
}

func TestAuthorizedTeamMemoryEmbeddingRepositoryUsesOnlyTeamProjectionTables(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamMemoryProjectionFixture(t, nil)
	defer fixture.object.store.Close()
	claim := fixture.claim(t, "embedding")
	results := make([]TeamMemoryEmbeddingResult, 0, len(fixture.memory.CapsuleIDs))
	for index, capsuleID := range fixture.memory.CapsuleIDs {
		results = append(results, TeamMemoryEmbeddingResult{
			CapsuleID: capsuleID,
			Vector:    []float32{float32(index) + 0.1, float32(index) + 0.2},
		})
	}
	request := TeamMemoryEmbeddingProjectionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		JobID: claim.JobID, LeaseToken: claim.LeaseToken,
		Model: "read_memory_v1", Results: results,
	}
	if _, err := fixture.object.store.CompleteTeamMemoryEmbeddingProjection(ctx, request); err != nil {
		t.Fatal(err)
	}
	var localEventsBefore, localEmbeddingsBefore int
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM events`).Scan(&localEventsBefore); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM event_embeddings`).Scan(&localEmbeddingsBefore); err != nil {
		t.Fatal(err)
	}
	filter, err := fixture.object.store.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
		PrincipalID:  fixture.object.actor.binding.AgentPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context:      teamauth.ActiveContext{TeamID: fixture.object.bootstrap.TeamID},
	})
	if err != nil {
		t.Fatal(err)
	}
	var selectedCapsuleID, selectedSummary string
	if err := fixture.object.store.DB().QueryRow(`
		SELECT capsule_id, redacted_summary
		  FROM team_memory_capsules
		 WHERE root_object_id = ?
		 ORDER BY capsule_id DESC LIMIT 1`, fixture.memory.ObjectID).
		Scan(&selectedCapsuleID, &selectedSummary); err != nil {
		t.Fatal(err)
	}
	selected, err := fixture.object.store.QueryAuthorizedTeamMemoryCapsules(
		ctx, filter, TeamTextReadQuery{Match: selectedSummary, Limit: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].CapsuleID != selectedCapsuleID {
		t.Fatalf("selected recall capsule = %+v, want %s", selected, selectedCapsuleID)
	}
	rows, err := fixture.object.store.QueryAuthorizedTeamMemoryEmbeddings(
		ctx, filter, TeamMemoryEmbeddingReadQuery{
			Model: request.Model,
			Capsules: []TeamMemoryCapsuleReadKey{{
				RootObjectID: selected[0].RootObjectID, CapsuleID: selected[0].CapsuleID,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RootObjectID != fixture.memory.ObjectID ||
		rows[0].CapsuleID != selectedCapsuleID || rows[0].DerivativeObjectID == "" ||
		rows[0].EmbeddingID == "" || rows[0].PartitionKey == "" ||
		rows[0].PrivacyTier != "normal" || len(rows[0].Vector) != 2 {
		t.Fatalf("authorized memory embeddings = %+v", rows)
	}
	var localEventsAfter, localEmbeddingsAfter int
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM events`).Scan(&localEventsAfter); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM event_embeddings`).Scan(&localEmbeddingsAfter); err != nil {
		t.Fatal(err)
	}
	if localEventsAfter != localEventsBefore || localEmbeddingsAfter != localEmbeddingsBefore {
		t.Fatalf("team repository touched local projections: events %d->%d embeddings %d->%d",
			localEventsBefore, localEventsAfter, localEmbeddingsBefore, localEmbeddingsAfter)
	}
}

func TestAuthorizedTeamMemoryRepositoryRequiresServiceGrantForExactParentKind(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	service, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", ClientID: "read-service",
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Service read project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
		TargetPrincipalID: service.PrincipalID, AccessLevel: "read",
	}); err != nil {
		t.Fatal(err)
	}
	insertTeamReadMemoryFixture(t, s, bootstrap, teamReadMemoryFixture{
		rootID: "service-memory", capsuleID: "service-capsule", ownerID: service.PrincipalID,
		scopeType: "project", scopeID: project.ProjectID,
		summary: "service exact parent kind", confidence: 0.8,
	})
	insertServiceReadGrant := func(id, objectKind string) {
		t.Helper()
		if _, err := s.DB().Exec(`
			INSERT INTO team_service_object_grants(
				grant_id, team_id, service_principal_id, object_kind, action,
				scope_type, scope_id, status, auth_epoch, created_at)
			VALUES (?, ?, ?, ?, 'read', 'project', ?, 'active', 1,
				'2026-07-11T00:00:00Z')`,
			id, bootstrap.TeamID, service.PrincipalID, objectKind, project.ProjectID,
		); err != nil {
			t.Fatal(err)
		}
	}
	insertServiceReadGrant("service-wrong-kind", "event")
	buildFilter := func() AuthorizedCandidateFilter {
		t.Helper()
		filter, err := s.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
			PrincipalID:  service.PrincipalID,
			Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
			Context: teamauth.ActiveContext{
				TeamID: bootstrap.TeamID, ProjectID: project.ProjectID,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return filter
	}
	rows, err := s.QueryAuthorizedTeamMemoryCapsules(ctx, buildFilter(), TeamTextReadQuery{Match: "service", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("wrong derivative-kind grant exposed parent memory: %+v", rows)
	}
	insertServiceReadGrant("service-memory-kind", "memory")
	rows, err = s.QueryAuthorizedTeamMemoryCapsules(ctx, buildFilter(), TeamTextReadQuery{Match: "service", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RootObjectID != "service-memory" {
		t.Fatalf("exact memory parent grant rows = %+v", rows)
	}
}

func TestAuthorizedTeamSemanticRepositoriesReturnOnlyActiveParentContributions(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()

	completeRoot := func(root TeamObjectWriteResult) {
		t.Helper()
		rootFixture := &teamSemanticProjectionFixture{graph: fixture.graph, root: root}
		for _, kind := range []string{"graph", "claim", "continuity"} {
			claim := rootFixture.claim(t, root.ObjectID, kind)
			completeStructuredProjection(t, rootFixture, kind, semanticProjectionRequest(rootFixture, claim))
		}
		claim := rootFixture.claim(t, root.ObjectID, "embedding")
		request := teamSemanticEmbeddingRequest(t, rootFixture, claim, 3)
		if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(ctx, request); err != nil {
			t.Fatalf("complete embedding for %s: %v", root.ObjectID, err)
		}
	}
	completeRoot(fixture.root)
	secondWrite := baseTeamGraphDeltaWrite()
	secondWrite.IdempotencyKey = "graph-read-second"
	secondRoot := storeTeamSemanticProjectionRoot(t, fixture.graph, secondWrite, "read-second")
	completeRoot(secondRoot)

	filter, err := fixture.graph.object.store.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
		PrincipalID:  fixture.graph.object.actor.binding.AgentPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context: teamauth.ActiveContext{
			TeamID: fixture.graph.object.bootstrap.TeamID, ProjectID: "project-pulse",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	graphRows, err := fixture.graph.object.store.QueryAuthorizedTeamGraphContributions(
		ctx, filter, TeamTextReadQuery{Match: "lisbon", Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	var factContribution *TeamAuthorizedGraphContribution
	for index := range graphRows {
		if graphRows[index].Fact != nil {
			factContribution = &graphRows[index]
			break
		}
	}
	if factContribution == nil || factContribution.RootObjectID == "" ||
		factContribution.DerivativeObjectID == "" || factContribution.IntentID == "" ||
		factContribution.PartitionKey == "" || len(factContribution.ResolvedRefs) == 0 ||
		factContribution.VisibleContributionCount != 2 {
		t.Fatalf("authorized graph contributions = %+v", graphRows)
	}
	assertions, err := fixture.graph.object.store.QueryAuthorizedTeamAssertionContributions(
		ctx, filter, TeamTextReadQuery{Match: "lisbon", Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(assertions) != 2 || assertions[0].Claim.Text == "" || assertions[0].ClaimSlotDigest == "" ||
		assertions[0].SourceGraphDerivativeObjectID == "" ||
		assertions[0].VisibleContributionCount != 2 {
		t.Fatalf("authorized assertion contributions = %+v", assertions)
	}
	graphFactByRoot := make(map[string]string)
	for _, contribution := range graphRows {
		if contribution.Fact != nil {
			graphFactByRoot[contribution.RootObjectID] = contribution.DerivativeObjectID
		}
	}
	for _, assertion := range assertions {
		if assertion.SourceGraphDerivativeObjectID != graphFactByRoot[assertion.RootObjectID] {
			t.Fatalf("assertion source graph derivative = %q for root %q, want %q",
				assertion.SourceGraphDerivativeObjectID, assertion.RootObjectID,
				graphFactByRoot[assertion.RootObjectID])
		}
	}
	continuity, err := fixture.graph.object.store.QueryAuthorizedTeamContinuityCheckpoints(
		ctx, filter, TeamTextReadQuery{Match: "scoped team storage", Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(continuity) != 2 || continuity[0].Checkpoint.ThreadID == "" ||
		continuity[0].VisibleContributionCount != 2 {
		t.Fatalf("authorized continuity contributions = %+v", continuity)
	}
	semanticSources := make([]TeamSemanticEmbeddingReadKey, len(graphRows))
	for index, row := range graphRows {
		semanticSources[index] = TeamSemanticEmbeddingReadKey{
			RootObjectID: row.RootObjectID, SourceGraphDerivativeObjectID: row.DerivativeObjectID,
		}
	}
	embeddings, err := fixture.graph.object.store.QueryAuthorizedTeamSemanticEmbeddings(
		ctx, filter, TeamSemanticEmbeddingReadQuery{
			Model: teamSemanticEmbeddingTestModel, Sources: semanticSources,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(embeddings) == 0 || embeddings[0].RootObjectID == "" ||
		embeddings[0].SourceGraphDerivativeObjectID == "" || embeddings[0].SourceKind == "" ||
		len(embeddings[0].Vector) != 3 || embeddings[0].VisibleContributionCount != 2 {
		t.Fatalf("authorized semantic embeddings = %+v", embeddings)
	}

	if _, err := fixture.graph.object.store.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1
		 WHERE object_id = ?`, secondRoot.ObjectID); err != nil {
		t.Fatal(err)
	}
	assertions, err = fixture.graph.object.store.QueryAuthorizedTeamAssertionContributions(
		ctx, filter, TeamTextReadQuery{Match: "lisbon", Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(assertions) != 1 || assertions[0].RootObjectID != fixture.root.ObjectID ||
		assertions[0].VisibleContributionCount != 1 {
		t.Fatalf("tombstoned shared-parent assertion rows = %+v", assertions)
	}

	fixture.graph.object.store.clock = func() time.Time {
		return time.Date(2026, 8, 1, 7, 1, 0, 0, time.FixedZone("GMT+7", 7*60*60))
	}
	assertions, err = fixture.graph.object.store.QueryAuthorizedTeamAssertionContributions(
		ctx, filter, TeamTextReadQuery{Match: "lisbon", Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(assertions) != 0 {
		t.Fatalf("expired parent influenced assertion rows: %+v", assertions)
	}
}

func TestAuthorizedTeamSemanticContributionLimitsSelectDerivativesBeforeMergingRoots(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	baseTime := fixture.graph.object.store.clock()

	completeRoot := func(root TeamObjectWriteResult) {
		t.Helper()
		rootFixture := &teamSemanticProjectionFixture{graph: fixture.graph, root: root}
		for _, kind := range []string{"graph", "claim", "continuity"} {
			claim := rootFixture.claim(t, root.ObjectID, kind)
			completeStructuredProjection(
				t, rootFixture, kind, semanticProjectionRequest(rootFixture, claim),
			)
		}
		embeddingClaim := rootFixture.claim(t, root.ObjectID, "embedding")
		if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
			ctx, teamSemanticEmbeddingRequest(t, rootFixture, embeddingClaim, 3),
		); err != nil {
			t.Fatalf("complete embedding for %s: %v", root.ObjectID, err)
		}
	}
	// Clear the fixture root's pending jobs. Its payload deliberately does not
	// match the starvation marker and therefore cannot affect selection below.
	completeRoot(fixture.root)

	fixture.graph.object.store.clock = func() time.Time { return baseTime.Add(10 * time.Second) }
	uniqueWrite := semanticReadStarvationWrite("unique", "semantic-read-starvation-unique")
	uniqueRoot := storeTeamSemanticProjectionRoot(t, fixture.graph, uniqueWrite, "starvation-unique")
	completeRoot(uniqueRoot)

	fixture.graph.object.store.clock = func() time.Time { return baseTime.Add(20 * time.Second) }
	sharedRoots := make([]TeamObjectWriteResult, 0, 3)
	for index := 0; index < 3; index++ {
		write := semanticReadStarvationWrite(
			"shared", "semantic-read-starvation-shared-"+string(rune('a'+index)),
		)
		if index == 2 {
			// Continuity derivatives converge on thread/session, so this
			// non-matching contribution must return once its derivative is
			// selected by the other matching roots.
			write.Continuity.Summary = "Shared continuity contribution without the search marker."
		}
		root := storeTeamSemanticProjectionRoot(
			t, fixture.graph, write, "starvation-shared-"+string(rune('a'+index)),
		)
		completeRoot(root)
		sharedRoots = append(sharedRoots, root)
	}

	filter, err := fixture.graph.object.store.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
		PrincipalID:  fixture.graph.object.actor.binding.AgentPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context: teamauth.ActiveContext{
			TeamID: fixture.graph.object.bootstrap.TeamID, ProjectID: "project-pulse",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		query func() ([]semanticReadContributionIdentity, error)
	}{
		{
			name: "graph",
			query: func() ([]semanticReadContributionIdentity, error) {
				rows, err := fixture.graph.object.store.QueryAuthorizedTeamGraphContributions(
					ctx, filter, TeamTextReadQuery{Match: "starvation", Limit: 2},
				)
				result := make([]semanticReadContributionIdentity, len(rows))
				for index, row := range rows {
					result[index] = semanticReadContributionIdentity{
						RootObjectID: row.RootObjectID, DerivativeObjectID: row.DerivativeObjectID,
						VisibleContributionCount: row.VisibleContributionCount,
					}
				}
				return result, err
			},
		},
		{
			name: "assertion",
			query: func() ([]semanticReadContributionIdentity, error) {
				rows, err := fixture.graph.object.store.QueryAuthorizedTeamAssertionContributions(
					ctx, filter, TeamTextReadQuery{Match: "starvation", Limit: 2},
				)
				result := make([]semanticReadContributionIdentity, len(rows))
				for index, row := range rows {
					result[index] = semanticReadContributionIdentity{
						RootObjectID: row.RootObjectID, DerivativeObjectID: row.DerivativeObjectID,
						VisibleContributionCount: row.VisibleContributionCount,
					}
				}
				return result, err
			},
		},
		{
			name: "continuity",
			query: func() ([]semanticReadContributionIdentity, error) {
				rows, err := fixture.graph.object.store.QueryAuthorizedTeamContinuityCheckpoints(
					ctx, filter, TeamTextReadQuery{Match: "starvation", Limit: 2},
				)
				result := make([]semanticReadContributionIdentity, len(rows))
				for index, row := range rows {
					result[index] = semanticReadContributionIdentity{
						RootObjectID: row.RootObjectID, DerivativeObjectID: row.DerivativeObjectID,
						VisibleContributionCount: row.VisibleContributionCount,
					}
				}
				return result, err
			},
		},
	}
	sharedRootIDs := make(map[string]struct{}, len(sharedRoots))
	for _, root := range sharedRoots {
		sharedRootIDs[root.ObjectID] = struct{}{}
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows, err := test.query()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 4 {
				t.Fatalf("contribution rows = %+v, want three shared plus one unique", rows)
			}
			derivatives := make(map[string]int)
			seenSharedRoots := make(map[string]struct{})
			for _, row := range rows {
				derivatives[row.DerivativeObjectID]++
				if _, shared := sharedRootIDs[row.RootObjectID]; shared {
					seenSharedRoots[row.RootObjectID] = struct{}{}
					if row.VisibleContributionCount != 3 {
						t.Fatalf("shared visible count = %d, want 3", row.VisibleContributionCount)
					}
				} else if row.RootObjectID == uniqueRoot.ObjectID {
					if row.VisibleContributionCount != 1 {
						t.Fatalf("unique visible count = %d, want 1", row.VisibleContributionCount)
					}
				} else {
					t.Fatalf("unexpected contribution root %q", row.RootObjectID)
				}
			}
			if len(derivatives) != 2 || len(seenSharedRoots) != 3 {
				t.Fatalf("selected derivatives/roots = derivatives:%v shared:%v", derivatives, seenSharedRoots)
			}
		})
	}

	selectedGraphRows, err := fixture.graph.object.store.QueryAuthorizedTeamGraphContributions(
		ctx, filter, TeamTextReadQuery{Match: "starvation", Limit: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(selectedGraphRows) != 3 {
		t.Fatalf("selected graph source contributions = %+v, want shared derivative roots", selectedGraphRows)
	}
	sources := make([]TeamSemanticEmbeddingReadKey, len(selectedGraphRows))
	wantSources := make(map[string]struct{}, len(selectedGraphRows))
	for index, row := range selectedGraphRows {
		sources[index] = TeamSemanticEmbeddingReadKey{
			RootObjectID: row.RootObjectID, SourceGraphDerivativeObjectID: row.DerivativeObjectID,
		}
		wantSources[row.RootObjectID+"\x00"+row.DerivativeObjectID] = struct{}{}
	}
	embeddings, err := fixture.graph.object.store.QueryAuthorizedTeamSemanticEmbeddings(
		ctx, filter, TeamSemanticEmbeddingReadQuery{
			Model: teamSemanticEmbeddingTestModel, Sources: sources,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(embeddings) != 3 {
		t.Fatalf("exact selected-source embeddings = %+v, want three shared roots", embeddings)
	}
	for _, embedding := range embeddings {
		key := embedding.RootObjectID + "\x00" + embedding.SourceGraphDerivativeObjectID
		if _, selected := wantSources[key]; !selected || embedding.VisibleContributionCount != 3 {
			t.Fatalf("unselected graph source embedding returned: %+v", embedding)
		}
	}
}

type semanticReadContributionIdentity struct {
	RootObjectID             string
	DerivativeObjectID       string
	VisibleContributionCount int
}

func TestNormalizeTeamSemanticEmbeddingReadQueryBoundsAndRejectsDuplicateSources(t *testing.T) {
	tooMany := make([]TeamSemanticEmbeddingReadKey, MaxTeamSemanticEmbeddingReadSources+1)
	for index := range tooMany {
		tooMany[index] = TeamSemanticEmbeddingReadKey{
			RootObjectID:                  "root-bound-" + string(rune(0x1000+index)),
			SourceGraphDerivativeObjectID: "source-bound-" + string(rune(0x5000+index)),
		}
	}
	if _, _, err := normalizeTeamSemanticEmbeddingReadQuery(TeamSemanticEmbeddingReadQuery{
		Model: teamSemanticEmbeddingTestModel, Sources: tooMany,
	}); !errors.Is(err, ErrInvalidTeamReadQuery) {
		t.Fatalf("over-bound semantic sources = %v, want invalid query", err)
	}
	duplicate := TeamSemanticEmbeddingReadKey{
		RootObjectID: "root-duplicate", SourceGraphDerivativeObjectID: "source-duplicate",
	}
	if _, _, err := normalizeTeamSemanticEmbeddingReadQuery(TeamSemanticEmbeddingReadQuery{
		Model: teamSemanticEmbeddingTestModel,
		Sources: []TeamSemanticEmbeddingReadKey{
			duplicate, duplicate,
		},
	}); !errors.Is(err, ErrInvalidTeamReadQuery) {
		t.Fatalf("duplicate semantic sources = %v, want invalid query", err)
	}
}

func semanticReadStarvationWrite(kind, idempotencyKey string) TeamGraphDeltaWrite {
	write := baseTeamGraphDeltaWrite()
	write.Facts[0].Text = "Starvation " + kind + " fact."
	write.Facts[0].Predicate = graphString("starvation_" + kind)
	write.Facts[0].ObjectText = graphString(kind)
	write.Continuity.ThreadID = kind + "-thread"
	write.Continuity.Summary = "Starvation " + kind + " continuity."
	write.IdempotencyKey = idempotencyKey
	return write
}

func TestAuthorizedTeamSemanticEmbeddingsRequireReadySourceGraphMaterialization(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	embeddingClaim := fixture.claim(t, fixture.root.ObjectID, "embedding")
	embeddingRequest := teamSemanticEmbeddingRequest(t, fixture, embeddingClaim, 3)
	if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
		ctx, embeddingRequest,
	); err != nil {
		t.Fatal(err)
	}
	filter, err := fixture.graph.object.store.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
		PrincipalID:  fixture.graph.object.actor.binding.AgentPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context: teamauth.ActiveContext{
			TeamID: fixture.graph.object.bootstrap.TeamID, ProjectID: "project-pulse",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sourceGraphDerivativeObjectID string
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT derivative_object_id
		  FROM team_semantic_projection_intents
		 WHERE root_object_id = ? AND projection_kind = 'graph'
		 ORDER BY source_kind, source_ordinal
		 LIMIT 1`, fixture.root.ObjectID).Scan(&sourceGraphDerivativeObjectID); err != nil {
		t.Fatal(err)
	}
	semanticQuery := TeamSemanticEmbeddingReadQuery{
		Model: teamSemanticEmbeddingTestModel,
		Sources: []TeamSemanticEmbeddingReadKey{{
			RootObjectID:                  fixture.root.ObjectID,
			SourceGraphDerivativeObjectID: sourceGraphDerivativeObjectID,
		}},
	}
	rows, err := fixture.graph.object.store.QueryAuthorizedTeamSemanticEmbeddings(
		ctx, filter, semanticQuery,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("embedding-ready/graph-pending row consumed limit: %+v", rows)
	}

	graphClaim := fixture.claim(t, fixture.root.ObjectID, "graph")
	completeStructuredProjection(t, fixture, "graph", semanticProjectionRequest(fixture, graphClaim))
	rows, err = fixture.graph.object.store.QueryAuthorizedTeamSemanticEmbeddings(
		ctx, filter, semanticQuery,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SourceGraphDerivativeObjectID == "" {
		t.Fatalf("ready source graph embedding row = %+v", rows)
	}
}

type teamReadMemoryFixture struct {
	rootID, capsuleID, ownerID string
	scopeType, scopeID         string
	summary                    string
	confidence                 float64
}

func insertTeamReadMemoryFixture(
	t *testing.T,
	s *Store,
	bootstrap BootstrapResult,
	fixture teamReadMemoryFixture,
) {
	t.Helper()
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, created_at, updated_at)
		VALUES (?, ?, ?, 'memory', ?, ?, ?, ?, 'normal', 'project',
			'active', 1, '2026-07-11T00:00:00Z', '2026-07-11T00:00:00Z')`,
		fixture.rootID, bootstrap.StoreID, bootstrap.TeamID, fixture.scopeType,
		fixture.scopeID, fixture.ownerID, fixture.ownerID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_memory_capsules(
			capsule_id, root_object_id, team_id, scope_type, scope_id,
			root_generation, item_ordinal, schema_version, source_host,
			conversation_scope, source_timestamp, kind, redacted_summary,
			confidence, evidence_hint, tags_json, created_at)
		VALUES (?, ?, ?, ?, ?, 1, 0, 'pulse.team.memory.v1', 'codex',
			'current_turn', '2026-07-11T12:00:00+07:00', 'fact', ?, ?,
			'synthetic', '["read-fixture"]', '2026-07-11T05:00:00Z')`,
		fixture.capsuleID, fixture.rootID, bootstrap.TeamID, fixture.scopeType,
		fixture.scopeID, fixture.summary, fixture.confidence,
	); err != nil {
		t.Fatal(err)
	}
}
