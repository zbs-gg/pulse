package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/capture"
	"github.com/nkkmnk/pulse/internal/historicalingest"
	"github.com/nkkmnk/pulse/internal/platform"
	_ "modernc.org/sqlite"
)

type HistoricalApplyCapability struct {
	AuthorizationID string
	AuditID         string
	Token           string
	ExpiresAt       time.Time
}

type HistoricalBackupReceipt struct {
	BackupID       string
	WriteSetDigest string
	IntegrityOK    bool
	ForeignKeysOK  bool
	path           string
}

func (s *Store) CompileHistoricalWriteSet(
	source historicalingest.ApplySource,
	bindingDigest, repositoryID string,
) (historicalingest.WriteSet, string, error) {
	if s == nil || (s.storeKind != StoreKindPersonal && s.storeKind != StoreKindDesk) {
		return historicalingest.WriteSet{}, "", historicalingest.ErrApplyDestination
	}
	if source.ManifestDigest == "" || source.Manifest.JobID == "" || source.Manifest.Revision < 1 ||
		source.Manifest.SourceSnapshotDigest != source.Snapshot.Digest || source.Contract.SchemaDigest != historicalingest.SchemaDigest() {
		return historicalingest.WriteSet{}, "", historicalingest.ErrApplyWriteSetInvalid
	}
	currentBinding, currentRepository, ok := s.ProductRuntimeBoundary()
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	if !ok || bindingDigest != currentBinding || bindingDigest != expectedBinding || repositoryID != currentRepository ||
		expectedResolver < 1 {
		return historicalingest.WriteSet{}, "", historicalingest.ErrApplyDestination
	}
	generation, err := s.historicalDestinationGeneration(s.db)
	if err != nil {
		return historicalingest.WriteSet{}, "", err
	}
	set := historicalingest.WriteSet{
		Schema: historicalingest.WriteSetSchemaV1, JobID: source.Manifest.JobID, Revision: source.Manifest.Revision,
		ManifestDigest: source.ManifestDigest, SourceSnapshotDigest: source.Snapshot.Digest,
		SchemaDigest: source.Contract.SchemaDigest, RunnerContractDigest: source.Contract.Digest,
		ParserVersion: source.Contract.ParserVersion, PromptVersion: source.Contract.PromptVersion,
		ModelID: source.Contract.ModelID, ModelEffort: source.Contract.ModelEffort,
		DestinationStoreID: s.storeID, DestinationGeneration: generation,
		DestinationBindingDigest: bindingDigest, RepositoryID: repositoryID,
		PolicyEpoch: expectedPolicy, ResolverEpoch: expectedResolver,
		MaterializerVersion: historicalingest.MaterializerVersionV1, DedupVersion: historicalingest.DedupVersionV1,
	}
	planned := map[string]historicalingest.WriteTarget{}
	for _, item := range source.IncludedItems() {
		writeItem, candidate, scope, err := historicalCanonicalCandidate(item, source.Manifest.JobID, repositoryID)
		if err != nil {
			return historicalingest.WriteSet{}, "", err
		}
		prepared, err := preparePrivateCandidate(candidate)
		if err != nil {
			return historicalingest.WriteSet{}, "", fmt.Errorf("compile historical candidate %s: %w", item.CandidateID, err)
		}
		writeItem.ContentDigest = prepared.digest
		key := historicalDedupKey(scope, prepared.kind, prepared.digest)
		if target, exists := planned[key]; exists {
			target.Outcome = historicalingest.ItemDeduplicated
			writeItem.Target = target
		} else {
			target, found, err := s.existingHistoricalTarget(scope, prepared.kind, prepared.digest)
			if err != nil {
				return historicalingest.WriteSet{}, "", err
			}
			if !found {
				target = historicalingest.WriteTarget{
					Outcome: historicalingest.ItemCreated, ObjectKind: prepared.kind,
					ObjectID:     historicalObjectID(source.Manifest.JobID, item.CandidateID),
					ObjectDigest: prepared.digest, LogicalGeneration: 1,
				}
			}
			planned[key] = target
			writeItem.Target = target
		}
		set.Items = append(set.Items, writeItem)
	}
	if len(set.Items) == 0 {
		return historicalingest.WriteSet{}, "", historicalingest.ErrApplyWriteSetInvalid
	}
	set.TargetVersionsDigest = historicalingest.TargetVersionsDigest(set.Items)
	encoded, digest, err := historicalingest.EncodeWriteSet(set)
	if err != nil {
		return historicalingest.WriteSet{}, "", err
	}
	if err := s.persistHistoricalWriteSet(source, set, encoded, digest); err != nil {
		return historicalingest.WriteSet{}, "", err
	}
	return set, digest, nil
}

