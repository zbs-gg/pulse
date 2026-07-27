package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const ContinuitySchema = "pulse.continuity.v2"

var threadIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$`)
var safeRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{1,159}$`)
var viewerUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var viewerBlockedPersonLabels = map[string]bool{
	"agent": true, "archive": true, "assistant": true, "chat": true, "chatgpt": true, "claude": true,
	"code": true, "codex": true, "command": true, "conversation": true, "export": true, "graph": true,
	"import": true, "memory": true, "preview": true, "pulse": true, "session": true, "source": true,
	"terminal": true, "tool": true, "viewer": true, "workflow": true,
	"движение": true, "назови": true, "обращ": true, "пауза": true, "привет": true, "пульс": true,
	"скажи": true, "сначала": true, "теперь": true, "чат": true,
}

type ContinuityCheckpoint struct {
	ID               int64    `json:"id,omitempty"`
	ThreadID         string   `json:"thread_id"`
	SessionID        string   `json:"session_id"`
	Host             string   `json:"host"`
	ProjectID        string   `json:"project_id,omitempty"`
	Summary          string   `json:"summary"`
	Decisions        []string `json:"decisions,omitempty"`
	OpenLoops        []string `json:"open_loops,omitempty"`
	DoNotRepeat      []string `json:"do_not_repeat,omitempty"`
	EmotionalAnchors []string `json:"emotional_anchors,omitempty"`
	StateSignals     []string `json:"state_signals,omitempty"`
	ActiveThreads    []string `json:"active_threads,omitempty"`
	ReviewInsights   []string `json:"review_insights,omitempty"`
	SourceRefs       []string `json:"source_refs,omitempty"`
	Confidence       float64  `json:"confidence"`
	CreatedAt        string   `json:"created_at,omitempty"`
}

type ContinuityObservation struct {
	ID              int64  `json:"id,omitempty"`
	ThreadID        string `json:"thread_id"`
	SessionID       string `json:"session_id"`
	Host            string `json:"host"`
	EventType       string `json:"event_type"`
	RedactedSummary string `json:"redacted_summary"`
	RawRef          string `json:"raw_ref,omitempty"`
	SourceRef       string `json:"source_ref,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
}

type ContinuitySession struct {
	SessionID string `json:"session_id"`
	ThreadID  string `json:"thread_id"`
	Host      string `json:"host"`
	ProjectID string `json:"project_id,omitempty"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
	Status    string `json:"status"`
}

