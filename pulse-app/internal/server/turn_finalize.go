package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

func (s *Server) handleTurnFinalize(w http.ResponseWriter, r *http.Request) {
	var req store.TurnFinalizeRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.FinalizeTurn(req, time.Now().UTC(), s.cfg.TrayGracePeriod)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	s.scheduleTurnResult(result)
	writeJSON(w, result)
}

func (s *Server) handleTurnNoChange(w http.ResponseWriter, r *http.Request) {
	var req store.TurnNoChangeRequest
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.FinalizeTurnNoChange(req, time.Now().UTC())
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleMemoryTrayList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	candidates, err := s.cfg.Store.ListMemoryTray(limit)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	writeJSON(w, map[string]any{"candidates": candidates})
}

func (s *Server) handleMemoryReceiptGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	receipt, err := s.cfg.Store.GetMemoryWriteReceipt(chi.URLParam(r, "id"))
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleMemoryTrayEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedVersion int                          `json:"expected_version"`
		Candidate       store.PrivateMemoryCandidate `json:"candidate"`
	}
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	receipt, err := s.cfg.Store.EditMemoryTrayCandidate(
		chi.URLParam(r, "id"), req.ExpectedVersion, req.Candidate,
		time.Now().UTC(), s.cfg.TrayGracePeriod,
	)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	s.scheduleReceipt(receipt, s.cfg.TrayGracePeriod)
	writeJSON(w, receipt)
}

func (s *Server) handleMemoryTrayCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedVersion int `json:"expected_version"`
	}
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	receipt, err := s.cfg.Store.CancelMemoryTrayCandidate(
		chi.URLParam(r, "id"), req.ExpectedVersion, time.Now().UTC(),
	)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleMemoryTrayCommit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpectedVersion int `json:"expected_version"`
	}
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	receipt, err := s.cfg.Store.CommitMemoryTrayCandidate(
		chi.URLParam(r, "id"), req.ExpectedVersion, time.Now().UTC(),
	)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	s.refreshProductRetrieval(receipt)
	writeJSON(w, receipt)
}

func (s *Server) handleMemoryCorrect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Candidate store.PrivateMemoryCandidate `json:"candidate"`
	}
	if err := decodeMemoryTrayBody(r, &req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.cfg.Store.PrepareMemoryCorrectionWithInvocation(
		chi.URLParam(r, "id"), req.Candidate, r.Header.Get("Idempotency-Key"),
		time.Now().UTC(), s.cfg.TrayGracePeriod,
	)
	if err != nil {
		writeMemoryTrayError(w, err)
		return
	}
	s.scheduleTurnResult(result)
	writeJSON(w, result)
}

func decodeMemoryTrayBody(r *http.Request, target any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("Memory Tray body is empty or too large")
	}
	return decodeStrictJSON(raw, target)
}

func writeMemoryTrayError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrMemoryTrayUnavailable), errors.Is(err, store.ErrMemoryTrayRequired):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, store.ErrMemoryTrayGraceActive):
		http.Error(w, err.Error(), http.StatusTooEarly)
	case errors.Is(err, store.ErrMemoryTrayVersionConflict), errors.Is(err, store.ErrMemoryTrayTerminal),
		errors.Is(err, store.ErrMemoryCorrectionConflict), errors.Is(err, store.ErrTurnAlreadyFinalized),
		errors.Is(err, store.ErrTurnFinalizeConflict), errors.Is(err, store.ErrProductRuntimeMismatch):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, "memory tray error: "+err.Error(), http.StatusBadRequest)
	}
}

func (s *Server) scheduleTurnResult(result store.TurnFinalizeResult) {
	for _, receipt := range result.Receipts {
		if receipt.Status == store.MemoryWritePending {
			s.scheduleReceipt(receipt, s.cfg.TrayGracePeriod)
		}
	}
}

func (s *Server) scheduleReceipt(receipt store.MemoryWriteReceipt, delay time.Duration) {
	s.scheduleReceiptAttempt(receipt, delay, 0)
}

func (s *Server) scheduleReceiptAttempt(receipt store.MemoryWriteReceipt, delay time.Duration, attempt int) {
	if receipt.CandidateID == "" || receipt.CandidateVersion < 1 {
		return
	}
	time.AfterFunc(delay, func() {
		committed, err := s.cfg.Store.CommitMemoryTrayCandidate(
			receipt.CandidateID, receipt.CandidateVersion, time.Now().UTC(),
		)
		if err == nil {
			s.refreshProductRetrieval(committed)
			return
		}
		if errors.Is(err, store.ErrMemoryTrayVersionConflict) || errors.Is(err, store.ErrMemoryTrayTerminal) {
			return
		}
		if errors.Is(err, store.ErrMemoryTrayGraceActive) {
			s.scheduleReceiptAttempt(receipt, 100*time.Millisecond, attempt)
			return
		}
		if attempt < 2 {
			s.scheduleReceiptAttempt(receipt, time.Duration(attempt+1)*250*time.Millisecond, attempt+1)
			return
		}
		if _, failErr := s.cfg.Store.FailMemoryTrayCandidate(
			receipt.CandidateID, receipt.CandidateVersion, "commit_failed", time.Now().UTC(),
		); failErr != nil && !errors.Is(failErr, store.ErrMemoryTrayVersionConflict) && !errors.Is(failErr, store.ErrMemoryTrayTerminal) {
			// A locked/unavailable DB cannot record its own failure. Keep retrying
			// until either the commit or the content-free failed receipt is durable.
			s.scheduleReceiptAttempt(receipt, time.Second, attempt)
		}
	})
}

