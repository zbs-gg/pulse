package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/historicalingest"
)

const historicalIngestRequestMaxBytes = 5 << 20

type HistoricalIngestWorkPayload struct {
	TrustedPrompt string
	Evidence      string
}

type historicalWorkerLease struct {
	Schema               string                    `json:"schema"`
	JobID                string                    `json:"job_id"`
	Unit                 historicalingest.WorkUnit `json:"unit"`
	LeaseToken           string                    `json:"lease_token"`
	CheckpointGeneration uint64                    `json:"checkpoint_generation"`
	ExpiresAt            time.Time                 `json:"expires_at"`
	SourceSnapshotDigest string                    `json:"source_snapshot_digest"`
	RunnerContractDigest string                    `json:"runner_contract_digest"`
	TrustedPrompt        string                    `json:"trusted_prompt"`
	Evidence             string                    `json:"evidence"`
}

func decodeHistoricalRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, historicalIngestRequestMaxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) historicalManager() (*historicalingest.IngestManager, error) {
	if s.historicalUnavailable != "" || s.historicalIngest == nil {
		return nil, errors.New("historical ingest unavailable")
	}
	return s.historicalIngest, nil
}

func (s *Server) handleHistoricalIngestStatus(w http.ResponseWriter, r *http.Request) {
	manager, err := s.historicalManager()
	if err != nil {
		writeHistoricalIngestError(w, err)
		return
	}
	status, err := manager.Status(chi.URLParam(r, "id"))
	if err != nil {
		writeHistoricalIngestError(w, err)
		return
	}
	writeJSON(w, status)
}

func (s *Server) handleHistoricalIngestLease(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decodeHistoricalRequest(w, r, &body) {
		return
	}
	manager, err := s.historicalManager()
	if err != nil {
		writeHistoricalIngestError(w, err)
		return
	}
	jobID := chi.URLParam(r, "id")
	lease, err := manager.LeaseNext(jobID)
	if errors.Is(err, historicalingest.ErrNoWorkAvailable) {
		if _, _, _, buildErr := manager.BuildManifest(jobID); buildErr != nil {
			writeHistoricalIngestError(w, buildErr)
			return
		}
		status, statusErr := manager.Status(jobID)
		if statusErr != nil {
			writeHistoricalIngestError(w, statusErr)
			return
		}
		writeJSON(w, struct {
			Done   bool                       `json:"done"`
			Status historicalingest.JobStatus `json:"status"`
		}{true, status})
		return
	}
	if err != nil {
		writeHistoricalIngestError(w, err)
		return
	}
	if s.historicalEvidence == nil {
		_, _ = manager.FailLease(jobID, lease.Unit.ID, lease.Token, "evidence_provider_unavailable")
		writeHistoricalIngestError(w, errors.New("historical evidence unavailable"))
		return
	}
	payload, err := s.historicalEvidence.LoadHistoricalIngestEvidence(r.Context(), lease.Unit)
	if err != nil || payload.TrustedPrompt == "" || len(payload.TrustedPrompt) > 32<<10 || len(payload.Evidence) > 4<<20 || digestHistoricalEvidence(payload.Evidence) != lease.Unit.EvidenceDigest {
		_, _ = manager.FailLease(jobID, lease.Unit.ID, lease.Token, "evidence_contract_mismatch")
		writeHistoricalIngestError(w, errors.New("historical evidence unavailable"))
		return
	}
	status, err := manager.Status(jobID)
	if err != nil {
		writeHistoricalIngestError(w, err)
		return
	}
	writeJSON(w, historicalWorkerLease{
		Schema: "pulse.historical_ingest.worker_lease.v1", JobID: jobID, Unit: lease.Unit,
		LeaseToken: lease.Token, CheckpointGeneration: lease.CheckpointGeneration, ExpiresAt: lease.ExpiresAt,
		SourceSnapshotDigest: status.SnapshotDigest, RunnerContractDigest: status.RunnerContract,
		TrustedPrompt: payload.TrustedPrompt, Evidence: payload.Evidence,
	})
}

func (s *Server) handleHistoricalIngestSubmit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UnitID         string                          `json:"unit_id"`
		LeaseToken     string                          `json:"lease_token"`
		RunnerContract string                          `json:"runner_contract_digest"`
		Result         historicalingest.WorkUnitResult `json:"result"`
		Usage          historicalingest.TokenUsage     `json:"usage"`
	}
	if !decodeHistoricalRequest(w, r, &body) {
		return
	}
	manager, err := s.historicalManager()
	if err != nil {
		writeHistoricalIngestError(w, err)
		return
	}
	receipt, err := manager.SubmitResult(chi.URLParam(r, "id"), body.UnitID, body.LeaseToken, body.Result, body.Usage, body.RunnerContract)
	if err != nil {
		writeHistoricalIngestError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) handleHistoricalIngestQuota(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UnitID     string `json:"unit_id"`
		LeaseToken string `json:"lease_token"`
	}
	if !decodeHistoricalRequest(w, r, &body) {
		return
	}
	manager, err := s.historicalManager()
	if err == nil {
		var status historicalingest.JobStatus
		status, err = manager.PauseQuota(chi.URLParam(r, "id"), body.UnitID, body.LeaseToken)
		if err == nil {
			writeJSON(w, status)
			return
		}
	}
	writeHistoricalIngestError(w, err)
}

func (s *Server) handleHistoricalIngestResume(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decodeHistoricalRequest(w, r, &body) {
		return
	}
	manager, err := s.historicalManager()
	if err == nil {
		var status historicalingest.JobStatus
		status, err = manager.ResumeJob(chi.URLParam(r, "id"))
		if err == nil {
			writeJSON(w, status)
			return
		}
	}
	writeHistoricalIngestError(w, err)
}

func (s *Server) handleHistoricalIngestCancel(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decodeHistoricalRequest(w, r, &body) {
		return
	}
	manager, err := s.historicalManager()
	if err == nil {
		var status historicalingest.JobStatus
		status, err = manager.CancelJob(chi.URLParam(r, "id"))
		if err == nil {
			writeJSON(w, status)
			return
		}
	}
	writeHistoricalIngestError(w, err)
}

func writeHistoricalIngestError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, historicalingest.ErrIngestJobNotFound):
		http.Error(w, "historical ingest job not found", http.StatusNotFound)
	case errors.Is(err, historicalingest.ErrLeaseConflict), errors.Is(err, historicalingest.ErrResultConflict),
		errors.Is(err, historicalingest.ErrIncompleteCohort), errors.Is(err, historicalingest.ErrJobNotExtracting),
		errors.Is(err, historicalingest.ErrNoWorkAvailable):
		http.Error(w, "historical ingest lifecycle conflict", http.StatusConflict)
	default:
		http.Error(w, "historical ingest unavailable", http.StatusServiceUnavailable)
	}
}

func digestHistoricalEvidence(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
