package store

import (
	"path/filepath"
	"testing"
)

func TestFunFactCandidatesExcludeSecretsPathsAndSensitiveActors(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "fun-facts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	db := s.DB()
	var visibleID, hiddenID int64
	if err := db.QueryRow(`
		INSERT INTO entities(canonical_name, kind, first_seen, last_seen)
		VALUES ('Pulse', 'project', '2026-08-22T00:00:00Z', '2026-08-22T00:00:00Z')
		RETURNING id`).Scan(&visibleID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		INSERT INTO entities(canonical_name, kind, first_seen, last_seen)
		VALUES ('Hidden person', 'person', '2026-08-22T00:00:00Z', '2026-08-22T00:00:00Z')
		RETURNING id`).Scan(&hiddenID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO sensitive_actors(entity_id, policy, added_at, added_by)
		VALUES (?, 'no_capture', '2026-08-22T00:00:00Z', 'test')`, hiddenID); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		entityID   int64
		text       string
		confidence float64
	}{
		{visibleID, "Pulse keeps approved memory local.", 1},
		{visibleID, "/Users/private/secret.txt", .9},
		{visibleID, "token=ghp_secret", .8},
		{hiddenID, "A hidden person's private fact.", .7},
	} {
		if _, err := db.Exec(`
			INSERT INTO facts(entity_id, text, confidence, created_at)
			VALUES (?, ?, ?, '2026-08-22T00:00:00Z')`, row.entityID, row.text, row.confidence); err != nil {
			t.Fatal(err)
		}
	}

	candidates, err := s.FunFactCandidates(6)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != "Pulse keeps approved memory local." {
		t.Fatalf("unsafe fun facts escaped filtering: %#v", candidates)
	}
}

func TestFunFactCandidatesClampLimit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "fun-facts-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var entityID int64
	if err := s.DB().QueryRow(`
		INSERT INTO entities(canonical_name, kind, first_seen, last_seen)
		VALUES ('Pulse', 'project', '2026-08-22T00:00:00Z', '2026-08-22T00:00:00Z')
		RETURNING id`).Scan(&entityID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 8; index++ {
		if _, err := s.DB().Exec(`
			INSERT INTO facts(entity_id, text, confidence, created_at)
			VALUES (?, printf('Approved fact number %d.', ?), 1, '2026-08-22T00:00:00Z')`, entityID, index); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := s.FunFactCandidates(99)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 6 {
		t.Fatalf("candidate count=%d, want 6", len(candidates))
	}
}
