package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/historicalingest"
)

const historicalEvidenceViewLimit = 8 << 10

type memoryHomeHistoricalReview struct {
	Available          bool
	HasManifest        bool
	Unavailable        bool
	JobID              string
	State              string
	StateLabel         string
	StateDetail        string
	SourceRootCount    int
	SourceFileCount    int
	CandidateCount     int
	WriteCount         int
	ExcludedCount      int
	ReviewedCount      int
	RemainingRequired  int
	Revision           int64
	ManifestDigest     string
	ReviewComplete     bool
	CanComplete        bool
	CanMutate          bool
	ConfirmationDigest string
	DestinationStoreID string
	RepositoryID       string
	Cards              []memoryHomeHistoricalCard
	RootOptions        []memoryHomeHistoricalRoot
}

type memoryHomeHistoricalRoot struct{ Value, Label string }

type memoryHomeHistoricalCard struct {
	CandidateID       string
	Kind              string
	PrimaryText       string
	Confidence        string
	EpistemicStatus   string
	Derivation        string
	ScopeKind         string
	ProjectID         string
	ValidFrom         string
	ValidTo           string
	ContinuityStatus  string
	Disposition       string
	RequiresReview    bool
	RequirementLabels []string
	EvidenceAvailable bool
	SourceCount       int
	RootIDs           string
	RootLabel         string
	RelationSubject   string
	RelationObject    string
}

func (s *Server) memoryHomeHistoricalReview() memoryHomeHistoricalReview {
	if s.historicalUnavailable != "" {
		return memoryHomeHistoricalReview{Unavailable: true, StateLabel: "Historical review unavailable", StateDetail: "The private review checkpoint failed integrity verification. No history was read or written."}
	}
	if s.historicalIngest == nil {
		return memoryHomeHistoricalReview{}
	}
	status, err := s.historicalIngest.LatestStatus()
	if errors.Is(err, historicalingest.ErrIngestJobNotFound) {
		return memoryHomeHistoricalReview{}
	}
	if err != nil {
		return memoryHomeHistoricalReview{Unavailable: true, StateLabel: "Historical review unavailable", StateDetail: "Pulse could not verify the latest private job checkpoint."}
	}
	if status.ManifestDigest == "" {
		title, detail := historicalReviewStateCopy(status.State, 0)
		return memoryHomeHistoricalReview{Available: true, JobID: status.JobID, State: string(status.State), StateLabel: title, StateDetail: detail, SourceRootCount: status.SourceRootCount, SourceFileCount: status.SourceFileCount}
	}
	snapshot, err := s.historicalIngest.ReviewSnapshot(status.JobID, nil)
	if err != nil {
		return memoryHomeHistoricalReview{Unavailable: true, StateLabel: "Historical review unavailable", StateDetail: "Pulse could not verify the latest private manifest. Nothing can be approved."}
	}
	if s.historicalEvidence == nil {
		unavailable := make(map[string]bool, len(snapshot.Items))
		for _, item := range snapshot.Items {
			unavailable[item.Item.CandidateID] = true
		}
		snapshot, err = s.historicalIngest.ReviewSnapshot(snapshot.JobID, unavailable)
		if err != nil {
			return memoryHomeHistoricalReview{Unavailable: true, StateLabel: "Historical review unavailable", StateDetail: "Pulse could not verify the latest private manifest. Nothing can be approved."}
		}
	}
	view := memoryHomeHistoricalReview{
		Available: true, HasManifest: true, JobID: snapshot.JobID, State: string(snapshot.State),
		SourceRootCount: snapshot.SourceRootCount, SourceFileCount: snapshot.SourceFileCount,
		CandidateCount: snapshot.CandidateCount, WriteCount: snapshot.WriteCount, ExcludedCount: snapshot.ExcludedCount,
		ReviewedCount: snapshot.ReviewedCount, RemainingRequired: snapshot.RemainingRequired,
		Revision: snapshot.Revision, ManifestDigest: snapshot.ManifestDigest, ReviewComplete: snapshot.ReviewComplete,
		CanComplete: snapshot.RemainingRequired == 0 && !snapshot.ReviewComplete && snapshot.State == historicalingest.JobManifestReady,
		CanMutate:   snapshot.State == historicalingest.JobManifestReady || snapshot.State == historicalingest.JobApprovalReady,
	}
	view.StateLabel, view.StateDetail = historicalReviewStateCopy(snapshot.State, snapshot.RemainingRequired)
	bindingDigest, repositoryID, boundaryOK := s.cfg.Store.ProductRuntimeBoundary()
	if boundaryOK {
		view.DestinationStoreID, view.RepositoryID = s.cfg.Store.StoreID(), repositoryID
		view.ConfirmationDigest = historicalReviewDestinationConfirmationDigest(snapshot.JobID, snapshot.Revision, snapshot.ManifestDigest, view.DestinationStoreID, repositoryID, bindingDigest)
	}
	rootSet := map[string]struct{}{}
	for _, item := range snapshot.Items {
		view.Cards = append(view.Cards, historicalReviewCard(item))
		for _, root := range item.SourceRoots {
			rootSet[root] = struct{}{}
		}
	}
	for root := range rootSet {
		view.RootOptions = append(view.RootOptions, memoryHomeHistoricalRoot{Value: root, Label: root})
	}
	sort.Slice(view.RootOptions, func(i, j int) bool { return view.RootOptions[i].Value < view.RootOptions[j].Value })
	return view
}

