package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

func TestMigrations034Through039InstallTeamObjectSemanticDeletionAndOwnerSchema(t *testing.T) {
	if teamauth.SchemaVersion != 44 {
		t.Fatalf("teamauth.SchemaVersion = %d, want 44", teamauth.SchemaVersion)
	}
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	if latest := migrations[len(migrations)-1]; latest.Version != 45 || latest.Name != "045_memory_presentation_receipts.sql" {
		t.Fatalf("latest migration = %+v, want 045_memory_presentation_receipts.sql", latest)
	}

	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	wantTables := []string{
		"team_assertion_materializations",
		"team_audit_event_order",
		"team_continuity_materializations",
		"team_deletion_discharges",
		"team_deletion_frontier",
		"team_deletion_operations",
		"team_owner_approvals",
		"team_remote_activation",
		"team_graph_delta_inputs",
		"team_graph_materializations",
		"team_idempotency_records",
		"team_memory_capsules",
		"team_memory_embeddings",
		"team_memory_events",
		"team_object_contributions",
		"team_object_registry",
		"team_object_storage_map",
		"team_policy_metadata",
		"team_projection_jobs",
		"team_projection_outputs",
		"team_semantic_embeddings",
		"team_semantic_materializations",
		"team_semantic_projection_intents",
		"team_service_object_grants",
		"team_writer_leases",
	}
	sort.Strings(wantTables)
	var gotTables []string
	rows, err := s.DB().Query(`
		SELECT name FROM sqlite_master
			 WHERE type = 'table' AND name IN (
			'team_audit_event_order', 'team_idempotency_records', 'team_memory_capsules',
			'team_memory_embeddings', 'team_memory_events', 'team_graph_delta_inputs',
			'team_deletion_operations', 'team_deletion_frontier', 'team_deletion_discharges',
			'team_owner_approvals', 'team_remote_activation',
			'team_object_contributions',
			'team_object_registry', 'team_object_storage_map',
			'team_policy_metadata', 'team_projection_jobs',
			'team_projection_outputs', 'team_semantic_projection_intents',
			'team_semantic_materializations', 'team_graph_materializations',
			'team_assertion_materializations', 'team_continuity_materializations',
			'team_semantic_embeddings',
			'team_service_object_grants', 'team_writer_leases')
		 ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		gotTables = append(gotTables, table)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotTables, wantTables) {
		t.Fatalf("policy tables = %v, want %v", gotTables, wantTables)
	}

	var storeID, teamID string
	var policyVersion, schemaVersion int
	var policyEpoch, globalEpoch int64
	if err := s.DB().QueryRow(`
		SELECT store_id, team_id, policy_version, schema_version, policy_epoch, global_epoch
		  FROM team_policy_metadata`).Scan(
		&storeID, &teamID, &policyVersion, &schemaVersion, &policyEpoch, &globalEpoch,
	); err != nil {
		t.Fatal(err)
	}
	if storeID != bootstrap.StoreID || teamID != bootstrap.TeamID ||
		policyVersion != teamauth.PolicyVersion || schemaVersion != teamauth.SchemaVersion ||
		policyEpoch != 1 || globalEpoch != bootstrap.AuthEpoch {
		t.Fatalf("policy metadata = store=%q team=%q policy=%d schema=%d policy_epoch=%d global_epoch=%d", storeID, teamID, policyVersion, schemaVersion, policyEpoch, globalEpoch)
	}
}

func TestSecurityEventAggregateClassificationColumnsAreBounded(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	insert := func(eventID, methodClass, pathClass string, count int) error {
		_, err := s.DB().Exec(`
			INSERT INTO team_security_events(
				event_id, store_id, occurred_at, event_type, outcome, team_id,
				policy_version, mode, reason_code, metadata_json,
				method_class, path_class, aggregate_count)
			VALUES (?, ?, '2026-07-10T00:00:00Z', 'authentication_denied', 'denied', ?,
				1, 'team-remote', 'expired_credential', '{}', ?, ?, ?)`,
			eventID, bootstrap.StoreID, bootstrap.TeamID, methodClass, pathClass, count)
		return err
	}
	if err := insert("aggregate-ok", "write", "mcp", 256); err != nil {
		t.Fatalf("insert classified aggregate: %v", err)
	}
	var methodClass, pathClass string
	var count int
	if err := s.DB().QueryRow(`
		SELECT method_class, path_class, aggregate_count
		  FROM team_security_events WHERE event_id = 'aggregate-ok'`).Scan(&methodClass, &pathClass, &count); err != nil {
		t.Fatal(err)
	}
	if methodClass != "write" || pathClass != "mcp" || count != 256 {
		t.Fatalf("stored aggregate = method=%q path=%q count=%d", methodClass, pathClass, count)
	}
	for _, test := range []struct {
		id, method, path string
		count            int
	}{
		{id: "bad-method", method: "POST /secret", path: "mcp", count: 1},
		{id: "bad-path", method: "write", path: "/team/private", count: 1},
		{id: "zero-count", method: "write", path: "mcp", count: 0},
		{id: "large-count", method: "write", path: "mcp", count: 1001},
	} {
		if err := insert(test.id, test.method, test.path, test.count); err == nil {
			t.Fatalf("accepted invalid security aggregate %+v", test)
		}
	}
}

func insertPolicyObject(t *testing.T, s *Store, bootstrap BootstrapResult, objectID, scopeType, scopeID, ownerID string) {
	t.Helper()
	authorID := ownerID
	if authorID == "" {
		authorID = bootstrap.OwnerPrincipalID
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, created_at, updated_at)
		VALUES (?, ?, ?, 'memory', ?, ?, NULLIF(?, ''), ?, 'normal', 'long_term',
			'active', 1, '2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z')`,
		objectID, bootstrap.StoreID, bootstrap.TeamID, scopeType, scopeID, ownerID, authorID,
	); err != nil {
		t.Fatalf("insert object %s: %v", objectID, err)
	}
}

func TestCanonicalRegistryScopeGenerationAndLifecycleAreGuarded(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()

	if _, err := s.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, created_at, updated_at)
		VALUES ('missing-scope', ?, ?, 'memory', '', '', ?, ?, 'normal', 'long_term',
			'active', 1, '2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z')`,
		bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID,
	); err == nil {
		t.Fatal("registry accepted a missing canonical scope")
	}

	insertPolicyObject(t, s, bootstrap, "personal-root", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	for name, update := range map[string]string{
		"scope type":   `UPDATE team_object_registry SET scope_type = 'team' WHERE object_id = 'personal-root'`,
		"scope id":     `UPDATE team_object_registry SET scope_id = ? WHERE object_id = 'personal-root'`,
		"owner":        `UPDATE team_object_registry SET owner_principal_id = NULL WHERE object_id = 'personal-root'`,
		"generation":   `UPDATE team_object_registry SET generation = 0 WHERE object_id = 'personal-root'`,
		"reactivation": `UPDATE team_object_registry SET lifecycle = 'complete' WHERE object_id = 'personal-root'; UPDATE team_object_registry SET lifecycle = 'active' WHERE object_id = 'personal-root'`,
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			if strings.Contains(update, "?") {
				_, err = s.DB().Exec(update, bootstrap.TeamID)
			} else {
				_, err = s.DB().Exec(update)
			}
			if err == nil {
				t.Fatalf("registry accepted forbidden %s update", name)
			}
		})
	}
}

func TestContributionLineageRejectsOrphansCrossBoundaryDuplicatesAndCycles(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Policy project")
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"a", "b", "c"} {
		insertPolicyObject(t, s, bootstrap, id, "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	}
	insertPolicyObject(t, s, bootstrap, "project-object", "project", project.ProjectID, bootstrap.OwnerPrincipalID)

	insertContribution := func(parent, derivative, teamID, scopeType, scopeID string) error {
		_, err := s.DB().Exec(`
			INSERT INTO team_object_contributions(
				parent_object_id, derivative_object_id, team_id, scope_type, scope_id,
				parent_generation, derivative_generation, created_at)
			VALUES (?, ?, ?, ?, ?, 1, 1, '2026-07-10T00:00:00Z')`,
			parent, derivative, teamID, scopeType, scopeID)
		return err
	}
	if err := insertContribution("missing", "a", bootstrap.TeamID, "personal", bootstrap.OwnerPrincipalID); err == nil {
		t.Fatal("contribution accepted an orphan parent")
	}
	if err := insertContribution("a", "missing", bootstrap.TeamID, "personal", bootstrap.OwnerPrincipalID); err == nil {
		t.Fatal("contribution accepted a derivative without registry identity")
	}
	if err := insertContribution("a", "project-object", bootstrap.TeamID, "personal", bootstrap.OwnerPrincipalID); err == nil {
		t.Fatal("contribution crossed canonical scopes")
	}
	if err := insertContribution("a", "b", "team-other", "personal", bootstrap.OwnerPrincipalID); err == nil {
		t.Fatal("contribution crossed teams")
	}
	if err := insertContribution("a", "b", bootstrap.TeamID, "personal", bootstrap.OwnerPrincipalID); err != nil {
		t.Fatalf("insert same-scope contribution: %v", err)
	}
	if err := insertContribution("a", "b", bootstrap.TeamID, "personal", bootstrap.OwnerPrincipalID); err == nil {
		t.Fatal("duplicate contribution was accepted")
	}
	if err := insertContribution("b", "c", bootstrap.TeamID, "personal", bootstrap.OwnerPrincipalID); err != nil {
		t.Fatalf("insert second same-scope contribution: %v", err)
	}
	if err := insertContribution("c", "a", bootstrap.TeamID, "personal", bootstrap.OwnerPrincipalID); err == nil {
		t.Fatal("cyclic contribution was accepted")
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_contributions
		   SET derivative_object_id = 'a'
		 WHERE parent_object_id = 'b' AND derivative_object_id = 'c'`); err == nil {
		t.Fatal("contribution identity update introduced a cycle")
	}
}

