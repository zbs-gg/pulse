package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/consolidation"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/unassigned"
	"github.com/nkkmnk/pulse/internal/userpresence"
)

type verifiedMemoryHomeDeliveryReader struct {
	store     *store.Store
	authority productBindingAuthority
}

func (reader verifiedMemoryHomeDeliveryReader) ReadMemoryHomeDeliveryFacts(
	repositoryID, bindingDigest string,
	limit int,
) ([]store.MemoryHomeDeliveryFact, error) {
	if repositoryID != reader.authority.RepositoryID || bindingDigest != reader.authority.BindingDigest {
		return nil, store.ErrContinuityDeliveryAuthority
	}
	return reader.store.ReadMemoryHomeDeliveryFactsForVerifiedBinding(repositoryID, bindingDigest, limit)
}

func (s *Server) buildMemoryHome(
	now time.Time,
	snapshot personalLiveReadinessSnapshot,
) (store.MemoryHomeData, error) {
	return s.buildMemoryHomeFiltered(now, snapshot, store.MemoryHomeFilter{})
}

func (s *Server) buildMemoryHomeFiltered(
	now time.Time,
	snapshot personalLiveReadinessSnapshot,
	filter store.MemoryHomeFilter,
) (store.MemoryHomeData, error) {
	return s.buildMemoryHomeFilteredForAuthority(now, snapshot, filter, nil)
}

func (s *Server) buildMemoryHomeFilteredForAuthority(
	now time.Time,
	snapshot personalLiveReadinessSnapshot,
	filter store.MemoryHomeFilter,
	authority *productBindingAuthority,
) (store.MemoryHomeData, error) {
	if s == nil || s.cfg.Store == nil {
		return store.MemoryHomeData{}, fmt.Errorf("Memory Home store is unavailable")
	}
	if err := validatePersonalLiveReadiness(snapshot); err != nil {
		return store.MemoryHomeData{}, fmt.Errorf("Memory Home readiness is invalid")
	}
	var bindingDigest, repositoryID string
	var scope store.PersonalMemoryScopeSnapshot
	requestScoped := authority != nil
	if requestScoped {
		bindingDigest, repositoryID = authority.BindingDigest, authority.RepositoryID
		var err error
		scope, err = s.cfg.Store.PersonalMemoryScopeSnapshotForBinding(bindingDigest, repositoryID)
		if err != nil {
			return store.MemoryHomeData{}, fmt.Errorf("Memory Home product boundary is unavailable")
		}
	} else {
		var ok bool
		bindingDigest, repositoryID, ok = s.cfg.Store.ProductRuntimeBoundary()
		if !ok {
			return store.MemoryHomeData{}, fmt.Errorf("Memory Home product boundary is unavailable")
		}
	}
	resumeQuery := store.ResumeQuery{ThreadID: repositoryID, TokenBudget: 1200}
	var resume store.ResumeBlock
	var err error
	if requestScoped {
		resume, err = s.cfg.Store.BuildResumeForPersonalScope(resumeQuery, scope)
	} else {
		resume, err = s.cfg.Store.BuildResume(resumeQuery)
	}
	if err != nil {
		return store.MemoryHomeData{}, err
	}
	objectIDs := append([]string(nil), resume.IncludedObjectIDs...)
	evidenceIDs := append([]string(nil), resume.IncludedEvidenceIDs...)
	sort.Strings(objectIDs)
	sort.Strings(evidenceIDs)
	digest := sha256.Sum256([]byte(resume.ResumeMarkdown))
	redactedResume := memoryHomeRedactedResume(resume)
	renderedBytes := len(redactedResume)
	preview := &store.MemoryHomeNextTaskPreview{
		Status: "preview_only", PayloadDigest: fmt.Sprintf("%x", digest[:]),
		ObjectIDs: objectIDs, EvidenceIDs: evidenceIDs, RedactedResume: redactedResume,
		MethodID: store.MemoryHomeCountMethodUTF8BytesDiv4Ceil, MethodVersion: "1",
		RenderedBytes: renderedBytes, PulseTokens: (renderedBytes + 3) / 4,
	}
	liveReadiness, checkedAt := s.memoryHomeLiveReadiness(snapshot, now)
	homeQuery := store.MemoryHomeQuery{
		RepositoryID: repositoryID, BindingDigest: bindingDigest, GeneratedAt: now.UTC(),
		LiveReadiness:   liveReadiness,
		NextTaskPreview: preview,
		Filter:          filter,
	}
	var data store.MemoryHomeData
	if requestScoped {
		data, err = s.cfg.Store.BuildMemoryHomeDataForPersonalScope(
			homeQuery, scope,
			verifiedMemoryHomeDeliveryReader{store: s.cfg.Store, authority: *authority},
		)
	} else {
		data, err = s.cfg.Store.BuildMemoryHomeData(homeQuery, s.cfg.Store)
	}
	if err != nil {
		return store.MemoryHomeData{}, err
	}
	if data.Readiness.ReasonCode == liveReadiness.ReasonCode {
		data.Readiness.CheckedAt = checkedAt
	}
	if s.consolidationReports != nil {
		destination := consolidation.Destination{
			StoreKind: data.Boundary.StoreKind, StoreID: data.Boundary.StoreID,
			BindingDigest: data.Boundary.BindingDigest, RepositoryID: data.Boundary.RepositoryID,
		}
		report, reportErr := s.consolidationReports.Latest(destination)
		if reportErr == nil {
			if s.consolidationInventory != nil {
				report, reportErr = s.consolidationInventory.EnsureFresh(report.InvocationID)
			}
			if reportErr == nil {
				data.Consolidation = &report
			}
		}
		if reportErr != nil && !errors.Is(reportErr, consolidation.ErrReportNotFound) {
			return store.MemoryHomeData{}, reportErr
		}
	}
	return data, nil
}

func (s *Server) memoryHomeLiveReadiness(
	snapshot personalLiveReadinessSnapshot,
	now time.Time,
) (store.MemoryHomeLiveReadiness, string) {
	if snapshot.Outcome != store.MemoryHomeReadinessReady {
		return store.MemoryHomeLiveReadiness{
			Outcome: snapshot.Outcome, ReasonCode: snapshot.ReasonCode, NextAction: snapshot.NextAction,
		}, snapshot.CheckedAt
	}
	checkedAt := now.UTC().Format(time.RFC3339Nano)
	if s.cfg.Retrieval == nil {
		downgraded := personalLiveReadinessForReason("full_retrieval_unavailable", checkedAt)
		return store.MemoryHomeLiveReadiness{
			Outcome: downgraded.Outcome, ReasonCode: downgraded.ReasonCode, NextAction: downgraded.NextAction,
		}, checkedAt
	}
	if !s.cfg.Retrieval.EmbedderReady() {
		downgraded := personalLiveReadinessForReason("local_embedder_warming", checkedAt)
		return store.MemoryHomeLiveReadiness{
			Outcome: downgraded.Outcome, ReasonCode: downgraded.ReasonCode, NextAction: downgraded.NextAction,
		}, checkedAt
	}
	return store.MemoryHomeLiveReadiness{
		Outcome: snapshot.Outcome, ReasonCode: snapshot.ReasonCode, NextAction: snapshot.NextAction,
	}, snapshot.CheckedAt
}

func memoryHomeRedactedResume(resume store.ResumeBlock) string {
	lines := []string{"Pulse next-task preview"}
	appendSection := func(label string, values []string, limit int) {
		if len(values) == 0 || limit < 1 {
			return
		}
		section := make([]string, 0, limit)
		for index, value := range values {
			if index >= limit {
				break
			}
			if value = strings.TrimSpace(value); value != "" {
				section = append(section, value)
			}
		}
		if len(section) > 0 {
			lines = append(lines, label+": "+strings.Join(section, "; "))
		}
	}
	appendSection("Where we left off", resume.Sections.WhereWeLeftOff, 2)
	appendSection("Active decisions", resume.Sections.ActiveDecisions, 3)
	appendSection("Open loops", resume.Sections.OpenLoops, 3)
	appendSection("Do not repeat", resume.Sections.DoNotRepeat, 2)
	appendSection("Suggested next step", resume.Sections.SuggestedNextStep, 1)
	return strings.Join(lines, " · ")
}

type memoryHomePendingCard struct {
	CandidateID   string
	Version       int
	Kind          string
	Summary       string
	CandidateJSON string
}

type memoryHomeUnassignedCard struct {
	ItemID        string
	ContentDigest string
	CreatedAt     string
	Host          string
	Kind          string
	Summary       string
}

type memoryHomeUnassignedActivity struct {
	ReceiptID     string
	ActionLabel   string
	Status        string
	CreatedAt     string
	ContentDigest string
}

type memoryHomePage struct {
	Data                    store.MemoryHomeData
	Pending                 []memoryHomePendingCard
	Historical              memoryHomeHistoricalReview
	EnhancedPresenceProfile userpresence.EnhancedPresenceProfile
	UnassignedEnabled       bool
	UnassignedUnavailable   bool
	Unassigned              []memoryHomeUnassignedCard
	UnassignedActivity      []memoryHomeUnassignedActivity
	CSRFToken               string
	HostActivity            []memoryHomeHostActivityCard
	Storage                 memoryHomeStorageCard
}

type memoryHomeHostActivityCard struct {
	HostLabel  string
	State      string
	StateLabel string
	Recall     string
	Write      string
}

type memoryHomeStorageSnapshot struct {
	Schema                string `json:"schema"`
	GeneratedAt           string `json:"generated_at"`
	TotalBytes            int64  `json:"total_bytes"`
	ProtectedReleaseBytes int64  `json:"protected_release_bytes"`
	ReclaimableBytes      int64  `json:"reclaimable_bytes"`
	SkippedBytes          int64  `json:"skipped_bytes"`
	ActiveEpoch           int    `json:"active_epoch"`
	PreviousEpoch         int    `json:"previous_epoch"`
	Archive               *struct {
		Path       string `json:"path"`
		VerifiedAt string `json:"verified_at"`
		SHA256     string `json:"sha256"`
	} `json:"archive,omitempty"`
}

type memoryHomeStorageCard struct {
	Available    bool
	NeedsCleanup bool
	Total        string
	Reclaimable  string
	Release      string
	Archive      string
}

