package ingest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/capture"
	"github.com/nkkmnk/pulse/internal/store"
)

func decodeResp(t *testing.T, body []byte) (resp struct{ Inserted, Duplicates, Revisions int }) {
	t.Helper()
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nbody=%s", err, string(body))
	}
	return resp
}

func timeParse(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time parse %q: %v", s, err)
	}
	return tt
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIngestInsertsNewObservation(t *testing.T) {
	s := openTestStore(t)
	h := NewHandler(s)

	obs := capture.Observation{
		SourceKind: "tg", SourceID: "m:1", ContentHash: "h1", Version: 1,
		Scope:       "user",
		CapturedAt:  timeParse(t, "2026-04-15T00:00:00Z"),
		ObservedAt:  timeParse(t, "2026-04-15T00:00:01Z"),
		Actors:      []capture.ActorRef{{Kind: "tg_user", ID: "123"}},
		ContentText: "hello",
	}
	body, _ := json.Marshal(map[string]any{"observations": []capture.Observation{obs}})

	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}

	resp := decodeResp(t, rec.Body.Bytes())
	if resp.Inserted != 1 {
		t.Errorf("inserted: got %d", resp.Inserted)
	}
}

func TestIngestDedupsIdenticalHash(t *testing.T) {
	s := openTestStore(t)
	h := NewHandler(s)

	obs := capture.Observation{
		SourceKind: "tg", SourceID: "m:1", ContentHash: "h1", Version: 1,
		Scope:       "user",
		CapturedAt:  timeParse(t, "2026-04-15T00:00:00Z"),
		ObservedAt:  timeParse(t, "2026-04-15T00:00:01Z"),
		Actors:      []capture.ActorRef{{Kind: "tg_user", ID: "123"}},
		ContentText: "hello",
	}
	body, _ := json.Marshal(map[string]any{"observations": []capture.Observation{obs}})

	// First ingest
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	h.ServeHTTP(httptest.NewRecorder(), req)

	// Second ingest — identical
	req2 := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	resp := decodeResp(t, rec2.Body.Bytes())
	if resp.Duplicates != 1 {
		t.Errorf("duplicates: got %d", resp.Duplicates)
	}
	if resp.Inserted != 0 {
		t.Errorf("inserted should be 0, got %d", resp.Inserted)
	}
}

func TestIngestEnqueuesExtractionJob(t *testing.T) {
	s := openTestStore(t)
	h := NewHandler(s)

	obs := capture.Observation{
		SourceKind: "tg", SourceID: "m:99", ContentHash: "h1", Version: 1,
		Scope:       "user",
		CapturedAt:  timeParse(t, "2026-04-15T00:00:00Z"),
		ObservedAt:  timeParse(t, "2026-04-15T00:00:01Z"),
		Actors:      []capture.ActorRef{{Kind: "tg_user", ID: "123"}},
		ContentText: "meaningful",
	}
	body, _ := json.Marshal(map[string]any{"observations": []capture.Observation{obs}})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body)))

	var jobCount int
	s.DB().QueryRow(`SELECT COUNT(*) FROM extraction_jobs WHERE state='pending'`).Scan(&jobCount)
	if jobCount != 1 {
		t.Errorf("expected 1 pending job, got %d", jobCount)
	}
}

func TestIngestRevisionOnHashChange(t *testing.T) {
	s := openTestStore(t)
	h := NewHandler(s)

	base := capture.Observation{
		SourceKind: "tg", SourceID: "m:1", ContentHash: "h1", Version: 1,
		Scope:       "user",
		CapturedAt:  timeParse(t, "2026-04-15T00:00:00Z"),
		ObservedAt:  timeParse(t, "2026-04-15T00:00:01Z"),
		Actors:      []capture.ActorRef{{Kind: "tg_user", ID: "123"}},
		ContentText: "hello",
	}
	body1, _ := json.Marshal(map[string]any{"observations": []capture.Observation{base}})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body1)))

	// Edited: content changed → new hash
	edited := base
	edited.ContentText = "hello world"
	edited.ContentHash = "h2"

	body2, _ := json.Marshal(map[string]any{"observations": []capture.Observation{edited}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body2)))

	resp := decodeResp(t, rec.Body.Bytes())
	if resp.Revisions != 1 {
		t.Errorf("revisions: got %d", resp.Revisions)
	}

	// Check DB: version=2 exists, observation_revisions has one row
	var maxVer int
	if err := s.DB().QueryRow(`SELECT MAX(version) FROM observations WHERE source_kind='tg' AND source_id='m:1'`).Scan(&maxVer); err != nil {
		t.Fatalf("scan max version: %v", err)
	}
	if maxVer != 2 {
		t.Errorf("max version: got %d", maxVer)
	}
	var revCount int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM observation_revisions`).Scan(&revCount); err != nil {
		t.Fatalf("scan rev count: %v", err)
	}
	if revCount != 1 {
		t.Errorf("revisions row count: got %d", revCount)
	}
}
