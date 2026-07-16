package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	ProjectSourceRegisterSchema       = "pulse.project_source.register.v1"
	ProjectSourceStatusSchema         = "pulse.project_source.status.v1"
	projectSourceMaxBytes       int64 = 8 << 20
)

var portableProjectIDPattern = regexp.MustCompile(`^project_[a-f0-9]{32}$`)

var (
	ErrProjectSourceUnavailable = errors.New("project source registry is unavailable for this store kind")
	ErrProjectSourceAuthority   = errors.New("project source metadata does not match the bound workspace")
	ErrProjectSourceInvalid     = errors.New("project source metadata is invalid")
)

type ProjectSourceRegistration struct {
	Schema            string `json:"schema"`
	PortableProjectID string `json:"portable_project_id"`
	RepositoryID      string `json:"repository_id"`
	BindingDigest     string `json:"binding_digest"`
	SourceKind        string `json:"source_kind"`
	Locator           string `json:"locator"`
	VersionDigest     string `json:"version_digest"`
	ByteCount         int64  `json:"byte_count"`
	ObservedAt        string `json:"observed_at"`
}

type ProjectSourceRegistrationResult struct {
	Schema               string `json:"schema"`
	PortableProjectID    string `json:"portable_project_id"`
	SourceID             string `json:"source_id"`
	VersionID            string `json:"version_id"`
	VersionDigest        string `json:"version_digest"`
	Locator              string `json:"locator"`
	ByteCount            int64  `json:"byte_count"`
	Status               string `json:"status"`
	ProcessingState      string `json:"processing_state"`
	RegisteredAt         string `json:"registered_at"`
	CurrentVersionDigest string `json:"current_version_digest"`
}

type ProjectSourceStatusRequest struct {
	Schema            string `json:"schema"`
	PortableProjectID string `json:"portable_project_id"`
	RepositoryID      string `json:"repository_id"`
	BindingDigest     string `json:"binding_digest"`
	SourceID          string `json:"source_id"`
}

type ProjectSourceStatus struct {
	Schema               string `json:"schema"`
	PortableProjectID    string `json:"portable_project_id"`
	SourceID             string `json:"source_id"`
	SourceKind           string `json:"source_kind"`
	Locator              string `json:"locator"`
	CurrentVersionID     string `json:"current_version_id"`
	CurrentVersionDigest string `json:"current_version_digest"`
	ByteCount            int64  `json:"byte_count"`
	ObservedAt           string `json:"observed_at"`
	ProcessingState      string `json:"processing_state"`
	VersionCount         int    `json:"version_count"`
}

func (s *Store) validateProjectSourceAuthority(portableProjectID, repositoryID, bindingDigest string) error {
	if s == nil || !s.productTrayRequired() {
		return ErrProjectSourceUnavailable
	}
	if !portableProjectIDPattern.MatchString(portableProjectID) || !validTrayIdentifier(repositoryID) ||
		!trayBindingDigestPattern.MatchString(bindingDigest) {
		return ErrProjectSourceInvalid
	}
	expectedBinding, expectedRepository, ok := s.ProductRuntimeBoundary()
	if !ok || expectedBinding != bindingDigest || expectedRepository != repositoryID {
		return ErrProjectSourceAuthority
	}
	return nil
}

func validateProjectSourceRegistration(req ProjectSourceRegistration) error {
	if req.Schema != ProjectSourceRegisterSchema || req.SourceKind != "repository_text" ||
		!validProjectSourceLocator(req.Locator) || !trayBindingDigestPattern.MatchString(req.VersionDigest) ||
		req.ByteCount < 0 || req.ByteCount > projectSourceMaxBytes || !canonicalProjectSourceTime(req.ObservedAt) {
		return ErrProjectSourceInvalid
	}
	return nil
}

