package ingest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkkmnk/pulse/internal/capture"
)

func TestSensitiveActorRedactContentPolicy(t *testing.T) {
	s := openTestStore(t)
	db := s.DB()

	// Seed: entity "Sam" with tg identity 42, policy=redact_content
	_, _ = db.Exec(`INSERT INTO entities (canonical_name, kind, first_seen, last_seen) VALUES ('Sam','person','2026-04-15T00:00:00Z','2026-04-15T00:00:00Z')`)
	_, _ = db.Exec(`INSERT INTO entity_identities (entity_id, source_kind, identifier, first_seen) VALUES (1,'telegram','42','2026-04-15T00:00:00Z')`)
	_, _ = db.Exec(`INSERT INTO sensitive_actors (entity_id, policy, added_at, added_by) VALUES (1,'redact_content','2026-04-15T00:00:00Z','admin')`)

	h := NewHandler(s)
	obs := capture.Observation{
		SourceKind: "telegram", SourceID: "m:555", ContentHash: "h1", Version: 1,
		Scope:       "user",
		CapturedAt:  timeParse(t, "2026-04-15T00:00:00Z"),
		ObservedAt:  timeParse(t, "2026-04-15T00:00:01Z"),
		Actors:      []capture.ActorRef{{Kind: "telegram", ID: "42"}},
		ContentText: "intimate content here",
	}
	body, _ := json.Marshal(map[string]any{"observations": []capture.Observation{obs}})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body)))

	var stored string
	var redacted int
	db.QueryRow(`SELECT COALESCE(content_text,''), redacted FROM observations WHERE source_id='m:555'`).Scan(&stored, &redacted)

	if redacted != 1 {
		t.Errorf("expected redacted=1, got %d", redacted)
	}
	if stored == "intimate content here" {
		t.Errorf("content should be redacted, got %q", stored)
	}
}

func TestSensitiveActorNoCapturePolicy(t *testing.T) {
	s := openTestStore(t)
	db := s.DB()

	_, _ = db.Exec(`INSERT INTO entities (canonical_name, kind, first_seen, last_seen) VALUES ('Bob','person','2026-04-15T00:00:00Z','2026-04-15T00:00:00Z')`)
	_, _ = db.Exec(`INSERT INTO entity_identities (entity_id, source_kind, identifier, first_seen) VALUES (1,'telegram','99','2026-04-15T00:00:00Z')`)
	_, _ = db.Exec(`INSERT INTO sensitive_actors (entity_id, policy, added_at, added_by) VALUES (1,'no_capture','2026-04-15T00:00:00Z','admin')`)

	h := NewHandler(s)
	obs := capture.Observation{
		SourceKind: "telegram", SourceID: "m:556", ContentHash: "h2", Version: 1,
		Scope:       "user",
		CapturedAt:  timeParse(t, "2026-04-15T00:00:00Z"),
		ObservedAt:  timeParse(t, "2026-04-15T00:00:01Z"),
		Actors:      []capture.ActorRef{{Kind: "telegram", ID: "99"}},
		ContentText: "private message",
	}
	body, _ := json.Marshal(map[string]any{"observations": []capture.Observation{obs}})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body)))

	var stored string
	var redacted int
	db.QueryRow(`SELECT COALESCE(content_text,''), redacted FROM observations WHERE source_id='m:556'`).Scan(&stored, &redacted)

	if redacted != 1 {
		t.Errorf("no_capture: expected redacted=1, got %d", redacted)
	}
	if stored == "private message" {
		t.Errorf("no_capture: content should be wiped, got %q", stored)
	}
}

func TestNonSensitiveActorUnaffected(t *testing.T) {
	s := openTestStore(t)
	h := NewHandler(s)

	obs := capture.Observation{
		SourceKind: "telegram", SourceID: "m:557", ContentHash: "h3", Version: 1,
		Scope:       "user",
		CapturedAt:  timeParse(t, "2026-04-15T00:00:00Z"),
		ObservedAt:  timeParse(t, "2026-04-15T00:00:01Z"),
		Actors:      []capture.ActorRef{{Kind: "telegram", ID: "777"}},
		ContentText: "public message",
	}
	body, _ := json.Marshal(map[string]any{"observations": []capture.Observation{obs}})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body)))

	var stored string
	var redacted int
	s.DB().QueryRow(`SELECT COALESCE(content_text,''), redacted FROM observations WHERE source_id='m:557'`).Scan(&stored, &redacted)

	if redacted != 0 {
		t.Errorf("non-sensitive: expected redacted=0, got %d", redacted)
	}
	if stored != "public message" {
		t.Errorf("non-sensitive: content should be unchanged, got %q", stored)
	}
}
