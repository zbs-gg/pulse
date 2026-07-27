package historicalingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

const (
	WriteSetSchemaV1        = "pulse.historical_ingest.write_set.v1"
	MaterializerVersionV1   = "historical_capsule_v1"
	DedupVersionV1          = "historical_exact_scope_digest_v1"
	ApplyAuthorizationTTL   = 10 * time.Minute
	ProjectionKindRetrieval = "retrieval"
)

var (
	ErrApplyNotReady        = errors.New("historical ingest apply is not ready")
	ErrApplyVersionConflict = errors.New("historical ingest apply version conflict")
	ErrApplyAuthorization   = errors.New("historical ingest apply authorization invalid")
	ErrApplyDestination     = errors.New("historical ingest apply destination changed")
	ErrApplyWriteSetInvalid = errors.New("historical ingest write set invalid")
)

type ApplySource struct {
	Manifest       Manifest
	ManifestDigest string
	Snapshot       SourceSnapshot
	Contract       RunnerContract
	Dispositions   map[string]ReviewDisposition
}

type WriteTarget struct {
	Outcome           ItemOutcomeKind `json:"outcome"`
	ObjectKind        string          `json:"object_kind"`
	ObjectID          string          `json:"object_id"`
	ObjectDigest      string          `json:"object_digest"`
	LogicalGeneration int64           `json:"logical_generation"`
}

type CanonicalWriteItem struct {
	CandidateID     string          `json:"candidate_id"`
	MaterialKind    MaterialKind    `json:"material_kind"`
	CapsuleKind     string          `json:"capsule_kind"`
	Summary         string          `json:"summary"`
	Confidence      float64         `json:"confidence"`
	Scope           Scope           `json:"scope"`
	EpistemicStatus EpistemicStatus `json:"epistemic_status"`
	Derivation      Derivation      `json:"derivation"`
	ValidTime       ValidTime       `json:"valid_time"`
	SourceRefs      []SourceRef     `json:"source_refs"`
	ContentDigest   string          `json:"content_digest"`
	Target          WriteTarget     `json:"target"`
}

type WriteSet struct {
	Schema                   string               `json:"schema"`
	JobID                    string               `json:"job_id"`
	Revision                 int64                `json:"revision"`
	ManifestDigest           string               `json:"manifest_digest"`
	SourceSnapshotDigest     string               `json:"source_snapshot_digest"`
	SchemaDigest             string               `json:"schema_digest"`
	RunnerContractDigest     string               `json:"runner_contract_digest"`
	ParserVersion            string               `json:"parser_version"`
	PromptVersion            string               `json:"prompt_version"`
	ModelID                  string               `json:"model_id"`
	ModelEffort              string               `json:"model_effort"`
	DestinationStoreID       string               `json:"destination_store_id"`
	DestinationGeneration    int64                `json:"destination_generation"`
	DestinationBindingDigest string               `json:"destination_binding_digest"`
	RepositoryID             string               `json:"repository_id"`
	PolicyEpoch              int64                `json:"policy_epoch"`
	ResolverEpoch            int64                `json:"resolver_epoch"`
	MaterializerVersion      string               `json:"materializer_version"`
	DedupVersion             string               `json:"dedup_version"`
	TargetVersionsDigest     string               `json:"target_versions_digest"`
	Items                    []CanonicalWriteItem `json:"items"`
}

