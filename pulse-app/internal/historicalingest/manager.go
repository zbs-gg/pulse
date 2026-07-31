package historicalingest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/nkkmnk/pulse/internal/platform"
)

var (
	ErrIngestJobNotFound           = errors.New("historical ingest job not found")
	ErrNoWorkAvailable             = errors.New("no historical ingest work available")
	ErrJobNotExtracting            = errors.New("historical ingest job is not extracting")
	ErrLeaseConflict               = errors.New("historical ingest lease conflict")
	ErrResultConflict              = errors.New("historical ingest result conflict")
	ErrIncompleteCohort            = errors.New("historical ingest cohort incomplete")
	ErrEgressAuthorizationConflict = errors.New("historical ingest egress authorization conflict")
	leaseIDPattern                 = regexp.MustCompile(`^lease_[a-z0-9]{16,64}$`)
	unitIDPattern                  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
)

type RunnerContract struct {
	Digest        string `json:"digest"`
	SchemaDigest  string `json:"schema_digest"`
	ModelID       string `json:"model_id"`
	ModelEffort   string `json:"model_effort"`
	ParserVersion string `json:"parser_version"`
	PromptVersion string `json:"prompt_version"`
}

func (contract RunnerContract) Validate() error {
	if !hexDigestPattern.MatchString(contract.Digest) || !hexDigestPattern.MatchString(contract.SchemaDigest) ||
		contract.ModelID != "gpt-5.6-luna" || contract.ModelEffort != "low" ||
		contract.ParserVersion == "" || len(contract.ParserVersion) > 128 || contract.PromptVersion == "" || len(contract.PromptVersion) > 128 {
		return errors.New("historical ingest runner contract invalid")
	}
	return nil
}

type TokenUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens"`
}

func (usage TokenUsage) Validate() error {
	if usage.InputTokens < 0 || usage.CachedInputTokens < 0 || usage.OutputTokens < 0 || usage.ReasoningTokens < 0 || usage.CachedInputTokens > usage.InputTokens {
		return errors.New("historical ingest usage invalid")
	}
	return nil
}

type IngestManagerConfig struct {
	RootDir         string
	Key             []byte
	Clock           func() time.Time
	NewLeaseID      func() string
	LeaseTTL        time.Duration
	VerifySnapshot  func(SourceSnapshot) error
	CheckpointWrite func(string, []byte) error
}

type IngestManager struct {
	mu              sync.Mutex
	rootDir         string
	key             []byte
	clock           func() time.Time
	newLeaseID      func() string
	leaseTTL        time.Duration
	verifySnapshot  func(SourceSnapshot) error
	checkpointWrite func(string, []byte) error
	jobs            map[string]ingestCheckpoint
	nextGeneration  uint64
}

type WorkLease struct {
	JobID                string    `json:"job_id"`
	Unit                 WorkUnit  `json:"unit"`
	Token                string    `json:"lease_token"`
	ExpiresAt            time.Time `json:"expires_at"`
	CheckpointGeneration uint64    `json:"checkpoint_generation"`
}

type UnitReceipt struct {
	JobID                string `json:"job_id"`
	UnitID               string `json:"unit_id"`
	ResultDigest         string `json:"result_digest"`
	CheckpointGeneration uint64 `json:"checkpoint_generation"`
}

type JobStatus struct {
	Schema                string     `json:"schema"`
	JobID                 string     `json:"job_id"`
	State                 JobState   `json:"state"`
	Generation            uint64     `json:"generation"`
	TotalUnits            int        `json:"total_units"`
	AcceptedUnits         int        `json:"accepted_units"`
	PendingUnits          int        `json:"pending_units"`
	LeasedUnits           int        `json:"leased_units"`
	ManifestRevision      int64      `json:"manifest_revision,omitempty"`
	ManifestDigest        string     `json:"manifest_digest,omitempty"`
	Usage                 TokenUsage `json:"usage"`
	ReasonCode            string     `json:"reason_code,omitempty"`
	SnapshotDigest        string     `json:"snapshot_digest"`
	RunnerContract        string     `json:"runner_contract_digest"`
	SourceRootCount       int        `json:"source_root_count"`
	SourceFileCount       int        `json:"source_file_count"`
	SourceBytes           int64      `json:"source_bytes"`
	EvidenceBytes         int64      `json:"evidence_bytes"`
	EgressAuthorized      bool       `json:"egress_authorized"`
	WriteSetDigest        string     `json:"write_set_digest,omitempty"`
	DestinationStoreID    string     `json:"destination_store_id,omitempty"`
	DestinationGeneration int64      `json:"destination_generation,omitempty"`
	BatchReceiptID        string     `json:"batch_receipt_id,omitempty"`
}

