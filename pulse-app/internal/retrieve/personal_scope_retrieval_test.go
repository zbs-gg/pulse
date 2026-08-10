package retrieve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

func insertScopedRetrievalEvent(
	t *testing.T,
	vault *store.Store,
	binding, repository, objectID, namespace, memoryScope string,
	eventID, entityID int64,
	title, entityName string,
	vector []float32,
) {
	t.Helper()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	contentHash := sha256.Sum256([]byte(objectID))
	digest := hex.EncodeToString(contentHash[:])
	ledgerID := "ledger_" + objectID
	candidateID := "candidate_" + objectID
	if _, err := vault.DB().Exec(`
		INSERT INTO turn_ledgers(
		    ledger_id, finalize_receipt_id, host, session_id, turn_id,
		    source_event_key, idempotency_key, binding_digest,
		    destination_store_id, destination_class, policy_epoch, resolver_epoch,
		    request_digest, state, created_at, finalized_at
		) VALUES (?, ?, 'codex', ?, ?, ?, ?, ?, ?, 'personal', 1, 1, ?, 'candidates', ?, ?)`,
		ledgerID, "finalize_"+objectID, "session_"+objectID, "turn_"+objectID,
		"source_"+objectID, "idem_"+objectID, binding, vault.StoreID(),
		digest, now, now); err != nil {
		t.Fatalf("insert ledger %s: %v", objectID, err)
	}
	if _, err := vault.DB().Exec(`
		INSERT INTO memory_tray_candidates(
		    candidate_id, ledger_id, candidate_kind, operation, version,
		    content_digest, payload_json, state, grace_expires_at,
		    canonical_object_id, created_at, updated_at, terminal_at
		) VALUES (?, ?, 'semantic_delta', 'create', 1, ?, '{}', 'committed', ?, ?, ?, ?, ?)`,
		candidateID, ledgerID, digest, now, objectID, now, now, now); err != nil {
		t.Fatalf("insert candidate %s: %v", objectID, err)
	}
	if _, err := vault.DB().Exec(`
		INSERT INTO private_memory_objects(
		    object_id, candidate_kind, content_digest, created_from_candidate_id,
		    created_at, lifecycle, logical_memory_id, logical_generation,
		    project_namespace_id, original_repository_id, memory_scope, modified_at
		) VALUES (?, 'semantic_delta', ?, ?, ?, 'active', ?, 1, ?, ?, ?, ?)`,
		objectID, digest, candidateID, now, objectID, namespace, repository, memoryScope, now); err != nil {
		t.Fatalf("insert object %s: %v", objectID, err)
	}
	if _, err := vault.DB().Exec(
		`INSERT INTO events(id, title, description, ts) VALUES (?, ?, ?, ?)`,
		eventID, title, title, now,
	); err != nil {
		t.Fatalf("insert event %s: %v", objectID, err)
	}
	vectorJSON, _ := json.Marshal(vector)
	if _, err := vault.DB().Exec(`
		INSERT INTO event_embeddings(event_id, model, dim, vector_json, text_source, updated_at)
		VALUES (?, 'fake-embed', ?, ?, ?, ?)`,
		eventID, len(vector), string(vectorJSON), title, now); err != nil {
		t.Fatalf("insert embedding %s: %v", objectID, err)
	}
	if _, err := vault.DB().Exec(`
		INSERT INTO entities(
		    id, canonical_name, kind, aliases, first_seen, last_seen,
		    salience_score, emotional_weight, scorer_version, description_md
		) VALUES (?, ?, 'thing', '[]', ?, ?, 1, 0, 'test', '')`,
		entityID, entityName, now, now); err != nil {
		t.Fatalf("insert entity %s: %v", objectID, err)
	}
	if _, err := vault.DB().Exec(
		`INSERT INTO event_entities(event_id, entity_id) VALUES (?, ?)`,
		eventID, entityID,
	); err != nil {
		t.Fatalf("link entity %s: %v", objectID, err)
	}
	for kind, ref := range map[string]any{"event": eventID, "entity": entityID} {
		if _, err := vault.DB().Exec(`
			INSERT INTO private_semantic_projection_rows(object_id, row_kind, row_ref)
			VALUES (?, ?, CAST(? AS TEXT))`, objectID, kind, ref); err != nil {
			t.Fatalf("insert %s projection %s: %v", kind, objectID, err)
		}
	}
}

