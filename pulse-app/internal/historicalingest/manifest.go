package historicalingest

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const SchemaVersionV1 = "https://zbs.gg/schemas/pulse/historical-ingest/v1"

//go:embed schema/historical_ingest_v1.schema.json
var schemaFS embed.FS

var (
	jobIDPattern       = regexp.MustCompile(`^job_[a-f0-9]{16,64}$`)
	candidateIDPattern = regexp.MustCompile(`^candidate_[a-f0-9]{16,64}$`)
	projectIDPattern   = regexp.MustCompile(`^project_[A-Za-z0-9._:-]{1,247}$`)
	sourceAliasPattern = regexp.MustCompile(`^source_[a-f0-9]{16,64}$`)
	opaqueRefPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	hexDigestPattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type MaterialKind string

const (
	MaterialKindEvent      MaterialKind = "event"
	MaterialKindDecision   MaterialKind = "decision"
	MaterialKindAssertion  MaterialKind = "assertion"
	MaterialKindPerson     MaterialKind = "person"
	MaterialKindProject    MaterialKind = "project"
	MaterialKindRelation   MaterialKind = "relation"
	MaterialKindState      MaterialKind = "state"
	MaterialKindContinuity MaterialKind = "continuity"
)

type ScopeKind string

const (
	ScopeProject    ScopeKind = "project"
	ScopeGlobal     ScopeKind = "global"
	ScopeUnassigned ScopeKind = "unassigned"
)

type Privacy string

const PrivacyPrivate Privacy = "private"

type EpistemicStatus string

const (
	EpistemicExplicit   EpistemicStatus = "explicit"
	EpistemicHypothesis EpistemicStatus = "hypothesis"
	EpistemicConflict   EpistemicStatus = "conflict"
)

type Derivation string

const (
	DerivationDirect   Derivation = "direct"
	DerivationInferred Derivation = "inferred"
)

type JobState string

const (
	JobPreflight         JobState = "preflight"
	JobSnapshotting      JobState = "snapshotting"
	JobAwaitingEgress    JobState = "awaiting_egress_consent"
	JobExtracting        JobState = "extracting"
	JobPausedQuota       JobState = "paused_quota"
	JobExtractionFailed  JobState = "extraction_failed"
	JobManifestReady     JobState = "manifest_ready"
	JobNothingToImport   JobState = "nothing_to_import"
	JobApprovalReady     JobState = "approval_ready"
	JobApproved          JobState = "approved"
	JobStale             JobState = "stale"
	JobApplying          JobState = "applying"
	JobCommittedIndexing JobState = "committed_indexing"
	JobIndexingFailed    JobState = "indexing_failed"
	JobRetrievalReady    JobState = "retrieval_ready"
	JobCanceled          JobState = "canceled"
)

type ProjectionState string

const (
	ProjectionPending ProjectionState = "pending"
	ProjectionReady   ProjectionState = "ready"
	ProjectionFailed  ProjectionState = "failed"
)

type Scope struct {
	Kind      ScopeKind `json:"kind"`
	ProjectID string    `json:"project_id,omitempty"`
}

func (scope Scope) Validate() error {
	switch scope.Kind {
	case ScopeProject:
		if !projectIDPattern.MatchString(scope.ProjectID) {
			return errors.New("project scope requires a stable project_id")
		}
	case ScopeGlobal, ScopeUnassigned:
		if scope.ProjectID != "" {
			return errors.New("non-project scope cannot carry project_id")
		}
	default:
		return fmt.Errorf("unsupported scope %q", scope.Kind)
	}
	return nil
}

type ValidTime struct {
	From time.Time  `json:"from"`
	To   *time.Time `json:"to,omitempty"`
}

func (value ValidTime) Validate() error {
	if value.From.IsZero() {
		return errors.New("valid time requires from")
	}
	if value.To != nil && !value.To.After(value.From) {
		return errors.New("valid time to must be after from")
	}
	return nil
}

type SourceRef struct {
	Alias         string `json:"alias"`
	PrefixDigest  string `json:"prefix_digest"`
	RecordLocator string `json:"record_locator"`
}

func (ref SourceRef) Validate() error {
	if !sourceAliasPattern.MatchString(ref.Alias) || !hexDigestPattern.MatchString(ref.PrefixDigest) ||
		!opaqueRefPattern.MatchString(ref.RecordLocator) {
		return errors.New("source reference is invalid")
	}
	return nil
}

// MaterialPayload is deliberately closed and content-oriented. Source paths,
// transcript bodies, credentials, and runtime authority do not have fields in
// this contract.
type MaterialPayload struct {
	Title            string   `json:"title,omitempty"`
	Summary          string   `json:"summary,omitempty"`
	SubjectID        string   `json:"subject_id,omitempty"`
	Predicate        string   `json:"predicate,omitempty"`
	ObjectValue      string   `json:"object_value,omitempty"`
	ObjectID         string   `json:"object_id,omitempty"`
	EntityType       string   `json:"entity_type,omitempty"`
	Name             string   `json:"name,omitempty"`
	StateKind        string   `json:"state_kind,omitempty"`
	Intensity        *float64 `json:"intensity,omitempty"`
	ContinuityStatus string   `json:"continuity_status,omitempty"`
}

func (payload MaterialPayload) Validate(kind MaterialKind) error {
	if !validMaterialText(payload.Title, 4000) || !validMaterialText(payload.Summary, 4000) ||
		!validMaterialText(payload.ObjectValue, 4000) || !validMaterialText(payload.Name, 512) ||
		!validMaterialText(payload.Predicate, 128) || !validMaterialText(payload.EntityType, 64) ||
		!validMaterialText(payload.StateKind, 128) || !validMaterialText(payload.ContinuityStatus, 32) ||
		(payload.SubjectID != "" && !opaqueRefPattern.MatchString(payload.SubjectID)) ||
		(payload.ObjectID != "" && !opaqueRefPattern.MatchString(payload.ObjectID)) {
		return errors.New("material payload text is invalid")
	}
	if payload.Intensity != nil && (*payload.Intensity < 0 || *payload.Intensity > 1) {
		return errors.New("state intensity must be between zero and one")
	}
	switch kind {
	case MaterialKindEvent:
		if payload.Title == "" || payload.Summary == "" {
			return errors.New("event requires title and summary")
		}
	case MaterialKindDecision:
		if payload.Summary == "" {
			return errors.New("decision requires summary")
		}
	case MaterialKindAssertion:
		if payload.SubjectID == "" || payload.Predicate == "" || payload.ObjectValue == "" {
			return errors.New("assertion requires subject_id, predicate, and object_value")
		}
	case MaterialKindPerson:
		if payload.EntityType != "person" || payload.Name == "" {
			return errors.New("person requires person entity_type and name")
		}
	case MaterialKindProject:
		if payload.EntityType != "project" || payload.Name == "" {
			return errors.New("project requires project entity_type and name")
		}
	case MaterialKindRelation:
		if payload.SubjectID == "" || payload.Predicate == "" || payload.ObjectID == "" {
			return errors.New("relation requires subject_id, predicate, and object_id")
		}
	case MaterialKindState:
		if payload.StateKind == "" || payload.Summary == "" {
			return errors.New("state requires state_kind and summary")
		}
	case MaterialKindContinuity:
		if payload.Summary == "" || (payload.ContinuityStatus != "open" && payload.ContinuityStatus != "closed" && payload.ContinuityStatus != "historical") {
			return errors.New("continuity requires summary and a supported status")
		}
	default:
		return fmt.Errorf("unsupported material kind %q", kind)
	}
	return nil
}

func validMaterialText(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) || !norm.NFC.IsNormalString(value) {
		return false
	}
	for _, char := range value {
		if unicode.Is(unicode.Cc, char) || unicode.Is(unicode.Cf, char) {
			return false
		}
	}
	return true
}