type ResumeQuery struct {
	ThreadID    string `json:"thread_id,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Host        string `json:"host,omitempty"`
	TokenBudget int    `json:"token_budget,omitempty"`
}

type ResumeSections struct {
	HarnessActivity             []string `json:"harness_activity,omitempty"`
	WhereWeLeftOff              []string `json:"where_we_left_off"`
	ActiveDecisions             []string `json:"active_decisions"`
	ActiveReviewedThreads       []string `json:"active_reviewed_threads,omitempty"`
	ReviewInsights              []string `json:"review_insights,omitempty"`
	OpenLoops                   []string `json:"open_loops"`
	DoNotRepeat                 []string `json:"do_not_repeat"`
	RelevantEmotionalState      []string `json:"relevant_emotional_state_context"`
	SuggestedNextStep           []string `json:"suggested_next_step"`
	EvidenceRefs                []string `json:"evidence_refs"`
	MaterialRefs                []string `json:"material_refs,omitempty"`
	RecentLocalAutoObservations []string `json:"recent_local_auto_observations,omitempty"`
}

type ResumeBlock struct {
	Schema                 string         `json:"schema"`
	ThreadID               string         `json:"thread_id"`
	ProjectID              string         `json:"project_id,omitempty"`
	SessionID              string         `json:"session_id,omitempty"`
	TokenBudget            int            `json:"token_budget"`
	TokenEstimate          int            `json:"token_estimate"`
	TokenEconomy           TokenEconomy   `json:"token_economy"`
	ResumeMarkdown         string         `json:"resume_markdown"`
	Sections               ResumeSections `json:"sections"`
	EvidenceRefs           []string       `json:"evidence_refs"`
	MaterialRefs           []string       `json:"material_refs,omitempty"`
	IncludedObjectIDs      []string       `json:"included_object_ids"`
	IncludedEvidenceIDs    []string       `json:"included_evidence_ids"`
	BaselineKind           string         `json:"baseline_kind,omitempty"`
	SourceEquivalentTokens *int           `json:"source_equivalent_tokens,omitempty"`
	CoverageCounted        int            `json:"coverage_counted,omitempty"`
	CoverageTotal          int            `json:"coverage_total,omitempty"`
	MemorySnapshotDigest   string         `json:"memory_snapshot_digest,omitempty"`
}

type TokenEconomy struct {
	State         string `json:"state"`
	MethodID      string `json:"method_id"`
	MethodVersion string `json:"method_version"`
	RenderedBytes int    `json:"rendered_bytes"`
	PulseTokens   int    `json:"pulse_tokens"`
	ReasonCode    string `json:"reason_code"`
}

const (
	TokenEconomyCollectingBaseline      = "collecting_baseline"
	TokenEconomyMethodUTF8BytesDiv4Ceil = "utf8_bytes_div4_ceil"
)

type ViewerData struct {
	NextResume       ResumeBlock               `json:"next_resume"`
	FirstMemory      ViewerFirstMemory         `json:"first_memory"`
	RecentSessions   []ContinuitySession       `json:"recent_sessions"`
	Activity         []ViewerActivityItem      `json:"activity"`
	OpenLoops        []string                  `json:"open_loops"`
	SavedDecisions   []string                  `json:"saved_decisions"`
	EmotionalAnchors []string                  `json:"emotional_anchors"`
	GraphProfile     ViewerGraphProfile        `json:"graph_profile"`
	MaterialGraph    MaterialGraph             `json:"material_graph"`
	HiddenEntities   []ViewerHiddenEntity      `json:"hidden_entities"`
	MemoryTray       []MemoryTrayCandidateView `json:"memory_tray"`
}

type ViewerActivityItem struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Host      string `json:"host,omitempty"`
	EventType string `json:"event_type,omitempty"`
	SourceRef string `json:"source_ref,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type ViewerFirstMemory struct {
	Status  string `json:"status"`
	ID      string `json:"id,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type ViewerGraphProfile struct {
	People         []string              `json:"people"`
	Memories       []string              `json:"memories"`
	Emotions       []string              `json:"emotions"`
	Relationships  []string              `json:"relationships"`
	FunFacts       []string              `json:"fun_facts"`
	PersonProfiles []ViewerPersonProfile `json:"person_profiles"`
}

type ViewerPersonProfile struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Aliases         []string `json:"aliases,omitempty"`
	Summary         string   `json:"summary,omitempty"`
	EmotionalWeight float64  `json:"emotional_weight,omitempty"`
	Facts           []string `json:"facts,omitempty"`
	Relationships   []string `json:"relationships,omitempty"`
	Memories        []string `json:"memories,omitempty"`
}

type ViewerHiddenEntity struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (s *Store) SaveCheckpoint(cp ContinuityCheckpoint) error {
	if s.productTrayRequired() {
		return ErrMemoryTrayRequired
	}
	cp.ThreadID = normalizeThreadID(cp.ThreadID, cp.ProjectID, cp.SessionID)
	cp.SessionID = strings.TrimSpace(cp.SessionID)
	cp.Host = strings.TrimSpace(cp.Host)
	cp.ProjectID = strings.TrimSpace(cp.ProjectID)
	cp.Summary = strings.TrimSpace(cp.Summary)
	if cp.ThreadID == "" {
		return fmt.Errorf("thread_id is required")
	}
	if cp.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if !validHost(cp.Host) {
		return fmt.Errorf("host is unsupported or missing")
	}
	if cp.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if looksLikeTranscript(cp.Summary) || looksSensitiveOrPathLike(cp.Summary) {
		return fmt.Errorf("summary is raw/secret/path-like")
	}
	if cp.Confidence < 0 || cp.Confidence > 1 {
		return fmt.Errorf("confidence must be 0..1")
	}
	if err := validateContinuityStrings("decisions", cp.Decisions); err != nil {
		return err
	}
	if err := validateContinuityStrings("open_loops", cp.OpenLoops); err != nil {
		return err
	}
	if err := validateContinuityStrings("do_not_repeat", cp.DoNotRepeat); err != nil {
		return err
	}
	if err := validateContinuityStrings("emotional_anchors", cp.EmotionalAnchors); err != nil {
		return err
	}
	if err := validateContinuityStrings("state_signals", cp.StateSignals); err != nil {
		return err
	}
	if err := validateContinuityStrings("active_threads", cp.ActiveThreads); err != nil {
		return err
	}
	if err := validateContinuityStrings("review_insights", cp.ReviewInsights); err != nil {
		return err
	}
	if err := validateContinuityRefs("source_refs", cp.SourceRefs); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if cp.CreatedAt == "" {
		cp.CreatedAt = now
	}

	decisions, _ := json.Marshal(cp.Decisions)
	openLoops, _ := json.Marshal(cp.OpenLoops)
	doNotRepeat, _ := json.Marshal(cp.DoNotRepeat)
	emotional, _ := json.Marshal(cp.EmotionalAnchors)
	state, _ := json.Marshal(cp.StateSignals)
	activeThreads, _ := json.Marshal(cp.ActiveThreads)
	reviewInsights, _ := json.Marshal(cp.ReviewInsights)
	refs, _ := json.Marshal(cp.SourceRefs)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertContinuityThread(tx, cp.ThreadID, cp.ProjectID, now); err != nil {
		return err
	}
	if err := upsertContinuitySession(tx, cp.SessionID, cp.ThreadID, cp.Host, cp.ProjectID, now, now, "checkpointed"); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO continuity_checkpoints
		  (thread_id, session_id, host, project_id, summary, decisions_json,
		   open_loops_json, do_not_repeat_json, emotional_anchors_json,
		   state_signals_json, active_threads_json, review_insights_json,
		   source_refs_json, confidence, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cp.ThreadID, cp.SessionID, cp.Host, cp.ProjectID, cp.Summary, string(decisions),
		string(openLoops), string(doNotRepeat), string(emotional), string(state),
		string(activeThreads), string(reviewInsights), string(refs), cp.Confidence, cp.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert checkpoint: %w", err)
	}
	return tx.Commit()
}

func (s *Store) SaveObservation(obs ContinuityObservation, rawRefsEnabled bool) error {
	if s.productTrayRequired() {
		return ErrMemoryTrayRequired
	}
	obs.ThreadID = normalizeThreadID(obs.ThreadID, "", obs.SessionID)
	obs.SessionID = strings.TrimSpace(obs.SessionID)
	obs.Host = strings.TrimSpace(obs.Host)
	obs.EventType = strings.TrimSpace(obs.EventType)
	obs.RedactedSummary = strings.TrimSpace(obs.RedactedSummary)
	obs.RawRef = strings.TrimSpace(obs.RawRef)
	obs.SourceRef = strings.TrimSpace(obs.SourceRef)
	if obs.ThreadID == "" {
		return fmt.Errorf("thread_id is required")
	}
	if obs.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if !validHost(obs.Host) {
		return fmt.Errorf("host is unsupported or missing")
	}
	if !validContinuityEvent(obs.EventType) {
		return fmt.Errorf("event_type is unsupported or missing")
	}
	if obs.RedactedSummary == "" {
		return fmt.Errorf("redacted_summary is required")
	}
	if looksLikeTranscript(obs.RedactedSummary) || looksSensitiveOrPathLike(obs.RedactedSummary) {
		return fmt.Errorf("redacted_summary is raw/secret/path-like")
	}
	if obs.RawRef != "" && !rawRefsEnabled {
		return fmt.Errorf("raw_ref requires explicit local raw refs mode")
	}
	if obs.RawRef != "" && !validContinuityRef(obs.RawRef) {
		return fmt.Errorf("raw_ref is unsafe")
	}
	if obs.SourceRef != "" && !validContinuityRef(obs.SourceRef) {
		return fmt.Errorf("source_ref is unsafe")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if obs.CreatedAt == "" {
		obs.CreatedAt = now
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertContinuityThread(tx, obs.ThreadID, "", now); err != nil {
		return err
	}
	if err := upsertContinuitySession(tx, obs.SessionID, obs.ThreadID, obs.Host, "", now, "", "active"); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO continuity_observations
		  (session_id, thread_id, host, event_type, redacted_summary, raw_ref, source_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		obs.SessionID, obs.ThreadID, obs.Host, obs.EventType, obs.RedactedSummary,
		nullableString(obs.RawRef), nullableString(obs.SourceRef), obs.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert observation: %w", err)
	}
	return tx.Commit()
}

func (s *Store) BuildResume(q ResumeQuery) (ResumeBlock, error) {
	var requestedScope *PersonalMemoryScopeSnapshot
	if scope, scoped, err := s.CurrentPersonalMemoryScopeSnapshot(); err != nil {
		return ResumeBlock{}, err
	} else if scoped {
		requestedScope = &scope
	}
	return s.buildResume(q, requestedScope)
}

// BuildResumeForPersonalScope assembles continuity against a server-verified
// request boundary. The scope is captured before candidate generation so a
// second project's rows cannot influence search, ranking, counts, or tokens.
func (s *Store) BuildResumeForPersonalScope(
	q ResumeQuery,
	scope PersonalMemoryScopeSnapshot,
) (ResumeBlock, error) {
	if scope.ProjectNamespaceID != stableProjectNamespace(scope.RepositoryID) ||
		!trayBindingDigestPattern.MatchString(scope.BindingDigest) ||
		!validTrayIdentifier(scope.RepositoryID) || scope.EligibilityRevision < 1 {
		return ResumeBlock{}, ErrContinuityDeliveryAuthority
	}
	return s.buildResume(q, &scope)
}

func (s *Store) buildResume(
	q ResumeQuery,
	requestedScope *PersonalMemoryScopeSnapshot,
) (ResumeBlock, error) {
	threadID := normalizeThreadID(q.ThreadID, q.ProjectID, q.SessionID)
	budget := q.TokenBudget
	if budget <= 0 {
		budget = 1200
	}
	if budget > 2000 {
		budget = 2000
	}
	if budget < 400 {
		budget = 400
	}

	cp, hasCheckpoint, err := s.latestCheckpoint(threadID)
	if err != nil {
		return ResumeBlock{}, err
	}
	observations, err := s.recentObservations(threadID, 5)
	if err != nil {
		return ResumeBlock{}, err
	}
	memories, err := s.recentResumeMemoriesForScope(8, requestedScope)
	if err != nil {
		return ResumeBlock{}, err
	}

	sections := ResumeSections{}
	if hasCheckpoint {
		sections.WhereWeLeftOff = []string{cp.Summary}
		sections.ActiveDecisions = cp.Decisions
		sections.ActiveReviewedThreads = cp.ActiveThreads
		sections.ReviewInsights = activeReviewInsights(cp.ReviewInsights, cp.ActiveThreads)
		sections.OpenLoops = cp.OpenLoops
		sections.DoNotRepeat = cp.DoNotRepeat
		sections.RelevantEmotionalState = append(sections.RelevantEmotionalState, cp.EmotionalAnchors...)
		sections.RelevantEmotionalState = append(sections.RelevantEmotionalState, cp.StateSignals...)
		sections.EvidenceRefs = append(sections.EvidenceRefs, cp.SourceRefs...)
		sections.EvidenceRefs = append(sections.EvidenceRefs, fmt.Sprintf("pulse:checkpoint:%d", cp.ID))
		sections.MaterialRefs = append(sections.MaterialRefs, materialRefsFromCheckpoint(cp)...)
	} else {
		sections.WhereWeLeftOff = []string{"No Pulse checkpoint exists for this thread yet."}
	}
	for _, obs := range observations {
		if promoteContinuityObservation(&sections, obs.RedactedSummary) {
			// The host already approved this memory capsule as redacted structured
			// content, so keep it in the resume section instead of the generic
			// hook-log bucket.
		} else {
			title, summary := humanizeObservation(obs)
			sections.RecentLocalAutoObservations = append(sections.RecentLocalAutoObservations, title+": "+summary)
		}
		if obs.SourceRef != "" {
			sections.EvidenceRefs = appendUniqueContinuityItem(sections.EvidenceRefs, obs.SourceRef)
		}
	}
	promotedMemories := make([]RecalledMemoryItem, 0, len(memories))
	for _, memory := range memories {
		promoted := promoteMemoryCapsuleToResume(&sections, memory)
		if promoted {
			promotedMemories = append(promotedMemories, memory)
		}
		if memory.EvidenceRef != "" && promoted {
			sections.EvidenceRefs = appendUniqueContinuityItem(sections.EvidenceRefs, memory.EvidenceRef)
		}
	}
	if len(sections.OpenLoops) > 0 {
		sections.SuggestedNextStep = []string{"Continue with: " + sections.OpenLoops[0]}
	} else {
		sections.SuggestedNextStep = []string{"Ask what changed since the last Pulse checkpoint, then continue from the current user request."}
	}
	sections.MaterialRefs = compactMaterialRefs(append(sections.MaterialRefs, materialRefsFromResumeSections(sections)...))

	// Cross-harness digest ("what's cooking across your harnesses") — the
	// new-empty-chat greeting. Honest per-host activity + a fun fact; empty when
	// nothing has been captured (no fabrication).
	sections.HarnessActivity = s.harnessActivityForScope(3, requestedScope)
	if len(sections.HarnessActivity) > 0 {
		if ff, _ := s.viewerGraphFunFactsForScope(1, requestedScope); len(ff) > 0 {
			sections.HarnessActivity = append(sections.HarnessActivity, "🌱 "+ff[0])
		}
	}

	fullMarkdown := renderResumeMarkdown(sections)
	markdown := trimMarkdownToBudget(fullMarkdown, budget)
	includedObjectIDs, includedEvidenceIDs := includedResumeManifest(markdown, promotedMemories, sections.EvidenceRefs)
	fullObjectIDs, fullEvidenceIDs := includedResumeManifest(fullMarkdown, promotedMemories, sections.EvidenceRefs)
	coverageCounted := len(includedObjectIDs) + len(includedEvidenceIDs)
	coverageTotal := len(fullObjectIDs) + len(fullEvidenceIDs)
	baselineKind := ""
	var sourceEquivalentTokens *int
	if coverageCounted > 0 && coverageTotal >= coverageCounted {
		baselineKind = "canonical_structured_resume_v1"
		value := estimateTokens(fullMarkdown)
		sourceEquivalentTokens = &value
	}
	tokenEstimate := estimateTokens(markdown)
	tokenEconomy := TokenEconomy{
		State:         TokenEconomyCollectingBaseline,
		MethodID:      TokenEconomyMethodUTF8BytesDiv4Ceil,
		MethodVersion: "1",
		RenderedBytes: len([]byte(markdown)),
		PulseTokens:   tokenEstimate,
		ReasonCode:    "comparable_receipt_required",
	}
	memorySnapshotDigest := ""
	if requestedScope != nil {
		memorySnapshotDigest = requestedScope.Digest()
	}
	return ResumeBlock{
		Schema:                 ContinuitySchema,
		ThreadID:               threadID,
		ProjectID:              strings.TrimSpace(q.ProjectID),
		SessionID:              strings.TrimSpace(q.SessionID),
		TokenBudget:            budget,
		TokenEstimate:          tokenEstimate,
		TokenEconomy:           tokenEconomy,
		ResumeMarkdown:         markdown,
		Sections:               sections,
		EvidenceRefs:           sections.EvidenceRefs,
		MaterialRefs:           sections.MaterialRefs,
		IncludedObjectIDs:      includedObjectIDs,
		IncludedEvidenceIDs:    includedEvidenceIDs,
		BaselineKind:           baselineKind,
		SourceEquivalentTokens: sourceEquivalentTokens,
		CoverageCounted:        coverageCounted,
		CoverageTotal:          coverageTotal,
		MemorySnapshotDigest:   memorySnapshotDigest,
	}, nil
}

func activeReviewInsights(insights []string, activeThreads []string) []string {
	if len(insights) == 0 || len(activeThreads) == 0 {
		return nil
	}
	normalizedThreads := make([]string, 0, len(activeThreads))
	for _, thread := range activeThreads {
		thread = strings.ToLower(strings.TrimSpace(thread))
		if thread != "" {
			normalizedThreads = append(normalizedThreads, thread)
		}
	}
	if len(normalizedThreads) == 0 {
		return nil
	}
	out := make([]string, 0, len(insights))
	for _, insight := range insights {
		trimmed := strings.TrimSpace(insight)
		if trimmed == "" {
			continue
		}
		normalizedInsight := strings.ToLower(trimmed)
		for _, thread := range normalizedThreads {
			if strings.Contains(normalizedInsight, thread) {
				out = append(out, trimmed)
				break
			}
		}
	}
	return out
}

func (s *Store) ViewerData(q ResumeQuery) (ViewerData, error) {
	resume, err := s.BuildResume(q)
	if err != nil {
		return ViewerData{}, err
	}
	threadID := resume.ThreadID
	sessions, err := s.RecentContinuitySessions(threadID, 10)
	if err != nil {
		return ViewerData{}, err
	}
	activity, err := s.ViewerActivity(threadID, 10)
	if err != nil {
		return ViewerData{}, err
	}
	graph, err := s.ViewerGraphProfile(24)
	if err != nil {
		return ViewerData{}, err
	}
	materialGraph, err := s.MaterialGraph(MaterialGraphQuery{
		ThreadID:  threadID,
		ProjectID: q.ProjectID,
		SessionID: q.SessionID,
		Limit:     50,
	})
	if err != nil {
		return ViewerData{}, err
	}
	hidden, err := s.ViewerHiddenEntities(24)
	if err != nil {
		return ViewerData{}, err
	}
	firstMemory, err := s.ViewerFirstMemory()
	if err != nil {
		return ViewerData{}, err
	}
	var memoryTray []MemoryTrayCandidateView
	if s.productTrayRequired() {
		memoryTray, err = s.ListMemoryTray(50)
		if err != nil {
			return ViewerData{}, err
		}
	}
	return ViewerData{
		NextResume:       resume,
		FirstMemory:      firstMemory,
		RecentSessions:   sessions,
		Activity:         activity,
		OpenLoops:        resume.Sections.OpenLoops,
		SavedDecisions:   resume.Sections.ActiveDecisions,
		EmotionalAnchors: resume.Sections.RelevantEmotionalState,
		GraphProfile:     graph,
		MaterialGraph:    materialGraph,
		HiddenEntities:   hidden,
		MemoryTray:       memoryTray,
	}, nil
}

func (s *Store) ViewerFirstMemory() (ViewerFirstMemory, error) {
	rows, err := s.db.Query(`
		SELECT id, redacted_summary, kind, confidence, privacy_tier, retention, tags, created_at
		  FROM memory_capsules
		 WHERE source_host='pulse-cli'
		   AND kind='system_event'
		   AND redacted_summary LIKE '%installed Pulse MCP%'
		 ORDER BY created_at DESC
		 LIMIT 20`)
	if err != nil {
		return ViewerFirstMemory{}, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanRecalledMemory(rows)
		if err != nil {
			return ViewerFirstMemory{}, err
		}
		if stringSliceContains(item.Tags, "pulse_install") && stringSliceContains(item.Tags, "first_memory") {
			return ViewerFirstMemory{
				Status:  "saved",
				ID:      item.ID,
				Summary: item.Summary,
			}, nil
		}
	}
	if err := rows.Err(); err != nil {
		return ViewerFirstMemory{}, err
	}
	return ViewerFirstMemory{Status: "pending"}, nil
}

func (s *Store) ViewerActivity(threadID string, limit int) ([]ViewerActivityItem, error) {
	observations, err := s.recentObservations(threadID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ViewerActivityItem, 0, len(observations))
	for _, obs := range observations {
		out = append(out, humanReadableActivity(obs))
	}
	return out, nil
}

func humanReadableActivity(obs ContinuityObservation) ViewerActivityItem {
	title, summary := humanizeObservation(obs)
	return ViewerActivityItem{
		ID:        obs.ID,
		Title:     title,
		Summary:   summary,
		Host:      obs.Host,
		EventType: obs.EventType,
		SourceRef: obs.SourceRef,
		CreatedAt: obs.CreatedAt,
	}
}

func humanizeObservation(obs ContinuityObservation) (string, string) {
	raw := strings.TrimSpace(obs.RedactedSummary)
	if label, text, ok := splitTypedContinuityObservation(raw); ok {
		switch label {
		case "Decision":
			return "Saved decision", text
		case "Open loop":
			return "Saved open loop", text
		case "Do-not-repeat":
			return "Saved do-not-repeat note", text
		case "Preference":
			return "Saved preference", text
		case "Correction":
			return "Saved correction", text
		case "Project state":
			return "Saved project state", text
		case "Relationship note":
			return "Saved relationship note", text
		case "State signal", "Emotional anchor":
			return "Saved state context", text
		case "System event":
			return "Saved Pulse event", text
		case "Fact":
			return "Saved fact", text
		}
	}
	if title, summary, ok := humanizeLegacyObservation(raw); ok {
		return title, summary
	}
	switch strings.TrimSpace(obs.EventType) {
	case "SessionStart":
		return "Session started", "Pulse prepared the next-session context."
	case "UserPromptSubmit":
		return "Prompt noticed", "Pulse noticed a new prompt; raw text is hidden."
	case "PostToolUse":
		return "Tool activity recorded", "Pulse noticed tool activity; raw tool input and output are hidden."
	case "Stop":
		return "Checkpoint requested", "Pulse prepared to save where the session left off."
	case "SessionEnd":
		return "Session ended", "Pulse recorded that the local session ended."
	default:
		return "Local activity recorded", "Pulse recorded a local activity event. Raw details are hidden."
	}
}

func humanizeLegacyObservation(raw string) (string, string, bool) {
	normalized := strings.TrimSpace(raw)
	lower := strings.ToLower(normalized)
	switch {
	case lower == "event_triggered_llm_logged":
		return "AI activity recorded", "Pulse recorded a background model event for this session. Raw prompt and transcript are hidden.", true
	case strings.HasPrefix(lower, "event_triggered_"):
		return "Background activity recorded", "Pulse recorded a background event for this session. Raw details are hidden.", true
	case strings.HasPrefix(normalized, "UserPromptSubmit:"):
		return "Prompt noticed", "Pulse noticed a new prompt; raw text is hidden.", true
	case strings.HasPrefix(normalized, "PostToolUse:"):
		text := strings.TrimSpace(strings.TrimPrefix(normalized, "PostToolUse:"))
		tool := strings.TrimSpace(strings.TrimSuffix(text, "ran. Raw tool input/output is hidden by default."))
		if tool != "" && tool != text {
			return "Tool ran: " + tool, "Raw tool input and output are hidden.", true
		}
		return "Tool activity recorded", "Pulse noticed tool activity; raw tool input and output are hidden.", true
	case strings.HasPrefix(normalized, "SessionStart:"):
		return "Session started", "Pulse prepared the next-session context.", true
	case strings.HasPrefix(normalized, "Stop:"):
		return "Checkpoint requested", "Pulse prepared to save where the session left off.", true
	case strings.HasPrefix(normalized, "SessionEnd:"):
		return "Session ended", "Pulse recorded that the local session ended.", true
	default:
		return "", "", false
	}
}

func stringSliceContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func (s *Store) ViewerHiddenEntities(limit int) ([]ViewerHiddenEntity, error) {
	if limit <= 0 || limit > 100 {
		limit = 24
	}
	rows, err := s.db.Query(`
		SELECT e.id, e.canonical_name, e.kind
		  FROM sensitive_actors sa
		  JOIN entities e ON e.id = sa.entity_id
		 ORDER BY sa.added_at DESC, e.canonical_name ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ViewerHiddenEntity{}
	for rows.Next() {
		var item ViewerHiddenEntity
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ViewerGraphProfile(limit int) (ViewerGraphProfile, error) {
	if limit <= 0 || limit > 100 {
		limit = 24
	}
	people, err := s.viewerGraphPeople(limit)
	if err != nil {
		return ViewerGraphProfile{}, err
	}
	memories, err := s.viewerGraphMemories(limit)
	if err != nil {
		return ViewerGraphProfile{}, err
	}
	relationships, err := s.viewerGraphRelationships(limit)
	if err != nil {
		return ViewerGraphProfile{}, err
	}
	emotions, err := s.viewerGraphEmotions(limit)
	if err != nil {
		return ViewerGraphProfile{}, err
	}
	funFacts, err := s.viewerGraphFunFacts(limit)
	if err != nil {
		return ViewerGraphProfile{}, err
	}
	personProfiles, err := s.viewerGraphPersonProfiles(limit)
	if err != nil {
		return ViewerGraphProfile{}, err
	}
	return ViewerGraphProfile{
		People:         people,
		Memories:       memories,
		Emotions:       emotions,
		Relationships:  relationships,
		FunFacts:       funFacts,
		PersonProfiles: personProfiles,
	}, nil
}

func (s *Store) viewerGraphPersonProfiles(limit int) ([]ViewerPersonProfile, error) {
	rows, err := s.db.Query(`
		SELECT id, canonical_name, COALESCE(aliases, '[]'), COALESCE(description_md, ''), emotional_weight
		  FROM entities
		 WHERE kind = 'person'
		   AND NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = entities.id)
		 ORDER BY salience_score DESC, last_seen DESC, canonical_name ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	profiles := []ViewerPersonProfile{}
	for rows.Next() {
		var id int64
		var aliasesJSON string
		var profile ViewerPersonProfile
		if err := rows.Scan(&id, &profile.Name, &aliasesJSON, &profile.Summary, &profile.EmotionalWeight); err != nil {
			return nil, err
		}
		if !viewerHumanFacingPersonLabel(profile.Name) {
			continue
		}
		profile.ID = id
		profile.Aliases = viewerFilteredStrings(parseSemanticAliases(aliasesJSON), limit, viewerHumanFacingPersonLabel)
		profile.Facts, err = s.viewerPersonFacts(id, 8)
		if err != nil {
			return nil, err
		}
		profile.Relationships, err = s.viewerPersonRelationships(id, 8)
		if err != nil {
			return nil, err
		}
		profile.Memories, err = s.viewerPersonMemories(id, 8)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *Store) viewerGraphPeople(limit int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT canonical_name
		  FROM entities
		 WHERE kind = 'person'
		   AND NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = entities.id)
		 ORDER BY salience_score DESC, last_seen DESC, canonical_name ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRowsFiltered(rows, limit, viewerHumanFacingPersonLabel)
}

func (s *Store) viewerGraphMemories(limit int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT title
		  FROM events
		 ORDER BY ts DESC, id DESC
		 LIMIT ?`, limit*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRowsFiltered(rows, limit, viewerHumanReadableLabel)
}

func (s *Store) viewerGraphRelationships(limit int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT fe.canonical_name, r.kind, te.canonical_name
		  FROM relations r
		  JOIN entities fe ON fe.id = r.from_entity_id
		  JOIN entities te ON te.id = r.to_entity_id
		 WHERE NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = fe.id)
		   AND NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = te.id)
		 ORDER BY r.strength DESC, r.last_seen DESC, fe.canonical_name ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelationRows(rows, limit)
}

func (s *Store) viewerGraphEmotions(limit int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT sentiment, MAX(emotional_weight) AS weight, MAX(ts) AS latest
		  FROM events
		 WHERE COALESCE(sentiment, '') <> ''
		 GROUP BY sentiment
		 ORDER BY weight DESC, latest DESC, sentiment ASC
		 LIMIT ?`, limit*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEmotionRows(rows, limit)
}

// relAgo renders an RFC3339 timestamp as a coarse relative age (m/h/d ago).
func relAgo(ts string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(ts))
	if err != nil {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// harnessActivity summarizes recent per-harness activity for the resume digest
// ("what's cooking across your harnesses"). Read-only over continuity_checkpoints
// (host + summary + created_at). Honest empty-states; best-effort (errors → nil).
func (s *Store) harnessActivity(limit int) []string {
	scope, scoped, err := s.CurrentPersonalMemoryScopeSnapshot()
	if err != nil {
		return nil
	}
	if !scoped {
		return s.harnessActivityForScope(limit, nil)
	}
	return s.harnessActivityForScope(limit, &scope)
}

func (s *Store) harnessActivityForScope(
	limit int,
	requestedScope *PersonalMemoryScopeSnapshot,
) []string {
	query := `
		SELECT host, COUNT(*) n, MAX(created_at) last
		FROM continuity_checkpoints WHERE host != '' GROUP BY host ORDER BY last DESC`
	args := []any{}
	if requestedScope != nil {
		query = `
			SELECT checkpoint.host, COUNT(*) n, MAX(checkpoint.created_at) last
			  FROM continuity_checkpoints checkpoint
			  JOIN private_semantic_projection_rows projection
			    ON projection.row_kind='checkpoint'
			   AND projection.row_ref=CAST(checkpoint.id AS TEXT)
			  JOIN private_memory_objects object ON object.object_id=projection.object_id
			 WHERE checkpoint.host!=''
			   AND object.lifecycle='active'
			   AND (
			       object.memory_scope='personal_global' OR
			       (object.memory_scope='project' AND object.project_namespace_id=?)
			   )
			 GROUP BY checkpoint.host
			 ORDER BY last DESC`
		args = append(args, requestedScope.ProjectNamespaceID)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	type h struct {
		host, last string
		n          int
	}
	var hs []h
	for rows.Next() {
		var x h
		if rows.Scan(&x.host, &x.n, &x.last) == nil {
			hs = append(hs, x)
		}
	}
	if len(hs) == 0 {
		return nil
	}
	var out []string
	for _, x := range hs {
		if limit > 0 && len(out) >= limit {
			break
		}
		var summary string
		if requestedScope != nil {
			_ = s.db.QueryRow(`
				SELECT checkpoint.summary
				  FROM continuity_checkpoints checkpoint
				  JOIN private_semantic_projection_rows projection
				    ON projection.row_kind='checkpoint'
				   AND projection.row_ref=CAST(checkpoint.id AS TEXT)
				  JOIN private_memory_objects object ON object.object_id=projection.object_id
				 WHERE checkpoint.host=?
				   AND object.lifecycle='active'
				   AND (
				       object.memory_scope='personal_global' OR
				       (object.memory_scope='project' AND object.project_namespace_id=?)
				   )
				 ORDER BY checkpoint.created_at DESC, checkpoint.id DESC
				 LIMIT 1`, x.host, requestedScope.ProjectNamespaceID).Scan(&summary)
		} else {
			_ = s.db.QueryRow(`SELECT summary FROM continuity_checkpoints WHERE host=? ORDER BY created_at DESC LIMIT 1`, x.host).Scan(&summary)
		}
		summary = strings.TrimSpace(summary)
		if r := []rune(summary); len(r) > 80 {
			summary = string(r[:80]) + "…"
		}
		line := fmt.Sprintf("%s — last active %s, %d checkpoints", x.host, relAgo(x.last), x.n)
		if summary != "" {
			line += "; latest: " + summary
		}
		out = append(out, line)
	}
	return out
}

func (s *Store) viewerGraphFunFacts(limit int) ([]string, error) {
	scope, scoped, err := s.CurrentPersonalMemoryScopeSnapshot()
	if err != nil {
		return nil, err
	}
	if !scoped {
		return s.viewerGraphFunFactsForScope(limit, nil)
	}
	return s.viewerGraphFunFactsForScope(limit, &scope)
}

func (s *Store) viewerGraphFunFactsForScope(
	limit int,
	requestedScope *PersonalMemoryScopeSnapshot,
) ([]string, error) {
	query := `
		SELECT f.text
		  FROM facts f
		  JOIN entities e ON e.id = f.entity_id
		 WHERE NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = e.id)
		 ORDER BY f.confidence DESC, f.created_at DESC, e.canonical_name ASC
		 LIMIT ?`
	args := []any{limit}
	if requestedScope != nil {
		query = `
			SELECT fact.text
			  FROM facts fact
			  JOIN entities entity ON entity.id=fact.entity_id
			  JOIN private_semantic_projection_rows projection
			    ON projection.row_kind='fact'
			   AND projection.row_ref=CAST(fact.id AS TEXT)
			  JOIN private_memory_objects object ON object.object_id=projection.object_id
			 WHERE object.lifecycle='active'
			   AND (
			       object.memory_scope='personal_global' OR
			       (object.memory_scope='project' AND object.project_namespace_id=?)
			   )
			   AND NOT EXISTS (
			       SELECT 1 FROM sensitive_actors actor
			        WHERE actor.entity_id=entity.id
			   )
			 ORDER BY fact.confidence DESC, fact.created_at DESC, entity.canonical_name ASC
			 LIMIT ?`
		args = []any{requestedScope.ProjectNamespaceID, limit}
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRowsFiltered(rows, limit, viewerHumanReadableLabel)
}

func (s *Store) viewerPersonFacts(entityID int64, limit int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT text
		  FROM facts
		 WHERE entity_id = ?
		 ORDER BY confidence DESC, created_at DESC, text ASC
		 LIMIT ?`, entityID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRowsFiltered(rows, limit, viewerHumanReadableLabel)
}