func historicalCanonicalCandidate(item historicalingest.MaterialItem, jobID, repositoryID string) (historicalingest.CanonicalWriteItem, PrivateMemoryCandidate, personalMemoryScope, error) {
	summary := historicalMaterialSummary(item)
	if summary == "" || len(summary) > 1200 {
		return historicalingest.CanonicalWriteItem{}, PrivateMemoryCandidate{}, personalMemoryScope{}, historicalingest.ErrApplyWriteSetInvalid
	}
	kind := historicalCapsuleKind(item)
	scope := historicalPersonalScope(item.Scope, jobID, repositoryID)
	capsule := MemoryCapsule{
		Schema: MemoryCapsuleSchema,
		Source: CapsuleSource{Host: "codex", ConversationScope: "project_context", Timestamp: item.ValidTime.From.UTC().Format(time.RFC3339)},
		Items: []MemoryCapsuleItem{{
			Kind: kind, RedactedSummary: summary, Confidence: item.Confidence,
			EvidenceHint: "user_confirmed", PrivacyTier: "private", Retention: "long_term",
			Tags: []string{"historical", "material:" + string(item.Kind), "epistemic:" + string(item.EpistemicStatus)},
		}},
		RawInputIncluded: false,
	}
	write := historicalingest.CanonicalWriteItem{
		CandidateID: item.CandidateID, MaterialKind: item.Kind, CapsuleKind: kind, Summary: summary,
		Confidence: item.Confidence, Scope: item.Scope, EpistemicStatus: item.EpistemicStatus,
		Derivation: item.Derivation, ValidTime: item.ValidTime, SourceRefs: append([]historicalingest.SourceRef(nil), item.SourceRefs...),
	}
	return write, PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &capsule}, scope, nil
}

func historicalMaterialSummary(item historicalingest.MaterialItem) string {
	switch item.Kind {
	case historicalingest.MaterialKindEvent:
		if item.Payload.Title != "" && item.Payload.Summary != "" && item.Payload.Title != item.Payload.Summary {
			return strings.TrimSpace(item.Payload.Title + " — " + item.Payload.Summary)
		}
		return strings.TrimSpace(item.Payload.Summary)
	case historicalingest.MaterialKindAssertion:
		return strings.TrimSpace(item.Payload.SubjectID + " " + item.Payload.Predicate + " " + item.Payload.ObjectValue)
	case historicalingest.MaterialKindPerson, historicalingest.MaterialKindProject:
		if item.Payload.Summary != "" {
			return strings.TrimSpace(item.Payload.Name + " — " + item.Payload.Summary)
		}
		return strings.TrimSpace(item.Payload.Name)
	case historicalingest.MaterialKindRelation:
		return strings.TrimSpace(item.Payload.SubjectID + " " + item.Payload.Predicate + " " + item.Payload.ObjectID)
	default:
		return strings.TrimSpace(item.Payload.Summary)
	}
}

func historicalCapsuleKind(item historicalingest.MaterialItem) string {
	switch item.Kind {
	case historicalingest.MaterialKindDecision:
		return "decision"
	case historicalingest.MaterialKindAssertion:
		return "fact"
	case historicalingest.MaterialKindPerson, historicalingest.MaterialKindRelation:
		return "relationship_note"
	case historicalingest.MaterialKindState:
		return "state_signal"
	case historicalingest.MaterialKindContinuity:
		if item.Payload.ContinuityStatus == "open" {
			return "open_loop"
		}
		return "project_state"
	default:
		return "project_state"
	}
}

func historicalPersonalScope(scope historicalingest.Scope, jobID, repositoryID string) personalMemoryScope {
	switch scope.Kind {
	case historicalingest.ScopeGlobal:
		return personalMemoryScope{ProjectNamespaceID: "", OriginalRepository: "", Scope: MemoryScopePersonalGlobal}
	case historicalingest.ScopeProject:
		currentNamespace := stableProjectNamespace(repositoryID)
		if scope.ProjectID == repositoryID || scope.ProjectID == currentNamespace {
			return personalMemoryScope{ProjectNamespaceID: currentNamespace, OriginalRepository: repositoryID, Scope: MemoryScopeProject}
		}
		// Model-authored project IDs are semantic labels, not trusted physical
		// repository namespaces. Keep an unmapped historical project isolated
		// until Home explicitly links or moves it; never inject it into whichever
		// repository happened to perform the import.
		historicalRepository := "repository_historical_" + shortHistoricalDigest(scope.ProjectID)
		return personalMemoryScope{ProjectNamespaceID: stableProjectNamespace(historicalRepository), OriginalRepository: historicalRepository, Scope: MemoryScopeProject}
	default:
		repository := "repository_unassigned_" + shortHistoricalDigest(jobID)
		return personalMemoryScope{ProjectNamespaceID: stableProjectNamespace(repository), OriginalRepository: repository, Scope: MemoryScopeProject}
	}
}

func historicalDedupKey(scope personalMemoryScope, kind, digest string) string {
	namespace := scope.ProjectNamespaceID
	if scope.Scope == MemoryScopePersonalGlobal {
		namespace = ""
	}
	return strings.Join([]string{scope.Scope, namespace, kind, digest}, "\x1f")
}

func historicalObjectID(jobID, candidateID string) string {
	digest := sha256.Sum256([]byte("pulse:historical-object:v1\x1f" + jobID + "\x1f" + candidateID))
	return "history_" + hex.EncodeToString(digest[:16])
}

func historicalTrayCandidateID(jobID, candidateID string) string {
	digest := sha256.Sum256([]byte("pulse:historical-tray-candidate:v1\x1f" + jobID + "\x1f" + candidateID))
	return "history_candidate_" + hex.EncodeToString(digest[:16])
}

func shortHistoricalDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:12])
}

func (s *Store) historicalDestinationGeneration(db interface{ QueryRow(string, ...any) *sql.Row }) (int64, error) {
	var generation int64
	err := db.QueryRow(`SELECT eligibility_revision FROM personal_memory_scope_state WHERE singleton=1`).Scan(&generation)
	return generation, err
}