func TestGenerationFenceAllowsTombstoneWithOldAttachmentsButRejectsLateAttachment(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	for _, id := range []string{"root", "derivative", "late-derivative"} {
		insertPolicyObject(t, s, bootstrap, id, "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_storage_map(
			object_id, team_id, scope_type, scope_id, generation,
			representation_kind, storage_key, created_at)
		VALUES ('root', ?, 'personal', ?, 1, 'root', 'root-row',
			'2026-07-10T00:00:00Z')`, bootstrap.TeamID, bootstrap.OwnerPrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_contributions(
			parent_object_id, derivative_object_id, team_id, scope_type, scope_id,
			parent_generation, derivative_generation, created_at)
		VALUES ('root', 'derivative', ?, 'personal', ?, 1, 1,
			'2026-07-10T00:00:00Z')`, bootstrap.TeamID, bootstrap.OwnerPrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_projection_jobs(
			job_id, store_id, team_id, root_object_id, root_generation,
			scope_type, scope_id, projection_kind, state, next_attempt_at, created_at, updated_at)
		VALUES ('job-old', ?, ?, 'root', 1, 'personal', ?, 'embedding', 'pending',
			'2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z')`,
		bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID); err != nil {
		t.Fatal(err)
	}

	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1,
		       updated_at = '2026-07-10T00:01:00Z'
		 WHERE object_id = 'root'`); err != nil {
		tx.Rollback()
		t.Fatalf("tombstone root with old attachments: %v", err)
	}
	if _, err := tx.Exec(`
			UPDATE team_projection_jobs
			   SET state = 'cancelled', next_attempt_at = NULL,
			       last_error_code = 'root_tombstoned', updated_at = '2026-07-10T00:01:00Z'
		 WHERE root_object_id = 'root' AND root_generation = 1`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tombstone: %v", err)
	}
	for _, table := range []string{"team_object_storage_map", "team_object_contributions", "team_projection_jobs"} {
		var rows int
		if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("%s old-generation rows = %d, want 1", table, rows)
		}
	}

	if _, err := s.DB().Exec(`
		INSERT INTO team_object_storage_map(
			object_id, team_id, scope_type, scope_id, generation,
			representation_kind, storage_key, created_at)
		VALUES ('root', ?, 'personal', ?, 1, 'embedding', 'late-row',
			'2026-07-10T00:02:00Z')`, bootstrap.TeamID, bootstrap.OwnerPrincipalID); err == nil {
		t.Fatal("late old-generation storage attachment was accepted")
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_contributions(
			parent_object_id, derivative_object_id, team_id, scope_type, scope_id,
			parent_generation, derivative_generation, created_at)
		VALUES ('root', 'late-derivative', ?, 'personal', ?, 1, 1,
			'2026-07-10T00:02:00Z')`, bootstrap.TeamID, bootstrap.OwnerPrincipalID); err == nil {
		t.Fatal("late old-generation contribution was accepted")
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_projection_jobs(
			job_id, store_id, team_id, root_object_id, root_generation,
			scope_type, scope_id, projection_kind, state, next_attempt_at, created_at, updated_at)
		VALUES ('job-late', ?, ?, 'root', 1, 'personal', ?, 'embedding', 'pending',
			'2026-07-10T00:02:00Z', '2026-07-10T00:02:00Z', '2026-07-10T00:02:00Z')`,
		bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID); err == nil {
		t.Fatal("late old-generation projection job was accepted")
	}
}

func TestAuthorizedCandidateFilterUsesScopePredicatesAndConcealsAbsence(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "member-subject", Role: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "member-subject", ClientID: "member-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	projectOne, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Assigned")
	if err != nil {
		t.Fatal(err)
	}
	projectTwo, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Not assigned")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID, ProjectID: projectOne.ProjectID,
		TargetPrincipalID: binding.AgentPrincipalID, AccessLevel: "read",
	}); err != nil {
		t.Fatal(err)
	}

	insertPolicyObject(t, s, bootstrap, "member-personal", "personal", member.PrincipalID, member.PrincipalID)
	insertPolicyObject(t, s, bootstrap, "owner-personal", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	insertPolicyObject(t, s, bootstrap, "assigned-project", "project", projectOne.ProjectID, member.PrincipalID)
	insertPolicyObject(t, s, bootstrap, "other-project", "project", projectTwo.ProjectID, bootstrap.OwnerPrincipalID)
	insertPolicyObject(t, s, bootstrap, "team-visible", "team", bootstrap.TeamID, "")

	filter, err := s.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
		PrincipalID:    binding.AgentPrincipalID,
		Capabilities:   []teamauth.Capability{teamauth.CapabilityRead},
		Context:        teamauth.ActiveContext{TeamID: bootstrap.TeamID, ProjectID: projectOne.ProjectID},
		PrivacyCeiling: "normal",
	})
	if err != nil {
		t.Fatal(err)
	}
	predicate, args, err := filter.SQLPredicate("o")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(predicate, "EXISTS") || strings.Contains(strings.ToLower(predicate), "object_id in") {
		t.Fatalf("candidate predicate materialized IDs instead of fixed scope predicates: %s", predicate)
	}
	rows, err := s.DB().Query(`SELECT o.object_id FROM team_object_registry o WHERE `+predicate+` ORDER BY o.object_id`, args...)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"assigned-project", "member-personal", "team-visible"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("authorized candidate IDs = %v, want %v", got, want)
	}

	if _, err := s.LookupAuthorizedTeamObject(ctx, filter, "member-personal"); err != nil {
		t.Fatalf("lookup visible object: %v", err)
	}
	inaccessibleErr := error(nil)
	if _, err := s.LookupAuthorizedTeamObject(ctx, filter, "owner-personal"); err != nil {
		inaccessibleErr = err
	}
	absentErr := error(nil)
	if _, err := s.LookupAuthorizedTeamObject(ctx, filter, "absent-object"); err != nil {
		absentErr = err
	}
	if !errors.Is(inaccessibleErr, ErrConcealedNotFound) || !errors.Is(absentErr, ErrConcealedNotFound) || inaccessibleErr.Error() != absentErr.Error() {
		t.Fatalf("concealment differs: inaccessible=%v absent=%v", inaccessibleErr, absentErr)
	}
}

