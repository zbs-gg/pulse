package store

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestMigration038InstallsMetadataOnlyDeletionSchema(t *testing.T) {
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 51 || migrations[37].Version != 38 ||
		migrations[37].Name != "038_team_deletion.sql" {
		t.Fatalf("migration 038 = %+v (count %d), want frozen 038_team_deletion.sql", migrations[37], len(migrations))
	}
	if migrations[36].SHA256 != frozenMigration037SHA256 {
		t.Fatalf("migration 037 fingerprint = %s, want frozen %s", migrations[36].SHA256, frozenMigration037SHA256)
	}

	s, _ := bootstrapTeamStore(t)
	defer s.Close()

	wantColumns := map[string][]string{
		"team_deletion_operations": {
			"operation_id", "store_id", "team_id", "root_object_id", "root_generation",
			"actor_principal_id", "oauth_client_key", "request_id", "idempotency_key_hash",
			"body_digest", "start_audit_event_id", "completion_audit_event_id", "state",
			"attempt_count", "lease_token_hash", "lease_expires_at", "next_attempt_at",
			"last_error_code", "started_at", "updated_at", "completed_at",
			"owner_approval_nonce_hash",
		},
		"team_deletion_frontier": {
			"operation_id", "object_id", "object_generation", "depth", "discovered_at",
		},
		"team_deletion_discharges": {
			"operation_id", "object_id", "object_generation", "depth", "disposition", "discharged_at",
		},
	}
	for table, want := range wantColumns {
		gotMap := teamTableColumns(t, s, table)
		got := make([]string, 0, len(gotMap))
		for column := range gotMap {
			got = append(got, column)
		}
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s columns = %v, want %v", table, got, want)
		}
	}

	var operationDDL, frontierDDL, dischargeDDL string
	if err := s.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'team_deletion_operations'`).Scan(&operationDDL); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'team_deletion_frontier'`).Scan(&frontierDDL); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'team_deletion_discharges'`).Scan(&dischargeDDL); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"raw_input", "transcript", "prompt", "summary", "payload_json", "idempotency_key text", "lease_token text"} {
		if strings.Contains(strings.ToLower(operationDDL+"\n"+frontierDDL+"\n"+dischargeDDL), forbidden) {
			t.Fatalf("deletion schema persists forbidden content or raw credential field %q", forbidden)
		}
	}
	for _, fragment := range []string{
		"state in ('pending', 'leased', 'cleanup_failed', 'complete')",
		"oauth_client_key = '' or",
		"length(idempotency_key_hash) = 64",
		"length(body_digest) = 64",
		"length(lease_token_hash) = 64",
		"primary key(operation_id, object_id)",
		"disposition in ('purged', 'preserved')",
	} {
		if !strings.Contains(strings.ToLower(operationDDL+"\n"+frontierDDL+"\n"+dischargeDDL), fragment) {
			t.Fatalf("deletion schema missing contract fragment %q", fragment)
		}
	}
}

func TestMigration038DeletionStateAndFrontierGuardsExist(t *testing.T) {
	s, _ := bootstrapTeamStore(t)
	defer s.Close()

	for _, trigger := range []string{
		"team_deletion_operations_initial_state_insert",
		"team_deletion_operations_root_fence_insert",
		"team_deletion_operations_actor_client_contract_insert",
		"team_deletion_operations_identity_immutable",
		"team_deletion_operations_state_forward_only",
		"team_deletion_operations_attempt_contract",
		"team_deletion_operations_completion_barrier",
		"team_deletion_operations_complete_immutable",
		"team_deletion_frontier_root_contract",
		"team_deletion_frontier_generation_fence_insert",
		"team_deletion_frontier_immutable",
		"team_deletion_frontier_discharge_before_delete",
		"team_deletion_discharges_contract_insert",
		"team_deletion_discharges_immutable",
		"team_deletion_discharges_no_delete",
		"team_object_contributions_deletion_frontier_complete",
	} {
		var count int
		if err := s.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("required migration 038 trigger %s missing", trigger)
		}
	}
}

func TestMigration038RejectsDeletingAnUncapturedContributionEdge(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	const (
		rootID       = "schema-undercaptured-root"
		derivativeID = "schema-undercaptured-derivative"
		now          = "2026-07-11T18:00:00Z"
	)
	insertPolicyObject(t, s, bootstrap, rootID, "personal",
		bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	insertPolicyObject(t, s, bootstrap, derivativeID, "personal",
		bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_contributions(
			parent_object_id, derivative_object_id, team_id, scope_type, scope_id,
			parent_generation, derivative_generation, created_at)
		VALUES (?, ?, ?, 'personal', ?, 1, 1, ?)`, rootID, derivativeID,
		bootstrap.TeamID, bootstrap.OwnerPrincipalID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = 2, updated_at = ?
		 WHERE object_id = ?`, now, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_audit_events(
			event_id, store_id, occurred_at, action, outcome, actor_principal_id,
			client_key, team_id, target_kind, target_id, request_id,
			policy_version, mode, auth_epoch, reason_code, metadata_json)
		VALUES ('schema-undercaptured-audit', ?, ?, 'team.object.delete.start',
			'allowed', ?, '', ?, 'memory', ?, 'schema-undercaptured-request',
			1, 'team-remote', 1, 'deletion_started', '{}')`, bootstrap.StoreID,
		now, bootstrap.OwnerPrincipalID, bootstrap.TeamID, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_operations(
			operation_id, store_id, team_id, root_object_id, root_generation,
			actor_principal_id, oauth_client_key, request_id,
			idempotency_key_hash, body_digest, start_audit_event_id,
			state, attempt_count, next_attempt_at, started_at, updated_at)
		VALUES ('schema-undercaptured-operation', ?, ?, ?, 1, ?, '',
			'schema-undercaptured-request', ?, ?, 'schema-undercaptured-audit',
			'pending', 0, ?, ?, ?)`, bootstrap.StoreID, bootstrap.TeamID, rootID,
		bootstrap.OwnerPrincipalID, strings.Repeat("2", 64), strings.Repeat("3", 64),
		now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_frontier(
			operation_id, object_id, object_generation, depth, discovered_at)
		VALUES ('schema-undercaptured-operation', ?, 1, 0, ?)`, rootID, now); err != nil {
		t.Fatal(err)
	}
	deleteEdge := func() error {
		_, err := s.DB().Exec(`
			DELETE FROM team_object_contributions
			 WHERE parent_object_id = ? AND derivative_object_id = ?`, rootID, derivativeID)
		return err
	}
	if err := deleteEdge(); err == nil {
		t.Fatal("deletion removed an edge whose derivative generation was not captured")
	}
	for _, lifecycle := range []string{"cleaning", "complete"} {
		if _, err := s.DB().Exec(`
			UPDATE team_object_registry SET lifecycle = ?, updated_at = ? WHERE object_id = ?`,
			lifecycle, now, rootID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_discharges(
			operation_id, object_id, object_generation, depth, disposition, discharged_at)
		VALUES ('schema-undercaptured-operation', ?, 1, 0, 'purged', ?)`, rootID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		DELETE FROM team_deletion_frontier
		 WHERE operation_id = 'schema-undercaptured-operation' AND object_id = ?`, rootID); err != nil {
		t.Fatal(err)
	}
	if err := deleteEdge(); err == nil {
		t.Fatal("deletion removed an uncaptured edge after source frontier discharge")
	}
}