func (set WriteSet) Validate() error {
	if set.Schema != WriteSetSchemaV1 || !jobIDPattern.MatchString(set.JobID) || set.Revision < 1 ||
		!hexDigestPattern.MatchString(set.ManifestDigest) || !hexDigestPattern.MatchString(set.SourceSnapshotDigest) ||
		!hexDigestPattern.MatchString(set.SchemaDigest) || !hexDigestPattern.MatchString(set.RunnerContractDigest) ||
		!hexDigestPattern.MatchString(set.DestinationBindingDigest) || !hexDigestPattern.MatchString(set.TargetVersionsDigest) ||
		set.DestinationStoreID == "" || set.RepositoryID == "" || set.DestinationGeneration < 1 ||
		set.PolicyEpoch < 0 || set.ResolverEpoch < 1 || set.MaterializerVersion != MaterializerVersionV1 ||
		set.DedupVersion != DedupVersionV1 || set.ModelID != "gpt-5.6-luna" || set.ModelEffort != "low" ||
		set.ParserVersion == "" || set.PromptVersion == "" || len(set.Items) == 0 {
		return ErrApplyWriteSetInvalid
	}
	seenCandidates := make(map[string]struct{}, len(set.Items))
	seenObjects := make(map[string]string, len(set.Items))
	for index, item := range set.Items {
		if !candidateIDPattern.MatchString(item.CandidateID) || item.MaterialKind == "" || item.CapsuleKind == "" ||
			item.Summary == "" || len(item.Summary) > 1200 || item.Confidence < 0 || item.Confidence > 1 ||
			item.Scope.Validate() != nil || item.ValidTime.Validate() != nil ||
			(item.EpistemicStatus != EpistemicExplicit && item.EpistemicStatus != EpistemicHypothesis && item.EpistemicStatus != EpistemicConflict) ||
			(item.Derivation != DerivationDirect && item.Derivation != DerivationInferred) ||
			(item.Derivation == DerivationInferred && item.EpistemicStatus == EpistemicExplicit) ||
			!hexDigestPattern.MatchString(item.ContentDigest) || item.Target.ObjectKind != "memory_capsule" ||
			!hexDigestPattern.MatchString(item.Target.ObjectDigest) || item.Target.ObjectDigest != item.ContentDigest ||
			item.Target.ObjectID == "" || item.Target.LogicalGeneration < 1 ||
			(item.Target.Outcome != ItemCreated && item.Target.Outcome != ItemDeduplicated) || len(item.SourceRefs) == 0 {
			return fmt.Errorf("%w: item %d", ErrApplyWriteSetInvalid, index)
		}
		for _, ref := range item.SourceRefs {
			if ref.Validate() != nil {
				return fmt.Errorf("%w: item %d source", ErrApplyWriteSetInvalid, index)
			}
		}
		if _, duplicate := seenCandidates[item.CandidateID]; duplicate {
			return ErrApplyWriteSetInvalid
		}
		seenCandidates[item.CandidateID] = struct{}{}
		if digest, exists := seenObjects[item.Target.ObjectID]; exists && digest != item.ContentDigest {
			return ErrApplyWriteSetInvalid
		}
		seenObjects[item.Target.ObjectID] = item.ContentDigest
	}
	if set.TargetVersionsDigest != TargetVersionsDigest(set.Items) {
		return ErrApplyWriteSetInvalid
	}
	return nil
}

func TargetVersionsDigest(items []CanonicalWriteItem) string {
	type version struct {
		CandidateID string      `json:"candidate_id"`
		Target      WriteTarget `json:"target"`
	}
	versions := make([]version, len(items))
	for index, item := range items {
		versions[index] = version{CandidateID: item.CandidateID, Target: item.Target}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].CandidateID < versions[j].CandidateID })
	encoded, _ := json.Marshal(versions)
	return digestBytes(encoded)
}

func EncodeWriteSet(set WriteSet) ([]byte, string, error) {
	if err := set.Validate(); err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(set)
	if err != nil {
		return nil, "", err
	}
	return encoded, digestBytes(encoded), nil
}

func DecodeWriteSet(data []byte) (WriteSet, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var set WriteSet
	if err := decoder.Decode(&set); err != nil {
		return WriteSet{}, "", fmt.Errorf("decode historical write set: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return WriteSet{}, "", ErrApplyWriteSetInvalid
	}
	if err := set.Validate(); err != nil {
		return WriteSet{}, "", err
	}
	return set, digestBytes(data), nil
}

func (m *IngestManager) ApplySource(jobID string, expectedRevision int64, expectedDigest string) (ApplySource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return ApplySource{}, ErrIngestJobNotFound
	}
	if checkpoint.State != JobApprovalReady || !checkpoint.ReviewComplete ||
		checkpoint.ManifestRevision != expectedRevision || checkpoint.ManifestDigest != expectedDigest {
		return ApplySource{}, ErrApplyNotReady
	}
	manifest, err := m.readManifestLocked(expectedDigest)
	if err != nil {
		return ApplySource{}, err
	}
	dispositions := make(map[string]ReviewDisposition, len(checkpoint.Review))
	for key, value := range checkpoint.Review {
		dispositions[key] = value
	}
	return ApplySource{Manifest: manifest, ManifestDigest: expectedDigest, Snapshot: checkpoint.Snapshot, Contract: checkpoint.Contract, Dispositions: dispositions}, nil
}

func (source ApplySource) IncludedItems() []MaterialItem {
	items := make([]MaterialItem, 0, len(source.Manifest.Items))
	for _, item := range source.Manifest.Items {
		if source.Dispositions[item.CandidateID] != ReviewExcluded {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CandidateID < items[j].CandidateID })
	return items
}

