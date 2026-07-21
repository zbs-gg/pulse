package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/consolidation"
)

const consolidationReportRequestMaxBytes = 64 << 10

type consolidationReportExplanation struct {
	Schema       string   `json:"schema"`
	InvocationID string   `json:"invocation_id"`
	Phase        string   `json:"phase"`
	ReasonCodes  []string `json:"reason_codes"`
	Blockers     []string `json:"blockers"`
	NextAction   string   `json:"next_action"`
}

func (s *Server) consolidationDestination() (consolidation.Destination, bool) {
	if s.cfg.Store == nil || s.consolidationReports == nil {
		return consolidation.Destination{}, false
	}
	bindingDigest, repositoryID, ok := s.cfg.Store.ProductRuntimeBoundary()
	if !ok {
		return consolidation.Destination{}, false
	}
	return consolidation.Destination{
		StoreKind: string(s.cfg.Store.StoreKind()), StoreID: s.cfg.Store.StoreID(),
		BindingDigest: bindingDigest, RepositoryID: repositoryID,
	}, true
}

func decodeEmptyConsolidationRequest(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, consolidationReportRequestMaxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body struct{}
	if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) handleConsolidationReportStart(w http.ResponseWriter, r *http.Request) {
	if !decodeEmptyConsolidationRequest(w, r) {
		return
	}
	destination, ok := s.consolidationDestination()
	if !ok {
		http.Error(w, "consolidation destination unavailable", http.StatusServiceUnavailable)
		return
	}
	report, _, err := s.consolidationReports.Start(destination)
	if err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	writeJSON(w, report)
}

func (s *Server) handleConsolidationReportLatest(w http.ResponseWriter, _ *http.Request) {
	destination, ok := s.consolidationDestination()
	if !ok {
		http.Error(w, "consolidation destination unavailable", http.StatusServiceUnavailable)
		return
	}
	report, err := s.consolidationReports.Latest(destination)
	if err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	writeJSON(w, report)
}

func (s *Server) reportForCurrentDestination(invocationID string) (consolidation.Report, error) {
	report, err := s.consolidationReports.Get(invocationID)
	if err != nil {
		return consolidation.Report{}, err
	}
	destination, ok := s.consolidationDestination()
	if !ok || report.Destination != destination {
		return consolidation.Report{}, consolidation.ErrInvalidAuthority
	}
	return report, nil
}

func (s *Server) handleConsolidationReportGet(w http.ResponseWriter, r *http.Request) {
	report, err := s.reportForCurrentDestination(chi.URLParam(r, "id"))
	if err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	writeJSON(w, report)
}

func (s *Server) handleConsolidationReportExplain(w http.ResponseWriter, r *http.Request) {
	report, err := s.reportForCurrentDestination(chi.URLParam(r, "id"))
	if err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	writeJSON(w, consolidationReportExplanation{
		Schema: "pulse.consolidation.explanation.v1", InvocationID: report.InvocationID,
		Phase: string(report.Phase), ReasonCodes: report.ReasonCodes,
		Blockers: report.Blockers, NextAction: report.NextAction,
	})
}

func (s *Server) handleConsolidationReportCancel(w http.ResponseWriter, r *http.Request) {
	if !decodeEmptyConsolidationRequest(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.reportForCurrentDestination(id); err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	report, err := s.consolidationReports.Cancel(id)
	if err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	writeJSON(w, report)
}

func (s *Server) handleConsolidationReportResume(w http.ResponseWriter, r *http.Request) {
	if !decodeEmptyConsolidationRequest(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.reportForCurrentDestination(id); err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	destination, ok := s.consolidationDestination()
	if !ok {
		http.Error(w, "consolidation destination unavailable", http.StatusServiceUnavailable)
		return
	}
	report, err := s.consolidationReports.Resume(id, destination)
	if err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	writeJSON(w, report)
}

func writeConsolidationReportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, consolidation.ErrReportNotFound):
		http.Error(w, "consolidation report not found", http.StatusNotFound)
	case errors.Is(err, consolidation.ErrStaleInvocation), errors.Is(err, consolidation.ErrReportNotResumable):
		http.Error(w, "consolidation report lifecycle conflict", http.StatusConflict)
	case errors.Is(err, consolidation.ErrInvalidAuthority):
		http.Error(w, "consolidation destination changed", http.StatusConflict)
	default:
		http.Error(w, "consolidation report unavailable", http.StatusInternalServerError)
	}
}