func historicalReviewStateCopy(state historicalingest.JobState, remaining int) (string, string) {
	switch state {
	case historicalingest.JobManifestReady:
		if remaining > 0 {
			return "Review needed", fmt.Sprintf("%d blocking candidate(s) still need an explicit keep or exclude decision.", remaining)
		}
		return "Ready to finish review", "Every blocking candidate has a disposition. Finishing review freezes a new revision; it does not write memory."
	case historicalingest.JobApprovalReady:
		return "Review complete", "This revision is frozen for the later apply compiler. No memory has been written yet."
	case historicalingest.JobNothingToImport:
		return "Nothing to import", "All selected roots were processed and produced no structured memory."
	case historicalingest.JobPausedQuota:
		return "Paused by subscription quota", "Accepted units are safe. Resume continues from the exact next uncommitted unit."
	case historicalingest.JobExtractionFailed:
		return "Extraction needs attention", "Accepted units are safe. Resume retries only the uncommitted work."
	case historicalingest.JobStale:
		return "Source changed", "The frozen source prefix no longer matches. This manifest cannot be reviewed or applied."
	case historicalingest.JobCanceled:
		return "Canceled", "No candidate from this job can be applied."
	default:
		return strings.ReplaceAll(string(state), "_", " "), "Pulse shows this lifecycle state without implying review or apply authority."
	}
}

func historicalReviewCard(value historicalingest.ReviewItem) memoryHomeHistoricalCard {
	item := value.Item
	card := memoryHomeHistoricalCard{
		CandidateID: item.CandidateID, Kind: string(item.Kind), PrimaryText: historicalPrimaryText(item),
		Confidence: fmt.Sprintf("%.0f%%", item.Confidence*100), EpistemicStatus: string(item.EpistemicStatus),
		Derivation: string(item.Derivation), ScopeKind: string(item.Scope.Kind), ProjectID: item.Scope.ProjectID,
		ValidFrom: item.ValidTime.From.UTC().Format(time.RFC3339), Disposition: string(value.Disposition),
		RequiresReview: value.RequiresReview, EvidenceAvailable: value.EvidenceAvailable, SourceCount: len(item.SourceRefs),
		ContinuityStatus: item.Payload.ContinuityStatus, RootIDs: strings.Join(value.SourceRoots, " "),
		RootLabel: strings.Join(value.SourceRoots, ", "), RelationSubject: item.Payload.SubjectID,
		RelationObject: item.Payload.ObjectID,
	}
	if item.ValidTime.To != nil {
		card.ValidTo = item.ValidTime.To.UTC().Format(time.RFC3339)
	}
	for _, code := range value.RequirementCodes {
		card.RequirementLabels = append(card.RequirementLabels, historicalRequirementLabel(code))
	}
	return card
}

