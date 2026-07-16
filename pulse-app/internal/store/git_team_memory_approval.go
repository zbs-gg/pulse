package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	GitTeamMemoryPresentationSchema  = "pulse.git_team_memory.presentation.v1"
	GitTeamMemoryExactOKSchema       = "pulse.git_team_memory.exact_ok.v1"
	GitTeamMemoryApprovalLeaseSchema = "pulse.git_team_memory.approval_lease.v1"
)

var (
	ErrGitTeamMemoryPresentationInvalid = errors.New("git team memory presentation is invalid")
	ErrGitTeamMemoryApprovalUnavailable = errors.New("git team memory approval is unavailable")
	ErrGitTeamMemoryApprovalAmbiguous   = errors.New("git team memory approval is ambiguous")
)

type GitTeamMemoryPresentationRequest struct {
	Schema            string   `json:"schema"`
	PortableProjectID string   `json:"portable_project_id"`
	RepositoryID      string   `json:"repository_id"`
	BindingDigest     string   `json:"binding_digest"`
	BatchID           string   `json:"batch_id"`
	BatchGeneration   int      `json:"batch_generation"`
	Host              string   `json:"host"`
	TaskID            string   `json:"task_id"`
	SessionRef        string   `json:"session_ref"`
	TurnRef           string   `json:"turn_ref"`
	SourceEventDigest string   `json:"source_event_digest"`
	CardBlockDigest   string   `json:"card_block_digest"`
	CandidateDigests  []string `json:"candidate_digests"`
}

type GitTeamMemoryPresentationReceipt struct {
	Schema           string   `json:"schema"`
	PresentationID   string   `json:"presentation_id"`
	GenerationID     string   `json:"generation_id"`
	BatchID          string   `json:"batch_id"`
	BatchGeneration  int      `json:"batch_generation"`
	CardBlockDigest  string   `json:"card_block_digest"`
	CandidateDigests []string `json:"candidate_digests"`
	State            string   `json:"state"`
	PresentedAt      string   `json:"presented_at"`
	ExpiresAt        string   `json:"expires_at"`
}

type GitTeamMemoryExactOKRequest struct {
	Schema            string `json:"schema"`
	PortableProjectID string `json:"portable_project_id"`
	RepositoryID      string `json:"repository_id"`
	BindingDigest     string `json:"binding_digest"`
	Host              string `json:"host"`
	SessionRef        string `json:"session_ref"`
	PromptEventDigest string `json:"prompt_event_digest"`
}

type GitTeamMemoryApprovalLease struct {
	Schema           string   `json:"schema"`
	LeaseID          string   `json:"lease_id"`
	PresentationID   string   `json:"presentation_id"`
	BatchID          string   `json:"batch_id"`
	BatchGeneration  int      `json:"batch_generation"`
	CandidateDigests []string `json:"candidate_digests"`
	AuthorityDigest  string   `json:"authority_digest"`
	State            string   `json:"state"`
	IssuedAt         string   `json:"issued_at"`
	ExpiresAt        string   `json:"expires_at"`
	ConsumedAt       string   `json:"consumed_at,omitempty"`
}

var hookRefPattern = map[string]string{
	"session": "session:",
	"turn":    "turn:",
}

func validHookRef(kind, value string) bool {
	prefix, ok := hookRefPattern[kind]
	return ok && strings.HasPrefix(value, prefix) &&
		trayBindingDigestPattern.MatchString(strings.TrimPrefix(value, prefix))
}

