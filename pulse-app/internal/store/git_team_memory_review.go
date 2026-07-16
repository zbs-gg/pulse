package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	GitTeamMemoryStageSchema   = "pulse.git_team_memory.stage.v1"
	GitTeamMemoryEditSchema    = "pulse.git_team_memory.edit.v1"
	GitTeamMemoryRejectSchema  = "pulse.git_team_memory.reject.v1"
	GitTeamMemoryInspectSchema = "pulse.git_team_memory.inspect.v1"
)

var (
	ErrGitTeamMemoryInvalid         = errors.New("git team memory review request is invalid")
	ErrGitTeamMemoryUnsafeCandidate = errors.New("git team memory candidate is unsafe")
	ErrGitTeamMemoryStaleSource     = errors.New("git team memory source version is stale")
	ErrGitTeamMemoryVersionConflict = errors.New("git team memory candidate version conflict")
	ErrGitTeamMemoryTerminal        = errors.New("git team memory candidate is terminal")
	ErrGitTeamMemoryConflict        = errors.New("git team memory idempotency conflict")
)

type GitTeamMemorySourceReference struct {
	SourceID      string `json:"source_id"`
	VersionDigest string `json:"version_digest"`
}

type GitTeamMemoryWarning struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

type GitTeamMemoryCandidateInput struct {
	Kind             string                         `json:"kind"`
	Statement        string                         `json:"statement"`
	Audience         string                         `json:"audience"`
	Confidence       float64                        `json:"confidence"`
	SourceReferences []GitTeamMemorySourceReference `json:"source_references"`
	AdvisoryWarnings []GitTeamMemoryWarning         `json:"advisory_warnings"`
}

type GitTeamMemoryStageRequest struct {
	Schema              string                        `json:"schema"`
	PortableProjectID   string                        `json:"portable_project_id"`
	RepositoryID        string                        `json:"repository_id"`
	BindingDigest       string                        `json:"binding_digest"`
	Host                string                        `json:"host"`
	TaskID              string                        `json:"task_id"`
	IdempotencyKey      string                        `json:"idempotency_key"`
	SourceID            string                        `json:"source_id"`
	SourceVersionDigest string                        `json:"source_version_digest"`
	Candidates          []GitTeamMemoryCandidateInput `json:"candidates"`
	RawInputIncluded    *bool                         `json:"raw_input_included"`
}

type GitTeamMemoryEditRequest struct {
	Schema            string                      `json:"schema"`
	PortableProjectID string                      `json:"portable_project_id"`
	RepositoryID      string                      `json:"repository_id"`
	BindingDigest     string                      `json:"binding_digest"`
	CandidateID       string                      `json:"candidate_id"`
	ExpectedVersion   int                         `json:"expected_version"`
	Candidate         GitTeamMemoryCandidateInput `json:"candidate"`
}

type GitTeamMemoryRejectRequest struct {
	Schema            string `json:"schema"`
	PortableProjectID string `json:"portable_project_id"`
	RepositoryID      string `json:"repository_id"`
	BindingDigest     string `json:"binding_digest"`
	CandidateID       string `json:"candidate_id"`
	ExpectedVersion   int    `json:"expected_version"`
	ReasonCode        string `json:"reason_code"`
}

type GitTeamMemoryInspectRequest struct {
	Schema            string `json:"schema"`
	PortableProjectID string `json:"portable_project_id"`
	RepositoryID      string `json:"repository_id"`
	BindingDigest     string `json:"binding_digest"`
	BatchID           string `json:"batch_id"`
}

type GitTeamMemoryCandidateView struct {
	CandidateID      string                         `json:"candidate_id"`
	BatchID          string                         `json:"batch_id"`
	Ordinal          int                            `json:"ordinal"`
	Version          int                            `json:"version"`
	State            string                         `json:"state"`
	Kind             string                         `json:"kind"`
	Statement        string                         `json:"statement"`
	Audience         string                         `json:"audience"`
	Confidence       float64                        `json:"confidence"`
	SourceReferences []GitTeamMemorySourceReference `json:"source_references"`
	AdvisoryWarnings []GitTeamMemoryWarning         `json:"advisory_warnings"`
	ContentDigest    string                         `json:"content_digest"`
	CreatedAt        string                         `json:"created_at"`
}

