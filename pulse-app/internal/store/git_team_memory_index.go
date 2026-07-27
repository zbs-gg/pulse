package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	GitTeamMemoryIndexSchema        = "pulse.git_team_memory.index.v1"
	GitTeamMemoryIndexReceiptSchema = "pulse.git_team_memory.index_receipt.v1"
	gitTeamMemoryIndexMaxFiles      = 256
	gitTeamMemoryIndexMaxBytes      = 4 << 20
)

var (
	ErrGitTeamMemoryIndexInvalid  = errors.New("git team memory index request is invalid")
	ErrGitTeamMemoryIndexConflict = errors.New("git team memory committed pack conflicts with its approval manifest")
	gitSharedMemoryIDPattern      = regexp.MustCompile(`^shared_memory_[a-f0-9]{32}$`)
	gitSharedMemoryPathPattern    = regexp.MustCompile(`^pulse-memory/memories/(shared_memory_[a-f0-9]{32})\.json$`)
	gitSharedManifestPathPattern  = regexp.MustCompile(`^pulse-memory/publications/([A-Za-z0-9][A-Za-z0-9._:-]{0,254})\.json$`)
)

type GitTeamMemoryIndexFile struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	SHA256     string `json:"sha256"`
	CommitHash string `json:"commit_hash"`
}

type GitTeamMemoryIndexRequest struct {
	Schema            string                   `json:"schema"`
	PortableProjectID string                   `json:"portable_project_id"`
	RepositoryID      string                   `json:"repository_id"`
	BindingDigest     string                   `json:"binding_digest"`
	HeadCommit        string                   `json:"head_commit"`
	Files             []GitTeamMemoryIndexFile `json:"files"`
}

type GitTeamMemoryIndexReceipt struct {
	Schema            string `json:"schema"`
	ReceiptID         string `json:"receipt_id"`
	PortableProjectID string `json:"portable_project_id"`
	HeadCommit        string `json:"head_commit"`
	ScanDigest        string `json:"scan_digest"`
	State             string `json:"state"`
	ActiveCount       int    `json:"active_count"`
	ChangedCount      int    `json:"changed_count"`
	RemovedCount      int    `json:"removed_count"`
	IndexedCount      int    `json:"indexed_count"`
	IndexedAt         string `json:"indexed_at"`
}

type GitTeamMemoryProvenance struct {
	MemoryID          string                         `json:"memory_id"`
	Visibility        string                         `json:"visibility"`
	PortableProjectID string                         `json:"portable_project_id"`
	Version           int                            `json:"version"`
	Kind              string                         `json:"kind"`
	FilePath          string                         `json:"file_path"`
	ContentSHA256     string                         `json:"content_sha256"`
	PublicationID     string                         `json:"publication_id"`
	PublicationPath   string                         `json:"publication_path"`
	ApproverLabel     string                         `json:"approver_label"`
	ApprovedAt        string                         `json:"approved_at"`
	ApprovalAuthority string                         `json:"approval_authority"`
	SourceReferences  []GitTeamMemorySourceReference `json:"source_references"`
	ObjectCommit      string                         `json:"object_commit"`
	ManifestCommit    string                         `json:"manifest_commit"`
}

type gitTeamMemoryProjectDocument struct {
	Schema    string `json:"schema"`
	ProjectID string `json:"project_id"`
}

type gitTeamMemoryObjectDocument struct {
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
}

