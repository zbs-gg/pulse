package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkkmnk/pulse/internal/consolidation"
)

const (
	MemoryHomeDataSchema              = "pulse.memory_home.v1"
	MemoryHomeReadinessSnapshotSchema = "pulse.readiness_snapshot.v1"

	MemoryHomeReadinessReady          = "ready"
	MemoryHomeReadinessWarming        = "warming"
	MemoryHomeReadinessActionRequired = "action_required"
	MemoryHomeReadinessPartial        = "partial"
	MemoryHomeReadinessBlocked        = "blocked"

	MemoryHomeEconomyCollectingBaseline = "collecting_baseline"
	MemoryHomeEconomyEstimated          = "estimated"
	MemoryHomeEconomyMeasured           = "measured"
	MemoryHomeEconomyUnavailable        = "unavailable"
	MemoryHomeEconomyTrendUp            = "up"
	MemoryHomeEconomyTrendDown          = "down"
	MemoryHomeEconomyTrendFlat          = "flat"

	MemoryHomeDeliveryOfferedToHost        = "offered_to_host"
	MemoryHomeDeliveryHostObserved         = "host_observed"
	MemoryHomeDeliveryPurposeSessionStart  = "session_start"
	MemoryHomeCountMethodUTF8BytesDiv4Ceil = "utf8_bytes_div4_ceil"
	MemoryHomeBaselineCanonicalStructured  = "canonical_structured_resume_v1"
)

type MemoryHomeBoundary struct {
	StoreID       string `json:"store_id"`
	StoreKind     string `json:"store_kind"`
	RepositoryID  string `json:"repository_id"`
	BindingDigest string `json:"binding_digest"`
	Locality      string `json:"locality"`
	Privacy       string `json:"privacy"`
}

type MemoryHomeNextAction struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

type MemoryHomeLiveReadiness struct {
	Outcome    string               `json:"outcome"`
	ReasonCode string               `json:"reason_code"`
	NextAction MemoryHomeNextAction `json:"next_action"`
}

type MemoryHomeReadinessSnapshot struct {
	Schema     string                   `json:"schema"`
	Outcome    string                   `json:"outcome"`
	ReasonCode string                   `json:"reason_code"`
	NextAction MemoryHomeNextAction     `json:"next_action"`
	CheckedAt  string                   `json:"checked_at"`
	Proof      MemoryHomeReadinessProof `json:"proof"`
}

type MemoryHomeReadinessProof struct {
	LiveOutcome           string `json:"live_outcome"`
	MemoryHost            string `json:"memory_host,omitempty"`
	DeliveryHost          string `json:"delivery_host,omitempty"`
	TerminalReceiptID     string `json:"terminal_receipt_id,omitempty"`
	PresentationReceiptID string `json:"presentation_receipt_id,omitempty"`
	ContextOfferReceiptID string `json:"context_offer_receipt_id,omitempty"`
	ContextAckReceiptID   string `json:"context_ack_receipt_id,omitempty"`
}

type MemoryHomeActiveMemory struct {
	ObjectID              string `json:"object_id"`
	Kind                  string `json:"kind"`
	RedactedSummary       string `json:"redacted_summary"`
	EditableSummary       string `json:"-"`
	Scope                 string `json:"scope"`
	ProjectNamespaceID    string `json:"project_namespace_id"`
	OriginalRepositoryID  string `json:"original_repository_id"`
	ProjectLabel          string `json:"project_label"`
	LogicalGeneration     int    `json:"logical_generation"`
	Host                  string `json:"host"`
	SessionRef            string `json:"session_ref"`
	SessionLabel          string `json:"session_label"`
	SharingState          string `json:"sharing_state"`
	CreatedAt             string `json:"created_at"`
	ModifiedAt            string `json:"modified_at"`
	TerminalReceiptID     string `json:"terminal_receipt_id"`
	PresentationReceiptID string `json:"presentation_receipt_id,omitempty"`
}

type MemoryHomeMemories struct {
	ActiveCount  int                      `json:"active_count"`
	LatestActive []MemoryHomeActiveMemory `json:"latest_active"`
}