func (s *Store) HideGraphEntity(entityID int64) error {
	if s.productTrayRequired() {
		return ErrMemoryTrayRequired
	}
	if entityID <= 0 {
		return fmt.Errorf("id is required")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(`
		INSERT INTO sensitive_actors(entity_id, policy, reason, added_at, added_by)
		VALUES (?, 'no_capture', 'hidden from Pulse viewer', ?, 'viewer')
		ON CONFLICT(entity_id) DO UPDATE SET
		  policy = 'no_capture',
		  reason = excluded.reason`,
		entityID, now)
	if err != nil {
		return fmt.Errorf("hide graph entity: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("entity not found")
	}
	return nil
}

func (s *Store) RestoreGraphEntity(entityID int64) error {
	if s.productTrayRequired() {
		return ErrMemoryTrayRequired
	}
	if entityID <= 0 {
		return fmt.Errorf("id is required")
	}
	res, err := s.db.Exec(`DELETE FROM sensitive_actors WHERE entity_id = ?`, entityID)
	if err != nil {
		return fmt.Errorf("restore graph entity: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("entity was not hidden")
	}
	return nil
}

func (s *Store) viewerPersonRelationships(entityID int64, limit int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT fe.canonical_name, r.kind, te.canonical_name
		  FROM relations r
		  JOIN entities fe ON fe.id = r.from_entity_id
		  JOIN entities te ON te.id = r.to_entity_id
		 WHERE (r.from_entity_id = ? OR r.to_entity_id = ?)
		   AND NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = fe.id)
		   AND NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = te.id)
		 ORDER BY r.strength DESC, r.last_seen DESC, fe.canonical_name ASC
		 LIMIT ?`, entityID, entityID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelationRows(rows, limit)
}

func (s *Store) viewerPersonMemories(entityID int64, limit int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT e.title
		  FROM events e
		  JOIN event_entities ee ON ee.event_id = e.id
		 WHERE ee.entity_id = ?
		 ORDER BY e.ts DESC, e.id DESC
		 LIMIT ?`, entityID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRowsFiltered(rows, limit, viewerHumanReadableLabel)
}

func scanStringRows(rows *sql.Rows) ([]string, error) {
	out := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out, rows.Err()
}

func scanStringRowsFiltered(rows *sql.Rows, limit int, allow func(string) bool) ([]string, error) {
	out := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		value = strings.TrimSpace(value)
		if value == "" || !allow(value) {
			continue
		}
		out = append(out, value)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

func scanRelationRows(rows *sql.Rows, limit int) ([]string, error) {
	out := []string{}
	for rows.Next() {
		var from, kind, to string
		if err := rows.Scan(&from, &kind, &to); err != nil {
			return nil, err
		}
		from = strings.TrimSpace(from)
		to = strings.TrimSpace(to)
		if from == "" || to == "" {
			continue
		}
		if !viewerRelationshipEndpointLabel(from) || !viewerRelationshipEndpointLabel(to) {
			continue
		}
		out = append(out, formatViewerRelation(from, kind, to))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

func formatViewerRelation(from, kind, to string) string {
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "mentioned_in":
		return fmt.Sprintf("%s mentioned in %s", from, to)
	case "related_to", "":
		return fmt.Sprintf("%s related to %s", from, to)
	default:
		return fmt.Sprintf("%s %s %s", from, humanizeRelationKind(kind), to)
	}
}

func viewerFilteredStrings(items []string, limit int, allow func(string) bool) []string {
	out := []string{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || !allow(item) {
			continue
		}
		out = append(out, item)
		if limit > 0 && len(out) >= limit {
			return out
		}
	}
	return out
}

func viewerHumanFacingPersonLabel(value string) bool {
	value = strings.TrimSpace(value)
	if !viewerHumanReadableLabel(value) {
		return false
	}
	key := strings.ToLower(value)
	if viewerBlockedPersonLabels[key] {
		return false
	}
	letters := 0
	for _, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= 'А' && r <= 'я') || r == 'Ё' || r == 'ё' {
			letters++
		}
	}
	return letters >= 3
}

func viewerRelationshipEndpointLabel(value string) bool {
	value = strings.TrimSpace(value)
	if !viewerHumanReadableLabel(value) {
		return false
	}
	return !viewerBlockedPersonLabels[strings.ToLower(value)]
}

func viewerHumanReadableLabel(value string) bool {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if value == "" || len(value) > 180 {
		return false
	}
	if viewerUUIDPattern.MatchString(lower) || looksSensitiveOrPathLike(value) {
		return false
	}
	if strings.HasPrefix(lower, "source:") || strings.HasPrefix(lower, "file:") || strings.HasPrefix(lower, "http:") || strings.HasPrefix(lower, "https:") {
		return false
	}
	if strings.Contains(lower, "://") || strings.Contains(lower, "/users/") || strings.Contains(lower, "source:memory/") {
		return false
	}
	if strings.HasPrefix(lower, "session-") || strings.HasPrefix(lower, "agent-") || strings.HasPrefix(lower, "rollout-") {
		return false
	}
	if strings.Count(value, "-") >= 4 && len(value) >= 24 {
		return false
	}
	return true
}

func scanEmotionRows(rows *sql.Rows, limit int) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var raw string
		var weight float64
		var latest string
		if err := rows.Scan(&raw, &weight, &latest); err != nil {
			return nil, err
		}
		_ = weight
		_ = latest
		for _, emotion := range splitEmotionLabels(raw) {
			key := strings.ToLower(emotion)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, emotion)
			if len(out) >= limit {
				return out, rows.Err()
			}
		}
	}
	return out, rows.Err()
}