type gitTeamMemoryManifestObject struct {
	MemoryID string `json:"memory_id"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
}

type gitTeamMemoryPublicationDocument struct {
	Schema            string                        `json:"schema"`
	PublicationID     string                        `json:"publication_id"`
	BatchID           string                        `json:"batch_id"`
	ProjectID         string                        `json:"project_id"`
	ProjectPath       string                        `json:"project_path"`
	ApprovalAuthority string                        `json:"approval_authority"`
	ApprovedAt        string                        `json:"approved_at"`
	Objects           []gitTeamMemoryManifestObject `json:"objects"`
}

type validatedGitTeamMemoryObject struct {
	document           gitTeamMemoryObjectDocument
	file               GitTeamMemoryIndexFile
	publicationID      string
	publicationPath    string
	manifestCommitHash string
}

type validatedGitTeamMemoryPack struct {
	projectID  string
	scanDigest string
	objects    map[string]validatedGitTeamMemoryObject
}

func decodeCanonicalGitTeamMemoryDocument(raw string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("git team memory document has trailing data")
	}
	canonical, err := canonicalGitTeamMemoryJSON(target)
	if err != nil || canonical != raw {
		return errors.New("git team memory document is not canonical")
	}
	return nil
}

func canonicalGitTeamMemoryIndexTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Year() >= 1970 && parsed.UTC().Format(time.RFC3339Nano) == value
}

func validGitTeamMemoryIndexStatus(value string) bool {
	return value == "active" || value == "superseded" || value == "removed"
}

func validateGitTeamMemoryIndexObject(document gitTeamMemoryObjectDocument, file GitTeamMemoryIndexFile) error {
	match := gitSharedMemoryPathPattern.FindStringSubmatch(file.Path)
	if len(match) != 2 || document.Schema != "pulse.git_team_memory.object.v1" ||
		document.MemoryID != match[1] || !gitSharedMemoryIDPattern.MatchString(document.MemoryID) ||
		document.Version < 1 || document.Version > 1_000_000 || !validGitTeamMemoryIndexStatus(document.Status) ||
		!validGitTeamMemoryApproverLabel(document.ApproverLabel) ||
		!canonicalGitTeamMemoryIndexTime(document.ApprovedAt) ||
		!trayBindingDigestPattern.MatchString(document.ApprovalAuthority) ||
		!trayBindingDigestPattern.MatchString(document.CandidateDigest) ||
		math.IsNaN(document.Confidence) || math.IsInf(document.Confidence, 0) {
		return ErrGitTeamMemoryIndexInvalid
	}
	if len(document.SourceReferences) != 1 {
		return ErrGitTeamMemoryIndexInvalid
	}
	prepared, err := prepareGitTeamMemoryCandidate(GitTeamMemoryCandidateInput{
		Kind: document.Kind, Statement: document.Content, Audience: "project",
		Confidence: document.Confidence, SourceReferences: document.SourceReferences,
		AdvisoryWarnings: document.Warnings,
	}, document.SourceReferences[0].SourceID, document.SourceReferences[0].VersionDigest)
	if err != nil || prepared.digest != document.CandidateDigest {
		return ErrGitTeamMemoryIndexConflict
	}
	return nil
}

func validateGitTeamMemoryIndexManifest(
	document gitTeamMemoryPublicationDocument,
	file GitTeamMemoryIndexFile,
	projectID string,
) error {
	match := gitSharedManifestPathPattern.FindStringSubmatch(file.Path)
	if len(match) != 2 || document.Schema != "pulse.git_team_memory.publication.v1" ||
		!validTrayIdentifier(document.PublicationID) || document.BatchID != match[1] ||
		document.ProjectID != projectID || document.ProjectPath != "pulse-memory/project.json" ||
		!trayBindingDigestPattern.MatchString(document.ApprovalAuthority) ||
		!canonicalGitTeamMemoryIndexTime(document.ApprovedAt) ||
		len(document.Objects) < 1 || len(document.Objects) > 20 {
		return ErrGitTeamMemoryIndexInvalid
	}
	seen := make(map[string]struct{}, len(document.Objects))
	for _, object := range document.Objects {
		pathMatch := gitSharedMemoryPathPattern.FindStringSubmatch(object.Path)
		if len(pathMatch) != 2 || object.MemoryID != pathMatch[1] ||
			!trayBindingDigestPattern.MatchString(object.SHA256) {
			return ErrGitTeamMemoryIndexInvalid
		}
		if _, duplicate := seen[object.Path]; duplicate {
			return ErrGitTeamMemoryIndexInvalid
		}
		seen[object.Path] = struct{}{}
	}
	return nil
}

func validateGitTeamMemoryIndexPack(req GitTeamMemoryIndexRequest) (validatedGitTeamMemoryPack, error) {
	if !gitCommitIDPattern.MatchString(req.HeadCommit) || len(req.Files) < 2 ||
		len(req.Files) > gitTeamMemoryIndexMaxFiles {
		return validatedGitTeamMemoryPack{}, ErrGitTeamMemoryIndexInvalid
	}
	files := make(map[string]GitTeamMemoryIndexFile, len(req.Files))
	totalBytes := 0
	scan := sha256.New()
	previousPath := ""
	for _, file := range req.Files {
		if file.Path <= previousPath || path.Clean(file.Path) != file.Path || strings.Contains(file.Path, `\`) ||
			(!gitSharedMemoryPathPattern.MatchString(file.Path) &&
				!gitSharedManifestPathPattern.MatchString(file.Path) && file.Path != "pulse-memory/project.json") ||
			!utf8.ValidString(file.Content) || !trayBindingDigestPattern.MatchString(file.SHA256) ||
			!gitCommitIDPattern.MatchString(file.CommitHash) {
			return validatedGitTeamMemoryPack{}, ErrGitTeamMemoryIndexInvalid
		}
		totalBytes += len([]byte(file.Content))
		if totalBytes > gitTeamMemoryIndexMaxBytes {
			return validatedGitTeamMemoryPack{}, ErrGitTeamMemoryIndexInvalid
		}
		digest := sha256.Sum256([]byte(file.Content))
		if hex.EncodeToString(digest[:]) != file.SHA256 {
			return validatedGitTeamMemoryPack{}, ErrGitTeamMemoryIndexConflict
		}
		previousPath = file.Path
		files[file.Path] = file
		scan.Write([]byte(file.Path))
		scan.Write([]byte{0})
		scan.Write([]byte(file.SHA256))
		scan.Write([]byte{0})
		scan.Write([]byte(file.CommitHash))
		scan.Write([]byte{0})
	}
	projectFile, ok := files["pulse-memory/project.json"]
	if !ok {
		return validatedGitTeamMemoryPack{}, ErrGitTeamMemoryIndexInvalid
	}
	var projectDocument gitTeamMemoryProjectDocument
	if err := decodeCanonicalGitTeamMemoryDocument(projectFile.Content, &projectDocument); err != nil ||
		projectDocument.Schema != "pulse.git_team_memory.project.v1" ||
		projectDocument.ProjectID != req.PortableProjectID {
		return validatedGitTeamMemoryPack{}, ErrGitTeamMemoryIndexConflict
	}
	type manifestFile struct {
		document gitTeamMemoryPublicationDocument
		file     GitTeamMemoryIndexFile
	}
	manifests := []manifestFile{}
	objects := make(map[string]validatedGitTeamMemoryObject)
	for _, file := range req.Files {
		switch {
		case gitSharedManifestPathPattern.MatchString(file.Path):
			var document gitTeamMemoryPublicationDocument
			if err := decodeCanonicalGitTeamMemoryDocument(file.Content, &document); err != nil ||
				validateGitTeamMemoryIndexManifest(document, file, req.PortableProjectID) != nil {
				return validatedGitTeamMemoryPack{}, ErrGitTeamMemoryIndexInvalid
			}
			manifests = append(manifests, manifestFile{document: document, file: file})
		case gitSharedMemoryPathPattern.MatchString(file.Path):
			var document gitTeamMemoryObjectDocument
			if err := decodeCanonicalGitTeamMemoryDocument(file.Content, &document); err != nil ||
				validateGitTeamMemoryIndexObject(document, file) != nil {
				return validatedGitTeamMemoryPack{}, ErrGitTeamMemoryIndexConflict
			}
			objects[file.Path] = validatedGitTeamMemoryObject{document: document, file: file}
		}
	}
	if len(manifests) == 0 {
		return validatedGitTeamMemoryPack{}, ErrGitTeamMemoryIndexInvalid
	}
	for objectPath, object := range objects {
		matched := false
		for _, manifest := range manifests {
			if manifest.document.ApprovalAuthority != object.document.ApprovalAuthority ||
				manifest.document.ApprovedAt != object.document.ApprovedAt {
				continue
			}
			for _, reference := range manifest.document.Objects {
				if reference.Path == objectPath && reference.MemoryID == object.document.MemoryID &&
					reference.SHA256 == object.file.SHA256 {
					object.publicationID = manifest.document.PublicationID
					object.publicationPath = manifest.file.Path
					object.manifestCommitHash = manifest.file.CommitHash
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return validatedGitTeamMemoryPack{}, ErrGitTeamMemoryIndexConflict
		}
		objects[objectPath] = object
	}
	return validatedGitTeamMemoryPack{
		projectID: req.PortableProjectID, scanDigest: hex.EncodeToString(scan.Sum(nil)), objects: objects,
	}, nil
}

type gitTeamMemoryProjectionRow struct {
	memoryID, digest, status, contentSHA string
	eventID                              sql.NullInt64
}

func (s *Store) ReconcileGitTeamMemoryIndex(
	req GitTeamMemoryIndexRequest,
	now time.Time,
) (GitTeamMemoryIndexReceipt, []CapsuleEventDoc, error) {
	if err := validateGitTeamMemoryEnvelope(s, req.Schema, GitTeamMemoryIndexSchema,
		req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return GitTeamMemoryIndexReceipt{}, nil, err
	}
	pack, err := validateGitTeamMemoryIndexPack(req)
	if err != nil {
		return GitTeamMemoryIndexReceipt{}, nil, err
	}
	indexedAt := now.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return GitTeamMemoryIndexReceipt{}, nil, err
	}
	defer tx.Rollback()
	existing := make(map[string]gitTeamMemoryProjectionRow)
	rows, err := tx.Query(`
		SELECT memory_id, candidate_digest, status, content_sha256, event_id
		  FROM git_memory_shared_projection WHERE portable_project_id=?`, pack.projectID)
	if err != nil {
		return GitTeamMemoryIndexReceipt{}, nil, err
	}
	for rows.Next() {
		var row gitTeamMemoryProjectionRow
		if err := rows.Scan(&row.memoryID, &row.digest, &row.status, &row.contentSHA, &row.eventID); err != nil {
			rows.Close()
			return GitTeamMemoryIndexReceipt{}, nil, err
		}
		existing[row.memoryID] = row
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return GitTeamMemoryIndexReceipt{}, nil, err
	}
	rows.Close()

	paths := make([]string, 0, len(pack.objects))
	for objectPath := range pack.objects {
		paths = append(paths, objectPath)
	}
	sort.Strings(paths)
	docs := []CapsuleEventDoc{}
	changed, removed, active := 0, 0, 0
	for _, objectPath := range paths {
		object := pack.objects[objectPath]
		document := object.document
		prior, hadPrior := existing[document.MemoryID]
		delete(existing, document.MemoryID)
		unchanged := hadPrior && prior.digest == document.CandidateDigest &&
			prior.status == document.Status && prior.contentSHA == object.file.SHA256 &&
			((document.Status == "active" && prior.eventID.Valid) ||
				(document.Status != "active" && !prior.eventID.Valid))
		var eventID sql.NullInt64
		if unchanged {
			eventID = prior.eventID
		} else {
			changed++
			if hadPrior && prior.eventID.Valid {
				if _, err := tx.Exec(`
					UPDATE git_memory_shared_projection
					   SET status='superseded', event_id=NULL, indexed_at=?
					 WHERE portable_project_id=? AND memory_id=?`, indexedAt, pack.projectID, prior.memoryID); err != nil {
					return GitTeamMemoryIndexReceipt{}, nil, err
				}
				if _, err := tx.Exec(`DELETE FROM events WHERE id=?`, prior.eventID.Int64); err != nil {
					return GitTeamMemoryIndexReceipt{}, nil, err
				}
			}
			if document.Status == "active" {
				id, err := projectCapsuleEvent(tx, document.Kind, document.Content, document.ApprovedAt,
					indexedAt, []string{"visibility:project", "project:" + pack.projectID})
				if err != nil {
					return GitTeamMemoryIndexReceipt{}, nil, err
				}
				eventID = sql.NullInt64{Int64: id, Valid: true}
				docs = append(docs, CapsuleEventDoc{EventID: id, Text: document.Content})
			}
		}
		if document.Status == "active" {
			active++
		}
		refsJSON, _ := json.Marshal(document.SourceReferences)
		warningsJSON, _ := json.Marshal(document.Warnings)
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO git_memory_shared_versions(
				portable_project_id, memory_id, version, candidate_digest, status, kind, content,
				confidence, approver_label, approved_at, authority_digest, source_refs_json,
				warnings_json, file_path, content_sha256, publication_id, publication_path,
				object_commit_hash, manifest_commit_hash, indexed_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			pack.projectID, document.MemoryID, document.Version, document.CandidateDigest,
			document.Status, document.Kind, document.Content, document.Confidence,
			document.ApproverLabel, document.ApprovedAt, document.ApprovalAuthority,
			string(refsJSON), string(warningsJSON), object.file.Path, object.file.SHA256,
			object.publicationID, object.publicationPath, object.file.CommitHash,
			object.manifestCommitHash, indexedAt); err != nil {
			return GitTeamMemoryIndexReceipt{}, nil, err
		}
		if _, err := tx.Exec(`
			INSERT INTO git_memory_shared_projection(
				portable_project_id, memory_id, version, candidate_digest, status, kind, content,
				confidence, approver_label, approved_at, authority_digest, source_refs_json,
				warnings_json, file_path, content_sha256, publication_id, publication_path,
				object_commit_hash, manifest_commit_hash, event_id, indexed_at,
				repository_id, binding_digest
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(portable_project_id, memory_id) DO UPDATE SET
				version=excluded.version, candidate_digest=excluded.candidate_digest,
				status=excluded.status, kind=excluded.kind, content=excluded.content,
				confidence=excluded.confidence, approver_label=excluded.approver_label,
				approved_at=excluded.approved_at, authority_digest=excluded.authority_digest,
				source_refs_json=excluded.source_refs_json, warnings_json=excluded.warnings_json,
				file_path=excluded.file_path, content_sha256=excluded.content_sha256,
				publication_id=excluded.publication_id, publication_path=excluded.publication_path,
				object_commit_hash=excluded.object_commit_hash,
				manifest_commit_hash=excluded.manifest_commit_hash,
				event_id=excluded.event_id, indexed_at=excluded.indexed_at,
				repository_id=excluded.repository_id,
				binding_digest=excluded.binding_digest`,
			pack.projectID, document.MemoryID, document.Version, document.CandidateDigest,
			document.Status, document.Kind, document.Content, document.Confidence,
			document.ApproverLabel, document.ApprovedAt, document.ApprovalAuthority,
			string(refsJSON), string(warningsJSON), object.file.Path, object.file.SHA256,
			object.publicationID, object.publicationPath, object.file.CommitHash,
			object.manifestCommitHash, nullableInt64(eventID), indexedAt,
			req.RepositoryID, req.BindingDigest); err != nil {
			return GitTeamMemoryIndexReceipt{}, nil, err
		}
	}
	for _, prior := range existing {
		if prior.status == "removed" && !prior.eventID.Valid {
			continue
		}
		changed++
		removed++
		if prior.eventID.Valid {
			if _, err := tx.Exec(`
				UPDATE git_memory_shared_projection
				   SET status='removed', event_id=NULL, indexed_at=?
				 WHERE portable_project_id=? AND memory_id=?`, indexedAt, pack.projectID, prior.memoryID); err != nil {
				return GitTeamMemoryIndexReceipt{}, nil, err
			}
			if _, err := tx.Exec(`DELETE FROM events WHERE id=?`, prior.eventID.Int64); err != nil {
				return GitTeamMemoryIndexReceipt{}, nil, err
			}
		}
		if _, err := tx.Exec(`
			UPDATE git_memory_shared_projection
			   SET status='removed', event_id=NULL, indexed_at=?
			 WHERE portable_project_id=? AND memory_id=?`, indexedAt, pack.projectID, prior.memoryID); err != nil {
			return GitTeamMemoryIndexReceipt{}, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return GitTeamMemoryIndexReceipt{}, nil, err
	}
	receiptDigest := sha256.Sum256([]byte("pulse-git-memory-index-receipt-v1\x00" +
		pack.projectID + "\x00" + req.HeadCommit + "\x00" + pack.scanDigest))
	return GitTeamMemoryIndexReceipt{
		Schema:            GitTeamMemoryIndexReceiptSchema,
		ReceiptID:         "shared_index_" + hex.EncodeToString(receiptDigest[:16]),
		PortableProjectID: pack.projectID, HeadCommit: req.HeadCommit,
		ScanDigest: pack.scanDigest, State: "reconciled", ActiveCount: active,
		ChangedCount: changed, RemovedCount: removed, IndexedAt: indexedAt,
	}, docs, nil
}

func nullableInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func (s *Store) GitTeamMemoryProvenanceForEvents(eventIDs []int64) (map[int64]GitTeamMemoryProvenance, error) {
	result := make(map[int64]GitTeamMemoryProvenance)
	for _, eventID := range eventIDs {
		if eventID < 1 {
			continue
		}
		var item GitTeamMemoryProvenance
		var refsJSON string
		err := s.db.QueryRow(`
			SELECT memory_id, portable_project_id, version, kind, file_path, content_sha256,
			       publication_id, publication_path, approver_label, approved_at,
			       authority_digest, source_refs_json, object_commit_hash, manifest_commit_hash
			  FROM git_memory_shared_projection
			 WHERE event_id=? AND status='active'`, eventID).Scan(
			&item.MemoryID, &item.PortableProjectID, &item.Version, &item.Kind,
			&item.FilePath, &item.ContentSHA256, &item.PublicationID, &item.PublicationPath,
			&item.ApproverLabel, &item.ApprovedAt, &item.ApprovalAuthority,
			&refsJSON, &item.ObjectCommit, &item.ManifestCommit,
		)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load git team memory provenance: %w", err)
		}
		if err := json.Unmarshal([]byte(refsJSON), &item.SourceReferences); err != nil {
			return nil, fmt.Errorf("load git team memory provenance: %w", err)
		}
		item.Visibility = "project"
		result[eventID] = item
	}
	return result, nil
}

func (s *Store) UnindexedGitTeamMemoryEventDocs(projectID string) ([]CapsuleEventDoc, error) {
	if !portableProjectIDPattern.MatchString(projectID) {
		return nil, ErrGitTeamMemoryIndexInvalid
	}
	rows, err := s.db.Query(`
		SELECT projection.event_id, projection.content
		  FROM git_memory_shared_projection projection
		  LEFT JOIN event_embeddings embedding ON embedding.event_id=projection.event_id
		 WHERE projection.portable_project_id=? AND projection.status='active'
		   AND projection.event_id IS NOT NULL AND embedding.event_id IS NULL
		 ORDER BY projection.memory_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []CapsuleEventDoc
	for rows.Next() {
		var doc CapsuleEventDoc
		if err := rows.Scan(&doc.EventID, &doc.Text); err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}