type MemoryHomeFilter struct {
	Text       string `json:"text,omitempty"`
	Project    string `json:"project,omitempty"`
	Harness    string `json:"harness,omitempty"`
	DateFrom   string `json:"date_from,omitempty"`
	DateTo     string `json:"date_to,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Sharing    string `json:"sharing,omitempty"`
	PageOffset int    `json:"page_offset,omitempty"`
	PageSize   int    `json:"page_size,omitempty"`
}

type MemoryHomeFacet struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type MemoryHomeFacets struct {
	Projects []MemoryHomeFacet `json:"projects"`
	Harness  []MemoryHomeFacet `json:"harnesses"`
}

type MemoryHomeReceipts struct {
	LatestTerminal []MemoryHomeActiveMemory `json:"latest_terminal"`
	Attempts       []MemoryHomeAttempt      `json:"attempts"`
}

type MemoryHomeAttempt struct {
	CandidateID     string `json:"candidate_id"`
	State           string `json:"state"`
	Kind            string `json:"kind"`
	RedactedSummary string `json:"redacted_summary"`
	ReceiptID       string `json:"receipt_id"`
	ReceiptStatus   string `json:"receipt_status"`
	CreatedAt       string `json:"created_at"`
}

type MemoryHomeContext struct {
	Selection      string                     `json:"selection"`
	LatestDelivery *MemoryHomeDeliverySummary `json:"latest_delivery,omitempty"`
}

type MemoryHomeDeliverySummary struct {
	ContextID       string   `json:"context_id"`
	Host            string   `json:"host"`
	OfferReceiptID  string   `json:"offer_receipt_id"`
	AckReceiptID    string   `json:"ack_receipt_id,omitempty"`
	Acknowledgement string   `json:"acknowledgement"`
	PayloadDigest   string   `json:"payload_digest"`
	ObjectIDs       []string `json:"object_ids,omitempty"`
	EvidenceIDs     []string `json:"evidence_ids,omitempty"`
	OfferedAt       string   `json:"offered_at"`
	ObservedAt      string   `json:"observed_at,omitempty"`
}

type MemoryHomeNextTaskPreview struct {
	Status         string   `json:"status"`
	PayloadDigest  string   `json:"payload_digest"`
	ObjectIDs      []string `json:"object_ids,omitempty"`
	EvidenceIDs    []string `json:"evidence_ids,omitempty"`
	RedactedResume string   `json:"redacted_resume"`
	MethodID       string   `json:"method_id"`
	MethodVersion  string   `json:"method_version"`
	RenderedBytes  int      `json:"rendered_bytes"`
	PulseTokens    int      `json:"pulse_tokens"`
}

type MemoryHomeData struct {
	Schema          string                      `json:"schema"`
	GeneratedAt     string                      `json:"generated_at"`
	Boundary        MemoryHomeBoundary          `json:"boundary"`
	Readiness       MemoryHomeReadinessSnapshot `json:"readiness"`
	Memories        MemoryHomeMemories          `json:"memories"`
	Filter          MemoryHomeFilter            `json:"filter"`
	Facets          MemoryHomeFacets            `json:"facets"`
	Receipts        MemoryHomeReceipts          `json:"receipts"`
	Context         MemoryHomeContext           `json:"context"`
	Economy         MemoryHomeEconomy           `json:"economy"`
	NextTaskPreview *MemoryHomeNextTaskPreview  `json:"next_task_preview,omitempty"`
	Consolidation   *consolidation.Report       `json:"consolidation,omitempty"`
}

type MemoryHomeProjectionInput struct {
	GeneratedAt       string
	Boundary          MemoryHomeBoundary
	LiveReadiness     MemoryHomeLiveReadiness
	ActiveCount       int
	ActiveMemories    []MemoryHomeActiveMemory
	ReadinessMemories []MemoryHomeActiveMemory
	Attempts          []MemoryHomeAttempt
	Deliveries        []MemoryHomeDeliveryFact
	CurrentSessionRef string
	NextTaskPreview   *MemoryHomeNextTaskPreview
	Filter            MemoryHomeFilter
	Facets            MemoryHomeFacets
}

type MemoryHomeQuery struct {
	RepositoryID      string
	BindingDigest     string
	GeneratedAt       time.Time
	LiveReadiness     MemoryHomeLiveReadiness
	CurrentSessionRef string
	NextTaskPreview   *MemoryHomeNextTaskPreview
	Filter            MemoryHomeFilter
}

type MemoryHomeDeliveryFactReader interface {
	ReadMemoryHomeDeliveryFacts(repositoryID, bindingDigest string, limit int) ([]MemoryHomeDeliveryFact, error)
}

func (s *Store) BuildMemoryHomeData(query MemoryHomeQuery, deliveries MemoryHomeDeliveryFactReader) (MemoryHomeData, error) {
	if s == nil {
		return MemoryHomeData{}, errors.New("Memory Home store is unavailable")
	}
	if _, err := s.trayDestination(); err != nil {
		return MemoryHomeData{}, err
	}
	if !validTrayIdentifier(query.RepositoryID) || !validMemoryHomeDigest(query.BindingDigest) || query.GeneratedAt.IsZero() {
		return MemoryHomeData{}, errors.New("Memory Home query is invalid")
	}
	expectedBinding, _, _ := s.productRuntimeAuthority()
	if expectedBinding != query.BindingDigest {
		return MemoryHomeData{}, ErrProductRuntimeMismatch
	}
	projectNamespaceID := s.currentPersonalMemoryScope(query.BindingDigest).ProjectNamespaceID
	activeCount, active, err := queryMemoryHomeCanonicalFactsFiltered(
		s.db, query.BindingDigest, projectNamespaceID, query.Filter,
	)
	if err != nil {
		return MemoryHomeData{}, err
	}
	facets, err := queryMemoryHomeFacets(s.db, query.BindingDigest, projectNamespaceID)
	if err != nil {
		return MemoryHomeData{}, err
	}
	attempts, err := queryMemoryHomeAttempts(s.db, query.BindingDigest, 10)
	if err != nil {
		return MemoryHomeData{}, err
	}
	deliveryFacts := []MemoryHomeDeliveryFact{}
	if deliveries != nil {
		facts, err := deliveries.ReadMemoryHomeDeliveryFacts(query.RepositoryID, query.BindingDigest, 100)
		if err != nil {
			return MemoryHomeData{}, err
		}
		for _, fact := range facts {
			if fact.RepositoryID == query.RepositoryID && fact.BindingDigest == query.BindingDigest {
				deliveryFacts = append(deliveryFacts, fact)
			}
		}
	}
	readinessMemories, err := queryMemoryHomeReadinessFacts(
		s.db, query.BindingDigest, projectNamespaceID, deliveryFacts,
	)
	if err != nil {
		return MemoryHomeData{}, err
	}
	return ProjectMemoryHomeData(MemoryHomeProjectionInput{
		GeneratedAt: query.GeneratedAt.UTC().Format(time.RFC3339Nano),
		Boundary: MemoryHomeBoundary{
			StoreID: s.storeID, StoreKind: string(s.storeKind), RepositoryID: query.RepositoryID,
			BindingDigest: query.BindingDigest, Locality: "device_local", Privacy: "private",
		},
		LiveReadiness: query.LiveReadiness, ActiveCount: activeCount, ActiveMemories: active,
		ReadinessMemories: readinessMemories,
		Attempts:          attempts, Deliveries: deliveryFacts, CurrentSessionRef: query.CurrentSessionRef,
		NextTaskPreview: query.NextTaskPreview, Filter: query.Filter, Facets: facets,
	}), nil
}

func ProjectMemoryHomeData(input MemoryHomeProjectionInput) MemoryHomeData {
	result := MemoryHomeData{
		Schema: MemoryHomeDataSchema, GeneratedAt: input.GeneratedAt, Boundary: input.Boundary,
		Readiness: MemoryHomeReadinessSnapshot{
			Schema: MemoryHomeReadinessSnapshotSchema, Outcome: input.LiveReadiness.Outcome,
			ReasonCode: input.LiveReadiness.ReasonCode, NextAction: input.LiveReadiness.NextAction,
			CheckedAt: input.GeneratedAt,
		},
		Memories: MemoryHomeMemories{LatestActive: []MemoryHomeActiveMemory{}},
		Receipts: MemoryHomeReceipts{LatestTerminal: []MemoryHomeActiveMemory{}, Attempts: []MemoryHomeAttempt{}},
		Context:  MemoryHomeContext{Selection: "none"}, Economy: ProjectMemoryHomeEconomy(input.Deliveries),
		NextTaskPreview: projectMemoryHomeNextTaskPreview(input.NextTaskPreview),
		Filter:          input.Filter,
		Facets:          input.Facets,
	}
	activeCount := input.ActiveCount
	if activeCount < len(input.ActiveMemories) {
		activeCount = len(input.ActiveMemories)
	}
	result.Memories.ActiveCount = activeCount
	result.Memories.LatestActive = boundedMemoryHomeCopy(input.ActiveMemories, 50)
	result.Receipts.LatestTerminal = boundedMemoryHomeCopy(input.ActiveMemories, 50)
	result.Receipts.Attempts = boundedMemoryHomeCopy(input.Attempts, 10)
	result.Context = projectMemoryHomeContext(input.Deliveries, input.CurrentSessionRef)
	readinessMemories := input.ReadinessMemories
	if readinessMemories == nil {
		readinessMemories = input.ActiveMemories
	}
	result.Readiness = projectMemoryHomeReadiness(input, readinessMemories, result.Memories.LatestActive)
	return result
}

const memoryHomeCandidateAttemptsQuery = `
	SELECT candidate.candidate_id, candidate.state, candidate.candidate_kind,
	       candidate.payload_json, receipt.receipt_id, receipt.status,
	       receipt.created_at, receipt.rowid
	  FROM turn_ledgers AS ledger INDEXED BY idx_turn_ledgers_memory_home_binding
	  JOIN memory_tray_candidates AS candidate INDEXED BY idx_memory_tray_ledger
	    ON candidate.ledger_id=ledger.ledger_id
	  JOIN memory_write_receipts AS receipt
	    ON receipt.rowid=(
	       SELECT latest.rowid
	         FROM memory_write_receipts AS latest
	              INDEXED BY idx_memory_write_receipts_candidate_latest
	        WHERE latest.candidate_id=candidate.candidate_id
	        ORDER BY latest.rowid DESC LIMIT 1
	    )
	 WHERE ledger.binding_digest=?
	   AND candidate.state IN ('pending','canceled','failed')
	 ORDER BY receipt.created_at DESC, receipt.rowid DESC
	 LIMIT ?`

const memoryHomeRejectedAttemptsQuery = `
	SELECT '', 'rejected', 'memory_attempt', '', receipt.receipt_id,
	       receipt.status, receipt.created_at, receipt.rowid
	  FROM turn_ledgers AS ledger INDEXED BY idx_turn_ledgers_memory_home_binding
	  JOIN memory_write_receipts AS receipt
	       INDEXED BY idx_memory_write_receipts_memory_home_rejected
	    ON receipt.ledger_id=ledger.ledger_id
	 WHERE ledger.binding_digest=?
	   AND receipt.status='rejected' AND receipt.candidate_id IS NULL
	 ORDER BY receipt.created_at DESC, receipt.rowid DESC
	 LIMIT ?`

type memoryHomeAttemptRow struct {
	attempt MemoryHomeAttempt
	rowID   int64
}

func queryMemoryHomeAttempts(db *sql.DB, bindingDigest string, limit int) ([]MemoryHomeAttempt, error) {
	if db == nil || !validMemoryHomeDigest(bindingDigest) {
		return nil, errors.New("Memory Home attempt boundary is invalid")
	}
	if limit < 1 || limit > 20 {
		return nil, errors.New("Memory Home attempt limit must be between 1 and 20")
	}
	candidates, err := queryMemoryHomeAttemptRows(db, memoryHomeCandidateAttemptsQuery, bindingDigest, limit)
	if err != nil {
		return nil, err
	}
	rejected, err := queryMemoryHomeAttemptRows(db, memoryHomeRejectedAttemptsQuery, bindingDigest, limit)
	if err != nil {
		return nil, err
	}
	rows := append(candidates, rejected...)
	sort.SliceStable(rows, func(left, right int) bool {
		if rows[left].attempt.CreatedAt == rows[right].attempt.CreatedAt {
			return rows[left].rowID > rows[right].rowID
		}
		return rows[left].attempt.CreatedAt > rows[right].attempt.CreatedAt
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	result := make([]MemoryHomeAttempt, len(rows))
	for index := range rows {
		result[index] = rows[index].attempt
	}
	return result, nil
}

func queryMemoryHomeAttemptRows(
	db *sql.DB, query, bindingDigest string, limit int,
) ([]memoryHomeAttemptRow, error) {
	rows, err := db.Query(query, bindingDigest, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]memoryHomeAttemptRow, 0, limit)
	for rows.Next() {
		var row memoryHomeAttemptRow
		var payload string
		if err := rows.Scan(
			&row.attempt.CandidateID, &row.attempt.State, &row.attempt.Kind, &payload,
			&row.attempt.ReceiptID, &row.attempt.ReceiptStatus, &row.attempt.CreatedAt, &row.rowID,
		); err != nil {
			return nil, err
		}
		switch row.attempt.State {
		case "pending":
			kind, summary, err := memoryHomeCandidateSummary(payload)
			if err != nil {
				return nil, err
			}
			row.attempt.Kind = kind
			row.attempt.RedactedSummary = boundedMemoryHomeSummary(summary)
		case "canceled":
			row.attempt.RedactedSummary = "Save canceled."
		case "failed":
			row.attempt.RedactedSummary = "Save failed."
		case "rejected":
			row.attempt.RedactedSummary = "Unsafe content was not saved."
		default:
			return nil, errors.New("Memory Home attempt state is invalid")
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func projectMemoryHomeNextTaskPreview(preview *MemoryHomeNextTaskPreview) *MemoryHomeNextTaskPreview {
	if preview == nil || preview.Status != "preview_only" || !validMemoryHomeDigest(preview.PayloadDigest) ||
		!validContinuityDeliveryIDs(preview.ObjectIDs) || !validContinuityDeliveryIDs(preview.EvidenceIDs) ||
		preview.MethodID != MemoryHomeCountMethodUTF8BytesDiv4Ceil || preview.MethodVersion != "1" ||
		!utf8.ValidString(preview.RedactedResume) || strings.TrimSpace(preview.RedactedResume) == "" ||
		len([]byte(preview.RedactedResume)) > 8192 || procedureContentUnsafe(preview.RedactedResume) ||
		preview.RenderedBytes != len([]byte(preview.RedactedResume)) ||
		preview.PulseTokens != (preview.RenderedBytes+3)/4 {
		return nil
	}
	return &MemoryHomeNextTaskPreview{
		Status: preview.Status, PayloadDigest: preview.PayloadDigest,
		ObjectIDs:      append([]string(nil), preview.ObjectIDs...),
		EvidenceIDs:    append([]string(nil), preview.EvidenceIDs...),
		RedactedResume: preview.RedactedResume, MethodID: preview.MethodID, MethodVersion: preview.MethodVersion,
		RenderedBytes: preview.RenderedBytes, PulseTokens: preview.PulseTokens,
	}
}

func boundedMemoryHomeCopy[T any](values []T, limit int) []T {
	if len(values) < limit {
		limit = len(values)
	}
	result := make([]T, limit)
	copy(result, values[:limit])
	return result
}

func projectMemoryHomeContext(facts []MemoryHomeDeliveryFact, currentSessionRef string) MemoryHomeContext {
	result := MemoryHomeContext{Selection: "none"}
	var latest *MemoryHomeDeliveryFact
	for index := range facts {
		fact := &facts[index]
		if fact.Acknowledgement != MemoryHomeDeliveryOfferedToHost || fact.Purpose != MemoryHomeDeliveryPurposeSessionStart {
			continue
		}
		if _, ok := canonicalMemoryHomeTime(fact.CreatedAt); !ok {
			continue
		}
		if latest == nil || memoryHomeTimeAfter(fact.CreatedAt, latest.CreatedAt) {
			latest = fact
		}
	}
	if latest == nil {
		return result
	}
	result.Selection = "last_task"
	if currentSessionRef != "" && latest.SessionRef == currentSessionRef {
		result.Selection = "current_task"
	}
	result.LatestDelivery = &MemoryHomeDeliverySummary{
		ContextID: latest.ContextID, Host: latest.Host, OfferReceiptID: latest.ReceiptID,
		Acknowledgement: MemoryHomeDeliveryOfferedToHost, PayloadDigest: latest.PayloadDigest,
		ObjectIDs: append([]string(nil), latest.ObjectIDs...), EvidenceIDs: append([]string(nil), latest.EvidenceIDs...),
		OfferedAt: latest.CreatedAt,
	}
	if observed, ok := matchingMemoryHomeObservation(*latest, facts); ok {
		result.LatestDelivery.Acknowledgement = MemoryHomeDeliveryHostObserved
		result.LatestDelivery.AckReceiptID = observed.ReceiptID
		result.LatestDelivery.ObservedAt = observed.CreatedAt
	}
	return result
}

func projectMemoryHomeReadiness(
	input MemoryHomeProjectionInput,
	readinessMemories []MemoryHomeActiveMemory,
	displayedMemories []MemoryHomeActiveMemory,
) MemoryHomeReadinessSnapshot {
	result := MemoryHomeReadinessSnapshot{
		Schema: MemoryHomeReadinessSnapshotSchema, Outcome: input.LiveReadiness.Outcome,
		ReasonCode: input.LiveReadiness.ReasonCode, NextAction: input.LiveReadiness.NextAction,
		CheckedAt: input.GeneratedAt, Proof: MemoryHomeReadinessProof{LiveOutcome: input.LiveReadiness.Outcome},
	}
	if input.LiveReadiness.Outcome != MemoryHomeReadinessReady {
		return result
	}
	if input.ActiveCount == 0 && len(input.ActiveMemories) == 0 {
		result.Outcome = MemoryHomeReadinessActionRequired
		result.ReasonCode = "first_memory_required"
		result.NextAction = MemoryHomeNextAction{Code: "save_first_memory", Label: "Save the first memory"}
		return result
	}
	var matchingOffer *MemoryHomeDeliveryFact
	var matchingMemory *MemoryHomeActiveMemory
	for memoryIndex := range readinessMemories {
		memory := &readinessMemories[memoryIndex]
		for index := range input.Deliveries {
			offer := &input.Deliveries[index]
			if offer.Acknowledgement != MemoryHomeDeliveryOfferedToHost || offer.Purpose != MemoryHomeDeliveryPurposeSessionStart ||
				!memoryHomeTimeAfter(offer.CreatedAt, memory.CreatedAt) || offer.BindingDigest != input.Boundary.BindingDigest ||
				offer.RepositoryID != input.Boundary.RepositoryID ||
				offer.SessionRef == "" || offer.SessionRef == memory.SessionRef || !slices.Contains(offer.ObjectIDs, memory.ObjectID) {
				continue
			}
			if matchingOffer == nil || memoryHomeTimeBefore(offer.CreatedAt, matchingOffer.CreatedAt) {
				matchingOffer = offer
				matchingMemory = memory
			}
		}
	}
	if matchingOffer == nil {
		if len(displayedMemories) > 0 {
			result.Proof.TerminalReceiptID = displayedMemories[0].TerminalReceiptID
			result.Proof.PresentationReceiptID = displayedMemories[0].PresentationReceiptID
		}
		result.Outcome = MemoryHomeReadinessActionRequired
		result.ReasonCode = "fresh_task_required"
		result.NextAction = MemoryHomeNextAction{Code: "open_fresh_task", Label: "Open a fresh agent task"}
		return result
	}
	result.Proof.TerminalReceiptID = matchingMemory.TerminalReceiptID
	result.Proof.PresentationReceiptID = matchingMemory.PresentationReceiptID
	result.Proof.MemoryHost = matchingMemory.Host
	result.Proof.DeliveryHost = matchingOffer.Host
	result.Proof.ContextOfferReceiptID = matchingOffer.ReceiptID
	observed, ok := matchingMemoryHomeObservation(*matchingOffer, input.Deliveries)
	if !ok {
		result.Outcome = MemoryHomeReadinessPartial
		result.ReasonCode = "host_observation_required"
		result.NextAction = MemoryHomeNextAction{Code: "continue_fresh_task", Label: "Continue the fresh task"}
		return result
	}
	result.Proof.ContextAckReceiptID = observed.ReceiptID
	result.Outcome = MemoryHomeReadinessReady
	result.ReasonCode = "memory_continuity_ready"
	result.NextAction = MemoryHomeNextAction{Code: "continue_working", Label: "Continue working"}
	return result
}

func queryMemoryHomeReadinessFacts(
	db *sql.DB, bindingDigest, projectNamespaceID string, deliveries []MemoryHomeDeliveryFact,
) ([]MemoryHomeActiveMemory, error) {
	if db == nil || !validMemoryHomeDigest(bindingDigest) || !validTrayIdentifier(projectNamespaceID) {
		return nil, errors.New("Memory Home readiness boundary is invalid")
	}
	objectIDs := make([]string, 0)
	seen := make(map[string]struct{})
	for _, delivery := range deliveries {
		if delivery.Acknowledgement != MemoryHomeDeliveryOfferedToHost ||
			delivery.Purpose != MemoryHomeDeliveryPurposeSessionStart || delivery.BindingDigest != bindingDigest {
			continue
		}
		for _, objectID := range delivery.ObjectIDs {
			if !validTrayIdentifier(objectID) {
				return nil, ErrContinuityDeliveryTransition
			}
			if _, ok := seen[objectID]; ok {
				continue
			}
			seen[objectID] = struct{}{}
			objectIDs = append(objectIDs, objectID)
		}
	}
	// SQLite builds in the supported matrix accept at least 999 host parameters.
	// Keep one parameter for the binding and leave ample headroom for wrappers.
	const batchSize = 400
	factsByID := make(map[string]MemoryHomeActiveMemory, len(objectIDs))
	for start := 0; start < len(objectIDs); start += batchSize {
		end := min(start+batchSize, len(objectIDs))
		batch, err := queryMemoryHomeCanonicalFactsByObjectIDs(
			db, bindingDigest, projectNamespaceID, objectIDs[start:end],
		)
		if err != nil {
			return nil, err
		}
		for _, fact := range batch {
			factsByID[fact.ObjectID] = fact
		}
	}
	result := make([]MemoryHomeActiveMemory, 0, len(factsByID))
	for _, objectID := range objectIDs {
		if fact, ok := factsByID[objectID]; ok {
			result = append(result, fact)
		}
	}
	return result, nil
}

const memoryHomeCanonicalFactQuery = `
	SELECT object.object_id, candidate.payload_json,
	       receipt.receipt_id, COALESCE(presentation.receipt_id, ''),
	       object.capture_host, object.capture_session_ref,
	       object.captured_at, object.modified_at,
	       object.memory_scope, object.project_namespace_id,
	       object.original_repository_id, object.logical_generation
	  FROM private_memory_objects object
	  JOIN memory_tray_candidates candidate
	    ON candidate.candidate_id=object.created_from_candidate_id
	   AND candidate.content_digest=object.content_digest
	  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
	  JOIN memory_write_receipts receipt
	    ON receipt.candidate_id=candidate.candidate_id
	   AND receipt.candidate_version=candidate.version
	   AND receipt.content_digest=candidate.content_digest
	   AND receipt.object_id=object.object_id
	   AND receipt.status IN ('created','updated','deduplicated')
	  LEFT JOIN memory_presentation_receipts presentation
	    ON presentation.candidate_id=candidate.candidate_id
	   AND presentation.candidate_version=candidate.version
	   AND presentation.content_digest=candidate.content_digest
	   AND presentation.binding_digest=ledger.binding_digest
	 WHERE object.lifecycle='active' AND ledger.binding_digest=?
	   AND (
	       object.memory_scope='personal_global' OR
	       (object.memory_scope='project' AND object.project_namespace_id=?)
	   )
	   AND receipt.rowid=(
	       SELECT latest_receipt.rowid FROM memory_write_receipts latest_receipt
	        WHERE latest_receipt.candidate_id=candidate.candidate_id
	          AND latest_receipt.candidate_version=candidate.version
	          AND latest_receipt.content_digest=candidate.content_digest
	          AND latest_receipt.object_id=object.object_id
	          AND latest_receipt.status IN ('created','updated','deduplicated')
	        ORDER BY latest_receipt.rowid DESC LIMIT 1
	   )
	   AND (
	       presentation.rowid IS NULL OR presentation.rowid=(
	           SELECT first_presentation.rowid FROM memory_presentation_receipts first_presentation
	            WHERE first_presentation.candidate_id=candidate.candidate_id
	              AND first_presentation.candidate_version=candidate.version
	              AND first_presentation.content_digest=candidate.content_digest
	              AND first_presentation.binding_digest=ledger.binding_digest
	            ORDER BY first_presentation.rowid LIMIT 1
	       )
	   )`

func queryMemoryHomeCanonicalFactsByObjectIDs(
	db *sql.DB, bindingDigest, projectNamespaceID string, objectIDs []string,
) ([]MemoryHomeActiveMemory, error) {
	if len(objectIDs) == 0 {
		return []MemoryHomeActiveMemory{}, nil
	}
	arguments := make([]any, 0, len(objectIDs)+2)
	arguments = append(arguments, bindingDigest, projectNamespaceID)
	for _, objectID := range objectIDs {
		arguments = append(arguments, objectID)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(objectIDs)), ",")
	rows, err := db.Query(memoryHomeCanonicalFactQuery+`
		AND object.object_id IN (`+placeholders+`)`, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemoryHomeCanonicalFacts(rows)
}

func queryMemoryHomeCanonicalFacts(
	db *sql.DB, bindingDigest, projectNamespaceID string, limit int,
) (int, []MemoryHomeActiveMemory, error) {
	if db == nil || !validMemoryHomeDigest(bindingDigest) || !validTrayIdentifier(projectNamespaceID) {
		return 0, nil, errors.New("Memory Home boundary is invalid")
	}
	if limit < 1 || limit > 20 {
		return 0, nil, errors.New("Memory Home memory limit must be between 1 and 20")
	}
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		  FROM private_memory_objects object
		  JOIN memory_tray_candidates candidate
		    ON candidate.candidate_id=object.created_from_candidate_id
		   AND candidate.content_digest=object.content_digest
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 WHERE object.lifecycle='active' AND ledger.binding_digest=?
		   AND (
		       object.memory_scope='personal_global' OR
		       (object.memory_scope='project' AND object.project_namespace_id=?)
		   )`, bindingDigest, projectNamespaceID).Scan(&count); err != nil {
		return 0, nil, err
	}
	rows, err := db.Query(memoryHomeCanonicalFactQuery+`
		ORDER BY receipt.created_at DESC, receipt.receipt_id DESC
		LIMIT ?`, bindingDigest, projectNamespaceID, limit)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	facts, err := scanMemoryHomeCanonicalFacts(rows)
	if err != nil {
		return 0, nil, err
	}
	return count, facts, nil
}

