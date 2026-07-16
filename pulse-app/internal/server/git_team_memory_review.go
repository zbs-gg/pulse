package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/store"
)

func (s *Server) handleGitTeamMemoryStage(w http.ResponseWriter, r *http.Request) {
	var req store.GitTeamMemoryStageRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.StageGitTeamMemoryReview(req, time.Now().UTC())
	if err != nil {
		writeGitMemoryReviewError(w, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleGitTeamMemoryInspect(w http.ResponseWriter, r *http.Request) {
	var req store.GitTeamMemoryInspectRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.InspectGitTeamMemoryReview(req)
	if err != nil {
		writeGitMemoryReviewError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, result)
}

func (s *Server) handleGitTeamMemoryEdit(w http.ResponseWriter, r *http.Request) {
	var req store.GitTeamMemoryEditRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if routeID := chi.URLParam(r, "id"); routeID == "" || routeID != req.CandidateID {
		http.Error(w, "bad request: candidate_id does not match route", http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.EditGitTeamMemoryCandidate(req, time.Now().UTC())
	if err != nil {
		writeGitMemoryReviewError(w, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleGitTeamMemoryReject(w http.ResponseWriter, r *http.Request) {
	var req store.GitTeamMemoryRejectRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if routeID := chi.URLParam(r, "id"); routeID == "" || routeID != req.CandidateID {
		http.Error(w, "bad request: candidate_id does not match route", http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.RejectGitTeamMemoryCandidate(req, time.Now().UTC())
	if err != nil {
		writeGitMemoryReviewError(w, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleGitTeamMemoryPresent(w http.ResponseWriter, r *http.Request) {
	var req store.GitTeamMemoryPresentationRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.PresentGitTeamMemoryCards(req, time.Now().UTC())
	if err != nil {
		writeGitMemoryReviewError(w, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleGitTeamMemoryExactOK(w http.ResponseWriter, r *http.Request) {
	var req store.GitTeamMemoryExactOKRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.ApproveExactGitTeamMemoryOK(req, time.Now().UTC())
	if err != nil {
		writeGitMemoryReviewError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, result)
}
