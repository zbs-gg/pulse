package server

import (
	"context"
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
	report, err := s.startConsolidationReport(r.Context())
	if err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	writeJSON(w, report)
}

func (s *Server) startConsolidationReport(ctx context.Context) (consolidation.Report, error) {
	destination, ok := s.consolidationDestination()
	if !ok {
		return consolidation.Report{}, consolidation.ErrInvalidAuthority
	}
	report, _, err := s.consolidationReports.Start(destination)
	if err != nil {
		return consolidation.Report{}, err
	}
	if s.consolidationInventory != nil && report.Phase == consolidation.PhaseReportReady {
		report, err = s.consolidationInventory.EnsureFresh(report.InvocationID)
		if err != nil {
			return consolidation.Report{}, err
		}
		if report.Phase == consolidation.PhaseStale {
			report, _, err = s.consolidationReports.Start(destination)
			if err != nil {
				return consolidation.Report{}, err
			}
		}
	}
	if s.consolidationInventory != nil && (report.Phase == consolidation.PhasePlanned ||
		report.Phase == consolidation.PhaseInventory || report.Phase == consolidation.PhaseDeterministicDedupe) {
		report, err = s.consolidationInventory.Run(ctx, report.InvocationID, destination)
		if err != nil {
			return consolidation.Report{}, err
		}
	}
	return report, nil
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
	if s.consolidationInventory != nil {
		report, err = s.consolidationInventory.EnsureFresh(report.InvocationID)
		if err != nil {
			writeConsolidationReportError(w, err)
			return
		}
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
	if s.consolidationInventory != nil {
		report, err = s.consolidationInventory.EnsureFresh(invocationID)
		if err != nil {
			return consolidation.Report{}, err
		}
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
	report, err := s.resumeConsolidationReport(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	writeJSON(w, report)
}

func (s *Server) resumeConsolidationReport(ctx context.Context, invocationID string) (consolidation.Report, error) {
	if _, err := s.reportForCurrentDestination(invocationID); err != nil {
		return consolidation.Report{}, err
	}
	destination, ok := s.consolidationDestination()
	if !ok {
		return consolidation.Report{}, consolidation.ErrInvalidAuthority
	}
	report, err := s.consolidationReports.Resume(invocationID, destination)
	if err != nil {
		return consolidation.Report{}, err
	}
	if s.consolidationInventory != nil {
		return s.consolidationInventory.Run(ctx, report.InvocationID, destination)
	}
	return report, nil
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
