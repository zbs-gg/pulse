package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/claude"
	"github.com/nkkmnk/pulse/internal/contextquery"
	"github.com/nkkmnk/pulse/internal/outbox"
	"github.com/nkkmnk/pulse/internal/prompt"
	"github.com/nkkmnk/pulse/internal/retrieve"
)

type outboxRow struct {
	ID       int64  `json:"id"`
	ChatID   int64  `json:"chat_id"`
	Text     string `json:"text"`
	ReplyTo  *int64 `json:"reply_to,omitempty"`
	Media    string `json:"media,omitempty"`
	Priority string `json:"priority"`
	Attempts int    `json:"attempts"`
}

func (s *Server) handleOutboxList(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	records, err := s.cfg.Outbox.Claim(limit)
	if err != nil {
		slog.Error("outbox claim failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]outboxRow, 0, len(records))
	for _, rec := range records {
		row := outboxRow{
			ID:       rec.ID,
			ChatID:   rec.ChatID,
			Text:     rec.Text,
			Media:    rec.Media,
			Priority: rec.Priority,
			Attempts: rec.Attempts,
		}
		if rec.ReplyTo != nil {
			rt := *rec.ReplyTo
			row.ReplyTo = &rt
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

type ackRequest struct {
	ID      int64  `json:"id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) handleOutboxAck(w http.ResponseWriter, r *http.Request) {
	var req ackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.ID == 0 {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	if err := s.cfg.Outbox.Ack(req.ID, req.Success, req.Error); err != nil {
		slog.Error("outbox ack failed", "err", err, "id", req.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type msgRequest struct {
	ChatID    int64  `json:"chat_id"`
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	// ReplyTo and Timestamp are sent by the bridge; reserved for M2 when
	// thread context and time-aware prompts land.
	ReplyTo   *int64 `json:"reply_to,omitempty"`
	Timestamp string `json:"timestamp"`
}

func (s *Server) handleMsg(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Builder == nil || s.cfg.Claude == nil {
		http.Error(w, "backend LLM disabled in host-extracted mode", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB

	var req msgRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.ChatID == 0 || req.Text == "" {
		http.Error(w, "chat_id and text required", http.StatusBadRequest)
		return
	}

	built, err := s.cfg.Builder.Build(prompt.BuildInput{UserMessage: req.Text})
	if err != nil {
		slog.Error("prompt build failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	resp, err := s.cfg.Claude.Complete(ctx, claude.CompleteRequest{
		Model:    s.cfg.DefaultModel,
		System:   built.System,
		Messages: built.Messages,
	})
	if err != nil {
		slog.Error("claude call failed", "err", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	if strings.TrimSpace(resp.Text) == "" {
		slog.Warn("claude returned empty text", "chat_id", req.ChatID, "stop_reason", resp.StopReason)
		http.Error(w, "empty reply", http.StatusBadGateway)
		return
	}

	msgID := req.MessageID
	if _, err := s.cfg.Outbox.Enqueue(outbox.Message{
		ChatID:  req.ChatID,
		Text:    resp.Text,
		ReplyTo: &msgID,
	}); err != nil {
		slog.Error("outbox enqueue failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// retrieveRequest is the JSON body for POST /retrieve.
type retrieveRequest struct {
	Query     string              `json:"query"`
	Mode      string              `json:"mode,omitempty"` // "auto" | "factual" | "empathic" | "chain"
	TopK      int                 `json:"top_k,omitempty"`
	UserState *retrieve.UserState `json:"user_state,omitempty"`
	// GraphMode (default-OFF): "" | "anchored" | "walk" — temporal entity-graph
	// retrieval as an extra RRF recall-injector. Omitted = unchanged behaviour.
	GraphMode string `json:"graph_mode,omitempty"`
}

type retrieveResponse struct {
	EventIDs             []int64                              `json:"event_ids"`
	ModeUsed             string                               `json:"mode_used"`
	Confidence           float64                              `json:"confidence"`
	Classifier           string                               `json:"classifier"`
	Reasoning            string                               `json:"reasoning,omitempty"`
	EmotionRole          string                               `json:"emotion_role,omitempty"`
	EmotionRoleReasoning string                               `json:"emotion_role_reasoning,omitempty"`
	SurfaceabilityAction string                               `json:"surfaceability_action,omitempty"`
	ScoreBreakdowns      map[int64]retrieve.ScoreBreakdown    `json:"score_breakdowns,omitempty"`
	CandidateEvidence    map[int64]retrieve.CandidateEvidence `json:"candidate_evidence,omitempty"`
}

// handleRetrieve serves POST /retrieve. Body: retrieveRequest. Returns
// ranked event IDs + router decision metadata.
func (s *Server) handleRetrieve(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Retrieval == nil {
		http.Error(w, "retrieval not configured", http.StatusServiceUnavailable)
		return
	}
	var req retrieveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	personalScope, ok := s.personalScopeForRequest(w, r)
	if !ok {
		return
	}
	mode := retrieve.QueryMode(req.Mode)
	if mode == "" {
		mode = retrieve.ModeAuto
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := s.cfg.Retrieval.Retrieve(ctx, retrieve.RetrieveRequest{
		Query:         req.Query,
		Mode:          mode,
		TopK:          req.TopK,
		UserState:     req.UserState,
		GraphMode:     req.GraphMode,
		PersonalScope: personalScope,
	})
	if err != nil {
		slog.Error("retrieve failed", "err", err)
		http.Error(w, "retrieval error", http.StatusInternalServerError)
		return
	}
	eventIDs := resp.EventIDs
	if eventIDs == nil {
		eventIDs = []int64{}
	}
	out := retrieveResponse{
		EventIDs:             eventIDs,
		ModeUsed:             string(resp.ModeUsed),
		Confidence:           resp.RouterDecision.Confidence,
		Classifier:           resp.RouterDecision.Classifier,
		Reasoning:            resp.RouterDecision.Reasoning,
		EmotionRole:          string(resp.EmotionRole.Role),
		EmotionRoleReasoning: resp.EmotionRole.Reasoning,
		SurfaceabilityAction: string(resp.SurfaceabilityAction),
		ScoreBreakdowns:      resp.ScoreBreakdowns,
		CandidateEvidence:    resp.CandidateEvidence,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleFeedSignals(w http.ResponseWriter, r *http.Request) {
	limit := 3
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	markConsumed := r.URL.Query().Get("mark_consumed") == "true"

	signals, err := s.cfg.Store.RecentFeedSignals(limit, markConsumed)
	if err != nil {
		slog.Error("feed_signals query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"signals": signals,
		"count":   len(signals),
	})
}

func (s *Server) handleContextQuery(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ContextQuery == nil {
		http.Error(w, "context query not configured", http.StatusServiceUnavailable)
		return
	}
	var req contextquery.ContextQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	personalScope, ok := s.personalScopeForRequest(w, r)
	if !ok {
		return
	}
	req.PersonalScope = personalScope
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := s.cfg.ContextQuery.Query(ctx, req)
	if err != nil {
		slog.Error("context query failed", "err", err)
		http.Error(w, "context query error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) handleMemoryRecallActivity(w http.ResponseWriter, r *http.Request) {
	authority, ok := s.requireProductBindingAuthority(w, r)
	if !ok {
		return
	}
	host, ok := exactSingleHeader(r, "X-Pulse-Product-Host")
	if !ok || (host != "codex" && host != "claude-code" && host != "opencode") {
		http.Error(w, "product host is invalid", http.StatusBadRequest)
		return
	}
	var request memoryRecallActivityRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Schema != "pulse.memory_recall_activity.v1" ||
		request.ResultCount < 0 || request.ResultCount > memoryRecallMaximumItems || !memoryActivityDigest.MatchString(request.ResultDigest) {
		http.Error(w, "memory recall activity is invalid", http.StatusBadRequest)
		return
	}
	if err := s.recordMemoryRecallActivity(authority, host, request, time.Now()); err != nil {
		http.Error(w, "memory recall activity is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
