package server

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// handleHealthSnapshot serves GET /health/snapshot.
//
// Default: returns the latest snapshot as a single JSON object.
//
// With `?days=N` (1..7): returns a JSON array of N snapshots, index 0 =
// today, ascending into the past. Bad/out-of-range values fall back to
// 1 (single object response is preserved when days is omitted; days=1
// also returns a single object so callers don't have to special-case).
//
// Auth: routed through the standard authMiddleware (X-Pulse-Key) — same
// model as /retrieve. No separate token.
//
// In M0 the data is fixture-only (Source="mock"); replace s.cfg.Health
// with a real provider when the Apple Health bridge lands.
func (s *Server) handleHealthSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Health == nil {
		http.Error(w, "health provider not configured", http.StatusServiceUnavailable)
		return
	}

	all := s.cfg.Health.Days()
	if len(all) == 0 {
		http.Error(w, "no health data", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	daysParam := r.URL.Query().Get("days")
	if daysParam == "" {
		// Default: just today's snapshot, single object.
		_ = json.NewEncoder(w).Encode(all[0])
		return
	}

	n, err := strconv.Atoi(daysParam)
	if err != nil || n <= 0 {
		// Same convention as /outbox?limit=...: fall back silently
		// rather than 400. Matches existing handler ergonomics.
		n = 1
	}
	if n > len(all) {
		n = len(all)
	}

	if n == 1 {
		_ = json.NewEncoder(w).Encode(all[0])
		return
	}
	_ = json.NewEncoder(w).Encode(all[:n])
}