func (s *Server) refreshProductRetrieval(receipt store.MemoryWriteReceipt) {
	s.refreshProductRetrievalAttempt(receipt, 0)
}

func (s *Server) refreshProductRetrievalAttempt(receipt store.MemoryWriteReceipt, attempt int) {
	if receipt.ObjectID == "" {
		return
	}
	if receipt.ReasonCode == "user_deleted" && s.cfg.Retrieval != nil && !s.cfg.Retrieval.EmbedderReady() {
		status := "complete"
		if err := s.cfg.Retrieval.Reload(context.Background()); err != nil {
			status = "failed"
			log.Printf("memory tray projection: post-delete reload failed: %v", err)
		}
		if err := s.cfg.Store.SetPrivateProjectionStatus(receipt.ObjectID, status, time.Now().UTC()); err != nil {
			log.Printf("memory tray projection: persist %s status failed: %v", status, err)
		}
		if status == "failed" {
			s.scheduleProjectionRetry(receipt, attempt)
		}
		return
	}
	if s.cfg.Retrieval == nil || !s.cfg.Retrieval.EmbedderReady() {
		// Keep the outbox pending. A later startup with a configured embedder
		// replays it, so memories captured in fallback mode never become
		// permanently dark.
		return
	}
	status := "complete"
	ctx := context.Background()
	for {
		docs, err := s.cfg.Store.UnindexedHostEventDocs(500)
		if err != nil {
			status = "failed"
			log.Printf("memory tray projection: list unindexed events failed: %v", err)
			break
		}
		if len(docs) == 0 {
			if err := s.cfg.Retrieval.Reload(ctx); err != nil {
				status = "failed"
				log.Printf("memory tray projection: reload failed: %v", err)
			}
			break
		}
		indexDocs := make([]retrieve.IndexEventDoc, len(docs))
		for index, doc := range docs {
			indexDocs[index] = retrieve.IndexEventDoc{EventID: doc.EventID, Text: doc.Text}
		}
		if err := s.cfg.Retrieval.EmbedAndIndexEvents(ctx, indexDocs); err != nil {
			status = "failed"
			log.Printf("memory tray projection: embed/index failed: %v", err)
			break
		}
		if len(docs) < 500 {
			break
		}
	}
	now := time.Now().UTC()
	if err := s.cfg.Store.SetPendingPrivateProjectionStatus(status, now); err != nil {
		log.Printf("memory tray projection: persist pending %s status failed: %v", status, err)
	}
	if receipt.ReasonCode == "user_deleted" && status == "failed" {
		if err := s.cfg.Store.SetPrivateProjectionStatus(receipt.ObjectID, status, now); err != nil {
			log.Printf("memory tray projection: persist delete %s status failed: %v", status, err)
		}
	}
	if status == "failed" {
		s.scheduleProjectionRetry(receipt, attempt)
	}
}

func (s *Server) scheduleProjectionRetry(receipt store.MemoryWriteReceipt, attempt int) {
	if attempt >= 5 {
		return
	}
	delay := time.Duration(1<<attempt) * 250 * time.Millisecond
	time.AfterFunc(delay, func() {
		s.refreshProductRetrievalAttempt(receipt, attempt+1)
	})
}

func (s *Server) recoverMemoryTray() error {
	if s.cfg.Store == nil || (s.cfg.Store.StoreKind() != store.StoreKindPersonal && s.cfg.Store.StoreKind() != store.StoreKindDesk) {
		return nil
	}
	after := ""
	for {
		candidates, err := s.cfg.Store.ListPendingMemoryTrayPage(after, 200)
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			expires, err := time.Parse(time.RFC3339Nano, candidate.GraceExpiresAt)
			if err != nil {
				if _, failErr := s.cfg.Store.FailMemoryTrayCandidate(
					candidate.CandidateID, candidate.Version, "commit_failed", time.Now().UTC(),
				); failErr != nil {
					return failErr
				}
				continue
			}
			delay := time.Until(expires)
			if delay < 0 {
				delay = 0
			}
			s.scheduleReceipt(candidate.LatestReceipt, delay)
			after = candidate.CandidateID
		}
		if len(candidates) < 200 {
			break
		}
	}
	afterObjectID := ""
	for {
		receipts, err := s.cfg.Store.ListPendingPrivateProjectionReceipts(afterObjectID, 200)
		if err != nil {
			return err
		}
		for _, receipt := range receipts {
			s.refreshProductRetrieval(receipt)
			afterObjectID = receipt.ObjectID
		}
		if len(receipts) < 200 {
			break
		}
	}
	return nil
}