func queryMemoryHomeCanonicalFactsFiltered(
	db *sql.DB,
	bindingDigest, projectNamespaceID string,
	filter MemoryHomeFilter,
) (int, []MemoryHomeActiveMemory, error) {
	if db == nil || !validMemoryHomeDigest(bindingDigest) || !validTrayIdentifier(projectNamespaceID) {
		return 0, nil, errors.New("Memory Home boundary is invalid")
	}
	if err := validateMemoryHomeFilter(filter); err != nil {
		return 0, nil, err
	}
	pageSize := filter.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	where := strings.Builder{}
	args := []any{bindingDigest, projectNamespaceID}
	if filter.Text != "" {
		where.WriteString(` AND LOWER(candidate.payload_json) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeMemoryHomeLike(strings.ToLower(filter.Text))+"%")
	}
	if filter.Project != "" {
		where.WriteString(` AND object.original_repository_id=?`)
		args = append(args, filter.Project)
	}
	if filter.Harness != "" {
		where.WriteString(` AND object.capture_host=?`)
		args = append(args, filter.Harness)
	}
	if filter.DateFrom != "" {
		where.WriteString(` AND object.captured_at>=?`)
		args = append(args, filter.DateFrom+"T00:00:00Z")
	}
	if filter.DateTo != "" {
		where.WriteString(` AND object.captured_at<?`)
		end, _ := time.Parse("2006-01-02", filter.DateTo)
		args = append(args, end.AddDate(0, 0, 1).Format("2006-01-02")+"T00:00:00Z")
	}
	if filter.Scope != "" {
		where.WriteString(` AND object.memory_scope=?`)
		args = append(args, filter.Scope)
	}
	switch filter.Sharing {
	case "":
	case "local_git":
		where.WriteString(`
			AND EXISTS (
			    SELECT 1
			      FROM git_memory_projects project
			      JOIN git_memory_sources source
			        ON source.portable_project_id=project.portable_project_id
			     WHERE project.repository_id=object.original_repository_id
			)`)
	case "unknown":
		where.WriteString(`
			AND NOT EXISTS (
			    SELECT 1
			      FROM git_memory_projects project
			      JOIN git_memory_sources source
			        ON source.portable_project_id=project.portable_project_id
			     WHERE project.repository_id=object.original_repository_id
			)`)
	case "device_only", "remote_git":
		// The current schema has no affirmative no-source or remote-push proof.
		// These filters are therefore truthfully empty, never inferred.
		where.WriteString(` AND 0`)
	}
	filteredQuery := memoryHomeCanonicalFactQuery + where.String()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM (`+filteredQuery+`)`, args...).Scan(&count); err != nil {
		return 0, nil, err
	}
	pageArgs := append(append([]any(nil), args...), pageSize, filter.PageOffset)
	rows, err := db.Query(filteredQuery+`
		ORDER BY object.modified_at DESC, receipt.created_at DESC, receipt.receipt_id DESC
		LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	facts, err := scanMemoryHomeCanonicalFacts(rows)
	if err != nil {
		return 0, nil, err
	}
	enrichMemoryHomeFacts(db, facts)
	return count, facts, nil
}

func validateMemoryHomeFilter(filter MemoryHomeFilter) error {
	if !utf8.ValidString(filter.Text) || len([]rune(filter.Text)) > 160 ||
		(filter.Project != "" && !validTrayIdentifier(filter.Project)) ||
		(filter.Harness != "" && !validContinuityDeliveryHost(filter.Harness)) ||
		(filter.Scope != "" && filter.Scope != MemoryScopeProject && filter.Scope != MemoryScopePersonalGlobal) ||
		(filter.Sharing != "" && filter.Sharing != "device_only" && filter.Sharing != "local_git" &&
			filter.Sharing != "remote_git" && filter.Sharing != "unknown") ||
		filter.PageOffset < 0 || filter.PageOffset > 100_000 ||
		filter.PageSize < 0 || filter.PageSize > 50 {
		return errors.New("Memory Home filter is invalid")
	}
	for _, date := range []string{filter.DateFrom, filter.DateTo} {
		if date == "" {
			continue
		}
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil || parsed.Format("2006-01-02") != date {
			return errors.New("Memory Home filter date is invalid")
		}
	}
	return nil
}

func escapeMemoryHomeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func enrichMemoryHomeFacts(db *sql.DB, facts []MemoryHomeActiveMemory) {
	sharing := make(map[string]string)
	labels := make(map[string]string)
	for index := range facts {
		fact := &facts[index]
		label, ok := labels[fact.OriginalRepositoryID]
		if !ok {
			label = memoryHomeProjectLabel(db, fact.OriginalRepositoryID)
			labels[fact.OriginalRepositoryID] = label
		}
		fact.ProjectLabel = label
		fact.SessionLabel = humanMemoryHomeSessionLabel(fact.Host, fact.CreatedAt)
		state, ok := sharing[fact.OriginalRepositoryID]
		if !ok {
			state = memoryHomeSharingState(db, fact.OriginalRepositoryID)
			sharing[fact.OriginalRepositoryID] = state
		}
		fact.SharingState = state
	}
}

func humanMemoryHomeProjectLabel(repositoryID string) string {
	label := strings.TrimPrefix(strings.TrimSpace(repositoryID), "repository_")
	label = strings.TrimPrefix(label, "repo_")
	if label == "" {
		return "Unlabeled project"
	}
	return label
}

func memoryHomeProjectLabel(db *sql.DB, repositoryID string) string {
	if db != nil {
		var label string
		if err := db.QueryRow(`
			SELECT label
			  FROM personal_project_labels
			 WHERE repository_id=?`, repositoryID).Scan(&label); err == nil && label != "" {
			return label
		}
	}
	return humanMemoryHomeProjectLabel(repositoryID)
}

func humanMemoryHomeSessionLabel(host, createdAt string) string {
	hostLabel := humanMemoryHomeHarnessLabel(host)
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return hostLabel + " · captured session"
	}
	return hostLabel + " · " + parsed.UTC().Format("02 Jan · 15:04 UTC")
}

func humanMemoryHomeHarnessLabel(host string) string {
	if label := map[string]string{
		"codex": "Codex", "claude-code": "Claude Code", "cursor": "Cursor",
	}[host]; label != "" {
		return label
	}
	return "AI session"
}

func memoryHomeSharingState(db *sql.DB, repositoryID string) string {
	if db == nil || repositoryID == "" {
		return "unknown"
	}
	var local int
	err := db.QueryRow(`
		SELECT EXISTS(
		    SELECT 1
		      FROM git_memory_projects project
		      JOIN git_memory_sources source
		        ON source.portable_project_id=project.portable_project_id
		     WHERE project.repository_id=?
		)`, repositoryID).Scan(&local)
	if err != nil || local == 0 {
		return "unknown"
	}
	return "local_git"
}

func queryMemoryHomeFacets(
	db *sql.DB, bindingDigest, projectNamespaceID string,
) (MemoryHomeFacets, error) {
	if db == nil || !validMemoryHomeDigest(bindingDigest) || !validTrayIdentifier(projectNamespaceID) {
		return MemoryHomeFacets{}, errors.New("Memory Home facet boundary is invalid")
	}
	rows, err := db.Query(`
		SELECT DISTINCT object.original_repository_id, object.capture_host
		  FROM private_memory_objects object
		  JOIN memory_tray_candidates candidate
		    ON candidate.candidate_id=object.created_from_candidate_id
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 WHERE object.lifecycle='active'
		   AND ledger.binding_digest=?
		   AND (
		       object.memory_scope='personal_global' OR
		       (object.memory_scope='project' AND object.project_namespace_id=?)
		   )
		 ORDER BY object.original_repository_id, object.capture_host`,
		bindingDigest, projectNamespaceID)
	if err != nil {
		return MemoryHomeFacets{}, err
	}
	defer rows.Close()
	projectSeen := map[string]bool{}
	hostSeen := map[string]bool{}
	result := MemoryHomeFacets{Projects: []MemoryHomeFacet{}, Harness: []MemoryHomeFacet{}}
	for rows.Next() {
		var project, host string
		if err := rows.Scan(&project, &host); err != nil {
			return MemoryHomeFacets{}, err
		}
		if project != "" && !projectSeen[project] {
			projectSeen[project] = true
			result.Projects = append(result.Projects, MemoryHomeFacet{
				Value: project, Label: memoryHomeProjectLabel(db, project),
			})
		}
		if host != "" && !hostSeen[host] {
			hostSeen[host] = true
			result.Harness = append(result.Harness, MemoryHomeFacet{
				Value: host, Label: humanMemoryHomeHarnessLabel(host),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return MemoryHomeFacets{}, err
	}
	return result, nil
}

func scanMemoryHomeCanonicalFacts(rows *sql.Rows) ([]MemoryHomeActiveMemory, error) {
	facts := []MemoryHomeActiveMemory{}
	for rows.Next() {
		var fact MemoryHomeActiveMemory
		var payload string
		if err := rows.Scan(
			&fact.ObjectID, &payload, &fact.TerminalReceiptID, &fact.PresentationReceiptID,
			&fact.Host, &fact.SessionRef, &fact.CreatedAt, &fact.ModifiedAt, &fact.Scope,
			&fact.ProjectNamespaceID, &fact.OriginalRepositoryID, &fact.LogicalGeneration,
		); err != nil {
			return nil, err
		}
		kind, summary, err := memoryHomeCandidateSummary(payload)
		if err != nil {
			return nil, err
		}
		fact.Kind = kind
		fact.EditableSummary = summary
		fact.RedactedSummary = boundedMemoryHomeSummary(summary)
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return facts, nil
}

type memoryHomeCandidatePayload struct {
	Kind    string `json:"kind"`
	Capsule *struct {
		Items []struct {
			Kind            string `json:"kind"`
			RedactedSummary string `json:"redacted_summary"`
		} `json:"items"`
	} `json:"capsule"`
	SemanticDelta *struct {
		Continuity *struct {
			Summary string `json:"summary"`
		} `json:"continuity"`
		Events []struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"events"`
		Facts []struct {
			Text string `json:"text"`
		} `json:"facts"`
		Nodes []struct {
			CanonicalName string `json:"canonical_name"`
			Summary       string `json:"summary"`
		} `json:"nodes"`
	} `json:"semantic_delta"`
}

func memoryHomeCandidateSummary(payload string) (string, string, error) {
	var candidate memoryHomeCandidatePayload
	if err := json.Unmarshal([]byte(payload), &candidate); err != nil {
		return "", "", errors.New("canonical Memory Home candidate JSON is invalid")
	}
	switch candidate.Kind {
	case "memory_capsule":
		if candidate.Capsule == nil || len(candidate.Capsule.Items) != 1 ||
			candidate.Capsule.Items[0].Kind == "" || candidate.Capsule.Items[0].RedactedSummary == "" {
			return "", "", errors.New("canonical Memory Home capsule is invalid")
		}
		return candidate.Capsule.Items[0].Kind, candidate.Capsule.Items[0].RedactedSummary, nil
	case "semantic_delta":
		if candidate.SemanticDelta == nil {
			return "", "", errors.New("canonical Memory Home semantic memory is invalid")
		}
		if candidate.SemanticDelta.Continuity != nil && candidate.SemanticDelta.Continuity.Summary != "" {
			return "semantic_delta", candidate.SemanticDelta.Continuity.Summary, nil
		}
		for _, event := range candidate.SemanticDelta.Events {
			if event.Summary != "" {
				return "semantic_delta", event.Summary, nil
			}
			if event.Title != "" {
				return "semantic_delta", event.Title, nil
			}
		}
		for _, fact := range candidate.SemanticDelta.Facts {
			if fact.Text != "" {
				return "semantic_delta", fact.Text, nil
			}
		}
		for _, node := range candidate.SemanticDelta.Nodes {
			if node.Summary != "" {
				return "semantic_delta", node.Summary, nil
			}
			if node.CanonicalName != "" {
				return "semantic_delta", node.CanonicalName, nil
			}
		}
		return "semantic_delta", "Structured memory", nil
	default:
		return "", "", errors.New("canonical Memory Home candidate kind is invalid")
	}
}

func boundedMemoryHomeSummary(summary string) string {
	runes := []rune(summary)
	if len(runes) <= 280 {
		return summary
	}
	return string(runes[:279]) + "…"
}

func matchingMemoryHomeObservation(offer MemoryHomeDeliveryFact, facts []MemoryHomeDeliveryFact) (MemoryHomeDeliveryFact, bool) {
	for _, observed := range facts {
		if memoryHomeTimeAfter(observed.CreatedAt, offer.CreatedAt) && memoryHomeObservationMatches(offer, observed) {
			return observed, true
		}
	}
	return MemoryHomeDeliveryFact{}, false
}

func memoryHomeObservationMatches(offer, observed MemoryHomeDeliveryFact) bool {
	return observed.Acknowledgement == MemoryHomeDeliveryHostObserved &&
		observed.ContextID == offer.ContextID && observed.PayloadDigest == offer.PayloadDigest &&
		observed.BindingDigest == offer.BindingDigest && observed.RepositoryID == offer.RepositoryID &&
		observed.Host == offer.Host && observed.SessionRef == offer.SessionRef && observed.Purpose == offer.Purpose &&
		observed.MethodID == offer.MethodID && observed.MethodVersion == offer.MethodVersion &&
		observed.RenderedBytes == offer.RenderedBytes && observed.PulseTokens == offer.PulseTokens &&
		observed.BaselineKind == offer.BaselineKind && observed.SourceEquivalentTokens == offer.SourceEquivalentTokens &&
		observed.CoverageCounted == offer.CoverageCounted && observed.CoverageTotal == offer.CoverageTotal &&
		slices.Equal(observed.ObjectIDs, offer.ObjectIDs) && slices.Equal(observed.EvidenceIDs, offer.EvidenceIDs)
}

func validMemoryHomeDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func canonicalMemoryHomeTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, false
	}
	return parsed, true
}

