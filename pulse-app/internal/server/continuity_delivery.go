package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

const (
	continuityDeliveryOfferSchema = "pulse.continuity_delivery.v1"
	continuityDeliveryMaxBody     = 128 << 10
)

type continuityDeliveryOfferBody struct {
	Schema                 string   `json:"schema"`
	ContextID              string   `json:"context_id"`
	Purpose                string   `json:"purpose"`
	BindingDigest          string   `json:"binding_digest"`
	RepositoryID           string   `json:"repository_id"`
	Host                   string   `json:"host"`
	SessionRef             string   `json:"session_ref"`
	SourceEventDigest      string   `json:"source_event_digest"`
	PayloadDigest          string   `json:"payload_digest"`
	ObjectIDs              []string `json:"object_ids"`
	EvidenceIDs            []string `json:"evidence_ids"`
	MethodID               string   `json:"method_id"`
	MethodVersion          string   `json:"method_version"`
	RenderedBytes          int      `json:"rendered_bytes"`
	PulseTokens            int      `json:"pulse_tokens"`
	BaselineKind           string   `json:"baseline_kind,omitempty"`
	SourceEquivalentTokens *int     `json:"source_equivalent_tokens,omitempty"`
	CoverageCounted        int      `json:"coverage_counted"`
	CoverageTotal          int      `json:"coverage_total"`
}

func decodeContinuityDeliveryBody(r *http.Request, target any) error {
	return decodeStrictJSONBody(r, target, continuityDeliveryMaxBody, store.ErrContinuityDeliveryInvalid)
}

func (s *Server) handleContinuityDeliveryOffer(w http.ResponseWriter, r *http.Request) {
	var body continuityDeliveryOfferBody
	if err := decodeContinuityDeliveryBody(r, &body); err != nil || body.Schema != continuityDeliveryOfferSchema {
		http.Error(w, "invalid continuity delivery offer", http.StatusBadRequest)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	receipt, err := s.cfg.Store.RecordContinuityOffer(r.Context(), store.ContinuityDeliveryOfferRequest{
		ContextID: body.ContextID, IdempotencyKey: idempotencyKey, Purpose: body.Purpose,
		BindingDigest: body.BindingDigest, RepositoryID: body.RepositoryID, Host: body.Host,
		SessionRef: body.SessionRef, SourceEventDigest: body.SourceEventDigest,
		PayloadDigest: body.PayloadDigest, ObjectIDs: body.ObjectIDs, EvidenceIDs: body.EvidenceIDs,
		MethodID: body.MethodID, MethodVersion: body.MethodVersion,
		RenderedBytes: body.RenderedBytes, PulseTokens: body.PulseTokens,
		BaselineKind: body.BaselineKind, SourceEquivalentTokens: body.SourceEquivalentTokens,
		CoverageCounted: body.CoverageCounted, CoverageTotal: body.CoverageTotal,
	}, time.Now().UTC())
	if err != nil {
		writeContinuityDeliveryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, receipt)
}

func writeContinuityDeliveryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrContinuityDeliveryAuthority):
		http.Error(w, "continuity delivery authority mismatch", http.StatusForbidden)
	case errors.Is(err, store.ErrContinuityDeliveryIdempotencyConflict),
		errors.Is(err, store.ErrContinuityDeliveryTransition):
		http.Error(w, "continuity delivery conflict", http.StatusConflict)
	case errors.Is(err, store.ErrContinuityDeliveryUnavailable):
		http.Error(w, "continuity delivery unavailable", http.StatusServiceUnavailable)
	case errors.Is(err, store.ErrContinuityDeliveryInvalid):
		http.Error(w, "invalid continuity delivery offer", http.StatusBadRequest)
	default:
		http.Error(w, "continuity delivery store error", http.StatusInternalServerError)
	}
}