func historicalPrimaryText(item historicalingest.MaterialItem) string {
	switch item.Kind {
	case historicalingest.MaterialKindAssertion:
		return item.Payload.ObjectValue
	case historicalingest.MaterialKindPerson, historicalingest.MaterialKindProject:
		return item.Payload.Name
	case historicalingest.MaterialKindRelation:
		return item.Payload.Predicate
	default:
		if item.Payload.Summary != "" {
			return item.Payload.Summary
		}
		return item.Payload.Title
	}
}

func historicalRequirementLabel(code string) string {
	switch code {
	case "evidence_unavailable":
		return "Evidence unavailable"
	case "hypothesis":
		return "Hypothesis"
	case "conflict":
		return "Conflicting evidence"
	case "inferred":
		return "Inferred"
	case "unassigned_scope":
		return "Needs a scope"
	default:
		return "Review required"
	}
}

func (s *Server) handleHomeHistoricalReviewItem(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	jobID, candidateID := chi.URLParam(r, "job"), chi.URLParam(r, "candidate")
	if !exactHistoricalReviewBase(r, candidateID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	revision, err := strconv.ParseInt(r.PostFormValue("expected_revision"), 10, 64)
	if err != nil || revision < 1 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	action := r.PostFormValue("action")
	disposition := historicalingest.ReviewDisposition(action)
	mutation := historicalingest.ReviewMutation{
		ExpectedRevision: revision, ExpectedDigest: r.PostFormValue("expected_digest"), CandidateID: candidateID,
		Disposition: disposition,
	}
	if action == "edit" {
		if !exactHistoricalEditFields(r) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		snapshot, snapshotErr := s.historicalIngest.ReviewSnapshot(jobID, nil)
		if snapshotErr != nil {
			writeHistoricalReviewError(w, snapshotErr)
			return
		}
		item, found := historicalReviewItemByID(snapshot, candidateID)
		if !found {
			writeHistoricalReviewError(w, historicalingest.ErrReviewInvalid)
			return
		}
		replacement, replacementErr := historicalReplacementFromForm(item, r)
		if replacementErr != nil {
			writeHistoricalReviewError(w, replacementErr)
			return
		}
		mutation.Disposition = historicalingest.ReviewKept
		mutation.Replacement = &replacement
	} else if action != string(historicalingest.ReviewKept) && action != string(historicalingest.ReviewExcluded) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := s.historicalIngest.MutateReview(jobID, mutation); err != nil {
		writeHistoricalReviewError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHomeHistoricalReviewComplete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	if !exactHomeFormFields(r, viewerSessionCSRFFormField, "expected_revision", "manifest_digest", "destination_store_id", "repository_id", "confirmation_digest") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	revision, err := strconv.ParseInt(r.PostFormValue("expected_revision"), 10, 64)
	if err != nil || revision < 1 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	bindingDigest, repositoryID, boundaryOK := s.cfg.Store.ProductRuntimeBoundary()
	if !boundaryOK || r.PostFormValue("destination_store_id") != s.cfg.Store.StoreID() || r.PostFormValue("repository_id") != repositoryID ||
		r.PostFormValue("confirmation_digest") != historicalReviewDestinationConfirmationDigest(chi.URLParam(r, "job"), revision, r.PostFormValue("manifest_digest"), s.cfg.Store.StoreID(), repositoryID, bindingDigest) {
		http.Error(w, "The historical review destination changed. Refresh Home.", http.StatusConflict)
		return
	}
	var unavailable map[string]bool
	if s.historicalEvidence == nil {
		snapshot, snapshotErr := s.historicalIngest.ReviewSnapshot(chi.URLParam(r, "job"), nil)
		if snapshotErr != nil {
			writeHistoricalReviewError(w, snapshotErr)
			return
		}
		unavailable = make(map[string]bool, len(snapshot.Items))
		for _, item := range snapshot.Items {
			unavailable[item.Item.CandidateID] = true
		}
	}
	if _, err := s.historicalIngest.CompleteReview(chi.URLParam(r, "job"), revision, r.PostFormValue("manifest_digest"), historicalingest.ReviewConfirmationDigest(chi.URLParam(r, "job"), revision, r.PostFormValue("manifest_digest")), unavailable); err != nil {
		writeHistoricalReviewError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func historicalReviewDestinationConfirmationDigest(jobID string, revision int64, manifestDigest, storeID, repositoryID, bindingDigest string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("pulse:historical-review-destination:v1:%s:%d:%s:%s:%s:%s", jobID, revision, manifestDigest, storeID, repositoryID, bindingDigest)))
	return hex.EncodeToString(digest[:])
}

func (s *Server) handleHomeHistoricalEvidence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.Fragment != "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if _, err := s.homeSessions.Authenticate(r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := s.verifyHomeBinding(r.Context()); err != nil {
		http.Error(w, "The project binding changed. Run pulse home again.", http.StatusConflict)
		return
	}
	if s.historicalEvidence == nil || s.historicalIngest == nil {
		http.Error(w, "Evidence is unavailable.", http.StatusServiceUnavailable)
		return
	}
	jobID := chi.URLParam(r, "job")
	unit, snapshot, err := s.historicalIngest.EvidenceUnitForCandidate(jobID, chi.URLParam(r, "candidate"))
	if err != nil {
		writeHistoricalReviewError(w, err)
		return
	}
	payload, err := s.historicalEvidence.LoadHistoricalIngestEvidence(r.Context(), unit)
	if err != nil || payload.Evidence == "" || digestHistoricalEvidence(payload.Evidence) != unit.EvidenceDigest {
		_, _ = s.historicalIngest.MarkStale(jobID, snapshot.Digest, "evidence_prefix_changed")
		http.Error(w, "Evidence changed or is unavailable. Refresh the history snapshot.", http.StatusConflict)
		return
	}
	evidence := payload.Evidence
	if len(evidence) > historicalEvidenceViewLimit {
		evidence = evidence[:historicalEvidenceViewLimit] + "\n… bounded by Pulse"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(evidence))
}

func (s *Server) handleHomeHistoricalEntityPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	if !exactHistoricalEntityFields(r, false) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	revision, err := strconv.ParseInt(r.PostFormValue("expected_revision"), 10, 64)
	if err != nil || revision < 1 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	preview, err := s.historicalIngest.PreviewEntityRewrite(
		chi.URLParam(r, "job"), revision, r.PostFormValue("expected_digest"), r.PostFormValue("mode"),
		r.PostFormValue("from_entity_id"), r.PostFormValue("to_entity_id"), historicalSelectedCandidates(r.PostFormValue("selected_candidates")),
	)
	if err != nil {
		writeHistoricalReviewError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, preview)
}

func (s *Server) handleHomeHistoricalEntityApply(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	if !exactHistoricalEntityFields(r, true) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	revision, err := strconv.ParseInt(r.PostFormValue("expected_revision"), 10, 64)
	if err != nil || revision < 1 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := s.historicalIngest.ApplyEntityRewrite(
		chi.URLParam(r, "job"), revision, r.PostFormValue("expected_digest"), r.PostFormValue("mode"),
		r.PostFormValue("from_entity_id"), r.PostFormValue("to_entity_id"), historicalSelectedCandidates(r.PostFormValue("selected_candidates")),
		r.PostFormValue("preview_digest"),
	); err != nil {
		writeHistoricalReviewError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func exactHistoricalReviewBase(r *http.Request, candidateID string) bool {
	if candidateID == "" {
		return false
	}
	action := r.PostFormValue("action")
	if action == "edit" {
		return true
	}
	return exactHomeFormFields(r, viewerSessionCSRFFormField, "expected_revision", "expected_digest", "action")
}

func historicalReviewItemByID(snapshot historicalingest.ReviewSnapshot, candidateID string) (historicalingest.MaterialItem, bool) {
	for _, item := range snapshot.Items {
		if item.Item.CandidateID == candidateID {
			return item.Item, true
		}
	}
	return historicalingest.MaterialItem{}, false
}

func historicalReplacementFromForm(item historicalingest.MaterialItem, r *http.Request) (historicalingest.MaterialItem, error) {
	text := strings.TrimSpace(r.PostFormValue("primary_text"))
	if text == "" || len(text) > 4000 {
		return historicalingest.MaterialItem{}, historicalingest.ErrReviewInvalid
	}
	switch item.Kind {
	case historicalingest.MaterialKindEvent:
		item.Payload.Summary = text
	case historicalingest.MaterialKindDecision, historicalingest.MaterialKindState, historicalingest.MaterialKindContinuity:
		item.Payload.Summary = text
	case historicalingest.MaterialKindAssertion:
		item.Payload.ObjectValue = text
	case historicalingest.MaterialKindPerson, historicalingest.MaterialKindProject:
		item.Payload.Name = text
	case historicalingest.MaterialKindRelation:
		item.Payload.Predicate = text
	}
	item.Scope.Kind = historicalingest.ScopeKind(r.PostFormValue("scope_kind"))
	item.Scope.ProjectID = strings.TrimSpace(r.PostFormValue("project_id"))
	if item.Scope.Kind != historicalingest.ScopeProject {
		item.Scope.ProjectID = ""
	}
	item.EpistemicStatus = historicalingest.EpistemicStatus(r.PostFormValue("epistemic_status"))
	from, err := time.Parse(time.RFC3339, r.PostFormValue("valid_from"))
	if err != nil {
		return historicalingest.MaterialItem{}, historicalingest.ErrReviewInvalid
	}
	item.ValidTime.From = from.UTC()
	if raw := r.PostFormValue("valid_to"); raw != "" {
		to, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return historicalingest.MaterialItem{}, historicalingest.ErrReviewInvalid
		}
		to = to.UTC()
		item.ValidTime.To = &to
	} else {
		item.ValidTime.To = nil
	}
	if item.Kind == historicalingest.MaterialKindContinuity {
		item.Payload.ContinuityStatus = r.PostFormValue("continuity_status")
	}
	if err := item.Validate(); err != nil {
		return historicalingest.MaterialItem{}, historicalingest.ErrReviewInvalid
	}
	return item, nil
}

func exactHistoricalEditFields(r *http.Request) bool {
	names := []string{viewerSessionCSRFFormField, "expected_revision", "expected_digest", "action", "primary_text", "scope_kind", "project_id", "valid_from", "valid_to", "epistemic_status", "continuity_status"}
	if r == nil || len(r.PostForm) != len(names) {
		return false
	}
	for _, name := range names {
		if values := r.PostForm[name]; len(values) != 1 {
			return false
		}
	}
	return r.PostFormValue(viewerSessionCSRFFormField) != "" && r.PostFormValue("expected_revision") != "" &&
		r.PostFormValue("expected_digest") != "" && r.PostFormValue("action") == "edit" &&
		r.PostFormValue("primary_text") != "" && r.PostFormValue("scope_kind") != "" &&
		r.PostFormValue("valid_from") != "" && r.PostFormValue("epistemic_status") != ""
}

func exactHistoricalEntityFields(r *http.Request, apply bool) bool {
	names := []string{viewerSessionCSRFFormField, "expected_revision", "expected_digest", "mode", "from_entity_id", "to_entity_id", "selected_candidates"}
	if apply {
		names = append(names, "preview_digest")
	}
	if r == nil || len(r.PostForm) != len(names) {
		return false
	}
	for _, name := range names {
		if values := r.PostForm[name]; len(values) != 1 {
			return false
		}
	}
	return r.PostFormValue(viewerSessionCSRFFormField) != "" && r.PostFormValue("expected_revision") != "" &&
		r.PostFormValue("expected_digest") != "" && r.PostFormValue("mode") != "" &&
		r.PostFormValue("from_entity_id") != "" && r.PostFormValue("to_entity_id") != "" &&
		(!apply || r.PostFormValue("preview_digest") != "")
}

func historicalSelectedCandidates(value string) []string {
	var result []string
	for _, candidate := range strings.Split(value, ",") {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			result = append(result, candidate)
		}
	}
	return result
}

func writeHistoricalReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, historicalingest.ErrReviewVersionConflict):
		http.Error(w, "The historical review changed. Refresh Home.", http.StatusConflict)
	case errors.Is(err, historicalingest.ErrReviewIncomplete):
		http.Error(w, "Blocking candidates still need a decision. Refresh Home.", http.StatusConflict)
	case errors.Is(err, historicalingest.ErrReviewInvalid):
		http.Error(w, "The historical review change is invalid.", http.StatusBadRequest)
	case errors.Is(err, historicalingest.ErrIngestJobNotFound):
		http.Error(w, "Historical review not found.", http.StatusNotFound)
	default:
		http.Error(w, "Historical review is unavailable.", http.StatusServiceUnavailable)
	}
}