func validProjectSourceLocator(locator string) bool {
	if locator == "" || len(locator) > 512 || !utf8.ValidString(locator) || !norm.NFC.IsNormalString(locator) ||
		strings.TrimSpace(locator) != locator || strings.Contains(locator, `\`) || strings.HasPrefix(locator, "/") ||
		containsUnsafeMemoryUnicode(locator) || path.Clean(locator) != locator || locator == "." || strings.HasPrefix(locator, "../") {
		return false
	}
	for _, part := range strings.Split(locator, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	if strings.EqualFold(strings.Split(locator, "/")[0], "pulse-memory") {
		return false
	}
	ext := strings.ToLower(path.Ext(locator))
	return ext == ".md" || ext == ".txt" || ext == ".markdown"
}

func canonicalProjectSourceTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Year() >= 1970 && parsed.UTC().Format(time.RFC3339Nano) == value
}

func deterministicGitMemoryID(prefix string, fields ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(append([]string{"pulse-git-memory-v1", prefix}, fields...), "\x00")))
	return prefix + "_" + hex.EncodeToString(digest[:16])
}

func (s *Store) RegisterProjectSource(req ProjectSourceRegistration, now time.Time) (ProjectSourceRegistrationResult, error) {
	if err := s.validateProjectSourceAuthority(req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return ProjectSourceRegistrationResult{}, err
	}
	if err := validateProjectSourceRegistration(req); err != nil {
		return ProjectSourceRegistrationResult{}, err
	}
	sourceID := deterministicGitMemoryID("source", req.PortableProjectID, req.SourceKind, req.Locator)
	versionID := deterministicGitMemoryID("source_version", sourceID, req.VersionDigest)
	createdAt := now.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return ProjectSourceRegistrationResult{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO git_memory_projects(
			portable_project_id, repository_id, binding_digest, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)`, req.PortableProjectID, req.RepositoryID, req.BindingDigest, createdAt, createdAt); err != nil {
		return ProjectSourceRegistrationResult{}, err
	}
	var storedRepository, storedBinding string
	if err := tx.QueryRow(`SELECT repository_id, binding_digest FROM git_memory_projects WHERE portable_project_id=?`,
		req.PortableProjectID).Scan(&storedRepository, &storedBinding); err != nil {
		return ProjectSourceRegistrationResult{}, err
	}
	if storedRepository != req.RepositoryID || storedBinding != req.BindingDigest {
		return ProjectSourceRegistrationResult{}, ErrProjectSourceAuthority
	}

	status := "registered"
	var currentVersionID, currentDigest string
	var currentByteCount int64
	err = tx.QueryRow(`
		SELECT current_version_id, current_version_digest, current_byte_count FROM git_memory_sources
		 WHERE source_id=?`, sourceID).Scan(&currentVersionID, &currentDigest, &currentByteCount)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`
			INSERT INTO git_memory_sources(
				source_id, portable_project_id, source_kind, locator, current_version_id,
				current_version_digest, current_byte_count, current_observed_at,
				registered_at, updated_at, processing_state
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')`,
			sourceID, req.PortableProjectID, req.SourceKind, req.Locator, versionID,
			req.VersionDigest, req.ByteCount, req.ObservedAt, createdAt, createdAt); err != nil {
			return ProjectSourceRegistrationResult{}, err
		}
		if _, err := tx.Exec(`
			INSERT INTO git_memory_source_versions(
				version_id, source_id, version_digest, byte_count, observed_at, registered_at, processing_state
			) VALUES (?, ?, ?, ?, ?, ?, 'pending')`,
			versionID, sourceID, req.VersionDigest, req.ByteCount, req.ObservedAt, createdAt); err != nil {
			return ProjectSourceRegistrationResult{}, err
		}
	case err != nil:
		return ProjectSourceRegistrationResult{}, err
	case currentDigest == req.VersionDigest:
		if currentByteCount != req.ByteCount {
			return ProjectSourceRegistrationResult{}, ErrProjectSourceInvalid
		}
		status = "unchanged"
		versionID = currentVersionID
		if _, err := tx.Exec(`
			UPDATE git_memory_sources SET current_observed_at=?, updated_at=? WHERE source_id=?`,
			req.ObservedAt, createdAt, sourceID); err != nil {
			return ProjectSourceRegistrationResult{}, err
		}
	default:
		status = "changed"
		if _, err := tx.Exec(`
			INSERT INTO git_memory_source_versions(
				version_id, source_id, version_digest, byte_count, observed_at, registered_at, processing_state
			) VALUES (?, ?, ?, ?, ?, ?, 'pending')`,
			versionID, sourceID, req.VersionDigest, req.ByteCount, req.ObservedAt, createdAt); err != nil {
			return ProjectSourceRegistrationResult{}, err
		}
		if _, err := tx.Exec(`
			UPDATE git_memory_sources SET current_version_id=?, current_version_digest=?,
			       current_byte_count=?, current_observed_at=?, processing_state='pending', updated_at=?
			 WHERE source_id=?`, versionID, req.VersionDigest, req.ByteCount, req.ObservedAt, createdAt, sourceID); err != nil {
			return ProjectSourceRegistrationResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ProjectSourceRegistrationResult{}, err
	}
	return ProjectSourceRegistrationResult{
		Schema: ProjectSourceRegisterSchema, PortableProjectID: req.PortableProjectID,
		SourceID: sourceID, VersionID: versionID, VersionDigest: req.VersionDigest,
		Locator: req.Locator, ByteCount: req.ByteCount, Status: status,
		ProcessingState: "pending", RegisteredAt: createdAt, CurrentVersionDigest: req.VersionDigest,
	}, nil
}

func (s *Store) ProjectSourceStatus(req ProjectSourceStatusRequest) (ProjectSourceStatus, error) {
	if err := s.validateProjectSourceAuthority(req.PortableProjectID, req.RepositoryID, req.BindingDigest); err != nil {
		return ProjectSourceStatus{}, err
	}
	if req.Schema != ProjectSourceStatusSchema || !validTrayIdentifier(req.SourceID) {
		return ProjectSourceStatus{}, ErrProjectSourceInvalid
	}
	var result ProjectSourceStatus
	result.Schema = ProjectSourceStatusSchema
	err := s.db.QueryRow(`
		SELECT source.portable_project_id, source.source_id, source.source_kind, source.locator,
		       source.current_version_id, source.current_version_digest, source.current_byte_count,
		       source.current_observed_at, source.processing_state,
		       (SELECT COUNT(*) FROM git_memory_source_versions version WHERE version.source_id=source.source_id)
		  FROM git_memory_sources source
		 WHERE source.source_id=? AND source.portable_project_id=?`, req.SourceID, req.PortableProjectID).Scan(
		&result.PortableProjectID, &result.SourceID, &result.SourceKind, &result.Locator,
		&result.CurrentVersionID, &result.CurrentVersionDigest, &result.ByteCount,
		&result.ObservedAt, &result.ProcessingState, &result.VersionCount,
	)
	if err != nil {
		return ProjectSourceStatus{}, fmt.Errorf("project source status: %w", err)
	}
	return result, nil
}
