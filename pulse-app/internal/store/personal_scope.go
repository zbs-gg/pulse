package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
)

const (
	MemoryScopeProject        = "project"
	MemoryScopePersonalGlobal = "personal_global"
)

type personalMemoryScope struct {
	ProjectNamespaceID string
	OriginalRepository string
	Scope              string
}

type MemoryScopeMoveReceipt struct {
	WriteReceipt      MemoryWriteReceipt `json:"write_receipt"`
	ObjectID          string             `json:"object_id"`
	Scope             string             `json:"scope"`
	LogicalGeneration int                `json:"logical_generation"`
}

// PersonalMemoryScopeSnapshot is the content-free eligibility boundary used by
// retrieval. When present, readers may consider only the current project
// namespace, Personal Global, and the current repository's approved project
// memory. EligibilityRevision changes after every private-memory create,
// correction, move, or delete so long-lived indexes can reload before ranking.
type PersonalMemoryScopeSnapshot struct {
	BindingDigest       string
	RepositoryID        string
	ProjectNamespaceID  string
	EligibilityRevision int64
}

func stableProjectNamespace(repositoryID string) string {
	digest := sha256.Sum256([]byte("pulse-personal-project-namespace-v1\x1f" + repositoryID))
	return "project_" + hex.EncodeToString(digest[:16])
}

func unresolvedProjectRepository(bindingDigest string) string {
	digest := sha256.Sum256([]byte("pulse-personal-unresolved-project-v1\x1f" + bindingDigest))
	return "repository_unresolved_" + hex.EncodeToString(digest[:16])
}

// RegisterPersonalProjectLabel stores only a short human label derived by the
// trusted launcher. It deliberately rejects paths and control characters so
// the one-vault project index never becomes a machine-path registry.
func (s *Store) RegisterPersonalProjectLabel(repositoryID, label string) error {
	if s == nil || s.db == nil || !validTrayIdentifier(repositoryID) {
		return errors.New("Personal project label boundary is invalid")
	}
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 96 || label == "." || label == ".." ||
		strings.ContainsAny(label, `/\`) {
		return errors.New("Personal project label is invalid")
	}
	for _, value := range label {
		if unicode.IsControl(value) {
			return errors.New("Personal project label is invalid")
		}
	}
	_, err := s.db.Exec(`
		INSERT INTO personal_project_labels(repository_id, label, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(repository_id) DO UPDATE SET
		    label=excluded.label,
		    updated_at=excluded.updated_at`,
		repositoryID, label, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) currentPersonalMemoryScope(bindingDigest string) personalMemoryScope {
	repository := unresolvedProjectRepository(bindingDigest)
	expectedBinding, _, _ := s.productRuntimeAuthority()
	s.continuityAuthorityMu.RLock()
	if s.continuityRepository != "" && bindingDigest == expectedBinding {
		repository = s.continuityRepository
	}
	s.continuityAuthorityMu.RUnlock()
	return personalMemoryScope{
		ProjectNamespaceID: stableProjectNamespace(repository),
		OriginalRepository: repository,
		Scope:              MemoryScopeProject,
	}
}

func backfillPersonalScopeForBindingTx(
	tx *sql.Tx,
	bindingDigest string,
	scope personalMemoryScope,
) error {
	previousRepository := unresolvedProjectRepository(bindingDigest)
	_, err := tx.Exec(`
		UPDATE private_memory_objects
		   SET project_namespace_id=?, original_repository_id=?
		 WHERE memory_scope='project'
		   AND (
		       project_namespace_id='' OR
		       project_namespace_id=?
		   )
		   AND object_id IN (
		       SELECT object.object_id
		         FROM private_memory_objects object
		         JOIN memory_tray_candidates candidate
		           ON candidate.candidate_id=object.created_from_candidate_id
		         JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		        WHERE ledger.binding_digest=?
		   )`,
		scope.ProjectNamespaceID, scope.OriginalRepository,
		stableProjectNamespace(previousRepository), bindingDigest,
	)
	return err
}

func advancePersonalEligibilityTx(tx *sql.Tx, now time.Time) error {
	result, err := tx.Exec(`
		UPDATE personal_memory_scope_state
		   SET eligibility_revision=eligibility_revision+1, updated_at=?
		 WHERE singleton=1`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrMemoryTrayUnavailable
	}
	return nil
}

// CurrentPersonalMemoryScopeSnapshot returns an active product boundary only
// after both runtime and repository authority have been configured. Ordinary
// Local Preview retrieval therefore keeps its historical unscoped behavior.
func (s *Store) CurrentPersonalMemoryScopeSnapshot() (PersonalMemoryScopeSnapshot, bool, error) {
	bindingDigest, repositoryID, ok := s.ProductRuntimeBoundary()
	if !ok {
		return PersonalMemoryScopeSnapshot{}, false, nil
	}
	var revision int64
	if err := s.db.QueryRow(`
		SELECT eligibility_revision
		  FROM personal_memory_scope_state
		 WHERE singleton=1`).Scan(&revision); err != nil {
		return PersonalMemoryScopeSnapshot{}, false, err
	}
	scope := s.currentPersonalMemoryScope(bindingDigest)
	return PersonalMemoryScopeSnapshot{
		BindingDigest:       bindingDigest,
		RepositoryID:        repositoryID,
		ProjectNamespaceID:  scope.ProjectNamespaceID,
		EligibilityRevision: revision,
	}, true, nil
}