type GitTeamMemoryBatchView struct {
	Schema              string                       `json:"schema"`
	BatchID             string                       `json:"batch_id"`
	PortableProjectID   string                       `json:"portable_project_id"`
	SourceID            string                       `json:"source_id"`
	SourceVersionDigest string                       `json:"source_version_digest"`
	SourceLocator       string                       `json:"source_locator"`
	Host                string                       `json:"host"`
	TaskID              string                       `json:"task_id"`
	Generation          int                          `json:"generation"`
	State               string                       `json:"state"`
	Candidates          []GitTeamMemoryCandidateView `json:"candidates"`
	CreatedAt           string                       `json:"created_at"`
	UpdatedAt           string                       `json:"updated_at"`
}

type canonicalGitTeamMemoryCandidate struct {
	Kind             string                         `json:"kind"`
	Statement        string                         `json:"statement"`
	Audience         string                         `json:"audience"`
	Confidence       float64                        `json:"confidence"`
	SourceReferences []GitTeamMemorySourceReference `json:"source_references"`
	Warnings         []GitTeamMemoryWarning         `json:"warnings"`
}

type preparedGitTeamMemoryCandidate struct {
	input        GitTeamMemoryCandidateInput
	digest       string
	refsJSON     string
	warningsJSON string
}

func validGitTeamMemoryKind(kind string) bool {
	switch kind {
	case "fact", "decision", "preference", "project_state", "open_loop", "correction", "do_not_repeat":
		return true
	default:
		return false
	}
}

func validGitTeamMemoryWarningCode(code string) bool {
	switch code {
	case "weak_evidence", "confidentiality", "over_broad", "contradiction", "unclear_scope":
		return true
	default:
		return false
	}
}

func prepareGitTeamMemoryCandidate(input GitTeamMemoryCandidateInput, sourceID, versionDigest string) (preparedGitTeamMemoryCandidate, error) {
	statement := strings.TrimSpace(input.Statement)
	if !validGitTeamMemoryKind(input.Kind) || input.Audience != "project" || statement == "" || len(statement) > 1200 ||
		!utf8.ValidString(statement) || !norm.NFC.IsNormalString(statement) || containsUnsafeMemoryUnicode(statement) ||
		math.IsNaN(input.Confidence) || math.IsInf(input.Confidence, 0) || input.Confidence < 0 || input.Confidence > 1 ||
		len(input.SourceReferences) != 1 || input.SourceReferences[0].SourceID != sourceID ||
		input.SourceReferences[0].VersionDigest != versionDigest || !trayBindingDigestPattern.MatchString(versionDigest) ||
		len(input.AdvisoryWarnings) > 4 {
		return preparedGitTeamMemoryCandidate{}, ErrGitTeamMemoryInvalid
	}
	if looksLikeTranscript(statement) || looksSensitiveOrPathLike(statement) {
		return preparedGitTeamMemoryCandidate{}, ErrGitTeamMemoryUnsafeCandidate
	}
	input.Statement = statement
	seenWarnings := make(map[string]struct{}, len(input.AdvisoryWarnings))
	for index := range input.AdvisoryWarnings {
		warning := &input.AdvisoryWarnings[index]
		warning.Summary = strings.TrimSpace(warning.Summary)
		if !validGitTeamMemoryWarningCode(warning.Code) || warning.Summary == "" || len(warning.Summary) > 240 ||
			!utf8.ValidString(warning.Summary) || !norm.NFC.IsNormalString(warning.Summary) ||
			containsUnsafeMemoryUnicode(warning.Summary) || looksLikeTranscript(warning.Summary) ||
			looksSensitiveOrPathLike(warning.Summary) {
			return preparedGitTeamMemoryCandidate{}, ErrGitTeamMemoryUnsafeCandidate
		}
		if _, duplicate := seenWarnings[warning.Code]; duplicate {
			return preparedGitTeamMemoryCandidate{}, ErrGitTeamMemoryInvalid
		}
		seenWarnings[warning.Code] = struct{}{}
	}
	// Warning order is canonical so two harnesses produce the same digest.
	sort.Slice(input.AdvisoryWarnings, func(i, j int) bool { return input.AdvisoryWarnings[i].Code < input.AdvisoryWarnings[j].Code })
	identity := canonicalGitTeamMemoryCandidate{
		Kind: input.Kind, Statement: input.Statement, Audience: input.Audience,
		Confidence: input.Confidence, SourceReferences: input.SourceReferences, Warnings: input.AdvisoryWarnings,
	}
	body, err := json.Marshal(identity)
	if err != nil {
		return preparedGitTeamMemoryCandidate{}, ErrGitTeamMemoryInvalid
	}
	digest := sha256.Sum256(body)
	refsJSON, _ := json.Marshal(input.SourceReferences)
	warningsJSON, _ := json.Marshal(input.AdvisoryWarnings)
	return preparedGitTeamMemoryCandidate{
		input: input, digest: hex.EncodeToString(digest[:]), refsJSON: string(refsJSON), warningsJSON: string(warningsJSON),
	}, nil
}