func (s *Store) existingHistoricalTarget(scope personalMemoryScope, kind, digest string) (historicalingest.WriteTarget, bool, error) {
	var objectID, objectDigest string
	var generation int64
	err := s.db.QueryRow(`
		SELECT object_id, content_digest, logical_generation
		  FROM private_memory_objects
		 WHERE memory_scope=? AND project_namespace_id=? AND candidate_kind=? AND content_digest=? AND lifecycle='active'`,
		scope.Scope, scope.ProjectNamespaceID, kind, digest,
	).Scan(&objectID, &objectDigest, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return historicalingest.WriteTarget{}, false, nil
	}
	if err != nil {
		return historicalingest.WriteTarget{}, false, err
	}
	return historicalingest.WriteTarget{Outcome: historicalingest.ItemDeduplicated, ObjectKind: kind, ObjectID: objectID, ObjectDigest: objectDigest, LogicalGeneration: generation}, true, nil
}

func (s *Store) persistHistoricalWriteSet(source historicalingest.ApplySource, set historicalingest.WriteSet, encoded []byte, digest string) error {
	manifestJSON, err := historicalingest.EncodeManifest(source.Manifest)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		INSERT OR IGNORE INTO historical_ingest_jobs(
		  job_id, store_id, state, root_limit, cutoff_at, source_snapshot_digest,
		  parser_version, scrubber_version, prompt_version, schema_digest, model_id, model_effort,
		  current_revision, current_manifest_digest, created_at, updated_at)
		VALUES (?, ?, 'approval_ready', 50, ?, ?, ?, 'historical_scrubber_v1', ?, ?, ?, ?, ?, ?, ?, ?)`,
		set.JobID, s.storeID, source.Snapshot.Cutoff.UTC().Format(time.RFC3339Nano), source.Snapshot.Digest,
		set.ParserVersion, set.PromptVersion, set.SchemaDigest, set.ModelID, set.ModelEffort,
		set.Revision, set.ManifestDigest, now, now)
	if err != nil {
		return err
	}
	for _, prefix := range source.Snapshot.Files {
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO historical_ingest_source_prefixes(
			  job_id, source_alias, root_id, captured_bytes, prefix_digest, parser_version,
			  record_count, included_count, excluded_count, blocking_count, captured_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, set.JobID, prefix.Alias, prefix.RootID,
			prefix.CapturedBytes, prefix.PrefixDigest, prefix.ParserVersion, prefix.RecordCount,
			prefix.IncludedCount, prefix.ExcludedCount, prefix.BlockingCount, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO historical_ingest_manifests(
		  job_id, revision, manifest_digest, source_snapshot_digest, schema_digest,
		  state, item_count, manifest_json, created_at)
		VALUES (?, ?, ?, ?, ?, 'approval_ready', ?, ?, ?)`, set.JobID, set.Revision, set.ManifestDigest,
		set.SourceSnapshotDigest, set.SchemaDigest, len(source.Manifest.Items), manifestJSON, now); err != nil {
		return err
	}
	for _, item := range source.Manifest.Items {
		itemJSON, _ := json.Marshal(item)
		itemSum := sha256.Sum256(itemJSON)
		disposition := "confirmed"
		if source.Dispositions[item.CandidateID] == historicalingest.ReviewExcluded {
			disposition = "excluded"
		}
		var projectID any
		if item.Scope.Kind == historicalingest.ScopeProject {
			projectID = item.Scope.ProjectID
		}
		var validTo any
		if item.ValidTime.To != nil {
			validTo = item.ValidTime.To.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO historical_ingest_manifest_items(
			  job_id, revision, candidate_id, material_kind, scope_kind, project_id,
			  epistemic_status, derivation, valid_from, valid_to, confidence, privacy,
			  item_digest, item_json, disposition)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'private', ?, ?, ?)`,
			set.JobID, set.Revision, item.CandidateID, item.Kind, item.Scope.Kind, projectID,
			item.EpistemicStatus, item.Derivation, item.ValidTime.From.UTC().Format(time.RFC3339Nano), validTo,
			item.Confidence, hex.EncodeToString(itemSum[:]), itemJSON, disposition); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO historical_ingest_write_sets(
		  job_id, revision, manifest_digest, write_set_digest, destination_store_id,
		  destination_generation, materializer_version, dedup_version, target_versions_digest,
		  write_set_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, set.JobID, set.Revision, set.ManifestDigest, digest,
		set.DestinationStoreID, set.DestinationGeneration, set.MaterializerVersion, set.DedupVersion,
		set.TargetVersionsDigest, encoded, now); err != nil {
		return err
	}
	var stored []byte
	var storedDigest string
	if err := tx.QueryRow(`SELECT write_set_json, write_set_digest FROM historical_ingest_write_sets WHERE job_id=? AND revision=? AND write_set_digest=?`, set.JobID, set.Revision, digest).Scan(&stored, &storedDigest); err != nil {
		return err
	}
	if storedDigest != digest || string(stored) != string(encoded) {
		return historicalingest.ErrApplyVersionConflict
	}
	return tx.Commit()
}

func (s *Store) LoadHistoricalWriteSet(jobID string, revision int64, manifestDigest, writeSetDigest string) (historicalingest.WriteSet, string, error) {
	var encoded []byte
	var storedDigest string
	err := s.db.QueryRow(`
		SELECT write_set_json, write_set_digest FROM historical_ingest_write_sets
		 WHERE job_id=? AND revision=? AND manifest_digest=? AND write_set_digest=?`, jobID, revision, manifestDigest, writeSetDigest,
	).Scan(&encoded, &storedDigest)
	if err != nil {
		return historicalingest.WriteSet{}, "", err
	}
	set, digest, err := historicalingest.DecodeWriteSet(encoded)
	if err != nil || digest != storedDigest {
		return historicalingest.WriteSet{}, "", historicalingest.ErrApplyVersionConflict
	}
	return set, digest, nil
}

func (s *Store) HistoricalDestinationGeneration() (int64, error) {
	return s.historicalDestinationGeneration(s.db)
}

func (s *Store) AuthorizeHistoricalApply(jobID, writeSetDigest string, destinationGeneration int64, now time.Time) (HistoricalApplyCapability, error) {
	var storedDigest string
	if err := s.db.QueryRow(`SELECT write_set_digest FROM historical_ingest_write_sets WHERE job_id=? AND write_set_digest=? AND destination_generation=?`, jobID, writeSetDigest, destinationGeneration).Scan(&storedDigest); err != nil {
		return HistoricalApplyCapability{}, historicalingest.ErrApplyAuthorization
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return HistoricalApplyCapability{}, err
	}
	token := hex.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(token))
	authorizationID, err := newOpaqueID("history_auth")
	if err != nil {
		return HistoricalApplyCapability{}, err
	}
	auditID, err := newOpaqueID("history_audit")
	if err != nil {
		return HistoricalApplyCapability{}, err
	}
	expires := now.UTC().Add(historicalingest.ApplyAuthorizationTTL)
	_, err = s.db.Exec(`
		INSERT INTO historical_ingest_authorizations(
		  authorization_id, audit_id, job_id, authorization_kind, bound_digest,
		  capability_hash, destination_generation, expires_at, created_at)
		VALUES (?, ?, ?, 'canonical_apply', ?, ?, ?, ?, ?)`, authorizationID, auditID, jobID,
		storedDigest, hex.EncodeToString(tokenHash[:]), destinationGeneration,
		expires.Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return HistoricalApplyCapability{}, err
	}
	return HistoricalApplyCapability{AuthorizationID: authorizationID, AuditID: auditID, Token: token, ExpiresAt: expires}, nil
}

func (s *Store) CreateHistoricalBackup(jobID, writeSetDigest string) (HistoricalBackupReceipt, error) {
	if !filepath.IsAbs(s.path) || jobID == "" || len(writeSetDigest) != 64 {
		return HistoricalBackupReceipt{}, historicalingest.ErrApplyDestination
	}
	backupDir := filepath.Join(filepath.Dir(s.path), "historical-backups")
	if err := platform.EnsurePrivateDirectory(backupDir); err != nil {
		return HistoricalBackupReceipt{}, err
	}
	backupID := "backup_" + shortHistoricalDigest(jobID+"\x1f"+writeSetDigest)
	path := filepath.Join(backupDir, backupID+".sqlite3")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
			return HistoricalBackupReceipt{}, err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return HistoricalBackupReceipt{}, err
		}
	} else if err != nil {
		return HistoricalBackupReceipt{}, err
	}
	receipt, err := verifyHistoricalBackup(path, backupID, writeSetDigest)
	if err != nil {
		return HistoricalBackupReceipt{}, err
	}
	return receipt, nil
}

func verifyHistoricalBackup(path, backupID, writeSetDigest string) (HistoricalBackupReceipt, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=foreign_keys(ON)", path))
	if err != nil {
		return HistoricalBackupReceipt{}, err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return HistoricalBackupReceipt{}, errors.New("historical backup integrity check failed")
	}
	var foreignKeyViolations int
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return HistoricalBackupReceipt{}, err
	}
	for rows.Next() {
		foreignKeyViolations++
	}
	if err := rows.Close(); err != nil || foreignKeyViolations != 0 {
		return HistoricalBackupReceipt{}, errors.New("historical backup foreign key check failed")
	}
	return HistoricalBackupReceipt{BackupID: backupID, WriteSetDigest: writeSetDigest, IntegrityOK: true, ForeignKeysOK: true, path: path}, nil
}

// RestoreHistoricalBackupTo proves a backup can materialize as a standalone
// disposable store. It never replaces an existing database; restoring the
// active vault remains an offline, consent-gated operator action.
func RestoreHistoricalBackupTo(receipt HistoricalBackupReceipt, destination string) (HistoricalBackupReceipt, error) {
	if !receipt.IntegrityOK || !receipt.ForeignKeysOK || !filepath.IsAbs(receipt.path) ||
		!filepath.IsAbs(destination) || receipt.path == destination {
		return HistoricalBackupReceipt{}, historicalingest.ErrApplyDestination
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return HistoricalBackupReceipt{}, historicalingest.ErrApplyDestination
	}
	if err := platform.EnsurePrivateDirectory(filepath.Dir(destination)); err != nil {
		return HistoricalBackupReceipt{}, err
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=foreign_keys(ON)", receipt.path))
	if err != nil {
		return HistoricalBackupReceipt{}, err
	}
	defer db.Close()
	if _, err := db.Exec(`VACUUM INTO ?`, destination); err != nil {
		_ = os.Remove(destination)
		return HistoricalBackupReceipt{}, err
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		_ = os.Remove(destination)
		return HistoricalBackupReceipt{}, err
	}
	restored, err := verifyHistoricalBackup(destination, receipt.BackupID, receipt.WriteSetDigest)
	if err != nil {
		_ = os.Remove(destination)
		return HistoricalBackupReceipt{}, err
	}
	return restored, nil
}

func (s *Store) ApplyHistoricalWriteSet(capability HistoricalApplyCapability, now time.Time) (historicalingest.BatchReceipt, error) {
	set, writeSetDigest, err := s.loadHistoricalWriteSetByAuthorization(capability.AuthorizationID)
	if err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	if receipt, found, err := s.loadHistoricalBatchReceipt(set.JobID, set.ManifestDigest, writeSetDigest); err != nil {
		return historicalingest.BatchReceipt{}, err
	} else if found {
		return receipt, nil
	}
	db, err := openHistoricalApplyDB(s.path)
	if err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	defer db.Close()
	if err := historicalingest.ValidateApplyRuntime(db); err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	defer tx.Rollback()
	if replay, found, err := loadHistoricalBatchReceiptTx(tx, set.JobID, set.ManifestDigest, writeSetDigest); err != nil {
		return historicalingest.BatchReceipt{}, err
	} else if found {
		return replay, nil
	}
	if err := validateHistoricalCapabilityTx(tx, capability, set, writeSetDigest, now); err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	generation, err := s.historicalDestinationGeneration(tx)
	if err != nil || generation != set.DestinationGeneration {
		return historicalingest.BatchReceipt{}, historicalingest.ErrApplyDestination
	}
	currentBinding, currentRepository, ok := s.ProductRuntimeBoundary()
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	if !ok || currentBinding != set.DestinationBindingDigest || currentBinding != expectedBinding ||
		currentRepository != set.RepositoryID || expectedPolicy != set.PolicyEpoch || expectedResolver != set.ResolverEpoch || s.storeID != set.DestinationStoreID {
		return historicalingest.BatchReceipt{}, historicalingest.ErrApplyDestination
	}
	receiptID := "history_receipt_" + shortHistoricalDigest(writeSetDigest)
	createdAt := now.UTC().Format(time.RFC3339Nano)
	createdCount, deduplicatedCount := 0, 0
	ledgerID := "history_ledger_" + shortHistoricalDigest(writeSetDigest)
	if err := insertHistoricalLedgerTx(tx, s, set, writeSetDigest, ledgerID, createdAt); err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	outcomes := make([]historicalingest.ItemOutcome, 0, len(set.Items))
	for _, item := range set.Items {
		candidate, scope, err := historicalCandidateFromWriteItem(item, set.JobID, set.RepositoryID)
		if err != nil {
			return historicalingest.BatchReceipt{}, err
		}
		prepared, err := preparePrivateCandidate(candidate)
		if err != nil || prepared.digest != item.ContentDigest || item.Target.ObjectDigest != prepared.digest {
			return historicalingest.BatchReceipt{}, historicalingest.ErrApplyVersionConflict
		}
		objectID, outcome, err := applyHistoricalItemTx(tx, s, set, item, candidate, prepared, scope, ledgerID, createdAt)
		if err != nil {
			return historicalingest.BatchReceipt{}, err
		}
		if outcome == historicalingest.ItemCreated {
			createdCount++
		} else {
			deduplicatedCount++
		}
		outcomes = append(outcomes, historicalingest.ItemOutcome{CandidateID: item.CandidateID, Outcome: outcome, ObjectKind: PrivateMemoryCandidateCapsule, ObjectID: objectID, ObjectDigest: item.ContentDigest})
	}
	if createdCount+deduplicatedCount != len(set.Items) {
		return historicalingest.BatchReceipt{}, historicalingest.ErrApplyVersionConflict
	}
	if _, err := tx.Exec(`
		INSERT INTO historical_ingest_batch_receipts(
		  receipt_id, job_id, manifest_digest, write_set_digest, destination_store_id,
		  destination_generation, created_count, deduplicated_count, committed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, receiptID, set.JobID, set.ManifestDigest, writeSetDigest,
		set.DestinationStoreID, set.DestinationGeneration, createdCount, deduplicatedCount, createdAt); err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	for _, outcome := range outcomes {
		if _, err := tx.Exec(`
			INSERT INTO historical_ingest_item_receipts(
			  receipt_id, candidate_id, outcome, object_kind, object_id, object_digest)
			VALUES (?, ?, ?, ?, ?, ?)`, receiptID, outcome.CandidateID, outcome.Outcome,
			outcome.ObjectKind, outcome.ObjectID, outcome.ObjectDigest); err != nil {
			return historicalingest.BatchReceipt{}, err
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO historical_ingest_projection_outbox(
		  receipt_id, projection_kind, generation, state, created_at, updated_at)
		VALUES (?, 'retrieval', 1, 'pending', ?, ?)`, receiptID, createdAt, createdAt); err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	capabilityHash := sha256.Sum256([]byte(capability.Token))
	updated, err := tx.Exec(`
		UPDATE historical_ingest_authorizations SET consumed_at=?
		 WHERE authorization_id=? AND capability_hash=? AND consumed_at IS NULL`, createdAt,
		capability.AuthorizationID, hex.EncodeToString(capabilityHash[:]))
	if err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	if affected, _ := updated.RowsAffected(); affected != 1 {
		return historicalingest.BatchReceipt{}, historicalingest.ErrApplyAuthorization
	}
	if _, err := tx.Exec(`UPDATE historical_ingest_jobs SET state='committed_indexing', updated_at=? WHERE job_id=?`, createdAt, set.JobID); err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	if err := advancePersonalEligibilityTx(tx, now); err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return historicalingest.BatchReceipt{}, err
	}
	return historicalingest.BatchReceipt{ReceiptID: receiptID, ManifestDigest: set.ManifestDigest, WriteSetDigest: writeSetDigest, DestinationStoreID: set.DestinationStoreID, DestinationGeneration: set.DestinationGeneration, CreatedCount: createdCount, DeduplicatedCount: deduplicatedCount, Outcomes: outcomes, CommittedAt: now.UTC()}, nil
}

func openHistoricalApplyDB(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(FULL)&_txlock=immediate", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *Store) loadHistoricalWriteSetByAuthorization(authorizationID string) (historicalingest.WriteSet, string, error) {
	var encoded []byte
	var storedDigest string
	err := s.db.QueryRow(`
		SELECT writes.write_set_json, writes.write_set_digest
		  FROM historical_ingest_authorizations auth
		  JOIN historical_ingest_write_sets writes
		    ON writes.job_id=auth.job_id AND writes.write_set_digest=auth.bound_digest
		 WHERE auth.authorization_id=? AND auth.authorization_kind='canonical_apply'`, authorizationID,
	).Scan(&encoded, &storedDigest)
	if err != nil {
		return historicalingest.WriteSet{}, "", historicalingest.ErrApplyAuthorization
	}
	set, digest, err := historicalingest.DecodeWriteSet(encoded)
	if err != nil || digest != storedDigest {
		return historicalingest.WriteSet{}, "", historicalingest.ErrApplyVersionConflict
	}
	return set, digest, nil
}

func validateHistoricalCapabilityTx(tx *sql.Tx, capability HistoricalApplyCapability, set historicalingest.WriteSet, writeSetDigest string, now time.Time) error {
	var jobID, boundDigest, capabilityHash, expiresAt string
	var generation int64
	var consumed sql.NullString
	err := tx.QueryRow(`
		SELECT job_id, bound_digest, capability_hash, destination_generation, expires_at, consumed_at
		  FROM historical_ingest_authorizations
		 WHERE authorization_id=? AND audit_id=? AND authorization_kind='canonical_apply'`, capability.AuthorizationID, capability.AuditID,
	).Scan(&jobID, &boundDigest, &capabilityHash, &generation, &expiresAt, &consumed)
	if err != nil {
		return historicalingest.ErrApplyAuthorization
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	tokenHash := sha256.Sum256([]byte(capability.Token))
	if err != nil || consumed.Valid || jobID != set.JobID || boundDigest != writeSetDigest ||
		capabilityHash != hex.EncodeToString(tokenHash[:]) || generation != set.DestinationGeneration || !now.UTC().Before(expires) {
		return historicalingest.ErrApplyAuthorization
	}
	return nil
}

func insertHistoricalLedgerTx(tx *sql.Tx, s *Store, set historicalingest.WriteSet, writeSetDigest, ledgerID, createdAt string) error {
	destination := string(capture.DestinationPersonal)
	if s.storeKind == StoreKindDesk {
		destination = string(capture.DestinationDesk)
	}
	_, err := tx.Exec(`
		INSERT INTO turn_ledgers(
		  ledger_id, finalize_receipt_id, host, session_id, turn_id, source_event_key,
		  idempotency_key, binding_digest, destination_store_id, destination_class,
		  policy_epoch, resolver_epoch, request_digest, state, created_at, finalized_at)
		VALUES (?, ?, 'pulse-cli', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'candidates', ?, ?)`,
		ledgerID, "history_finalize_"+shortHistoricalDigest(writeSetDigest),
		"history_session_"+shortHistoricalDigest(set.JobID), "history_turn_"+shortHistoricalDigest(writeSetDigest),
		"history_event_"+shortHistoricalDigest(writeSetDigest), "history_idempotency_"+shortHistoricalDigest(writeSetDigest),
		set.DestinationBindingDigest, set.DestinationStoreID, destination, set.PolicyEpoch, set.ResolverEpoch,
		writeSetDigest, createdAt, createdAt)
	return err
}

func historicalCandidateFromWriteItem(item historicalingest.CanonicalWriteItem, jobID, repositoryID string) (PrivateMemoryCandidate, personalMemoryScope, error) {
	material := historicalingest.MaterialItem{
		CandidateID: item.CandidateID, Kind: item.MaterialKind, Confidence: item.Confidence,
		Privacy: historicalingest.PrivacyPrivate, EpistemicStatus: item.EpistemicStatus,
		Derivation: item.Derivation, ValidTime: item.ValidTime, Scope: item.Scope, SourceRefs: item.SourceRefs,
	}
	capsule := MemoryCapsule{
		Schema: MemoryCapsuleSchema,
		Source: CapsuleSource{Host: "codex", ConversationScope: "project_context", Timestamp: item.ValidTime.From.UTC().Format(time.RFC3339)},
		Items: []MemoryCapsuleItem{{Kind: item.CapsuleKind, RedactedSummary: item.Summary, Confidence: item.Confidence,
			EvidenceHint: "user_confirmed", PrivacyTier: "private", Retention: "long_term",
			Tags: []string{"historical", "material:" + string(item.MaterialKind), "epistemic:" + string(item.EpistemicStatus)}}},
		RawInputIncluded: false,
	}
	return PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &capsule}, historicalPersonalScope(material.Scope, jobID, repositoryID), nil
}

func applyHistoricalItemTx(tx *sql.Tx, s *Store, set historicalingest.WriteSet, item historicalingest.CanonicalWriteItem, candidate PrivateMemoryCandidate, prepared preparedPrivateCandidate, scope personalMemoryScope, ledgerID, createdAt string) (string, historicalingest.ItemOutcomeKind, error) {
	objectID := item.Target.ObjectID
	trayCandidateID := historicalTrayCandidateID(set.JobID, item.CandidateID)
	var currentDigest, currentScope, currentNamespace string
	var currentGeneration int64
	err := tx.QueryRow(`
		SELECT content_digest, memory_scope, project_namespace_id, logical_generation
		  FROM private_memory_objects WHERE object_id=? AND lifecycle='active'`, objectID,
	).Scan(&currentDigest, &currentScope, &currentNamespace, &currentGeneration)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	actualOutcome := item.Target.Outcome
	if actualOutcome == historicalingest.ItemCreated {
		if found {
			return "", "", historicalingest.ErrApplyVersionConflict
		}
		var competing string
		err := tx.QueryRow(`
			SELECT object_id FROM private_memory_objects
			 WHERE memory_scope=? AND project_namespace_id=? AND candidate_kind=? AND content_digest=? AND lifecycle='active'`,
			scope.Scope, scope.ProjectNamespaceID, prepared.kind, prepared.digest).Scan(&competing)
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			return "", "", historicalingest.ErrApplyVersionConflict
		}
		ids, err := rememberPrivateCapsuleTx(tx, *candidate.Capsule)
		if err != nil || len(ids) != 1 {
			return "", "", fmt.Errorf("historical capsule create: %w", err)
		}
		if _, err := tx.Exec(`UPDATE memory_capsules SET id=? WHERE id=?`, objectID, ids[0]); err != nil {
			return "", "", err
		}
	} else if !found || currentDigest != item.ContentDigest || currentScope != scope.Scope || currentNamespace != scope.ProjectNamespaceID || currentGeneration != item.Target.LogicalGeneration {
		return "", "", historicalingest.ErrApplyVersionConflict
	}
	payload, _ := json.Marshal(candidate)
	if _, err := tx.Exec(`
		INSERT INTO memory_tray_candidates(
		  candidate_id, ledger_id, candidate_kind, operation, version, content_digest,
		  payload_json, state, grace_expires_at, canonical_object_id, created_at, updated_at, terminal_at)
		VALUES (?, ?, 'memory_capsule', 'create', 1, ?, ?, 'committed', ?, ?, ?, ?, ?)`,
		trayCandidateID, ledgerID, item.ContentDigest, payload, createdAt, objectID, createdAt, createdAt, createdAt); err != nil {
		return "", "", err
	}
	if actualOutcome == historicalingest.ItemCreated {
		if _, err := tx.Exec(`
			INSERT INTO private_memory_objects(
			  object_id, candidate_kind, content_digest, created_from_candidate_id, created_at,
			  logical_memory_id, logical_generation, project_namespace_id, original_repository_id,
			  memory_scope, modified_at, capture_host, capture_session_ref, captured_at)
			VALUES (?, 'memory_capsule', ?, ?, ?, ?, 1, ?, ?, ?, ?, 'pulse-cli', ?, ?)`,
			objectID, item.ContentDigest, trayCandidateID, createdAt, objectID, scope.ProjectNamespaceID,
			scope.OriginalRepository, scope.Scope, createdAt, "history_"+shortHistoricalDigest(set.JobID), createdAt); err != nil {
			return "", "", err
		}
	}
	destination := capture.DestinationPersonal
	if s.storeKind == StoreKindDesk {
		destination = capture.DestinationDesk
	}
	status := MemoryWriteCreated
	if actualOutcome == historicalingest.ItemDeduplicated {
		status = MemoryWriteDeduplicated
	}
	receipt, err := insertWriteReceiptTx(tx, MemoryWriteReceipt{
		LedgerID: ledgerID, CandidateID: trayCandidateID, CandidateVersion: 1,
		Status: status, Destination: destination, ContentDigest: item.ContentDigest, ObjectID: objectID,
		PolicyEpoch: set.PolicyEpoch, ResolverEpoch: set.ResolverEpoch,
		MeasurementMethod: "historical_manifest_v1", CreatedAt: createdAt,
	})
	if err != nil {
		return "", "", err
	}
	if err := insertWriteAuditTx(tx, receipt, "historical_apply", status); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(`
		INSERT INTO memory_write_idempotency(operation, idempotency_key, request_digest, receipt_id, object_id, created_at)
		VALUES ('historical_apply', ?, ?, ?, ?, ?)`, trayCandidateID, item.ContentDigest, receipt.ReceiptID, objectID, createdAt); err != nil {
		return "", "", err
	}
	var projectID any
	if item.Scope.Kind == historicalingest.ScopeProject {
		projectID = item.Scope.ProjectID
	}
	var validTo any
	if item.ValidTime.To != nil {
		validTo = item.ValidTime.To.UTC().Format(time.RFC3339Nano)
	}
	metadataPayload, _ := json.Marshal(struct {
		Scope      historicalingest.Scope           `json:"scope"`
		Status     historicalingest.EpistemicStatus `json:"epistemic_status"`
		Derivation historicalingest.Derivation      `json:"derivation"`
		Valid      historicalingest.ValidTime       `json:"valid_time"`
	}{item.Scope, item.EpistemicStatus, item.Derivation, item.ValidTime})
	metadataSum := sha256.Sum256(metadataPayload)
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO historical_ingest_object_metadata(
		  store_id, object_kind, object_id, scope_kind, project_id, epistemic_status,
		  valid_from, valid_to, manifest_digest, candidate_id, metadata_digest)
		VALUES (?, 'memory_capsule', ?, ?, ?, ?, ?, ?, ?, ?, ?)`, set.DestinationStoreID, objectID,
		item.Scope.Kind, projectID, item.EpistemicStatus, item.ValidTime.From.UTC().Format(time.RFC3339Nano),
		validTo, set.ManifestDigest, item.CandidateID, hex.EncodeToString(metadataSum[:])); err != nil {
		return "", "", err
	}
	var sourceOrdinal int
	_ = tx.QueryRow(`SELECT COALESCE(MAX(ordinal)+1, 0) FROM historical_ingest_source_refs WHERE store_id=? AND object_kind='memory_capsule' AND object_id=?`, set.DestinationStoreID, objectID).Scan(&sourceOrdinal)
	for _, ref := range item.SourceRefs {
		if sourceOrdinal > 255 {
			return "", "", historicalingest.ErrApplyWriteSetInvalid
		}
		if _, err := tx.Exec(`
			INSERT INTO historical_ingest_source_refs(
			  store_id, object_kind, object_id, ordinal, source_alias, prefix_digest, record_locator)
			VALUES (?, 'memory_capsule', ?, ?, ?, ?, ?)`, set.DestinationStoreID, objectID,
			sourceOrdinal, ref.Alias, ref.PrefixDigest, ref.RecordLocator); err != nil {
			return "", "", err
		}
		sourceOrdinal++
	}
	projectionID := "history_projection_" + shortHistoricalDigest(objectID)
	if _, err := tx.Exec(`
		INSERT INTO private_projection_outbox(
		  projection_id, object_id, candidate_kind, status, attempt_count, created_at, updated_at)
		VALUES (?, ?, 'memory_capsule', 'pending', 0, ?, ?)
		ON CONFLICT(object_id, candidate_kind) DO UPDATE SET status='pending', attempt_count=0, updated_at=excluded.updated_at`,
		projectionID, objectID, createdAt, createdAt); err != nil {
		return "", "", err
	}
	return objectID, actualOutcome, nil
}

func (s *Store) loadHistoricalBatchReceipt(jobID, manifestDigest, writeSetDigest string) (historicalingest.BatchReceipt, bool, error) {
	return loadHistoricalBatchReceiptDB(s.db, jobID, manifestDigest, writeSetDigest)
}

func (s *Store) HistoricalBatchReceipt(jobID, manifestDigest, writeSetDigest string) (historicalingest.BatchReceipt, bool, error) {
	return s.loadHistoricalBatchReceipt(jobID, manifestDigest, writeSetDigest)
}

func loadHistoricalBatchReceiptTx(tx *sql.Tx, jobID, manifestDigest, writeSetDigest string) (historicalingest.BatchReceipt, bool, error) {
	return loadHistoricalBatchReceiptDB(tx, jobID, manifestDigest, writeSetDigest)
}

type historicalReceiptDB interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

func loadHistoricalBatchReceiptDB(db historicalReceiptDB, jobID, manifestDigest, writeSetDigest string) (historicalingest.BatchReceipt, bool, error) {
	var receipt historicalingest.BatchReceipt
	var committedAt string
	err := db.QueryRow(`
		SELECT receipt_id, manifest_digest, write_set_digest, destination_store_id,
		       destination_generation, created_count, deduplicated_count, committed_at
		  FROM historical_ingest_batch_receipts
		 WHERE job_id=? AND manifest_digest=? AND write_set_digest=?`, jobID, manifestDigest, writeSetDigest,
	).Scan(&receipt.ReceiptID, &receipt.ManifestDigest, &receipt.WriteSetDigest, &receipt.DestinationStoreID,
		&receipt.DestinationGeneration, &receipt.CreatedCount, &receipt.DeduplicatedCount, &committedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return historicalingest.BatchReceipt{}, false, nil
	}
	if err != nil {
		return historicalingest.BatchReceipt{}, false, err
	}
	receipt.CommittedAt, err = time.Parse(time.RFC3339Nano, committedAt)
	if err != nil {
		return historicalingest.BatchReceipt{}, false, err
	}
	rows, err := db.Query(`
		SELECT candidate_id, outcome, object_kind, object_id, object_digest
		  FROM historical_ingest_item_receipts WHERE receipt_id=? ORDER BY candidate_id`, receipt.ReceiptID)
	if err != nil {
		return historicalingest.BatchReceipt{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var outcome historicalingest.ItemOutcome
		if err := rows.Scan(&outcome.CandidateID, &outcome.Outcome, &outcome.ObjectKind, &outcome.ObjectID, &outcome.ObjectDigest); err != nil {
			return historicalingest.BatchReceipt{}, false, err
		}
		receipt.Outcomes = append(receipt.Outcomes, outcome)
	}
	return receipt, true, rows.Err()
}

func (s *Store) HistoricalProjectionState(receiptID string) (historicalingest.ProjectionState, error) {
	var total, complete, failed int
	err := s.db.QueryRow(`
		SELECT count(*),
		       sum(CASE WHEN projection.status='complete' THEN 1 ELSE 0 END),
		       sum(CASE WHEN projection.status='failed' THEN 1 ELSE 0 END)
		  FROM historical_ingest_item_receipts item
		  JOIN private_projection_outbox projection ON projection.object_id=item.object_id
		 WHERE item.receipt_id=?`, receiptID).Scan(&total, &complete, &failed)
	if err != nil || total == 0 {
		return historicalingest.ProjectionPending, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if failed > 0 {
		_, _ = s.db.Exec(`UPDATE historical_ingest_projection_outbox SET state='failed', updated_at=? WHERE receipt_id=?`, now, receiptID)
		return historicalingest.ProjectionFailed, nil
	}
	if complete == total {
		_, _ = s.db.Exec(`UPDATE historical_ingest_projection_outbox SET state='ready', updated_at=? WHERE receipt_id=?`, now, receiptID)
		return historicalingest.ProjectionReady, nil
	}
	return historicalingest.ProjectionPending, nil
}
