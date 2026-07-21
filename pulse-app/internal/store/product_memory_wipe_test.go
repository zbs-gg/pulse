package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openProductMemoryWipeTestVault(t *testing.T) *Store {
	t.Helper()
	vault, err := OpenVault(
		filepath.Join(t.TempDir(), "personal.db"),
		StoreKindPersonal,
		"store_personal_protected_wipe",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	binding := strings.Repeat("a", 64)
	if err := vault.ConfigureProductRuntimeAuthority(binding, 7, 11); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_pulse"); err != nil {
		t.Fatal(err)
	}
	return vault
}

func seedProductMemoryWipeCapsule(t *testing.T, vault *Store, id, summary string) {
	t.Helper()
	_, err := vault.DB().Exec(`
		INSERT INTO memory_capsules(
			id, schema_version, source_host, conversation_scope, source_timestamp,
			kind, redacted_summary, confidence, evidence_hint, privacy_tier,
			retention, tags, created_at
		) VALUES (?, 'pulse.memory_capsule.v1', 'codex', 'project',
		          '2026-07-19T00:00:00Z', 'decision', ?, 1, 'fixture',
		          'private', 'durable', '[]', '2026-07-19T00:00:00Z')`,
		id, summary,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestProductMemoryWipeSnapshotIsContentBoundAndAppliedAtomically(t *testing.T) {
	vault := openProductMemoryWipeTestVault(t)
	seedProductMemoryWipeCapsule(t, vault, "capsule_first", "The exact approved memory.")

	snapshot, err := vault.PrepareProductMemoryWipe(context.Background())
	if err != nil {
		t.Fatalf("prepare protected wipe: %v", err)
	}
	if snapshot.Schema != ProductMemoryWipeSnapshotSchemaV1 || snapshot.Version != 1 ||
		snapshot.StoreID != vault.StoreID() || snapshot.BindingDigest != strings.Repeat("a", 64) ||
		snapshot.PolicyEpoch != 7 || snapshot.AffectedDataVersion != 1 ||
		snapshot.AffectedDataCount == 0 || len(snapshot.AffectedDataDigest) != 64 {
		t.Fatalf("invalid protected wipe snapshot: %#v", snapshot)
	}

	receipt, err := vault.WipeProductMemoryIfSnapshot(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("apply protected wipe: %v", err)
	}
	if receipt.Schema != ProductMemoryWipeReceiptSchemaV1 || receipt.SnapshotDigest != snapshot.AffectedDataDigest ||
		receipt.AffectedDataCount != snapshot.AffectedDataCount || receipt.StoreID != snapshot.StoreID {
		t.Fatalf("invalid protected wipe receipt: %#v", receipt)
	}
	var remaining int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("protected wipe left %d capsules", remaining)
	}
}

func TestProductMemoryWipeRejectsStaleSnapshotWithoutDeletingNewData(t *testing.T) {
	vault := openProductMemoryWipeTestVault(t)
	seedProductMemoryWipeCapsule(t, vault, "capsule_first", "The approved memory set.")
	snapshot, err := vault.PrepareProductMemoryWipe(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	seedProductMemoryWipeCapsule(t, vault, "capsule_after_approval", "This arrived after approval.")
	if _, err := vault.WipeProductMemoryIfSnapshot(context.Background(), snapshot); !errors.Is(err, ErrProductMemoryWipeSnapshotStale) {
		t.Fatalf("stale protected wipe err=%v", err)
	}
	var remaining int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("stale protected wipe changed data; capsules=%d", remaining)
	}
}

func TestProductMemoryWipeSnapshotIgnoresRowsOutsideTheAffectedSet(t *testing.T) {
	vault := openProductMemoryWipeTestVault(t)
	seedProductMemoryWipeCapsule(t, vault, "capsule_first", "The exact affected set.")
	snapshot, err := vault.PrepareProductMemoryWipe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.DB().Exec(`
		INSERT INTO outbox(dedupe_key, chat_id, text, created_at)
		VALUES ('unrelated-daemon-work', 1, 'not product memory', '2026-07-19T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.WipeProductMemoryIfSnapshot(context.Background(), snapshot); err != nil {
		t.Fatalf("unrelated row made exact affected set stale: %v", err)
	}
	var capsules, outboxRows int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&capsules); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM outbox`).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if capsules != 0 || outboxRows != 1 {
		t.Fatalf("exact affected wipe capsules=%d unrelated_outbox=%d", capsules, outboxRows)
	}
}

func TestProductMemoryWipeSerializesAConcurrentPostApprovalWrite(t *testing.T) {
	vault := openProductMemoryWipeTestVault(t)
	seedProductMemoryWipeCapsule(t, vault, "capsule_first", "The approved memory set.")
	snapshot, err := vault.PrepareProductMemoryWipe(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var wipeErr, writeErr error
	go func() {
		defer wait.Done()
		<-start
		_, wipeErr = vault.WipeProductMemoryIfSnapshot(context.Background(), snapshot)
	}()
	go func() {
		defer wait.Done()
		<-start
		_, writeErr = vault.DB().Exec(`
			INSERT INTO memory_capsules(
				id, schema_version, source_host, conversation_scope, source_timestamp,
				kind, redacted_summary, confidence, evidence_hint, privacy_tier,
				retention, tags, created_at
			) VALUES ('capsule_concurrent', 'pulse.memory_capsule.v1', 'codex', 'project',
			          '2026-07-19T00:00:01Z', 'decision', 'Arrived concurrently.', 1,
			          'fixture', 'private', 'durable', '[]', '2026-07-19T00:00:01Z')`)
	}()
	close(start)
	wait.Wait()
	if writeErr != nil {
		t.Fatalf("concurrent write: %v", writeErr)
	}
	var remaining, concurrent int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules WHERE id='capsule_concurrent'`).Scan(&concurrent); err != nil {
		t.Fatal(err)
	}
	if concurrent != 1 {
		t.Fatal("concurrent post-approval memory was deleted")
	}
	if wipeErr == nil {
		if remaining != 1 {
			t.Fatalf("wipe won the transaction but left %d capsules", remaining)
		}
		return
	}
	if !errors.Is(wipeErr, ErrProductMemoryWipeSnapshotStale) || remaining != 2 {
		t.Fatalf("write won the transaction: wipe_err=%v remaining=%d", wipeErr, remaining)
	}
}

func TestProductMemoryWipeRejectsTamperedBoundaryAndDigest(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(*ProductMemoryWipeSnapshotV1)
	}{
		{name: "binding", fn: func(snapshot *ProductMemoryWipeSnapshotV1) { snapshot.BindingDigest = strings.Repeat("b", 64) }},
		{name: "digest", fn: func(snapshot *ProductMemoryWipeSnapshotV1) { snapshot.AffectedDataDigest = strings.Repeat("b", 64) }},
		{name: "count", fn: func(snapshot *ProductMemoryWipeSnapshotV1) { snapshot.AffectedDataCount++ }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			vault := openProductMemoryWipeTestVault(t)
			seedProductMemoryWipeCapsule(t, vault, "capsule_first", "The protected memory.")
			snapshot, err := vault.PrepareProductMemoryWipe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			mutate.fn(&snapshot)
			if _, err := vault.WipeProductMemoryIfSnapshot(context.Background(), snapshot); !errors.Is(err, ErrProductMemoryWipeSnapshotInvalid) &&
				!errors.Is(err, ErrProductMemoryWipeSnapshotStale) {
				t.Fatalf("tampered protected wipe err=%v", err)
			}
			var remaining int
			if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining != 1 {
				t.Fatalf("tampered protected wipe changed data; capsules=%d", remaining)
			}
		})
	}
}
