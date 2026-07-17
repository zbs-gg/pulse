package store

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestMigration037InstallsScopedSemanticContributionSchema(t *testing.T) {
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 52 || migrations[36].Version != 37 ||
		migrations[36].Name != "037_team_semantic_materializations.sql" {
		t.Fatalf("migration 037 = %+v (count %d), want frozen 037_team_semantic_materializations.sql", migrations[36], len(migrations))
	}
	if migrations[35].SHA256 != frozenMigration036SHA256 {
		t.Fatalf("migration 036 fingerprint = %s, want frozen %s", migrations[35].SHA256, frozenMigration036SHA256)
	}
	if migrations[36].SHA256 != frozenMigration037SHA256 {
		t.Fatalf("migration 037 fingerprint = %s, want frozen %s", migrations[36].SHA256, frozenMigration037SHA256)
	}

	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	var schemaVersion, minReaderVersion, minWriterVersion int
	if err := s.DB().QueryRow(`
		SELECT policy.schema_version, team.min_reader_version, team.min_writer_version
		  FROM team_policy_metadata policy
		  JOIN team_stores team
		    ON team.store_id = policy.store_id AND team.team_id = policy.team_id
		 WHERE policy.store_id = ? AND policy.team_id = ?`,
		bootstrap.StoreID, bootstrap.TeamID,
	).Scan(&schemaVersion, &minReaderVersion, &minWriterVersion); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != 44 || minReaderVersion != 44 || minWriterVersion != 44 {
		t.Fatalf("current floors after frozen migration 037 = schema %d reader %d writer %d", schemaVersion, minReaderVersion, minWriterVersion)
	}

	wantColumns := map[string][]string{
		"team_semantic_materializations": {
			"intent_id", "job_id", "root_object_id", "root_generation",
			"derivative_object_id", "derivative_generation", "store_id", "team_id",
			"scope_type", "scope_id", "projection_kind", "semantic_key_digest",
			"policy_digest", "payload_digest", "created_at",
		},
		"team_graph_materializations": {
			"intent_id", "store_id", "team_id", "scope_type", "scope_id",
			"derivative_object_id", "graph_kind", "payload_json", "resolved_refs_json",
			"content_digest", "created_at",
		},
		"team_assertion_materializations": {
			"intent_id", "store_id", "team_id", "scope_type", "scope_id",
			"derivative_object_id", "claim_slot_digest", "claim_json",
			"source_refs_json", "content_digest", "created_at",
		},
		"team_continuity_materializations": {
			"intent_id", "store_id", "team_id", "scope_type", "scope_id",
			"derivative_object_id", "thread_slot_digest", "session_slot_digest",
			"checkpoint_json", "content_digest", "created_at",
		},
		"team_semantic_embeddings": {
			"intent_id", "model", "store_id", "team_id", "scope_type", "scope_id",
			"derivative_object_id", "dimensions", "vector_json", "vector_digest",
			"content_digest", "created_at",
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
		for _, forbidden := range []string{
			"raw_input", "transcript", "secret", "path", "actor_principal_id",
			"owner_principal_id", "idempotency_key",
		} {
			if _, present := gotMap[forbidden]; present {
				t.Fatalf("%s persists forbidden field %s", table, forbidden)
			}
		}
	}

	for _, table := range []string{
		"team_semantic_materializations", "team_graph_materializations",
		"team_assertion_materializations", "team_continuity_materializations",
		"team_semantic_embeddings",
	} {
		assertScopeFirstIndex(t, s, table)
	}

	for _, trigger := range []string{
		"team_semantic_materializations_generation_fence_insert",
		"team_semantic_materializations_immutable",
		"team_graph_materializations_contract_insert",
		"team_graph_materializations_immutable",
		"team_assertion_materializations_contract_insert",
		"team_assertion_materializations_immutable",
		"team_continuity_materializations_contract_insert",
		"team_continuity_materializations_immutable",
		"team_semantic_embeddings_contract_insert",
		"team_semantic_embeddings_immutable",
	} {
		var count int
		if err := s.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trigger).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("required migration 037 trigger %s missing", trigger)
		}
	}
}

func TestMigration037KeepsSharedDerivativeContributionIdentityNonUnique(t *testing.T) {
	s, _ := bootstrapTeamStore(t)
	defer s.Close()

	rows, err := s.DB().Query(`PRAGMA index_list('team_semantic_materializations')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if unique == 0 {
			continue
		}
		columns := indexColumns(t, s, name)
		if reflect.DeepEqual(columns, []string{"derivative_object_id"}) {
			t.Fatalf("shared derivative was made unique by %s", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMigration037RequiresTypedJSONAndDigestMaterial(t *testing.T) {
	s, _ := bootstrapTeamStore(t)
	defer s.Close()

	wantDDL := map[string][]string{
		"team_graph_materializations": {
			"json_valid(payload_json) = 1", "json_type(payload_json) = 'object'",
			"json_valid(resolved_refs_json) = 1", "json_type(resolved_refs_json) = 'array'",
		},
		"team_assertion_materializations": {
			"json_valid(claim_json) = 1", "json_type(claim_json) = 'object'",
			"json_valid(source_refs_json) = 1", "json_type(source_refs_json) = 'array'",
		},
		"team_continuity_materializations": {
			"json_valid(checkpoint_json) = 1", "json_type(checkpoint_json) = 'object'",
		},
		"team_semantic_embeddings": {
			"json_valid(vector_json) = 1", "json_type(vector_json) = 'array'",
			"json_array_length(vector_json) = dimensions",
		},
	}
	for table, fragments := range wantDDL {
		var ddl string
		if err := s.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(ddl, fragment) {
				t.Fatalf("%s DDL missing %q", table, fragment)
			}
		}
		for _, digest := range []string{"content_digest"} {
			if !strings.Contains(ddl, fmt.Sprintf("length(%s) = 64", digest)) {
				t.Fatalf("%s DDL does not constrain %s", table, digest)
			}
		}
	}
}

func assertScopeFirstIndex(t *testing.T, s *Store, table string) {
	t.Helper()
	rows, err := s.DB().Query(fmt.Sprintf(`PRAGMA index_list(%q)`, table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		columns := indexColumns(t, s, name)
		if len(columns) >= 4 && reflect.DeepEqual(columns[:4], []string{"store_id", "team_id", "scope_type", "scope_id"}) {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("%s has no store/team/scope-first index", table)
	}
}

func indexColumns(t *testing.T, s *Store, index string) []string {
	t.Helper()
	rows, err := s.DB().Query(fmt.Sprintf(`PRAGMA index_info(%q)`, index))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var sequence, cid int
		var name string
		if err := rows.Scan(&sequence, &cid, &name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}
