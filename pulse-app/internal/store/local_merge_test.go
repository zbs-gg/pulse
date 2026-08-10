package store

import (
	"crypto/sha256"
	"database/sql"
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
	uniqueID := insertLocalMergeTestEvent(t, legacy, "Unique old decision", "Carry this over", "2025-02-01T00:00:00Z")
	if _, err := legacy.db.Exec(`INSERT INTO event_embeddings(event_id,model,dim,vector_json,text_source) VALUES(?, 'bge-m3', 1024, '[]', 'managed')`, uniqueID); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.Exec(`INSERT INTO event_embeddings(event_id,model,dim,vector_json,text_source) VALUES(?, 'bge-m3-mlx-fp16', 1024, '[]', 'legacy')`, duplicateID); err != nil {
		t.Fatal(err)
	}
	insertLocalMergeTestAssertion(t, legacy, "nik:timezone", "timezone", "Europe/Moscow")
	insertLocalMergeTestAssertion(t, legacy, "nik:language", "language", "Russian")
	if _, err := legacy.db.Exec(`INSERT INTO memory_capsules(id,schema_version,source_host,conversation_scope,source_timestamp,kind,redacted_summary,confidence,evidence_hint,privacy_tier,retention,tags,created_at,status,event_id,content_digest) VALUES('pulse:legacy','pulse.memory_capsule.v1','codex','project_context','2025-02-01T00:00:00Z','decision','Legacy capsule',1,'user_confirmed','private','project','[]','2025-02-01T00:00:00Z','active',?,NULL)`, uniqueID); err != nil {
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
	if preview.Totals.EventsCreated != 1 || preview.Totals.EventsDeduplicated != 1 || preview.Totals.CapsulesCreated != 2 || preview.Totals.AssertionsCreated != 1 || preview.Totals.EmbeddingsCreated != 2 {
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
	var eventCount, capsuleCount, linkedCapsuleCount, archiveProjections, joyCount, assertionCount, timezoneCount, orphanAssertionLinks int
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM memory_capsules WHERE redacted_summary='Legacy capsule'`).Scan(&capsuleCount); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM memory_capsules capsule JOIN events event ON event.id=capsule.event_id WHERE capsule.redacted_summary='Legacy capsule' AND event.title='Unique old decision'`).Scan(&linkedCapsuleCount); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM private_semantic_projection_rows WHERE object_id=? AND row_kind='event'`, localMergeArchiveCapsuleID).Scan(&archiveProjections); err != nil {
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
	if eventCount != 2 || capsuleCount != 1 || linkedCapsuleCount != 1 || archiveProjections != 2 || joyCount != 1 || assertionCount != 1 || timezoneCount != 1 {
		t.Fatalf("merged database is incomplete: events=%d capsules=%d linked_capsules=%d archive_projections=%d current_emotion=%d assertions=%d selected_conflict=%d", eventCount, capsuleCount, linkedCapsuleCount, archiveProjections, joyCount, assertionCount, timezoneCount)
	}
	if orphanAssertionLinks != 0 {
		t.Fatal("migrated assertion retained source-local event IDs")
	}
	rebuildTx, err := merged.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuildPrivateSemanticProjectionTx(rebuildTx); err != nil {
		rebuildTx.Rollback()
		t.Fatal(err)
	}
	if err := rebuildTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM private_semantic_projection_rows WHERE object_id=? AND row_kind='event'`, localMergeArchiveCapsuleID).Scan(&archiveProjections); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if archiveProjections != 2 || eventCount != 2 {
		t.Fatalf("semantic rebuild removed imported personal archive: projections=%d events=%d", archiveProjections, eventCount)
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

func TestLocalMergeAddsOnlyUncoveredSafeClaudeMemMaterial(t *testing.T) {
	home := t.TempDir()
	storeID := "store_personal_claude_mem_merge"
	canonicalPath := filepath.Join(home, ".pulse", "vaults", "personal", storeID, "pulse.db")
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := OpenVault(canonicalPath, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := canonical.Close(); err != nil {
		t.Fatal(err)
	}

	legacyPath := filepath.Join(home, ".pulse", "pulse.db")
	legacy, err := Open(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	insertLocalMergeTestEvent(t, legacy, "Already represented", "Old Pulse already processed the first Claude Mem item", "2026-05-23T07:00:14Z")
	for _, item := range []struct{ kind, sourceID string }{
		{"claude-mem", "claude-mem:obs:1"},
		{"claude-mem.summary", "claude-mem:summary:1"},
	} {
		if _, err := legacy.db.Exec(`INSERT INTO observations(source_kind,source_id,content_hash,scope,captured_at,observed_at,actors) VALUES(?,?,?,'shared','2026-05-23T07:00:14Z','2026-05-23T07:00:14Z','[]')`, item.kind, item.sourceID, "hash:"+item.sourceID); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	claudeMemPath := filepath.Join(home, ".claude-mem", "claude-mem.db")
	if err := os.MkdirAll(filepath.Dir(claudeMemPath), 0o700); err != nil {
		t.Fatal(err)
	}
	claudeMem, err := sql.Open("sqlite", claudeMemPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE observations(id INTEGER PRIMARY KEY,memory_session_id TEXT NOT NULL,project TEXT NOT NULL,text TEXT,type TEXT NOT NULL,title TEXT,subtitle TEXT,facts TEXT,narrative TEXT,concepts TEXT,files_read TEXT,files_modified TEXT,prompt_number INTEGER,discovery_tokens INTEGER,created_at TEXT NOT NULL,created_at_epoch INTEGER NOT NULL,content_hash TEXT,generated_by_model TEXT,relevance_count INTEGER,merged_into_project TEXT,agent_type TEXT,agent_id TEXT,metadata TEXT)`,
		`CREATE TABLE session_summaries(id INTEGER PRIMARY KEY,memory_session_id TEXT NOT NULL,project TEXT NOT NULL,request TEXT,investigated TEXT,learned TEXT,completed TEXT,next_steps TEXT,files_read TEXT,files_edited TEXT,notes TEXT,prompt_number INTEGER,discovery_tokens INTEGER,created_at TEXT NOT NULL,created_at_epoch INTEGER NOT NULL,merged_into_project TEXT)`,
		`CREATE TABLE user_prompts(id INTEGER PRIMARY KEY,content_session_id TEXT NOT NULL,prompt_number INTEGER NOT NULL,prompt_text TEXT NOT NULL,created_at TEXT NOT NULL,created_at_epoch INTEGER NOT NULL)`,
	} {
		if _, err := claudeMem.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO observations(id,memory_session_id,project,type,title,subtitle,facts,narrative,concepts,created_at,created_at_epoch,content_hash) VALUES(1,'s1','project','decision','Covered decision','','["covered"]','covered','[]','2026-05-23T07:00:14Z',1,'one')`,
		`INSERT INTO observations(id,memory_session_id,project,type,title,subtitle,facts,narrative,concepts,created_at,created_at_epoch,content_hash) VALUES(2,'s2','project','decision','Keep the operator flow small','One useful decision','["The installer remains optional","/Users/example/private.txt"]','The owner chose one project-bound memory.','["local memory"]','2026-07-01T07:00:14Z',2,'two')`,
		`INSERT INTO observations(id,memory_session_id,project,type,title,subtitle,facts,narrative,concepts,created_at,created_at_epoch,content_hash) VALUES(3,'s3','project','discovery','/Users/example/private.txt','','["/Users/example/secret.key"]','','[]','2026-07-02T07:00:14Z',3,'three')`,
		`INSERT INTO session_summaries(id,memory_session_id,project,completed,next_steps,created_at,created_at_epoch) VALUES(1,'s1','project','covered','covered','2026-05-23T08:15:09Z',1)`,
		`INSERT INTO session_summaries(id,memory_session_id,project,investigated,learned,completed,next_steps,notes,created_at,created_at_epoch) VALUES(2,'s2','project','Compared the local stores','The working memory must fail open','Prepared one safe preview','Check recall in a fresh host','/Users/example/private.txt','2026-07-01T08:15:09Z',2)`,
		`INSERT INTO session_summaries(id,memory_session_id,project,notes,created_at,created_at_epoch) VALUES(3,'s3','project','/Users/example/secret.key','2026-07-02T08:15:09Z',3)`,
		`INSERT INTO user_prompts(id,content_session_id,prompt_number,prompt_text,created_at,created_at_epoch) VALUES(1,'s2',1,'RAW-PROMPT-MUST-NOT-BE-IMPORTED','2026-07-01T08:00:00Z',1)`,
	} {
		if _, err := claudeMem.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := claudeMem.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(claudeMemPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest := sha256.Sum256(before)

	preview, err := BuildLocalMergePreview(home, canonicalPath, storeID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var claudeSource *LocalMergeSource
	for index := range preview.Sources {
		if preview.Sources[index].Kind == "claude_mem" {
			claudeSource = &preview.Sources[index]
			break
		}
	}
	if claudeSource == nil {
		t.Fatal("Claude Mem source missing from preview")
	}
	for key, want := range map[string]int64{
		"observations": 3, "observations_skipped_existing": 1, "observations_created": 1, "observations_skipped_unsafe": 1,
		"session_summaries": 3, "session_summaries_skipped_existing": 1, "session_summaries_created": 1,
		"session_summaries_skipped_unsafe": 1, "session_summary_capsules_created": 1, "user_prompts": 1,
	} {
		if got := claudeSource.Counts[key]; got != want {
			t.Fatalf("Claude Mem count %s=%d, want %d; all=%+v", key, got, want, claudeSource.Counts)
		}
	}
	after, err := os.ReadFile(claudeMemPath)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(after) != beforeDigest {
		t.Fatal("Claude Mem source changed while building preview")
	}

	merged, err := OpenVault(preview.TargetPath, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatal(err)
	}
	defer merged.Close()
	var observationTwo, observationOne, unsafe, summaryCapsule, rawPrompt, brokenLinks int
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM events WHERE tags LIKE '%observation:2%'`).Scan(&observationTwo); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM events WHERE tags LIKE '%observation:1%'`).Scan(&observationOne); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM events WHERE COALESCE(title,'') LIKE '%/Users/%' OR COALESCE(description,'') LIKE '%/Users/%'`).Scan(&unsafe); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM memory_capsules WHERE id='pulse:claude-mem:summary:2' AND event_id IS NOT NULL`).Scan(&summaryCapsule); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM events WHERE COALESCE(description,'') LIKE '%RAW-PROMPT-MUST-NOT-BE-IMPORTED%'`).Scan(&rawPrompt); err != nil {
		t.Fatal(err)
	}
	if err := merged.db.QueryRow(`SELECT COUNT(*) FROM memory_capsules capsule LEFT JOIN events event ON event.id=capsule.event_id WHERE capsule.id='pulse:claude-mem:summary:2' AND event.id IS NULL`).Scan(&brokenLinks); err != nil {
		t.Fatal(err)
	}
	if observationTwo != 1 || observationOne != 0 || unsafe != 0 || summaryCapsule != 1 || rawPrompt != 0 || brokenLinks != 0 {
		t.Fatalf("Claude Mem preview unsafe or incomplete: new=%d covered=%d unsafe=%d capsule=%d raw=%d broken=%d", observationTwo, observationOne, unsafe, summaryCapsule, rawPrompt, brokenLinks)
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
