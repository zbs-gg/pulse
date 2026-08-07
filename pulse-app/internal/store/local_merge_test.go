package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalMergeBuildsBesideSourcesAndCommitsWithRollbackCopy(t *testing.T) {
	home := t.TempDir()
	storeID := "store_personal_local_merge_test"
	canonicalPath := filepath.Join(home, ".pulse", "vaults", "personal", storeID, "pulse.db")
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := OpenVault(canonicalPath, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatal(err)
	}
	currentID := insertLocalMergeTestEvent(t, canonical, "Same decision", "Keep it", "2026-08-01T00:00:00Z")
	if _, err := canonical.db.Exec(`INSERT INTO event_emotions(event_id,joy,tagger,confidence,updated_at,derivation,emotion_key) VALUES(?,0.8,'manual',1,'2026-08-01T00:00:00Z','explicit','current-key')`, currentID); err != nil {
		t.Fatal(err)
	}
	insertLocalMergeTestAssertion(t, canonical, "nik:timezone", "timezone", "Asia/Bangkok")
	if err := canonical.Close(); err != nil {
		t.Fatal(err)
	}

	legacyPath := filepath.Join(home, ".pulse", "pulse.db")
	legacy, err := Open(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicateID := insertLocalMergeTestEvent(t, legacy, " Same  decision ", "keep IT", "2025-01-01T00:00:00Z")
	if _, err := legacy.db.Exec(`UPDATE events SET sentiment='positive' WHERE id=?`, duplicateID); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec(`INSERT INTO event_emotions(event_id,sadness,tagger,confidence,updated_at,derivation,emotion_key) VALUES(?,0.9,'manual',1,'2025-01-01T00:00:00Z','explicit','legacy-key')`, duplicateID); err != nil {
		t.Fatal(err)
	}
	insertLocalMergeTestEvent(t, legacy, "Unique old decision", "Carry this over", "2025-02-01T00:00:00Z")
	insertLocalMergeTestAssertion(t, legacy, "nik:timezone", "timezone", "Europe/Moscow")
	insertLocalMergeTestAssertion(t, legacy, "nik:language", "language", "Russian")
	if _, err := legacy.db.Exec(`INSERT INTO memory_capsules(id,schema_version,source_host,conversation_scope,source_timestamp,kind,redacted_summary,confidence,evidence_hint,privacy_tier,retention,tags,created_at,status,content_digest) VALUES('pulse:legacy','pulse.memory_capsule.v1','codex','project_context','2025-02-01T00:00:00Z','decision','Legacy capsule',1,'user_confirmed','private','project','[]','2025-02-01T00:00:00Z','active',NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyDigest := sha256.Sum256(legacyBefore)

	preview, err := BuildLocalMergePreview(home, canonicalPath, storeID, time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Status != "needs_review" || len(preview.Conflicts) != 2 {
		t.Fatalf("expected two real conflicts, got status=%s conflicts=%d", preview.Status, len(preview.Conflicts))
	}
	if preview.Totals.EventsCreated != 1 || preview.Totals.EventsDeduplicated != 1 || preview.Totals.CapsulesCreated != 1 || preview.Totals.AssertionsCreated != 1 {
		t.Fatalf("unexpected merge totals: %+v", preview.Totals)
	}
	if _, err := os.Stat(preview.TargetPath); err != nil {
		t.Fatalf("prepared database missing: %v", err)
	}
	for index := range preview.Conflicts {
		preview.Conflicts[index].Selected = "current"
		if preview.Conflicts[index].Kind == "assertion" {
			preview.Conflicts[index].Selected = "imported"
		}
	}
	previewPath := filepath.Join(home, "reviewed.json")
	if err := WriteLocalMergePreview(previewPath, preview); err != nil {
		t.Fatal(err)
	}
	archivePath, err := CommitLocalMergePreview(previewPath, "merge local pulse memory", time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("rollback copy missing: %v", err)
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(legacyAfter) != legacyDigest {
		t.Fatal("legacy source changed")
	}
	merged, err := OpenVault(canonicalPath, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatal(err)
	}
	var eventCount, capsuleCount, joyCount, assertionCount, timezoneCount, orphanAssertionLinks int
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM memory_capsules WHERE redacted_summary='Legacy capsule'`).Scan(&capsuleCount); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM event_emotions WHERE joy=0.8 AND sadness=0`).Scan(&joyCount); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM assertions WHERE claim_key='nik:language' AND object_text='Russian' AND status='active'`).Scan(&assertionCount); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM assertions WHERE claim_key='nik:timezone' AND object_text='Europe/Moscow' AND status='active'`).Scan(&timezoneCount); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM assertions WHERE claim_key='nik:language' AND source_event_ids IS NOT NULL`).Scan(&orphanAssertionLinks); err != nil {
		t.Fatal(err)
	}
	if eventCount != 2 || capsuleCount != 1 || joyCount != 1 || assertionCount != 1 || timezoneCount != 1 {
		t.Fatalf("merged database is incomplete: events=%d capsules=%d current_emotion=%d assertions=%d selected_conflict=%d", eventCount, capsuleCount, joyCount, assertionCount, timezoneCount)
	}
	if orphanAssertionLinks != 0 {
		t.Fatal("migrated assertion retained source-local event IDs")
	}
	if err := merged.Close(); err != nil {
		t.Fatal(err)
	}

	// A failed first startup can leave WAL sidecars beside the new database.
	// Rollback must move those aside together with the database and restore the
	// previously working Personal memory.
	if err := os.WriteFile(canonicalPath+"-wal", []byte("new-memory-wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreLocalMergeArchive(previewPath, archivePath); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenVault(canonicalPath, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var restoredEvents int
	if err := restored.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&restoredEvents); err != nil {
		t.Fatal(err)
	}
	if restoredEvents != 1 {
		t.Fatalf("rollback restored %d events, want 1", restoredEvents)
	}
	newWAL, err := os.ReadFile(preview.TargetPath + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if string(newWAL) != "new-memory-wal" {
		t.Fatalf("new database WAL was not preserved: %q", newWAL)
	}
}

func insertLocalMergeTestAssertion(t *testing.T, store *Store, claimKey, predicate, object string) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO assertions(claim_key,predicate,object_text,confidence,system_from,status,scope_type,scope_id,visibility,created_at) VALUES(?,?,?,1,'2026-08-01T00:00:00Z','active','personal','','private','2026-08-01T00:00:00Z')`, claimKey, predicate, object); err != nil {
		t.Fatal(err)
	}
}

func TestLocalMergeRefusesCommitWhenCurrentMemoryChanged(t *testing.T) {
	home := t.TempDir()
	storeID := "store_personal_local_merge_changed"
	canonicalPath := filepath.Join(home, ".pulse", "vaults", "personal", storeID, "pulse.db")
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := OpenVault(canonicalPath, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatal(err)
	}
	insertLocalMergeTestEvent(t, canonical, "Before preview", "one", "2026-08-01T00:00:00Z")
	if err := canonical.Close(); err != nil {
		t.Fatal(err)
	}
	preview, err := BuildLocalMergePreview(home, canonicalPath, storeID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	previewPath := filepath.Join(home, "preview.json")
	if err := WriteLocalMergePreview(previewPath, preview); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenVault(canonicalPath, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatal(err)
	}
	insertLocalMergeTestEvent(t, reopened, "After preview", "two", "2026-08-02T00:00:00Z")
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitLocalMergePreview(previewPath, "merge local pulse memory", time.Now()); err == nil || err.Error() != "Personal memory changed after preview; create a fresh preview" {
		t.Fatalf("expected changed-memory refusal, got %v", err)
	}
}

func TestLocalMergeConvertsMixedTeamEraDatabaseWithoutOpeningItAsPersonal(t *testing.T) {
	home := t.TempDir()
	storeID := "store_personal_mixed_team_conversion"
	canonicalPath := filepath.Join(home, ".pulse", "vaults", "personal", storeID, "pulse.db")
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := OpenVault(canonicalPath, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatal(err)
	}
	insertLocalMergeTestEvent(t, canonical, "Mixed-era decision", "Keep this local memory", "2026-08-01T00:00:00Z")
	if _, err := canonical.db.Exec(`CREATE TABLE team_private_marker(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := canonical.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVault(canonicalPath, StoreKindPersonal, storeID); !errors.Is(err, ErrUnsupportedTeamDatabase) {
		t.Fatalf("mixed source unexpectedly opened as Personal: %v", err)
	}

	preview, err := BuildLocalMergePreview(home, canonicalPath, storeID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Sources) != 1 || preview.Sources[0].Kind != "current_personal_conversion" {
		t.Fatalf("mixed source was not converted explicitly: %+v", preview.Sources)
	}
	converted, err := OpenVault(preview.TargetPath, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatal(err)
	}
	defer converted.Close()
	var events, teamTables int
	if err := converted.db.QueryRow(`SELECT COUNT(*) FROM events WHERE title='Mixed-era decision'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := converted.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'team_%'`).Scan(&teamTables); err != nil {
		t.Fatal(err)
	}
	if events != 1 || teamTables != 0 {
		t.Fatalf("converted database events=%d team_tables=%d", events, teamTables)
	}
}

func insertLocalMergeTestEvent(t *testing.T, store *Store, title, description, occurredAt string) int64 {
	t.Helper()
	result, err := store.db.Exec(`INSERT INTO events(title,description,emotional_weight,ts,belief_class,confidence_floor,archivable,provenance,domain,user_flag,access_count) VALUES(?,?,0,?,'operational',0,1,'interactive_memory','real',0,0)`, title, description, occurredAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if id < 1 {
		t.Fatal(fmt.Errorf("invalid event id %d", id))
	}
	return id
}
