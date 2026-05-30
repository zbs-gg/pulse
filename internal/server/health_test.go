package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/health"
)

func newHealthTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	provider := health.NewFixtureProvider(
		time.Date(2026, 4, 29, 17, 30, 0, 0, time.UTC),
	)
	srv, err := New(Config{
		IPCSecret: "secret",
		Health:    provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(srv.Handler())
}

func TestHealthSnapshotRequiresAuth(t *testing.T) {
	ts := newHealthTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHealthSnapshotReturnsTodayByDefault(t *testing.T) {
	ts := newHealthTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/health/snapshot", nil)
	req.Header.Set("X-Pulse-Key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var snap health.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Today fixture is the bad-recovery day.
	if snap.HRV >= 50 {
		t.Errorf("expected today HRV < 50, got %d", snap.HRV)
	}
	if snap.Source != "mock" {
		t.Errorf("expected source=mock, got %q", snap.Source)
	}
	if snap.SleepHoursLast == 0 {
		t.Errorf("expected non-zero sleep_hours_last")
	}
}

func TestHealthSnapshotDaysParam(t *testing.T) {
	ts := newHealthTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/health/snapshot?days=3", nil)
	req.Header.Set("X-Pulse-Key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var snaps []health.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snaps); err != nil {
		t.Fatalf("decode array: %v", err)
	}
	if len(snaps) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(snaps))
	}
	// Trend check: today (index 0) worse than -2d (index 2).
	if snaps[0].HRV >= snaps[2].HRV {
		t.Errorf("trend broken: today HRV %d should be < -2d HRV %d",
			snaps[0].HRV, snaps[2].HRV)
	}
}

func TestHealthSnapshotDaysOneStillSingleObject(t *testing.T) {
	// days=1 should match the default shape (single object, not [obj]).
	// Heart + simple consumers would otherwise need to special-case.
	ts := newHealthTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/health/snapshot?days=1", nil)
	req.Header.Set("X-Pulse-Key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 || body[0] != '{' {
		t.Errorf("expected single JSON object, got %s", string(body))
	}
}

func TestHealthSnapshotInvalidDaysFallsBack(t *testing.T) {
	ts := newHealthTestServer(t)
	defer ts.Close()

	for _, v := range []string{"abc", "0", "-3"} {
		req, _ := http.NewRequest("GET", ts.URL+"/health/snapshot?days="+v, nil)
		req.Header.Set("X-Pulse-Key", "secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("days=%s: %v", v, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("days=%s: expected 200, got %d", v, resp.StatusCode)
		}
	}
}

func TestHealthSnapshotDaysOver4Capped(t *testing.T) {
	ts := newHealthTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/health/snapshot?days=99", nil)
	req.Header.Set("X-Pulse-Key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var snaps []health.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snaps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(snaps) != 4 {
		t.Errorf("expected cap at 4, got %d", len(snaps))
	}
}

func TestHealthSnapshotReturns503WhenNotConfigured(t *testing.T) {
	srv, err := New(Config{IPCSecret: "secret"}) // no Health
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/health/snapshot", nil)
	req.Header.Set("X-Pulse-Key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestHealthSnapshotShapeMatchesHeartUserState(t *testing.T) {
	// Sanity-check field names land on the wire under the names Heart's
	// chat client expects in api.ts UserState. Catches accidental
	// json-tag drift.
	ts := newHealthTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/health/snapshot", nil)
	req.Header.Set("X-Pulse-Key", "secret")
	resp, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)

	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, key := range []string{
		"hrv", "stress_proxy", "sleep_quality", "sleep_hours_last",
		"sleep_hours_avg_7d", "steps_today", "last_workout_days",
		"hr_trend", "hrv_trend", "timestamp", "source",
	} {
		if _, ok := generic[key]; !ok {
			t.Errorf("missing key %q in response: %s", key, string(body))
		}
	}
}
