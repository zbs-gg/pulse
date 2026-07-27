package historicalingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/nkkmnk/pulse/internal/platform"
)

var (
	ErrReviewVersionConflict = errors.New("historical ingest review version conflict")
	ErrReviewIncomplete      = errors.New("historical ingest review is incomplete")
	ErrReviewInvalid         = errors.New("historical ingest review mutation invalid")
)

type ReviewDisposition string

const (
	ReviewPending  ReviewDisposition = "pending"
	ReviewKept     ReviewDisposition = "kept"
	ReviewExcluded ReviewDisposition = "excluded"
)

type ReviewItem struct {
	Item              MaterialItem      `json:"item"`
	Disposition       ReviewDisposition `json:"disposition"`
	RequiresReview    bool              `json:"requires_review"`
	RequirementCodes  []string          `json:"requirement_codes"`
	EvidenceAvailable bool              `json:"evidence_available"`
	SourceRoots       []string          `json:"source_roots"`
}

type ReviewSnapshot struct {
	Schema            string       `json:"schema"`
	JobID             string       `json:"job_id"`
	State             JobState     `json:"state"`
	Revision          int64        `json:"revision"`
	ManifestDigest    string       `json:"manifest_digest"`
	SourceRootCount   int          `json:"source_root_count"`
	SourceFileCount   int          `json:"source_file_count"`
	CandidateCount    int          `json:"candidate_count"`
	WriteCount        int          `json:"write_count"`
	ExcludedCount     int          `json:"excluded_count"`
	ReviewedCount     int          `json:"reviewed_count"`
	RemainingRequired int          `json:"remaining_required"`
	ReviewComplete    bool         `json:"review_complete"`
	Items             []ReviewItem `json:"items"`
}

type ReviewMutation struct {
	ExpectedRevision int64
	ExpectedDigest   string
	CandidateID      string
	Disposition      ReviewDisposition
	Replacement      *MaterialItem
}

type EntityRewritePreview struct {
	Schema             string                  `json:"schema"`
	JobID              string                  `json:"job_id"`
	Revision           int64                   `json:"revision"`
	ManifestDigest     string                  `json:"manifest_digest"`
	Mode               string                  `json:"mode"`
	FromEntityID       string                  `json:"from_entity_id"`
	ToEntityID         string                  `json:"to_entity_id"`
	SelectedCandidates []string                `json:"selected_candidates,omitempty"`
	Affected           []EntityRewriteAffected `json:"affected"`
	PreviewDigest      string                  `json:"preview_digest"`
}

type EntityRewriteAffected struct {
	OldCandidateID string       `json:"old_candidate_id"`
	NewCandidateID string       `json:"new_candidate_id"`
	Kind           MaterialKind `json:"kind"`
	Scope          Scope        `json:"scope"`
	SourceRefs     []SourceRef  `json:"source_refs"`
}

func validReviewDisposition(value ReviewDisposition) bool {
	return value == ReviewPending || value == ReviewKept || value == ReviewExcluded
}

func (m *IngestManager) ReviewSnapshot(jobID string, unavailableEvidence map[string]bool) (ReviewSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return ReviewSnapshot{}, ErrIngestJobNotFound
	}
	if checkpoint.ManifestDigest == "" {
		return ReviewSnapshot{}, ErrIncompleteCohort
	}
	manifest, err := m.readManifestLocked(checkpoint.ManifestDigest)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	return reviewSnapshotFor(checkpoint, manifest, unavailableEvidence), nil
}

func (m *IngestManager) LatestReviewSnapshot(unavailableEvidence map[string]bool) (ReviewSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var selected *ingestCheckpoint
	for _, checkpoint := range m.jobs {
		if checkpoint.ManifestDigest == "" {
			continue
		}
		if selected == nil || checkpoint.UpdatedAt.After(selected.UpdatedAt) ||
			(checkpoint.UpdatedAt.Equal(selected.UpdatedAt) && checkpoint.Generation > selected.Generation) {
			copy := checkpoint
			selected = &copy
		}
	}
	if selected == nil {
		return ReviewSnapshot{}, ErrIngestJobNotFound
	}
	manifest, err := m.readManifestLocked(selected.ManifestDigest)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	return reviewSnapshotFor(*selected, manifest, unavailableEvidence), nil
}

