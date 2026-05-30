package ingest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nkkmnk/pulse/internal/capture"
	"github.com/nkkmnk/pulse/internal/store"
)

// Handler processes POST /ingest requests.
type Handler struct {
	store *store.Store
}

// NewHandler returns a new Handler backed by the given store.
func NewHandler(s *store.Store) *Handler {
	return &Handler{store: s}
}

type request struct {
	Observations []capture.Observation `json:"observations"`
}

type response struct {
	Inserted   int     `json:"inserted"`
	Duplicates int     `json:"duplicates"`
	Revisions  int     `json:"revisions"`
	IDs        []int64 `json:"ids,omitempty"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	resp := response{}
	for i := range req.Observations {
		obs := &req.Observations[i]
		if err := validate(obs); err != nil {
			http.Error(w, fmt.Sprintf("obs[%d]: %v", i, err), http.StatusBadRequest)
			return
		}
		op, id, err := h.upsert(r.Context(), obs)
		if err != nil {
			http.Error(w, fmt.Sprintf("obs[%d] store: %v", i, err), http.StatusInternalServerError)
			return
		}
		switch op {
		case opInsert:
			resp.Inserted++
		case opDuplicate:
			resp.Duplicates++
		case opRevision:
			resp.Revisions++
		}
		resp.IDs = append(resp.IDs, id)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func validate(obs *capture.Observation) error {
	if obs.SourceKind == "" {
		return fmt.Errorf("source_kind required")
	}
	if obs.SourceID == "" {
		return fmt.Errorf("source_id required")
	}
	if obs.Scope != "assistant" && obs.Scope != "user" && obs.Scope != "shared" {
		return fmt.Errorf("scope must be assistant|user|shared, got %q", obs.Scope)
	}
	if obs.CapturedAt.IsZero() {
		return fmt.Errorf("captured_at required")
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now().UTC()
	}
	if obs.Version == 0 {
		obs.Version = 1
	}
	if obs.ContentHash == "" {
		obs.ContentHash = capture.ComputeContentHash(obs.ContentText, obs.Metadata)
	}
	return nil
}

type op int

const (
	opInsert op = iota
	opDuplicate
	opRevision
)