func TestPersonalScopeExcludesForeignProjectBeforeDenseLexicalGraphAndChainRanking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personal-scope.db")
	vault, err := store.OpenVault(path, store.StoreKindPersonal, "store_scope_retrieve")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()

	binding := strings.Repeat("a", 64)
	repository := "repository_project_a"
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, repository); err != nil {
		t.Fatal(err)
	}
	scope, ok, err := vault.CurrentPersonalMemoryScopeSnapshot()
	if err != nil || !ok {
		t.Fatalf("scope=%#v ok=%v err=%v", scope, ok, err)
	}

	insertScopedRetrievalEvent(t, vault, binding, repository,
		"object_project_a", scope.ProjectNamespaceID, store.MemoryScopeProject,
		101, 1001, "eligible project alpha", "projectalpha",
		[]float32{0, 1, 0, 0, 0})
	scopeB, err := vault.PersonalMemoryScopeSnapshotForBinding(binding, "repository_project_b")
	if err != nil {
		t.Fatal(err)
	}
	insertScopedRetrievalEvent(t, vault, binding, "repository_project_b",
		"object_project_b", scopeB.ProjectNamespaceID, store.MemoryScopeProject,
		102, 1002, "foreignneedle perfect match", "foreignneedle",
		[]float32{1, 0, 0, 0, 0})
	insertScopedRetrievalEvent(t, vault, binding, repository,
		"object_personal_global", scope.ProjectNamespaceID, store.MemoryScopePersonalGlobal,
		103, 1003, "globalneedle visible everywhere", "globalneedle",
		[]float32{0, 0, 1, 0, 0})

	if _, err := vault.DB().Exec(`
		INSERT INTO event_chains(parent_id, child_id, strength, kind)
		VALUES (102, 101, 1, 'causal'),
		       (101, 103, 1, 'causal')`); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	engine := New(Config{
		Store: vault, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &now,
	})
	ctx := context.Background()
	if err := engine.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := engine.eventIDs, []int64{101, 102, 103}; !reflect.DeepEqual(got, want) {
		t.Fatalf("global dense index=%v want=%v", got, want)
	}
	if got := engine.parentToChild[102]; !reflect.DeepEqual(got, []int64{101}) {
		t.Fatalf("global chain index=%v want=[101]", got)
	}
	if got := engine.parentToChild[101]; !reflect.DeepEqual(got, []int64{103}) {
		t.Fatalf("eligible chain=%v want=[103]", got)
	}

	foreignLexical, err := BM25SearchScoped(ctx, vault.DB(), "foreignneedle", 5, &scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(foreignLexical) != 0 {
		t.Fatalf("foreign lexical candidate leaked: %v", foreignLexical)
	}
	globalLexical, err := BM25SearchScoped(ctx, vault.DB(), "globalneedle", 5, &scope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(globalLexical, []int64{103}) {
		t.Fatalf("global lexical=%v want=[103]", globalLexical)
	}

	if got := engine.retrieveGraphCandidatesScoped(ctx, "foreignneedle", "anchored", 5, &scope); len(got) != 0 {
		t.Fatalf("foreign graph candidate leaked: %v", got)
	}
	if got := engine.retrieveGraphCandidatesScoped(ctx, "globalneedle", "anchored", 5, &scope); !reflect.DeepEqual(got, []int64{103}) {
		t.Fatalf("global graph=%v want=[103]", got)
	}

	response, err := engine.Retrieve(ctx, RetrieveRequest{
		Query: "foreignneedle", Mode: ModeFactual, TopK: 10, PersonalScope: &scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, eventID := range response.EventIDs {
		if eventID == 102 {
			t.Fatalf("foreign perfect vector influenced result: %v", response.EventIDs)
		}
	}
	responseB, err := engine.Retrieve(ctx, RetrieveRequest{
		Query: "foreignneedle", Mode: ModeFactual, TopK: 10, PersonalScope: &scopeB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(responseB.EventIDs, 102) || containsID(responseB.EventIDs, 101) {
		t.Fatalf("project B boundary=%v want project B + personal only", responseB.EventIDs)
	}

	var wait sync.WaitGroup
	errors := make(chan string, 40)
	for index := 0; index < 20; index++ {
		for _, probe := range []struct {
			scope   *store.PersonalMemoryScopeSnapshot
			allowed int64
			foreign int64
		}{{&scope, 101, 102}, {&scopeB, 102, 101}} {
			wait.Add(1)
			go func(probe struct {
				scope   *store.PersonalMemoryScopeSnapshot
				allowed int64
				foreign int64
			}) {
				defer wait.Done()
				got, retrieveErr := engine.Retrieve(ctx, RetrieveRequest{
					Query: "project memory", Mode: ModeFactual, TopK: 10, PersonalScope: probe.scope,
				})
				if retrieveErr != nil {
					errors <- fmt.Sprintf("scope=%s err=%v", probe.scope.RepositoryID, retrieveErr)
					return
				}
				if !containsID(got.EventIDs, probe.allowed) || containsID(got.EventIDs, probe.foreign) {
					errors <- fmt.Sprintf("scope=%s ids=%v", probe.scope.RepositoryID, got.EventIDs)
				}
			}(probe)
		}
	}
	wait.Wait()
	close(errors)
	for message := range errors {
		t.Fatal(message)
	}
}

func TestPersonalFreshProjectWriteJoinsGlobalIndexWithoutRestart(t *testing.T) {
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "fresh-personal.db"), store.StoreKindPersonal, "store_fresh_personal")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	binding := strings.Repeat("c", 64)
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_a"); err != nil {
		t.Fatal(err)
	}
	scopeA, _, err := vault.CurrentPersonalMemoryScopeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	scopeB, err := vault.PersonalMemoryScopeSnapshotForBinding(binding, "repository_b")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	engine := New(Config{Store: vault, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &now})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	insertScopedRetrievalEvent(t, vault, binding, "repository_b", "object_fresh_b",
		scopeB.ProjectNamespaceID, store.MemoryScopeProject, 201, 2001,
		"fresh project decision", "freshproject", []float32{1, 0, 0, 0, 0})
	if err := engine.RefreshEvents(context.Background(), []int64{201}); err != nil {
		t.Fatal(err)
	}
	if !containsID(engine.eventIDs, 201) {
		t.Fatalf("fresh event absent from global live index: %v", engine.eventIDs)
	}
	resultB, err := engine.Retrieve(context.Background(), RetrieveRequest{
		Query: "fresh project decision", Mode: ModeFactual, TopK: 5, PersonalScope: &scopeB,
	})
	if err != nil || !containsID(resultB.EventIDs, 201) {
		t.Fatalf("fresh project B recall ids=%v err=%v", resultB.EventIDs, err)
	}
	resultA, err := engine.Retrieve(context.Background(), RetrieveRequest{
		Query: "fresh project decision", Mode: ModeFactual, TopK: 5, PersonalScope: &scopeA,
	})
	if err != nil || containsID(resultA.EventIDs, 201) {
		t.Fatalf("fresh project B leaked to A ids=%v err=%v", resultA.EventIDs, err)
	}
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