func splitEmotionLabels(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', '|', '/', '\n', '\r', '\t':
			return true
		default:
			return false
		}
	})
	out := []string{}
	for _, field := range fields {
		emotion := strings.TrimSpace(field)
		if emotion != "" {
			out = append(out, emotion)
		}
	}
	return out
}

func humanizeRelationKind(kind string) string {
	kind = strings.TrimSpace(strings.ToLower(kind))
	switch kind {
	case "mentioned_in":
		return "mentioned in"
	case "related_to":
		return "related to"
	case "":
		return "related to"
	default:
		return strings.ReplaceAll(kind, "_", " ")
	}
}

func (s *Store) RecentContinuitySessions(threadID string, limit int) ([]ContinuitySession, error) {
	threadID = strings.TrimSpace(threadID)
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.Query(`
		SELECT session_id, thread_id, host, COALESCE(project_id, ''), started_at,
		       COALESCE(ended_at, ''), status
		  FROM continuity_sessions
		 WHERE (? = '' OR thread_id = ?)
		 ORDER BY started_at DESC
		 LIMIT ?`, threadID, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContinuitySession{}
	for rows.Next() {
		var sess ContinuitySession
		if err := rows.Scan(&sess.SessionID, &sess.ThreadID, &sess.Host, &sess.ProjectID, &sess.StartedAt, &sess.EndedAt, &sess.Status); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) WipeContinuity() error {
	if s.productTrayRequired() {
		return ErrMemoryTrayRequired
	}
	_, err := s.db.Exec(`
		DELETE FROM continuity_observations;
		DELETE FROM continuity_checkpoints;
		DELETE FROM continuity_sessions;
		DELETE FROM continuity_threads;`)
	return err
}

func (s *Store) latestCheckpoint(threadID string) (ContinuityCheckpoint, bool, error) {
	var cp ContinuityCheckpoint
	var decisions, openLoops, doNotRepeat, emotional, state, activeThreads, reviewInsights, refs string
	scope, scoped, err := s.CurrentPersonalMemoryScopeSnapshot()
	if err != nil {
		return ContinuityCheckpoint{}, false, err
	}
	query := `
		SELECT id, thread_id, session_id, host, COALESCE(project_id, ''), summary,
		       decisions_json, open_loops_json, do_not_repeat_json, emotional_anchors_json,
		       state_signals_json, active_threads_json, review_insights_json,
		       source_refs_json, confidence, created_at
		  FROM continuity_checkpoints
		 WHERE thread_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`
	args := []any{threadID}
	if scoped {
		query = `
			SELECT checkpoint.id, checkpoint.thread_id, checkpoint.session_id,
			       checkpoint.host, COALESCE(checkpoint.project_id, ''), checkpoint.summary,
			       checkpoint.decisions_json, checkpoint.open_loops_json,
			       checkpoint.do_not_repeat_json, checkpoint.emotional_anchors_json,
			       checkpoint.state_signals_json, checkpoint.active_threads_json,
			       checkpoint.review_insights_json, checkpoint.source_refs_json,
			       checkpoint.confidence, checkpoint.created_at
			  FROM continuity_checkpoints checkpoint
			  JOIN private_semantic_projection_rows projection
			    ON projection.row_kind='checkpoint'
			   AND projection.row_ref=CAST(checkpoint.id AS TEXT)
			  JOIN private_memory_objects object ON object.object_id=projection.object_id
			 WHERE checkpoint.thread_id=?
			   AND object.lifecycle='active'
			   AND (
			       object.memory_scope='personal_global' OR
			       (object.memory_scope='project' AND object.project_namespace_id=?)
			   )
			 ORDER BY checkpoint.created_at DESC, checkpoint.id DESC
			 LIMIT 1`
		args = []any{threadID, scope.ProjectNamespaceID}
	}
	err = s.db.QueryRow(query, args...).Scan(
		&cp.ID, &cp.ThreadID, &cp.SessionID, &cp.Host, &cp.ProjectID, &cp.Summary,
		&decisions, &openLoops, &doNotRepeat, &emotional, &state, &activeThreads, &reviewInsights, &refs,
		&cp.Confidence, &cp.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return ContinuityCheckpoint{}, false, nil
	}
	if err != nil {
		return ContinuityCheckpoint{}, false, err
	}
	_ = json.Unmarshal([]byte(decisions), &cp.Decisions)
	_ = json.Unmarshal([]byte(openLoops), &cp.OpenLoops)
	_ = json.Unmarshal([]byte(doNotRepeat), &cp.DoNotRepeat)
	_ = json.Unmarshal([]byte(emotional), &cp.EmotionalAnchors)
	_ = json.Unmarshal([]byte(state), &cp.StateSignals)
	_ = json.Unmarshal([]byte(activeThreads), &cp.ActiveThreads)
	_ = json.Unmarshal([]byte(reviewInsights), &cp.ReviewInsights)
	_ = json.Unmarshal([]byte(refs), &cp.SourceRefs)
	return cp, true, nil
}

func (s *Store) recentObservations(threadID string, limit int) ([]ContinuityObservation, error) {
	scope, scoped, err := s.CurrentPersonalMemoryScopeSnapshot()
	if err != nil {
		return nil, err
	}
	query := `
		SELECT id, session_id, thread_id, host, event_type, redacted_summary,
		       COALESCE(raw_ref, ''), COALESCE(source_ref, ''), created_at
		  FROM continuity_observations
		 WHERE thread_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT ?`
	args := []any{threadID, limit}
	if scoped {
		query = `
			SELECT observation.id, observation.session_id, observation.thread_id,
			       observation.host, observation.event_type, observation.redacted_summary,
			       COALESCE(observation.raw_ref, ''), COALESCE(observation.source_ref, ''),
			       observation.created_at
			  FROM continuity_observations observation
			  JOIN private_semantic_projection_rows projection
			    ON projection.row_kind='session'
			   AND projection.row_ref=observation.session_id
			  JOIN private_memory_objects object ON object.object_id=projection.object_id
			 WHERE observation.thread_id=?
			   AND object.lifecycle='active'
			   AND (
			       object.memory_scope='personal_global' OR
			       (object.memory_scope='project' AND object.project_namespace_id=?)
			   )
			 ORDER BY observation.created_at DESC, observation.id DESC
			 LIMIT ?`
		args = []any{threadID, scope.ProjectNamespaceID, limit}
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContinuityObservation{}
	for rows.Next() {
		var obs ContinuityObservation
		if err := rows.Scan(&obs.ID, &obs.SessionID, &obs.ThreadID, &obs.Host, &obs.EventType, &obs.RedactedSummary, &obs.RawRef, &obs.SourceRef, &obs.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, obs)
	}
	return out, rows.Err()
}

func (s *Store) recentResumeMemories(limit int) ([]RecalledMemoryItem, error) {
	var requestedScope *PersonalMemoryScopeSnapshot
	if scope, scoped, err := s.CurrentPersonalMemoryScopeSnapshot(); err != nil {
		return nil, err
	} else if scoped {
		requestedScope = &scope
	}
	return s.recentResumeMemoriesForScope(limit, requestedScope)
}

func (s *Store) recentResumeMemoriesForScope(
	limit int,
	requestedScope *PersonalMemoryScopeSnapshot,
) ([]RecalledMemoryItem, error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	query := `
		SELECT id, redacted_summary, kind, confidence, privacy_tier, retention, tags, created_at
		  FROM memory_capsules
		 WHERE kind IN ('fact', 'decision', 'preference', 'project_state', 'open_loop',
		                'correction', 'relationship_note', 'do_not_repeat', 'state_signal')
		 ORDER BY created_at DESC
		 LIMIT ?`
	args := []any{limit}
	if requestedScope != nil {
		query = `
			SELECT capsule.id, capsule.redacted_summary, capsule.kind, capsule.confidence,
			       capsule.privacy_tier, capsule.retention, capsule.tags, capsule.created_at
			  FROM memory_capsules capsule
			  JOIN private_memory_objects object ON object.object_id=capsule.id
			 WHERE capsule.kind IN (
			       'fact', 'decision', 'preference', 'project_state', 'open_loop',
			       'correction', 'relationship_note', 'do_not_repeat', 'state_signal'
			   )
			   AND capsule.status='active'
			   AND object.lifecycle='active'
			   AND (
			       object.memory_scope='personal_global' OR
			       (object.memory_scope='project' AND object.project_namespace_id=?)
			   )
			 ORDER BY capsule.created_at DESC
			 LIMIT ?`
		args = []any{requestedScope.ProjectNamespaceID, limit}
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecalledMemoryItem{}
	for rows.Next() {
		item, err := scanRecalledMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func promoteMemoryCapsuleToResume(sections *ResumeSections, memory RecalledMemoryItem) bool {
	summary := strings.TrimSpace(memory.Summary)
	if summary == "" {
		return false
	}
	var target *[]string
	switch strings.TrimSpace(memory.Kind) {
	case "decision", "preference", "correction":
		target = &sections.ActiveDecisions
	case "open_loop":
		target = &sections.OpenLoops
	case "do_not_repeat":
		target = &sections.DoNotRepeat
	case "relationship_note", "state_signal":
		target = &sections.RelevantEmotionalState
	case "fact", "project_state":
		target = &sections.WhereWeLeftOff
	default:
		return false
	}
	before := len(*target)
	*target = appendUniqueContinuityItem(*target, summary)
	return len(*target) > before
}

func includedResumeManifest(markdown string, memories []RecalledMemoryItem, evidenceRefs []string) ([]string, []string) {
	renderedItems := make(map[string]struct{})
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(line, "- ") {
			renderedItems[strings.TrimPrefix(line, "- ")] = struct{}{}
		}
	}
	objectIDs := make([]string, 0, len(memories))
	for _, memory := range memories {
		summary := strings.TrimSpace(memory.Summary)
		if _, rendered := renderedItems[summary]; memory.ID != "" && summary != "" && rendered {
			objectIDs = appendUniqueContinuityItem(objectIDs, memory.ID)
		}
	}
	evidenceIDs := make([]string, 0, len(evidenceRefs))
	for _, evidenceRef := range evidenceRefs {
		if _, rendered := renderedItems[evidenceRef]; evidenceRef != "" && rendered {
			evidenceIDs = appendUniqueContinuityItem(evidenceIDs, evidenceRef)
		}
	}
	sort.Strings(objectIDs)
	sort.Strings(evidenceIDs)
	return objectIDs, evidenceIDs
}

func promoteContinuityObservation(sections *ResumeSections, summary string) bool {
	label, text, ok := splitTypedContinuityObservation(summary)
	if !ok {
		return false
	}
	switch label {
	case "Decision", "Preference", "Correction":
		sections.ActiveDecisions = appendUniqueContinuityItem(sections.ActiveDecisions, text)
	case "Open loop":
		sections.OpenLoops = appendUniqueContinuityItem(sections.OpenLoops, text)
	case "Do-not-repeat":
		sections.DoNotRepeat = appendUniqueContinuityItem(sections.DoNotRepeat, text)
	case "Emotional anchor", "Relationship note", "State signal":
		sections.RelevantEmotionalState = appendUniqueContinuityItem(sections.RelevantEmotionalState, text)
	case "Fact", "Project state", "System event":
		sections.WhereWeLeftOff = appendUniqueContinuityItem(sections.WhereWeLeftOff, text)
	default:
		return false
	}
	return true
}

func splitTypedContinuityObservation(summary string) (string, string, bool) {
	summary = strings.TrimSpace(summary)
	for _, label := range []string{
		"Decision",
		"Preference",
		"Correction",
		"Open loop",
		"Do-not-repeat",
		"Emotional anchor",
		"Relationship note",
		"State signal",
		"Project state",
		"System event",
		"Fact",
	} {
		prefix := label + ": "
		if strings.HasPrefix(summary, prefix) {
			text := strings.TrimSpace(strings.TrimPrefix(summary, prefix))
			return label, text, text != ""
		}
	}
	return "", "", false
}

func appendUniqueContinuityItem(items []string, item string) []string {
	item = strings.TrimSpace(item)
	if item == "" {
		return items
	}
	for _, existing := range items {
		if strings.EqualFold(strings.TrimSpace(existing), item) {
			return items
		}
	}
	return append(items, item)
}

func upsertContinuityThread(tx *sql.Tx, threadID, projectID, now string) error {
	_, err := tx.Exec(`
		INSERT INTO continuity_threads(thread_id, project_id, title, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET
		  project_id = COALESCE(NULLIF(excluded.project_id, ''), continuity_threads.project_id),
		  updated_at = excluded.updated_at`,
		threadID, nullableString(projectID), threadID, now, now)
	return err
}

func upsertContinuitySession(tx *sql.Tx, sessionID, threadID, host, projectID, startedAt, endedAt, status string) error {
	_, err := tx.Exec(`
		INSERT INTO continuity_sessions(session_id, thread_id, host, project_id, started_at, ended_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
		  thread_id = excluded.thread_id,
		  host = excluded.host,
		  project_id = COALESCE(NULLIF(excluded.project_id, ''), continuity_sessions.project_id),
		  ended_at = COALESCE(NULLIF(excluded.ended_at, ''), continuity_sessions.ended_at),
		  status = excluded.status`,
		sessionID, threadID, host, nullableString(projectID), startedAt, nullableString(endedAt), status)
	return err
}

func normalizeThreadID(threadID, projectID, sessionID string) string {
	for _, candidate := range []string{threadID, projectID, sessionID, filepath.Base(filepath.Clean(projectID))} {
		candidate = sanitizeContinuityID(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return "default"
}

func sanitizeContinuityID(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.ReplaceAll(v, " ", "-")
	v = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.' || r == '_' || r == ':' || r == '-':
			return r
		default:
			return '-'
		}
	}, v)
	v = strings.Trim(v, ".:-_")
	if len(v) > 96 {
		v = v[:96]
	}
	if !threadIDPattern.MatchString(v) {
		return ""
	}
	return v
}

func validateContinuityStrings(field string, values []string) error {
	if len(values) > 20 {
		return fmt.Errorf("%s has too many items: max 20", field)
	}
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s[%d] is empty", field, i)
		}
		if len(value) > 1200 {
			return fmt.Errorf("%s[%d] is too long", field, i)
		}
		if looksLikeTranscript(value) || looksSensitiveOrPathLike(value) {
			return fmt.Errorf("%s[%d] is raw/secret/path-like", field, i)
		}
	}
	return nil
}

func validateContinuityRefs(field string, values []string) error {
	if len(values) > 50 {
		return fmt.Errorf("%s has too many items: max 50", field)
	}
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !validContinuityRef(value) {
			return fmt.Errorf("%s[%d] is unsafe", field, i)
		}
	}
	return nil
}

func validContinuityRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 160 {
		return false
	}
	if looksSensitiveOrPathLike(ref) {
		return false
	}
	for _, blocked := range []string{"/", "\\", "?", "&", "="} {
		if strings.Contains(ref, blocked) {
			return false
		}
	}
	for _, prefix := range []string{"pulse:", "pulse:hook:", "local-ref:", "obs:", "checkpoint:", "raw:"} {
		if strings.HasPrefix(ref, prefix) {
			return safeRefPattern.MatchString(ref)
		}
	}
	return false
}