func TestMigration038RejectsDeletionOperationForContributedDerivative(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	const (
		parentID     = "schema-delete-parent"
		derivativeID = "schema-delete-derivative"
		now          = "2026-07-11T18:00:00Z"
	)
	insertPolicyObject(t, s, bootstrap, parentID, "personal",
		bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	insertPolicyObject(t, s, bootstrap, derivativeID, "personal",
		bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_contributions(
			parent_object_id, derivative_object_id, team_id, scope_type, scope_id,
			parent_generation, derivative_generation, created_at)
		VALUES (?, ?, ?, 'personal', ?, 1, 1, ?)`, parentID, derivativeID,
		bootstrap.TeamID, bootstrap.OwnerPrincipalID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = 2, updated_at = ?
		 WHERE object_id = ?`, now, derivativeID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_audit_events(
			event_id, store_id, occurred_at, action, outcome, actor_principal_id,
			client_key, team_id, target_kind, target_id, request_id,
			policy_version, mode, auth_epoch, reason_code, metadata_json)
		VALUES ('schema-delete-derivative-audit', ?, ?, 'team.object.delete.start',
			'allowed', ?, '', ?, 'memory', ?, 'schema-delete-derivative-request',
			1, 'team-remote', 1, 'deletion_started', '{}')`,
		bootstrap.StoreID, now, bootstrap.OwnerPrincipalID, bootstrap.TeamID,
		derivativeID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_operations(
			operation_id, store_id, team_id, root_object_id, root_generation,
			actor_principal_id, oauth_client_key, request_id,
			idempotency_key_hash, body_digest, start_audit_event_id,
			state, attempt_count, next_attempt_at, started_at, updated_at)
		VALUES ('schema-delete-derivative-operation', ?, ?, ?, 1, ?, '',
			'schema-delete-derivative-request', ?, ?, 'schema-delete-derivative-audit',
			'pending', 0, ?, ?, ?)`, bootstrap.StoreID, bootstrap.TeamID,
		derivativeID, bootstrap.OwnerPrincipalID, strings.Repeat("a", 64),
		strings.Repeat("b", 64), now, now, now); err == nil {
		t.Fatal("deletion operation accepted a root with an inbound contribution")
	}
}

func TestMigration038RejectsDeletionOperationForPendingFrontierDescendant(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	const (
		rootID       = "schema-frontier-root"
		derivativeID = "schema-frontier-derivative"
		now          = "2026-07-11T18:00:00Z"
	)
	insertPolicyObject(t, s, bootstrap, rootID, "personal",
		bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	insertPolicyObject(t, s, bootstrap, derivativeID, "personal",
		bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_contributions(
			parent_object_id, derivative_object_id, team_id, scope_type, scope_id,
			parent_generation, derivative_generation, created_at)
		VALUES (?, ?, ?, 'personal', ?, 1, 1, ?)`, rootID, derivativeID,
		bootstrap.TeamID, bootstrap.OwnerPrincipalID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry SET lifecycle = 'tombstoned', generation = 2, updated_at = ?
		 WHERE object_id = ?`, now, rootID); err != nil {
		t.Fatal(err)
	}
	insertStartAudit := func(eventID, targetID, requestID string) {
		t.Helper()
		if _, err := s.DB().Exec(`
			INSERT INTO team_audit_events(
				event_id, store_id, occurred_at, action, outcome, actor_principal_id,
				client_key, team_id, target_kind, target_id, request_id,
				policy_version, mode, auth_epoch, reason_code, metadata_json)
			VALUES (?, ?, ?, 'team.object.delete.start', 'allowed', ?, '', ?,
				'memory', ?, ?, 1, 'team-remote', 1, 'deletion_started', '{}')`,
			eventID, bootstrap.StoreID, now, bootstrap.OwnerPrincipalID,
			bootstrap.TeamID, targetID, requestID); err != nil {
			t.Fatal(err)
		}
	}
	insertStartAudit("schema-frontier-root-audit", rootID, "schema-frontier-root-request")
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_operations(
			operation_id, store_id, team_id, root_object_id, root_generation,
			actor_principal_id, oauth_client_key, request_id,
			idempotency_key_hash, body_digest, start_audit_event_id,
			state, attempt_count, next_attempt_at, started_at, updated_at)
		VALUES ('schema-frontier-root-operation', ?, ?, ?, 1, ?, '',
			'schema-frontier-root-request', ?, ?, 'schema-frontier-root-audit',
			'pending', 0, ?, ?, ?)`, bootstrap.StoreID, bootstrap.TeamID, rootID,
		bootstrap.OwnerPrincipalID, strings.Repeat("c", 64), strings.Repeat("d", 64),
		now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_frontier(
			operation_id, object_id, object_generation, depth, discovered_at)
		VALUES ('schema-frontier-root-operation', ?, 1, 0, ?),
		       ('schema-frontier-root-operation', ?, 1, 1, ?)`,
		rootID, now, derivativeID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		DELETE FROM team_object_contributions
		 WHERE parent_object_id = ? AND derivative_object_id = ?`, rootID, derivativeID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry SET lifecycle = 'tombstoned', generation = 2, updated_at = ?
		 WHERE object_id = ?`, now, derivativeID); err != nil {
		t.Fatal(err)
	}
	insertStartAudit("schema-frontier-derivative-audit", derivativeID,
		"schema-frontier-derivative-request")
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_operations(
			operation_id, store_id, team_id, root_object_id, root_generation,
			actor_principal_id, oauth_client_key, request_id,
			idempotency_key_hash, body_digest, start_audit_event_id,
			state, attempt_count, next_attempt_at, started_at, updated_at)
		VALUES ('schema-frontier-derivative-operation', ?, ?, ?, 1, ?, '',
			'schema-frontier-derivative-request', ?, ?, 'schema-frontier-derivative-audit',
			'pending', 0, ?, ?, ?)`, bootstrap.StoreID, bootstrap.TeamID, derivativeID,
		bootstrap.OwnerPrincipalID, strings.Repeat("e", 64), strings.Repeat("f", 64),
		now, now, now); err == nil {
		t.Fatal("deletion operation accepted a pending frontier descendant")
	}
	if _, err := s.DB().Exec(`
		UPDATE team_deletion_operations
		   SET state = 'leased', attempt_count = 1, lease_token_hash = ?,
		       lease_expires_at = '2026-07-11T18:05:00Z', next_attempt_at = NULL,
		       updated_at = ?
		 WHERE operation_id = 'schema-frontier-root-operation'`,
		strings.Repeat("1", 64), now); err != nil {
		t.Fatal(err)
	}
	for _, objectID := range []string{rootID, derivativeID} {
		if _, err := s.DB().Exec(`
			UPDATE team_object_registry SET lifecycle = 'cleaning', updated_at = ?
			 WHERE object_id = ?`, now, objectID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`
		DELETE FROM team_deletion_frontier
		 WHERE operation_id = 'schema-frontier-root-operation' AND object_id = ?`,
		derivativeID); err == nil {
		t.Fatal("descendant frontier was removed without a discharge")
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_discharges(
			operation_id, object_id, object_generation, depth, disposition, discharged_at)
		VALUES ('schema-frontier-root-operation', ?, 1, 1, 'purged', ?)`,
		derivativeID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		DELETE FROM team_deletion_frontier
		 WHERE operation_id = 'schema-frontier-root-operation' AND object_id = ?`,
		derivativeID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry SET lifecycle = 'complete', updated_at = ?
		 WHERE object_id = ?`, now, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_discharges(
			operation_id, object_id, object_generation, depth, disposition, discharged_at)
		VALUES ('schema-frontier-root-operation', ?, 1, 0, 'purged', ?)`,
		rootID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		DELETE FROM team_deletion_frontier
		 WHERE operation_id = 'schema-frontier-root-operation' AND object_id = ?`,
		rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_audit_events(
			event_id, store_id, occurred_at, action, outcome, actor_principal_id,
			client_key, team_id, target_kind, target_id, request_id,
			policy_version, mode, auth_epoch, reason_code, metadata_json)
		VALUES ('schema-frontier-root-complete-audit', ?, ?,
			'team.object.delete.complete', 'allowed', ?, '', ?, 'memory', ?,
			'schema-frontier-root-request', 1, 'team-remote', 1,
			'deletion_complete', '{}')`, bootstrap.StoreID, now,
		bootstrap.OwnerPrincipalID, bootstrap.TeamID, rootID); err != nil {
		t.Fatal(err)
	}
	completeRoot := func() error {
		_, err := s.DB().Exec(`
			UPDATE team_deletion_operations
			   SET state = 'complete', lease_token_hash = NULL, lease_expires_at = NULL,
			       completion_audit_event_id = 'schema-frontier-root-complete-audit',
			       completed_at = ?, updated_at = ?
			 WHERE operation_id = 'schema-frontier-root-operation'`, now, now)
		return err
	}
	if err := completeRoot(); err == nil {
		t.Fatal("deletion completed while a purged descendant registry row remained")
	}
	if _, err := s.DB().Exec(`DELETE FROM team_object_registry WHERE object_id = ?`, derivativeID); err != nil {
		t.Fatal(err)
	}
	if err := completeRoot(); err != nil {
		t.Fatalf("complete deletion after descendant physical purge: %v", err)
	}
}

func TestMigration038AcceptsHumanOwnerWithoutOAuthClientAndRejectsUnboundClient(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()

	const (
		rootID    = "schema-delete-root"
		requestID = "schema-delete-request"
		now       = "2026-07-11T18:00:00Z"
	)
	insertPolicyObject(t, s, bootstrap, rootID, "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	if _, err := s.DB().Exec(`
		INSERT INTO team_object_storage_map(
			object_id, team_id, scope_type, scope_id, generation,
			representation_kind, storage_key, created_at)
		VALUES (?, ?, 'personal', ?, 1, 'memory', ?, ?)`,
		rootID, bootstrap.TeamID, bootstrap.OwnerPrincipalID,
		"memory:"+rootID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = 2, updated_at = ?
		 WHERE object_id = ?`, now, rootID); err != nil {
		t.Fatal(err)
	}
	insertAudit := func(eventID, clientKey, action, reason string) {
		t.Helper()
		var policyVersion int
		var authEpoch int64
		if err := s.DB().QueryRow(`
			SELECT policy_version, global_epoch FROM team_policy_metadata`).
			Scan(&policyVersion, &authEpoch); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`
			INSERT INTO team_audit_events(
				event_id, store_id, occurred_at, action, outcome, actor_principal_id,
				client_key, team_id, target_kind, target_id, request_id,
				policy_version, mode, auth_epoch, reason_code, metadata_json)
			VALUES (?, ?, ?, ?, 'allowed', ?, ?, ?,
				'memory', ?, ?, ?, 'team-remote', ?, ?, '{}')`,
			eventID, bootstrap.StoreID, now, action, bootstrap.OwnerPrincipalID,
			clientKey, bootstrap.TeamID, rootID, requestID, policyVersion, authEpoch,
			reason); err != nil {
			t.Fatal(err)
		}
	}
	insertOperation := func(operationID, auditID, clientKey string) error {
		_, err := s.DB().Exec(`
			INSERT INTO team_deletion_operations(
				operation_id, store_id, team_id, root_object_id, root_generation,
				actor_principal_id, oauth_client_key, request_id,
				idempotency_key_hash, body_digest, start_audit_event_id,
				state, attempt_count, next_attempt_at, started_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?,
				'pending', 0, ?, ?, ?)`,
			operationID, bootstrap.StoreID, bootstrap.TeamID, rootID,
			bootstrap.OwnerPrincipalID, clientKey, requestID,
			strings.Repeat("b", 64), strings.Repeat("c", 64), auditID,
			now, now, now)
		return err
	}

	insertAudit("schema-delete-audit-complete-before", "",
		"team.object.delete.complete", "deletion_complete")
	insertAudit("schema-delete-audit-wrong-start", "", "team.object.write", "object_stored")
	if err := insertOperation("schema-delete-operation-wrong-start", "schema-delete-audit-wrong-start", ""); err == nil {
		t.Fatal("deletion operation accepted a non-deletion start audit")
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_audit_events(
			event_id, store_id, occurred_at, action, outcome, actor_principal_id,
			client_key, team_id, project_id, target_kind, target_id, request_id,
			policy_version, mode, auth_epoch, reason_code, metadata_json)
		VALUES ('schema-delete-audit-wrong-attribution', ?, '2026-07-11T18:00:01Z',
			'team.object.delete.start', 'allowed', ?, '', ?, 'project-outside-team',
			'memory', ?, ?, 999, 'team-remote', 999, 'deletion_started', '{}')`,
		bootstrap.StoreID, bootstrap.OwnerPrincipalID, bootstrap.TeamID, rootID,
		requestID); err != nil {
		t.Fatal(err)
	}
	if err := insertOperation("schema-delete-operation-wrong-attribution",
		"schema-delete-audit-wrong-attribution", ""); err == nil {
		t.Fatal("deletion operation accepted false project/policy/epoch/time attribution")
	}
	unboundClient := strings.Repeat("a", 64)
	insertAudit("schema-delete-audit-unbound", unboundClient,
		"team.object.delete.start", "deletion_started")
	if err := insertOperation("schema-delete-operation-unbound", "schema-delete-audit-unbound", unboundClient); err == nil {
		t.Fatal("deletion operation accepted an OAuth client not bound to its actor")
	}

	agent := addMutationAuthorizationActor(t, s, bootstrap, "schema-delete-agent-empty-client", "member")
	insertPolicyObject(t, s, bootstrap, "schema-delete-agent-root", "personal",
		agent.member.PrincipalID, agent.member.PrincipalID)
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = 2, updated_at = ?
		 WHERE object_id = 'schema-delete-agent-root'`, now); err != nil {
		t.Fatal(err)
	}
	var agentPolicyVersion int
	var agentAuthEpoch int64
	if err := s.DB().QueryRow(`
		SELECT policy_version, global_epoch FROM team_policy_metadata`).
		Scan(&agentPolicyVersion, &agentAuthEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_audit_events(
			event_id, store_id, occurred_at, action, outcome, actor_principal_id,
			client_key, team_id, target_kind, target_id, request_id,
			policy_version, mode, auth_epoch, reason_code, metadata_json)
		VALUES ('schema-delete-audit-agent-empty', ?, ?, 'team.object.delete.start',
			'allowed', ?, '', ?, 'memory', 'schema-delete-agent-root', ?,
			?, 'team-remote', ?, 'deletion_started', '{}')`,
		bootstrap.StoreID, now, agent.binding.AgentPrincipalID, bootstrap.TeamID,
		requestID, agentPolicyVersion, agentAuthEpoch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_operations(
			operation_id, store_id, team_id, root_object_id, root_generation,
			actor_principal_id, oauth_client_key, request_id,
			idempotency_key_hash, body_digest, start_audit_event_id,
			state, attempt_count, next_attempt_at, started_at, updated_at)
		VALUES ('schema-delete-operation-agent-empty', ?, ?, 'schema-delete-agent-root', 1,
			?, '', ?, ?, ?, 'schema-delete-audit-agent-empty',
			'pending', 0, ?, ?, ?)`, bootstrap.StoreID, bootstrap.TeamID,
		agent.binding.AgentPrincipalID, requestID, strings.Repeat("8", 64),
		strings.Repeat("9", 64), now, now, now); err == nil {
		t.Fatal("deletion operation accepted an agent without its bound OAuth client")
	}

	insertAudit("schema-delete-audit-owner", "", "team.object.delete.start", "deletion_started")
	for _, statement := range []string{
		`INSERT INTO team_deletion_operations(
			operation_id, store_id, team_id, root_object_id, root_generation,
			actor_principal_id, oauth_client_key, request_id,
			idempotency_key_hash, body_digest, start_audit_event_id,
			state, attempt_count, lease_token_hash, lease_expires_at,
			started_at, updated_at)
		 VALUES ('schema-delete-operation-direct-leased', ?, ?, ?, 1, ?, '', ?, ?, ?,
			'schema-delete-audit-owner', 'leased', 1, ?, '2026-07-11T18:05:00Z', ?, ?)`,
		`INSERT INTO team_deletion_operations(
			operation_id, store_id, team_id, root_object_id, root_generation,
			actor_principal_id, oauth_client_key, request_id,
			idempotency_key_hash, body_digest, start_audit_event_id,
			completion_audit_event_id, state, attempt_count, completed_at,
			started_at, updated_at)
		 VALUES ('schema-delete-operation-direct-complete', ?, ?, ?, 1, ?, '', ?, ?, ?,
			'schema-delete-audit-owner', 'schema-delete-audit-complete-before',
			'complete', 1, ?, ?, ?)`,
	} {
		args := []any{
			bootstrap.StoreID, bootstrap.TeamID, rootID, bootstrap.OwnerPrincipalID,
			requestID, strings.Repeat("e", 64), strings.Repeat("f", 64),
		}
		if strings.Contains(statement, "direct-leased") {
			args = append(args, strings.Repeat("1", 64), now, now)
		} else {
			args = append(args, now, now, now)
		}
		if _, err := s.DB().Exec(statement, args...); err == nil {
			t.Fatalf("deletion operation accepted a direct non-pending insert: %s", statement)
		}
	}
	if err := insertOperation("schema-delete-operation-owner", "schema-delete-audit-owner", ""); err != nil {
		t.Fatalf("human Owner deletion operation: %v", err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_frontier(
			operation_id, object_id, object_generation, depth, discovered_at)
		VALUES ('schema-delete-operation-owner', ?, 1, 1, ?)`, rootID, now); err == nil {
		t.Fatal("deletion frontier accepted the root above depth zero")
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_frontier(
			operation_id, object_id, object_generation, depth, discovered_at)
		VALUES ('schema-delete-operation-owner', ?, 1, 0, ?)`, rootID, now); err != nil {
		t.Fatalf("root deletion frontier: %v", err)
	}

	insertPolicyObject(t, s, bootstrap, "schema-unrelated-object", "personal",
		bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_frontier(
			operation_id, object_id, object_generation, depth, discovered_at)
		VALUES ('schema-delete-operation-owner', 'schema-unrelated-object', 1, 1, ?)`, now); err == nil {
		t.Fatal("deletion frontier accepted an unrelated same-team object")
	}

	if _, err := s.DB().Exec(`
		UPDATE team_deletion_operations
		   SET state = 'leased', attempt_count = 1,
		       lease_token_hash = ?, lease_expires_at = ?, next_attempt_at = NULL,
		       updated_at = ?
		 WHERE operation_id = 'schema-delete-operation-owner'`,
		strings.Repeat("d", 64), "2026-07-11T18:05:00Z", now); err != nil {
		t.Fatalf("lease deletion operation: %v", err)
	}
	insertAudit("schema-delete-audit-complete", "",
		"team.object.delete.complete", "deletion_complete")
	insertAudit("schema-delete-audit-wrong-action", "", "team.object.write", "object_stored")
	if _, err := s.DB().Exec(`
		INSERT INTO team_audit_events(
			event_id, store_id, occurred_at, action, outcome, actor_principal_id,
			client_key, team_id, project_id, target_kind, target_id, request_id,
			policy_version, mode, auth_epoch, reason_code, metadata_json)
		VALUES ('schema-delete-audit-complete-wrong-attribution', ?,
			'2026-07-11T18:00:01Z', 'team.object.delete.complete', 'allowed', ?, '', ?,
			'project-outside-team', 'memory', ?, ?, 2, 'team-remote', 2,
			'deletion_complete', '{}')`, bootstrap.StoreID, bootstrap.OwnerPrincipalID,
		bootstrap.TeamID, rootID, requestID); err != nil {
		t.Fatal(err)
	}
	complete := func(auditID string) error {
		_, err := s.DB().Exec(`
			UPDATE team_deletion_operations
			   SET state = 'complete', lease_token_hash = NULL, lease_expires_at = NULL,
			       completion_audit_event_id = ?, completed_at = ?, updated_at = ?
			 WHERE operation_id = 'schema-delete-operation-owner'`, auditID, now, now)
		return err
	}
	if err := complete("schema-delete-audit-complete"); err == nil {
		t.Fatal("deletion completed while its root was still tombstoned")
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry SET lifecycle = 'cleaning', updated_at = ? WHERE object_id = ?`, now, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		UPDATE team_object_registry SET lifecycle = 'complete', updated_at = ? WHERE object_id = ?`, now, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		DELETE FROM team_deletion_frontier
		 WHERE operation_id = 'schema-delete-operation-owner'`); err == nil {
		t.Fatal("deletion frontier was removed without durable discharge proof")
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_discharges(
			operation_id, object_id, object_generation, depth, disposition, discharged_at)
		VALUES ('schema-delete-operation-owner', ?, 1, 0, 'purged',
			'2026-99-99T99:99:99Z')`, rootID); err == nil {
		t.Fatal("deletion discharge accepted a malformed authority-bearing timestamp")
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_deletion_discharges(
			operation_id, object_id, object_generation, depth, disposition, discharged_at)
		VALUES ('schema-delete-operation-owner', ?, 1, 0, 'purged', ?)`, rootID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		DELETE FROM team_deletion_frontier
		 WHERE operation_id = 'schema-delete-operation-owner'`); err != nil {
		t.Fatal(err)
	}
	if err := complete("schema-delete-audit-complete"); err == nil {
		t.Fatal("deletion completed while root storage payload remained")
	}
	if _, err := s.DB().Exec(`
		DELETE FROM team_object_storage_map WHERE object_id = ?`, rootID); err != nil {
		t.Fatal(err)
	}
	if err := complete("schema-delete-audit-complete-before"); err == nil {
		t.Fatal("deletion accepted a completion audit ordered before its start audit")
	}
	if err := complete("schema-delete-audit-wrong-action"); err == nil {
		t.Fatal("deletion accepted a non-deletion completion audit")
	}
	if err := complete("schema-delete-audit-complete-wrong-attribution"); err == nil {
		t.Fatal("deletion accepted completion attribution different from its start audit")
	}
	if err := complete("schema-delete-audit-complete"); err != nil {
		t.Fatalf("complete deletion after root barrier: %v", err)
	}
}
