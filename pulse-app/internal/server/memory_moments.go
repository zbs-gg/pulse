package server

import (
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/store"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) handleMemoryMomentWrite(w http.ResponseWriter, r *http.Request) {
	var req store.TurnFinalizeRequest
	if err := decodeStrictJSONBody(r, &req, 16<<20, errors.New("moment exceeds 16 MiB transport envelope; nothing accepted; split into linked requests")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var authority *productBindingAuthority
	binding, repository, epoch := req.BindingDigest, "", req.ResolverEpoch
	if s.cfg.ProductBindingVerifier != nil {
		verified, ok := s.requireProductBindingAuthority(w, r)
		if !ok {
			return
		}
		authority = &verified
		binding, repository, epoch = verified.BindingDigest, verified.RepositoryID, verified.ResolverEpoch
	}
	result, err := s.cfg.Store.FinalizeMemoryMoment(req, time.Now().UTC(), binding, repository, epoch)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	// All candidates are durable before materialization. Startup recovery handles
	// an interrupted request without rerunning the model or inventing new turns.
	result = s.commitTurnResultNowForAuthority(result, authority)
	writeJSON(w, result)
}

func (s *Server) handleMemoryMomentRead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	binding, repository := s.cfg.Store.MemoryMomentLocalBinding(), ""
	if s.cfg.ProductBindingVerifier != nil {
		verified, ok := s.requireProductBindingAuthority(w, r)
		if !ok {
			return
		}
		binding, repository = verified.BindingDigest, verified.RepositoryID
	}
	id := chi.URLParam(r, "id")
	if r.URL.Query().Get("status") == "true" {
		result, err := s.cfg.Store.MemoryMomentStatus(id, binding)
		if err != nil {
			writeMemoryTrayError(w, err)
			return
		}
		writeJSON(w, result)
		return
	}
	cursor, err := strconv.Atoi(r.URL.Query().Get("cursor"))
	if r.URL.Query().Get("cursor") == "" {
		cursor = 0
		err = nil
	}
	if err != nil {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	page, err := s.cfg.Store.ReadMemoryMoment(id, binding, repository, cursor)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	writeJSON(w, page)
}
