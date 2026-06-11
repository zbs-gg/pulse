package server

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

func (s *Server) handleGraphDelta(w http.ResponseWriter, r *http.Request) {
	var req store.SemanticDelta
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	res, err := s.cfg.Store.SaveSemanticDelta(req)
	if err != nil {
		http.Error(w, "semantic delta error: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Make freshly ingested events retrievable immediately: embed them as
	// search documents and reload the in-memory index. Best-effort — a
	// failed embed must not fail the write, but the response says so.
	if s.cfg.Retrieval != nil && s.cfg.Retrieval.EmbedderReady() &&
		len(res.EventIDs) == len(req.Events) && len(req.Events) > 0 {
		docs := make([]retrieve.IndexEventDoc, 0, len(req.Events))
		for i, ev := range req.Events {
			docs = append(docs, retrieve.IndexEventDoc{
				EventID: res.EventIDs[i],
				Text:    ev.Title + "\n" + ev.Summary,
			})
		}
		indexed := true
		if err := s.cfg.Retrieval.EmbedAndIndexEvents(r.Context(), docs); err != nil {
			log.Printf("graph delta: index events failed: %v", err)
			indexed = false
		}
		res.EventsIndexed = &indexed
	}
	writeJSON(w, res)
}

type graphEntityHideRequest struct {
	ID      int64  `json:"id"`
	Confirm string `json:"confirm"`
}

type graphEntityHideResponse struct {
	OK bool `json:"ok"`
}

func (s *Server) handleGraphEntityHide(w http.ResponseWriter, r *http.Request) {
	var req graphEntityHideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Confirm != "hide graph entity" {
		http.Error(w, "confirm must be \"hide graph entity\"", http.StatusBadRequest)
		return
	}
	if err := s.cfg.Store.HideGraphEntity(req.ID); err != nil {
		http.Error(w, "hide graph entity error: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, graphEntityHideResponse{OK: true})
}

type graphEntityRestoreRequest struct {
	ID      int64  `json:"id"`
	Confirm string `json:"confirm"`
}

type graphEntityRestoreResponse struct {
	OK bool `json:"ok"`
}

func (s *Server) handleGraphEntityRestore(w http.ResponseWriter, r *http.Request) {
	var req graphEntityRestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Confirm != "restore graph entity" {
		http.Error(w, "confirm must be \"restore graph entity\"", http.StatusBadRequest)
		return
	}
	if err := s.cfg.Store.RestoreGraphEntity(req.ID); err != nil {
		http.Error(w, "restore graph entity error: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, graphEntityRestoreResponse{OK: true})
}
