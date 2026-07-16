package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

func (s *Server) handleProjectSourceRegister(w http.ResponseWriter, r *http.Request) {
	var req store.ProjectSourceRegistration
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.RegisterProjectSource(req, time.Now().UTC())
	if err != nil {
		writeGitMemoryReviewError(w, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleProjectSourceStatus(w http.ResponseWriter, r *http.Request) {
	var req store.ProjectSourceStatusRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.ProjectSourceStatus(req)
	if err != nil {
		writeGitMemoryReviewError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, result)
}

func writeGitMemoryReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrProjectSourceUnavailable):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, store.ErrProjectSourceAuthority), errors.Is(err, store.ErrGitTeamMemoryStaleSource),
		errors.Is(err, store.ErrGitTeamMemoryVersionConflict), errors.Is(err, store.ErrGitTeamMemoryTerminal),
		errors.Is(err, store.ErrGitTeamMemoryConflict), errors.Is(err, store.ErrGitTeamMemoryApprovalUnavailable),
		errors.Is(err, store.ErrGitTeamMemoryApprovalAmbiguous),
		errors.Is(err, store.ErrGitTeamMemoryPublicationConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, "git team memory review error: "+err.Error(), http.StatusBadRequest)
	}
}
