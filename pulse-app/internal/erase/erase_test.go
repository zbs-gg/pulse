package erase

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSoftEraseEntity(t *testing.T) {
	s := openTestStore(t)
	db := s.DB()

	// Fixture: one entity with two observations linked via evidence
	res, err := db.Exec(`INSERT INTO entities (canonical_name, kind, first_seen, last_seen) VALUES ('Alice','person','2026-04-15T00:00:00Z','2026-04-15T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	entID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("entity last id: %v", err)
	}

	res1, err := db.Exec(`INSERT INTO observations (source_kind, source_id, content_hash, version, scope, captured_at, observed_at, actors, content_text) VALUES ('tg','m:1','h1',1,'user','2026-04-15T00:00:00Z','2026-04-15T00:00:00Z','[]','private message 1')`)
	if err != nil {
		t.Fatal(err)
	}
	obsID1, err := res1.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	res2, err := db.Exec(`INSERT INTO observations (source_kind, source_id, content_hash, version, scope, captured_at, observed_at, actors, content_text) VALUES ('tg','m:2','h2',1,'user','2026-04-15T00:00:00Z','2026-04-15T00:00:00Z','[]','private message 2')`)
	if err != nil {
		t.Fatal(err)
	}
	obsID2, err := res2.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO evidence (subject_kind, subject_id, observation_id, created_at) VALUES ('entity',?,?,'2026-04-15T00:00:00Z'), ('entity',?,?,'2026-04-15T00:00:00Z')`,
		entID, obsID1, entID, obsID2)
	if err != nil {
		t.Fatal(err)
	}

	// Act
	e := NewEraser(s)
	err = e.SoftErase(context.Background(), entID, "admin", "user request")
	if err != nil {
		t.Fatalf("soft erase: %v", err)
	}

	// Assert: content_text is NULL, redacted=1 for both observations
	var redactedCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations WHERE redacted=1 AND content_text IS NULL`).Scan(&redactedCount); err != nil {
		t.Fatalf("query redacted count: %v", err)
	}
	if redactedCount != 2 {
		t.Errorf("expected 2 redacted, got %d", redactedCount)
	}

	// Evidence preserved (row still there)
	var evCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM evidence WHERE subject_id=?`, entID).Scan(&evCount); err != nil {
		t.Fatalf("query evidence count: %v", err)
	}
	if evCount != 2 {
		t.Errorf("evidence should be preserved, got %d", evCount)
	}

	// erasure_log row present
	var logCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM erasure_log WHERE op_kind='soft'`).Scan(&logCount); err != nil {
		t.Fatalf("query erasure_log count: %v", err)
	}
	if logCount != 1 {
		t.Errorf("expected 1 erasure_log row, got %d", logCount)
	}
}

func TestHardEraseEntity(t *testing.T) {
	s := openTestStore(t)
	db := s.DB()

	res, err := db.Exec(`INSERT INTO entities (canonical_name, kind, first_seen, last_seen) VALUES ('Bob','person','2026-04-15T00:00:00Z','2026-04-15T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	entID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	obsRes, err := db.Exec(`INSERT INTO observations (source_kind, source_id, content_hash, version, scope, captured_at, observed_at, actors, content_text) VALUES ('tg','m:1','h1',1,'user','2026-04-15T00:00:00Z','2026-04-15T00:00:00Z','[]','private')`)
	if err != nil {
		t.Fatal(err)
	}
	obsID, err := obsRes.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO evidence (subject_kind, subject_id, observation_id, created_at) VALUES ('entity',?,?,'2026-04-15T00:00:00Z')`, entID, obsID)
	if err != nil {
		t.Fatal(err)
	}

	e := NewEraser(s)
	if err := e.HardErase(context.Background(), entID, "admin", "GDPR request"); err != nil {
		t.Fatalf("hard erase: %v", err)
	}

	var obsCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&obsCount); err != nil {
		t.Fatalf("query observations: %v", err)
	}
	if obsCount != 0 {
		t.Errorf("expected 0 observations, got %d", obsCount)
	}

	var evCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM evidence`).Scan(&evCount); err != nil {
		t.Fatalf("query evidence: %v", err)
	}
	if evCount != 0 {
		t.Errorf("expected 0 evidence rows (cascade), got %d", evCount)
	}

	var logCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM erasure_log WHERE op_kind='hard'`).Scan(&logCount); err != nil {
		t.Fatalf("query erasure_log: %v", err)
	}
	if logCount != 1 {
		t.Errorf("expected 1 hard erasure_log, got %d", logCount)
	}
}