func reviewSnapshotFor(checkpoint ingestCheckpoint, manifest Manifest, unavailableEvidence map[string]bool) ReviewSnapshot {
	snapshot := ReviewSnapshot{
		Schema: "pulse.historical_ingest.review.v1", JobID: checkpoint.JobID, State: checkpoint.State,
		Revision: manifest.Revision, ManifestDigest: checkpoint.ManifestDigest,
		SourceRootCount: checkpoint.Snapshot.RootCount, SourceFileCount: len(checkpoint.Snapshot.Files),
		CandidateCount: len(manifest.Items), ReviewComplete: checkpoint.ReviewComplete,
		Items: make([]ReviewItem, 0, len(manifest.Items)),
	}
	aliasRoots := make(map[string]string, len(checkpoint.Snapshot.Files))
	for _, source := range checkpoint.Snapshot.Files {
		aliasRoots[source.Alias] = source.RootID
	}
	for _, item := range manifest.Items {
		disposition := checkpoint.Review[item.CandidateID]
		if disposition == "" {
			disposition = ReviewPending
		}
		evidenceAvailable := !unavailableEvidence[item.CandidateID]
		reasons := reviewRequirementCodes(item, evidenceAvailable)
		roots := make([]string, 0, len(item.SourceRefs))
		for _, ref := range item.SourceRefs {
			if root := aliasRoots[ref.Alias]; root != "" {
				roots = append(roots, root)
			}
		}
		entry := ReviewItem{Item: item, Disposition: disposition, RequiresReview: len(reasons) > 0, RequirementCodes: reasons, EvidenceAvailable: evidenceAvailable, SourceRoots: sortedUniqueStrings(roots)}
		snapshot.Items = append(snapshot.Items, entry)
		if disposition == ReviewExcluded {
			snapshot.ExcludedCount++
		} else {
			snapshot.WriteCount++
		}
		if disposition != ReviewPending {
			snapshot.ReviewedCount++
		}
		if entry.RequiresReview && disposition == ReviewPending {
			snapshot.RemainingRequired++
		}
	}
	sort.SliceStable(snapshot.Items, func(i, j int) bool {
		left, right := snapshot.Items[i], snapshot.Items[j]
		leftRank, rightRank := reviewRank(left), reviewRank(right)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if left.Item.Kind != right.Item.Kind {
			return left.Item.Kind < right.Item.Kind
		}
		return left.Item.CandidateID < right.Item.CandidateID
	})
	return snapshot
}

func reviewRequirementCodes(item MaterialItem, evidenceAvailable bool) []string {
	var reasons []string
	if !evidenceAvailable {
		reasons = append(reasons, "evidence_unavailable")
	}
	if item.EpistemicStatus == EpistemicHypothesis {
		reasons = append(reasons, "hypothesis")
	}
	if item.EpistemicStatus == EpistemicConflict {
		reasons = append(reasons, "conflict")
	}
	if item.Derivation == DerivationInferred {
		reasons = append(reasons, "inferred")
	}
	if item.Scope.Kind == ScopeUnassigned {
		reasons = append(reasons, "unassigned_scope")
	}
	return reasons
}

func reviewRank(item ReviewItem) int {
	if !item.EvidenceAvailable {
		return 0
	}
	if item.RequiresReview {
		return 1
	}
	return 2
}