func validateGitTeamMemoryEnvelope(s *Store, schema, expectedSchema, projectID, repositoryID, bindingDigest string) error {
	if schema != expectedSchema {
		return ErrGitTeamMemoryInvalid
	}
	if err := s.validateProjectSourceAuthority(projectID, repositoryID, bindingDigest); err != nil {
		return err
	}
	return nil
}

func (s *Store) StageGitTeamMemoryReview(req GitTeamMemoryStageRequest, now time.Time) (GitTeamMemoryBatchView, error) {
	if err := validateGitTeamMemoryEnvelope(s, req.Schema, GitTeamMemoryStageSchema, req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return GitTeamMemoryBatchView{}, err
	}
	if !validHost(req.Host) || !validTrayIdentifier(req.TaskID) || !validTrayIdentifier(req.IdempotencyKey) ||
		!validTrayIdentifier(req.SourceID) || !trayBindingDigestPattern.MatchString(req.SourceVersionDigest) ||
		req.RawInputIncluded == nil || *req.RawInputIncluded || len(req.Candidates) == 0 || len(req.Candidates) > 20 {
		return GitTeamMemoryBatchView{}, ErrGitTeamMemoryInvalid
	}
	prepared := make([]preparedGitTeamMemoryCandidate, len(req.Candidates))
	for index, candidate := range req.Candidates {
		item, err := prepareGitTeamMemoryCandidate(candidate, req.SourceID, req.SourceVersionDigest)
		if err != nil {
			return GitTeamMemoryBatchView{}, err
		}
		prepared[index] = item
	}
	requestDigest, err := requestDigest(req)
	if err != nil {
		return GitTeamMemoryBatchView{}, ErrGitTeamMemoryInvalid
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return GitTeamMemoryBatchView{}, err
	}
	defer tx.Rollback()
	var sourceVersionID, currentDigest, sourceLocator string
	if err := tx.QueryRow(`
		SELECT current_version_id, current_version_digest, locator FROM git_memory_sources
		 WHERE source_id=? AND portable_project_id=?`, req.SourceID, req.PortableProjectID).Scan(&sourceVersionID, &currentDigest, &sourceLocator); err != nil {
		return GitTeamMemoryBatchView{}, err
	}
	if currentDigest != req.SourceVersionDigest {
		return GitTeamMemoryBatchView{}, ErrGitTeamMemoryStaleSource
	}
	var existingBatch, existingDigest string
	err = tx.QueryRow(`SELECT batch_id, request_digest FROM git_memory_review_batches WHERE portable_project_id=? AND idempotency_key=?`,
		req.PortableProjectID, req.IdempotencyKey).Scan(&existingBatch, &existingDigest)
	if err == nil {
		if existingDigest != requestDigest {
			return GitTeamMemoryBatchView{}, ErrGitTeamMemoryConflict
		}
		if err := tx.Rollback(); err != nil {
			return GitTeamMemoryBatchView{}, err
		}
		return s.InspectGitTeamMemoryReview(GitTeamMemoryInspectRequest{
			Schema: GitTeamMemoryInspectSchema, PortableProjectID: req.PortableProjectID,
			RepositoryID: req.RepositoryID, BindingDigest: req.BindingDigest, BatchID: existingBatch,
		})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return GitTeamMemoryBatchView{}, err
	}
	batchID, err := newOpaqueID("review_batch")
	if err != nil {
		return GitTeamMemoryBatchView{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO git_memory_review_batches(
			batch_id, portable_project_id, source_id, source_version_id, source_version_digest,
			host, task_id, idempotency_key, request_digest, generation, state, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'staged', ?, ?)`, batchID, req.PortableProjectID,
		req.SourceID, sourceVersionID, req.SourceVersionDigest, req.Host, req.TaskID, req.IdempotencyKey,
		requestDigest, createdAt, createdAt); err != nil {
		return GitTeamMemoryBatchView{}, err
	}
	views := make([]GitTeamMemoryCandidateView, 0, len(prepared))
	for ordinal, item := range prepared {
		candidateID, err := newOpaqueID("shared_candidate")
		if err != nil {
			return GitTeamMemoryBatchView{}, err
		}
		if _, err := tx.Exec(`
			INSERT INTO git_memory_review_candidates(
				candidate_id, batch_id, ordinal, current_version, current_digest, state, created_at, updated_at
			) VALUES (?, ?, ?, 1, ?, 'staged', ?, ?)`, candidateID, batchID, ordinal, item.digest, createdAt, createdAt); err != nil {
			return GitTeamMemoryBatchView{}, err
		}
		if _, err := tx.Exec(`
			INSERT INTO git_memory_candidate_versions(
				candidate_id, version, candidate_kind, statement, audience, confidence,
				source_refs_json, warnings_json, content_digest, created_at
			) VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`, candidateID, item.input.Kind, item.input.Statement,
			item.input.Audience, item.input.Confidence, item.refsJSON, item.warningsJSON, item.digest, createdAt); err != nil {
			return GitTeamMemoryBatchView{}, err
		}
		if err := insertGitMemoryReceiptAuditTx(tx, batchID, candidateID, 1, item.digest, "staged", "stage", "accepted", "", createdAt); err != nil {
			return GitTeamMemoryBatchView{}, err
		}
		views = append(views, gitTeamMemoryCandidateView(candidateID, batchID, ordinal, 1, "staged", item, createdAt))
	}
	if _, err := tx.Exec(`UPDATE git_memory_sources SET processing_state='reviewed', updated_at=? WHERE source_id=?`, createdAt, req.SourceID); err != nil {
		return GitTeamMemoryBatchView{}, err
	}
	if err := tx.Commit(); err != nil {
		return GitTeamMemoryBatchView{}, err
	}
	return GitTeamMemoryBatchView{
		Schema: GitTeamMemoryInspectSchema, BatchID: batchID, PortableProjectID: req.PortableProjectID,
		SourceID: req.SourceID, SourceVersionDigest: req.SourceVersionDigest, SourceLocator: sourceLocator, Host: req.Host,
		TaskID: req.TaskID, Generation: 1, State: "staged", Candidates: views,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}, nil
}

func gitTeamMemoryCandidateView(candidateID, batchID string, ordinal, version int, state string, item preparedGitTeamMemoryCandidate, createdAt string) GitTeamMemoryCandidateView {
	return GitTeamMemoryCandidateView{
		CandidateID: candidateID, BatchID: batchID, Ordinal: ordinal, Version: version, State: state,
		Kind: item.input.Kind, Statement: item.input.Statement, Audience: item.input.Audience,
		Confidence: item.input.Confidence, SourceReferences: item.input.SourceReferences,
		AdvisoryWarnings: item.input.AdvisoryWarnings, ContentDigest: item.digest, CreatedAt: createdAt,
	}
}

func insertGitMemoryReceiptAuditTx(tx *sql.Tx, batchID, candidateID string, version int, digest, status, action, outcome, reason, createdAt string) error {
	receiptID, err := newOpaqueID("shared_receipt")
	if err != nil {
		return err
	}
	var reasonValue any
	if reason != "" {
		reasonValue = reason
	}
	if _, err := tx.Exec(`
		INSERT INTO git_memory_receipts(receipt_id, batch_id, candidate_id, candidate_version, content_digest, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, receiptID, batchID, candidateID, version, digest, status, createdAt); err != nil {
		return err
	}
	auditID, err := newOpaqueID("shared_audit")
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
		INSERT INTO git_memory_audit(audit_id, batch_id, candidate_id, action, outcome, reason_code, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, auditID, batchID, candidateID, action, outcome, reasonValue, createdAt)
	return err
}

func (s *Store) EditGitTeamMemoryCandidate(req GitTeamMemoryEditRequest, now time.Time) (GitTeamMemoryCandidateView, error) {
	if err := validateGitTeamMemoryEnvelope(s, req.Schema, GitTeamMemoryEditSchema, req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if !validTrayIdentifier(req.CandidateID) || req.ExpectedVersion < 1 || len(req.Candidate.SourceReferences) != 1 {
		return GitTeamMemoryCandidateView{}, ErrGitTeamMemoryInvalid
	}
	ref := req.Candidate.SourceReferences[0]
	prepared, err := prepareGitTeamMemoryCandidate(req.Candidate, ref.SourceID, ref.VersionDigest)
	if err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	defer tx.Rollback()
	var batchID, state, sourceID, sourceDigest string
	var ordinal, currentVersion int
	if err := tx.QueryRow(`
		SELECT candidate.batch_id, candidate.ordinal, candidate.current_version, candidate.state,
		       batch.source_id, batch.source_version_digest
		  FROM git_memory_review_candidates candidate
		  JOIN git_memory_review_batches batch ON batch.batch_id=candidate.batch_id
		 WHERE candidate.candidate_id=? AND batch.portable_project_id=?`, req.CandidateID, req.PortableProjectID).Scan(
		&batchID, &ordinal, &currentVersion, &state, &sourceID, &sourceDigest); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if state != "staged" {
		return GitTeamMemoryCandidateView{}, ErrGitTeamMemoryTerminal
	}
	if currentVersion != req.ExpectedVersion {
		return GitTeamMemoryCandidateView{}, ErrGitTeamMemoryVersionConflict
	}
	var currentSourceDigest string
	if err := tx.QueryRow(`SELECT current_version_digest FROM git_memory_sources WHERE source_id=?`, sourceID).Scan(&currentSourceDigest); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if ref.SourceID != sourceID || ref.VersionDigest != sourceDigest || currentSourceDigest != sourceDigest {
		return GitTeamMemoryCandidateView{}, ErrGitTeamMemoryStaleSource
	}
	nextVersion := currentVersion + 1
	if _, err := tx.Exec(`
		INSERT INTO git_memory_candidate_versions(
			candidate_id, version, candidate_kind, statement, audience, confidence,
			source_refs_json, warnings_json, content_digest, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, req.CandidateID, nextVersion, prepared.input.Kind,
		prepared.input.Statement, prepared.input.Audience, prepared.input.Confidence,
		prepared.refsJSON, prepared.warningsJSON, prepared.digest, createdAt); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if _, err := tx.Exec(`
		UPDATE git_memory_review_candidates SET current_version=?, current_digest=?, updated_at=?
		 WHERE candidate_id=? AND current_version=? AND state='staged'`, nextVersion, prepared.digest, createdAt,
		req.CandidateID, currentVersion); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if _, err := tx.Exec(`UPDATE git_memory_review_batches SET generation=generation+1, updated_at=? WHERE batch_id=?`, createdAt, batchID); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if _, err := tx.Exec(`UPDATE git_memory_card_generations SET state='invalidated' WHERE batch_id=? AND state IN ('created','presented')`, batchID); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if err := insertGitMemoryReceiptAuditTx(tx, batchID, req.CandidateID, nextVersion, prepared.digest, "edited", "edit", "accepted", "", createdAt); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if err := tx.Commit(); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	return gitTeamMemoryCandidateView(req.CandidateID, batchID, ordinal, nextVersion, "staged", prepared, createdAt), nil
}

func (s *Store) RejectGitTeamMemoryCandidate(req GitTeamMemoryRejectRequest, now time.Time) (GitTeamMemoryCandidateView, error) {
	if err := validateGitTeamMemoryEnvelope(s, req.Schema, GitTeamMemoryRejectSchema, req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if !validTrayIdentifier(req.CandidateID) || req.ExpectedVersion < 1 || req.ReasonCode != "user_rejected" {
		return GitTeamMemoryCandidateView{}, ErrGitTeamMemoryInvalid
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	defer tx.Rollback()
	view, err := loadGitTeamMemoryCandidateTx(tx, req.CandidateID, req.PortableProjectID)
	if err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if view.State != "staged" {
		return GitTeamMemoryCandidateView{}, ErrGitTeamMemoryTerminal
	}
	if view.Version != req.ExpectedVersion {
		return GitTeamMemoryCandidateView{}, ErrGitTeamMemoryVersionConflict
	}
	result, err := tx.Exec(`
		UPDATE git_memory_review_candidates SET state='rejected', updated_at=?, terminal_at=?
		 WHERE candidate_id=? AND current_version=? AND state='staged'`, createdAt, createdAt, req.CandidateID, req.ExpectedVersion)
	if err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return GitTeamMemoryCandidateView{}, ErrGitTeamMemoryVersionConflict
	}
	decisionID, err := newOpaqueID("shared_decision")
	if err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO git_memory_review_decisions(
			decision_id, batch_id, candidate_id, candidate_version, candidate_digest, decision, reason_code, created_at
		) VALUES (?, ?, ?, ?, ?, 'rejected', 'user_rejected', ?)`, decisionID, view.BatchID,
		req.CandidateID, req.ExpectedVersion, view.ContentDigest, createdAt); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if _, err := tx.Exec(`UPDATE git_memory_card_generations SET state='invalidated' WHERE batch_id=? AND state IN ('created','presented')`, view.BatchID); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if _, err := tx.Exec(`
		UPDATE git_memory_review_batches SET generation=generation+1,
		 state=CASE WHEN NOT EXISTS (
		   SELECT 1 FROM git_memory_review_candidates WHERE batch_id=? AND candidate_id<>? AND state='staged'
		 ) THEN 'rejected' ELSE state END,
		 updated_at=? WHERE batch_id=?`, view.BatchID, req.CandidateID, createdAt, view.BatchID); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if err := insertGitMemoryReceiptAuditTx(tx, view.BatchID, req.CandidateID, req.ExpectedVersion, view.ContentDigest, "rejected", "reject", "accepted", "user_rejected", createdAt); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if err := tx.Commit(); err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	view.State = "rejected"
	return view, nil
}

func (s *Store) InspectGitTeamMemoryReview(req GitTeamMemoryInspectRequest) (GitTeamMemoryBatchView, error) {
	if err := validateGitTeamMemoryEnvelope(s, req.Schema, GitTeamMemoryInspectSchema, req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return GitTeamMemoryBatchView{}, err
	}
	if !validTrayIdentifier(req.BatchID) {
		return GitTeamMemoryBatchView{}, ErrGitTeamMemoryInvalid
	}
	var result GitTeamMemoryBatchView
	result.Schema = GitTeamMemoryInspectSchema
	err := s.db.QueryRow(`
		SELECT batch.batch_id, batch.portable_project_id, batch.source_id,
		       batch.source_version_digest, source.locator, batch.host, batch.task_id,
		       batch.generation, batch.state, batch.created_at, batch.updated_at
		  FROM git_memory_review_batches batch
		  JOIN git_memory_sources source ON source.source_id=batch.source_id
		 WHERE batch.batch_id=? AND batch.portable_project_id=?`, req.BatchID, req.PortableProjectID).Scan(
		&result.BatchID, &result.PortableProjectID, &result.SourceID, &result.SourceVersionDigest,
		&result.SourceLocator, &result.Host, &result.TaskID, &result.Generation, &result.State,
		&result.CreatedAt, &result.UpdatedAt,
	)
	if err != nil {
		return GitTeamMemoryBatchView{}, err
	}
	rows, err := s.db.Query(`
		SELECT candidate.candidate_id, candidate.batch_id, candidate.ordinal,
		       candidate.current_version, candidate.state, version.candidate_kind,
		       version.statement, version.audience, version.confidence,
		       version.source_refs_json, version.warnings_json, version.content_digest, version.created_at
		  FROM git_memory_review_candidates candidate
		  JOIN git_memory_candidate_versions version
		    ON version.candidate_id=candidate.candidate_id AND version.version=candidate.current_version
		 WHERE candidate.batch_id=? ORDER BY candidate.ordinal`, req.BatchID)
	if err != nil {
		return GitTeamMemoryBatchView{}, err
	}
	defer rows.Close()
	result.Candidates = []GitTeamMemoryCandidateView{}
	for rows.Next() {
		view, err := scanGitTeamMemoryCandidate(rows)
		if err != nil {
			return GitTeamMemoryBatchView{}, err
		}
		result.Candidates = append(result.Candidates, view)
	}
	return result, rows.Err()
}

type gitMemoryScanner interface{ Scan(...any) error }

func scanGitTeamMemoryCandidate(scanner gitMemoryScanner) (GitTeamMemoryCandidateView, error) {
	var view GitTeamMemoryCandidateView
	var refsJSON, warningsJSON string
	err := scanner.Scan(&view.CandidateID, &view.BatchID, &view.Ordinal, &view.Version, &view.State,
		&view.Kind, &view.Statement, &view.Audience, &view.Confidence, &refsJSON, &warningsJSON,
		&view.ContentDigest, &view.CreatedAt)
	if err != nil {
		return GitTeamMemoryCandidateView{}, err
	}
	if json.Unmarshal([]byte(refsJSON), &view.SourceReferences) != nil ||
		json.Unmarshal([]byte(warningsJSON), &view.AdvisoryWarnings) != nil {
		return GitTeamMemoryCandidateView{}, errors.New("git team memory candidate metadata is corrupt")
	}
	return view, nil
}

func loadGitTeamMemoryCandidateTx(tx *sql.Tx, candidateID, projectID string) (GitTeamMemoryCandidateView, error) {
	row := tx.QueryRow(`
		SELECT candidate.candidate_id, candidate.batch_id, candidate.ordinal,
		       candidate.current_version, candidate.state, version.candidate_kind,
		       version.statement, version.audience, version.confidence,
		       version.source_refs_json, version.warnings_json, version.content_digest, version.created_at
		  FROM git_memory_review_candidates candidate
		  JOIN git_memory_review_batches batch ON batch.batch_id=candidate.batch_id
		  JOIN git_memory_candidate_versions version
		    ON version.candidate_id=candidate.candidate_id AND version.version=candidate.current_version
		 WHERE candidate.candidate_id=? AND batch.portable_project_id=?`, candidateID, projectID)
	view, err := scanGitTeamMemoryCandidate(row)
	if err != nil {
		return GitTeamMemoryCandidateView{}, fmt.Errorf("git team memory candidate: %w", err)
	}
	return view, nil
}