func TestAuthorizedCandidateFilterExcludesMissingActiveContexts(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()

	member, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "context-member", Role: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "context-member", ClientID: "context-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Context project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
		TargetPrincipalID: binding.AgentPrincipalID, AccessLevel: "read",
	}); err != nil {
		t.Fatal(err)
	}

	insertPolicyObject(t, s, bootstrap, "context-personal", "personal", member.PrincipalID, member.PrincipalID)
	insertPolicyObject(t, s, bootstrap, "context-project", "project", project.ProjectID, member.PrincipalID)
	insertPolicyObject(t, s, bootstrap, "context-repo", "repo", "repo-context", member.PrincipalID)
	insertPolicyObject(t, s, bootstrap, "context-agent", "agent", binding.BindingID, member.PrincipalID)
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, expires_at, created_at, updated_at)
		VALUES ('context-session', ?, ?, 'memory', 'session', 'session-context',
			?, ?, 'normal', 'session', 'active', 1, '2035-01-01T00:00:00Z',
			'2026-07-11T00:00:00Z', '2026-07-11T00:00:00Z')`,
		bootstrap.StoreID, bootstrap.TeamID, member.PrincipalID, member.PrincipalID,
	); err != nil {
		t.Fatal(err)
	}

	filter, err := s.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
		PrincipalID:  member.PrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context:      teamauth.ActiveContext{TeamID: bootstrap.TeamID},
	})
	if err != nil {
		t.Fatal(err)
	}
	predicate, args, err := filter.SQLPredicate("object")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.DB().Query(`
		SELECT object.object_id FROM team_object_registry object
		 WHERE `+predicate+` ORDER BY object.object_id`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var objectID string
		if err := rows.Scan(&objectID); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(objectID, "context-") {
			got = append(got, objectID)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"context-personal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing-context candidates = %v, want %v", got, want)
	}
}

func TestAuthorizedCandidateFilterCanonicalizesAgentScopeToCurrentBinding(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()

	member, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "two-binding-human", Role: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "two-binding-human", ClientID: "binding-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "two-binding-human", ClientID: "binding-two",
	})
	if err != nil {
		t.Fatal(err)
	}
	insertPolicyObject(t, s, bootstrap, "agent-binding-one", "agent", first.BindingID, member.PrincipalID)
	insertPolicyObject(t, s, bootstrap, "agent-binding-two", "agent", second.BindingID, member.PrincipalID)

	build := func(principalID, requestedAgentID string) (AuthorizedCandidateFilter, error) {
		return s.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
			PrincipalID:  principalID,
			Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
			Context: teamauth.ActiveContext{
				TeamID: bootstrap.TeamID, AgentID: requestedAgentID,
			},
		})
	}
	firstFilter, err := build(first.AgentPrincipalID, "")
	if err != nil {
		t.Fatal(err)
	}
	if firstFilter.context.AgentID != first.BindingID {
		t.Fatalf("canonical agent context = %q, want binding %q", firstFilter.context.AgentID, first.BindingID)
	}
	if _, err := build(first.AgentPrincipalID, second.BindingID); !errors.Is(err, ErrTeamPolicyDenied) {
		t.Fatalf("conflicting caller agent_id = %v, want policy denied", err)
	}
	secondFilter, err := build(second.AgentPrincipalID, second.BindingID)
	if err != nil {
		t.Fatal(err)
	}

	visible := func(filter AuthorizedCandidateFilter) []string {
		t.Helper()
		predicate, args, err := filter.SQLPredicate("object")
		if err != nil {
			t.Fatal(err)
		}
		rows, err := s.DB().Query(`
			SELECT object.object_id FROM team_object_registry object
			 WHERE `+predicate+` AND object.scope_type = 'agent'
			 ORDER BY object.object_id`, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if got, want := visible(firstFilter), []string{"agent-binding-one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first binding candidates = %v, want %v", got, want)
	}
	if got, want := visible(secondFilter), []string{"agent-binding-two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second binding candidates = %v, want %v", got, want)
	}
}

func TestAuthorizedCandidateFilterFingerprintAndFreshEmptyRecheck(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "fingerprint-member", Role: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "fingerprint-member", ClientID: "fingerprint-agent",
	})
	if err != nil {
		t.Fatal(err)
	}

	request := CandidateFilterRequest{
		PrincipalID:  binding.AgentPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context: teamauth.ActiveContext{
			TeamID: bootstrap.TeamID, RepoID: "repo-fingerprint",
		},
	}
	first, err := s.BuildAuthorizedCandidateFilter(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	s.clock = func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }
	second, err := s.BuildAuthorizedCandidateFilter(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.FilterFingerprint() == "" || first.FilterFingerprint() != second.FilterFingerprint() {
		t.Fatalf("filter fingerprint is not stable: first=%q second=%q",
			first.FilterFingerprint(), second.FilterFingerprint())
	}
	changed := request
	changed.Context.RepoID = "repo-other"
	third, err := s.BuildAuthorizedCandidateFilter(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if third.FilterFingerprint() == first.FilterFingerprint() {
		t.Fatal("different active contexts shared one filter fingerprint")
	}
	if err := s.RecheckAuthorizedCandidateFilter(ctx, first); err != nil {
		t.Fatalf("fresh empty-result recheck: %v", err)
	}
	if err := s.RevokeAgentBinding(ctx, bootstrap.OwnerPrincipalID, binding.BindingID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecheckAuthorizedCandidateFilter(ctx, first); !errors.Is(err, ErrPrincipalRevoked) {
		t.Fatalf("revoked empty-result recheck = %v, want principal_revoked", err)
	}
	if err := s.RecheckAuthorizedCandidateRoots(ctx, first, []string{"unused-root"}); !errors.Is(err, ErrPrincipalRevoked) {
		t.Fatalf("revoked root recheck = %v, want principal_revoked", err)
	}
	if _, err := s.BuildAuthorizedCandidateFilter(ctx, request); !errors.Is(err, ErrPrincipalRevoked) {
		t.Fatalf("next-request revoked filter = %v, want principal_revoked", err)
	}
	_ = member
}

func TestRecheckAuthorizedCandidateRootsUsesOneSnapshotAndBoundaryTime(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return base }
	filter, err := s.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
		PrincipalID:  bootstrap.OwnerPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context:      teamauth.ActiveContext{TeamID: bootstrap.TeamID},
	})
	if err != nil {
		t.Fatal(err)
	}
	insertExpiring := func(objectID string, expiresAt time.Time) {
		t.Helper()
		if _, err := s.DB().Exec(`
			INSERT INTO team_object_registry(
				object_id, store_id, team_id, object_kind, scope_type, scope_id,
				owner_principal_id, author_principal_id, privacy_tier, retention,
				lifecycle, generation, expires_at, created_at, updated_at)
			VALUES (?, ?, ?, 'memory', 'personal', ?, ?, ?, 'normal', 'long_term',
				'active', 1, ?, '2026-07-11T00:00:00Z', '2026-07-11T00:00:00Z')`,
			objectID, bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID,
			bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID,
			expiresAt.Format(time.RFC3339Nano),
		); err != nil {
			t.Fatal(err)
		}
	}
	insertExpiring("batch-earlier-expiry", base.Add(time.Second))
	insertExpiring("batch-later-expiry", base.Add(10*time.Second))

	clockCalls := 0
	s.clock = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return base
		}
		return base.Add(2 * time.Second)
	}
	if err := s.RecheckAuthorizedCandidateRoots(ctx, filter, []string{
		"batch-earlier-expiry", "batch-later-expiry", "batch-earlier-expiry",
	}); err != nil {
		t.Fatalf("batch root recheck: %v", err)
	}
	if clockCalls != 1 {
		t.Fatalf("batch root recheck evaluated %d response times, want one", clockCalls)
	}
}

func TestAuthorizedCandidateFilterConcealsExpiredSessionObjectsAtCandidateAndResponseBoundaries(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()

	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	expiresAt := now.Add(5 * time.Minute)
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, expires_at, created_at, updated_at)
		VALUES ('active-session', ?, ?, 'memory', 'session', 'session-opaque', ?, ?,
			'normal', 'session', 'active', 1, ?, ?, ?)`,
		bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID,
		bootstrap.OwnerPrincipalID, expiresAt.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	insertPolicyObject(t, s, bootstrap, "durable-personal", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)

	buildFilter := func() AuthorizedCandidateFilter {
		t.Helper()
		filter, err := s.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
			PrincipalID:  bootstrap.OwnerPrincipalID,
			Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
			Context: teamauth.ActiveContext{
				TeamID: bootstrap.TeamID, SessionID: "session-opaque",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return filter
	}
	candidates := func(filter AuthorizedCandidateFilter) []string {
		t.Helper()
		predicate, args, err := filter.SQLPredicate("object")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(predicate, "expires_at") || strings.Contains(strings.ToLower(predicate), "object_id in") {
			t.Fatalf("expiry filter is not a bounded SQL predicate: %s", predicate)
		}
		rows, err := s.DB().Query(`
			SELECT object.object_id
			  FROM team_object_registry object
			 WHERE `+predicate+`
			 ORDER BY object.object_id`, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return ids
	}

	filterBeforeExpiry := buildFilter()
	if got, want := candidates(filterBeforeExpiry), []string{"active-session", "durable-personal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pre-expiry candidates = %v, want %v", got, want)
	}

	now = expiresAt
	filterAfterExpiry := buildFilter()
	if got, want := candidates(filterAfterExpiry), []string{"durable-personal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("post-expiry candidates = %v, want %v", got, want)
	}
	if _, err := s.LookupAuthorizedTeamObject(ctx, filterBeforeExpiry, "active-session"); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("stale pre-expiry filter lookup error = %v, want concealed", err)
	}
	if err := s.RecheckAuthorizedTeamObjectAccess(ctx, filterBeforeExpiry, "active-session"); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("expired session side-effect recheck error = %v, want concealed", err)
	}
	if err := s.RecheckAuthorizedTeamObjectAccess(ctx, filterBeforeExpiry, "durable-personal"); err != nil {
		t.Fatalf("non-expiring personal object recheck: %v", err)
	}
}

