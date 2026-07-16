package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	GitTeamMemoryPublicationStartSchema    = "pulse.git_team_memory.publication_start.v1"
	GitTeamMemoryPublicationFinalizeSchema = "pulse.git_team_memory.publication_finalize.v1"
	GitTeamMemoryPublicationReceiptSchema  = "pulse.git_team_memory.publication_receipt.v1"
)

var (
	ErrGitTeamMemoryPublicationInvalid  = errors.New("git team memory publication request is invalid")
	ErrGitTeamMemoryPublicationConflict = errors.New("git team memory publication conflicts with current state")
)

var gitObjectIDPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64}|unborn)$`)
var gitCommitIDPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

type GitTeamMemoryPublicationStartRequest struct {
	Schema            string `json:"schema"`
	PortableProjectID string `json:"portable_project_id"`
	RepositoryID      string `json:"repository_id"`
	BindingDigest     string `json:"binding_digest"`
	ApprovalLeaseID   string `json:"approval_lease_id"`
	ApproverLabel     string `json:"approver_label"`
	ExpectedParent    string `json:"expected_parent"`
}

type GitTeamMemoryPublicationFile struct {
	Path     string `json:"path"`
	MemoryID string `json:"memory_id,omitempty"`
	Content  string `json:"content"`
	SHA256   string `json:"sha256"`
	Bytes    int    `json:"bytes"`
}

type GitTeamMemoryPublicationReceipt struct {
	Schema         string                         `json:"schema"`
	PublicationID  string                         `json:"publication_id"`
	BatchID        string                         `json:"batch_id"`
	State          string                         `json:"state"`
	ExpectedParent string                         `json:"expected_parent"`
	FilesDigest    string                         `json:"files_digest"`
	Files          []GitTeamMemoryPublicationFile `json:"files,omitempty"`
	CommitHash     string                         `json:"commit_hash,omitempty"`
	CreatedAt      string                         `json:"created_at"`
	UpdatedAt      string                         `json:"updated_at"`
}

type GitTeamMemoryPublicationFinalizeRequest struct {
	Schema            string `json:"schema"`
	PortableProjectID string `json:"portable_project_id"`
	RepositoryID      string `json:"repository_id"`
	BindingDigest     string `json:"binding_digest"`
	PublicationID     string `json:"publication_id"`
	FilesDigest       string `json:"files_digest"`
	Outcome           string `json:"outcome"`
	CommitHash        string `json:"commit_hash,omitempty"`
}

func gitTeamMemoryApproverLabelDigest(label string) string {
	digest := sha256.Sum256([]byte("pulse-git-memory-approver-label-v1\x00" + label))
	return hex.EncodeToString(digest[:])
}

func validGitTeamMemoryApproverLabel(label string) bool {
	return label == strings.TrimSpace(label) && label != "" && utf8.ValidString(label) &&
		norm.NFC.IsNormalString(label) && utf8.RuneCountInString(label) <= 80 &&
		!containsUnsafeMemoryUnicode(label) && !looksLikeTranscript(label) &&
		!looksSensitiveOrPathLike(label)
}

func canonicalGitTeamMemoryJSON(value any) (string, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body) + "\n", nil
}

func publicationMemoryID(projectID, candidateID string) string {
	digest := sha256.Sum256([]byte("pulse-git-memory-object-v1\x00" + projectID + "\x00" + candidateID))
	return "shared_memory_" + hex.EncodeToString(digest[:16])
}

func publicationFile(path, memoryID, content string) GitTeamMemoryPublicationFile {
	digest := sha256.Sum256([]byte(content))
	return GitTeamMemoryPublicationFile{
		Path: path, MemoryID: memoryID, Content: content,
		SHA256: hex.EncodeToString(digest[:]), Bytes: len([]byte(content)),
	}
}

type gitMemoryPublicationCandidate struct {
	CandidateID      string
	Version          int
	Digest           string
	Kind             string
	Statement        string
	Confidence       float64
	SourceReferences []GitTeamMemorySourceReference
	Warnings         []GitTeamMemoryWarning
}

func loadGitMemoryPublicationCandidatesTx(tx *sql.Tx, batchID string) ([]gitMemoryPublicationCandidate, error) {
	rows, err := tx.Query(`
		SELECT candidate.candidate_id, candidate.current_version, candidate.current_digest,
		       version.candidate_kind, version.statement, version.confidence,
		       version.source_refs_json, version.warnings_json
		  FROM git_memory_review_candidates candidate
		  JOIN git_memory_candidate_versions version
		    ON version.candidate_id=candidate.candidate_id AND version.version=candidate.current_version
		 WHERE candidate.batch_id=? ORDER BY candidate.ordinal`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []gitMemoryPublicationCandidate{}
	for rows.Next() {
		var item gitMemoryPublicationCandidate
		var refsJSON, warningsJSON string
		if err := rows.Scan(&item.CandidateID, &item.Version, &item.Digest, &item.Kind,
			&item.Statement, &item.Confidence, &refsJSON, &warningsJSON); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(refsJSON), &item.SourceReferences) != nil ||
			json.Unmarshal([]byte(warningsJSON), &item.Warnings) != nil {
			return nil, errors.New("git team memory publication candidate metadata is corrupt")
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 || len(result) > 20 {
		return nil, ErrGitTeamMemoryPublicationConflict
	}
	return result, nil
}

func buildGitMemoryPublicationFilesTx(
	tx *sql.Tx,
	publicationID, batchID, projectID, approverLabel string,
	lease GitTeamMemoryApprovalLease,
) ([]GitTeamMemoryPublicationFile, string, error) {
	candidates, err := loadGitMemoryPublicationCandidatesTx(tx, batchID)
	if err != nil {
		return nil, "", err
	}
	if len(candidates) != len(lease.CandidateDigests) {
		return nil, "", ErrGitTeamMemoryPublicationConflict
	}
	for index := range candidates {
		if candidates[index].Digest != lease.CandidateDigests[index] {
			return nil, "", ErrGitTeamMemoryPublicationConflict
		}
	}
	projectContent, err := canonicalGitTeamMemoryJSON(struct {
		Schema    string `json:"schema"`
		ProjectID string `json:"project_id"`
	}{Schema: "pulse.git_team_memory.project.v1", ProjectID: projectID})
	if err != nil {
		return nil, "", err
	}
	files := []GitTeamMemoryPublicationFile{
		publicationFile("pulse-memory/project.json", "", projectContent),
	}
	type manifestObject struct {
		MemoryID string `json:"memory_id"`
		Path     string `json:"path"`
		SHA256   string `json:"sha256"`
	}
	objects := make([]manifestObject, 0, len(candidates))
	for _, candidate := range candidates {
		memoryID := publicationMemoryID(projectID, candidate.CandidateID)
		content, err := canonicalGitTeamMemoryJSON(struct {
			Schema            string                         `json:"schema"`
			MemoryID          string                         `json:"memory_id"`
			Version           int                            `json:"version"`
			Status            string                         `json:"status"`
			Kind              string                         `json:"kind"`
			Content           string                         `json:"content"`
			Confidence        float64                        `json:"confidence"`
			CandidateDigest   string                         `json:"candidate_digest"`
			ApproverLabel     string                         `json:"approver_label"`
			ApprovedAt        string                         `json:"approved_at"`
			ApprovalAuthority string                         `json:"approval_authority"`
			SourceReferences  []GitTeamMemorySourceReference `json:"source_references"`
			Warnings          []GitTeamMemoryWarning         `json:"warnings"`
		}{
			Schema: "pulse.git_team_memory.object.v1", MemoryID: memoryID, Version: candidate.Version,
			Status: "active", Kind: candidate.Kind, Content: candidate.Statement,
			Confidence: candidate.Confidence, CandidateDigest: candidate.Digest,
			ApproverLabel: approverLabel, ApprovedAt: lease.IssuedAt,
			ApprovalAuthority: lease.AuthorityDigest,
			SourceReferences:  candidate.SourceReferences, Warnings: candidate.Warnings,
		})
		if err != nil {
			return nil, "", err
		}
		path := "pulse-memory/memories/" + memoryID + ".json"
		file := publicationFile(path, memoryID, content)
		files = append(files, file)
		objects = append(objects, manifestObject{MemoryID: memoryID, Path: path, SHA256: file.SHA256})
	}
	manifestContent, err := canonicalGitTeamMemoryJSON(struct {
		Schema            string           `json:"schema"`
		PublicationID     string           `json:"publication_id"`
		BatchID           string           `json:"batch_id"`
		ProjectID         string           `json:"project_id"`
		ProjectPath       string           `json:"project_path"`
		ApprovalAuthority string           `json:"approval_authority"`
		ApprovedAt        string           `json:"approved_at"`
		Objects           []manifestObject `json:"objects"`
	}{
		Schema: "pulse.git_team_memory.publication.v1", PublicationID: publicationID,
		BatchID: batchID, ProjectID: projectID, ProjectPath: "pulse-memory/project.json",
		ApprovalAuthority: lease.AuthorityDigest, ApprovedAt: lease.IssuedAt, Objects: objects,
	})
	if err != nil {
		return nil, "", err
	}
	files = append(files, publicationFile("pulse-memory/publications/"+batchID+".json", "", manifestContent))
	hash := sha256.New()
	for _, file := range files {
		hash.Write([]byte(file.Path))
		hash.Write([]byte{0})
		hash.Write([]byte(file.SHA256))
		hash.Write([]byte{0})
	}
	return files, hex.EncodeToString(hash.Sum(nil)), nil
}

func loadGitMemoryPublicationReceiptTx(tx *sql.Tx, publicationID string) (GitTeamMemoryPublicationReceipt, string, string, error) {
	var result GitTeamMemoryPublicationReceipt
	var leaseID, approverLabel, authorityDigest string
	result.Schema = GitTeamMemoryPublicationReceiptSchema
	err := tx.QueryRow(`
		SELECT publication_id, batch_id, state, expected_parent, files_digest,
		       COALESCE(commit_hash, ''), created_at, updated_at,
		       approval_lease_id, approver_label, authority_digest
		  FROM git_memory_publications WHERE publication_id=?`, publicationID).Scan(
		&result.PublicationID, &result.BatchID, &result.State, &result.ExpectedParent,
		&result.FilesDigest, &result.CommitHash, &result.CreatedAt, &result.UpdatedAt,
		&leaseID, &approverLabel, &authorityDigest,
	)
	return result, leaseID, approverLabel, err
}

func (s *Store) BeginGitTeamMemoryPublication(req GitTeamMemoryPublicationStartRequest, now time.Time) (GitTeamMemoryPublicationReceipt, error) {
	if err := validateGitTeamMemoryEnvelope(s, req.Schema, GitTeamMemoryPublicationStartSchema,
		req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	if !validTrayIdentifier(req.ApprovalLeaseID) || !validGitTeamMemoryApproverLabel(req.ApproverLabel) ||
		!gitObjectIDPattern.MatchString(req.ExpectedParent) {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationInvalid
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	defer tx.Rollback()
	var existingID string
	err = tx.QueryRow(`SELECT publication_id FROM git_memory_publications WHERE approval_lease_id=?`, req.ApprovalLeaseID).Scan(&existingID)
	if err == nil {
		receipt, leaseID, label, loadErr := loadGitMemoryPublicationReceiptTx(tx, existingID)
		if loadErr != nil {
			return GitTeamMemoryPublicationReceipt{}, loadErr
		}
		if leaseID != req.ApprovalLeaseID || label != req.ApproverLabel || receipt.ExpectedParent != req.ExpectedParent {
			return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationConflict
		}
		lease, loadErr := loadApprovalLeaseTx(tx, leaseID)
		if loadErr != nil {
			return GitTeamMemoryPublicationReceipt{}, loadErr
		}
		files, filesDigest, buildErr := buildGitMemoryPublicationFilesTx(
			tx, receipt.PublicationID, receipt.BatchID, req.PortableProjectID, label, lease,
		)
		if buildErr != nil || filesDigest != receipt.FilesDigest {
			return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationConflict
		}
		receipt.Files = files
		return receipt, tx.Rollback()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	lease, err := loadApprovalLeaseTx(tx, req.ApprovalLeaseID)
	if err != nil || lease.State != "issued" || lease.ApproverLabelDigest != gitTeamMemoryApproverLabelDigest(req.ApproverLabel) {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryApprovalUnavailable
	}
	expires, err := time.Parse(time.RFC3339Nano, lease.ExpiresAt)
	if err != nil || !expires.After(now) {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryApprovalUnavailable
	}
	var projectID, batchState string
	if err := tx.QueryRow(`SELECT portable_project_id, state FROM git_memory_review_batches WHERE batch_id=?`, lease.BatchID).Scan(
		&projectID, &batchState); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	if projectID != req.PortableProjectID || batchState != "approved" {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationConflict
	}
	publicationID, err := newOpaqueID("shared_publication")
	if err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	files, filesDigest, err := buildGitMemoryPublicationFilesTx(
		tx, publicationID, lease.BatchID, projectID, req.ApproverLabel, lease,
	)
	if err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	createdAt := now.Format(time.RFC3339Nano)
	if _, err := tx.Exec(`
		INSERT INTO git_memory_publications(
			publication_id, batch_id, state, expected_parent, files_digest,
			created_at, updated_at, approval_lease_id, approver_label, authority_digest
		) VALUES (?, ?, 'publishing', ?, ?, ?, ?, ?, ?, ?)`, publicationID, lease.BatchID,
		req.ExpectedParent, filesDigest, createdAt, createdAt, lease.LeaseID,
		req.ApproverLabel, lease.AuthorityDigest); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	for ordinal, file := range files {
		var memoryID any
		if file.MemoryID != "" {
			memoryID = file.MemoryID
		}
		if _, err := tx.Exec(`
			INSERT INTO git_memory_publication_files(
				publication_id, ordinal, memory_id, path, content_sha256, byte_count
			) VALUES (?, ?, ?, ?, ?, ?)`, publicationID, ordinal, memoryID, file.Path, file.SHA256, file.Bytes); err != nil {
			return GitTeamMemoryPublicationReceipt{}, err
		}
	}
	if result, err := tx.Exec(`
		UPDATE git_memory_approval_leases SET state='consumed', consumed_at=?
		 WHERE lease_id=? AND state='issued'`, createdAt, lease.LeaseID); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryApprovalUnavailable
	}
	if result, err := tx.Exec(`UPDATE git_memory_review_batches SET state='publishing', updated_at=? WHERE batch_id=? AND state='approved'`,
		createdAt, lease.BatchID); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationConflict
	}
	if result, err := tx.Exec(`UPDATE git_memory_review_candidates SET state='publishing', updated_at=? WHERE batch_id=? AND state='approved'`,
		createdAt, lease.BatchID); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	} else if affected, _ := result.RowsAffected(); affected != int64(len(files)-2) {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationConflict
	}
	if err := insertGitMemoryBatchReceiptAuditTx(tx, lease.BatchID, "publishing", "publish", "accepted", "", createdAt); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	return GitTeamMemoryPublicationReceipt{
		Schema: GitTeamMemoryPublicationReceiptSchema, PublicationID: publicationID,
		BatchID: lease.BatchID, State: "publishing", ExpectedParent: req.ExpectedParent,
		FilesDigest: filesDigest, Files: files, CreatedAt: createdAt, UpdatedAt: createdAt,
	}, nil
}

func (s *Store) FinalizeGitTeamMemoryPublication(req GitTeamMemoryPublicationFinalizeRequest, now time.Time) (GitTeamMemoryPublicationReceipt, error) {
	if err := validateGitTeamMemoryEnvelope(s, req.Schema, GitTeamMemoryPublicationFinalizeSchema,
		req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	if !validTrayIdentifier(req.PublicationID) || !trayBindingDigestPattern.MatchString(req.FilesDigest) ||
		(req.Outcome != "committed" && req.Outcome != "published_uncommitted") ||
		(req.Outcome == "committed" && !gitCommitIDPattern.MatchString(req.CommitHash)) ||
		(req.Outcome == "published_uncommitted" && req.CommitHash != "") {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationInvalid
	}
	updatedAt := now.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	defer tx.Rollback()
	receipt, _, _, err := loadGitMemoryPublicationReceiptTx(tx, req.PublicationID)
	if err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	var projectID string
	if err := tx.QueryRow(`SELECT portable_project_id FROM git_memory_review_batches WHERE batch_id=?`, receipt.BatchID).Scan(&projectID); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	if projectID != req.PortableProjectID || receipt.FilesDigest != req.FilesDigest {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationConflict
	}
	if receipt.State == "committed" || receipt.State == "published_uncommitted" {
		if receipt.State != req.Outcome || receipt.CommitHash != req.CommitHash {
			return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationConflict
		}
		return receipt, tx.Rollback()
	}
	if receipt.State != "publishing" {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationConflict
	}
	var commitValue any
	if req.CommitHash != "" {
		commitValue = req.CommitHash
	}
	if _, err := tx.Exec(`
		UPDATE git_memory_publications SET state=?, commit_hash=?, updated_at=?
		 WHERE publication_id=? AND state='publishing'`, req.Outcome, commitValue, updatedAt, req.PublicationID); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	if result, err := tx.Exec(`UPDATE git_memory_review_batches SET state=?, updated_at=? WHERE batch_id=? AND state='publishing'`,
		req.Outcome, updatedAt, receipt.BatchID); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		return GitTeamMemoryPublicationReceipt{}, ErrGitTeamMemoryPublicationConflict
	}
	if _, err := tx.Exec(`UPDATE git_memory_review_candidates SET state='published', updated_at=? WHERE batch_id=? AND state='publishing'`,
		updatedAt, receipt.BatchID); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	if err := insertGitMemoryBatchReceiptAuditTx(tx, receipt.BatchID, req.Outcome, "publish", "accepted", "", updatedAt); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return GitTeamMemoryPublicationReceipt{}, err
	}
	receipt.State = req.Outcome
	receipt.CommitHash = req.CommitHash
	receipt.UpdatedAt = updatedAt
	return receipt, nil
}

func (receipt GitTeamMemoryPublicationReceipt) Validate() error {
	if receipt.Schema != GitTeamMemoryPublicationReceiptSchema || !validTrayIdentifier(receipt.PublicationID) ||
		!validTrayIdentifier(receipt.BatchID) || !gitObjectIDPattern.MatchString(receipt.ExpectedParent) ||
		!trayBindingDigestPattern.MatchString(receipt.FilesDigest) {
		return fmt.Errorf("git team memory publication receipt is invalid")
	}
	return nil
}