func NewIngestManager(cfg IngestManagerConfig) (*IngestManager, error) {
	if !filepath.IsAbs(cfg.RootDir) || len(cfg.Key) < 32 {
		return nil, errors.New("historical ingest requires absolute private root and 32-byte key")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.NewLeaseID == nil {
		cfg.NewLeaseID = randomLeaseID
	}
	if cfg.LeaseTTL == 0 {
		cfg.LeaseTTL = 5 * time.Minute
	}
	if cfg.LeaseTTL < time.Second || cfg.LeaseTTL > 30*time.Minute {
		return nil, errors.New("historical ingest lease ttl invalid")
	}
	if cfg.CheckpointWrite == nil {
		cfg.CheckpointWrite = func(path string, payload []byte) error {
			_, err := platform.CreatePrivateFileExclusive(path, payload)
			return err
		}
	}
	if err := platform.EnsurePrivateDirectory(cfg.RootDir); err != nil {
		return nil, err
	}
	manager := &IngestManager{
		rootDir: cfg.RootDir, key: append([]byte(nil), cfg.Key...), clock: cfg.Clock,
		newLeaseID: cfg.NewLeaseID, leaseTTL: cfg.LeaseTTL, verifySnapshot: cfg.VerifySnapshot,
		checkpointWrite: cfg.CheckpointWrite,
		jobs:            map[string]ingestCheckpoint{}, nextGeneration: 1,
	}
	if err := manager.loadCheckpoints(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *IngestManager) StartJob(jobID string, snapshot SourceSnapshot, units []WorkUnit, contract RunnerContract) (JobStatus, error) {
	return m.startJob(jobID, snapshot, units, contract, JobExtracting)
}

func (m *IngestManager) StartJobAwaitingEgress(jobID string, snapshot SourceSnapshot, units []WorkUnit, contract RunnerContract) (JobStatus, error) {
	return m.startJob(jobID, snapshot, units, contract, JobAwaitingEgress)
}

func (m *IngestManager) startJob(jobID string, snapshot SourceSnapshot, units []WorkUnit, contract RunnerContract, initialState JobState) (JobStatus, error) {
	if !jobIDPattern.MatchString(jobID) || validateSourceSnapshot(snapshot) != nil || contract.Validate() != nil || len(units) == 0 {
		return JobStatus{}, errors.New("historical ingest start contract invalid")
	}
	if initialState != JobExtracting && initialState != JobAwaitingEgress {
		return JobStatus{}, errors.New("historical ingest start state invalid")
	}
	ordered := append([]WorkUnit(nil), units...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].RootID != ordered[j].RootID {
			return ordered[i].RootID < ordered[j].RootID
		}
		if ordered[i].Ordinal != ordered[j].Ordinal {
			return ordered[i].Ordinal < ordered[j].Ordinal
		}
		return ordered[i].ID < ordered[j].ID
	})
	seen := map[string]struct{}{}
	snapshotRoots := map[string]struct{}{}
	aliasRoots := map[string]string{}
	for _, source := range snapshot.Files {
		snapshotRoots[source.RootID] = struct{}{}
		aliasRoots[source.Alias] = source.RootID
	}
	unitRoots := map[string]struct{}{}
	checkpoints := make([]unitCheckpoint, len(ordered))
	for index, unit := range ordered {
		if validateWorkUnit(unit, snapshot.Digest) != nil {
			return JobStatus{}, errors.New("historical ingest work unit invalid")
		}
		if _, exists := seen[unit.ID]; exists {
			return JobStatus{}, errors.New("historical ingest duplicate work unit")
		}
		if _, exists := snapshotRoots[unit.RootID]; !exists {
			return JobStatus{}, errors.New("historical ingest work unit root missing from snapshot")
		}
		for _, alias := range unit.SourceAliases {
			if aliasRoots[alias] != unit.RootID {
				return JobStatus{}, errors.New("historical ingest work unit source does not belong to root")
			}
		}
		seen[unit.ID] = struct{}{}
		unitRoots[unit.RootID] = struct{}{}
		checkpoints[index] = unitCheckpoint{Unit: unit, State: unitPending}
	}
	if len(unitRoots) != len(snapshotRoots) {
		return JobStatus{}, errors.New("historical ingest root coverage incomplete")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.jobs[jobID]; ok {
		if current.Snapshot.Digest != snapshot.Digest || current.Contract.Digest != contract.Digest {
			return JobStatus{}, ErrResultConflict
		}
		return statusFor(current), nil
	}
	now := m.clock().UTC()
	checkpoint := ingestCheckpoint{Schema: ingestCheckpointSchema, JobID: jobID, State: initialState, Snapshot: snapshot, Contract: contract, Units: checkpoints, CreatedAt: now, UpdatedAt: now}
	if initialState == JobAwaitingEgress {
		checkpoint.ReasonCode = "provider_consent_required"
	}
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) AuthorizeEgress(jobID, snapshotDigest, runnerContractDigest string) (JobStatus, error) {
	if !hexDigestPattern.MatchString(snapshotDigest) || !hexDigestPattern.MatchString(runnerContractDigest) {
		return JobStatus{}, ErrEgressAuthorizationConflict
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	if checkpoint.Snapshot.Digest != snapshotDigest || checkpoint.Contract.Digest != runnerContractDigest {
		return JobStatus{}, ErrEgressAuthorizationConflict
	}
	if checkpoint.State == JobExtracting && checkpoint.EgressAuthorizedAt != nil {
		return statusFor(checkpoint), nil
	}
	if checkpoint.State != JobAwaitingEgress {
		return JobStatus{}, ErrEgressAuthorizationConflict
	}
	now := m.clock().UTC()
	checkpoint.State, checkpoint.ReasonCode, checkpoint.EgressAuthorizedAt = JobExtracting, "", &now
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) LeaseNext(jobID string) (WorkLease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return WorkLease{}, ErrIngestJobNotFound
	}
	if checkpoint.State != JobExtracting {
		return WorkLease{}, ErrJobNotExtracting
	}
	now := m.clock().UTC()
	for index := range checkpoint.Units {
		unit := &checkpoint.Units[index]
		if unit.State == unitLeased && unit.LeaseExpiresAt != nil && !unit.LeaseExpiresAt.After(now) {
			unit.State, unit.LeaseHash, unit.LeaseExpiresAt = unitPending, "", nil
		}
	}
	for index := range checkpoint.Units {
		unit := &checkpoint.Units[index]
		if unit.State != unitPending {
			continue
		}
		token := m.newLeaseID()
		if !leaseIDPattern.MatchString(token) {
			return WorkLease{}, errors.New("historical ingest generated invalid lease")
		}
		expires := now.Add(m.leaseTTL)
		unit.State, unit.LeaseHash, unit.LeaseExpiresAt = unitLeased, digestString(token), &expires
		if err := m.commitLocked(&checkpoint); err != nil {
			return WorkLease{}, err
		}
		return WorkLease{JobID: jobID, Unit: unit.Unit, Token: token, ExpiresAt: expires, CheckpointGeneration: checkpoint.Generation}, nil
	}
	return WorkLease{}, ErrNoWorkAvailable
}

func (m *IngestManager) SubmitResult(jobID, unitID, leaseToken string, result WorkUnitResult, usage TokenUsage, contractDigest string) (UnitReceipt, error) {
	if usage.Validate() != nil || !hexDigestPattern.MatchString(contractDigest) || validateWorkUnitResult(result) != nil {
		return UnitReceipt{}, errors.New("historical ingest submitted result invalid")
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxAcceptedResult {
		return UnitReceipt{}, errors.New("historical ingest result too large")
	}
	resultDigest := digestBytes(encoded)
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return UnitReceipt{}, ErrIngestJobNotFound
	}
	if checkpoint.Contract.Digest != contractDigest {
		return UnitReceipt{}, ErrResultConflict
	}
	index := unitIndex(checkpoint.Units, unitID)
	if index < 0 {
		return UnitReceipt{}, ErrNoWorkAvailable
	}
	unit := &checkpoint.Units[index]
	if result.WorkUnitID != unit.Unit.ID || result.EvidenceDigest != unit.Unit.EvidenceDigest {
		return UnitReceipt{}, ErrResultConflict
	}
	if validateResultProvenance(result, unit.Unit, checkpoint.Snapshot) != nil || unsafeEvidencePattern.Match(encoded) {
		return UnitReceipt{}, ErrResultConflict
	}
	if unit.State == unitAccepted {
		if unit.ResultDigest != resultDigest {
			return UnitReceipt{}, ErrResultConflict
		}
		return UnitReceipt{JobID: jobID, UnitID: unitID, ResultDigest: resultDigest, CheckpointGeneration: unit.AcceptedGeneration}, nil
	}
	if checkpoint.State != JobExtracting || unit.State != unitLeased || unit.LeaseHash != digestString(leaseToken) || unit.LeaseExpiresAt == nil || !unit.LeaseExpiresAt.After(m.clock().UTC()) {
		return UnitReceipt{}, ErrLeaseConflict
	}
	resultPath := filepath.Join(m.rootDir, "result-"+resultDigest+".json")
	if _, err := platform.CreatePrivateFileExclusive(resultPath, encoded); errors.Is(err, os.ErrExist) {
		existing, readErr := platform.ReadPrivateFile(resultPath, privateIngestPolicy(maxAcceptedResult))
		if readErr != nil || string(existing) != string(encoded) {
			return UnitReceipt{}, ErrResultConflict
		}
	} else if err != nil {
		return UnitReceipt{}, err
	}
	unit.State, unit.LeaseHash, unit.LeaseExpiresAt = unitAccepted, "", nil
	unit.ResultDigest, unit.Usage = resultDigest, usage
	unit.AcceptedGeneration = m.nextGeneration
	if err := m.commitLocked(&checkpoint); err != nil {
		return UnitReceipt{}, err
	}
	return UnitReceipt{JobID: jobID, UnitID: unitID, ResultDigest: resultDigest, CheckpointGeneration: unit.AcceptedGeneration}, nil
}

func (m *IngestManager) PauseQuota(jobID, unitID, leaseToken string) (JobStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	index := unitIndex(checkpoint.Units, unitID)
	if checkpoint.State != JobExtracting || index < 0 || checkpoint.Units[index].State != unitLeased || checkpoint.Units[index].LeaseHash != digestString(leaseToken) {
		return JobStatus{}, ErrLeaseConflict
	}
	checkpoint.Units[index].State, checkpoint.Units[index].LeaseHash, checkpoint.Units[index].LeaseExpiresAt = unitPending, "", nil
	checkpoint.State, checkpoint.ReasonCode = JobPausedQuota, "subscription_quota"
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) ResumeJob(jobID string) (JobStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	if checkpoint.State != JobPausedQuota && checkpoint.State != JobExtractionFailed {
		return JobStatus{}, ErrJobNotExtracting
	}
	checkpoint.State, checkpoint.ReasonCode = JobExtracting, ""
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) FailLease(jobID, unitID, leaseToken, reason string) (JobStatus, error) {
	if !unitIDPattern.MatchString(reason) {
		return JobStatus{}, errors.New("historical ingest failure reason invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	index := unitIndex(checkpoint.Units, unitID)
	if checkpoint.State != JobExtracting || index < 0 || checkpoint.Units[index].State != unitLeased || checkpoint.Units[index].LeaseHash != digestString(leaseToken) {
		return JobStatus{}, ErrLeaseConflict
	}
	checkpoint.Units[index].State, checkpoint.Units[index].LeaseHash, checkpoint.Units[index].LeaseExpiresAt = unitPending, "", nil
	checkpoint.State, checkpoint.ReasonCode = JobExtractionFailed, reason
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) CancelJob(jobID string) (JobStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	if checkpoint.State == JobCanceled {
		return statusFor(checkpoint), nil
	}
	if checkpoint.State == JobRetrievalReady || checkpoint.State == JobApplying || checkpoint.State == JobCommittedIndexing {
		return JobStatus{}, ErrJobNotExtracting
	}
	for index := range checkpoint.Units {
		if checkpoint.Units[index].State == unitLeased {
			checkpoint.Units[index].State, checkpoint.Units[index].LeaseHash, checkpoint.Units[index].LeaseExpiresAt = unitPending, "", nil
		}
	}
	checkpoint.State, checkpoint.ReasonCode, checkpoint.ReviewComplete = JobCanceled, "user_canceled", false
	checkpoint.WriteSetDigest, checkpoint.DestinationStoreID, checkpoint.DestinationGeneration, checkpoint.BatchReceiptID = "", "", 0, ""
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) Status(jobID string) (JobStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) LatestStatus() (JobStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var selected *ingestCheckpoint
	for _, checkpoint := range m.jobs {
		if selected == nil || checkpoint.UpdatedAt.After(selected.UpdatedAt) ||
			(checkpoint.UpdatedAt.Equal(selected.UpdatedAt) && checkpoint.Generation > selected.Generation) {
			copy := checkpoint
			selected = &copy
		}
	}
	if selected == nil {
		return JobStatus{}, ErrIngestJobNotFound
	}
	return statusFor(*selected), nil
}

func (m *IngestManager) MarkStale(jobID, snapshotDigest, reason string) (JobStatus, error) {
	if !hexDigestPattern.MatchString(snapshotDigest) || !unitIDPattern.MatchString(reason) {
		return JobStatus{}, ErrReviewInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	if checkpoint.Snapshot.Digest != snapshotDigest {
		return JobStatus{}, ErrReviewVersionConflict
	}
	checkpoint.State, checkpoint.ReasonCode, checkpoint.ReviewComplete = JobStale, reason, false
	checkpoint.WriteSetDigest, checkpoint.DestinationStoreID, checkpoint.DestinationGeneration, checkpoint.BatchReceiptID = "", "", 0, ""
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) BuildManifest(jobID string) (Manifest, string, TokenUsage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return Manifest{}, "", TokenUsage{}, ErrIngestJobNotFound
	}
	if checkpoint.ManifestDigest != "" {
		manifest, err := m.readManifestLocked(checkpoint.ManifestDigest)
		return manifest, checkpoint.ManifestDigest, aggregateUsage(checkpoint.Units), err
	}
	for _, unit := range checkpoint.Units {
		if unit.State != unitAccepted {
			return Manifest{}, "", TokenUsage{}, ErrIncompleteCohort
		}
	}
	if m.verifySnapshot != nil {
		if err := m.verifySnapshot(checkpoint.Snapshot); err != nil {
			checkpoint.State = JobStale
			checkpoint.ReasonCode = "source_prefix_changed"
			_ = m.commitLocked(&checkpoint)
			return Manifest{}, "", TokenUsage{}, err
		}
	}
	results := make([]WorkUnitResult, 0, len(checkpoint.Units))
	for _, unit := range checkpoint.Units {
		encoded, err := platform.ReadPrivateFile(filepath.Join(m.rootDir, "result-"+unit.ResultDigest+".json"), privateIngestPolicy(maxAcceptedResult))
		if err != nil || digestBytes(encoded) != unit.ResultDigest {
			return Manifest{}, "", TokenUsage{}, ErrIngestCheckpointIntegrity
		}
		var result WorkUnitResult
		decoderErr := json.Unmarshal(encoded, &result)
		if decoderErr != nil || validateWorkUnitResult(result) != nil {
			return Manifest{}, "", TokenUsage{}, ErrIngestCheckpointIntegrity
		}
		results = append(results, result)
	}
	manifest, err := MergeAcceptedResults(jobID, checkpoint.Snapshot.Digest, results)
	if err != nil {
		return Manifest{}, "", TokenUsage{}, err
	}
	encoded, err := EncodeManifest(manifest)
	if err != nil {
		return Manifest{}, "", TokenUsage{}, err
	}
	digest := digestBytes(encoded)
	manifestPath := filepath.Join(m.rootDir, "manifest-"+digest+".json")
	if _, err := platform.CreatePrivateFileExclusive(manifestPath, encoded); errors.Is(err, os.ErrExist) {
		existing, readErr := platform.ReadPrivateFile(manifestPath, privateIngestPolicy(maxIngestManifest))
		if readErr != nil || string(existing) != string(encoded) {
			return Manifest{}, "", TokenUsage{}, ErrIngestCheckpointIntegrity
		}
	} else if err != nil {
		return Manifest{}, "", TokenUsage{}, err
	}
	checkpoint.ManifestRevision, checkpoint.ManifestDigest = 1, digest
	if len(manifest.Items) == 0 {
		checkpoint.State = JobNothingToImport
	} else {
		checkpoint.State = JobManifestReady
	}
	if err := m.commitLocked(&checkpoint); err != nil {
		return Manifest{}, "", TokenUsage{}, err
	}
	return manifest, digest, aggregateUsage(checkpoint.Units), nil
}

func (m *IngestManager) readManifestLocked(digest string) (Manifest, error) {
	encoded, err := platform.ReadPrivateFile(filepath.Join(m.rootDir, "manifest-"+digest+".json"), privateIngestPolicy(maxIngestManifest))
	if err != nil || digestBytes(encoded) != digest {
		return Manifest{}, ErrIngestCheckpointIntegrity
	}
	return DecodeManifest(encoded)
}

func validateSourceSnapshot(snapshot SourceSnapshot) error {
	if !hexDigestPattern.MatchString(snapshot.Digest) || snapshot.Cutoff.IsZero() || snapshot.RootCount < 1 || snapshot.ParserVersion == "" || len(snapshot.Files) == 0 {
		return errors.New("snapshot invalid")
	}
	aliases := map[string]struct{}{}
	roots := map[string]struct{}{}
	for _, file := range snapshot.Files {
		if !sourceAliasPattern.MatchString(file.Alias) || file.CapturedBytes < 0 || !hexDigestPattern.MatchString(file.PrefixDigest) || !opaqueRefPattern.MatchString(file.RootID) || file.ParserVersion == "" ||
			file.RecordCount < 0 || file.IncludedCount < 0 || file.ExcludedCount < 0 || file.BlockingCount < 0 || file.RecordCount != file.IncludedCount+file.ExcludedCount+file.BlockingCount {
			return errors.New("snapshot source invalid")
		}
		if _, exists := aliases[file.Alias]; exists {
			return errors.New("snapshot source duplicate")
		}
		aliases[file.Alias] = struct{}{}
		roots[file.RootID] = struct{}{}
	}
	if len(roots) != snapshot.RootCount {
		return errors.New("snapshot root count invalid")
	}
	return nil
}

func validateWorkUnit(unit WorkUnit, snapshotDigest string) error {
	if !unitIDPattern.MatchString(unit.ID) || !opaqueRefPattern.MatchString(unit.RootID) || unit.Ordinal < 0 || unit.SnapshotDigest != snapshotDigest || !hexDigestPattern.MatchString(unit.EvidenceDigest) || unit.EvidenceBytes <= 0 || len(unit.SourceAliases) == 0 {
		return errors.New("work unit invalid")
	}
	for _, alias := range unit.SourceAliases {
		if !sourceAliasPattern.MatchString(alias) {
			return errors.New("work unit alias invalid")
		}
	}
	return nil
}

func validateWorkUnitResult(result WorkUnitResult) error {
	if result.SchemaVersion != SchemaVersionV1 || !unitIDPattern.MatchString(result.WorkUnitID) || !hexDigestPattern.MatchString(result.EvidenceDigest) || (len(result.Items) == 0) != result.ZeroMaterial {
		return errors.New("work unit result invalid")
	}
	seen := map[string]struct{}{}
	for _, item := range result.Items {
		if err := item.Validate(); err != nil {
			return err
		}
		if _, ok := seen[item.CandidateID]; ok {
			return errors.New("duplicate candidate")
		}
		seen[item.CandidateID] = struct{}{}
	}
	return nil
}

func validateResultProvenance(result WorkUnitResult, unit WorkUnit, snapshot SourceSnapshot) error {
	allowed := map[string]string{}
	unitAliases := map[string]struct{}{}
	for _, alias := range unit.SourceAliases {
		unitAliases[alias] = struct{}{}
	}
	for _, source := range snapshot.Files {
		if _, ok := unitAliases[source.Alias]; ok && source.RootID == unit.RootID {
			allowed[source.Alias] = source.PrefixDigest
		}
	}
	if len(allowed) != len(unitAliases) {
		return errors.New("historical ingest unit provenance incomplete")
	}
	for _, item := range result.Items {
		for _, ref := range item.SourceRefs {
			if allowed[ref.Alias] != ref.PrefixDigest {
				return errors.New("historical ingest result provenance mismatch")
			}
		}
	}
	return nil
}

func statusFor(checkpoint ingestCheckpoint) JobStatus {
	status := JobStatus{Schema: "pulse.historical_ingest.status.v1", JobID: checkpoint.JobID, State: checkpoint.State, Generation: checkpoint.Generation, TotalUnits: len(checkpoint.Units), ManifestRevision: checkpoint.ManifestRevision, ManifestDigest: checkpoint.ManifestDigest, ReasonCode: checkpoint.ReasonCode, SnapshotDigest: checkpoint.Snapshot.Digest, RunnerContract: checkpoint.Contract.Digest, SourceRootCount: checkpoint.Snapshot.RootCount, SourceFileCount: len(checkpoint.Snapshot.Files), EgressAuthorized: checkpoint.EgressAuthorizedAt != nil, WriteSetDigest: checkpoint.WriteSetDigest, DestinationStoreID: checkpoint.DestinationStoreID, DestinationGeneration: checkpoint.DestinationGeneration, BatchReceiptID: checkpoint.BatchReceiptID}
	for _, source := range checkpoint.Snapshot.Files {
		status.SourceBytes += source.CapturedBytes
	}
	for _, unit := range checkpoint.Units {
		status.EvidenceBytes += unit.Unit.EvidenceBytes
		switch unit.State {
		case unitAccepted:
			status.AcceptedUnits++
			status.Usage = addUsage(status.Usage, unit.Usage)
		case unitLeased:
			status.LeasedUnits++
		case unitPending:
			status.PendingUnits++
		}
	}
	return status
}

func aggregateUsage(units []unitCheckpoint) TokenUsage {
	var total TokenUsage
	for _, unit := range units {
		if unit.State == unitAccepted {
			total = addUsage(total, unit.Usage)
		}
	}
	return total
}
func addUsage(a, b TokenUsage) TokenUsage {
	return TokenUsage{a.InputTokens + b.InputTokens, a.CachedInputTokens + b.CachedInputTokens, a.OutputTokens + b.OutputTokens, a.ReasoningTokens + b.ReasoningTokens}
}
func unitIndex(units []unitCheckpoint, id string) int {
	for index := range units {
		if units[index].Unit.ID == id {
			return index
		}
	}
	return -1
}
func digestBytes(value []byte) string  { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func digestString(value string) string { return digestBytes([]byte(value)) }
func randomLeaseID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("historical ingest random unavailable")
	}
	return "lease_" + hex.EncodeToString(value[:])
}
func validManagerState(state JobState) bool {
	switch state {
	case JobAwaitingEgress, JobExtracting, JobPausedQuota, JobExtractionFailed, JobManifestReady, JobNothingToImport, JobApprovalReady, JobApproved, JobApplying, JobCommittedIndexing, JobIndexingFailed, JobRetrievalReady, JobStale, JobCanceled:
		return true
	default:
		return false
	}
}
