package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

type rememberResponse struct {
	OK  bool     `json:"ok"`
	IDs []string `json:"ids"`
}

type recallResponse struct {
	Items []store.RecalledMemoryItem `json:"items"`
}

type statusResponse struct {
	BillingMode       string `json:"billing_mode"`
	Host              string `json:"host"`
	BackendLLMEnabled bool   `json:"backend_llm_enabled"`
	RawCaptureEnabled bool   `json:"raw_capture_enabled"`
	StoragePath       string `json:"storage_path"`
	Schema            string `json:"schema"`
	ItemCount         int    `json:"item_count"`
	LastWrite         string `json:"last_write,omitempty"`
	CaptureEnabled    bool   `json:"capture_enabled"`
	CaptureState      string `json:"capture_state"`
	// FullRetrieval is true only when the state-aware retrieval engine is
	// running with an embedder. False means fallback memory only — callers
	// must not present this as full Pulse.
	FullRetrieval                 bool   `json:"full_retrieval"`
	Embedder                      string `json:"embedder,omitempty"`
	ConsolidationReportsAvailable bool   `json:"consolidation_reports_available"`
	ConsolidationReportsState     string `json:"consolidation_reports_state"`
	MemorySnapshotDigest          string `json:"memory_snapshot_digest,omitempty"`
}