func memoryHomeTimeAfter(left, right string) bool {
	leftTime, leftOK := canonicalMemoryHomeTime(left)
	rightTime, rightOK := canonicalMemoryHomeTime(right)
	return leftOK && rightOK && leftTime.After(rightTime)
}

func memoryHomeTimeBefore(left, right string) bool {
	leftTime, leftOK := canonicalMemoryHomeTime(left)
	rightTime, rightOK := canonicalMemoryHomeTime(right)
	return leftOK && rightOK && leftTime.Before(rightTime)
}

type MemoryHomeDeliveryFact struct {
	ReceiptID                 string   `json:"receipt_id"`
	ContextID                 string   `json:"context_id"`
	Acknowledgement           string   `json:"acknowledgement"`
	Purpose                   string   `json:"purpose"`
	PayloadDigest             string   `json:"payload_digest"`
	BindingDigest             string   `json:"binding_digest"`
	RepositoryID              string   `json:"repository_id"`
	Host                      string   `json:"host"`
	SessionRef                string   `json:"session_ref"`
	ObjectIDs                 []string `json:"object_ids,omitempty"`
	EvidenceIDs               []string `json:"evidence_ids,omitempty"`
	MethodID                  string   `json:"method_id"`
	MethodVersion             string   `json:"method_version"`
	RenderedBytes             int      `json:"rendered_bytes"`
	PulseTokens               int      `json:"pulse_tokens"`
	SourceEquivalentTokens    int      `json:"source_equivalent_tokens,omitempty"`
	BaselineKind              string   `json:"baseline_kind,omitempty"`
	CoverageCounted           int      `json:"coverage_counted,omitempty"`
	CoverageTotal             int      `json:"coverage_total,omitempty"`
	ProviderActualInputTokens int      `json:"provider_actual_input_tokens,omitempty"`
	ProviderActualSource      string   `json:"provider_actual_source,omitempty"`
	ProviderEvidenceDigest    string   `json:"provider_evidence_digest,omitempty"`
	ProviderEvidenceVerified  bool     `json:"provider_evidence_verified,omitempty"`
	CreatedAt                 string   `json:"created_at"`
}