func validContinuityEvent(kind string) bool {
	switch kind {
	case "SessionStart", "UserPromptSubmit", "PostToolUse", "Stop", "SessionEnd":
		return true
	default:
		return false
	}
}

func renderResumeMarkdown(sections ResumeSections) string {
	var b strings.Builder
	b.WriteString("# Pulse Resume\n")
	if len(sections.HarnessActivity) > 0 {
		writeResumeSection(&b, "Across your harnesses", sections.HarnessActivity)
	}
	writeResumeSection(&b, "Where we left off", sections.WhereWeLeftOff)
	writeResumeSection(&b, "Active decisions", sections.ActiveDecisions)
	writeResumeSection(&b, "Active reviewed threads", sections.ActiveReviewedThreads)
	writeResumeSection(&b, "Review insights", sections.ReviewInsights)
	writeResumeSection(&b, "Open loops", sections.OpenLoops)
	writeResumeSection(&b, "Do-not-repeat", sections.DoNotRepeat)
	writeResumeSection(&b, "Relevant emotional/state context", sections.RelevantEmotionalState)
	writeResumeSection(&b, "Suggested next step", sections.SuggestedNextStep)
	writeResumeSection(&b, "Evidence refs", sections.EvidenceRefs)
	return b.String()
}

func writeResumeSection(b *strings.Builder, title string, items []string) {
	b.WriteString("\n## ")
	b.WriteString(title)
	b.WriteString("\n")
	if len(items) == 0 {
		b.WriteString("- None recorded.\n")
		return
	}
	for _, item := range items {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(item))
		b.WriteString("\n")
	}
}

func trimMarkdownToBudget(markdown string, budget int) string {
	maxBytes := budget * 4
	if len(markdown) <= maxBytes {
		return markdown
	}
	if maxBytes < 80 {
		maxBytes = 80
	}
	const marker = "\n- [truncated to Pulse resume budget]\n"
	end := maxBytes - len(marker)
	if end > len(markdown) {
		end = len(markdown)
	}
	for end > 0 && end < len(markdown) && !utf8.RuneStart(markdown[end]) {
		end--
	}
	return strings.TrimSpace(markdown[:end]) + marker
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