type MaterialItem struct {
	CandidateID     string          `json:"candidate_id"`
	Kind            MaterialKind    `json:"kind"`
	Confidence      float64         `json:"confidence"`
	Privacy         Privacy         `json:"privacy"`
	EpistemicStatus EpistemicStatus `json:"epistemic_status"`
	Derivation      Derivation      `json:"derivation"`
	ValidTime       ValidTime       `json:"valid_time"`
	Scope           Scope           `json:"scope"`
	SourceRefs      []SourceRef     `json:"source_refs"`
	Payload         MaterialPayload `json:"payload"`
}

func (item MaterialItem) Validate() error {
	if !candidateIDPattern.MatchString(item.CandidateID) {
		return errors.New("candidate_id is invalid")
	}
	if item.Confidence < 0 || item.Confidence > 1 {
		return errors.New("confidence must be between zero and one")
	}
	if item.Privacy != PrivacyPrivate {
		return errors.New("historical Personal material must remain private")
	}
	if item.EpistemicStatus != EpistemicExplicit && item.EpistemicStatus != EpistemicHypothesis && item.EpistemicStatus != EpistemicConflict {
		return errors.New("epistemic status is invalid")
	}
	if item.Derivation != DerivationDirect && item.Derivation != DerivationInferred {
		return errors.New("derivation is invalid")
	}
	if item.Derivation == DerivationInferred && item.EpistemicStatus == EpistemicExplicit {
		return errors.New("inferred material cannot be explicit")
	}
	if err := item.ValidTime.Validate(); err != nil {
		return err
	}
	if err := item.Scope.Validate(); err != nil {
		return err
	}
	if len(item.SourceRefs) == 0 {
		return errors.New("material requires provenance")
	}
	for _, ref := range item.SourceRefs {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	return item.Payload.Validate(item.Kind)
}

type Manifest struct {
	SchemaVersion        string         `json:"schema_version"`
	JobID                string         `json:"job_id"`
	Revision             int64          `json:"revision"`
	SourceSnapshotDigest string         `json:"source_snapshot_digest"`
	Items                []MaterialItem `json:"items"`
}

func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != SchemaVersionV1 || !jobIDPattern.MatchString(manifest.JobID) ||
		manifest.Revision < 1 || !hexDigestPattern.MatchString(manifest.SourceSnapshotDigest) {
		return errors.New("manifest identity is invalid")
	}
	seen := make(map[string]struct{}, len(manifest.Items))
	for index, item := range manifest.Items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("item %d: %w", index, err)
		}
		if _, ok := seen[item.CandidateID]; ok {
			return fmt.Errorf("item %d: duplicate candidate_id", index)
		}
		seen[item.CandidateID] = struct{}{}
	}
	return nil
}