type memoryHomeTemplateData struct {
	memoryHomePage
	StoreLabel               string
	StatusTitle              string
	StatusDetail             string
	StatusTone               string
	MemoryCount              string
	ContextState             string
	ContextHost              string
	EconomyValue             string
	EconomyDetail            string
	HasLatestMemory          bool
	HasEmotionalMemory       bool
	HasNextPreview           bool
	HasPending               bool
	HasUnassigned            bool
	HasUnassignedActivity    bool
	HasAttempts              bool
	HasContext               bool
	HasProtectedActions      bool
	CanProtectedWipe         bool
	EstimatedPercent         string
	Attempts                 []store.MemoryHomeAttempt
	HasConsolidation         bool
	ConsolidationCanonical   []consolidation.Source
	ConsolidationImports     []consolidation.Source
	ConsolidationArtifacts   []consolidation.Source
	ConsolidationSourceCount int
	ConsolidationAction      string
	ConsolidationActionLabel string
	ConsolidationHealthLabel string
	HasMemoryFilter          bool
	HasMoreMemories          bool
	HasPreviousMemories      bool
	NextMemoryOffset         int
	PreviousMemoryOffset     int
}

func memoryHomeHostActivityCards(value supportedHostLifecycleReadiness) []memoryHomeHostActivityCard {
	cards := make([]memoryHomeHostActivityCard, 0, 2)
	for _, host := range value.Hosts {
		if host.Host != "codex" && host.Host != "claude-code" {
			continue
		}
		card := memoryHomeHostActivityCard{HostLabel: map[string]string{
			"codex": "Codex", "claude-code": "Claude Code",
		}[host.Host]}
		if host.LifecycleReady {
			card.State, card.StateLabel = "ready", "Automatic memory observed"
		} else if host.LastWriteAt != "" || host.LastRecallAt != "" {
			card.State, card.StateLabel = "partial", "Connected; one proof is still missing"
		} else {
			card.State, card.StateLabel = "action", "Connected; no recent activity receipt"
		}
		if host.LastRecallAt == "" {
			card.Recall = "No automatic recall recorded yet"
		} else if host.RecallCount == 0 {
			card.Recall = "Last search found no relevant memory · " + host.LastRecallAt
		} else {
			proof := "Pulse delivered memory; model use is not observable"
			if host.DeliveryProof == "host_observed" {
				proof = "Host confirmed the delivered memory"
			}
			card.Recall = fmt.Sprintf("Last recall: %d item(s) · %s · %s", host.RecallCount, proof, host.LastRecallAt)
		}
		if host.LastWriteAt == "" {
			card.Write = "No durable write recorded yet"
		} else {
			card.Write = "Last durable write · " + host.LastWriteAt
		}
		cards = append(cards, card)
	}
	return cards
}

