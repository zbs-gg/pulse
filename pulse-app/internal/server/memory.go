package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
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
}

func (s *Server) handleMemoryRemember(w http.ResponseWriter, r *http.Request) {
	var capsule store.MemoryCapsule
	if err := json.NewDecoder(r.Body).Decode(&capsule); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	ids, err := s.cfg.Store.RememberCapsule(capsule)
	if err != nil {
		http.Error(w, "invalid memory capsule: "+err.Error(), http.StatusBadRequest)
		return
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
		billing.Host = "claude-code"
	}
	if billing.StoragePath == "" {
		billing.StoragePath = s.cfg.Store.DBPath()
	}
	writeJSON(w, statusResponse{
		BillingMode:       billing.Mode,
		Host:              billing.Host,
		BackendLLMEnabled: billing.BackendLLMEnabled,
		RawCaptureEnabled: billing.RawCaptureEnabled,
		StoragePath:       billing.StoragePath,
		Schema:            store.MemoryCapsuleSchema,
		ItemCount:         storeStatus.ItemCount,
		LastWrite:         storeStatus.LastWrite,
	})
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
	id := chi.URLParam(r, "id")
	if id == "" {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		id = req.ID
	}
	if err := s.cfg.Store.DeleteMemory(strings.TrimSpace(id)); err != nil {
		http.Error(w, "memory delete error: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMemoryWipe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Confirm != "wipe pulse memory" {
		http.Error(w, "memory wipe requires confirm=\"wipe pulse memory\"", http.StatusBadRequest)
		return
	}
	if err := s.cfg.Store.WipeMemory(); err != nil {
		http.Error(w, "memory wipe error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