func EncodeManifest(manifest Manifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(manifest)
}

func DecodeManifest(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode historical manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, errors.New("decode historical manifest: trailing value")
		}
		return Manifest{}, fmt.Errorf("decode historical manifest trailing value: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func SchemaBytes() []byte {
	body, err := schemaFS.ReadFile("schema/historical_ingest_v1.schema.json")
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), body...)
}

func SchemaDigest() string {
	digest := sha256.Sum256(SchemaBytes())
	return hex.EncodeToString(digest[:])
}

type SourceFilePrefix struct {
	Alias         string `json:"alias"`
	CapturedBytes int64  `json:"captured_bytes"`
	PrefixDigest  string `json:"prefix_digest"`
	RootID        string `json:"root_id"`
	ParserVersion string `json:"parser_version"`
	RecordCount   int64  `json:"record_count"`
	IncludedCount int64  `json:"included_count"`
	ExcludedCount int64  `json:"excluded_count"`
	BlockingCount int64  `json:"blocking_count"`
}

type SourceSnapshot struct {
	Digest        string             `json:"digest"`
	Cutoff        time.Time          `json:"cutoff"`
	RootCount     int                `json:"root_count"`
	Files         []SourceFilePrefix `json:"files"`
	ParserVersion string             `json:"parser_version"`
}