func (m *IngestManager) MutateReview(jobID string, mutation ReviewMutation) (ReviewSnapshot, error) {
	if mutation.ExpectedRevision < 1 || !hexDigestPattern.MatchString(mutation.ExpectedDigest) ||
		!candidateIDPattern.MatchString(mutation.CandidateID) || !validReviewDisposition(mutation.Disposition) || mutation.Disposition == ReviewPending {
		return ReviewSnapshot{}, ErrReviewInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, manifest, err := m.reviewManifestLocked(jobID, mutation.ExpectedRevision, mutation.ExpectedDigest)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	index := manifestItemIndex(manifest.Items, mutation.CandidateID)
	if index < 0 {
		return ReviewSnapshot{}, ErrReviewInvalid
	}
	oldID := mutation.CandidateID
	if mutation.Replacement != nil {
		replacement := *mutation.Replacement
		original := manifest.Items[index]
		if replacement.Privacy != PrivacyPrivate || !sameSourceRefs(replacement.SourceRefs, original.SourceRefs) {
			return ReviewSnapshot{}, ErrReviewInvalid
		}
		replacement.CandidateID = "candidate_" + materialIdentity(replacement)
		if err := replacement.Validate(); err != nil {
			return ReviewSnapshot{}, ErrReviewInvalid
		}
		manifest.Items[index] = replacement
		for other := range manifest.Items {
			if other != index && manifest.Items[other].CandidateID == replacement.CandidateID {
				return ReviewSnapshot{}, ErrReviewInvalid
			}
		}
		mutation.CandidateID = replacement.CandidateID
	}
	if checkpoint.Review == nil {
		checkpoint.Review = map[string]ReviewDisposition{}
	}
	delete(checkpoint.Review, oldID)
	checkpoint.Review[mutation.CandidateID] = mutation.Disposition
	checkpoint.ReviewComplete = false
	checkpoint.State = JobManifestReady
	checkpoint.ReasonCode = ""
	if err := m.persistReviewRevisionLocked(&checkpoint, &manifest); err != nil {
		return ReviewSnapshot{}, err
	}
	return reviewSnapshotFor(checkpoint, manifest, nil), nil
}

func (m *IngestManager) CompleteReview(jobID string, expectedRevision int64, expectedDigest, confirmationDigest string, unavailableEvidence map[string]bool) (ReviewSnapshot, error) {
	if confirmationDigest != ReviewConfirmationDigest(jobID, expectedRevision, expectedDigest) {
		return ReviewSnapshot{}, ErrReviewInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, manifest, err := m.reviewManifestLocked(jobID, expectedRevision, expectedDigest)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	current := reviewSnapshotFor(checkpoint, manifest, unavailableEvidence)
	if current.RemainingRequired != 0 {
		return ReviewSnapshot{}, ErrReviewIncomplete
	}
	manifest.Revision++
	checkpoint.ReviewComplete = true
	checkpoint.State = JobApprovalReady
	if err := m.persistReviewRevisionLocked(&checkpoint, &manifest); err != nil {
		return ReviewSnapshot{}, err
	}
	return reviewSnapshotFor(checkpoint, manifest, unavailableEvidence), nil
}

func ReviewConfirmationDigest(jobID string, revision int64, manifestDigest string) string {
	return digestString(fmt.Sprintf("pulse:historical-review:v1:%s:%d:%s", jobID, revision, manifestDigest))
}

func (m *IngestManager) EvidenceUnitForCandidate(jobID, candidateID string) (WorkUnit, SourceSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return WorkUnit{}, SourceSnapshot{}, ErrIngestJobNotFound
	}
	manifest, err := m.readManifestLocked(checkpoint.ManifestDigest)
	if err != nil {
		return WorkUnit{}, SourceSnapshot{}, err
	}
	index := manifestItemIndex(manifest.Items, candidateID)
	if index < 0 {
		return WorkUnit{}, SourceSnapshot{}, ErrReviewInvalid
	}
	aliases := map[string]struct{}{}
	for _, ref := range manifest.Items[index].SourceRefs {
		aliases[ref.Alias] = struct{}{}
	}
	for _, unit := range checkpoint.Units {
		for _, alias := range unit.Unit.SourceAliases {
			if _, ok := aliases[alias]; ok {
				return unit.Unit, checkpoint.Snapshot, nil
			}
		}
	}
	return WorkUnit{}, SourceSnapshot{}, ErrReviewInvalid
}

func (m *IngestManager) PreviewEntityRewrite(jobID string, expectedRevision int64, expectedDigest, mode, fromID, toID string, selected []string) (EntityRewritePreview, error) {
	if (mode != "merge" && mode != "split") || !opaqueRefPattern.MatchString(fromID) || !opaqueRefPattern.MatchString(toID) || fromID == toID {
		return EntityRewritePreview{}, ErrReviewInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, manifest, err := m.reviewManifestLocked(jobID, expectedRevision, expectedDigest)
	if err != nil {
		return EntityRewritePreview{}, err
	}
	selected = sortedUniqueStrings(selected)
	selectedSet := map[string]struct{}{}
	for _, candidateID := range selected {
		if !candidateIDPattern.MatchString(candidateID) {
			return EntityRewritePreview{}, ErrReviewInvalid
		}
		selectedSet[candidateID] = struct{}{}
	}
	if mode == "split" && len(selectedSet) == 0 {
		return EntityRewritePreview{}, ErrReviewInvalid
	}
	preview := EntityRewritePreview{Schema: "pulse.historical_ingest.entity_rewrite_preview.v1", JobID: jobID, Revision: manifest.Revision, ManifestDigest: expectedDigest, Mode: mode, FromEntityID: fromID, ToEntityID: toID, SelectedCandidates: selected}
	for _, item := range manifest.Items {
		if mode == "split" {
			if _, ok := selectedSet[item.CandidateID]; !ok {
				continue
			}
		}
		rewritten, changed := rewriteItemEntity(item, fromID, toID)
		if !changed {
			continue
		}
		rewritten.CandidateID = "candidate_" + materialIdentity(rewritten)
		preview.Affected = append(preview.Affected, EntityRewriteAffected{OldCandidateID: item.CandidateID, NewCandidateID: rewritten.CandidateID, Kind: item.Kind, Scope: item.Scope, SourceRefs: item.SourceRefs})
	}
	if len(preview.Affected) == 0 {
		return EntityRewritePreview{}, ErrReviewInvalid
	}
	encoded, _ := json.Marshal(preview)
	preview.PreviewDigest = digestBytes(encoded)
	return preview, nil
}

func (m *IngestManager) ApplyEntityRewrite(jobID string, expectedRevision int64, expectedDigest, mode, fromID, toID string, selected []string, previewDigest string) (ReviewSnapshot, error) {
	preview, err := m.PreviewEntityRewrite(jobID, expectedRevision, expectedDigest, mode, fromID, toID, selected)
	if err != nil || preview.PreviewDigest != previewDigest {
		return ReviewSnapshot{}, ErrReviewVersionConflict
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, manifest, err := m.reviewManifestLocked(jobID, expectedRevision, expectedDigest)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	selectedSet := map[string]struct{}{}
	for _, id := range selected {
		selectedSet[id] = struct{}{}
	}
	for index, item := range manifest.Items {
		if mode == "split" {
			if _, ok := selectedSet[item.CandidateID]; !ok {
				continue
			}
		}
		if rewritten, changed := rewriteItemEntity(item, fromID, toID); changed {
			rewritten.CandidateID = "candidate_" + materialIdentity(rewritten)
			manifest.Items[index] = rewritten
		}
	}
	manifest.Items = canonicalizeReviewedItems(manifest.Items)
	checkpoint.Review = nil
	checkpoint.ReviewComplete = false
	checkpoint.State = JobManifestReady
	checkpoint.ReasonCode = ""
	if err := m.persistReviewRevisionLocked(&checkpoint, &manifest); err != nil {
		return ReviewSnapshot{}, err
	}
	return reviewSnapshotFor(checkpoint, manifest, nil), nil
}

func (m *IngestManager) reviewManifestLocked(jobID string, expectedRevision int64, expectedDigest string) (ingestCheckpoint, Manifest, error) {
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return ingestCheckpoint{}, Manifest{}, ErrIngestJobNotFound
	}
	if checkpoint.ManifestRevision != expectedRevision || checkpoint.ManifestDigest != expectedDigest || !hexDigestPattern.MatchString(expectedDigest) {
		return ingestCheckpoint{}, Manifest{}, ErrReviewVersionConflict
	}
	if checkpoint.State != JobManifestReady && checkpoint.State != JobApprovalReady {
		return ingestCheckpoint{}, Manifest{}, ErrJobNotExtracting
	}
	manifest, err := m.readManifestLocked(expectedDigest)
	return checkpoint, manifest, err
}

func (m *IngestManager) persistReviewRevisionLocked(checkpoint *ingestCheckpoint, manifest *Manifest) error {
	if manifest.Revision <= checkpoint.ManifestRevision {
		manifest.Revision = checkpoint.ManifestRevision + 1
	}
	encoded, err := EncodeManifest(*manifest)
	if err != nil || len(encoded) > maxIngestManifest || unsafeEvidencePattern.Match(encoded) {
		return ErrReviewInvalid
	}
	digest := digestBytes(encoded)
	path := filepath.Join(m.rootDir, "manifest-"+digest+".json")
	if err := createReviewManifest(path, encoded); err != nil {
		return err
	}
	checkpoint.ManifestRevision = manifest.Revision
	checkpoint.ManifestDigest = digest
	checkpoint.WriteSetDigest = ""
	checkpoint.DestinationStoreID = ""
	checkpoint.DestinationGeneration = 0
	checkpoint.BatchReceiptID = ""
	return m.commitLocked(checkpoint)
}

func createReviewManifest(path string, encoded []byte) error {
	_, err := platform.CreatePrivateFileExclusive(path, encoded)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := platform.ReadPrivateFile(path, privateIngestPolicy(maxIngestManifest))
		if readErr != nil || string(existing) != string(encoded) {
			return ErrIngestCheckpointIntegrity
		}
		return nil
	}
	return err
}

func manifestItemIndex(items []MaterialItem, candidateID string) int {
	for index := range items {
		if items[index].CandidateID == candidateID {
			return index
		}
	}
	return -1
}

func sameSourceRefs(left, right []SourceRef) bool {
	left, right = sortedUniqueSourceRefs(append([]SourceRef(nil), left...)), sortedUniqueSourceRefs(append([]SourceRef(nil), right...))
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

func rewriteItemEntity(item MaterialItem, fromID, toID string) (MaterialItem, bool) {
	changed := false
	if item.Payload.SubjectID == fromID {
		item.Payload.SubjectID, changed = toID, true
	}
	if item.Payload.ObjectID == fromID {
		item.Payload.ObjectID, changed = toID, true
	}
	return item, changed
}

func canonicalizeReviewedItems(items []MaterialItem) []MaterialItem {
	groups := map[string]MaterialItem{}
	for _, item := range items {
		identity := materialIdentity(item)
		item.CandidateID = "candidate_" + identity
		if current, ok := groups[identity]; ok {
			current.SourceRefs = sortedUniqueSourceRefs(append(current.SourceRefs, item.SourceRefs...))
			groups[identity] = current
		} else {
			groups[identity] = item
		}
	}
	result := make([]MaterialItem, 0, len(groups))
	for _, item := range groups {
		result = append(result, item)
	}
	markAssertionConflicts(result)
	for index := range result {
		result[index].CandidateID = "candidate_" + materialIdentity(result[index])
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CandidateID < result[j].CandidateID })
	return result
}

func sortedUniqueStrings(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return append([]string(nil), result...)
}
