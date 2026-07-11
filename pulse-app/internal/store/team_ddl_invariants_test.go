package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestTeamStoreSchemaFloorsCannotDecrease(t *testing.T) {
	for _, tc := range []struct {
		name       string
		regression string
	}{
		{
			name:       "reader floor",
			regression: `UPDATE team_stores SET min_reader_version = 33 WHERE singleton = 1`,
		},
		{
			name:       "writer floor",
			regression: `UPDATE team_stores SET min_writer_version = 34 WHERE singleton = 1`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := bootstrapTeamStore(t)
			defer s.Close()

			// Raise both floors above their bootstrap values first. This makes the
			// attempted regression satisfy the table's static CHECK constraints,
			// so only the required monotonic DDL guard can reject it.
			if _, err := s.DB().Exec(`
				UPDATE team_stores
				   SET min_reader_version = 34, min_writer_version = 35
				 WHERE singleton = 1`); err != nil {
				t.Fatalf("raise schema floors: %v", err)
			}
			if _, err := s.DB().Exec(tc.regression); err == nil {
				t.Fatal("team store schema floor decreased without a DDL rejection")
			}
		})
	}
}

func TestEveryTeamAuthEpochColumnRejectsRegressionAtDDL(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()

	if _, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           testBootstrapRoot().Issuer,
		Subject:          testBootstrapRoot().Subject,
		ClientID:         "ddl-invariant-agent",
	}); err != nil {
		t.Fatalf("seed agent binding: %v", err)
	}
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "DDL invariant project")
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID:  bootstrap.OwnerPrincipalID,
		ProjectID:         project.ProjectID,
		TargetPrincipalID: bootstrap.OwnerPrincipalID,
		AccessLevel:       "admin",
	}); err != nil {
		t.Fatalf("seed project grant: %v", err)
	}
	service, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           testBootstrapRoot().Issuer,
		ClientID:         "ddl-invariant-service",
	})
	if err != nil {
		t.Fatalf("seed service principal: %v", err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO team_service_object_grants(
			grant_id, team_id, service_principal_id, object_kind, action,
			scope_type, scope_id, status, auth_epoch, created_at)
		VALUES ('ddl_service_grant', ?, ?, '*', 'read', 'team', ?, 'active', 1,
			'2026-07-10T00:00:00Z')`, bootstrap.TeamID, service.PrincipalID, bootstrap.TeamID); err != nil {
		t.Fatalf("seed service object grant: %v", err)
	}

	got := teamTablesWithColumn(t, s, "auth_epoch")
	want := []string{
		"team_agent_bindings",
		"team_audit_events",
		"team_memberships",
		"team_principals",
		"team_project_grants",
		"team_service_object_grants",
		"team_stores",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("team auth_epoch tables = %v, want %v", got, want)
	}

	for _, table := range got {
		t.Run(table, func(t *testing.T) {
			var rows int
			if err := s.DB().QueryRow(fmt.Sprintf(`SELECT count(*) FROM %q`, table)).Scan(&rows); err != nil {
				t.Fatalf("count fixture rows: %v", err)
			}
			if rows == 0 {
				t.Fatal("auth_epoch table has no fixture row")
			}

			if table != "team_audit_events" {
				if _, err := s.DB().Exec(fmt.Sprintf(`UPDATE %q SET auth_epoch = auth_epoch + 5`, table)); err != nil {
					t.Fatalf("raise auth_epoch: %v", err)
				}
			}
			if _, err := s.DB().Exec(fmt.Sprintf(`UPDATE %q SET auth_epoch = auth_epoch - 1`, table)); err == nil {
				t.Fatal("auth_epoch decreased without a DDL rejection")
			}
		})
	}
}

func TestTeamEventMetadataOnlyAcceptsEmptyObject(t *testing.T) {
	for _, table := range []string{"team_audit_events", "team_security_events"} {
		t.Run(table, func(t *testing.T) {
			s, bootstrap := bootstrapTeamStore(t)
			defer s.Close()

			insert := func(eventID, metadata string) error {
				if table == "team_audit_events" {
					_, err := s.DB().Exec(`
						INSERT INTO team_audit_events(
							event_id, store_id, occurred_at, action, outcome,
							team_id, target_kind, policy_version, mode,
							auth_epoch, reason_code, metadata_json)
						VALUES (?, ?, '2026-07-10T00:00:00Z', 'ddl.test', 'allowed',
							?, 'schema', 1, 'team-remote', 1, 'ddl_test', ?)`,
						eventID, bootstrap.StoreID, bootstrap.TeamID, metadata)
					return err
				}
				_, err := s.DB().Exec(`
					INSERT INTO team_security_events(
						event_id, store_id, occurred_at, event_type, outcome,
						team_id, policy_version, mode, reason_code, metadata_json)
					VALUES (?, ?, '2026-07-10T00:00:00Z', 'ddl.test', 'allowed',
						?, 1, 'team-remote', 'ddl_test', ?)`,
					eventID, bootstrap.StoreID, bootstrap.TeamID, metadata)
				return err
			}

			if err := insert("event_empty_metadata", "{}"); err != nil {
				t.Fatalf("insert empty metadata object: %v", err)
			}
			if err := insert("event_nonempty_metadata", `{"detail":"must-not-persist"}`); err == nil {
				t.Fatal("non-empty event metadata was accepted")
			}
		})
	}
}

func TestMigration034FreezeTablesAndTriggersExist(t *testing.T) {
	s, _ := bootstrapTeamStore(t)
	defer s.Close()

	for _, table := range []string{"team_audit_event_order", "team_projection_outputs"} {
		var count int
		if err := s.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("required migration-034 table %s missing", table)
		}
	}
	for _, trigger := range []string{
		"team_audit_event_order_after_insert",
		"team_audit_event_order_no_update",
		"team_audit_event_order_no_delete",
		"team_projection_jobs_active_generation_on_state",
		"team_projection_jobs_cancel_only_tombstoned",
		"team_projection_jobs_terminal_immutable",
		"team_projection_outputs_generation_fence_insert",
	} {
		var count int
		if err := s.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("required migration-034 trigger %s missing", trigger)
		}
	}

	columns := teamTableColumns(t, s, "team_projection_jobs")
	for _, required := range []string{"lease_token_hash", "terminal_lease_token_hash", "completion_digest", "next_attempt_at"} {
		if _, ok := columns[required]; !ok {
			t.Fatalf("team_projection_jobs missing %s", required)
		}
	}
	if _, unsafe := columns["lease_token"]; unsafe {
		t.Fatal("team_projection_jobs still persists raw lease_token")
	}
	for _, unsafe := range []string{"terminal_lease_token", "completion_payload"} {
		if _, present := columns[unsafe]; present {
			t.Fatalf("team_projection_jobs persists unsafe terminal material %s", unsafe)
		}
	}
}

func teamTableColumns(t *testing.T, s *Store, table string) map[string]struct{} {
	t.Helper()
	rows, err := s.DB().Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func teamTablesWithColumn(t *testing.T, s *Store, column string) []string {
	t.Helper()
	rows, err := s.DB().Query(`
		SELECT name
		  FROM sqlite_master
		 WHERE type = 'table' AND name LIKE 'team_%'
		 ORDER BY name`)
	if err != nil {
		t.Fatalf("list team tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			t.Fatalf("scan team table: %v", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate team tables: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close team tables: %v", err)
	}

	var matches []string
	for _, table := range tables {
		columns, err := s.DB().Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
		if err != nil {
			t.Fatalf("inspect %s columns: %v", table, err)
		}
		for columns.Next() {
			var cid, notNull, primaryKey int
			var name, dataType string
			var defaultValue any
			if err := columns.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
				columns.Close()
				t.Fatalf("scan %s column: %v", table, err)
			}
			if name == column {
				matches = append(matches, table)
			}
		}
		if err := columns.Err(); err != nil {
			columns.Close()
			t.Fatalf("iterate %s columns: %v", table, err)
		}
		if err := columns.Close(); err != nil {
			t.Fatalf("close %s columns: %v", table, err)
		}
	}
	return matches
}