func (m *IngestManager) RecordWriteSet(jobID, manifestDigest, writeSetDigest, destinationStoreID string, destinationGeneration int64) (JobStatus, error) {
	if !hexDigestPattern.MatchString(manifestDigest) || !hexDigestPattern.MatchString(writeSetDigest) || destinationStoreID == "" || destinationGeneration < 1 {
		return JobStatus{}, ErrApplyWriteSetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	if checkpoint.State != JobApprovalReady || !checkpoint.ReviewComplete || checkpoint.ManifestDigest != manifestDigest {
		return JobStatus{}, ErrApplyNotReady
	}
	if checkpoint.WriteSetDigest == writeSetDigest && checkpoint.DestinationStoreID == destinationStoreID && checkpoint.DestinationGeneration == destinationGeneration {
		return statusFor(checkpoint), nil
	}
	if checkpoint.WriteSetDigest != "" && (checkpoint.DestinationStoreID != destinationStoreID || destinationGeneration <= checkpoint.DestinationGeneration) {
		return JobStatus{}, ErrApplyVersionConflict
	}
	checkpoint.WriteSetDigest = writeSetDigest
	checkpoint.DestinationStoreID = destinationStoreID
	checkpoint.DestinationGeneration = destinationGeneration
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) MarkApplying(jobID, manifestDigest, writeSetDigest string) (JobStatus, error) {
	return m.transitionApplyState(jobID, manifestDigest, writeSetDigest, JobApplying, "")
}

func (m *IngestManager) MarkApplyReady(jobID, manifestDigest, writeSetDigest, reason string) (JobStatus, error) {
	return m.transitionApplyState(jobID, manifestDigest, writeSetDigest, JobApprovalReady, reason)
}

func (m *IngestManager) MarkCommitted(jobID, manifestDigest, writeSetDigest, receiptID string) (JobStatus, error) {
	if receiptID == "" || !hexDigestPattern.MatchString(manifestDigest) || !hexDigestPattern.MatchString(writeSetDigest) {
		return JobStatus{}, ErrApplyVersionConflict
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	if checkpoint.State != JobApplying || checkpoint.ManifestDigest != manifestDigest || checkpoint.WriteSetDigest != writeSetDigest {
		return JobStatus{}, ErrApplyVersionConflict
	}
	checkpoint.State, checkpoint.ReasonCode = JobCommittedIndexing, ""
	checkpoint.BatchReceiptID = receiptID
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) MarkProjectionState(jobID, receiptID string, state ProjectionState) (JobStatus, error) {
	if receiptID == "" || (state != ProjectionReady && state != ProjectionFailed && state != ProjectionPending) {
		return JobStatus{}, ErrApplyVersionConflict
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	if checkpoint.BatchReceiptID != receiptID || (checkpoint.State != JobCommittedIndexing && checkpoint.State != JobIndexingFailed && checkpoint.State != JobRetrievalReady) {
		return JobStatus{}, ErrApplyVersionConflict
	}
	if (state == ProjectionPending && checkpoint.State == JobCommittedIndexing) ||
		(state == ProjectionReady && checkpoint.State == JobRetrievalReady) ||
		(state == ProjectionFailed && checkpoint.State == JobIndexingFailed) {
		return statusFor(checkpoint), nil
	}
	switch state {
	case ProjectionReady:
		checkpoint.State, checkpoint.ReasonCode = JobRetrievalReady, ""
	case ProjectionFailed:
		checkpoint.State, checkpoint.ReasonCode = JobIndexingFailed, "projection_failed"
	case ProjectionPending:
		checkpoint.State, checkpoint.ReasonCode = JobCommittedIndexing, ""
	}
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}

func (m *IngestManager) transitionApplyState(jobID, manifestDigest, writeSetDigest string, next JobState, reason string) (JobStatus, error) {
	if !hexDigestPattern.MatchString(manifestDigest) || !hexDigestPattern.MatchString(writeSetDigest) {
		return JobStatus{}, ErrApplyVersionConflict
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return JobStatus{}, ErrIngestJobNotFound
	}
	allowed := false
	switch next {
	case JobApplying:
		allowed = checkpoint.State == JobApprovalReady
	case JobApprovalReady:
		allowed = checkpoint.State == JobApplying
	}
	if !allowed || checkpoint.ManifestDigest != manifestDigest || checkpoint.WriteSetDigest != writeSetDigest {
		return JobStatus{}, ErrApplyVersionConflict
	}
	checkpoint.State, checkpoint.ReasonCode = next, reason
	if err := m.commitLocked(&checkpoint); err != nil {
		return JobStatus{}, err
	}
	return statusFor(checkpoint), nil
}