func TestPolicyEpochMustBeRecheckedForReadsAndInsideWrites(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	filter, err := s.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
		PrincipalID:  bootstrap.OwnerPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context:      teamauth.ActiveContext{TeamID: bootstrap.TeamID},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filter.PolicyEpoch()
	if err := s.RecheckTeamPolicyEpoch(ctx, snapshot); err != nil {
		t.Fatalf("fresh read epoch: %v", err)
	}

	if _, err := s.DB().Exec(`UPDATE team_stores SET auth_epoch = auth_epoch + 1 WHERE singleton = 1`); err != nil {
		t.Fatal(err)
	}
	if err := s.RecheckTeamPolicyEpoch(ctx, snapshot); !errors.Is(err, ErrTeamPolicyEpochChanged) {
		t.Fatalf("stale read epoch error = %v", err)
	}
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := s.RecheckTeamPolicyEpochTx(ctx, tx, snapshot); !errors.Is(err, ErrTeamPolicyEpochChanged) {
		t.Fatalf("stale in-transaction epoch error = %v", err)
	}
}

func TestWriterLeaseAndPolicyReadinessFailClosed(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	lease, err := s.AcquireTeamWriterLease(ctx, TeamWriterLeaseRequest{
		WriterID: "team-daemon-a", WriterVersion: teamauth.SchemaVersion, TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	var storedTokenHash string
	if err := s.DB().QueryRow(`SELECT lease_token_hash FROM team_writer_leases`).Scan(&storedTokenHash); err != nil {
		t.Fatal(err)
	}
	if len(storedTokenHash) != 64 || storedTokenHash == lease.Token || strings.Contains(storedTokenHash, lease.Token) {
		t.Fatalf("writer lease persisted unsafe token material: raw=%q stored=%q", lease.Token, storedTokenHash)
	}
	if _, err := s.CheckTeamPolicyReadiness(ctx, TeamPolicyReadinessOptions{
		TeamReadinessOptions: TeamReadinessOptions{
			ExpectedStoreID: bootstrap.StoreID, ExpectedTeamID: bootstrap.TeamID,
			ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion,
		},
		WriterID: lease.WriterID, WriterToken: lease.Token,
	}); err != nil {
		t.Fatalf("policy readiness: %v", err)
	}
	if _, err := s.AcquireTeamWriterLease(ctx, TeamWriterLeaseRequest{
		WriterID: "team-daemon-b", WriterVersion: teamauth.SchemaVersion, TTL: time.Minute,
	}); !errors.Is(err, ErrTeamWriterLeaseHeld) {
		t.Fatalf("second active writer error = %v", err)
	}
	if _, err := s.CheckTeamPolicyReadiness(ctx, TeamPolicyReadinessOptions{
		TeamReadinessOptions: TeamReadinessOptions{
			ExpectedStoreID: bootstrap.StoreID, ExpectedTeamID: bootstrap.TeamID,
			ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion,
		},
		WriterID: "wrong-writer", WriterToken: lease.Token,
	}); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("wrong writer readiness error = %v", err)
	}

	if _, err := s.DB().Exec(`UPDATE team_writer_leases SET expires_at = '2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireTeamWriterLease(ctx, TeamWriterLeaseRequest{
		WriterID: lease.WriterID, WriterVersion: teamauth.SchemaVersion,
		Token: lease.Token, TTL: time.Minute,
	}); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("expired heartbeat reacquired a rotated lease: %v", err)
	}
	replacement, err := s.AcquireTeamWriterLease(ctx, TeamWriterLeaseRequest{
		WriterID: "team-daemon-b", WriterVersion: teamauth.SchemaVersion, TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("replace expired writer: %v", err)
	}
	if replacement.WriterID != "team-daemon-b" || replacement.Token == lease.Token {
		t.Fatalf("replacement lease = %+v, old = %+v", replacement, lease)
	}
}

func acquireReadyWriter(t *testing.T, s *Store) TeamWriterLease {
	t.Helper()
	lease, err := s.AcquireTeamWriterLease(context.Background(), TeamWriterLeaseRequest{
		WriterID: "team-daemon", WriterVersion: teamauth.SchemaVersion, TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("acquire writer: %v", err)
	}
	return lease
}

func policyReadinessOptions(bootstrap BootstrapResult, lease TeamWriterLease) TeamPolicyReadinessOptions {
	return TeamPolicyReadinessOptions{
		TeamReadinessOptions: TeamReadinessOptions{
			ExpectedStoreID: bootstrap.StoreID, ExpectedTeamID: bootstrap.TeamID,
			ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion,
		},
		WriterID: lease.WriterID, WriterToken: lease.Token,
	}
}

func TestPolicyReadinessRejectsVersionEpochDurabilityAndLeaseMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, BootstrapResult, TeamWriterLease)
		want   error
	}{
		{name: "policy version mismatch", mutate: func(t *testing.T, s *Store, _ BootstrapResult, _ TeamWriterLease) {
			if _, err := s.DB().Exec(`UPDATE team_policy_metadata SET policy_version = policy_version + 1`); err != nil {
				t.Fatal(err)
			}
		}, want: ErrTeamPolicyNotReady},
		{name: "schema version mismatch", mutate: func(t *testing.T, s *Store, _ BootstrapResult, _ TeamWriterLease) {
			if _, err := s.DB().Exec(`UPDATE team_policy_metadata SET schema_version = schema_version + 1`); err != nil {
				t.Fatal(err)
			}
		}, want: ErrTeamPolicyNotReady},
		{name: "policy global epoch mismatch", mutate: func(t *testing.T, s *Store, _ BootstrapResult, _ TeamWriterLease) {
			if _, err := s.DB().Exec(`UPDATE team_policy_metadata SET global_epoch = global_epoch + 1`); err != nil {
				t.Fatal(err)
			}
		}, want: ErrTeamPolicyNotReady},
		{name: "wrong durability", mutate: func(t *testing.T, s *Store, _ BootstrapResult, _ TeamWriterLease) {
			if _, err := s.DB().Exec(`PRAGMA synchronous=NORMAL`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "expired lease", mutate: func(t *testing.T, s *Store, _ BootstrapResult, _ TeamWriterLease) {
			if _, err := s.DB().Exec(`UPDATE team_writer_leases SET expires_at = '2020-01-01T00:00:00Z'`); err != nil {
				t.Fatal(err)
			}
		}, want: ErrTeamWriterLeaseMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, bootstrap := bootstrapTeamStore(t)
			defer s.Close()
			lease := acquireReadyWriter(t, s)
			test.mutate(t, s, bootstrap, lease)
			_, err := s.CheckTeamPolicyReadiness(context.Background(), policyReadinessOptions(bootstrap, lease))
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("readiness error = %v, want %v", err, test.want)
				}
			} else if err == nil {
				t.Fatal("readiness accepted unsafe state")
			}
		})
	}

	t.Run("missing lease", func(t *testing.T) {
		s, bootstrap := bootstrapTeamStore(t)
		defer s.Close()
		_, err := s.CheckTeamPolicyReadiness(context.Background(), policyReadinessOptions(bootstrap, TeamWriterLease{}))
		if !errors.Is(err, ErrTeamWriterLeaseMismatch) {
			t.Fatalf("missing lease error = %v", err)
		}
	})

	t.Run("active legacy writer", func(t *testing.T) {
		s, bootstrap := bootstrapTeamStore(t)
		defer s.Close()
		now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
		if _, err := s.DB().Exec(`
			INSERT INTO team_writer_leases(
				store_id, team_id, writer_id, runtime_mode, writer_version,
				lease_token_hash, acquired_at, heartbeat_at, expires_at)
			VALUES (?, ?, 'legacy-writer', 'local-legacy', 33,
				?, ?, ?, ?)`,
			bootstrap.StoreID, bootstrap.TeamID, strings.Repeat("a", 64), now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano), now.Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		_, err := s.CheckTeamPolicyReadiness(context.Background(), policyReadinessOptions(bootstrap, TeamWriterLease{
			WriterID: "legacy-writer", Token: "legacy-writer-token-0001",
		}))
		if !errors.Is(err, ErrTeamWriterLeaseMismatch) {
			t.Fatalf("legacy writer readiness error = %v", err)
		}
	})
}

func TestPolicyReadinessRejectsInvalidRegistryAndOrphanStorage(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*testing.T, *Store, BootstrapResult)
	}{
		{name: "invalid unscoped registry", inject: func(t *testing.T, s *Store, bootstrap BootstrapResult) {
			s.DB().SetMaxOpenConns(1)
			if _, err := s.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`
				INSERT INTO team_object_registry(
					object_id, store_id, team_id, object_kind, scope_type, scope_id,
					owner_principal_id, author_principal_id, privacy_tier, retention,
					lifecycle, generation, created_at, updated_at)
				VALUES ('invalid-scope', ?, ?, 'memory', 'personal', 'not-the-owner',
					?, ?, 'normal', 'long_term', 'active', 1,
					'2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z')`,
				bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "storage mapping without registry identity", inject: func(t *testing.T, s *Store, bootstrap BootstrapResult) {
			s.DB().SetMaxOpenConns(1)
			if _, err := s.DB().Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			// Corruption fixture: model a pre-guard/externally modified database by
			// disabling both the FK and the attachment-time generation trigger.
			if _, err := s.DB().Exec(`DROP TRIGGER team_object_storage_map_generation_fence_insert`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`
				INSERT INTO team_object_storage_map(
					object_id, team_id, scope_type, scope_id, generation,
					representation_kind, storage_key, created_at)
				VALUES ('orphan-object', ?, 'team', ?, 1, 'root', 'orphan-row',
					'2026-07-10T00:00:00Z')`, bootstrap.TeamID, bootstrap.TeamID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`PRAGMA foreign_keys=ON`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "invalid team memory row", inject: func(t *testing.T, s *Store, bootstrap BootstrapResult) {
			s.DB().SetMaxOpenConns(1)
			insertPolicyObject(t, s, bootstrap, "invalid-memory-root", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
			if _, err := s.DB().Exec(`DROP TRIGGER team_memory_capsules_generation_fence_insert`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`
				INSERT INTO team_memory_capsules(
					capsule_id, root_object_id, team_id, scope_type, scope_id,
					root_generation, item_ordinal, schema_version, source_host,
					conversation_scope, source_timestamp, kind, redacted_summary,
					confidence, evidence_hint, tags_json, created_at)
				VALUES ('invalid-team-memory', 'invalid-memory-root', 'wrong-team', 'repo',
					'wrong-scope', 9, 30, 'wrong.schema', 'unknown-host', 'raw',
					'not-a-timestamp', 'unknown', 'synthetic invalid row', 2.0,
					'unknown', '{}', '2026-07-10T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "pending projection on tombstoned generation", inject: func(t *testing.T, s *Store, bootstrap BootstrapResult) {
			insertPolicyObject(t, s, bootstrap, "stale-job-root", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
			if _, err := s.DB().Exec(`
				INSERT INTO team_projection_jobs(
					job_id, store_id, team_id, root_object_id, root_generation,
					scope_type, scope_id, projection_kind, state, next_attempt_at,
					created_at, updated_at)
				VALUES ('stale-pending-job', ?, ?, 'stale-job-root', 1,
					'personal', ?, 'embedding', 'pending', '2026-07-10T00:00:00Z',
					'2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z')`,
				bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`
				UPDATE team_object_registry
				   SET lifecycle = 'tombstoned', generation = generation + 1
				 WHERE object_id = 'stale-job-root'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "projection output without derivative", inject: func(t *testing.T, s *Store, bootstrap BootstrapResult) {
			s.DB().SetMaxOpenConns(1)
			insertPolicyObject(t, s, bootstrap, "output-root", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
			if _, err := s.DB().Exec(`
				INSERT INTO team_projection_jobs(
					job_id, store_id, team_id, root_object_id, root_generation,
					scope_type, scope_id, projection_kind, state, next_attempt_at,
					created_at, updated_at)
				VALUES ('broken-output-job', ?, ?, 'output-root', 1,
					'personal', ?, 'embedding', 'pending', '2026-07-10T00:00:00Z',
					'2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z')`,
				bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`DROP TRIGGER team_projection_outputs_generation_fence_insert`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`
				INSERT INTO team_projection_outputs(job_id, derivative_object_id, derivative_generation, created_at)
				VALUES ('broken-output-job', 'missing-derivative', 1, '2026-07-10T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`PRAGMA foreign_keys=ON`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "audit event missing durable order", inject: func(t *testing.T, s *Store, bootstrap BootstrapResult) {
			if _, err := s.DB().Exec(`DROP TRIGGER team_audit_event_order_after_insert`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`
				INSERT INTO team_audit_events(
					event_id, store_id, occurred_at, action, outcome,
					actor_principal_id, team_id, target_kind, policy_version,
					mode, auth_epoch, reason_code, metadata_json)
				VALUES ('unordered-audit', ?, '2026-07-10T00:00:00Z', 'memory.write', 'allowed',
					?, ?, 'memory', 1, 'team-remote', 1, 'stored', '{}')`,
				bootstrap.StoreID, bootstrap.OwnerPrincipalID, bootstrap.TeamID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, bootstrap := bootstrapTeamStore(t)
			defer s.Close()
			lease := acquireReadyWriter(t, s)
			test.inject(t, s, bootstrap)
			if _, err := s.CheckTeamPolicyReadiness(context.Background(), policyReadinessOptions(bootstrap, lease)); !errors.Is(err, ErrTeamPolicyNotReady) {
				t.Fatalf("unsafe registry readiness error = %v", err)
			}
		})
	}
}

func TestPolicyMetadataCreatedForFreshAndV33UpgradeButNotUnmarkedLocal(t *testing.T) {
	t.Run("unmarked local has no policy identity", func(t *testing.T) {
		s, err := Open(filepath.Join(t.TempDir(), "local.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var rows int
		if err := s.DB().QueryRow(`SELECT count(*) FROM team_policy_metadata`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("unmarked local policy rows = %d, want 0", rows)
		}
	})

	t.Run("marked v33 upgrade gets policy identity", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "team-v33.db")
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE schema_meta (version INTEGER PRIMARY KEY, applied TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		migrations, err := loadMigrationSet(migrationsFS)
		if err != nil {
			t.Fatal(err)
		}
		for _, migration := range migrations[:33] {
			if _, err := db.Exec(migration.SQL); err != nil {
				t.Fatalf("apply migration %d: %v", migration.Version, err)
			}
			if _, err := db.Exec(`INSERT INTO schema_meta(version, applied) VALUES (?, 'fixture')`, migration.Version); err != nil {
				t.Fatal(err)
			}
		}
		for _, migration := range migrations[:33] {
			if _, err := db.Exec(`
				INSERT INTO schema_migration_manifest(version, name, sha256, applied_at)
				VALUES (?, ?, ?, 'fixture')`, migration.Version, migration.Name, migration.SHA256); err != nil {
				t.Fatal(err)
			}
		}
		root := testBootstrapRoot()
		fingerprint, err := root.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO team_stores(
				singleton, store_id, team_id, team_name, min_reader_version,
				min_writer_version, durability_profile, auth_epoch,
				bootstrap_root_fingerprint, bootstrap_consumed_at, created_at)
			VALUES (1, 'store-v33', 'team-v33', 'Upgrade fixture', 33, 33,
				'wal-full-fk', 1, ?, 'fixture', 'fixture')`, fingerprint); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		upgraded, err := OpenTeam(path, reviewTeamOptions(root))
		if err != nil {
			t.Fatalf("open upgraded team: %v", err)
		}
		defer upgraded.Close()
		var policyVersion, schemaVersion int
		var storeID, teamID string
		if err := upgraded.DB().QueryRow(`
			SELECT store_id, team_id, policy_version, schema_version
			  FROM team_policy_metadata`).Scan(&storeID, &teamID, &policyVersion, &schemaVersion); err != nil {
			t.Fatal(err)
		}
		if storeID != "store-v33" || teamID != "team-v33" || policyVersion != teamauth.PolicyVersion || schemaVersion != teamauth.SchemaVersion {
			t.Fatalf("upgraded policy metadata = store=%q team=%q policy=%d schema=%d", storeID, teamID, policyVersion, schemaVersion)
		}
	})
}

func TestServiceCandidateFilterRequiresBothProjectAndObjectGrantAndHidesTombstones(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	service, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", ClientID: "calendar-connector",
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Service project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
		TargetPrincipalID: service.PrincipalID, AccessLevel: "read",
	}); err != nil {
		t.Fatal(err)
	}
	insertPolicyObject(t, s, bootstrap, "service-project", "project", project.ProjectID, service.PrincipalID)
	insertPolicyObject(t, s, bootstrap, "service-team", "team", bootstrap.TeamID, "")
	insertPolicyObject(t, s, bootstrap, "ungranted-personal", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	for _, grant := range []struct {
		id, scopeType, scopeID string
	}{
		{id: "service-project-read", scopeType: "project", scopeID: project.ProjectID},
		{id: "service-team-read", scopeType: "team", scopeID: bootstrap.TeamID},
	} {
		if _, err := s.DB().Exec(`
			INSERT INTO team_service_object_grants(
				grant_id, team_id, service_principal_id, object_kind, action,
				scope_type, scope_id, status, auth_epoch, created_at)
			VALUES (?, ?, ?, '*', 'read', ?, ?, 'active', 1,
				'2026-07-10T00:00:00Z')`,
			grant.id, bootstrap.TeamID, service.PrincipalID, grant.scopeType, grant.scopeID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`
		UPDATE team_service_object_grants
		   SET service_principal_id = ?
		 WHERE grant_id = 'service-team-read'`, bootstrap.OwnerPrincipalID); err == nil {
		t.Fatal("service grant was reassigned to a human principal")
	}

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
	predicate, args, err := filter.SQLPredicate("object")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.DB().Query(`SELECT object.object_id FROM team_object_registry object WHERE `+predicate+` ORDER BY object.object_id`, args...)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"service-project", "service-team"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("service candidates = %v, want %v", got, want)
	}

	if _, err := s.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1
		 WHERE object_id = 'service-team'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupAuthorizedTeamObject(ctx, filter, "service-team"); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("tombstoned lookup error = %v", err)
	}

	if _, err := s.DB().Exec(`
		UPDATE team_service_object_grants
		   SET status = 'revoked', auth_epoch = auth_epoch + 1, revoked_at = '2026-07-10T00:01:00Z'
		 WHERE grant_id = 'service-project-read'`); err != nil {
		t.Fatal(err)
	}
	if err := s.RecheckTeamPolicyEpoch(ctx, filter.PolicyEpoch()); !errors.Is(err, ErrTeamPolicyEpochChanged) {
		t.Fatalf("service grant revoke did not invalidate filter: %v", err)
	}
	newFilter, err := s.BuildAuthorizedCandidateFilter(ctx, CandidateFilterRequest{
		PrincipalID:  service.PrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context: teamauth.ActiveContext{
			TeamID: bootstrap.TeamID, ProjectID: project.ProjectID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupAuthorizedTeamObject(ctx, newFilter, "service-project"); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("revoked service object grant lookup error = %v", err)
	}
}

func TestStorageMapRejectsCrossObjectAliasesAndUnsafeKeys(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Storage isolation")
	if err != nil {
		t.Fatal(err)
	}
	insertPolicyObject(t, s, bootstrap, "storage-personal", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	insertPolicyObject(t, s, bootstrap, "storage-project", "project", project.ProjectID, bootstrap.OwnerPrincipalID)

	insert := func(objectID, scopeType, scopeID, representation, key string) error {
		_, err := s.DB().Exec(`
			INSERT INTO team_object_storage_map(
				object_id, team_id, scope_type, scope_id, generation,
				representation_kind, storage_key, created_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, '2026-07-11T00:00:00Z')`,
			objectID, bootstrap.TeamID, scopeType, scopeID, representation, key)
		return err
	}
	if err := insert("storage-personal", "personal", bootstrap.OwnerPrincipalID, "embedding", "opaque:key_1.2-"); err != nil {
		t.Fatalf("insert safe storage key: %v", err)
	}
	if err := insert("storage-project", "project", project.ProjectID, "embedding", "opaque:key_1.2-"); err == nil {
		t.Fatal("same physical storage row was mapped to two canonical objects")
	}
	if err := insert("storage-project", "project", project.ProjectID, "event", "opaque:key_1.2-"); err != nil {
		t.Fatalf("representation kind should partition storage keys: %v", err)
	}
	for index, key := range []string{"path/to/row", `..\escape`, "has space", "line\nbreak", ""} {
		if err := insert("storage-project", "project", project.ProjectID, "unsafe"+string(rune('a'+index)), key); err == nil {
			t.Fatalf("unsafe storage key %q was accepted", key)
		}
	}
}

func insertProjectionJobFixture(
	t *testing.T,
	s *Store,
	bootstrap BootstrapResult,
	jobID, rootID, projectionKind, state string,
	attempts int,
	leaseHash, leaseExpiry, nextAttempt *string,
) error {
	t.Helper()
	var terminalHash, completionDigest, lastError any
	if state == "failed" {
		terminalHash = strings.Repeat("b", 64)
		lastError = TeamProjectionFailureTemporary
	}
	if state == "ready" {
		terminalHash = strings.Repeat("b", 64)
		completionDigest = strings.Repeat("c", 64)
	}
	if state == "cancelled" {
		lastError = TeamProjectionCancellationRootTombstoned
	}
	_, err := s.DB().Exec(`
		INSERT INTO team_projection_jobs(
			job_id, store_id, team_id, root_object_id, root_generation,
			scope_type, scope_id, projection_kind, state, attempt_count,
			lease_token_hash, terminal_lease_token_hash, completion_digest,
			lease_expires_at, next_attempt_at, last_error_code, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, 'personal', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			'2026-07-11T00:00:00Z', '2026-07-11T00:00:00Z')`,
		jobID, bootstrap.StoreID, bootstrap.TeamID, rootID, bootstrap.OwnerPrincipalID,
		projectionKind, state, attempts, leaseHash, terminalHash, completionDigest,
		leaseExpiry, nextAttempt, lastError)
	return err
}

func TestProjectionJobsHaveStrictLeaseRetryAndLogicalIdentity(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	insertPolicyObject(t, s, bootstrap, "job-root", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	next := "2026-07-11T00:05:00Z"
	leaseExpiry := "2026-07-11T00:01:00Z"
	shortLeaseExpiry := "soon"
	lowerHash := strings.Repeat("a", 64)
	upperHash := strings.Repeat("A", 64)

	if err := insertProjectionJobFixture(t, s, bootstrap, "job-pending", "job-root", "embedding", "pending", 0, nil, nil, &next); err != nil {
		t.Fatalf("insert scheduled pending job: %v", err)
	}
	if err := insertProjectionJobFixture(t, s, bootstrap, "job-duplicate", "job-root", "embedding", "pending", 0, nil, nil, &next); err == nil {
		t.Fatal("duplicate logical projection job was accepted")
	}
	if err := insertProjectionJobFixture(t, s, bootstrap, "job-other-kind", "job-root", "event", "pending", 0, nil, nil, &next); err != nil {
		t.Fatalf("different projection kind should be independent: %v", err)
	}

	for _, test := range []struct {
		name, jobID, kind, state string
		attempts                 int
		leaseHash, leaseExpiry   *string
		nextAttempt              *string
	}{
		{name: "pending with lease", jobID: "shape-pending-lease", kind: "claim", state: "pending", leaseHash: &lowerHash, leaseExpiry: &leaseExpiry, nextAttempt: &next},
		{name: "pending without retry", jobID: "shape-pending-retry", kind: "graph", state: "pending"},
		{name: "leased without hash", jobID: "shape-leased-hash", kind: "continuity", state: "leased", attempts: 1, leaseExpiry: &leaseExpiry},
		{name: "leased uppercase hash", jobID: "shape-leased-case", kind: "procedure", state: "leased", attempts: 1, leaseHash: &upperHash, leaseExpiry: &leaseExpiry},
		{name: "leased invalid expiry", jobID: "shape-leased-expiry", kind: "checkpoint", state: "leased", attempts: 1, leaseHash: &lowerHash, leaseExpiry: &shortLeaseExpiry},
		{name: "leased with retry", jobID: "shape-leased-retry", kind: "assertion", state: "leased", attempts: 1, leaseHash: &lowerHash, leaseExpiry: &leaseExpiry, nextAttempt: &next},
		{name: "failed without retry", jobID: "shape-failed-retry", kind: "summary", state: "failed", attempts: 1},
		{name: "ready with retry", jobID: "shape-ready-retry", kind: "resume", state: "ready", attempts: 1, nextAttempt: &next},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := insertProjectionJobFixture(t, s, bootstrap, test.jobID, "job-root", test.kind, test.state, test.attempts, test.leaseHash, test.leaseExpiry, test.nextAttempt); err == nil {
				t.Fatal("invalid projection job state shape was accepted")
			}
		})
	}
	if err := insertProjectionJobFixture(t, s, bootstrap, "job-leased", "job-root", "leased-valid", "leased", 1, &lowerHash, &leaseExpiry, nil); err != nil {
		t.Fatalf("valid leased job: %v", err)
	}
	if err := insertProjectionJobFixture(t, s, bootstrap, "job-failed", "job-root", "failed-valid", "failed", 1, nil, nil, &next); err != nil {
		t.Fatalf("valid retryable failed job: %v", err)
	}
}

func TestProjectionJobOutputLineageResolvesIntentAndPreservesOldAttachments(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Output isolation")
	if err != nil {
		t.Fatal(err)
	}
	insertPolicyObject(t, s, bootstrap, "output-root", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	insertPolicyObject(t, s, bootstrap, "output-derivative", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	insertPolicyObject(t, s, bootstrap, "output-cross-scope", "project", project.ProjectID, bootstrap.OwnerPrincipalID)
	next := "2026-07-11T00:05:00Z"
	if err := insertProjectionJobFixture(t, s, bootstrap, "output-job", "output-root", "claim", "pending", 0, nil, nil, &next); err != nil {
		t.Fatal(err)
	}
	var outputs int
	if err := s.DB().QueryRow(`SELECT count(*) FROM team_projection_outputs WHERE job_id = 'output-job'`).Scan(&outputs); err != nil {
		t.Fatalf("query unresolved job intent: %v", err)
	}
	if outputs != 0 {
		t.Fatalf("new job has %d outputs before projection", outputs)
	}
	insertOutput := func(derivative string, generation int) error {
		_, err := s.DB().Exec(`
			INSERT INTO team_projection_outputs(job_id, derivative_object_id, derivative_generation, created_at)
			VALUES ('output-job', ?, ?, '2026-07-11T00:01:00Z')`, derivative, generation)
		return err
	}
	if err := insertOutput("output-derivative", 1); err == nil {
		t.Fatal("unleased projection intent attached an output")
	}
	if _, err := s.DB().Exec(`
		UPDATE team_projection_jobs
		   SET state = 'leased', attempt_count = 1, next_attempt_at = NULL,
		       lease_token_hash = ?, lease_expires_at = '2026-07-11T00:10:00Z'
		 WHERE job_id = 'output-job'`, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("lease output job: %v", err)
	}
	if err := insertOutput("output-derivative", 1); err != nil {
		t.Fatalf("attach same-scope output: %v", err)
	}
	if err := insertOutput("output-derivative", 1); err == nil {
		t.Fatal("duplicate job output lineage was accepted")
	}
	if err := insertOutput("output-cross-scope", 1); err == nil {
		t.Fatal("job output crossed canonical scopes")
	}
	if err := insertOutput("output-root", 1); err == nil {
		t.Fatal("projection job treated its root as a derivative output")
	}
	if err := insertOutput("output-derivative", 2); err == nil {
		t.Fatal("job output attached a stale derivative generation")
	}
	if _, err := s.DB().Exec(`
		UPDATE team_projection_jobs
		   SET state = 'ready', terminal_lease_token_hash = lease_token_hash,
		       completion_digest = ?, lease_token_hash = NULL, lease_expires_at = NULL
		 WHERE job_id = 'output-job'`, strings.Repeat("c", 64)); err != nil {
		t.Fatalf("complete output job: %v", err)
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry SET lifecycle = 'tombstoned', generation = generation + 1
		 WHERE object_id = 'output-root'`); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM team_projection_outputs WHERE job_id = 'output-job'`).Scan(&outputs); err != nil {
		t.Fatal(err)
	}
	if outputs != 1 {
		t.Fatalf("tombstone removed %d old job outputs, want one preserved", 1-outputs)
	}
}

func TestProjectionJobCannotLeaseOrBecomeReadyAfterRootTombstone(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	insertPolicyObject(t, s, bootstrap, "stale-job-root", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	next := "2026-07-11T00:05:00Z"
	if err := insertProjectionJobFixture(t, s, bootstrap, "stale-job", "stale-job-root", "embedding", "pending", 0, nil, nil, &next); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry SET lifecycle = 'tombstoned', generation = generation + 1
		 WHERE object_id = 'stale-job-root'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		UPDATE team_projection_jobs
		   SET state = 'leased', attempt_count = 1, next_attempt_at = NULL,
		       lease_token_hash = ?, lease_expires_at = '2026-07-11T00:10:00Z'
		 WHERE job_id = 'stale-job'`, strings.Repeat("a", 64)); err == nil {
		t.Fatal("tombstoned root job was leased")
	}
	if _, err := s.DB().Exec(`
		UPDATE team_projection_jobs SET state = 'ready', next_attempt_at = NULL
		 WHERE job_id = 'stale-job'`); err == nil {
		t.Fatal("tombstoned root job became ready")
	}
	var jobs int
	if err := s.DB().QueryRow(`SELECT count(*) FROM team_projection_jobs WHERE job_id = 'stale-job'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatal("old pending job attachment was removed instead of preserved")
	}
}

func insertAuditFixture(t *testing.T, s *Store, bootstrap BootstrapResult, eventID string) {
	t.Helper()
	if _, err := s.DB().Exec(`
		INSERT INTO team_audit_events(
			event_id, store_id, occurred_at, action, outcome, actor_principal_id,
			client_key, team_id, target_kind, target_id, policy_version, mode,
			auth_epoch, reason_code, metadata_json)
		VALUES (?, ?, '2026-07-11T00:00:00Z', 'fixture.write', 'allowed', ?,
			NULL, ?, 'memory', 'fixture-target', 1, 'team-remote', 1, 'fixture', '{}')`,
		eventID, bootstrap.StoreID, bootstrap.OwnerPrincipalID, bootstrap.TeamID); err != nil {
		t.Fatalf("insert audit fixture: %v", err)
	}
}

func TestIdempotencyStateAndDigestShapesAreExact(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	insertPolicyObject(t, s, bootstrap, "idem-object", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	for _, eventID := range []string{"audit-pending", "audit-stored", "audit-failed", "audit-invalid"} {
		insertAuditFixture(t, s, bootstrap, eventID)
	}
	clientKey := strings.Repeat("a", 64)
	bodyDigest := strings.Repeat("b", 64)
	insert := func(action, keyHash, digest, state string, objectID, auditID *string) error {
		_, err := s.DB().Exec(`
			INSERT INTO team_idempotency_records(
				team_id, principal_id, client_key, action, idempotency_key_hash,
				body_digest, state, object_id, audit_event_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '2026-07-11T00:00:00Z', '2026-07-11T00:00:00Z')`,
			bootstrap.TeamID, bootstrap.OwnerPrincipalID, clientKey, action, keyHash,
			digest, state, objectID, auditID)
		return err
	}
	objectID := "idem-object"
	storedAudit, failedAudit, invalidAudit := "audit-stored", "audit-failed", "audit-invalid"
	if err := insert("pending-ok", strings.Repeat("1", 64), bodyDigest, "pending", nil, nil); err != nil {
		t.Fatalf("valid pending idempotency: %v", err)
	}
	if err := insert("stored-ok", strings.Repeat("2", 64), bodyDigest, "stored", &objectID, &storedAudit); err != nil {
		t.Fatalf("valid stored idempotency: %v", err)
	}
	if err := insert("failed-ok", strings.Repeat("3", 64), bodyDigest, "failed", nil, &failedAudit); err != nil {
		t.Fatalf("valid failed idempotency: %v", err)
	}

	for _, test := range []struct {
		name, action, key, digest, state string
		objectID, auditID                *string
	}{
		{name: "uppercase key hash", action: "bad-key-case", key: strings.Repeat("A", 64), digest: bodyDigest, state: "pending"},
		{name: "nonhex key hash", action: "bad-key-hex", key: strings.Repeat("g", 64), digest: bodyDigest, state: "pending"},
		{name: "uppercase body digest", action: "bad-body-case", key: strings.Repeat("4", 64), digest: strings.Repeat("B", 64), state: "pending"},
		{name: "nonhex body digest", action: "bad-body-hex", key: strings.Repeat("5", 64), digest: strings.Repeat("g", 64), state: "pending"},
		{name: "pending object", action: "bad-pending-object", key: strings.Repeat("6", 64), digest: bodyDigest, state: "pending", objectID: &objectID},
		{name: "pending audit", action: "bad-pending-audit", key: strings.Repeat("7", 64), digest: bodyDigest, state: "pending", auditID: &invalidAudit},
		{name: "stored without object", action: "bad-stored-object", key: strings.Repeat("8", 64), digest: bodyDigest, state: "stored", auditID: &invalidAudit},
		{name: "stored without audit", action: "bad-stored-audit", key: strings.Repeat("9", 64), digest: bodyDigest, state: "stored", objectID: &objectID},
		{name: "failed with object", action: "bad-failed-object", key: strings.Repeat("c", 64), digest: bodyDigest, state: "failed", objectID: &objectID, auditID: &invalidAudit},
		{name: "failed without audit", action: "bad-failed-audit", key: strings.Repeat("d", 64), digest: bodyDigest, state: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := insert(test.action, test.key, test.digest, test.state, test.objectID, test.auditID); err == nil {
				t.Fatal("invalid idempotency state or digest shape was accepted")
			}
		})
	}
}

func TestObjectLifecycleAndGenerationTransitionsAreExact(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	for _, id := range []string{"life-no-bump", "life-big-bump", "life-active-bump", "life-retry", "life-complete"} {
		insertPolicyObject(t, s, bootstrap, id, "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'tombstoned' WHERE object_id = 'life-no-bump'`); err == nil {
		t.Fatal("active to tombstoned without generation +1 was accepted")
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'tombstoned', generation = generation + 2 WHERE object_id = 'life-big-bump'`); err == nil {
		t.Fatal("active to tombstoned with generation +2 was accepted")
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET generation = generation + 1 WHERE object_id = 'life-active-bump'`); err == nil {
		t.Fatal("active object generation changed without tombstone")
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'tombstoned', generation = generation + 1 WHERE object_id = 'life-retry'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'cleaning' WHERE object_id = 'life-retry'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'tombstoned' WHERE object_id = 'life-retry'`); err != nil {
		t.Fatalf("cleaning lease expiry could not return to tombstoned: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET generation = generation + 1 WHERE object_id = 'life-retry'`); err == nil {
		t.Fatal("non-tombstone transition changed generation")
	}

	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'tombstoned', generation = generation + 1 WHERE object_id = 'life-complete'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'cleaning' WHERE object_id = 'life-complete'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'cleanup_failed' WHERE object_id = 'life-complete'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'complete' WHERE object_id = 'life-complete'`); err == nil {
		t.Fatal("cleanup_failed transitioned directly to complete")
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'cleaning' WHERE object_id = 'life-complete'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE team_object_registry SET lifecycle = 'complete' WHERE object_id = 'life-complete'`); err != nil {
		t.Fatalf("cleaning could not complete: %v", err)
	}
}

func TestSessionScopedObjectsRequireAnExpiry(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	insert := func(id string, expiresAt *string) error {
		_, err := s.DB().Exec(`
			INSERT INTO team_object_registry(
				object_id, store_id, team_id, object_kind, scope_type, scope_id,
				owner_principal_id, author_principal_id, privacy_tier, retention,
				lifecycle, generation, expires_at, created_at, updated_at)
			VALUES (?, ?, ?, 'memory', 'session', 'session-opaque', ?, ?, 'normal', 'session',
				'active', 1, ?, '2026-07-11T00:00:00Z', '2026-07-11T00:00:00Z')`,
			id, bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID, expiresAt)
		return err
	}
	if err := insert("session-without-expiry", nil); err == nil {
		t.Fatal("session-scoped object without expiry was accepted")
	}
	expires := "2026-07-12T00:00:00Z"
	if err := insert("session-with-expiry", &expires); err != nil {
		t.Fatalf("session-scoped object with expiry: %v", err)
	}
}

func TestPolicyReadinessRejectsMissingOrMalformedSessionExpiry(t *testing.T) {
	for _, test := range []struct {
		name        string
		scopeType   string
		retention   string
		expiresAt   any
		bypassCheck bool
		wantReady   bool
	}{
		{
			name: "session scope without expiry", scopeType: "session", retention: "session",
			bypassCheck: true,
		},
		{
			name: "session retention without expiry", scopeType: "personal", retention: "session",
			bypassCheck: true,
		},
		{
			name: "malformed session expiry", scopeType: "session", retention: "session",
			expiresAt: strings.Repeat("x", 20),
		},
		{
			name: "session expiry exceeds maximum lifetime", scopeType: "session", retention: "session",
			expiresAt: "2026-07-11T10:00:01Z",
		},
		{
			name: "session expiry is not after creation", scopeType: "session", retention: "session",
			expiresAt: "2026-07-10T10:00:00Z",
		},
		{
			name: "expired session is structurally valid", scopeType: "session", retention: "session",
			expiresAt: "2026-07-10T11:00:00Z", wantReady: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, bootstrap := bootstrapTeamStore(t)
			defer s.Close()
			lease := acquireReadyWriter(t, s)
			if test.bypassCheck {
				s.DB().SetMaxOpenConns(1)
				if _, err := s.DB().Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
			}
			scopeID := "session-opaque"
			if test.scopeType == "personal" {
				scopeID = bootstrap.OwnerPrincipalID
			}
			if _, err := s.DB().Exec(`
				INSERT INTO team_object_registry(
					object_id, store_id, team_id, object_kind, scope_type, scope_id,
					owner_principal_id, author_principal_id, privacy_tier, retention,
					lifecycle, generation, expires_at, created_at, updated_at)
				VALUES ('session-expiry-readiness', ?, ?, 'memory', ?, ?, ?, ?,
					'normal', ?, 'active', 1, ?,
					'2026-07-10T10:00:00Z', '2026-07-10T10:00:00Z')`,
				bootstrap.StoreID, bootstrap.TeamID, test.scopeType, scopeID,
				bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID,
				test.retention, test.expiresAt); err != nil {
				t.Fatal(err)
			}
			if test.bypassCheck {
				if _, err := s.DB().Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
					t.Fatal(err)
				}
			}

			_, err := s.CheckTeamPolicyReadiness(context.Background(), policyReadinessOptions(bootstrap, lease))
			if test.wantReady {
				if err != nil {
					t.Fatalf("expired but valid session readiness: %v", err)
				}
			} else if !errors.Is(err, ErrTeamPolicyNotReady) {
				t.Fatalf("readiness error = %v, want %v", err, ErrTeamPolicyNotReady)
			}
		})
	}
}

func TestAuditEventsReceiveImmutableInsertionOrder(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	insertAuditFixture(t, s, bootstrap, "audit-order-a")
	insertAuditFixture(t, s, bootstrap, "audit-order-b")
	var first, second int64
	if err := s.DB().QueryRow(`SELECT audit_sequence FROM team_audit_event_order WHERE event_id = 'audit-order-a'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT audit_sequence FROM team_audit_event_order WHERE event_id = 'audit-order-b'`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second != first+1 {
		t.Fatalf("audit order = %d then %d, want contiguous insertion order", first, second)
	}
	if _, err := s.DB().Exec(`UPDATE team_audit_event_order SET audit_sequence = audit_sequence + 10 WHERE event_id = 'audit-order-a'`); err == nil {
		t.Fatal("audit ordering row was mutable")
	}
	if _, err := s.DB().Exec(`DELETE FROM team_audit_event_order WHERE event_id = 'audit-order-a'`); err == nil {
		t.Fatal("audit ordering row was deletable")
	}
	var auditRows, orderRows int
	if err := s.DB().QueryRow(`SELECT count(*) FROM team_audit_events`).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM team_audit_event_order`).Scan(&orderRows); err != nil {
		t.Fatal(err)
	}
	if orderRows != auditRows {
		t.Fatalf("audit ordering coverage = %d/%d", orderRows, auditRows)
	}
}
