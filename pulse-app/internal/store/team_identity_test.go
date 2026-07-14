package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

func testBootstrapRoot() teamauth.BootstrapRoot {
	return teamauth.BootstrapRoot{
		Issuer:        "https://issuer.example",
		Subject:       "owner-external-subject",
		AdminClientID: "deployment-admin-client",
	}
}

func bootstrapTeamStore(t *testing.T) (*Store, BootstrapResult) {
	t.Helper()
	root := testBootstrapRoot()
	s, err := OpenTeam(filepath.Join(t.TempDir(), "team.db"), reviewTeamOptions(root))
	if err != nil {
		t.Fatalf("OpenTeam: %v", err)
	}
	result, err := s.BootstrapTeam(context.Background(), BootstrapTeamRequest{
		TeamName: "Synthetic Pilot", PresentedRoot: root,
	})
	if err != nil {
		s.Close()
		t.Fatalf("BootstrapTeam: %v", err)
	}
	return s, result
}

func TestTeamStoreBootstrapIsPinnedAtomicAndDurable(t *testing.T) {
	ctx := context.Background()
	expected := testBootstrapRoot()
	s, err := OpenTeam(filepath.Join(t.TempDir(), "team.db"), reviewTeamOptions(expected))
	if err != nil {
		t.Fatalf("OpenTeam: %v", err)
	}
	defer s.Close()

	var synchronous int
	if err := s.DB().QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 {
		t.Fatalf("team synchronous = %d, want FULL (2)", synchronous)
	}
	if _, err := s.CheckTeamReadiness(ctx, TeamReadinessOptions{ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion}); !errors.Is(err, ErrTeamStoreUninitialized) {
		t.Fatalf("unmarked readiness error = %v", err)
	}

	wrong := expected
	wrong.Subject = "attacker-first-token"
	if _, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{
		TeamName: "Synthetic Pilot", PresentedRoot: wrong,
	}); !errors.Is(err, ErrBootstrapRootMismatch) {
		t.Fatalf("wrong bootstrap root error = %v", err)
	}
	var markers int
	if err := s.DB().QueryRow(`SELECT count(*) FROM team_stores`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 0 {
		t.Fatal("wrong bootstrap root consumed the empty store")
	}

	result, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{
		TeamName: "Synthetic Pilot", PresentedRoot: expected,
	})
	if err != nil {
		t.Fatalf("pinned bootstrap: %v", err)
	}
	if result.StoreID == "" || result.TeamID == "" || result.OwnerPrincipalID == "" {
		t.Fatalf("bootstrap returned incomplete opaque IDs: %+v", result)
	}
	info, err := s.CheckTeamReadiness(ctx, TeamReadinessOptions{
		ExpectedStoreID: result.StoreID,
		ExpectedTeamID:  result.TeamID,
		ReaderVersion:   teamauth.SchemaVersion,
		WriterVersion:   teamauth.SchemaVersion,
	})
	if err != nil {
		t.Fatalf("ready team store: %v", err)
	}
	if info.AuthEpoch != 1 || info.MinReaderVersion != teamauth.SchemaVersion || info.MinWriterVersion != teamauth.SchemaVersion {
		t.Fatalf("unexpected team metadata: %+v", info)
	}
	if epoch, err := s.CurrentTeamAuthEpoch(ctx); err != nil || epoch != 1 {
		t.Fatalf("current team auth epoch = %d, %v; want 1", epoch, err)
	}
	owner, err := s.ResolveTeamPrincipal(ctx, result.OwnerPrincipalID)
	if err != nil {
		t.Fatalf("resolve owner: %v", err)
	}
	if owner.Kind != "human" || owner.MembershipRole != "owner" || owner.TeamEpoch != 1 {
		t.Fatalf("resolved owner = %+v", owner)
	}
	if _, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{
		TeamName: "Second Team", PresentedRoot: expected,
	}); !errors.Is(err, ErrBootstrapConsumed) {
		t.Fatalf("second bootstrap error = %v", err)
	}

	// External claims and client IDs must not be persisted in identity/audit rows.
	for _, table := range []string{"team_human_identities", "team_audit_events", "team_security_events"} {
		rows, err := s.DB().Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			serialized := ""
			for _, value := range values {
				serialized += strings.ToLower(stringValue(value))
			}
			for _, raw := range []string{expected.Issuer, expected.Subject, expected.AdminClientID} {
				if strings.Contains(serialized, strings.ToLower(raw)) {
					t.Fatalf("%s persisted raw external identity material", table)
				}
			}
		}
		rows.Close()
	}
	if _, err := s.DB().Exec(`UPDATE team_stores SET store_id = 'replacement' WHERE singleton = 1`); err == nil {
		t.Fatal("team store identity was mutable")
	}
}