func (s *Server) handleMemoryRemember(w http.ResponseWriter, r *http.Request) {
	var capsule store.MemoryCapsule
	var decodeErr error
	if s.cfg.Store.StoreKind() == store.StoreKindPersonal || s.cfg.Store.StoreKind() == store.StoreKindDesk {
		decodeErr = decodeMemoryTrayBody(r, &capsule)
	} else {
		decodeErr = json.NewDecoder(r.Body).Decode(&capsule)
	}
	if decodeErr != nil {
		http.Error(w, "bad request: "+decodeErr.Error(), http.StatusBadRequest)
		return
	}
	if s.cfg.Store.StoreKind() == store.StoreKindPersonal || s.cfg.Store.StoreKind() == store.StoreKindDesk {
		result, err := s.cfg.Store.PrepareManualMemoryCapsuleWithInvocation(
			capsule, r.Header.Get("Idempotency-Key"), time.Now().UTC(), s.cfg.TrayGracePeriod,
		)
		if err != nil {
			writeMemoryTrayError(w, err)
			return
		}
		result = s.commitTurnResultNow(result)
		writeJSON(w, result)
		return
	}
	ids, err := s.cfg.Store.RememberCapsule(capsule)
	if err != nil {
		http.Error(w, "invalid memory capsule: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Make projected capsule events retrievable immediately: embed them as
	// search documents and reload the in-memory index (same pattern as the
	// /graph/delta handler). Best-effort — a failed embed must not fail the
	// write; engine nil / embedder off ⇒ skip silently (the events exist and
	// stay dark until an embedder is configured).
	if s.cfg.Retrieval != nil && s.cfg.Retrieval.EmbedderReady() {
		if docs, derr := s.cfg.Store.CapsuleEventDocs(ids); derr == nil && len(docs) > 0 {
			indexDocs := make([]retrieve.IndexEventDoc, len(docs))
			for i, d := range docs {
				indexDocs[i] = retrieve.IndexEventDoc{EventID: d.EventID, Text: d.Text}
			}
			if err := s.cfg.Retrieval.EmbedAndIndexEvents(r.Context(), indexDocs); err != nil {
				log.Printf("memory remember: index capsule events failed: %v", err)
			}
		}
	}
	writeJSON(w, rememberResponse{OK: true, IDs: ids})
}

func (s *Server) handleMemoryRecall(w http.ResponseWriter, r *http.Request) {
	var req store.RecallMemoryQuery
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.cfg.Store.RecallMemory(req)
	if err != nil {
		http.Error(w, "memory recall error: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, recallResponse{Items: items})
}

func (s *Server) handleMemoryStatus(w http.ResponseWriter, r *http.Request) {
	storeStatus, err := s.cfg.Store.MemoryStatus()
	if err != nil {
		http.Error(w, "memory status error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	billing := s.cfg.Billing
	if billing.Mode == "" {
		billing.Mode = "host-extracted"
	}
	if billing.Host == "" {
		billing.Host = "pulse-product"
	}
	if billing.StoragePath == "" {
		billing.StoragePath = s.cfg.Store.DBPath()
	}
	fullRetrieval := s.cfg.Retrieval != nil && s.cfg.Retrieval.EmbedderReady()
	embedder := ""
	if fullRetrieval {
		embedder = s.cfg.Retrieval.EmbedderModel()
	}
	captureEnabled, captureState := readCaptureState(s.cfg.Store.DBPath())
	consolidationState := "unavailable"
	if s.consolidationUnavailable != "" {
		consolidationState = s.consolidationUnavailable
	} else if s.consolidationReports != nil {
		consolidationState = "ready"
	}
	memorySnapshotDigest := ""
	if scope, scoped, scopeErr := s.cfg.Store.CurrentPersonalMemoryScopeSnapshot(); scopeErr != nil {
		http.Error(w, "memory status error: "+scopeErr.Error(), http.StatusInternalServerError)
		return
	} else if scoped {
		memorySnapshotDigest = scope.Digest()
	}
	writeJSON(w, statusResponse{
		BillingMode:                   billing.Mode,
		Host:                          billing.Host,
		BackendLLMEnabled:             billing.BackendLLMEnabled,
		RawCaptureEnabled:             billing.RawCaptureEnabled,
		StoragePath:                   billing.StoragePath,
		Schema:                        store.MemoryCapsuleSchema,
		ItemCount:                     storeStatus.ItemCount,
		LastWrite:                     storeStatus.LastWrite,
		CaptureEnabled:                captureEnabled,
		CaptureState:                  captureState,
		FullRetrieval:                 fullRetrieval,
		Embedder:                      embedder,
		ConsolidationReportsAvailable: s.consolidationReports != nil && s.consolidationUnavailable == "",
		ConsolidationReportsState:     consolidationState,
		MemorySnapshotDigest:          memorySnapshotDigest,
	})
}

func readCaptureState(dbPath string) (bool, string) {
	body, err := os.ReadFile(filepath.Join(filepath.Dir(dbPath), "capture-state.json"))
	if os.IsNotExist(err) {
		return true, "enabled"
	}
	if err != nil {
		return false, "invalid"
	}
	var state struct {
		Schema  string `json:"schema"`
		Enabled *bool  `json:"enabled"`
	}
	if json.Unmarshal(body, &state) != nil || state.Schema != "pulse.capture_state.v1" || state.Enabled == nil {
		return false, "invalid"
	}
	if *state.Enabled {
		return true, "enabled"
	}
	return false, "disabled"
}

func (s *Server) handleMemoryExport(w http.ResponseWriter, r *http.Request) {
	out, err := s.cfg.Store.ExportMemory()
	if err != nil {
		http.Error(w, "memory export error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleMemoryImport(w http.ResponseWriter, r *http.Request) {
	var in store.MemoryExport
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	ids, err := s.cfg.Store.ImportMemory(in)
	if err != nil {
		http.Error(w, "memory import error: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, rememberResponse{OK: true, IDs: ids})
}

func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Store.StoreKind() == store.StoreKindPersonal || s.cfg.Store.StoreKind() == store.StoreKindDesk {
		http.Error(w, "product memory deletion requires fresh OS-backed user presence; the privileged local deletion surface is not active", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		var req struct {
			ID string `json:"id"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		id = req.ID
	}
	id = strings.TrimSpace(id)
	if err := s.cfg.Store.DeleteMemory(id); err != nil {
		http.Error(w, "memory delete error: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "deleted_id": id})
}

// handleMemoryConsolidate runs an explicit, opt-in near-duplicate capsule
// consolidation pass (invalidate-not-delete). It is never on the write or
// retrieve hot path; the client must call it deliberately. Default is a dry run
// unless the body sets dry_run=false.
func (s *Server) handleMemoryConsolidate(w http.ResponseWriter, r *http.Request) {
	// Safe default: dry-run unless the caller explicitly sends {"dry_run": false}.
	// An empty body (io.EOF) leaves DryRun=true, so a bodyless POST never mutates.
	opt := store.ConsolidateOptions{DryRun: true}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&opt); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	result, err := s.cfg.Store.ConsolidateCapsules(opt)
	if err != nil {
		if errors.Is(err, store.ErrMemoryTrayRequired) {
			writeMemoryTrayError(w, err)
			return
		}
		http.Error(w, "memory consolidate error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleMemoryWipe(w http.ResponseWriter, r *http.Request) {
	// A confirmation phrase carried by the same HTTP caller is not user
	// presence. Agents can type it, wrap the CLI in a pseudo-terminal, or call
	// this route directly. Product vaults therefore fail closed until the
	// privileged local surface wires a fresh OS-backed vault.wipe assertion.
	// Local Preview retains its explicit confirmation contract below.
	if s.cfg.Store.StoreKind() == store.StoreKindPersonal || s.cfg.Store.StoreKind() == store.StoreKindDesk {
		http.Error(w, "product memory wipe requires fresh OS-backed user presence; the privileged local wipe surface is not active", http.StatusForbidden)
		return
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	decodeErr := json.NewDecoder(r.Body).Decode(&req)
	if decodeErr != nil {
		http.Error(w, "bad request: "+decodeErr.Error(), http.StatusBadRequest)
		return
	}
	if req.Confirm != "wipe pulse memory" {
		http.Error(w, "memory wipe requires confirm=\"wipe pulse memory\"", http.StatusBadRequest)
		return
	}
	wipeErr := s.cfg.Store.WipeMemory()
	if wipeErr != nil {
		if errors.Is(wipeErr, store.ErrMemoryTrayRequired) {
			writeMemoryTrayError(w, wipeErr)
			return
		}
		http.Error(w, "memory wipe error: "+wipeErr.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