type WorkUnit struct {
	ID             string   `json:"id"`
	RootID         string   `json:"root_id"`
	SnapshotDigest string   `json:"snapshot_digest"`
	EvidenceDigest string   `json:"evidence_digest"`
	SourceAliases  []string `json:"source_aliases"`
	Ordinal        int      `json:"ordinal"`
}

type WorkUnitResult struct {
	SchemaVersion  string         `json:"schema_version"`
	WorkUnitID     string         `json:"work_unit_id"`
	EvidenceDigest string         `json:"evidence_digest"`
	Items          []MaterialItem `json:"items"`
	ZeroMaterial   bool           `json:"zero_material"`
}

type ManifestRevision struct {
	JobID          string    `json:"job_id"`
	Revision       int64     `json:"revision"`
	ManifestDigest string    `json:"manifest_digest"`
	WriteSetDigest string    `json:"write_set_digest,omitempty"`
	ReviewComplete bool      `json:"review_complete"`
	CreatedAt      time.Time `json:"created_at"`
}

type ApplyAuthorization struct {
	AuditID               string    `json:"audit_id"`
	ManifestDigest        string    `json:"manifest_digest"`
	WriteSetDigest        string    `json:"write_set_digest"`
	DestinationStoreID    string    `json:"destination_store_id"`
	DestinationGeneration int64     `json:"destination_generation"`
	ExpiresAt             time.Time `json:"expires_at"`
}

type ItemOutcomeKind string

const (
	ItemCreated      ItemOutcomeKind = "created"
	ItemDeduplicated ItemOutcomeKind = "deduplicated"
)

type ItemOutcome struct {
	CandidateID  string          `json:"candidate_id"`
	Outcome      ItemOutcomeKind `json:"outcome"`
	ObjectID     string          `json:"object_id"`
	ObjectDigest string          `json:"object_digest"`
}

type BatchReceipt struct {
	ReceiptID      string        `json:"receipt_id"`
	ManifestDigest string        `json:"manifest_digest"`
	WriteSetDigest string        `json:"write_set_digest"`
	Outcomes       []ItemOutcome `json:"outcomes"`
	CommittedAt    time.Time     `json:"committed_at"`
}

// SupportedSQLiteVersion implements the fixed-version floor from the SQLite
// security guidance used by the historical apply path.
func SupportedSQLiteVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	version := [3]int{}
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return false
		}
		version[index] = parsed
	}
	if version == [3]int{3, 44, 6} || version == [3]int{3, 50, 7} {
		return true
	}
	minimum := [3]int{3, 51, 3}
	for index := range version {
		if version[index] > minimum[index] {
			return true
		}
		if version[index] < minimum[index] {
			return false
		}
	}
	return true
}

func ValidateApplyRuntime(db *sql.DB) error {
	var version, journal string
	var foreignKeys, synchronous, busyTimeout int
	if err := db.QueryRow(`SELECT sqlite_version()`).Scan(&version); err != nil {
		return fmt.Errorf("read sqlite version: %w", err)
	}
	if !SupportedSQLiteVersion(version) {
		return fmt.Errorf("sqlite version %s is not supported for historical apply", version)
	}
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign_keys: %w", err)
	}
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		return fmt.Errorf("read synchronous: %w", err)
	}
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		return fmt.Errorf("read busy_timeout: %w", err)
	}
	if strings.ToLower(journal) != "wal" || foreignKeys != 1 || synchronous != 2 || busyTimeout < 1000 {
		return fmt.Errorf("historical apply runtime mismatch: journal_mode=%s foreign_keys=%d synchronous=%d busy_timeout=%d", journal, foreignKeys, synchronous, busyTimeout)
	}
	return nil
}
