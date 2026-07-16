package server

import (
	"context"
	"net/http"
	"time"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

func (s *Server) handleGitTeamMemoryIndex(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Retrieval == nil || !s.cfg.Retrieval.EmbedderReady() {
		http.Error(w, "git team memory retrieval is not configured", http.StatusServiceUnavailable)
		return
	}
	var req store.GitTeamMemoryIndexRequest
	if err := decodeStrictJSONBody(r, &req, 4<<20, store.ErrGitTeamMemoryIndexInvalid); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	receipt, _, err := s.cfg.Store.ReconcileGitTeamMemoryIndex(req, time.Now().UTC())
	if err != nil {
		writeGitMemoryReviewError(w, err)
		return
	}
	docs, err := s.cfg.Store.UnindexedGitTeamMemoryEventDocs(req.PortableProjectID)
	if err != nil {
		http.Error(w, "git team memory index unavailable", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if len(docs) > 0 {
		indexDocs := make([]retrieve.IndexEventDoc, len(docs))
		for index, doc := range docs {
			indexDocs[index] = retrieve.IndexEventDoc{EventID: doc.EventID, Text: doc.Text}
		}
		indexed, embedErr := s.cfg.Retrieval.EmbedAndPersistEvents(ctx, indexDocs)
		receipt.IndexedCount = indexed
		if embedErr != nil {
			// Reconciliation may have removed a superseded event. Reload even when
			// embedding the replacement failed so stale content cannot remain in
			// the in-memory candidate set.
			_ = s.cfg.Retrieval.Reload(context.Background())
			http.Error(w, "git team memory embedding unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	if err := s.cfg.Retrieval.Reload(ctx); err != nil {
		http.Error(w, "git team memory retrieval reload failed", http.StatusServiceUnavailable)
		return
	}
	receipt.State = "indexed"
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, receipt)
}