func memoryHomeBytes(bytes int64) string {
	if bytes < 0 {
		return "Unavailable"
	}
	value := float64(bytes)
	units := []string{"B", "KB", "MB", "GB", "TB"}
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if value >= 10 || unit == 0 {
		return fmt.Sprintf("%.0f %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func readMemoryHomeStorageCard(path string) memoryHomeStorageCard {
	if path == "" {
		return memoryHomeStorageCard{}
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || len(raw) > 16<<10 {
		return memoryHomeStorageCard{}
	}
	var value memoryHomeStorageSnapshot
	if json.Unmarshal(raw, &value) != nil || value.Schema != "pulse.storage_home.v1" ||
		value.TotalBytes < 0 || value.ProtectedReleaseBytes < 0 || value.ReclaimableBytes < 0 ||
		value.ActiveEpoch < 1 || value.PreviousEpoch < 1 {
		return memoryHomeStorageCard{}
	}
	archive := "No verified external archive recorded"
	if value.Archive != nil && value.Archive.Path != "" && value.Archive.VerifiedAt != "" {
		archive = "Verified archive: " + value.Archive.Path + " · " + value.Archive.VerifiedAt
	}
	return memoryHomeStorageCard{
		Available: true, NeedsCleanup: value.ReclaimableBytes > 0, Total: memoryHomeBytes(value.TotalBytes),
		Reclaimable: memoryHomeBytes(value.ReclaimableBytes),
		Release:     fmt.Sprintf("Active epoch %d · rollback epoch %d · %s protected", value.ActiveEpoch, value.PreviousEpoch, memoryHomeBytes(value.ProtectedReleaseBytes)),
		Archive:     archive,
	}
}

func renderMemoryHomeHTML(page memoryHomePage) (string, error) {
	if page.EnhancedPresenceProfile.Schema == "" {
		page.EnhancedPresenceProfile = userpresence.NewUnavailableAuthorizer("enhanced_presence_unavailable").Profile()
	}
	attempts := memoryHomeBoundedAttempts(page.Data.Receipts.Attempts)
	view := memoryHomeTemplateData{
		memoryHomePage:        page,
		StoreLabel:            memoryHomeStoreLabel(page.Data.Boundary.StoreKind),
		MemoryCount:           fmt.Sprintf("%d", page.Data.Memories.ActiveCount),
		HasLatestMemory:       len(page.Data.Memories.LatestActive) > 0,
		HasEmotionalMemory:    len(page.Data.EmotionalMemory) > 0,
		HasNextPreview:        page.Data.NextTaskPreview != nil,
		HasPending:            len(page.Pending) > 0,
		HasUnassigned:         len(page.Unassigned) > 0,
		HasUnassignedActivity: len(page.UnassignedActivity) > 0,
		HasAttempts:           len(attempts) > 0,
		HasContext:            page.Data.Context.LatestDelivery != nil,
		HasProtectedActions:   profileHasValidProtectedActions(page.EnhancedPresenceProfile),
		CanProtectedWipe:      profileAuthorizes(page.EnhancedPresenceProfile, userpresence.ActionVaultWipe),
		Attempts:              attempts,
		HasMemoryFilter:       memoryHomeFilterActive(page.Data.Filter),
	}
	pageSize := page.Data.Filter.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	view.HasMoreMemories = page.Data.Filter.PageOffset+len(page.Data.Memories.LatestActive) < page.Data.Memories.ActiveCount
	view.HasPreviousMemories = page.Data.Filter.PageOffset > 0
	view.NextMemoryOffset = page.Data.Filter.PageOffset + pageSize
	view.PreviousMemoryOffset = max(0, page.Data.Filter.PageOffset-pageSize)
	if page.Data.Consolidation != nil {
		view.HasConsolidation = true
		view.ConsolidationCanonical, view.ConsolidationImports, view.ConsolidationArtifacts =
			memoryHomeConsolidationGroups(page.Data.Consolidation.Sources, 24)
		view.ConsolidationSourceCount = len(page.Data.Consolidation.Sources)
		view.ConsolidationHealthLabel = strings.ReplaceAll(string(page.Data.Consolidation.Phase), "_", " ")
		switch page.Data.Consolidation.Phase {
		case consolidation.PhaseCanceled:
			view.ConsolidationAction, view.ConsolidationActionLabel = "resume", "Resume this report"
		case consolidation.PhasePlanned, consolidation.PhaseInventory, consolidation.PhaseDeterministicDedupe,
			consolidation.PhaseCancelRequested:
			view.ConsolidationAction, view.ConsolidationActionLabel = "cancel", "Cancel this scan"
		case consolidation.PhasePartial, consolidation.PhaseStale:
			view.ConsolidationAction, view.ConsolidationActionLabel = "start", "Start a fresh report"
		}
	}
	view.StatusTitle, view.StatusDetail, view.StatusTone = memoryHomeReadinessCopy(page.Data.Readiness)
	view.ContextState = memoryHomeContextCopy(page.Data.Context)
	if page.Data.Context.LatestDelivery != nil {
		view.ContextHost = page.Data.Context.LatestDelivery.Host
	}
	view.EconomyValue, view.EconomyDetail, view.EstimatedPercent = memoryHomeEconomyCopy(page.Data.Economy)
	var output bytes.Buffer
	if err := memoryHomeTemplate.Execute(&output, view); err != nil {
		return "", err
	}
	return output.String(), nil
}

func memoryHomeFilterActive(filter store.MemoryHomeFilter) bool {
	return filter.Text != "" || filter.Project != "" || filter.Harness != "" ||
		filter.DateFrom != "" || filter.DateTo != "" || filter.Scope != ""
}

func memoryHomeConsolidationGroups(
	sources []consolidation.Source,
	limit int,
) (canonical, imports, artifacts []consolidation.Source) {
	for _, source := range sources {
		if len(canonical)+len(imports)+len(artifacts) >= limit {
			break
		}
		switch source.Classification {
		case consolidation.ClassificationCanonicalVault:
			canonical = append(canonical, source)
		case consolidation.ClassificationCache, consolidation.ClassificationBackup,
			consolidation.ClassificationReleaseArtifact, consolidation.ClassificationCodeCheckout:
			artifacts = append(artifacts, source)
		default:
			imports = append(imports, source)
		}
	}
	return canonical, imports, artifacts
}

func profileHasValidProtectedActions(profile userpresence.EnhancedPresenceProfile) bool {
	return profileAuthorizes(profile, userpresence.ActionBindingChange) ||
		profileAuthorizes(profile, userpresence.ActionVaultWipe)
}

func memoryHomeBoundedAttempts(attempts []store.MemoryHomeAttempt) []store.MemoryHomeAttempt {
	const limit = 10
	if len(attempts) > limit {
		attempts = attempts[:limit]
	}
	result := append([]store.MemoryHomeAttempt(nil), attempts...)
	for index := range result {
		const summaryLimit = 280
		runes := []rune(result[index].RedactedSummary)
		if len(runes) > summaryLimit {
			result[index].RedactedSummary = string(runes[:summaryLimit-1]) + "…"
		}
	}
	return result
}

func memoryHomeStoreLabel(kind string) string {
	switch store.StoreKind(kind) {
	case store.StoreKindPersonal:
		return "Personal memory"
	default:
		return "Local memory"
	}
}

func memoryHomeReadinessCopy(readiness store.MemoryHomeReadinessSnapshot) (title, detail, tone string) {
	switch readiness.ReasonCode {
	case "memory_continuity_ready":
		return "Pulse is remembering across tasks", "Canonical memory was offered to a fresh task and the host confirmed receipt.", "ready"
	case "host_observation_required":
		return "Pulse is remembering, but the harness has not confirmed receipt yet", "The context offer is recorded. Pulse will not call this fully ready without trustworthy host evidence.", "partial"
	case "full_retrieval_unavailable":
		return "Full retrieval is not enabled", "The local private store is available with fallback keyword recall. Pulse retrieval is not being claimed.", "partial"
	case "local_embedder_warming":
		return "Pulse is warming up", "The local memory engine is starting and no completion is being claimed yet.", "warming"
	case "codex_plugin_unavailable":
		return "Pulse Codex plugin needs repair", "Doctor could not verify the exact Pulse plugin generation. Home preserves that result.", "action"
	case "codex_hook_lifecycle_required":
		return "Pulse needs one verified Codex lifecycle", "Doctor has not observed one complete same-task lifecycle yet.", "action"
	case "codex_hook_trust_required":
		return "Pulse hooks need trust", "Codex has not trusted the exact Pulse hook bundle yet.", "action"
	case "codex_native_lifecycle_attestation_unavailable":
		return "Native lifecycle evidence is unavailable", "Use the explicit Pulse MCP tools until Codex exposes replayable lifecycle evidence.", "action"
	case "presence_required":
		return "Pulse presence trust needs repair", "Doctor could not verify the local OS presence helper.", "action"
	case "binding_repair_required":
		return "Pulse workspace binding needs repair", "Doctor could not verify this workspace's exact product binding.", "action"
	case "codex_activation_incomplete":
		return "Pulse Codex activation is incomplete", "Doctor could not verify the exact runtime and daemon activation pair.", "action"
	case "daemon_unavailable":
		return "Pulse daemon is unavailable", "Doctor could not verify the bound local vault through the live daemon.", "action"
	case "first_memory_required":
		return "Pulse is connected", "The local private store is available. Save the first structured memory to begin continuity proof.", "action"
	case "fresh_task_required":
		return "A fresh task is needed", "Canonical memory is available. Open a fresh task so Pulse can record an exact context offer.", "action"
	}
	switch readiness.Outcome {
	case store.MemoryHomeReadinessReady:
		return "Pulse retrieval is ready", "The live memory engine is ready. Cross-task continuity is shown only when its receipt proof is available.", "ready"
	case store.MemoryHomeReadinessPartial:
		return "Pulse needs one more proof", "The live check is partial. Follow the next action; no context delivery or host acknowledgement is being implied.", "partial"
	case store.MemoryHomeReadinessBlocked:
		return "Pulse needs attention", "The live product check is blocked. The action below is the shortest honest recovery path.", "blocked"
	case store.MemoryHomeReadinessWarming:
		return "Pulse is warming up", "The local memory engine is starting and no completion is being claimed yet.", "warming"
	default:
		return "Pulse is connected", "The local private store is available. Complete the next action to prove cross-task continuity.", "action"
	}
}

func memoryHomeContextCopy(context store.MemoryHomeContext) string {
	if context.LatestDelivery == nil {
		return "No context offer yet"
	}
	if context.LatestDelivery.Acknowledgement == store.MemoryHomeDeliveryHostObserved {
		return "Context observed"
	}
	return "Context offered"
}

func memoryHomeEconomyCopy(economy store.MemoryHomeEconomy) (value, detail, percent string) {
	switch economy.State {
	case store.MemoryHomeEconomyMeasured:
		if economy.MeasuredAvoidedTokens != nil {
			return fmt.Sprintf("%d", *economy.MeasuredAvoidedTokens), "Measured avoided input tokens", ""
		}
	case store.MemoryHomeEconomyEstimated:
		if economy.EstimatedAvoidedTokens != nil {
			return fmt.Sprintf("%d", *economy.EstimatedAvoidedTokens), "Estimated avoided tokens from comparable receipts", memoryHomePercent(economy.EstimatedReductionPercent)
		}
	case store.MemoryHomeEconomyUnavailable:
		return "Unavailable", "Receipts use incomparable methods; Pulse will not mix them.", ""
	case store.MemoryHomeEconomyCollectingBaseline:
		if offer := economy.LatestOffer; offer != nil && offer.PulseTokens > 0 && offer.RenderedBytes > 0 &&
			offer.MethodID != "" && offer.MethodVersion != "" {
			return "No estimate yet", fmt.Sprintf(
				"Latest offer: %d Pulse tokens · %d rendered bytes · %s v%s · Collecting a comparable baseline",
				offer.PulseTokens, offer.RenderedBytes, offer.MethodID, offer.MethodVersion,
			), ""
		}
	}
	return "No estimate yet", "Collecting a comparable baseline", ""
}

func memoryHomePercent(value *float64) string {
	if value == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", *value), "0"), ".") + "%"
}

func memoryHomePendingCards(candidates []store.MemoryTrayPendingCandidate) ([]memoryHomePendingCard, error) {
	cards := make([]memoryHomePendingCard, 0, len(candidates))
	for _, candidate := range candidates {
		kind, summary := memoryHomeCandidateDisplay(candidate.Candidate)
		encoded, err := json.MarshalIndent(candidate.Candidate, "", "  ")
		if err != nil {
			return nil, err
		}
		cards = append(cards, memoryHomePendingCard{
			CandidateID: candidate.CandidateID, Version: candidate.Version,
			Kind: kind, Summary: summary, CandidateJSON: string(encoded),
		})
	}
	return cards, nil
}

func memoryHomeUnassignedCards(cards []unassigned.Card) []memoryHomeUnassignedCard {
	result := make([]memoryHomeUnassignedCard, 0, len(cards))
	for _, card := range cards {
		result = append(result, memoryHomeUnassignedCard{
			ItemID: card.ItemID, ContentDigest: card.ContentDigest, CreatedAt: card.CreatedAt,
			Host: card.Host, Kind: card.Kind, Summary: card.Summary,
		})
	}
	return result
}

func memoryHomeUnassignedActivities(
	activity []unassigned.Activity,
	currentBindingDigest string,
) []memoryHomeUnassignedActivity {
	result := make([]memoryHomeUnassignedActivity, 0, len(activity))
	for _, value := range activity {
		label := "Deleted from Inbox"
		if value.Action == "assign" {
			label = "Saved to this project"
			if value.BindingDigest != "" && value.BindingDigest != currentBindingDigest {
				label = "Saved to another project"
			}
		}
		result = append(result, memoryHomeUnassignedActivity{
			ReceiptID: value.ReceiptID, ActionLabel: label, Status: value.Status,
			CreatedAt: value.CreatedAt, ContentDigest: value.ContentDigest,
		})
	}
	return result
}

func memoryHomeCandidateDisplay(candidate store.PrivateMemoryCandidate) (kind, summary string) {
	if candidate.Capsule != nil && len(candidate.Capsule.Items) == 1 {
		return candidate.Capsule.Items[0].Kind, candidate.Capsule.Items[0].RedactedSummary
	}
	if delta := candidate.SemanticDelta; delta != nil {
		if delta.Continuity != nil && delta.Continuity.Summary != "" {
			return "continuity", delta.Continuity.Summary
		}
		if len(delta.Events) > 0 {
			if delta.Events[0].Summary != "" {
				return "event", delta.Events[0].Summary
			}
			return "event", delta.Events[0].Title
		}
		if len(delta.Facts) > 0 {
			return "fact", delta.Facts[0].Text
		}
		if len(delta.Nodes) > 0 {
			return delta.Nodes[0].Kind, delta.Nodes[0].Summary
		}
	}
	return "memory", "Structured memory awaiting review"
}

var memoryHomeTemplate = template.Must(template.New("memory-home").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="data:,">
  <title>Pulse Memory Home</title>
  <style>
    :root { color-scheme:light; --bg:#f5f3ee; --paper:#fffdfa; --ink:#1d211f; --muted:#6b716c; --line:#deddd7; --green:#2f694f; --green-soft:#e4efe7; --amber:#8a6428; --amber-soft:#f6ecd8; --red:#8b4646; --red-soft:#f4e2e0; --blue:#355f76; --blue-soft:#e4eef3; font:15px/1.5 Inter,ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }
    * { box-sizing:border-box; } [hidden] { display:none!important; }
    body { margin:0; color:var(--ink); background:var(--bg); overflow-x:hidden; }
    main { width:min(1120px,100%); min-width:0; margin:0 auto; padding:22px clamp(18px,4vw,52px) 72px; }
    h1,h2,h3,p { margin:0; } h1 { font-size:clamp(28px,4vw,34px); line-height:1.08; letter-spacing:-.035em; max-width:780px; } h2 { font-size:20px; } h3 { font-size:16px; }
    p { color:var(--muted); } code,pre,textarea { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; }
    .topbar { display:flex; align-items:center; justify-content:space-between; gap:16px; margin-bottom:18px; }
    .brand { display:flex; align-items:center; gap:10px; font-weight:750; letter-spacing:-.02em; }
    .pulse-dot { width:10px; height:10px; border-radius:50%; background:var(--green); box-shadow:0 0 0 6px var(--green-soft); }
    .boundary { display:flex; flex-wrap:wrap; gap:8px; color:var(--muted); font-size:13px; }
    .pill { padding:5px 9px; border:1px solid var(--line); border-radius:999px; background:rgba(255,255,255,.58); }
    .app-nav { display:flex; align-items:center; gap:4px; margin-right:auto; }
    .app-nav a { min-height:38px; padding:8px 12px; border-radius:999px; color:var(--muted); text-decoration:none; }
    .app-nav a[aria-current="page"] { color:var(--ink); background:var(--paper); box-shadow:inset 0 0 0 1px var(--line); }
    .status-banner { display:flex; align-items:flex-start; justify-content:space-between; gap:20px; margin:0 0 22px; padding:12px 15px; border:1px solid var(--line); border-left-width:4px; border-radius:14px; background:var(--paper); }
    .status-banner p { font-size:13px; } .status-banner strong { display:block; margin-bottom:2px; }
    .feed { margin-top:0; }
    .feed-head { display:flex; align-items:end; justify-content:space-between; gap:18px; margin-bottom:14px; }
    .feed-head p { margin-top:4px; }
    .feed-count { flex:0 0 auto; color:var(--muted); font-size:13px; }
    .hero { display:grid; grid-template-columns:minmax(0,1.55fr) minmax(260px,.75fr); gap:26px; align-items:end; padding-bottom:34px; border-bottom:1px solid var(--line); }
    .eyebrow { margin-bottom:14px; color:var(--green); font-weight:700; text-transform:uppercase; letter-spacing:.08em; font-size:12px; }
    .hero-copy { margin-top:17px; max-width:680px; font-size:17px; }
    .action { padding:20px; border-radius:18px; background:var(--paper); border:1px solid var(--line); }
    .action strong { display:block; margin-bottom:6px; }
    .status-ready { border-left:5px solid var(--green); } .status-partial,.status-warming { border-left:5px solid var(--amber); } .status-blocked { border-left:5px solid var(--red); } .status-action { border-left:5px solid var(--blue); }
    .activity-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; margin:22px 0 34px; }
    .activity-card { min-width:0; padding:18px; border:1px solid var(--line); border-left-width:5px; border-radius:16px; background:var(--paper); }
    .activity-card strong { display:block; margin-bottom:5px; } .activity-card p { margin-top:7px; font-size:13px; overflow-wrap:anywhere; }
    .metrics { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:1px; margin:30px 0 46px; background:var(--line); border:1px solid var(--line); border-radius:18px; overflow:hidden; }
    .metric { min-height:142px; padding:22px; background:var(--paper); } .metric .value { display:block; margin:11px 0 5px; font-size:34px; line-height:1; font-weight:750; letter-spacing:-.04em; } .metric small { color:var(--muted); }
    section { margin-top:38px; } .section-head { display:flex; justify-content:space-between; gap:18px; align-items:end; margin-bottom:16px; } .section-head p { max-width:590px; }
    .tray { display:grid; gap:14px; } .tray-card { padding:22px; border:1px solid #dbcba8; border-radius:18px; background:#fffaf0; }
    .tray-card header { display:flex; justify-content:space-between; gap:12px; margin-bottom:14px; } .kind { color:var(--amber); font-weight:700; text-transform:uppercase; font-size:12px; letter-spacing:.07em; }
    .summary { font-size:18px; color:var(--ink); }
    details { margin-top:12px; } summary { cursor:pointer; color:var(--muted); } textarea { width:100%; min-height:120px; margin-top:10px; padding:12px; border:1px solid var(--line); border-radius:12px; background:white; resize:vertical; }
    .controls { display:flex; flex-wrap:wrap; gap:9px; margin-top:12px; } button { min-height:38px; padding:8px 13px; border:1px solid var(--line); border-radius:999px; background:white; color:var(--ink); cursor:pointer; } button.primary { background:var(--ink); color:white; border-color:var(--ink); } button.danger { background:var(--red); color:white; border-color:var(--red); } button:disabled { cursor:not-allowed; opacity:.58; }
    .filters { display:grid; grid-template-columns:minmax(220px,2fr) repeat(5,minmax(130px,1fr)); gap:8px; align-items:end; margin:14px 0 16px; }
    .filter-field { display:grid; gap:5px; color:var(--muted); font-size:12px; } .filter-field input,.filter-field select { min-height:40px; padding:8px 10px; border:1px solid var(--line); border-radius:10px; background:var(--paper); color:var(--ink); }
    .filter-actions,.pagination { display:flex; gap:8px; align-items:center; flex-wrap:wrap; } .pagination { margin-top:14px; }
    .memory-list { display:grid; grid-template-columns:minmax(0,820px); gap:10px; } .memory { padding:16px; border:1px solid var(--line); border-radius:16px; background:var(--paper); } .memory .meta { display:flex; justify-content:space-between; gap:12px; margin-bottom:8px; color:var(--muted); font-size:12px; }
    .memory .summary { font-size:16px; line-height:1.45; }
    .memory-actions { display:flex; flex-wrap:wrap; align-items:flex-start; gap:8px; margin-top:10px; }
    .memory-actions details,.memory-actions form { margin:0; }
    .memory-actions summary { min-height:38px; padding:8px 13px; border:1px solid var(--line); border-radius:999px; background:white; color:var(--ink); list-style:none; }
    .memory-actions summary::-webkit-details-marker { display:none; }
    .memory-editor { width:min(680px,calc(100vw - 72px)); padding-top:2px; }
    .technical { margin-top:10px; font-size:12px; }
    .secondary-details { margin-top:38px; padding-top:18px; border-top:1px solid var(--line); }
    .secondary-details > summary { font-weight:700; color:var(--ink); }
    .secondary-body { padding-top:4px; }
    .project-card { max-width:820px; padding:18px; border:1px solid var(--line); border-radius:16px; background:var(--paper); }
    .project-card .boundary { margin-top:10px; }
    .preview { min-width:0; padding:22px; border:1px solid var(--line); border-radius:18px; background:var(--paper); } .preview pre { white-space:pre-wrap; overflow-wrap:anywhere; color:#3b413d; } .preview p,.preview code { overflow-wrap:anywhere; }
    .authority-actions { display:flex; flex-wrap:wrap; gap:8px; margin-top:14px; }
    .ocean { padding:clamp(22px,4vw,34px); border:1px solid var(--line); border-radius:22px; background:var(--paper); }
    .ocean-head { display:flex; align-items:flex-start; justify-content:space-between; gap:24px; }
    .ocean-head p { max-width:650px; margin-top:8px; }
    .ocean-state { flex:0 0 auto; padding:7px 11px; border-radius:999px; background:var(--blue-soft); color:var(--blue); font-weight:700; text-transform:capitalize; }
    .ocean-answers { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; margin-top:22px; }
    .ocean-answer { min-width:0; padding:18px; border:1px solid var(--line); border-radius:15px; background:#fff; }
    .ocean-answer strong { display:block; margin-bottom:6px; }
    .ocean-answer code { overflow-wrap:anywhere; }
    .ocean-totals { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; margin-top:12px; }
    .ocean-total { padding:16px 18px; border-radius:15px; background:var(--green-soft); }
    .ocean-total strong { display:block; font-size:28px; line-height:1; margin-top:6px; }
    .ocean-next { display:flex; align-items:center; justify-content:space-between; gap:18px; margin-top:18px; padding-top:18px; border-top:1px solid var(--line); }
    .source-groups { margin-top:18px; }
    .source-group { margin-top:14px; }
    .source-row { display:grid; grid-template-columns:minmax(150px,.8fr) minmax(160px,1fr) auto; gap:14px; align-items:center; padding:11px 0; border-bottom:1px solid var(--line); }
    .source-row:last-child { border-bottom:0; }
    .source-row small { color:var(--muted); }
    .history-review { padding:clamp(22px,4vw,34px); border:1px solid var(--line); border-radius:22px; background:var(--paper); }
    .history-answers,.history-counts { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; margin-top:18px; }
    .history-answer,.history-count { min-width:0; padding:17px; border:1px solid var(--line); border-radius:15px; background:#fff; }
    .history-answer strong,.history-count strong { display:block; margin-bottom:5px; }
    .history-count strong { font-size:27px; line-height:1; }
    .history-toolbar { display:flex; flex-wrap:wrap; gap:10px; margin:20px 0 14px; padding:14px; border-radius:15px; background:var(--blue-soft); }
    .history-toolbar label { display:grid; gap:4px; min-width:145px; color:var(--blue); font-size:12px; font-weight:700; }
    select,input[type="text"] { width:100%; min-width:0; min-height:44px; max-width:100%; padding:8px 10px; border:1px solid var(--line); border-radius:10px; background:white; color:var(--ink); font:inherit; }
    .history-cards { display:grid; gap:14px; }
    .history-card { min-width:0; padding:20px; border:1px solid var(--line); border-radius:17px; background:#fff; }
    .history-card.blocking { border-color:#d9b36d; background:#fffaf0; }
    .history-card.excluded { opacity:.72; }
    .history-card header { display:flex; flex-wrap:wrap; justify-content:space-between; gap:10px; margin-bottom:12px; }
    .history-badges { display:flex; flex-wrap:wrap; gap:7px; }
    .history-card .summary { overflow-wrap:anywhere; }
    .history-meta { display:flex; flex-wrap:wrap; gap:7px; margin-top:13px; }
    .history-edit-grid { display:grid; min-width:0; grid-template-columns:repeat(2,minmax(0,1fr)); gap:10px; margin-top:12px; }
    .history-edit-grid > *,.history-final > * { min-width:0; }
    .history-edit-grid label { display:grid; gap:5px; color:var(--muted); font-size:12px; }
    .history-edit-grid .wide { grid-column:1 / -1; }
    .history-evidence { max-height:300px; overflow:auto; white-space:pre-wrap; overflow-wrap:anywhere; padding:13px; border:1px solid var(--line); border-radius:12px; background:var(--bg); color:#3b413d; }
    .history-final { display:flex; min-width:0; align-items:center; justify-content:space-between; gap:18px; margin-top:18px; padding-top:18px; border-top:1px solid var(--line); }
    .history-final p,.history-final code { max-width:100%; overflow-wrap:anywhere; word-break:break-word; }
    .protected-wipe { margin-top:14px; padding:22px; border:1px solid #d7b4b0; border-radius:18px; background:var(--red-soft); }
    .protected-wipe h3 { margin-bottom:8px; } .protected-wipe .warning { margin-top:10px; color:var(--ink); }
    .protected-wipe-review,.protected-wipe-receipt { margin-top:16px; padding:16px; border:1px solid #ca9b96; border-radius:14px; background:var(--paper); outline-offset:4px; }
    .protected-wipe-receipt { border-color:#9ab8a7; background:var(--green-soft); }
    .sr-only { position:absolute!important; width:1px!important; height:1px!important; padding:0!important; margin:-1px!important; overflow:hidden!important; clip:rect(0,0,0,0)!important; white-space:nowrap!important; border:0!important; }
    .empty { padding:24px; border:1px dashed #c9c8c1; border-radius:16px; color:var(--muted); }
    .receipt { margin-top:14px; color:var(--muted); font-size:12px; overflow-wrap:anywhere; }
    .logout { margin:0; } .logout button { background:transparent; }
    @media (max-width:760px) {
      .topbar { display:grid; grid-template-columns:1fr auto; align-items:start; margin-bottom:18px; }
      .app-nav { grid-column:1 / -1; grid-row:2; margin-top:8px; }
      .logout { grid-column:2; grid-row:1; }
      .hero,.metrics,.activity-grid,.memory-list,.ocean-answers,.ocean-totals,.filters,.history-answers,.history-counts,.history-edit-grid { grid-template-columns:1fr; }
      .history-edit-grid .wide { grid-column:auto; }
      .status-banner,.feed-head { align-items:flex-start; flex-direction:column; gap:8px; }
      .ocean-head,.ocean-next { align-items:flex-start; flex-direction:column; }
      .history-final { align-items:flex-start; flex-direction:column; }
      .source-row { grid-template-columns:1fr; gap:4px; }
      .section-head { flex-direction:column; align-items:flex-start; gap:8px; }
      .section-head p { max-width:100%; }
    }
    @media (prefers-reduced-motion:reduce) { * { scroll-behavior:auto!important; } }
  </style>
</head>
<body>
<main>
  <div class="topbar">
    <div class="brand"><span class="pulse-dot"></span>Pulse</div>
    <nav class="app-nav" aria-label="Memory Home"><a href="#memories" aria-current="page">Memories</a><a href="#projects">Projects</a></nav>
    {{if .CSRFToken}}<form class="logout" method="post" action="logout"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button>Lock Home</button></form>{{end}}
  </div>

  {{if ne .StatusTone "ready"}}<div class="status-banner status-{{.StatusTone}}" role="status"><div><strong>{{.StatusTitle}}</strong><p>{{.StatusDetail}}</p></div><p>Next: {{.Data.Readiness.NextAction.Label}}</p></div>{{end}}

  <section aria-labelledby="activity-title">
    <div class="section-head"><div><div class="eyebrow">Works without opening this page</div><h2 id="activity-title">Pulse activity</h2></div><p>Pulse can prove delivery and saving. Codex does not expose whether its model actually used delivered context, so that claim stays separate.</p></div>
    <div class="activity-grid">
      {{range .HostActivity}}<article class="activity-card status-{{.State}}"><strong>{{.HostLabel}} · {{.StateLabel}}</strong><p>{{.Recall}}</p><p>{{.Write}}</p></article>{{end}}
      {{if .Storage.Available}}<article class="activity-card status-{{if .Storage.NeedsCleanup}}partial{{else}}ready{{end}}"><strong>Storage · {{.Storage.Total}} total</strong><p>{{.Storage.Reclaimable}} can be removed with <code>pulse storage clean</code>.</p><p>{{.Storage.Release}}</p><p>{{.Storage.Archive}}</p></article>{{else}}<article class="activity-card status-action"><strong>Storage status unavailable</strong><p>Run <code>pulse home</code> from the installed Pulse command to refresh it.</p></article>{{end}}
    </div>
  </section>

  <section class="feed" id="memories" aria-labelledby="memories-title">
    <div class="feed-head"><div><div class="eyebrow">Personal memory</div><h1 id="memories-title">Memories</h1><p>Useful structured memories saved automatically for this project and Personal Global.</p></div><div class="feed-count">{{.MemoryCount}} matching</div></div>
    <form class="filters" method="get" action="" aria-label="Filter memories">
      <label class="filter-field">Search<input type="search" name="q" maxlength="160" value="{{.Data.Filter.Text}}" placeholder="Words in memory"></label>
      <label class="filter-field">Project<select name="project"><option value="">All visible</option>{{range .Data.Facets.Projects}}<option value="{{.Value}}"{{if eq $.Data.Filter.Project .Value}} selected{{end}}>{{.Label}}</option>{{end}}</select></label>
      <label class="filter-field">Harness<select name="harness"><option value="">All harnesses</option>{{range .Data.Facets.Harness}}<option value="{{.Value}}"{{if eq $.Data.Filter.Harness .Value}} selected{{end}}>{{.Label}}</option>{{end}}</select></label>
      <label class="filter-field">From<input type="date" name="from" value="{{.Data.Filter.DateFrom}}"></label>
      <label class="filter-field">To<input type="date" name="to" value="{{.Data.Filter.DateTo}}"></label>
      <label class="filter-field">Scope<select name="scope"><option value="">Any scope</option><option value="project"{{if eq .Data.Filter.Scope "project"}} selected{{end}}>Project</option><option value="personal_global"{{if eq .Data.Filter.Scope "personal_global"}} selected{{end}}>Personal Global</option></select></label>

      <div class="filter-actions"><button class="primary" type="submit">Filter</button>{{if .HasMemoryFilter}}<a href="./">Clear</a>{{end}}</div>
    </form>
    {{if .HasLatestMemory}}<div class="memory-list">{{range .Data.Memories.LatestActive}}<article class="memory"><div class="meta"><span>{{.Kind}} · {{.Host}} · {{.ProjectLabel}} · {{if eq .Scope "personal_global"}}Personal Global{{else}}Project{{end}} · Device only</span><span>{{.CreatedAt}}</span></div><p class="receipt">{{.SessionLabel}}</p><p class="summary">{{.RedactedSummary}}</p><div class="memory-actions"><details><summary>Edit</summary><form class="memory-editor" method="post" action="memory/{{.ObjectID}}/edit" data-home-mutation data-home-pending-label="Saving edit…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_generation" value="{{.LogicalGeneration}}"><textarea name="summary" maxlength="1200">{{.EditableSummary}}</textarea><div class="controls"><button class="primary" type="submit">Save edit</button></div></form></details><form method="post" action="memory/{{.ObjectID}}/move" data-home-mutation data-home-pending-label="Moving memory…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_generation" value="{{.LogicalGeneration}}">{{if eq .Scope "personal_global"}}<input type="hidden" name="target_scope" value="project"><button>Move to {{.ProjectLabel}}</button>{{else}}<input type="hidden" name="target_scope" value="personal_global"><button>Move to Personal Global</button>{{end}}</form><form method="post" action="memory/{{.ObjectID}}/delete" data-home-mutation data-home-pending-label="Deleting memory…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_generation" value="{{.LogicalGeneration}}"><button>Delete</button></form></div><details class="technical"><summary>Technical details</summary><div class="receipt">Object {{.ObjectID}} · Generation {{.LogicalGeneration}} · Scope {{.Scope}} · Namespace {{.ProjectNamespaceID}} · Original project {{.OriginalRepositoryID}} · Host {{.Host}} · Session {{.SessionRef}}<br>Terminal receipt {{.TerminalReceiptID}}{{if .PresentationReceiptID}} · Presentation receipt {{.PresentationReceiptID}}{{end}}</div></details></article>{{end}}</div>{{else if .HasMemoryFilter}}<div class="empty">No memories match these filters. Clear one filter or widen the date range.</div>{{else}}<div class="empty">No canonical memory yet. Useful memories will appear here automatically.</div>{{end}}
    {{if or .HasPreviousMemories .HasMoreMemories}}<div class="pagination">
      {{if .HasPreviousMemories}}<form method="get" action=""><input type="hidden" name="q" value="{{.Data.Filter.Text}}"><input type="hidden" name="project" value="{{.Data.Filter.Project}}"><input type="hidden" name="harness" value="{{.Data.Filter.Harness}}"><input type="hidden" name="from" value="{{.Data.Filter.DateFrom}}"><input type="hidden" name="to" value="{{.Data.Filter.DateTo}}"><input type="hidden" name="scope" value="{{.Data.Filter.Scope}}"><input type="hidden" name="offset" value="{{.PreviousMemoryOffset}}"><button>Previous</button></form>{{end}}
      {{if .HasMoreMemories}}<form method="get" action=""><input type="hidden" name="q" value="{{.Data.Filter.Text}}"><input type="hidden" name="project" value="{{.Data.Filter.Project}}"><input type="hidden" name="harness" value="{{.Data.Filter.Harness}}"><input type="hidden" name="from" value="{{.Data.Filter.DateFrom}}"><input type="hidden" name="to" value="{{.Data.Filter.DateTo}}"><input type="hidden" name="scope" value="{{.Data.Filter.Scope}}"><input type="hidden" name="offset" value="{{.NextMemoryOffset}}"><button>Next</button></form>{{end}}
    </div>{{end}}
  </section>

  {{if .HasEmotionalMemory}}
  <section id="emotions" aria-labelledby="emotions-title">
    <div class="section-head"><div><div class="eyebrow">Moment, not personality</div><h2 id="emotions-title">Emotional memory</h2></div><p>The event stays in history. Only its influence on today fades: it halves every 24 hours and stops shaping current context after seven days.</p></div>
    {{range .Data.EmotionalPatterns}}<div class="status-banner"><div><strong>{{.Emotion}} appears repeatedly</strong><p>{{.Count}} confirmed moments between {{.FirstSeen}} and {{.LastSeen}}. This is an observation, not a personality trait.</p></div></div>{{end}}
    <div class="memory-list">
      {{range .Data.EmotionalMemory}}{{$event := .}}
      <article class="memory">
        <div class="meta"><span>{{if eq .Derivation "inferred"}}Pulse inferred this{{else}}Named or confirmed by you{{end}} · Device only</span><span>{{.OccurredAt}}</span></div>
        <p class="summary">{{.Summary}}</p>
        {{range .Labels}}<p><strong>{{.Emotion}}</strong> · then {{printf "%.2f" .Intensity}} · now {{printf "%.2f" .CurrentInfluence}}</p>{{end}}
        {{if .Trigger}}<p>Cause: {{.Trigger}} {{if .TriggerConfirmed}}(confirmed){{else}}(Pulse assumption){{end}}</p>{{else}}<p>Cause is not known.</p>{{end}}
        {{if .Question}}<p class="receipt">Waiting question: {{.Question.Question}}</p>{{end}}
        <div class="memory-actions">
          <form method="post" action="emotion/{{.EventID}}/confirm" data-home-mutation data-home-pending-label="Confirming emotion…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><button class="primary">Confirm this mark</button></form>
          <details><summary>Correct emotion or cause</summary>
            <form class="memory-editor" method="post" action="emotion/{{.EventID}}/edit" data-home-mutation data-home-pending-label="Updating emotion…">
              <input type="hidden" name="csrf_token" value="{{$.CSRFToken}}">
              <p class="receipt">Saving replaces the current emotional mark with the values below. The event itself is not changed.</p>
              <label>Emotion<select name="emotion"><option{{if eq .PrimaryEmotion "joy"}} selected{{end}}>joy</option><option{{if eq .PrimaryEmotion "sadness"}} selected{{end}}>sadness</option><option{{if eq .PrimaryEmotion "anger"}} selected{{end}}>anger</option><option{{if eq .PrimaryEmotion "fear"}} selected{{end}}>fear</option><option{{if eq .PrimaryEmotion "trust"}} selected{{end}}>trust</option><option{{if eq .PrimaryEmotion "disgust"}} selected{{end}}>disgust</option><option{{if eq .PrimaryEmotion "anticipation"}} selected{{end}}>anticipation</option><option{{if eq .PrimaryEmotion "surprise"}} selected{{end}}>surprise</option><option{{if eq .PrimaryEmotion "shame"}} selected{{end}}>shame</option><option{{if eq .PrimaryEmotion "guilt"}} selected{{end}}>guilt</option></select></label>
              <label>Intensity 0–1<input name="intensity" type="number" min="0" max="1" step="0.01" value="{{printf "%.2f" .PrimaryIntensity}}"></label>
              <label>Confidence 0–1<input name="confidence" type="number" min="0" max="1" step="0.01" value="{{printf "%.2f" .Confidence}}"></label>
              <input type="hidden" name="derivation" value="user_confirmed">
              <label>Your label<input name="observed_label" maxlength="120" value="{{.ObservedLabel}}"></label>
              <label>Cause, if known<input name="trigger_summary" maxlength="360" value="{{.Trigger}}"></label>
              <input type="hidden" name="trigger_derivation" value="user_confirmed"><input type="hidden" name="trigger_confidence" value="1">
              <div class="controls"><button class="primary">Save correction</button></div>
            </form>
          </details>
          <form method="post" action="emotion/{{.EventID}}/delete" data-home-mutation data-home-confirm="Delete only this emotional mark?" data-home-pending-label="Deleting emotional mark…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><button>Delete emotional mark</button></form>
        </div>
      </article>
      {{end}}
    </div>
  </section>
  {{end}}

  {{if .HasPending}}
  <section id="memory-tray">
    <div class="section-head"><div><div class="eyebrow">Automatic retry</div><h2>Memory activity</h2></div><p>Useful memory is saved immediately. A card appears here only while Pulse retries a transient write problem.</p></div>
    <div class="tray">
      {{range .Pending}}
      <article class="tray-card" data-candidate-id="{{.CandidateID}}" data-candidate-version="{{.Version}}">
        <header><span class="kind">{{.Kind}}</span><span class="pill">Retrying</span></header>
        <p class="summary">{{.Summary}}</p>
        <details><summary>Edit the exact structured record</summary>
          <form method="post" action="tray/{{.CandidateID}}/edit" data-home-mutation>
            <input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_version" value="{{.Version}}">
            <textarea name="candidate_json">{{.CandidateJSON}}</textarea>
            <div class="controls"><button class="primary" type="submit">Save edit</button></div>
          </form>
        </details>
        <div class="controls">
          <form method="post" action="tray/{{.CandidateID}}/cancel" data-home-mutation data-home-pending-label="Deleting memory…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_version" value="{{.Version}}"><button>Delete</button></form>
        </div>
        <p class="receipt" data-home-status aria-live="polite">Pulse is retrying automatically. Opening Home is never required.</p>
      </article>
      {{end}}
    </div>
  </section>
  {{end}}

  <section id="projects" aria-labelledby="projects-title">
    <div class="section-head"><div><div class="eyebrow">Where memory lives</div><h2 id="projects-title">Projects</h2></div><p>Verified projects represented in this bound vault. Fragmented legacy stores are never silently opened or merged.</p></div>
    {{range .Data.Facets.Projects}}<article class="project-card"><strong>{{.Label}}</strong><p>Memories from this project are in the bound {{$.StoreLabel}} vault. Session labels come only from safe capture metadata.</p><div class="boundary"><span class="pill">Device local</span><span class="pill">Canonical vault</span></div><details class="technical"><summary>Technical details</summary><div class="receipt">Repository <code>{{.Value}}</code></div></details></article>{{else}}<article class="project-card"><strong>Current project</strong><p>No canonical memories have been captured here yet.</p><details class="technical"><summary>Technical details</summary><div class="receipt">Repository <code>{{.Data.Boundary.RepositoryID}}</code></div></details></article>{{end}}
  </section>


  <details class="secondary-details"><summary>Status and evidence</summary><div class="secondary-body">
  <div class="metrics" aria-label="Memory proof">
    <div class="metric"><span>Saved memories</span><span class="value">{{.MemoryCount}}</span><small>Canonical, active, private records</small></div>
    <div class="metric"><span>Continuity</span><span class="value" style="font-size:25px">{{.ContextState}}</span><small>{{if .HasContext}}Receipt-backed delivery{{if .ContextHost}} via {{.ContextHost}}{{end}}{{else}}Waiting for a fresh task{{end}}</small></div>
    <div class="metric"><span>Token economy</span><span class="value">{{.EconomyValue}}</span><small>{{.EconomyDetail}}{{if .EstimatedPercent}} · {{.EstimatedPercent}}{{end}}</small></div>
  </div>
  <div class="action status-{{.StatusTone}}"><strong>{{.StatusTitle}}</strong><p>{{.StatusDetail}}</p><p class="receipt">Exact check: <code>{{.Data.Readiness.ReasonCode}}</code> · checked {{.Data.Readiness.CheckedAt}} · next: {{.Data.Readiness.NextAction.Label}}</p></div>

  <section id="memory-ocean" aria-labelledby="memory-ocean-title">
    <div class="ocean">
      {{if .HasConsolidation}}
      <div class="ocean-head">
        <div><div class="eyebrow">Memory ocean</div><h2 id="memory-ocean-title">What Pulse found on this computer</h2><p>This report only reads recognized local sources. It does not import, merge, delete, publish, or change the project destination.</p></div>
        <div class="ocean-state" role="status" aria-live="polite">{{.ConsolidationHealthLabel}}</div>
      </div>
      <div class="ocean-answers">
        <div class="ocean-answer"><strong>Where memory for this project is written</strong><p>{{.Data.Consolidation.Destination.StoreKind}} store <code>{{.Data.Consolidation.Destination.StoreID}}</code> for <code>{{.Data.Consolidation.Destination.RepositoryID}}</code></p></div>
        <div class="ocean-answer"><strong>Which sources were inspected</strong><p>{{.ConsolidationSourceCount}} recognized source entries. Local paths stay private in the owner-only sidecar.</p></div>
      </div>
      <div class="ocean-totals" aria-label="Consolidation totals">
        <div class="ocean-total">Already represented<strong>{{.Data.Consolidation.Totals.AlreadyRepresented}}</strong></div>
        <div class="ocean-total">Unique<strong>{{.Data.Consolidation.Totals.Unique}}</strong></div>
        <div class="ocean-total">Needs review<strong>{{.Data.Consolidation.Totals.Ambiguous}}</strong></div>
      </div>
      <div class="ocean-next">
        <div><strong>Next</strong><p>{{.Data.Consolidation.NextAction}}</p>{{if .Data.Consolidation.Blockers}}<p class="receipt">Active blockers: {{range $index,$value := .Data.Consolidation.Blockers}}{{if $index}}, {{end}}<code>{{$value}}</code>{{end}}</p>{{end}}</div>
        {{if eq .ConsolidationAction "start"}}<form method="post" action="consolidation/start" data-home-mutation data-home-pending-label="Starting a fresh read-only report…"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button class="primary">{{.ConsolidationActionLabel}}</button></form>{{end}}
        {{if eq .ConsolidationAction "cancel"}}<form method="post" action="consolidation/{{.Data.Consolidation.InvocationID}}/cancel" data-home-mutation data-home-pending-label="Canceling this report…"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button>{{.ConsolidationActionLabel}}</button></form>{{end}}
        {{if eq .ConsolidationAction "resume"}}<form method="post" action="consolidation/{{.Data.Consolidation.InvocationID}}/resume" data-home-mutation data-home-pending-label="Resuming the read-only report…"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button class="primary">{{.ConsolidationActionLabel}}</button></form>{{end}}
      </div>
      <details class="source-groups"><summary>Inspect grouped source counts</summary>
        {{if .ConsolidationCanonical}}<div class="source-group"><h3>Canonical destination</h3>{{range .ConsolidationCanonical}}<div class="source-row"><code>{{.Alias}}</code><span>{{.Classification}}</span><small>{{.ReasonCode}} · {{index .Counts "source_rows"}} source rows</small></div>{{end}}</div>{{end}}
        {{if .ConsolidationImports}}<div class="source-group"><h3>Import candidates</h3>{{range .ConsolidationImports}}<div class="source-row"><code>{{.Alias}}</code><span>{{.Classification}}</span><small>{{.ReasonCode}} · {{index .Counts "source_rows"}} source rows</small></div>{{end}}</div>{{end}}
        {{if .ConsolidationArtifacts}}<div class="source-group"><h3>Non-memory artifacts</h3>{{range .ConsolidationArtifacts}}<div class="source-row"><code>{{.Alias}}</code><span>{{.Classification}}</span><small>{{.ReasonCode}}</small></div>{{end}}</div>{{end}}
      </details>
      <p class="receipt">Report <code>{{.Data.Consolidation.InvocationID}}</code> · digest <code>{{.Data.Consolidation.ReportDigest}}</code></p>
      {{else}}
      <div class="ocean-head"><div><div class="eyebrow">Memory ocean</div><h2 id="memory-ocean-title">See all recognized local memory sources</h2><p>Pulse has not created a consolidation report for this project yet. The scan is read-only and keeps local paths out of the portable result.</p></div><div class="ocean-state">Not started</div></div>
      <div class="ocean-next"><p>No memory source will be imported or deleted.</p><form method="post" action="consolidation/start" data-home-mutation data-home-pending-label="Inspecting recognized local sources…"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button class="primary">Create read-only report</button></form></div>
      {{end}}
    </div>
  </section>

  {{if or .Historical.Available .Historical.Unavailable}}
  <section id="historical-review" aria-labelledby="historical-review-title">
    <div class="history-review">
      <div class="ocean-head">
        <div><div class="eyebrow">Historical memory dry run</div><h2 id="historical-review-title" tabindex="-1">{{.Historical.StateLabel}}</h2><p>{{.Historical.StateDetail}}</p></div>
        {{if .Historical.Available}}<div class="ocean-state" role="status" aria-live="polite">{{.Historical.State}}</div>{{end}}
      </div>
      {{if .Historical.Unavailable}}<div class="empty" style="margin-top:18px">The canonical memory vault is unchanged. Inspect or remove the private historical sidecar before retrying.</div>{{else}}
      <div class="history-answers">
        <div class="history-answer"><strong>What Pulse read</strong><p>{{.Historical.SourceRootCount}} frozen root session trees across {{.Historical.SourceFileCount}} path-free source aliases ({{.Historical.SourceBytes}} captured), producing {{.Historical.TotalUnits}} isolated extraction turns from {{.Historical.EvidenceBytes}} normalized evidence. Raw source files were not changed.</p></div>
        <div class="history-answer"><strong>What Pulse will write</strong><p>{{if .Historical.HasManifest}}{{.Historical.WriteCount}} structured private candidates in this revision; {{.Historical.ExcludedCount}} excluded. This is still a dry run.{{else}}No candidate manifest exists yet. Extraction and review cannot imply a future write.{{end}}</p></div>
      </div>
      {{if .Historical.CanAuthorizeEgress}}
      <div class="history-final">
        <div><strong>Allow {{.Historical.TotalUnits}} isolated Luna turns for this exact snapshot?</strong><p>Pulse will send {{.Historical.EvidenceBytes}} of normalized records from these {{.Historical.SourceRootCount}} root trees to <strong>GPT-5.6 Luna · low</strong> through your existing Codex/ChatGPT subscription. Local paths, attachments, credentials, hidden reasoning, inherited summaries, and raw source files are excluded. This may consume significant subscription quota; Pulse pauses on quota and never falls back to an API or another model. This creates a dry-run manifest only; it cannot write memory.</p><p class="receipt">Snapshot <code>{{.Historical.SnapshotDigest}}</code> · {{.Historical.SourceFileCount}} files · {{.Historical.TotalUnits}} model turns</p></div>
        <form method="post" action="history/{{.Historical.JobID}}/authorize-egress" data-home-mutation data-home-pending-label="Authorizing only this frozen snapshot…">
          <input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><input type="hidden" name="snapshot_digest" value="{{.Historical.SnapshotDigest}}"><input type="hidden" name="runner_contract_digest" value="{{.Historical.RunnerContract}}"><input type="hidden" name="confirmation_digest" value="{{.Historical.EgressConfirmationDigest}}">
          <button class="primary">Authorize {{.Historical.TotalUnits}} Luna turns</button>
        </form>
      </div>
      {{end}}
      {{if .Historical.HasManifest}}
      <div class="history-counts" aria-label="Historical review progress">
        <div class="history-count"><span>Reviewed</span><strong>{{.Historical.ReviewedCount}} / {{.Historical.CandidateCount}}</strong><p>Explicit keep, edit, or exclude decisions.</p></div>
        <div class="history-count"><span>Blocking decisions left</span><strong>{{.Historical.RemainingRequired}}</strong><p>Hypotheses, conflicts, inferred items, unassigned scope, or unavailable evidence.</p></div>
      </div>
      {{if .Historical.Cards}}
      <div class="history-toolbar" aria-label="Filter historical candidates">
        <label>Root session<select data-history-filter="root"><option value="">All roots</option>{{range .Historical.RootOptions}}<option value="{{.Value}}">{{.Label}}</option>{{end}}</select></label>
        <label>Kind<select data-history-filter="kind"><option value="">All kinds</option><option>event</option><option>decision</option><option>assertion</option><option>person</option><option>project</option><option>relation</option><option>state</option><option>continuity</option></select></label>
        <label>Scope<select data-history-filter="scope"><option value="">All scopes</option><option>project</option><option>global</option><option>unassigned</option></select></label>
        <label>Disposition<select data-history-filter="disposition"><option value="">All dispositions</option><option>pending</option><option>kept</option><option>excluded</option></select></label>
      </div>
      <p class="receipt" data-history-filter-status role="status" aria-live="polite"></p>
      <div class="history-cards">
        {{range .Historical.Cards}}
        <article class="history-card {{if .RequiresReview}}blocking{{end}} {{if eq .Disposition "excluded"}}excluded{{end}}" data-history-card data-history-candidate-id="{{.CandidateID}}" data-root="{{.RootIDs}}" data-kind="{{.Kind}}" data-scope="{{.ScopeKind}}" data-disposition="{{.Disposition}}" tabindex="-1">
          <header><div class="history-badges"><span class="kind">{{.Kind}}</span><span class="pill">{{.Disposition}}</span>{{range .RequirementLabels}}<span class="pill">{{.}}</span>{{end}}</div><span class="pill">{{.Confidence}} confidence</span></header>
          {{if eq .Kind "relation"}}<p class="receipt">{{.RelationSubject}} → {{.RelationObject}}</p>{{end}}
          <p class="summary">{{.PrimaryText}}</p>
          <div class="history-meta"><span class="pill">{{.EpistemicStatus}}</span><span class="pill">{{.Derivation}}</span><span class="pill">{{.ScopeKind}}{{if .ProjectID}} · {{.ProjectID}}{{end}}</span><span class="pill">{{.SourceCount}} source ref(s)</span>{{if .RootLabel}}<span class="pill">{{.RootLabel}}</span>{{end}}</div>
          {{if $.Historical.CanMutate}}
          <div class="controls">
            <form method="post" action="history/{{$.Historical.JobID}}/items/{{.CandidateID}}/review" data-home-mutation data-home-pending-label="Keeping this exact candidate…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_revision" value="{{$.Historical.Revision}}"><input type="hidden" name="expected_digest" value="{{$.Historical.ManifestDigest}}"><input type="hidden" name="action" value="kept"><button class="primary">Keep</button></form>
            <form method="post" action="history/{{$.Historical.JobID}}/items/{{.CandidateID}}/review" data-home-mutation data-home-pending-label="Excluding this exact candidate…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_revision" value="{{$.Historical.Revision}}"><input type="hidden" name="expected_digest" value="{{$.Historical.ManifestDigest}}"><input type="hidden" name="action" value="excluded"><button>Exclude</button></form>
          </div>
          <details><summary>Edit text, scope, time, and status</summary>
            <form method="post" action="history/{{$.Historical.JobID}}/items/{{.CandidateID}}/review" data-home-mutation data-home-pending-label="Creating a corrected review revision…">
              <input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_revision" value="{{$.Historical.Revision}}"><input type="hidden" name="expected_digest" value="{{$.Historical.ManifestDigest}}"><input type="hidden" name="action" value="edit">
              <div class="history-edit-grid">
                <label class="wide">Primary text<input type="text" name="primary_text" value="{{.PrimaryText}}" maxlength="1200" required></label>
                <label>Scope<select name="scope_kind"><option value="project" {{if eq .ScopeKind "project"}}selected{{end}}>project</option><option value="global" {{if eq .ScopeKind "global"}}selected{{end}}>global</option><option value="unassigned" {{if eq .ScopeKind "unassigned"}}selected{{end}}>unassigned</option></select></label>
                <label>Project ID<input type="text" name="project_id" value="{{.ProjectID}}" placeholder="Only for project scope"></label>
                <label>Valid from<input type="text" name="valid_from" value="{{.ValidFrom}}" required></label>
                <label>Valid to<input type="text" name="valid_to" value="{{.ValidTo}}" placeholder="Optional RFC3339"></label>
                <label>Knowledge status<select name="epistemic_status">{{if ne .Derivation "inferred"}}<option value="explicit" {{if eq .EpistemicStatus "explicit"}}selected{{end}}>explicit</option>{{end}}<option value="hypothesis" {{if eq .EpistemicStatus "hypothesis"}}selected{{end}}>hypothesis</option><option value="conflict" {{if eq .EpistemicStatus "conflict"}}selected{{end}}>conflict</option></select></label>
                {{if eq .Kind "continuity"}}<label>Continuity status<select name="continuity_status"><option value="open" {{if eq .ContinuityStatus "open"}}selected{{end}}>open</option><option value="closed" {{if eq .ContinuityStatus "closed"}}selected{{end}}>closed</option><option value="historical" {{if eq .ContinuityStatus "historical"}}selected{{end}}>historical</option></select></label>{{else}}<input type="hidden" name="continuity_status" value="">{{end}}
              </div>
              <div class="controls"><button class="primary">Save correction</button></div>
            </form>
          </details>
          {{else}}<p class="receipt">This job state is read-only. Start or resume a fresh snapshot to change it.</p>{{end}}
          <details><summary>Evidence</summary>{{if .EvidenceAvailable}}<button type="button" data-history-evidence="history/{{$.Historical.JobID}}/items/{{.CandidateID}}/evidence">Load bounded source evidence</button><pre class="history-evidence" data-history-evidence-output hidden tabindex="-1"></pre>{{else}}<p class="receipt">Evidence is unavailable. Keep or exclude explicitly before review can finish.</p>{{end}}</details>
          <p class="receipt">Candidate {{.CandidateID}}</p>
        </article>
        {{end}}
      </div>
      {{else}}<div class="empty" style="margin-top:18px">The exact selected roots produced no structured candidates. There is nothing to approve or apply.</div>{{end}}
      {{if and .Historical.Cards .Historical.CanMutate}}<details><summary>Merge or split an entity reference</summary>
        <p class="receipt">Preview first. Merge rewrites every matching subject/object reference; split rewrites only the comma-separated candidate IDs you name. The preview lists every affected candidate, scope, provenance count, and resulting ID.</p>
        <form action="history/{{.Historical.JobID}}/entities/preview" data-history-entity-form data-apply-action="history/{{.Historical.JobID}}/entities/apply">
          <input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><input type="hidden" name="expected_revision" value="{{.Historical.Revision}}"><input type="hidden" name="expected_digest" value="{{.Historical.ManifestDigest}}">
          <div class="history-edit-grid">
            <label>Operation<select name="mode"><option value="merge">merge all matching references</option><option value="split">split selected candidates</option></select></label>
            <label>Current entity ID<input type="text" name="from_entity_id" required></label>
            <label>Resulting entity ID<input type="text" name="to_entity_id" required></label>
            <label class="wide">Candidate IDs for split<input type="text" name="selected_candidates" placeholder="candidate_…, candidate_…"></label>
          </div>
          <div class="controls"><button type="submit">Preview affected graph material</button><button type="button" class="primary" data-history-entity-confirm disabled>Confirm this review correction</button></div>
          <div class="history-evidence" data-history-entity-preview hidden tabindex="-1" role="status" aria-live="polite"></div>
        </form>
      </details>{{end}}
      <div class="history-final">
        <div><strong>Revision {{.Historical.Revision}}</strong><p>Manifest <code>{{.Historical.ManifestDigest}}</code>. Finishing review creates another immutable digest and still writes nothing.</p></div>
        {{if .Historical.CanComplete}}<form method="post" action="history/{{.Historical.JobID}}/complete" data-home-mutation data-home-pending-label="Freezing the reviewed revision…"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><input type="hidden" name="expected_revision" value="{{.Historical.Revision}}"><input type="hidden" name="manifest_digest" value="{{.Historical.ManifestDigest}}"><input type="hidden" name="destination_store_id" value="{{.Historical.DestinationStoreID}}"><input type="hidden" name="repository_id" value="{{.Historical.RepositoryID}}"><input type="hidden" name="confirmation_digest" value="{{.Historical.ConfirmationDigest}}"><button class="primary">Finish review — do not write yet</button></form>
        {{else if .Historical.CanPrepareApply}}<form method="post" action="history/{{.Historical.JobID}}/prepare-apply" data-home-mutation data-home-pending-label="Compiling the exact write set…"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><input type="hidden" name="expected_revision" value="{{.Historical.Revision}}"><input type="hidden" name="manifest_digest" value="{{.Historical.ManifestDigest}}"><input type="hidden" name="destination_store_id" value="{{.Historical.DestinationStoreID}}"><input type="hidden" name="repository_id" value="{{.Historical.RepositoryID}}"><button class="primary">Prepare exact write set — still do not write</button></form>
        {{else if .Historical.CanApply}}<div><p class="receipt">Write set <code>{{.Historical.WriteSetDigest}}</code> · destination generation {{.Historical.DestinationGeneration}} · {{.Historical.PlannedCreatedCount}} create · {{.Historical.PlannedDeduplicatedCount}} exact deduplicate</p><form method="post" action="history/{{.Historical.JobID}}/apply" data-home-mutation data-home-pending-label="Backing up the vault and applying the exact write set…"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><input type="hidden" name="expected_revision" value="{{.Historical.Revision}}"><input type="hidden" name="manifest_digest" value="{{.Historical.ManifestDigest}}"><input type="hidden" name="write_set_digest" value="{{.Historical.WriteSetDigest}}"><input type="hidden" name="destination_store_id" value="{{.Historical.DestinationStoreID}}"><input type="hidden" name="destination_generation" value="{{.Historical.DestinationGeneration}}"><input type="hidden" name="confirmation_digest" value="{{.Historical.ApplyConfirmationDigest}}"><button class="primary">Back up and import these {{.Historical.WriteCount}} memories</button></form></div>
        {{else if .Historical.BatchReceiptID}}<span class="pill">Committed receipt · {{.Historical.BatchReceiptID}}</span>{{else if .Historical.ReviewComplete}}<span class="pill">Review frozen · refresh to verify the write set</span>{{else}}<button disabled>Finish blocking decisions first</button>{{end}}
      </div>
      {{end}}
      {{end}}
    </div>
  </section>
  {{end}}

  <section id="authority-profile">
    <div class="section-head"><div><div class="eyebrow">Security boundary</div><h2>Protected actions</h2></div><p>Ordinary memory and Memory Home do not require enhanced verification. This profile controls only destructive vault and binding actions.</p></div>
    <div class="preview">
      <p>Profile <code>{{.EnhancedPresenceProfile.Schema}}</code> · version {{.EnhancedPresenceProfile.Version}} · adapter <code>{{.EnhancedPresenceProfile.Kind}}</code> · {{if .EnhancedPresenceProfile.Available}}available{{else}}unavailable{{end}}{{if .EnhancedPresenceProfile.ReasonCode}} · reason <code>{{.EnhancedPresenceProfile.ReasonCode}}</code>{{end}}</p>
      {{if .HasProtectedActions}}<div class="authority-actions" aria-label="Protected actions this profile can authorize">{{range .EnhancedPresenceProfile.ProtectedActions}}<span class="pill"><code>{{.}}</code></span>{{end}}</div>{{else}}<div class="empty" style="margin-top:14px">No protected actions can be authorized on this machine.</div>{{end}}
    </div>
    {{if .CanProtectedWipe}}
    <article class="protected-wipe" data-protected-wipe>
      <h3 id="protected-wipe-title">Delete this project’s stored Pulse records</h3>
      <p>This control is available only because the enhanced profile can authorize <code>vault.wipe</code>.</p>
      <p class="warning"><strong>Warning:</strong> this permanently removes the exact project-bound record set you review. It cannot be undone.</p>
      <div data-protected-wipe-idle class="controls"><button type="button" data-protected-wipe-begin>Review exact stored records</button></div>
      <div class="protected-wipe-review" data-protected-wipe-review hidden tabindex="-1" role="group" aria-labelledby="protected-wipe-title" aria-describedby="protected-wipe-warning">
        <p id="protected-wipe-warning"><strong>Permanent deletion.</strong> Pulse will delete exactly <strong data-protected-wipe-count></strong> stored records across memory and continuity, including supporting local receipts and projections. Record contents stay hidden.</p>
        <p>Approval expires at <time data-protected-wipe-expiry></time> · <span data-protected-wipe-countdown aria-hidden="true"></span><span data-protected-wipe-countdown-live class="sr-only" role="status" aria-live="polite" aria-atomic="true"></span></p>
        <div class="controls"><button type="button" class="danger" data-protected-wipe-complete>Verify with this device and delete exact records</button><button type="button" data-protected-wipe-cancel>Cancel</button></div>
      </div>
      <div class="protected-wipe-receipt" data-protected-wipe-receipt hidden tabindex="-1" role="group" aria-label="Protected wipe receipt">
        <h3>Deletion receipt</h3>
        <p>Deleted exactly <strong data-protected-wipe-receipt-count></strong> stored records across memory and continuity, including supporting local receipts and projections, at <time data-protected-wipe-receipt-time></time>.</p>
        <p class="receipt">Receipt <code data-protected-wipe-receipt-schema></code> · snapshot digest <code data-protected-wipe-receipt-digest></code></p>
      </div>
      <p class="receipt" data-protected-wipe-status role="status" aria-live="polite" aria-atomic="true">No deletion is prepared.</p>
    </article>
    {{end}}
  </section>

  {{if .UnassignedEnabled}}
  <section id="unassigned-inbox">
    <div class="section-head"><div><div class="eyebrow">Not in any project yet</div><h2>Unassigned Inbox</h2></div><p>These structured records are local and non-retrievable. Save one to this exact project, or delete it.</p></div>
    {{if .UnassignedUnavailable}}<div class="empty">Inbox is unavailable. No queued content was read or moved. Repair its private local file, then refresh Home.</div>{{else if .HasUnassigned}}
    <div class="tray">
      {{range .Unassigned}}
      <article class="tray-card">
        <header><span class="kind">{{.Kind}}</span><span class="pill">Unassigned · {{.Host}}</span></header>
        <p class="summary">{{.Summary}}</p>
        <div class="controls">
          <form method="post" action="unassigned/{{.ItemID}}/assign" data-home-mutation data-home-pending-label="Saving to this project…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="content_digest" value="{{.ContentDigest}}"><input type="hidden" name="expected_binding_digest" value="{{$.Data.Boundary.BindingDigest}}"><button class="primary">Save to this project</button></form>
          <form method="post" action="unassigned/{{.ItemID}}/delete" data-home-mutation data-home-confirm="Delete this unassigned memory?" data-home-pending-label="Deleting the exact Inbox card…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="content_digest" value="{{.ContentDigest}}"><input type="hidden" name="expected_binding_digest" value="{{$.Data.Boundary.BindingDigest}}"><button>Delete</button></form>
        </div>
        <p class="receipt">Not counted as memory · captured {{.CreatedAt}} · digest {{.ContentDigest}}</p>
      </article>
      {{end}}
    </div>
    {{else}}<div class="empty">No unassigned memories. Work inside a bound project, or explicitly assign any future Inbox card here.</div>{{end}}
    {{if .HasUnassignedActivity}}<div class="memory-list" style="margin-top:14px">{{range .UnassignedActivity}}<article class="memory attempt"><div class="meta"><span>{{.ActionLabel}}</span><span>{{.CreatedAt}}</span></div><p class="summary">{{.Status}}</p><div class="receipt">Inbox receipt {{.ReceiptID}} · digest {{.ContentDigest}}</div></article>{{end}}</div>{{end}}
  </section>
  {{end}}

  {{if .HasAttempts}}
  <section id="memory-attempts">
    <div class="section-head"><div><div class="eyebrow">Non-canonical activity</div><h2>Recent save attempts</h2></div><p>Pending, canceled, failed, and rejected attempts stay separate from canonical memory.</p></div>
    <div class="memory-list">{{range .Attempts}}<article class="memory attempt"><div class="meta"><span>{{.Kind}} · {{.State}}</span><span>{{.CreatedAt}}</span></div><p class="summary">{{.RedactedSummary}}</p><div class="receipt">Attempt receipt {{.ReceiptID}} · Status {{.ReceiptStatus}}</div></article>{{end}}</div>
  </section>
  {{end}}

  <section>
    <div class="section-head"><div><div class="eyebrow">Cross-task continuity</div><h2>What the next task receives</h2></div><p>This is a bounded preview, not a promise that the host consumed it.</p></div>
    {{if .HasNextPreview}}<div class="preview"><pre>{{.Data.NextTaskPreview.RedactedResume}}</pre><div class="receipt">{{.Data.NextTaskPreview.PulseTokens}} Pulse tokens · preview only · digest {{.Data.NextTaskPreview.PayloadDigest}}</div></div>{{else}}<div class="empty">No safe preview is available yet. Pulse will not expose path-like, secret-like, or raw transcript content here.</div>{{end}}
  </section>
  </div></details>
  <script src="assets/home.js" defer></script>
</main>
</body>
</html>`))