func TestTeamStoreRejectsLegacyLocalDataAndLocalReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pulse.db")
	local, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.DB().Exec(`
		INSERT INTO entities(canonical_name, kind, first_seen, last_seen)
		VALUES ('legacy', 'person', '2026-07-10T00:00:00Z', '2026-07-10T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	root := testBootstrapRoot()
	if _, err := OpenTeam(path, reviewTeamOptions(root)); !errors.Is(err, ErrStoreIdentityMismatch) {
		t.Fatalf("legacy team open error = %v", err)
	}

	clean, result := bootstrapTeamStore(t)
	markedPath := clean.DBPath()
	clean.Close()
	if _, err := Open(markedPath); !errors.Is(err, ErrTeamStoreRequiresTeamOpen) {
		t.Fatalf("local reopen of team store error = %v", err)
	}
	_ = result
}

func TestTeamIdentityBindingsRevocationAndAuditAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	root := testBootstrapRoot()

	first, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           root.Issuer,
		Subject:          root.Subject,
		ClientID:         "agent-client-a",
	})
	if err != nil {
		t.Fatalf("first binding: %v", err)
	}
	repeat, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           root.Issuer,
		Subject:          root.Subject,
		ClientID:         "agent-client-a",
	})
	if err != nil || repeat.BindingID != first.BindingID {
		t.Fatalf("idempotent binding = %+v, %v; first = %+v", repeat, err, first)
	}
	second, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           root.Issuer,
		Subject:          root.Subject,
		ClientID:         "agent-client-b",
	})
	if err != nil {
		t.Fatalf("second client binding: %v", err)
	}
	if first.BindingID == second.BindingID || first.AgentPrincipalID == second.AgentPrincipalID {
		t.Fatal("different clients collapsed to one agent identity")
	}
	resolvedAgent, err := s.ResolveTeamPrincipal(ctx, first.AgentPrincipalID)
	if err != nil {
		t.Fatalf("resolve agent principal: %v", err)
	}
	if resolvedAgent.Kind != "agent" || resolvedAgent.HumanPrincipalID != bootstrap.OwnerPrincipalID ||
		resolvedAgent.BindingID != first.BindingID || resolvedAgent.MembershipRole != "owner" {
		t.Fatalf("resolved agent = %+v", resolvedAgent)
	}

	before, err := s.CheckTeamReadiness(ctx, TeamReadinessOptions{ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAgentBinding(ctx, bootstrap.OwnerPrincipalID, first.BindingID); err != nil {
		t.Fatalf("revoke binding: %v", err)
	}
	if _, err := s.ResolveAgentBinding(ctx, root.Issuer, root.Subject, "agent-client-a"); !errors.Is(err, ErrPrincipalRevoked) {
		t.Fatalf("resolve after revoke error = %v", err)
	}
	after, err := s.CheckTeamReadiness(ctx, TeamReadinessOptions{ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if after.AuthEpoch <= before.AuthEpoch {
		t.Fatalf("auth epoch did not advance: before=%d after=%d", before.AuthEpoch, after.AuthEpoch)
	}
	var audits int
	if err := s.DB().QueryRow(`
		SELECT count(*) FROM team_audit_events
		 WHERE action = 'agent_binding.revoke' AND target_id = ? AND outcome = 'allowed'`, first.BindingID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("revoke audit count = %d, want 1", audits)
	}
	if err := s.RevokeMembership(ctx, bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("last owner revoke error = %v", err)
	}
}

func TestMembershipRevocationInvalidatesDependentBindings(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          "member-subject",
		Role:             "member",
	})
	if err != nil {
		t.Fatalf("add member: %v", err)
	}
	duplicate, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          "member-subject",
		Role:             "member",
	})
	if err != nil || duplicate.PrincipalID != member.PrincipalID {
		t.Fatalf("idempotent member = %+v, %v; first = %+v", duplicate, err, member)
	}
	if _, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          "member-subject",
		Role:             "owner",
	}); !errors.Is(err, ErrInvalidTeamIdentityMutation) {
		t.Fatalf("conflicting membership reassignment error = %v", err)
	}
	binding, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          "member-subject",
		ClientID:         "member-agent-client",
	})
	if err != nil {
		t.Fatalf("bind member agent: %v", err)
	}
	before, err := s.CheckTeamReadiness(ctx, TeamReadinessOptions{ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeMembership(ctx, bootstrap.OwnerPrincipalID, member.PrincipalID); err != nil {
		t.Fatalf("revoke member: %v", err)
	}
	if _, err := s.ResolveAgentBinding(ctx, "https://issuer.example", "member-subject", "member-agent-client"); !errors.Is(err, ErrPrincipalRevoked) {
		t.Fatalf("dependent binding remained active: %+v, %v", binding, err)
	}
	after, err := s.CheckTeamReadiness(ctx, TeamReadinessOptions{ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if after.AuthEpoch <= before.AuthEpoch {
		t.Fatalf("membership revocation epoch did not advance: %d -> %d", before.AuthEpoch, after.AuthEpoch)
	}
}

func TestPrivilegedMutationRollsBackWhenAuditCannotAppend(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	if _, err := s.DB().Exec(`
		CREATE TRIGGER reject_service_audit
		BEFORE INSERT ON team_audit_events
		WHEN NEW.action = 'service_principal.create'
		BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	before, err := s.CheckTeamReadiness(ctx, TeamReadinessOptions{ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		ClientID:         "must-rollback",
	}); err == nil {
		t.Fatal("service principal mutation succeeded without durable audit")
	}
	after, err := s.CheckTeamReadiness(ctx, TeamReadinessOptions{ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if after.AuthEpoch != before.AuthEpoch {
		t.Fatalf("failed audited mutation advanced epoch: %d -> %d", before.AuthEpoch, after.AuthEpoch)
	}
	var services int
	if err := s.DB().QueryRow(`SELECT count(*) FROM team_principals WHERE kind = 'service'`).Scan(&services); err != nil {
		t.Fatal(err)
	}
	if services != 0 {
		t.Fatalf("failed audited mutation left %d service principals", services)
	}
}

func TestTeamReadinessRejectsIdentityAndVersionMismatch(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	if _, err := s.CheckTeamReadiness(ctx, TeamReadinessOptions{
		ExpectedStoreID: "wrong-store", ExpectedTeamID: bootstrap.TeamID,
		ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion,
	}); !errors.Is(err, ErrTeamStoreIdentityMismatch) {
		t.Fatalf("store mismatch error = %v", err)
	}
	if _, err := s.CheckTeamReadiness(ctx, TeamReadinessOptions{
		ExpectedStoreID: bootstrap.StoreID, ExpectedTeamID: bootstrap.TeamID,
		ReaderVersion: 32, WriterVersion: 32,
	}); !errors.Is(err, ErrUnsupportedTeamSchema) {
		t.Fatalf("old binary readiness error = %v", err)
	}
}

func TestServiceNamespaceAndProjectGrantMembershipInvariant(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()

	service, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		ClientID:         "deployment-admin-client",
	})
	if err != nil {
		t.Fatalf("service principal: %v", err)
	}
	if service.PrincipalID == bootstrap.OwnerPrincipalID {
		t.Fatal("service and human namespaces collapsed")
	}
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Synthetic Project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID:  bootstrap.OwnerPrincipalID,
		ProjectID:         project.ProjectID,
		TargetPrincipalID: "principal_missing",
		AccessLevel:       "read",
	}); !errors.Is(err, ErrMembershipRequired) {
		t.Fatalf("grant to non-member error = %v", err)
	}
	grant, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID:  bootstrap.OwnerPrincipalID,
		ProjectID:         project.ProjectID,
		TargetPrincipalID: service.PrincipalID,
		AccessLevel:       "write",
	})
	if err != nil {
		t.Fatalf("grant to active service principal: %v", err)
	}
	resolvedService, err := s.ResolveTeamPrincipal(ctx, service.PrincipalID)
	if err != nil {
		t.Fatalf("resolve service principal: %v", err)
	}
	if resolvedService.Kind != "service" || resolvedService.MembershipRole != "member" {
		t.Fatalf("resolved service = %+v", resolvedService)
	}
	resolvedGrant, err := s.ResolveProjectGrant(ctx, project.ProjectID, service.PrincipalID)
	if err != nil || resolvedGrant.GrantID != grant.GrantID || resolvedGrant.AccessLevel != "write" {
		t.Fatalf("resolved service grant = %+v, %v", resolvedGrant, err)
	}
	if err := s.RevokeProjectGrant(ctx, bootstrap.OwnerPrincipalID, grant.GrantID); err != nil {
		t.Fatalf("revoke project grant: %v", err)
	}
	if _, err := s.ResolveProjectGrant(ctx, project.ProjectID, service.PrincipalID); !errors.Is(err, ErrProjectGrantRequired) {
		t.Fatalf("revoked project grant error = %v", err)
	}
	if err := s.RevokeServicePrincipal(ctx, bootstrap.OwnerPrincipalID, service.PrincipalID); err != nil {
		t.Fatalf("revoke service principal: %v", err)
	}
	if _, err := s.ResolveTeamPrincipal(ctx, service.PrincipalID); !errors.Is(err, ErrPrincipalRevoked) {
		t.Fatalf("revoked service resolution error = %v", err)
	}
}

func stringValue(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return ""
	}
}