func validDigestList(values []string) bool {
	if len(values) == 0 || len(values) > 20 {
		return false
	}
	for _, value := range values {
		if !trayBindingDigestPattern.MatchString(value) {
			return false
		}
	}
	return true
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func gitTeamMemoryCandidateDigestsTx(tx *sql.Tx, batchID string) ([]string, error) {
	rows, err := tx.Query(`
		SELECT current_digest FROM git_memory_review_candidates
		 WHERE batch_id=? AND state='staged' ORDER BY ordinal`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadPresentationReceiptTx(tx *sql.Tx, presentationID string) (GitTeamMemoryPresentationReceipt, error) {
	var result GitTeamMemoryPresentationReceipt
	var candidateJSON string
	result.Schema = GitTeamMemoryPresentationSchema
	err := tx.QueryRow(`
		SELECT presentation.presentation_id, presentation.generation_id,
		       presentation.batch_id, presentation.batch_generation,
		       presentation.card_block_digest, presentation.candidate_digests_json,
		       presentation.state, presentation.presented_at, presentation.expires_at
		  FROM git_memory_hook_presentations presentation
		 WHERE presentation.presentation_id=?`, presentationID).Scan(
		&result.PresentationID, &result.GenerationID, &result.BatchID,
		&result.BatchGeneration, &result.CardBlockDigest, &candidateJSON,
		&result.State, &result.PresentedAt, &result.ExpiresAt,
	)
	if err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	if json.Unmarshal([]byte(candidateJSON), &result.CandidateDigests) != nil || !validDigestList(result.CandidateDigests) {
		return GitTeamMemoryPresentationReceipt{}, errors.New("git team memory presentation metadata is corrupt")
	}
	return result, nil
}

func (s *Store) PresentGitTeamMemoryCards(req GitTeamMemoryPresentationRequest, now time.Time) (GitTeamMemoryPresentationReceipt, error) {
	if err := validateGitTeamMemoryEnvelope(s, req.Schema, GitTeamMemoryPresentationSchema,
		req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	if !validTrayIdentifier(req.BatchID) || req.BatchGeneration < 1 || req.Host != "codex" ||
		!validTrayIdentifier(req.TaskID) || !validHookRef("session", req.SessionRef) ||
		!validHookRef("turn", req.TurnRef) || !trayBindingDigestPattern.MatchString(req.SourceEventDigest) ||
		!trayBindingDigestPattern.MatchString(req.CardBlockDigest) || !validDigestList(req.CandidateDigests) {
		return GitTeamMemoryPresentationReceipt{}, ErrGitTeamMemoryPresentationInvalid
	}
	presentedAt := now.UTC().Format(time.RFC3339Nano)
	expiresAt := now.UTC().Add(10 * time.Minute).Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	defer tx.Rollback()
	var batchGeneration int
	var host, taskID, state, sourceDigest, currentSourceDigest string
	err = tx.QueryRow(`
		SELECT batch.generation, batch.host, batch.task_id, batch.state,
		       batch.source_version_digest, source.current_version_digest
		  FROM git_memory_review_batches batch
		  JOIN git_memory_sources source ON source.source_id=batch.source_id
		 WHERE batch.batch_id=? AND batch.portable_project_id=?`, req.BatchID, req.PortableProjectID).Scan(
		&batchGeneration, &host, &taskID, &state, &sourceDigest, &currentSourceDigest,
	)
	if err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	if batchGeneration != req.BatchGeneration || host != req.Host || taskID != req.TaskID ||
		state != "staged" || sourceDigest != currentSourceDigest {
		return GitTeamMemoryPresentationReceipt{}, ErrGitTeamMemoryVersionConflict
	}
	digests, err := gitTeamMemoryCandidateDigestsTx(tx, req.BatchID)
	if err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	if !sameStrings(digests, req.CandidateDigests) {
		return GitTeamMemoryPresentationReceipt{}, ErrGitTeamMemoryVersionConflict
	}
	var existingID string
	err = tx.QueryRow(`
		SELECT presentation_id FROM git_memory_hook_presentations
		 WHERE batch_id=? AND batch_generation=? AND state='presented'
		 ORDER BY presented_at DESC, presentation_id DESC LIMIT 1`, req.BatchID, req.BatchGeneration).Scan(&existingID)
	if err == nil {
		result, loadErr := loadPresentationReceiptTx(tx, existingID)
		if loadErr != nil {
			return GitTeamMemoryPresentationReceipt{}, loadErr
		}
		if result.CardBlockDigest != req.CardBlockDigest || !sameStrings(result.CandidateDigests, req.CandidateDigests) {
			return GitTeamMemoryPresentationReceipt{}, ErrGitTeamMemoryVersionConflict
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, result.ExpiresAt)
		if parseErr != nil {
			return GitTeamMemoryPresentationReceipt{}, errors.New("git team memory presentation expiry is corrupt")
		}
		if expires.After(now.UTC()) {
			return result, tx.Rollback()
		}
		if _, err := tx.Exec(`UPDATE git_memory_hook_presentations SET state='invalidated' WHERE presentation_id=? AND state='presented'`, existingID); err != nil {
			return GitTeamMemoryPresentationReceipt{}, err
		}
		if _, err := tx.Exec(`UPDATE git_memory_card_generations SET state='invalidated' WHERE generation_id=? AND state='presented'`, result.GenerationID); err != nil {
			return GitTeamMemoryPresentationReceipt{}, err
		}
		err = sql.ErrNoRows
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	generationID, err := newOpaqueID("card_generation")
	if err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	presentationID, err := newOpaqueID("card_presentation")
	if err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	candidateJSON, _ := json.Marshal(req.CandidateDigests)
	if _, err := tx.Exec(`
		INSERT INTO git_memory_card_generations(
			generation_id, batch_id, batch_generation, card_block_digest,
			candidate_digests_json, authority_kind, state, created_at, presented_at
		) VALUES (?, ?, ?, ?, ?, 'codex_stop', 'presented', ?, ?)`, generationID,
		req.BatchID, req.BatchGeneration, req.CardBlockDigest, string(candidateJSON), presentedAt, presentedAt); err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO git_memory_hook_presentations(
			presentation_id, generation_id, batch_id, batch_generation, host, task_id,
			session_ref, turn_ref, source_event_digest, card_block_digest,
			candidate_digests_json, state, presented_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'presented', ?, ?)`, presentationID,
		generationID, req.BatchID, req.BatchGeneration, req.Host, req.TaskID, req.SessionRef,
		req.TurnRef, req.SourceEventDigest, req.CardBlockDigest, string(candidateJSON), presentedAt, expiresAt); err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	if err := insertGitMemoryBatchReceiptAuditTx(tx, req.BatchID, "presented", "present", "accepted", "", presentedAt); err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return GitTeamMemoryPresentationReceipt{}, err
	}
	return GitTeamMemoryPresentationReceipt{
		Schema: GitTeamMemoryPresentationSchema, PresentationID: presentationID,
		GenerationID: generationID, BatchID: req.BatchID, BatchGeneration: req.BatchGeneration,
		CardBlockDigest: req.CardBlockDigest, CandidateDigests: append([]string(nil), req.CandidateDigests...),
		State: "presented", PresentedAt: presentedAt, ExpiresAt: expiresAt,
	}, nil
}

func insertGitMemoryBatchReceiptAuditTx(tx *sql.Tx, batchID, status, action, outcome, reason, createdAt string) error {
	receiptID, err := newOpaqueID("shared_receipt")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO git_memory_receipts(receipt_id, batch_id, candidate_id, candidate_version, content_digest, status, created_at)
		VALUES (?, ?, NULL, 0, NULL, ?, ?)`, receiptID, batchID, status, createdAt); err != nil {
		return err
	}
	auditID, err := newOpaqueID("shared_audit")
	if err != nil {
		return err
	}
	var reasonValue any
	if reason != "" {
		reasonValue = reason
	}
	_, err = tx.Exec(`
		INSERT INTO git_memory_audit(audit_id, batch_id, candidate_id, action, outcome, reason_code, created_at)
		VALUES (?, ?, NULL, ?, ?, ?, ?)`, auditID, batchID, action, outcome, reasonValue, createdAt)
	return err
}

func gitTeamMemoryAuthorityDigest(sessionRef, promptEventDigest, presentationID, cardDigest string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"pulse-git-memory-exact-ok-v1", sessionRef, promptEventDigest, presentationID, cardDigest,
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

func loadApprovalLeaseTx(tx *sql.Tx, leaseID string) (GitTeamMemoryApprovalLease, error) {
	var result GitTeamMemoryApprovalLease
	var candidateJSON string
	result.Schema = GitTeamMemoryApprovalLeaseSchema
	err := tx.QueryRow(`
		SELECT lease_id, presentation_id, batch_id, batch_generation,
		       candidate_digests_json, authority_digest, state, issued_at,
		       expires_at, COALESCE(consumed_at, '')
		  FROM git_memory_approval_leases WHERE lease_id=?`, leaseID).Scan(
		&result.LeaseID, &result.PresentationID, &result.BatchID, &result.BatchGeneration,
		&candidateJSON, &result.AuthorityDigest, &result.State, &result.IssuedAt,
		&result.ExpiresAt, &result.ConsumedAt,
	)
	if err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	if json.Unmarshal([]byte(candidateJSON), &result.CandidateDigests) != nil || !validDigestList(result.CandidateDigests) {
		return GitTeamMemoryApprovalLease{}, errors.New("git team memory approval lease metadata is corrupt")
	}
	return result, nil
}

func (s *Store) ApproveExactGitTeamMemoryOK(req GitTeamMemoryExactOKRequest, now time.Time) (GitTeamMemoryApprovalLease, error) {
	if err := validateGitTeamMemoryEnvelope(s, req.Schema, GitTeamMemoryExactOKSchema,
		req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	if req.Host != "codex" || !validHookRef("session", req.SessionRef) ||
		!trayBindingDigestPattern.MatchString(req.PromptEventDigest) {
		return GitTeamMemoryApprovalLease{}, ErrGitTeamMemoryPresentationInvalid
	}
	issuedAt := now.UTC().Format(time.RFC3339Nano)
	expiresAt := now.UTC().Add(2 * time.Minute).Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	defer tx.Rollback()
	var existingLease string
	err = tx.QueryRow(`SELECT lease_id FROM git_memory_approval_leases WHERE prompt_event_digest=?`, req.PromptEventDigest).Scan(&existingLease)
	if err == nil {
		result, loadErr := loadApprovalLeaseTx(tx, existingLease)
		if loadErr != nil {
			return GitTeamMemoryApprovalLease{}, loadErr
		}
		return result, tx.Rollback()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return GitTeamMemoryApprovalLease{}, err
	}
	type pendingPresentation struct {
		presentationID, batchID, cardDigest, candidateJSON, expiresAt string
		generation                                                    int
	}
	rows, err := tx.Query(`
		SELECT presentation.presentation_id, presentation.batch_id,
		       presentation.batch_generation, presentation.card_block_digest,
		       presentation.candidate_digests_json, presentation.expires_at
		  FROM git_memory_hook_presentations presentation
		  JOIN git_memory_card_generations card ON card.generation_id=presentation.generation_id
		  JOIN git_memory_review_batches batch ON batch.batch_id=presentation.batch_id
		  JOIN git_memory_sources source ON source.source_id=batch.source_id
		 WHERE presentation.host=? AND presentation.session_ref=?
		   AND presentation.state='presented' AND card.state='presented'
		   AND batch.state='staged'
		   AND batch.portable_project_id=?
		   AND batch.source_version_digest=source.current_version_digest
		 ORDER BY presentation.presented_at, presentation.presentation_id`,
		req.Host, req.SessionRef, req.PortableProjectID)
	if err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	pending := []pendingPresentation{}
	for rows.Next() {
		var item pendingPresentation
		if err := rows.Scan(&item.presentationID, &item.batchID, &item.generation,
			&item.cardDigest, &item.candidateJSON, &item.expiresAt); err != nil {
			rows.Close()
			return GitTeamMemoryApprovalLease{}, err
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, item.expiresAt)
		if parseErr != nil {
			rows.Close()
			return GitTeamMemoryApprovalLease{}, errors.New("git team memory presentation expiry is corrupt")
		}
		if expires.After(now.UTC()) {
			pending = append(pending, item)
		}
	}
	if err := rows.Close(); err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	if len(pending) == 0 {
		return GitTeamMemoryApprovalLease{}, ErrGitTeamMemoryApprovalUnavailable
	}
	if len(pending) != 1 {
		return GitTeamMemoryApprovalLease{}, ErrGitTeamMemoryApprovalAmbiguous
	}
	selected := pending[0]
	var digests []string
	if json.Unmarshal([]byte(selected.candidateJSON), &digests) != nil || !validDigestList(digests) {
		return GitTeamMemoryApprovalLease{}, errors.New("git team memory presentation metadata is corrupt")
	}
	currentDigests, err := gitTeamMemoryCandidateDigestsTx(tx, selected.batchID)
	if err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	if !sameStrings(currentDigests, digests) {
		return GitTeamMemoryApprovalLease{}, ErrGitTeamMemoryVersionConflict
	}
	authorityDigest := gitTeamMemoryAuthorityDigest(req.SessionRef, req.PromptEventDigest,
		selected.presentationID, selected.cardDigest)
	leaseID, err := newOpaqueID("approval_lease")
	if err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO git_memory_approval_leases(
			lease_id, presentation_id, batch_id, batch_generation, session_ref,
			prompt_event_digest, authority_digest, candidate_digests_json,
			state, issued_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'issued', ?, ?)`, leaseID, selected.presentationID,
		selected.batchID, selected.generation, req.SessionRef, req.PromptEventDigest,
		authorityDigest, selected.candidateJSON, issuedAt, expiresAt); err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	candidateRows, err := tx.Query(`
		SELECT candidate_id, current_version, current_digest
		  FROM git_memory_review_candidates
		 WHERE batch_id=? AND state='staged' ORDER BY ordinal`, selected.batchID)
	if err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	type candidateDecision struct {
		id, digest string
		version    int
	}
	decisions := []candidateDecision{}
	for candidateRows.Next() {
		var item candidateDecision
		if err := candidateRows.Scan(&item.id, &item.version, &item.digest); err != nil {
			candidateRows.Close()
			return GitTeamMemoryApprovalLease{}, err
		}
		decisions = append(decisions, item)
	}
	if err := candidateRows.Close(); err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	for _, item := range decisions {
		decisionID, err := newOpaqueID("shared_decision")
		if err != nil {
			return GitTeamMemoryApprovalLease{}, err
		}
		if _, err := tx.Exec(`
			INSERT INTO git_memory_review_decisions(
				decision_id, batch_id, candidate_id, candidate_version, candidate_digest,
				decision, reason_code, authority_digest, created_at
			) VALUES (?, ?, ?, ?, ?, 'approved', 'trusted_exact_ok', ?, ?)`, decisionID,
			selected.batchID, item.id, item.version, item.digest, authorityDigest, issuedAt); err != nil {
			return GitTeamMemoryApprovalLease{}, err
		}
		if err := insertGitMemoryReceiptAuditTx(tx, selected.batchID, item.id, item.version,
			item.digest, "approved", "approve", "accepted", "", issuedAt); err != nil {
			return GitTeamMemoryApprovalLease{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE git_memory_review_candidates SET state='approved', updated_at=? WHERE batch_id=? AND state='staged'`,
		issuedAt, selected.batchID); err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	if _, err := tx.Exec(`UPDATE git_memory_review_batches SET state='approved', updated_at=? WHERE batch_id=? AND state='staged'`,
		issuedAt, selected.batchID); err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	if _, err := tx.Exec(`UPDATE git_memory_hook_presentations SET state='approved' WHERE presentation_id=? AND state='presented'`,
		selected.presentationID); err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	return GitTeamMemoryApprovalLease{
		Schema: GitTeamMemoryApprovalLeaseSchema, LeaseID: leaseID,
		PresentationID: selected.presentationID, BatchID: selected.batchID,
		BatchGeneration: selected.generation, CandidateDigests: digests,
		AuthorityDigest: authorityDigest, State: "issued", IssuedAt: issuedAt, ExpiresAt: expiresAt,
	}, nil
}

func (s *Store) ConsumeGitTeamMemoryApprovalLease(leaseID string, now time.Time) (GitTeamMemoryApprovalLease, error) {
	if s == nil || !s.productTrayRequired() || !validTrayIdentifier(leaseID) {
		return GitTeamMemoryApprovalLease{}, ErrGitTeamMemoryApprovalUnavailable
	}
	tx, err := s.db.Begin()
	if err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	defer tx.Rollback()
	lease, err := loadApprovalLeaseTx(tx, leaseID)
	if err != nil || lease.State != "issued" {
		return GitTeamMemoryApprovalLease{}, ErrGitTeamMemoryApprovalUnavailable
	}
	consumedAt := now.UTC().Format(time.RFC3339Nano)
	expiresAt, parseErr := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
	if parseErr != nil {
		return GitTeamMemoryApprovalLease{}, errors.New("git team memory approval lease expiry is corrupt")
	}
	if !expiresAt.After(now.UTC()) {
		_, _ = tx.Exec(`UPDATE git_memory_approval_leases SET state='expired' WHERE lease_id=? AND state='issued'`, leaseID)
		_ = tx.Commit()
		return GitTeamMemoryApprovalLease{}, ErrGitTeamMemoryApprovalUnavailable
	}
	result, err := tx.Exec(`
		UPDATE git_memory_approval_leases SET state='consumed', consumed_at=?
		 WHERE lease_id=? AND state='issued'`, consumedAt, leaseID)
	if err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return GitTeamMemoryApprovalLease{}, ErrGitTeamMemoryApprovalUnavailable
	}
	if err := tx.Commit(); err != nil {
		return GitTeamMemoryApprovalLease{}, err
	}
	lease.State = "consumed"
	lease.ConsumedAt = consumedAt
	return lease, nil
}

func (lease GitTeamMemoryApprovalLease) Validate() error {
	if lease.Schema != GitTeamMemoryApprovalLeaseSchema || !validTrayIdentifier(lease.LeaseID) ||
		!validTrayIdentifier(lease.PresentationID) || !validTrayIdentifier(lease.BatchID) ||
		lease.BatchGeneration < 1 || !validDigestList(lease.CandidateDigests) ||
		!trayBindingDigestPattern.MatchString(lease.AuthorityDigest) {
		return fmt.Errorf("approval lease is invalid")
	}
	return nil
}
