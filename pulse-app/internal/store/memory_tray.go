package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/capture"
)

const (
	PrivateMemoryCandidateCapsule       = "memory_capsule"
	PrivateMemoryCandidateSemanticDelta = "semantic_delta"

	TurnFinalizedCandidates   = "candidates"
	TurnFinalizedNoChange     = "no_change"
	TurnFinalizedRejected     = "rejected"
	TurnFinalizeRequestSchema = "pulse.turn_finalize.v1"
	TurnNoChangeRequestSchema = "pulse.turn_no_change.v1"

	MemoryWritePending      = capture.ReceiptPending
	MemoryWriteCreated      = capture.ReceiptCreated
	MemoryWriteDeduplicated = capture.ReceiptDeduplicated
	MemoryWriteUpdated      = capture.ReceiptUpdated
	MemoryWriteCanceled     = capture.ReceiptCanceled
	MemoryWriteRejected     = capture.ReceiptRejected
	MemoryWriteFailed       = capture.ReceiptFailed

	TurnFinalizeReceiptSchema       = "pulse.turn_finalize_receipt.v1"
	MemoryPresentationReceiptSchema = "pulse.memory_presentation_receipt.v1"
)

var (
	ErrMemoryTrayRequired         = errors.New("product semantic writes require Memory Tray")
	ErrMemoryTrayUnavailable      = errors.New("Memory Tray is unavailable for this store kind")
	ErrMemoryTrayGraceActive      = errors.New("Memory Tray grace period is active")
	ErrMemoryTrayNotPresented     = errors.New("Memory Tray candidate has not been presented")
	ErrMemoryTrayVersionConflict  = errors.New("Memory Tray candidate version conflict")
	ErrMemoryScopeConflict        = errors.New("memory scope changed concurrently")
	ErrMemoryPresentationConflict = errors.New("memory presentation does not match the current candidate")
	ErrMemoryCorrectionConflict   = errors.New("memory correction target changed after preview")
	ErrMemoryTrayTerminal         = errors.New("Memory Tray candidate is terminal")
	ErrTurnAlreadyFinalized       = errors.New("turn already finalized with a different result")
	ErrTurnFinalizeConflict       = errors.New("turn finalization idempotency conflict")
	ErrProductRuntimeMismatch     = errors.New("turn runtime authority does not match the bound vault")
)

var trayIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
var trayBindingDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validTrayIdentifier(value string) bool {
	if !trayIdentifierPattern.MatchString(value) {
		return false
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{"sk-", "ghp_", "gho_", "ghu_", "ghs_", "github_pat_", "xoxb-", "xoxp-", "xapp-", "akia", "ya29."} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	return true
}

type PrivateMemoryCandidate struct {
	Kind          string         `json:"kind"`
	Capsule       *MemoryCapsule `json:"capsule,omitempty"`
	SemanticDelta *SemanticDelta `json:"semantic_delta,omitempty"`
}

type TurnFinalizeRequest struct {
	Schema                      string                   `json:"schema"`
	Host                        string                   `json:"host"`
	SessionID                   string                   `json:"session_id"`
	TurnID                      string                   `json:"turn_id"`
	SourceEventKey              string                   `json:"source_event_key"`
	IdempotencyKey              string                   `json:"idempotency_key"`
	BindingDigest               string                   `json:"binding_digest"`
	PolicyEpoch                 int64                    `json:"policy_epoch"`
	ResolverEpoch               int64                    `json:"resolver_epoch"`
	Candidates                  []PrivateMemoryCandidate `json:"candidates"`
	operation                   string
	targetObjectID              string
	expectedTargetContentDigest string
	expectedTargetGeneration    int
}

type TurnNoChangeRequest struct {
	Schema         string `json:"schema"`
	Host           string `json:"host"`
	SessionID      string `json:"session_id"`
	TurnID         string `json:"turn_id"`
	SourceEventKey string `json:"source_event_key"`
	IdempotencyKey string `json:"idempotency_key"`
	BindingDigest  string `json:"binding_digest"`
	PolicyEpoch    int64  `json:"policy_epoch"`
	ResolverEpoch  int64  `json:"resolver_epoch"`
}

type TurnFinalizeResult struct {
	LedgerID        string               `json:"ledger_id"`
	Status          string               `json:"status"`
	FinalizeReceipt TurnFinalizeReceipt  `json:"finalize_receipt"`
	Receipts        []MemoryWriteReceipt `json:"receipts"`
}

type MemoryWriteReceipt struct {
	Schema             string                     `json:"schema"`
	ReceiptID          string                     `json:"receipt_id"`
	LedgerID           string                     `json:"ledger_id"`
	CandidateID        string                     `json:"candidate_id,omitempty"`
	CandidateVersion   int                        `json:"candidate_version,omitempty"`
	Status             capture.WriteReceiptStatus `json:"status"`
	Destination        capture.Destination        `json:"destination"`
	DestinationStoreID string                     `json:"destination_store_id"`
	SafeProvenance     MemoryWriteProvenance      `json:"safe_provenance"`
	ContentDigest      string                     `json:"content_digest,omitempty"`
	ObjectID           string                     `json:"object_id,omitempty"`
	ReasonCode         string                     `json:"reason_code,omitempty"`
	PolicyEpoch        int64                      `json:"policy_epoch"`
	ResolverEpoch      int64                      `json:"resolver_epoch"`
	MeasurementMethod  string                     `json:"measurement_method"`
	CreatedAt          string                     `json:"created_at"`
}

type TurnFinalizeReceipt struct {
	Schema             string                `json:"schema"`
	ReceiptID          string                `json:"receipt_id"`
	LedgerID           string                `json:"ledger_id"`
	Status             string                `json:"status"`
	Destination        capture.Destination   `json:"destination"`
	DestinationStoreID string                `json:"destination_store_id"`
	SafeProvenance     MemoryWriteProvenance `json:"safe_provenance"`
	PolicyEpoch        int64                 `json:"policy_epoch"`
	ResolverEpoch      int64                 `json:"resolver_epoch"`
	CreatedAt          string                `json:"created_at"`
}

type MemoryWriteProvenance struct {
	Host           string `json:"host"`
	SessionID      string `json:"session_id"`
	TurnID         string `json:"turn_id"`
	SourceEventKey string `json:"source_event_key"`
}

type MemoryPresentationRequest struct {
	CandidateID            string `json:"candidate_id"`
	CandidateVersion       int    `json:"candidate_version"`
	ContentDigest          string `json:"content_digest"`
	BindingDigest          string `json:"binding_digest"`
	TrustedSurfaceKind     string `json:"trusted_surface_kind"`
	TrustedSurfaceInstance string `json:"trusted_surface_instance"`
}

type MemoryPresentationReceipt struct {
	Schema                 string `json:"schema"`
	ReceiptID              string `json:"receipt_id"`
	CandidateID            string `json:"candidate_id"`
	CandidateVersion       int    `json:"candidate_version"`
	ContentDigest          string `json:"content_digest"`
	BindingDigest          string `json:"binding_digest"`
	TrustedSurfaceKind     string `json:"trusted_surface_kind"`
	TrustedSurfaceInstance string `json:"trusted_surface_instance"`
	PresentedAt            string `json:"presented_at"`
	GraceExpiresAt         string `json:"grace_expires_at"`
}

// TerminalMemoryReadinessFact is derived from a terminal write receipt, the
// current active object, and the bound turn ledger. A presentation receipt is
// optional audit evidence that Memory Home displayed the exact card; ordinary
// Personal continuity does not require the user to open Home before saving.
type TerminalMemoryReadinessFact struct {
	ReceiptID             string   `json:"receipt_id"`
	PresentationReceiptID string   `json:"presentation_receipt_id,omitempty"`
	ObjectID              string   `json:"object_id"`
	EvidenceIDs           []string `json:"evidence_ids,omitempty"`
	Status                string   `json:"status"`
	ContentDigest         string   `json:"content_digest"`
	MemoryKind            string   `json:"memory_kind"`
	ConversationScope     string   `json:"conversation_scope"`
	BindingDigest         string   `json:"binding_digest"`
	RepositoryID          string   `json:"repository_id"`
	Host                  string   `json:"host"`
	SessionRef            string   `json:"session_ref"`
	CreatedAt             string   `json:"created_at"`
	Active                bool     `json:"active"`
}

type MemoryTrayCandidateView struct {
	CandidateID       string                 `json:"candidate_id"`
	LedgerID          string                 `json:"ledger_id"`
	Version           int                    `json:"version"`
	State             string                 `json:"state"`
	Operation         string                 `json:"operation"`
	TargetObjectID    string                 `json:"target_object_id,omitempty"`
	CanonicalObjectID string                 `json:"canonical_object_id,omitempty"`
	Current           bool                   `json:"current"`
	ProjectionStatus  string                 `json:"projection_status"`
	DestinationClass  string                 `json:"destination_class"`
	ContentDigest     string                 `json:"content_digest"`
	GraceExpiresAt    string                 `json:"grace_expires_at"`
	Candidate         PrivateMemoryCandidate `json:"candidate"`
	LatestReceipt     MemoryWriteReceipt     `json:"latest_receipt"`
	ReceiptHistory    []MemoryWriteReceipt   `json:"receipt_history"`
}

type preparedPrivateCandidate struct {
	kind    string
	payload []byte
	digest  string
}

type privateCapsuleIdentity struct {
	Schema           string              `json:"schema"`
	Items            []MemoryCapsuleItem `json:"items"`
	RawInputIncluded bool                `json:"raw_input_included"`
}

type privateSemanticIdentity struct {
	Schema           string              `json:"schema"`
	Nodes            []SemanticNode      `json:"nodes,omitempty"`
	Edges            []SemanticEdge      `json:"edges,omitempty"`
	Facts            []SemanticFact      `json:"facts,omitempty"`
	Events           []SemanticEvent     `json:"events,omitempty"`
	Continuity       *SemanticContinuity `json:"continuity,omitempty"`
	RawInputIncluded bool                `json:"raw_input_included"`
}

type trayCandidateRow struct {
	id, ledgerID, kind, operation, targetObjectID, targetContentDigest string
	digest, payload, state, graceExpires                               string
	host, sessionID, bindingDigest                                     string
	version, targetLogicalGeneration                                   int
	policyEpoch, resolverEpoch                                         int64
	destination                                                        string
}

func (s *Store) trayDestination() (string, error) {
	if s.storeKind == StoreKindPersonal {
		return "personal", nil
	}
	return "", ErrMemoryTrayUnavailable
}

func (s *Store) productTrayRequired() bool {
	return s.storeKind == StoreKindPersonal
}

func validateTrayEnvelope(host, sessionID, turnID, sourceEventKey, idempotencyKey, bindingDigest string, policyEpoch, resolverEpoch int64) error {
	if !validHost(host) {
		return errors.New("turn host is unsupported")
	}
	for name, value := range map[string]string{
		"session_id": sessionID, "turn_id": turnID, "source_event_key": sourceEventKey,
		"idempotency_key": idempotencyKey,
	} {
		// Envelope identifiers are typed metadata, not user content. Their closed
		// grammar already excludes paths, whitespace, controls, and transcript
		// text; running opaque generated IDs through the content-secret detector
		// would incorrectly reject our own SHA-256 correlations.
		if !validTrayIdentifier(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if !trayBindingDigestPattern.MatchString(bindingDigest) {
		return errors.New("binding_digest must be a canonical SHA-256 digest")
	}
	if policyEpoch < 0 || resolverEpoch < 0 {
		return errors.New("epochs must be non-negative")
	}
	return nil
}

func validateTrayGrace(grace time.Duration) error {
	if grace < 0 || grace > 30*time.Second {
		return errors.New("private memory write delay must be between 0s and 30s")
	}
	return nil
}

func preparePrivateCandidate(candidate PrivateMemoryCandidate) (preparedPrivateCandidate, error) {
	var identity any
	switch candidate.Kind {
	case PrivateMemoryCandidateCapsule:
		if candidate.Capsule == nil || candidate.SemanticDelta != nil || len(candidate.Capsule.Items) != 1 {
			return preparedPrivateCandidate{}, errors.New("capsule candidate must contain exactly one item")
		}
		if err := validateMemoryCapsule(*candidate.Capsule); err != nil {
			return preparedPrivateCandidate{}, err
		}
		identity = privateCapsuleIdentity{
			Schema: candidate.Capsule.Schema, Items: candidate.Capsule.Items,
			RawInputIncluded: candidate.Capsule.RawInputIncluded,
		}
	case PrivateMemoryCandidateSemanticDelta:
		if candidate.SemanticDelta == nil || candidate.Capsule != nil {
			return preparedPrivateCandidate{}, errors.New("semantic candidate shape is invalid")
		}
		for _, event := range candidate.SemanticDelta.Events {
			if len(event.Claims) > 0 {
				return preparedPrivateCandidate{}, errors.New("private semantic claims require the governed projection path")
			}
		}
		if err := validateSemanticDelta(*candidate.SemanticDelta); err != nil {
			return preparedPrivateCandidate{}, err
		}
		identity = privateSemanticIdentity{
			Schema: candidate.SemanticDelta.Schema, Nodes: candidate.SemanticDelta.Nodes,
			Edges: candidate.SemanticDelta.Edges, Facts: candidate.SemanticDelta.Facts,
			Events: candidate.SemanticDelta.Events, Continuity: candidate.SemanticDelta.Continuity,
			RawInputIncluded: candidate.SemanticDelta.RawInputIncluded,
		}
	default:
		return preparedPrivateCandidate{}, errors.New("candidate kind is unsupported")
	}
	payload, err := json.Marshal(candidate)
	if err != nil {
		return preparedPrivateCandidate{}, err
	}
	identityPayload, err := json.Marshal(identity)
	if err != nil {
		return preparedPrivateCandidate{}, err
	}
	digest := sha256.Sum256(identityPayload)
	return preparedPrivateCandidate{kind: candidate.Kind, payload: payload, digest: hex.EncodeToString(digest[:])}, nil
}

func prepareBoundPrivateCandidate(candidate PrivateMemoryCandidate, host, sessionID string) (preparedPrivateCandidate, error) {
	switch candidate.Kind {
	case PrivateMemoryCandidateCapsule:
		if candidate.Capsule == nil || candidate.Capsule.Source.Host != host {
			return preparedPrivateCandidate{}, errors.New("capsule source host does not match turn provenance")
		}
	case PrivateMemoryCandidateSemanticDelta:
		if candidate.SemanticDelta == nil || candidate.SemanticDelta.Source.Host != host {
			return preparedPrivateCandidate{}, errors.New("semantic source host does not match turn provenance")
		}
		sourceSessionID := candidate.SemanticDelta.Source.SessionID
		if sourceSessionID != "" && sourceSessionID != sessionID && sourceSessionID != opaqueTurnCorrelation("session", sessionID) {
			return preparedPrivateCandidate{}, errors.New("semantic source session does not match turn provenance")
		}
		if sourceSessionID != "" {
			delta := *candidate.SemanticDelta
			delta.Source.SessionID = opaqueTurnCorrelation("session", sourceSessionID)
			candidate.SemanticDelta = &delta
		}
	}
	return preparePrivateCandidate(candidate)
}

func opaqueTurnCorrelation(kind, value string) string {
	prefix := kind + ":"
	if strings.HasPrefix(value, prefix) && trayBindingDigestPattern.MatchString(strings.TrimPrefix(value, prefix)) {
		return value
	}
	digest := sha256.Sum256([]byte(kind + "\x1f" + value))
	return prefix + hex.EncodeToString(digest[:])
}

func protectTurnEnvelopeIDs(sessionID, turnID, sourceEventKey, idempotencyKey string) (string, string, string, string) {
	return opaqueTurnCorrelation("session", sessionID),
		opaqueTurnCorrelation("turn", turnID),
		opaqueTurnCorrelation("event", sourceEventKey),
		opaqueTurnCorrelation("idempotency", idempotencyKey)
}

func requestDigest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

// FinalizeTurn persists either a complete safe pending batch or content-free
// rejection receipts. Candidate payload validation happens before a DB
// transaction is opened, so rejected bytes never reach SQLite or its WAL.
func (s *Store) FinalizeTurn(req TurnFinalizeRequest, now time.Time, grace time.Duration) (TurnFinalizeResult, error) {
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.finalizeTurnForAuthority(
		req, now, grace, expectedBinding, expectedPolicy, expectedResolver,
	)
}

// FinalizeTurnForVerifiedBinding admits a Personal write only after the server
// has re-verified the signed workspace binding carried by this request. The
// product policy epoch is currently fixed at zero by every host adapter.
func (s *Store) FinalizeTurnForVerifiedBinding(
	req TurnFinalizeRequest,
	now time.Time,
	grace time.Duration,
	bindingDigest, repositoryID string,
	resolverEpoch int64,
) (TurnFinalizeResult, error) {
	if !trayBindingDigestPattern.MatchString(bindingDigest) || !validTrayIdentifier(repositoryID) || resolverEpoch < 1 {
		return TurnFinalizeResult{}, ErrProductRuntimeMismatch
	}
	return s.finalizeTurnForAuthority(req, now, grace, bindingDigest, 0, resolverEpoch)
}

func (s *Store) finalizeTurnForAuthority(
	req TurnFinalizeRequest,
	now time.Time,
	grace time.Duration,
	expectedBinding string,
	expectedPolicy, expectedResolver int64,
) (TurnFinalizeResult, error) {
	destination, err := s.trayDestination()
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	if err := validateTrayEnvelope(req.Host, req.SessionID, req.TurnID, req.SourceEventKey, req.IdempotencyKey, req.BindingDigest, req.PolicyEpoch, req.ResolverEpoch); err != nil {
		return TurnFinalizeResult{}, err
	}
	if req.Schema != TurnFinalizeRequestSchema {
		return TurnFinalizeResult{}, errors.New("invalid turn finalize schema")
	}
	if req.BindingDigest != expectedBinding || req.PolicyEpoch != expectedPolicy || req.ResolverEpoch != expectedResolver {
		return TurnFinalizeResult{}, ErrProductRuntimeMismatch
	}
	if len(req.Candidates) == 0 || len(req.Candidates) > 20 {
		return TurnFinalizeResult{}, errors.New("finalize requires 1..20 candidates")
	}
	operation := req.operation
	if operation == "" {
		operation = "create"
	}
	if operation != "create" && operation != "correct" {
		return TurnFinalizeResult{}, errors.New("private memory operation is unsupported")
	}
	if operation == "correct" {
		if len(req.Candidates) != 1 || !validTrayIdentifier(req.targetObjectID) {
			return TurnFinalizeResult{}, errors.New("correction target is invalid")
		}
	} else if req.targetObjectID != "" {
		return TurnFinalizeResult{}, errors.New("create cannot carry a correction target")
	}
	if err := validateTrayGrace(grace); err != nil {
		return TurnFinalizeResult{}, err
	}
	digest, err := requestDigest(req)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	operationIdentity := digest + "\x1f" + operation + "\x1f" + req.targetObjectID
	if req.expectedTargetContentDigest != "" {
		operationIdentity += "\x1f" + req.expectedTargetContentDigest
	}
	if req.expectedTargetGeneration > 0 {
		operationIdentity += fmt.Sprintf("\x1f%d", req.expectedTargetGeneration)
	}
	operationDigest := sha256.Sum256([]byte(operationIdentity))
	digest = hex.EncodeToString(operationDigest[:])
	prepared := make([]preparedPrivateCandidate, len(req.Candidates))
	unsafeBatch := false
	for index, candidate := range req.Candidates {
		item, candidateErr := prepareBoundPrivateCandidate(candidate, req.Host, req.SessionID)
		if candidateErr != nil {
			unsafeBatch = true
			continue
		}
		prepared[index] = item
	}
	req.SessionID, req.TurnID, req.SourceEventKey, req.IdempotencyKey = protectTurnEnvelopeIDs(
		req.SessionID, req.TurnID, req.SourceEventKey, req.IdempotencyKey,
	)

	tx, err := s.db.Begin()
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	defer tx.Rollback()
	targetContentDigest := ""
	if operation == "correct" && !unsafeBatch {
		var targetKind, lifecycle, targetBinding string
		var targetGeneration int
		var targetPolicy, targetResolver int64
		if err := tx.QueryRow(`
			SELECT object.candidate_kind, object.lifecycle, object.content_digest,
			       object.logical_generation, ledger.binding_digest,
			       ledger.policy_epoch, ledger.resolver_epoch
			  FROM private_memory_objects object
			  JOIN memory_tray_candidates candidate
			    ON candidate.candidate_id=object.created_from_candidate_id
			  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
			 WHERE object.object_id=?`,
			req.targetObjectID,
		).Scan(
			&targetKind, &lifecycle, &targetContentDigest, &targetGeneration,
			&targetBinding, &targetPolicy, &targetResolver,
		); err != nil {
			return TurnFinalizeResult{}, err
		}
		if targetBinding != expectedBinding || targetPolicy != expectedPolicy || targetResolver != expectedResolver {
			return TurnFinalizeResult{}, ErrProductRuntimeMismatch
		}
		if lifecycle != "active" || targetKind != prepared[0].kind {
			return TurnFinalizeResult{}, errors.New("correction target is inactive or has a different kind")
		}
		if req.expectedTargetContentDigest != "" &&
			targetContentDigest != req.expectedTargetContentDigest {
			return TurnFinalizeResult{}, ErrMemoryCorrectionConflict
		}
		if req.expectedTargetGeneration > 0 &&
			targetGeneration != req.expectedTargetGeneration {
			return TurnFinalizeResult{}, ErrMemoryScopeConflict
		}
	}
	ledgerID, err := newOpaqueID("turn")
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	finalizeReceiptID, err := newOpaqueID("receipt")
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	// Ordinary Personal memory is eligible for canonical persistence
	// immediately. Keep the legacy column populated for schema compatibility,
	// but never turn it into a user-facing review gate.
	graceExpiresAt := now.UTC().Format(time.RFC3339Nano)
	state := TurnFinalizedCandidates
	if unsafeBatch {
		state = TurnFinalizedRejected
	}
	inserted, err := tx.Exec(`
		INSERT OR IGNORE INTO turn_ledgers(
			ledger_id, finalize_receipt_id, host, session_id, turn_id, source_event_key, idempotency_key,
			binding_digest, destination_store_id, destination_class, policy_epoch,
			resolver_epoch, request_digest, state, created_at, finalized_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ledgerID, finalizeReceiptID, req.Host, req.SessionID, req.TurnID, req.SourceEventKey, req.IdempotencyKey,
		req.BindingDigest, s.storeID, destination, req.PolicyEpoch, req.ResolverEpoch,
		digest, state, createdAt, createdAt,
	)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	if affected, _ := inserted.RowsAffected(); affected == 0 {
		existing, found, loadErr := loadExistingTurnTx(tx, req.Host, req.SessionID, req.TurnID, digest)
		if loadErr != nil {
			return TurnFinalizeResult{}, loadErr
		}
		if !found {
			return TurnFinalizeResult{}, ErrTurnFinalizeConflict
		}
		return existing, nil
	}

	result := TurnFinalizeResult{
		LedgerID: ledgerID,
		Status:   state,
		FinalizeReceipt: newTurnFinalizeReceipt(
			finalizeReceiptID, ledgerID, state, capture.Destination(destination), s.storeID,
			req.Host, req.SessionID, req.TurnID, req.SourceEventKey,
			req.PolicyEpoch, req.ResolverEpoch, createdAt,
		),
		Receipts: make([]MemoryWriteReceipt, 0, len(req.Candidates)),
	}
	for index := range req.Candidates {
		if unsafeBatch {
			receipt, err := insertWriteReceiptTx(tx, MemoryWriteReceipt{
				LedgerID: ledgerID, Status: MemoryWriteRejected, Destination: capture.Destination(destination),
				ReasonCode: "unsafe_payload", PolicyEpoch: req.PolicyEpoch, ResolverEpoch: req.ResolverEpoch,
				MeasurementMethod: "host_structured_v1", CreatedAt: createdAt,
			})
			if err != nil {
				return TurnFinalizeResult{}, err
			}
			if err := insertWriteAuditTx(tx, receipt, "finalize", MemoryWriteRejected); err != nil {
				return TurnFinalizeResult{}, err
			}
			if _, err := tx.Exec(`
				INSERT INTO memory_write_idempotency(
					operation, idempotency_key, request_digest, receipt_id, created_at
				) VALUES ('finalize_rejected', ?, ?, ?, ?)`,
				fmt.Sprintf("%s:%d", req.IdempotencyKey, index), digest, receipt.ReceiptID, createdAt,
			); err != nil {
				return TurnFinalizeResult{}, err
			}
			result.Receipts = append(result.Receipts, receipt)
			continue
		}
		candidateID, err := newOpaqueID("candidate")
		if err != nil {
			return TurnFinalizeResult{}, err
		}
		if _, err := tx.Exec(`
			INSERT INTO memory_tray_candidates(
				candidate_id, ledger_id, candidate_kind, operation, target_object_id, target_content_digest,
				target_logical_generation,
				version, content_digest, payload_json,
				state, grace_expires_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, 0), 1, ?, ?, 'pending', ?, ?, ?)`,
			candidateID, ledgerID, prepared[index].kind, operation, req.targetObjectID,
			targetContentDigest, req.expectedTargetGeneration, prepared[index].digest,
			string(prepared[index].payload), graceExpiresAt, createdAt, createdAt,
		); err != nil {
			return TurnFinalizeResult{}, err
		}
		receipt, err := insertWriteReceiptTx(tx, MemoryWriteReceipt{
			LedgerID: ledgerID, CandidateID: candidateID, CandidateVersion: 1,
			Status: MemoryWritePending, Destination: capture.Destination(destination), ContentDigest: prepared[index].digest,
			PolicyEpoch: req.PolicyEpoch, ResolverEpoch: req.ResolverEpoch,
			MeasurementMethod: "host_structured_v1", CreatedAt: createdAt,
		})
		if err != nil {
			return TurnFinalizeResult{}, err
		}
		if err := insertWriteAuditTx(tx, receipt, "finalize", MemoryWritePending); err != nil {
			return TurnFinalizeResult{}, err
		}
		if _, err := tx.Exec(`
			INSERT INTO memory_write_idempotency(
				operation, idempotency_key, request_digest, receipt_id, created_at
			) VALUES ('finalize', ?, ?, ?, ?)`,
			fmt.Sprintf("%s:%d", req.IdempotencyKey, index), digest, receipt.ReceiptID, createdAt,
		); err != nil {
			return TurnFinalizeResult{}, err
		}
		result.Receipts = append(result.Receipts, receipt)
	}
	if err := tx.Commit(); err != nil {
		return TurnFinalizeResult{}, err
	}
	return result, nil
}

func (s *Store) FinalizeTurnNoChange(req TurnNoChangeRequest, now time.Time) (TurnFinalizeResult, error) {
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.finalizeTurnNoChangeForAuthority(
		req, now, expectedBinding, expectedPolicy, expectedResolver,
	)
}

func (s *Store) FinalizeTurnNoChangeForVerifiedBinding(
	req TurnNoChangeRequest,
	now time.Time,
	bindingDigest, repositoryID string,
	resolverEpoch int64,
) (TurnFinalizeResult, error) {
	if !trayBindingDigestPattern.MatchString(bindingDigest) || !validTrayIdentifier(repositoryID) || resolverEpoch < 1 {
		return TurnFinalizeResult{}, ErrProductRuntimeMismatch
	}
	return s.finalizeTurnNoChangeForAuthority(req, now, bindingDigest, 0, resolverEpoch)
}

func (s *Store) finalizeTurnNoChangeForAuthority(
	req TurnNoChangeRequest,
	now time.Time,
	expectedBinding string,
	expectedPolicy, expectedResolver int64,
) (TurnFinalizeResult, error) {
	destination, err := s.trayDestination()
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	if err := validateTrayEnvelope(req.Host, req.SessionID, req.TurnID, req.SourceEventKey, req.IdempotencyKey, req.BindingDigest, req.PolicyEpoch, req.ResolverEpoch); err != nil {
		return TurnFinalizeResult{}, err
	}
	if req.Schema != TurnNoChangeRequestSchema {
		return TurnFinalizeResult{}, errors.New("invalid turn no-change schema")
	}
	if req.BindingDigest != expectedBinding || req.PolicyEpoch != expectedPolicy || req.ResolverEpoch != expectedResolver {
		return TurnFinalizeResult{}, ErrProductRuntimeMismatch
	}
	digest, err := requestDigest(req)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	req.SessionID, req.TurnID, req.SourceEventKey, req.IdempotencyKey = protectTurnEnvelopeIDs(
		req.SessionID, req.TurnID, req.SourceEventKey, req.IdempotencyKey,
	)
	tx, err := s.db.Begin()
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	defer tx.Rollback()
	ledgerID, err := newOpaqueID("turn")
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	finalizeReceiptID, err := newOpaqueID("receipt")
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	inserted, err := tx.Exec(`
		INSERT OR IGNORE INTO turn_ledgers(
			ledger_id, finalize_receipt_id, host, session_id, turn_id, source_event_key, idempotency_key,
			binding_digest, destination_store_id, destination_class, policy_epoch,
			resolver_epoch, request_digest, state, created_at, finalized_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'no_change', ?, ?)`,
		ledgerID, finalizeReceiptID, req.Host, req.SessionID, req.TurnID, req.SourceEventKey, req.IdempotencyKey,
		req.BindingDigest, s.storeID, destination, req.PolicyEpoch, req.ResolverEpoch,
		digest, createdAt, createdAt,
	)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	if affected, _ := inserted.RowsAffected(); affected == 0 {
		existing, found, loadErr := loadExistingTurnTx(tx, req.Host, req.SessionID, req.TurnID, digest)
		if loadErr != nil {
			return TurnFinalizeResult{}, loadErr
		}
		if !found || existing.Status != TurnFinalizedNoChange {
			return TurnFinalizeResult{}, ErrTurnAlreadyFinalized
		}
		return existing, nil
	}
	if err := tx.Commit(); err != nil {
		return TurnFinalizeResult{}, err
	}
	return TurnFinalizeResult{
		LedgerID: ledgerID,
		Status:   TurnFinalizedNoChange,
		FinalizeReceipt: newTurnFinalizeReceipt(
			finalizeReceiptID, ledgerID, TurnFinalizedNoChange, capture.Destination(destination), s.storeID,
			req.Host, req.SessionID, req.TurnID, req.SourceEventKey,
			req.PolicyEpoch, req.ResolverEpoch, createdAt,
		),
		Receipts: []MemoryWriteReceipt{},
	}, nil
}

func loadExistingTurnTx(tx *sql.Tx, host, sessionID, turnID, digest string) (TurnFinalizeResult, bool, error) {
	var ledgerID, finalizeReceiptID, storedDigest, state, destination, destinationStoreID string
	var storedHost, storedSessionID, storedTurnID, sourceEventKey, createdAt string
	var policyEpoch, resolverEpoch int64
	err := tx.QueryRow(`
		SELECT ledger_id, finalize_receipt_id, request_digest, state,
		       destination_class, destination_store_id, host, session_id, turn_id,
		       source_event_key, policy_epoch, resolver_epoch, finalized_at
		  FROM turn_ledgers
		 WHERE host=? AND session_id=? AND turn_id=?`, host, sessionID, turnID,
	).Scan(
		&ledgerID, &finalizeReceiptID, &storedDigest, &state,
		&destination, &destinationStoreID, &storedHost, &storedSessionID, &storedTurnID,
		&sourceEventKey, &policyEpoch, &resolverEpoch, &createdAt,
	)
	if err == sql.ErrNoRows {
		return TurnFinalizeResult{}, false, nil
	}
	if err != nil {
		return TurnFinalizeResult{}, false, err
	}
	if storedDigest != digest {
		return TurnFinalizeResult{}, false, ErrTurnAlreadyFinalized
	}
	receipts, err := loadLatestLedgerReceiptsTx(tx, ledgerID)
	if err != nil {
		return TurnFinalizeResult{}, false, err
	}
	return TurnFinalizeResult{
		LedgerID: ledgerID,
		Status:   state,
		FinalizeReceipt: newTurnFinalizeReceipt(
			finalizeReceiptID, ledgerID, state, capture.Destination(destination), destinationStoreID,
			storedHost, storedSessionID, storedTurnID, sourceEventKey,
			policyEpoch, resolverEpoch, createdAt,
		),
		Receipts: receipts,
	}, true, nil
}

func newTurnFinalizeReceipt(
	receiptID, ledgerID, status string,
	destination capture.Destination,
	destinationStoreID, host, sessionID, turnID, sourceEventKey string,
	policyEpoch, resolverEpoch int64,
	createdAt string,
) TurnFinalizeReceipt {
	return TurnFinalizeReceipt{
		Schema:             TurnFinalizeReceiptSchema,
		ReceiptID:          receiptID,
		LedgerID:           ledgerID,
		Status:             status,
		Destination:        destination,
		DestinationStoreID: destinationStoreID,
		SafeProvenance: MemoryWriteProvenance{
			Host:           host,
			SessionID:      sessionID,
			TurnID:         turnID,
			SourceEventKey: sourceEventKey,
		},
		PolicyEpoch:   policyEpoch,
		ResolverEpoch: resolverEpoch,
		CreatedAt:     createdAt,
	}
}

func insertWriteReceiptTx(tx *sql.Tx, receipt MemoryWriteReceipt) (MemoryWriteReceipt, error) {
	receiptID, err := newOpaqueID("receipt")
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	receipt.ReceiptID = receiptID
	receipt.Schema = capture.WriteReceiptSchema
	if err := tx.QueryRow(`
		SELECT destination_store_id, host, session_id, turn_id, source_event_key
		  FROM turn_ledgers WHERE ledger_id=?`, receipt.LedgerID,
	).Scan(
		&receipt.DestinationStoreID, &receipt.SafeProvenance.Host,
		&receipt.SafeProvenance.SessionID, &receipt.SafeProvenance.TurnID,
		&receipt.SafeProvenance.SourceEventKey,
	); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := capture.ValidateWriteReceipt(capture.WriteReceipt{
		Schema:      receipt.Schema,
		Status:      receipt.Status,
		ReceiptID:   receipt.ReceiptID,
		Destination: receipt.Destination,
		ObjectID:    receipt.ObjectID,
	}); err != nil {
		return MemoryWriteReceipt{}, fmt.Errorf("canonical write receipt: %w", err)
	}
	var candidateID, digest, objectID, reason any
	if receipt.CandidateID != "" {
		candidateID = receipt.CandidateID
	}
	if receipt.ContentDigest != "" {
		digest = receipt.ContentDigest
	}
	if receipt.ObjectID != "" {
		objectID = receipt.ObjectID
	}
	if receipt.ReasonCode != "" {
		reason = receipt.ReasonCode
	}
	_, err = tx.Exec(`
		INSERT INTO memory_write_receipts(
			receipt_id, ledger_id, candidate_id, candidate_version, status,
			destination_class, destination_store_id, provenance_host,
			provenance_session_id, provenance_turn_id, provenance_source_event_key,
			content_digest, object_id, reason_code, policy_epoch,
			resolver_epoch, measurement_method, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		receipt.ReceiptID, receipt.LedgerID, candidateID, receipt.CandidateVersion,
		receipt.Status, receipt.Destination, receipt.DestinationStoreID,
		receipt.SafeProvenance.Host, receipt.SafeProvenance.SessionID,
		receipt.SafeProvenance.TurnID, receipt.SafeProvenance.SourceEventKey,
		digest, objectID, reason,
		receipt.PolicyEpoch, receipt.ResolverEpoch, receipt.MeasurementMethod, receipt.CreatedAt,
	)
	return receipt, err
}

func insertWriteAuditTx(tx *sql.Tx, receipt MemoryWriteReceipt, action string, outcome capture.WriteReceiptStatus) error {
	var candidateID, reason any
	if receipt.CandidateID != "" {
		candidateID = receipt.CandidateID
	}
	if receipt.ReasonCode != "" {
		reason = receipt.ReasonCode
	}
	_, err := tx.Exec(`
		INSERT INTO memory_write_audit(
			ledger_id, candidate_id, receipt_id, action, outcome, reason_code,
			policy_epoch, resolver_epoch, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		receipt.LedgerID, candidateID, receipt.ReceiptID, action, outcome, reason,
		receipt.PolicyEpoch, receipt.ResolverEpoch, receipt.CreatedAt,
	)
	return err
}

func scanWriteReceipt(scanner interface{ Scan(...any) error }) (MemoryWriteReceipt, error) {
	receipt := MemoryWriteReceipt{Schema: capture.WriteReceiptSchema}
	var candidateID, digest, objectID, reason sql.NullString
	err := scanner.Scan(
		&receipt.ReceiptID, &receipt.LedgerID, &candidateID, &receipt.CandidateVersion,
		&receipt.Status, &receipt.Destination, &receipt.DestinationStoreID,
		&receipt.SafeProvenance.Host, &receipt.SafeProvenance.SessionID,
		&receipt.SafeProvenance.TurnID, &receipt.SafeProvenance.SourceEventKey,
		&digest, &objectID, &reason,
		&receipt.PolicyEpoch, &receipt.ResolverEpoch, &receipt.MeasurementMethod, &receipt.CreatedAt,
	)
	if candidateID.Valid {
		receipt.CandidateID = candidateID.String
	}
	if digest.Valid {
		receipt.ContentDigest = digest.String
	}
	if objectID.Valid {
		receipt.ObjectID = objectID.String
	}
	if reason.Valid {
		receipt.ReasonCode = reason.String
	}
	return receipt, err
}

const receiptColumns = `
	receipt_id, ledger_id, candidate_id, candidate_version, status,
	destination_class, destination_store_id, provenance_host, provenance_session_id,
	provenance_turn_id, provenance_source_event_key,
	content_digest, object_id, reason_code, policy_epoch,
	resolver_epoch, measurement_method, created_at`

const qualifiedReceiptColumns = `
	receipt.receipt_id, receipt.ledger_id, receipt.candidate_id, receipt.candidate_version, receipt.status,
	receipt.destination_class, receipt.destination_store_id, receipt.provenance_host, receipt.provenance_session_id,
	receipt.provenance_turn_id, receipt.provenance_source_event_key,
	receipt.content_digest, receipt.object_id, receipt.reason_code, receipt.policy_epoch,
	receipt.resolver_epoch, receipt.measurement_method, receipt.created_at`

func loadLatestLedgerReceiptsTx(tx *sql.Tx, ledgerID string) ([]MemoryWriteReceipt, error) {
	rows, err := tx.Query(`
		SELECT `+receiptColumns+` FROM memory_write_receipts receipt
		 WHERE ledger_id=?
		   AND (candidate_id IS NULL OR receipt_id=(
		       SELECT latest.receipt_id FROM memory_write_receipts latest
		        WHERE latest.candidate_id=receipt.candidate_id
		          AND latest.ledger_id=receipt.ledger_id
		        ORDER BY latest.rowid DESC LIMIT 1))
		 ORDER BY rowid`, ledgerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var receipts []MemoryWriteReceipt
	for rows.Next() {
		receipt, err := scanWriteReceipt(rows)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

func loadLatestCandidateReceiptTx(tx *sql.Tx, candidateID string) (MemoryWriteReceipt, error) {
	return scanWriteReceipt(tx.QueryRow(`
		SELECT `+receiptColumns+` FROM memory_write_receipts
		 WHERE candidate_id=? ORDER BY rowid DESC LIMIT 1`, candidateID))
}

func loadCandidateCommitReceiptTx(tx *sql.Tx, row trayCandidateRow) (MemoryWriteReceipt, error) {
	return scanWriteReceipt(tx.QueryRow(`
		SELECT `+receiptColumns+` FROM memory_write_receipts
		 WHERE candidate_id=? AND ledger_id=? AND candidate_version=?
		   AND ((?='create' AND status IN ('created','deduplicated')) OR
		        (?='correct' AND status='updated' AND reason_code='user_corrected'))
		 ORDER BY rowid DESC LIMIT 1`, row.id, row.ledgerID, row.version, row.operation, row.operation))
}

func loadTrayCandidateTx(tx *sql.Tx, candidateID string) (trayCandidateRow, error) {
	var row trayCandidateRow
	err := tx.QueryRow(`
		SELECT candidate.candidate_id, candidate.ledger_id, candidate.candidate_kind,
		       candidate.operation, COALESCE(candidate.target_object_id, ''),
		       COALESCE(candidate.target_content_digest, ''),
		       COALESCE(candidate.target_logical_generation, 0),
		       candidate.version, candidate.content_digest, candidate.payload_json,
		       candidate.state, candidate.grace_expires_at, ledger.binding_digest, ledger.policy_epoch,
		       ledger.resolver_epoch, ledger.destination_class, ledger.host, ledger.session_id
		  FROM memory_tray_candidates candidate
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 WHERE candidate.candidate_id=?`, candidateID,
	).Scan(&row.id, &row.ledgerID, &row.kind, &row.operation,
		&row.targetObjectID, &row.targetContentDigest, &row.targetLogicalGeneration,
		&row.version, &row.digest, &row.payload,
		&row.state, &row.graceExpires, &row.bindingDigest, &row.policyEpoch, &row.resolverEpoch, &row.destination,
		&row.host, &row.sessionID)
	return row, err
}

func scanMemoryPresentationReceipt(scanner interface{ Scan(...any) error }) (MemoryPresentationReceipt, error) {
	receipt := MemoryPresentationReceipt{Schema: MemoryPresentationReceiptSchema}
	err := scanner.Scan(
		&receipt.ReceiptID, &receipt.CandidateID, &receipt.CandidateVersion,
		&receipt.ContentDigest, &receipt.TrustedSurfaceKind, &receipt.TrustedSurfaceInstance,
		&receipt.BindingDigest, &receipt.PresentedAt, &receipt.GraceExpiresAt,
	)
	return receipt, err
}

func loadExactMemoryPresentationReceiptTx(tx *sql.Tx, req MemoryPresentationRequest) (MemoryPresentationReceipt, error) {
	return scanMemoryPresentationReceipt(tx.QueryRow(`
		SELECT receipt_id, candidate_id, candidate_version, content_digest,
		       trusted_surface_kind, trusted_surface_instance, binding_digest,
		       presented_at, grace_expires_at
		  FROM memory_presentation_receipts
		 WHERE candidate_id=? AND candidate_version=? AND content_digest=?
		   AND trusted_surface_kind=? AND trusted_surface_instance=? AND binding_digest=?
		 ORDER BY rowid LIMIT 1`,
		req.CandidateID, req.CandidateVersion, req.ContentDigest,
		req.TrustedSurfaceKind, req.TrustedSurfaceInstance, req.BindingDigest,
	))
}

// PresentMemoryTrayCandidate records content-free proof that an authenticated,
// trusted human surface rendered the exact current candidate. Presentation is
// optional audit evidence only; it never delays or authorizes persistence. The
// empty-deadline branch only upgrades candidates created by an older runtime.
func (s *Store) PresentMemoryTrayCandidate(
	req MemoryPresentationRequest,
	now time.Time,
	grace time.Duration,
) (MemoryPresentationReceipt, error) {
	if _, err := s.trayDestination(); err != nil {
		return MemoryPresentationReceipt{}, err
	}
	if err := validateTrayGrace(grace); err != nil {
		return MemoryPresentationReceipt{}, err
	}
	if !validTrayIdentifier(req.CandidateID) || req.CandidateVersion < 1 ||
		!trayBindingDigestPattern.MatchString(req.ContentDigest) ||
		!trayBindingDigestPattern.MatchString(req.BindingDigest) {
		return MemoryPresentationReceipt{}, errors.New("memory presentation identity is invalid")
	}
	if req.TrustedSurfaceKind != "memory_home" || !validTrayIdentifier(req.TrustedSurfaceInstance) {
		return MemoryPresentationReceipt{}, errors.New("memory presentation surface is not trusted")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return MemoryPresentationReceipt{}, err
	}
	defer tx.Rollback()
	row, err := loadTrayCandidateTx(tx, req.CandidateID)
	if err != nil {
		return MemoryPresentationReceipt{}, err
	}
	if row.version != req.CandidateVersion {
		return MemoryPresentationReceipt{}, ErrMemoryTrayVersionConflict
	}
	if row.state != "pending" {
		return MemoryPresentationReceipt{}, ErrMemoryTrayTerminal
	}
	if row.digest != req.ContentDigest {
		return MemoryPresentationReceipt{}, ErrMemoryPresentationConflict
	}
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	if row.bindingDigest != req.BindingDigest || row.bindingDigest != expectedBinding ||
		row.policyEpoch != expectedPolicy || row.resolverEpoch != expectedResolver {
		return MemoryPresentationReceipt{}, ErrProductRuntimeMismatch
	}
	if existing, loadErr := loadExactMemoryPresentationReceiptTx(tx, req); loadErr == nil {
		return existing, nil
	} else if loadErr != sql.ErrNoRows {
		return MemoryPresentationReceipt{}, loadErr
	}

	presentedAt := now.UTC().Format(time.RFC3339Nano)
	graceExpiresAt := row.graceExpires
	if graceExpiresAt == "" {
		graceExpiresAt = presentedAt
	}
	receiptID, err := newOpaqueID("presentation")
	if err != nil {
		return MemoryPresentationReceipt{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO memory_presentation_receipts(
			receipt_id, candidate_id, candidate_version, content_digest,
			trusted_surface_kind, trusted_surface_instance, binding_digest,
			presented_at, grace_expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		receiptID, req.CandidateID, req.CandidateVersion, req.ContentDigest,
		req.TrustedSurfaceKind, req.TrustedSurfaceInstance, req.BindingDigest,
		presentedAt, graceExpiresAt,
	); err != nil {
		return MemoryPresentationReceipt{}, err
	}
	if row.graceExpires == "" {
		updated, err := tx.Exec(`
			UPDATE memory_tray_candidates
			   SET grace_expires_at=?, updated_at=?
			 WHERE candidate_id=? AND version=? AND content_digest=?
			   AND state='pending' AND grace_expires_at=''`,
			graceExpiresAt, presentedAt, req.CandidateID, req.CandidateVersion, req.ContentDigest,
		)
		if err != nil {
			return MemoryPresentationReceipt{}, err
		}
		if affected, _ := updated.RowsAffected(); affected != 1 {
			return MemoryPresentationReceipt{}, ErrMemoryTrayVersionConflict
		}
	}
	receipt := MemoryPresentationReceipt{
		Schema: MemoryPresentationReceiptSchema, ReceiptID: receiptID,
		CandidateID: req.CandidateID, CandidateVersion: req.CandidateVersion,
		ContentDigest: req.ContentDigest, BindingDigest: req.BindingDigest,
		TrustedSurfaceKind: req.TrustedSurfaceKind, TrustedSurfaceInstance: req.TrustedSurfaceInstance,
		PresentedAt: presentedAt, GraceExpiresAt: graceExpiresAt,
	}
	if err := tx.Commit(); err != nil {
		return MemoryPresentationReceipt{}, err
	}
	return receipt, nil
}

// TerminalMemoryReadinessFacts exposes the immutable, content-free half of
// the cross-session readiness chain. repositoryID must come from the signed
// workspace binding that owns the returned binding digest; U6 joins these
// facts with its delivery receipts without inventing onboarding state.
func (s *Store) TerminalMemoryReadinessFacts(
	repositoryID string,
	expectedBindingDigest string,
	limit int,
) ([]TerminalMemoryReadinessFact, error) {
	if _, err := s.trayDestination(); err != nil {
		return nil, err
	}
	if !validTrayIdentifier(repositoryID) {
		return nil, errors.New("readiness repository identity is invalid")
	}
	configuredBindingDigest, _, _ := s.productRuntimeAuthority()
	if !trayBindingDigestPattern.MatchString(expectedBindingDigest) || expectedBindingDigest != configuredBindingDigest {
		return nil, ErrProductRuntimeMismatch
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("readiness fact limit must be between 1 and 100")
	}
	rows, err := s.db.Query(`
		SELECT receipt.receipt_id, COALESCE(presentation.receipt_id, ''), receipt.object_id,
		       receipt.status, candidate.content_digest, candidate.payload_json,
		       ledger.binding_digest, receipt.provenance_host,
		       receipt.provenance_session_id, receipt.created_at
		  FROM memory_write_receipts receipt
		  JOIN memory_tray_candidates candidate
		    ON candidate.candidate_id=receipt.candidate_id
		   AND candidate.version=receipt.candidate_version
		   AND candidate.content_digest=receipt.content_digest
		  JOIN turn_ledgers ledger ON ledger.ledger_id=receipt.ledger_id
		  JOIN private_memory_objects object
		    ON object.object_id=receipt.object_id
		   AND object.content_digest=receipt.content_digest
		   AND object.lifecycle='active'
		  LEFT JOIN memory_presentation_receipts presentation
		    ON presentation.candidate_id=candidate.candidate_id
		   AND presentation.candidate_version=candidate.version
		   AND presentation.content_digest=candidate.content_digest
		   AND presentation.binding_digest=ledger.binding_digest
		   AND presentation.receipt_id=(
		       SELECT first_presentation.receipt_id
		         FROM memory_presentation_receipts first_presentation
		        WHERE first_presentation.candidate_id=candidate.candidate_id
		          AND first_presentation.candidate_version=candidate.version
		          AND first_presentation.content_digest=candidate.content_digest
		          AND first_presentation.binding_digest=ledger.binding_digest
		        ORDER BY first_presentation.rowid
		        LIMIT 1
		   )
		 WHERE receipt.status IN ('created','updated','deduplicated')
		   AND ledger.binding_digest=?
		 ORDER BY receipt.created_at, receipt.receipt_id
		 LIMIT ?`, expectedBindingDigest, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	facts := make([]TerminalMemoryReadinessFact, 0)
	for rows.Next() {
		var fact TerminalMemoryReadinessFact
		var payload string
		if err := rows.Scan(
			&fact.ReceiptID, &fact.PresentationReceiptID, &fact.ObjectID,
			&fact.Status, &fact.ContentDigest, &payload, &fact.BindingDigest,
			&fact.Host, &fact.SessionRef, &fact.CreatedAt,
		); err != nil {
			return nil, err
		}
		kind, scope, err := terminalReadinessCandidateMetadata(payload)
		if err != nil {
			return nil, err
		}
		fact.MemoryKind = kind
		fact.ConversationScope = scope
		fact.RepositoryID = repositoryID
		fact.EvidenceIDs = []string{}
		fact.Active = true
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return facts, nil
}

func terminalReadinessCandidateMetadata(payload string) (string, string, error) {
	var candidate PrivateMemoryCandidate
	if err := json.Unmarshal([]byte(payload), &candidate); err != nil {
		return "", "", errors.New("stored readiness candidate JSON is invalid")
	}
	switch candidate.Kind {
	case PrivateMemoryCandidateCapsule:
		if candidate.Capsule == nil || len(candidate.Capsule.Items) != 1 ||
			candidate.Capsule.Items[0].Kind == "" || candidate.Capsule.Source.ConversationScope == "" {
			return "", "", errors.New("stored readiness capsule metadata is invalid")
		}
		return candidate.Capsule.Items[0].Kind, candidate.Capsule.Source.ConversationScope, nil
	case PrivateMemoryCandidateSemanticDelta:
		if candidate.SemanticDelta == nil || candidate.SemanticDelta.Source.ConversationScope == "" {
			return "", "", errors.New("stored readiness semantic metadata is invalid")
		}
		return PrivateMemoryCandidateSemanticDelta, candidate.SemanticDelta.Source.ConversationScope, nil
	default:
		return "", "", errors.New("stored readiness candidate kind is invalid")
	}
}

func (s *Store) CommitMemoryTrayCandidate(candidateID string, expectedVersion int, now time.Time) (MemoryWriteReceipt, error) {
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.commitMemoryTrayCandidateForAuthority(
		candidateID, expectedVersion, now,
		expectedBinding, expectedPolicy, expectedResolver,
		s.currentPersonalMemoryScope(expectedBinding),
	)
}

// CommitMemoryTrayCandidateForVerifiedBinding materializes a candidate inside
// the namespace derived from the request's signed repository identity.
func (s *Store) CommitMemoryTrayCandidateForVerifiedBinding(
	candidateID string,
	expectedVersion int,
	now time.Time,
	bindingDigest, repositoryID string,
	resolverEpoch int64,
) (MemoryWriteReceipt, error) {
	if !trayBindingDigestPattern.MatchString(bindingDigest) || !validTrayIdentifier(repositoryID) || resolverEpoch < 1 {
		return MemoryWriteReceipt{}, ErrProductRuntimeMismatch
	}
	return s.commitMemoryTrayCandidateForAuthority(
		candidateID, expectedVersion, now,
		bindingDigest, 0, resolverEpoch,
		personalMemoryScopeForRepository(repositoryID),
	)
}

func (s *Store) commitMemoryTrayCandidateForAuthority(
	candidateID string,
	expectedVersion int,
	now time.Time,
	expectedBinding string,
	expectedPolicy, expectedResolver int64,
	scope personalMemoryScope,
) (MemoryWriteReceipt, error) {
	if _, err := s.trayDestination(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	defer tx.Rollback()
	row, err := loadTrayCandidateTx(tx, candidateID)
	if err != nil {
		return MemoryWriteReceipt{}, fmt.Errorf("load tray candidate %q: %w", candidateID, err)
	}
	if row.version != expectedVersion {
		return MemoryWriteReceipt{}, ErrMemoryTrayVersionConflict
	}
	if row.state == "committed" {
		return loadCandidateCommitReceiptTx(tx, row)
	}
	if row.state != "pending" {
		return MemoryWriteReceipt{}, ErrMemoryTrayTerminal
	}
	var candidate PrivateMemoryCandidate
	if err := json.Unmarshal([]byte(row.payload), &candidate); err != nil {
		return MemoryWriteReceipt{}, fmt.Errorf("stored candidate JSON is invalid: %w", err)
	}
	revalidated, err := prepareBoundPrivateCandidate(candidate, row.host, row.sessionID)
	if err != nil {
		return MemoryWriteReceipt{}, fmt.Errorf("stored candidate is no longer safe: %w", err)
	}
	if revalidated.kind != row.kind || revalidated.digest != row.digest || string(revalidated.payload) != row.payload {
		return MemoryWriteReceipt{}, errors.New("stored candidate does not match previewed canonical payload")
	}
	if row.bindingDigest != expectedBinding || row.policyEpoch != expectedPolicy || row.resolverEpoch != expectedResolver {
		return MemoryWriteReceipt{}, ErrProductRuntimeMismatch
	}
	if err := backfillPersonalScopeForBindingTx(tx, row.bindingDigest, scope); err != nil {
		return MemoryWriteReceipt{}, err
	}
	changed, err := tx.Exec(`
		UPDATE memory_tray_candidates SET state='committing', updated_at=?
		 WHERE candidate_id=? AND version=? AND state='pending'`,
		now.UTC().Format(time.RFC3339Nano), candidateID, expectedVersion)
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if affected, _ := changed.RowsAffected(); affected != 1 {
		return MemoryWriteReceipt{}, ErrMemoryTrayVersionConflict
	}

	status := MemoryWriteCreated
	objectID := ""
	mutationAt := now.UTC().Format(time.RFC3339Nano)
	if row.operation == "correct" {
		status = MemoryWriteUpdated
		objectID = row.targetObjectID
		var targetKind, lifecycle string
		var existingDigest string
		var targetNamespace, targetScope string
		var targetGeneration int
		if err := tx.QueryRow(`
			SELECT candidate_kind, lifecycle, content_digest, project_namespace_id,
			       memory_scope, logical_generation
			  FROM private_memory_objects WHERE object_id=?`,
			objectID,
		).Scan(
			&targetKind, &lifecycle, &existingDigest, &targetNamespace,
			&targetScope, &targetGeneration,
		); err != nil {
			return MemoryWriteReceipt{}, err
		}
		if lifecycle != "active" || targetKind != row.kind {
			return MemoryWriteReceipt{}, errors.New("correction target is inactive or has a different kind")
		}
		if existingDigest != row.targetContentDigest {
			return MemoryWriteReceipt{}, ErrMemoryCorrectionConflict
		}
		if row.targetLogicalGeneration > 0 &&
			targetGeneration != row.targetLogicalGeneration {
			return MemoryWriteReceipt{}, ErrMemoryScopeConflict
		}
		var duplicateID string
		err := tx.QueryRow(`
			SELECT object_id FROM private_memory_objects
			 WHERE project_namespace_id=? AND memory_scope=?
			   AND candidate_kind=? AND content_digest=?
			   AND lifecycle='active' AND object_id!=?`,
			targetNamespace, targetScope, row.kind, row.digest, objectID,
		).Scan(&duplicateID)
		if err == nil {
			return MemoryWriteReceipt{}, errors.New("correction duplicates another active memory object")
		}
		if err != sql.ErrNoRows {
			return MemoryWriteReceipt{}, err
		}
		switch row.kind {
		case PrivateMemoryCandidateCapsule:
			if err := replacePrivateCapsuleTx(tx, objectID, *candidate.Capsule); err != nil {
				return MemoryWriteReceipt{}, err
			}
		case PrivateMemoryCandidateSemanticDelta:
			// The active-object pointer changes before the deterministic rebuild,
			// so the old contribution disappears and the replacement is projected
			// with the same stable canonical object ID.
		default:
			return MemoryWriteReceipt{}, errors.New("stored correction kind is invalid")
		}
		updateResult, err := tx.Exec(`
			UPDATE private_memory_objects
			 SET content_digest=?, created_from_candidate_id=?,
			       logical_generation=logical_generation+1, modified_at=?
			 WHERE object_id=? AND lifecycle='active'
			   AND (?=0 OR logical_generation=?)`,
			row.digest, candidateID, mutationAt, objectID,
			row.targetLogicalGeneration, row.targetLogicalGeneration,
		)
		if err != nil {
			return MemoryWriteReceipt{}, err
		}
		if affected, _ := updateResult.RowsAffected(); affected != 1 {
			return MemoryWriteReceipt{}, ErrMemoryScopeConflict
		}
		if row.kind == PrivateMemoryCandidateSemanticDelta {
			if err := rebuildPrivateSemanticProjectionTx(tx); err != nil {
				return MemoryWriteReceipt{}, fmt.Errorf("rebuild corrected semantic projection: %w", err)
			}
		}
	} else {
		err = tx.QueryRow(`
			SELECT object_id FROM private_memory_objects
			 WHERE project_namespace_id=? AND memory_scope=?
			   AND candidate_kind=? AND content_digest=? AND lifecycle='active'`,
			scope.ProjectNamespaceID, scope.Scope, row.kind, row.digest,
		).Scan(&objectID)
		if err == nil {
			status = MemoryWriteDeduplicated
		} else if err != sql.ErrNoRows {
			return MemoryWriteReceipt{}, err
		} else {
			switch row.kind {
			case PrivateMemoryCandidateCapsule:
				ids, err := rememberPrivateCapsuleTx(tx, *candidate.Capsule)
				if err != nil {
					return MemoryWriteReceipt{}, err
				}
				if len(ids) != 1 {
					return MemoryWriteReceipt{}, errors.New("tray capsule commit did not create one object")
				}
				objectID = ids[0]
			case PrivateMemoryCandidateSemanticDelta:
				objectID, err = newOpaqueID("semantic")
				if err != nil {
					return MemoryWriteReceipt{}, err
				}
			default:
				return MemoryWriteReceipt{}, errors.New("stored candidate kind is invalid")
			}
			createdAt := mutationAt
			if _, err := tx.Exec(`
				INSERT INTO private_memory_objects(
					object_id, candidate_kind, content_digest, created_from_candidate_id, created_at,
					logical_memory_id, logical_generation, project_namespace_id,
					original_repository_id, memory_scope, modified_at,
					capture_host, capture_session_ref, captured_at
				) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)`,
				objectID, row.kind, row.digest, candidateID, createdAt,
				objectID, scope.ProjectNamespaceID, scope.OriginalRepository, scope.Scope, createdAt,
				row.host, row.sessionID, createdAt,
			); err != nil {
				return MemoryWriteReceipt{}, err
			}
			if row.kind == PrivateMemoryCandidateSemanticDelta {
				if err := rebuildPrivateSemanticProjectionTx(tx); err != nil {
					return MemoryWriteReceipt{}, fmt.Errorf("rebuild created semantic projection: %w", err)
				}
			}
		}
	}

	createdAt := mutationAt
	measurementMethod := "host_structured_v1"
	reasonCode := ""
	if row.operation == "correct" {
		measurementMethod = "human_control_v1"
		reasonCode = "user_corrected"
	}
	receipt, err := insertWriteReceiptTx(tx, MemoryWriteReceipt{
		LedgerID: row.ledgerID, CandidateID: candidateID, CandidateVersion: row.version,
		Status: status, Destination: capture.Destination(row.destination), ContentDigest: row.digest, ObjectID: objectID,
		ReasonCode:  reasonCode,
		PolicyEpoch: row.policyEpoch, ResolverEpoch: row.resolverEpoch,
		MeasurementMethod: measurementMethod, CreatedAt: createdAt,
	})
	if err != nil {
		return MemoryWriteReceipt{}, fmt.Errorf("insert committed memory receipt: %w", err)
	}
	if err := insertWriteAuditTx(tx, receipt, row.operation, status); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO memory_write_idempotency(
			operation, idempotency_key, request_digest, receipt_id, object_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
		row.operation, fmt.Sprintf("%s:%d", candidateID, row.version), row.digest, receipt.ReceiptID, objectID, createdAt,
	); err != nil {
		return MemoryWriteReceipt{}, err
	}
	projectionID, err := newOpaqueID("projection")
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO private_projection_outbox(
			projection_id, object_id, candidate_kind, status, attempt_count, created_at, updated_at
		) VALUES (?, ?, ?, 'pending', 0, ?, ?)
		ON CONFLICT(object_id, candidate_kind) DO UPDATE SET
		  status='pending', attempt_count=0, updated_at=excluded.updated_at`,
		projectionID, objectID, row.kind, createdAt, createdAt); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if _, err := tx.Exec(`
		UPDATE memory_tray_candidates
		   SET state='committed', canonical_object_id=?, updated_at=?, terminal_at=?
		 WHERE candidate_id=? AND version=? AND state='committing'`,
		objectID, createdAt, createdAt, candidateID, row.version); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := advancePersonalEligibilityTx(tx, now); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	return receipt, nil
}

func replacePrivateCapsuleTx(tx *sql.Tx, objectID string, capsule MemoryCapsule) error {
	var eventID sql.NullInt64
	if err := tx.QueryRow(`SELECT event_id FROM memory_capsules WHERE id=?`, objectID).Scan(&eventID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM memory_capsules WHERE id=?`, objectID); err != nil {
		return err
	}
	if eventID.Valid {
		if _, err := tx.Exec(`DELETE FROM events WHERE id=?`, eventID.Int64); err != nil {
			return err
		}
	}
	ids, err := rememberPrivateCapsuleTx(tx, capsule)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return errors.New("capsule correction did not create one replacement")
	}
	_, err = tx.Exec(`UPDATE memory_capsules SET id=? WHERE id=?`, objectID, ids[0])
	return err
}

// projectPrivateSemanticObjectTx materializes one active private object's
// contribution into the product-owned graph. Product objects share canonical
// nodes, relations, facts, threads, and sessions, while every contributing
// object records lineage to the shared row. Legacy pre-Tray rows are never
// selected or updated.
func projectPrivateSemanticObjectTx(tx *sql.Tx, objectID string, delta SemanticDelta) error {
	if err := validateSemanticDelta(delta); err != nil {
		return err
	}
	var objectScope, projectNamespace string
	if err := tx.QueryRow(`
		SELECT memory_scope, project_namespace_id
		  FROM private_memory_objects
		 WHERE object_id=? AND lifecycle='active'`, objectID,
	).Scan(&objectScope, &projectNamespace); err != nil {
		return err
	}
	now := delta.Source.Timestamp
	entityIDs := make(map[string]int64, len(delta.Nodes))
	for _, node := range delta.Nodes {
		rowID, err := upsertPrivateSemanticNodeTx(
			tx, objectScope, projectNamespace, node, now,
		)
		if err != nil {
			return fmt.Errorf("project private node %q: %w", node.ClientID, err)
		}
		entityIDs[node.ClientID] = rowID
		if err := insertPrivateSemanticProjectionRefTx(tx, objectID, "entity", strconv.FormatInt(rowID, 10)); err != nil {
			return fmt.Errorf("record private node %q lineage: %w", node.ClientID, err)
		}
	}
	for _, edge := range delta.Edges {
		fromID, fromOK := entityIDs[edge.From]
		toID, toOK := entityIDs[edge.To]
		if !fromOK || !toOK {
			return errors.New("semantic edge references an unknown private node")
		}
		if _, err := tx.Exec(`
			INSERT INTO relations(from_entity_id, to_entity_id, kind, strength, first_seen, last_seen, context)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(from_entity_id, to_entity_id, kind) DO UPDATE SET
			  strength=MAX(relations.strength, excluded.strength),
			  last_seen=MAX(relations.last_seen, excluded.last_seen),
			  context=COALESCE(NULLIF(excluded.context, ''), relations.context)`,
			fromID, toID, edge.Kind, edge.Strength, now, now, nullableString(edge.Summary)); err != nil {
			return fmt.Errorf("select private relation %q: %w", edge.Kind, err)
		}
		var rowID int64
		if err := tx.QueryRow(`
			SELECT id FROM relations WHERE from_entity_id=? AND to_entity_id=? AND kind=?`,
			fromID, toID, edge.Kind).Scan(&rowID); err != nil {
			return fmt.Errorf("select private relation %q: %w", edge.Kind, err)
		}
		if err := insertPrivateSemanticProjectionRefTx(tx, objectID, "relation", strconv.FormatInt(rowID, 10)); err != nil {
			return fmt.Errorf("record private relation %q lineage: %w", edge.Kind, err)
		}
	}
	for _, fact := range delta.Facts {
		entityID, ok := entityIDs[fact.Node]
		if !ok {
			return errors.New("semantic fact references an unknown private node")
		}
		if _, err := tx.Exec(`
			INSERT INTO facts(
			  entity_id, text, confidence, scorer_version, created_at, verified,
			  extractor_version, belief_class, confidence_floor, provenance, domain
			) VALUES (?, ?, ?, 'host-extracted', ?, 0, 'pulse.private_semantic.v1',
			          'operational', 0, 'interactive_memory', ?)
			ON CONFLICT(entity_id, text) DO UPDATE SET
			  confidence=MAX(facts.confidence, excluded.confidence),
			  scorer_version=excluded.scorer_version,
			  domain=excluded.domain`,
			entityID, fact.Text, fact.Confidence, now, normalizeDomain(fact.Domain)); err != nil {
			return err
		}
		var rowID int64
		if err := tx.QueryRow(`SELECT id FROM facts WHERE entity_id=? AND text=?`, entityID, fact.Text).Scan(&rowID); err != nil {
			return fmt.Errorf("select private fact for %q: %w", fact.Node, err)
		}
		if err := insertPrivateSemanticProjectionRefTx(tx, objectID, "fact", strconv.FormatInt(rowID, 10)); err != nil {
			return fmt.Errorf("record private fact for %q lineage: %w", fact.Node, err)
		}
	}
	for _, event := range delta.Events {
		rowID, err := insertSemanticEvent(tx, entityIDs, event, now)
		if err != nil {
			return fmt.Errorf("insert private event %q: %w", event.ClientID, err)
		}
		if err := insertPrivateSemanticProjectionRefTx(tx, objectID, "event", strconv.FormatInt(rowID, 10)); err != nil {
			return fmt.Errorf("record private event %q lineage: %w", event.ClientID, err)
		}
	}
	if delta.Continuity != nil {
		threadID := "private:" + normalizeThreadID(delta.Source.ThreadID, delta.Source.ProjectID, delta.Source.SessionID)
		sessionID := strings.TrimSpace(delta.Source.SessionID)
		if sessionID == "" {
			sessionID = threadID + ":semantic-delta"
		} else {
			sessionID = "private:" + sessionID
		}
		if err := saveSemanticContinuityWithRefsTx(
			tx, delta, threadID, sessionID, now, []string{"pulse:private-memory:" + objectID},
		); err != nil {
			return err
		}
		var rowID int64
		if err := tx.QueryRow(`SELECT last_insert_rowid()`).Scan(&rowID); err != nil {
			return err
		}
		if err := insertPrivateSemanticProjectionRefTx(tx, objectID, "thread", threadID); err != nil {
			return err
		}
		if err := insertPrivateSemanticProjectionRefTx(tx, objectID, "session", sessionID); err != nil {
			return err
		}
		if err := insertPrivateSemanticProjectionRefTx(tx, objectID, "checkpoint", strconv.FormatInt(rowID, 10)); err != nil {
			return err
		}
	}
	return nil
}

func upsertPrivateSemanticNodeTx(
	tx *sql.Tx,
	objectScope, projectNamespace string,
	node SemanticNode,
	now string,
) (int64, error) {
	nodeKeys := semanticEntityKeys(node.Kind, node.CanonicalName, node.Aliases)
	rows, err := tx.Query(`
		SELECT DISTINCT entity.id, entity.canonical_name, COALESCE(entity.aliases, '[]')
		  FROM entities entity
		  JOIN private_semantic_projection_rows projection
		    ON projection.row_kind='entity' AND projection.row_ref=CAST(entity.id AS TEXT)
		  JOIN private_memory_objects object ON object.object_id=projection.object_id
		 WHERE entity.kind=?
		   AND object.lifecycle='active'
		   AND object.memory_scope=?
		   AND (
		       ?='personal_global' OR
		       object.project_namespace_id=?
		   )
		 ORDER BY entity.salience_score DESC, entity.last_seen DESC, entity.id ASC`,
		node.Kind, objectScope, objectScope, projectNamespace)
	if err != nil {
		return 0, err
	}
	var rowID int64
	var canonicalName, aliasesJSON string
	for rows.Next() {
		var candidateID int64
		var candidateName, candidateAliases string
		if err := rows.Scan(&candidateID, &candidateName, &candidateAliases); err != nil {
			rows.Close()
			return 0, err
		}
		if semanticKeysOverlap(nodeKeys, semanticEntityKeys(node.Kind, candidateName, parseSemanticAliases(candidateAliases))) {
			rowID, canonicalName, aliasesJSON = candidateID, candidateName, candidateAliases
			break
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if rowID == 0 {
		aliases, _ := json.Marshal(cleanSemanticAliases(node.CanonicalName, node.Aliases))
		result, err := tx.Exec(`
			INSERT INTO entities(
			  canonical_name, kind, aliases, first_seen, last_seen, salience_score,
			  emotional_weight, scorer_version, description_md, extractor_version
			) VALUES (?, ?, ?, ?, ?, ?, ?, 'host-extracted', ?, 'pulse.private_semantic.v1')`,
			node.CanonicalName, node.Kind, string(aliases), now, now, node.Salience,
			node.EmotionalWeight, node.Summary)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	}
	mergedAliases := mergeSemanticAliases(canonicalName, parseSemanticAliases(aliasesJSON), append(node.Aliases, node.CanonicalName))
	aliases, _ := json.Marshal(mergedAliases)
	_, err = tx.Exec(`
		UPDATE entities SET aliases=?, last_seen=MAX(last_seen, ?),
		       salience_score=MAX(salience_score, ?),
		       emotional_weight=MAX(emotional_weight, ?),
		       description_md=COALESCE(NULLIF(?, ''), description_md),
		       scorer_version='host-extracted'
		 WHERE id=?`, string(aliases), now, node.Salience, node.EmotionalWeight, node.Summary, rowID)
	return rowID, err
}

func insertPrivateSemanticProjectionRefTx(tx *sql.Tx, objectID, kind, rowRef string) error {
	_, err := tx.Exec(`
		INSERT OR IGNORE INTO private_semantic_projection_rows(object_id, row_kind, row_ref)
		VALUES (?, ?, ?)`, objectID, kind, rowRef)
	return err
}

func rebuildPrivateSemanticProjectionTx(tx *sql.Tx) error {
	rows, err := tx.Query(`
		SELECT object.object_id, candidate.payload_json
		  FROM private_memory_objects object
		  JOIN memory_tray_candidates candidate
		    ON candidate.candidate_id=object.created_from_candidate_id
		 WHERE object.lifecycle='active' AND object.candidate_kind='semantic_delta'
		 ORDER BY object.created_at, object.object_id`)
	if err != nil {
		return err
	}
	type activeProjection struct {
		objectID string
		delta    SemanticDelta
	}
	var active []activeProjection
	for rows.Next() {
		var item activeProjection
		var payload string
		if err := rows.Scan(&item.objectID, &payload); err != nil {
			rows.Close()
			return err
		}
		var candidate PrivateMemoryCandidate
		if err := json.Unmarshal([]byte(payload), &candidate); err != nil || candidate.SemanticDelta == nil {
			rows.Close()
			return errors.New("active semantic projection payload is invalid")
		}
		item.delta = *candidate.SemanticDelta
		active = append(active, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := clearPrivateSemanticProjectionTx(tx); err != nil {
		return err
	}
	for _, item := range active {
		if err := projectPrivateSemanticObjectTx(tx, item.objectID, item.delta); err != nil {
			return fmt.Errorf("rebuild private semantic object %s: %w", item.objectID, err)
		}
	}
	_, err = tx.Exec(`
		UPDATE private_projection_outbox
		   SET status='pending', attempt_count=0, updated_at=?
		 WHERE object_id IN (
		   SELECT object_id FROM private_memory_objects
		    WHERE lifecycle='active' AND candidate_kind='semantic_delta'
		 )`, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func clearPrivateSemanticProjectionTx(tx *sql.Tx) error {
	deleteNumeric := func(table, kind string) error {
		_, err := tx.Exec(`DELETE FROM `+table+` WHERE id IN (
			SELECT CAST(row_ref AS INTEGER) FROM private_semantic_projection_rows WHERE row_kind=?
		)`, kind)
		return err
	}
	for _, item := range []struct{ table, kind string }{
		{"continuity_checkpoints", "checkpoint"},
		{"events", "event"},
		{"facts", "fact"},
		{"relations", "relation"},
	} {
		if err := deleteNumeric(item.table, item.kind); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM continuity_sessions WHERE session_id IN (
		SELECT row_ref FROM private_semantic_projection_rows WHERE row_kind='session'
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM continuity_threads WHERE thread_id IN (
		SELECT row_ref FROM private_semantic_projection_rows WHERE row_kind='thread'
	)`); err != nil {
		return err
	}
	if err := deleteNumeric("entities", "entity"); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM private_semantic_projection_rows`)
	return err
}

func deletePrivateSemanticProjectionTx(tx *sql.Tx, _ string) error {
	return rebuildPrivateSemanticProjectionTx(tx)
}

func (s *Store) EditMemoryTrayCandidate(candidateID string, expectedVersion int, replacement PrivateMemoryCandidate, now time.Time, grace time.Duration) (MemoryWriteReceipt, error) {
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.editMemoryTrayCandidateForAuthority(
		candidateID, expectedVersion, replacement, now, grace,
		expectedBinding, expectedPolicy, expectedResolver,
	)
}

func (s *Store) EditMemoryTrayCandidateForVerifiedBinding(
	candidateID string,
	expectedVersion int,
	replacement PrivateMemoryCandidate,
	now time.Time,
	grace time.Duration,
	bindingDigest, repositoryID string,
	resolverEpoch int64,
) (MemoryWriteReceipt, error) {
	if !trayBindingDigestPattern.MatchString(bindingDigest) ||
		!validTrayIdentifier(repositoryID) || resolverEpoch < 1 {
		return MemoryWriteReceipt{}, ErrProductRuntimeMismatch
	}
	return s.editMemoryTrayCandidateForAuthority(
		candidateID, expectedVersion, replacement, now, grace,
		bindingDigest, 0, resolverEpoch,
	)
}

func (s *Store) editMemoryTrayCandidateForAuthority(
	candidateID string,
	expectedVersion int,
	replacement PrivateMemoryCandidate,
	now time.Time,
	grace time.Duration,
	expectedBinding string,
	expectedPolicy, expectedResolver int64,
) (MemoryWriteReceipt, error) {
	if _, err := s.trayDestination(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := validateTrayGrace(grace); err != nil {
		return MemoryWriteReceipt{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	defer tx.Rollback()
	row, err := loadTrayCandidateTx(tx, candidateID)
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if row.bindingDigest != expectedBinding || row.policyEpoch != expectedPolicy || row.resolverEpoch != expectedResolver {
		return MemoryWriteReceipt{}, ErrProductRuntimeMismatch
	}
	prepared, err := prepareBoundPrivateCandidate(replacement, row.host, row.sessionID)
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if row.version == expectedVersion+1 && row.state == "pending" && prepared.kind == row.kind &&
		prepared.digest == row.digest && string(prepared.payload) == row.payload {
		return loadLatestCandidateReceiptTx(tx, candidateID)
	}
	if row.version != expectedVersion {
		return MemoryWriteReceipt{}, ErrMemoryTrayVersionConflict
	}
	if row.state != "pending" {
		return MemoryWriteReceipt{}, ErrMemoryTrayTerminal
	}
	if row.operation == "correct" && prepared.kind != row.kind {
		return MemoryWriteReceipt{}, errors.New("correction edit cannot change memory kind")
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	newVersion := row.version + 1
	result, err := tx.Exec(`
		UPDATE memory_tray_candidates
		   SET candidate_kind=?, version=?, content_digest=?, payload_json=?,
		       grace_expires_at=?, updated_at=?
		 WHERE candidate_id=? AND version=? AND state='pending'`,
		prepared.kind, newVersion, prepared.digest, string(prepared.payload),
		createdAt, createdAt,
		candidateID, expectedVersion)
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return MemoryWriteReceipt{}, ErrMemoryTrayVersionConflict
	}
	receipt, err := insertWriteReceiptTx(tx, MemoryWriteReceipt{
		LedgerID: row.ledgerID, CandidateID: candidateID, CandidateVersion: newVersion,
		Status: MemoryWritePending, Destination: capture.Destination(row.destination), ContentDigest: prepared.digest,
		PolicyEpoch: row.policyEpoch, ResolverEpoch: row.resolverEpoch,
		MeasurementMethod: "human_edit_v1", CreatedAt: createdAt,
	})
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := insertWriteAuditTx(tx, receipt, "edit", MemoryWritePending); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	return receipt, nil
}

func (s *Store) CancelMemoryTrayCandidate(candidateID string, expectedVersion int, now time.Time) (MemoryWriteReceipt, error) {
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.cancelMemoryTrayCandidateForAuthority(
		candidateID, expectedVersion, now,
		expectedBinding, expectedPolicy, expectedResolver,
	)
}

func (s *Store) CancelMemoryTrayCandidateForVerifiedBinding(
	candidateID string,
	expectedVersion int,
	now time.Time,
	bindingDigest, repositoryID string,
	resolverEpoch int64,
) (MemoryWriteReceipt, error) {
	if !trayBindingDigestPattern.MatchString(bindingDigest) ||
		!validTrayIdentifier(repositoryID) || resolverEpoch < 1 {
		return MemoryWriteReceipt{}, ErrProductRuntimeMismatch
	}
	return s.cancelMemoryTrayCandidateForAuthority(
		candidateID, expectedVersion, now,
		bindingDigest, 0, resolverEpoch,
	)
}

func (s *Store) cancelMemoryTrayCandidateForAuthority(
	candidateID string,
	expectedVersion int,
	now time.Time,
	expectedBinding string,
	expectedPolicy, expectedResolver int64,
) (MemoryWriteReceipt, error) {
	if _, err := s.trayDestination(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	defer tx.Rollback()
	row, err := loadTrayCandidateTx(tx, candidateID)
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if row.bindingDigest != expectedBinding || row.policyEpoch != expectedPolicy || row.resolverEpoch != expectedResolver {
		return MemoryWriteReceipt{}, ErrProductRuntimeMismatch
	}
	if row.version == expectedVersion && row.state == "canceled" {
		return loadLatestCandidateReceiptTx(tx, candidateID)
	}
	if row.version != expectedVersion {
		return MemoryWriteReceipt{}, ErrMemoryTrayVersionConflict
	}
	if row.state != "pending" {
		return MemoryWriteReceipt{}, ErrMemoryTrayTerminal
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.Exec(`
		UPDATE memory_tray_candidates
		   SET state='canceled', payload_json='{}', updated_at=?, terminal_at=?
		 WHERE candidate_id=? AND version=? AND state='pending'`,
		createdAt, createdAt, candidateID, expectedVersion)
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return MemoryWriteReceipt{}, ErrMemoryTrayVersionConflict
	}
	receipt, err := insertWriteReceiptTx(tx, MemoryWriteReceipt{
		LedgerID: row.ledgerID, CandidateID: candidateID, CandidateVersion: row.version,
		Status: MemoryWriteCanceled, Destination: capture.Destination(row.destination), ContentDigest: row.digest,
		ReasonCode: "user_canceled", PolicyEpoch: row.policyEpoch, ResolverEpoch: row.resolverEpoch,
		MeasurementMethod: "human_control_v1", CreatedAt: createdAt,
	})
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := insertWriteAuditTx(tx, receipt, "cancel", MemoryWriteCanceled); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	return receipt, nil
}

// FailMemoryTrayCandidate records a content-free terminal receipt after the
// bounded commit worker has exhausted its retries. The candidate payload is
// erased in the same transaction, so a failed automatic write cannot remain
// as an indefinitely pending private draft.
func (s *Store) FailMemoryTrayCandidate(candidateID string, expectedVersion int, reasonCode string, now time.Time) (MemoryWriteReceipt, error) {
	if _, err := s.trayDestination(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if reasonCode != "commit_failed" {
		return MemoryWriteReceipt{}, errors.New("failure reason is unsupported")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	defer tx.Rollback()
	row, err := loadTrayCandidateTx(tx, candidateID)
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if row.version != expectedVersion {
		return MemoryWriteReceipt{}, ErrMemoryTrayVersionConflict
	}
	if row.state == "failed" {
		return loadLatestCandidateReceiptTx(tx, candidateID)
	}
	if row.state != "pending" {
		return MemoryWriteReceipt{}, ErrMemoryTrayTerminal
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.Exec(`
		UPDATE memory_tray_candidates
		   SET state='failed', payload_json='{}', updated_at=?, terminal_at=?
		 WHERE candidate_id=? AND version=? AND state='pending'`,
		createdAt, createdAt, candidateID, expectedVersion)
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return MemoryWriteReceipt{}, ErrMemoryTrayVersionConflict
	}
	receipt, err := insertWriteReceiptTx(tx, MemoryWriteReceipt{
		LedgerID: row.ledgerID, CandidateID: candidateID, CandidateVersion: row.version,
		Status: MemoryWriteFailed, Destination: capture.Destination(row.destination),
		ReasonCode: reasonCode, PolicyEpoch: row.policyEpoch, ResolverEpoch: row.resolverEpoch,
		MeasurementMethod: "commit_worker_v1", CreatedAt: createdAt,
	})
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := insertWriteAuditTx(tx, receipt, "commit", MemoryWriteFailed); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO memory_write_idempotency(
			operation, idempotency_key, request_digest, receipt_id, created_at
		) VALUES ('commit_failed', ?, ?, ?, ?)`,
		fmt.Sprintf("%s:%d", candidateID, row.version), row.digest, receipt.ReceiptID, createdAt,
	); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	return receipt, nil
}

func manualCandidateKey(value any) (string, error) {
	digest, err := requestDigest(value)
	if err != nil {
		return "", err
	}
	return digest[:24], nil
}

func localStoreBindingDigest(storeID string) string {
	digest := sha256.Sum256([]byte("store:" + storeID))
	return hex.EncodeToString(digest[:])
}

func manualSemanticSessionID(delta SemanticDelta) string {
	value := strings.Join([]string{
		strings.TrimSpace(delta.Source.ThreadID),
		strings.TrimSpace(delta.Source.ProjectID),
		strings.TrimSpace(delta.Source.ConversationScope),
	}, "\x1f")
	digest := sha256.Sum256([]byte("manual-semantic-session\x1f" + value))
	return "manual_session_" + hex.EncodeToString(digest[:12])
}

func normalizeManualInvocation(kind, key string) (string, error) {
	if key == "" {
		return newOpaqueID(kind)
	}
	if !validTrayIdentifier(key) {
		return "", errors.New("idempotency key is invalid")
	}
	digest := sha256.Sum256([]byte("pulse-manual-invocation-v1\x00" + kind + "\x00" + key))
	return kind + "_" + hex.EncodeToString(digest[:16]), nil
}

func (s *Store) PrepareManualMemoryCapsule(capsule MemoryCapsule, now time.Time, grace time.Duration) (TurnFinalizeResult, error) {
	return s.PrepareManualMemoryCapsuleWithInvocation(capsule, "", now, grace)
}

func (s *Store) PrepareManualMemoryCapsuleWithInvocation(capsule MemoryCapsule, invocationKey string, now time.Time, grace time.Duration) (TurnFinalizeResult, error) {
	// Manual ingress is authenticated as the local control surface. The model
	// payload cannot choose receipt provenance by setting source.host.
	capsule.Source.Host = "pulse-cli"
	key, err := manualCandidateKey(capsule)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	invocationID, err := normalizeManualInvocation("manual", invocationKey)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	candidates := make([]PrivateMemoryCandidate, 0, len(capsule.Items))
	for _, item := range capsule.Items {
		itemCapsule := capsule
		itemCapsule.Items = []MemoryCapsuleItem{item}
		candidates = append(candidates, PrivateMemoryCandidate{
			Kind: PrivateMemoryCandidateCapsule, Capsule: &itemCapsule,
		})
	}
	bindingDigest, policyEpoch, resolverEpoch := s.productRuntimeAuthority()
	return s.FinalizeTurn(TurnFinalizeRequest{
		Schema: TurnFinalizeRequestSchema,
		Host:   "pulse-cli", SessionID: "manual_session_" + key,
		TurnID: "manual_turn_" + invocationID, SourceEventKey: "manual:memory:" + invocationID,
		IdempotencyKey: "manual_memory_" + invocationID, BindingDigest: bindingDigest,
		PolicyEpoch: policyEpoch, ResolverEpoch: resolverEpoch, Candidates: candidates,
	}, now, grace)
}

// PrepareUnassignedMemoryCapsuleWithInvocation moves one host-extracted,
// digest-bound Inbox capsule into the current bound Vault's ordinary durable
// write path. The caller is the trusted local Home surface, while the original
// supported harness remains the truthful provenance host.
func (s *Store) PrepareUnassignedMemoryCapsuleWithInvocation(
	capsule MemoryCapsule,
	invocationKey string,
	now time.Time,
	grace time.Duration,
) (TurnFinalizeResult, error) {
	if !validHost(capsule.Source.Host) || capsule.Source.Host == "pulse-cli" {
		return TurnFinalizeResult{}, errors.New("unassigned capsule host is invalid")
	}
	key, err := manualCandidateKey(capsule)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	invocationID, err := normalizeManualInvocation("unassigned", invocationKey)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	candidates := make([]PrivateMemoryCandidate, 0, len(capsule.Items))
	for _, item := range capsule.Items {
		itemCapsule := capsule
		itemCapsule.Items = []MemoryCapsuleItem{item}
		candidates = append(candidates, PrivateMemoryCandidate{
			Kind: PrivateMemoryCandidateCapsule, Capsule: &itemCapsule,
		})
	}
	bindingDigest, policyEpoch, resolverEpoch := s.productRuntimeAuthority()
	return s.FinalizeTurn(TurnFinalizeRequest{
		Schema: TurnFinalizeRequestSchema,
		Host:   capsule.Source.Host, SessionID: "unassigned_session_" + key,
		TurnID: "unassigned_turn_" + invocationID, SourceEventKey: "unassigned:memory:" + invocationID,
		IdempotencyKey: "unassigned_memory_" + invocationID, BindingDigest: bindingDigest,
		PolicyEpoch: policyEpoch, ResolverEpoch: resolverEpoch, Candidates: candidates,
	}, now, grace)
}

func (s *Store) PrepareManualSemanticDelta(delta SemanticDelta, now time.Time, grace time.Duration) (TurnFinalizeResult, error) {
	return s.PrepareManualSemanticDeltaWithInvocation(delta, "", now, grace)
}

func (s *Store) PrepareManualSemanticDeltaWithInvocation(delta SemanticDelta, invocationKey string, now time.Time, grace time.Duration) (TurnFinalizeResult, error) {
	delta.Source.Host = "pulse-cli"
	delta.Source.SessionID = ""
	invocationID, err := normalizeManualInvocation("manual", invocationKey)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	sessionID := manualSemanticSessionID(delta)
	delta.Source.SessionID = sessionID
	bindingDigest, policyEpoch, resolverEpoch := s.productRuntimeAuthority()
	return s.FinalizeTurn(TurnFinalizeRequest{
		Schema: TurnFinalizeRequestSchema,
		Host:   "pulse-cli", SessionID: sessionID,
		TurnID: "manual_turn_" + invocationID, SourceEventKey: "manual:semantic:" + invocationID,
		IdempotencyKey: "manual_semantic_" + invocationID, BindingDigest: bindingDigest,
		PolicyEpoch: policyEpoch, ResolverEpoch: resolverEpoch,
		Candidates: []PrivateMemoryCandidate{{
			Kind: PrivateMemoryCandidateSemanticDelta, SemanticDelta: &delta,
		}},
	}, now, grace)
}

// PrepareMemoryCorrection creates a durable correction candidate for an active
// object. Ordinary Personal corrections are eligible for immediate commit and
// retain the same receipt and compare-and-swap guarantees as automatic writes.
func (s *Store) PrepareMemoryCorrection(
	targetObjectID string,
	replacement PrivateMemoryCandidate,
	now time.Time,
	grace time.Duration,
) (TurnFinalizeResult, error) {
	return s.PrepareMemoryCorrectionWithInvocation(targetObjectID, replacement, "", now, grace)
}

func (s *Store) PrepareMemoryCorrectionWithInvocation(
	targetObjectID string,
	replacement PrivateMemoryCandidate,
	invocationKey string,
	now time.Time,
	grace time.Duration,
) (TurnFinalizeResult, error) {
	bindingDigest, policyEpoch, resolverEpoch := s.productRuntimeAuthority()
	return s.prepareMemoryCorrectionWithInvocation(
		targetObjectID, replacement, invocationKey, "", 0, now, grace,
		bindingDigest, policyEpoch, resolverEpoch,
	)
}

func (s *Store) prepareMemoryCorrectionWithInvocation(
	targetObjectID string,
	replacement PrivateMemoryCandidate,
	invocationKey string,
	expectedTargetContentDigest string,
	expectedTargetGeneration int,
	now time.Time,
	grace time.Duration,
	bindingDigest string,
	policyEpoch, resolverEpoch int64,
) (TurnFinalizeResult, error) {
	if !validTrayIdentifier(targetObjectID) {
		return TurnFinalizeResult{}, errors.New("correction target is invalid")
	}
	if expectedTargetContentDigest != "" && !trayBindingDigestPattern.MatchString(expectedTargetContentDigest) {
		return TurnFinalizeResult{}, errors.New("correction target digest is invalid")
	}
	switch replacement.Kind {
	case PrivateMemoryCandidateCapsule:
		if replacement.Capsule != nil {
			capsule := *replacement.Capsule
			capsule.Source.Host = "pulse-cli"
			replacement.Capsule = &capsule
		}
	case PrivateMemoryCandidateSemanticDelta:
		if replacement.SemanticDelta != nil {
			delta := *replacement.SemanticDelta
			delta.Source.Host = "pulse-cli"
			delta.Source.SessionID = ""
			replacement.SemanticDelta = &delta
		}
	}
	key, err := manualCandidateKey(struct {
		Target      string                 `json:"target"`
		Replacement PrivateMemoryCandidate `json:"replacement"`
	}{targetObjectID, replacement})
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	invocationID, err := normalizeManualInvocation("control", invocationKey)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	sessionID := "control_session_" + key
	if replacement.SemanticDelta != nil {
		delta := *replacement.SemanticDelta
		sessionID = manualSemanticSessionID(delta)
		delta.Source.SessionID = sessionID
		replacement.SemanticDelta = &delta
	}
	return s.finalizeTurnForAuthority(TurnFinalizeRequest{
		Schema: TurnFinalizeRequestSchema,
		Host:   "pulse-cli", SessionID: sessionID,
		TurnID: "correct_turn_" + invocationID, SourceEventKey: "control:correct:" + invocationID,
		IdempotencyKey: "correct_" + invocationID, BindingDigest: bindingDigest,
		PolicyEpoch: policyEpoch, ResolverEpoch: resolverEpoch,
		Candidates: []PrivateMemoryCandidate{replacement},
		operation:  "correct", targetObjectID: targetObjectID,
		expectedTargetContentDigest: expectedTargetContentDigest,
		expectedTargetGeneration:    expectedTargetGeneration,
	}, now, grace, bindingDigest, policyEpoch, resolverEpoch)
}

// PrepareMemorySummaryCorrectionWithInvocation is the simple Memory Home edit
// path. It preserves the structured candidate and changes only the human-
// visible summary selected by Home. The target digest is captured before the
// correction is prepared so a concurrent edit cannot be overwritten.
func (s *Store) PrepareMemorySummaryCorrectionWithInvocation(
	targetObjectID string,
	summary string,
	invocationKey string,
	now time.Time,
	grace time.Duration,
) (TurnFinalizeResult, error) {
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.prepareMemorySummaryCorrectionWithInvocation(
		targetObjectID, summary, invocationKey, 0, now, grace,
		expectedBinding, expectedPolicy, expectedResolver,
		s.currentPersonalMemoryScope(expectedBinding),
	)
}

// PrepareMemorySummaryCorrectionAtGenerationWithInvocation gives Memory Home
// a generation-fenced edit path. Scope moves, deletes, and earlier edits all
// advance the generation, so a stale browser tab cannot overwrite any of them.
func (s *Store) PrepareMemorySummaryCorrectionAtGenerationWithInvocation(
	targetObjectID string,
	summary string,
	invocationKey string,
	expectedGeneration int,
	now time.Time,
	grace time.Duration,
) (TurnFinalizeResult, error) {
	if expectedGeneration < 1 {
		return TurnFinalizeResult{}, errors.New("correction target generation is invalid")
	}
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.prepareMemorySummaryCorrectionWithInvocation(
		targetObjectID, summary, invocationKey, expectedGeneration, now, grace,
		expectedBinding, expectedPolicy, expectedResolver,
		s.currentPersonalMemoryScope(expectedBinding),
	)
}

func (s *Store) PrepareMemorySummaryCorrectionAtGenerationWithInvocationForVerifiedBinding(
	targetObjectID string,
	summary string,
	invocationKey string,
	expectedGeneration int,
	now time.Time,
	grace time.Duration,
	bindingDigest, repositoryID string,
	resolverEpoch int64,
) (TurnFinalizeResult, error) {
	if expectedGeneration < 1 || !trayBindingDigestPattern.MatchString(bindingDigest) ||
		!validTrayIdentifier(repositoryID) || resolverEpoch < 1 {
		return TurnFinalizeResult{}, ErrProductRuntimeMismatch
	}
	return s.prepareMemorySummaryCorrectionWithInvocation(
		targetObjectID, summary, invocationKey, expectedGeneration, now, grace,
		bindingDigest, 0, resolverEpoch, personalMemoryScopeForRepository(repositoryID),
	)
}

func (s *Store) prepareMemorySummaryCorrectionWithInvocation(
	targetObjectID string,
	summary string,
	invocationKey string,
	expectedGeneration int,
	now time.Time,
	grace time.Duration,
	expectedBinding string,
	expectedPolicy, expectedResolver int64,
	authority personalMemoryScope,
) (TurnFinalizeResult, error) {
	if !validTrayIdentifier(targetObjectID) {
		return TurnFinalizeResult{}, errors.New("correction target is invalid")
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return TurnFinalizeResult{}, errors.New("memory summary is required")
	}
	var payload, targetContentDigest, lifecycle, bindingDigest string
	var memoryScope, projectNamespace, originalRepository string
	var targetGeneration int
	var policyEpoch, resolverEpoch int64
	err := s.db.QueryRow(`
		SELECT candidate.payload_json, object.content_digest, object.lifecycle,
		       object.logical_generation, object.memory_scope,
		       object.project_namespace_id, object.original_repository_id,
		       ledger.binding_digest,
		       ledger.policy_epoch, ledger.resolver_epoch
		  FROM private_memory_objects object
		  JOIN memory_tray_candidates candidate
		    ON candidate.candidate_id=object.created_from_candidate_id
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 WHERE object.object_id=?`, targetObjectID,
	).Scan(
		&payload, &targetContentDigest, &lifecycle, &targetGeneration,
		&memoryScope, &projectNamespace, &originalRepository,
		&bindingDigest, &policyEpoch, &resolverEpoch,
	)
	if err != nil {
		return TurnFinalizeResult{}, err
	}
	if bindingDigest != expectedBinding || policyEpoch != expectedPolicy || resolverEpoch != expectedResolver {
		return TurnFinalizeResult{}, ErrProductRuntimeMismatch
	}
	if originalRepository != authority.OriginalRepository ||
		projectNamespace != authority.ProjectNamespaceID ||
		(memoryScope != MemoryScopeProject && memoryScope != MemoryScopePersonalGlobal) {
		return TurnFinalizeResult{}, ErrProductRuntimeMismatch
	}
	if lifecycle != "active" {
		return TurnFinalizeResult{}, errors.New("correction target is inactive")
	}
	if expectedGeneration > 0 && targetGeneration != expectedGeneration {
		return TurnFinalizeResult{}, ErrMemoryScopeConflict
	}
	var replacement PrivateMemoryCandidate
	if err := json.Unmarshal([]byte(payload), &replacement); err != nil {
		return TurnFinalizeResult{}, errors.New("stored correction target is invalid")
	}
	if err := replacePrivateMemorySummary(&replacement, summary); err != nil {
		return TurnFinalizeResult{}, err
	}
	return s.prepareMemoryCorrectionWithInvocation(
		targetObjectID, replacement, invocationKey, targetContentDigest,
		expectedGeneration, now, grace,
		expectedBinding, expectedPolicy, expectedResolver,
	)
}

func replacePrivateMemorySummary(candidate *PrivateMemoryCandidate, summary string) error {
	if candidate == nil {
		return errors.New("stored correction target is invalid")
	}
	switch candidate.Kind {
	case PrivateMemoryCandidateCapsule:
		if candidate.Capsule == nil || len(candidate.Capsule.Items) != 1 {
			return errors.New("stored correction capsule is invalid")
		}
		candidate.Capsule.Items[0].RedactedSummary = summary
		return nil
	case PrivateMemoryCandidateSemanticDelta:
		if candidate.SemanticDelta == nil {
			return errors.New("stored semantic correction is invalid")
		}
		delta := candidate.SemanticDelta
		if delta.Continuity != nil {
			delta.Continuity.Summary = summary
			return nil
		}
		if len(delta.Events) > 0 {
			delta.Events[0].Summary = summary
			return nil
		}
		if len(delta.Facts) > 0 {
			delta.Facts[0].Text = summary
			return nil
		}
		if len(delta.Nodes) > 0 {
			delta.Nodes[0].Summary = summary
			return nil
		}
		return errors.New("stored semantic correction has no editable summary")
	default:
		return errors.New("stored correction kind is invalid")
	}
}

func (s *Store) ListMemoryTray(limit int) ([]MemoryTrayCandidateView, error) {
	if _, err := s.trayDestination(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT candidate.candidate_id, candidate.ledger_id, candidate.version,
		       candidate.state, candidate.operation, COALESCE(candidate.target_object_id, ''),
		       COALESCE(candidate.canonical_object_id, ''),
		       EXISTS(SELECT 1 FROM private_memory_objects object
		               WHERE object.created_from_candidate_id=candidate.candidate_id
		                 AND object.lifecycle='active'),
		       COALESCE((SELECT status FROM private_projection_outbox projection
		                  WHERE projection.object_id=candidate.canonical_object_id), 'waiting_for_commit'),
		       ledger.destination_class, candidate.content_digest,
		       candidate.grace_expires_at, candidate.payload_json
		  FROM memory_tray_candidates candidate
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 ORDER BY candidate.updated_at DESC, candidate.candidate_id
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanMemoryTrayViews(s.db, rows)
}

// MemoryTrayPendingCandidate is the bounded candidate shape needed by a
// trusted review surface. It deliberately omits receipt history and canonical
// object projections so rendering or acknowledging one card stays O(1).
type MemoryTrayPendingCandidate struct {
	CandidateID   string                 `json:"candidate_id"`
	Version       int                    `json:"version"`
	ContentDigest string                 `json:"content_digest"`
	Candidate     PrivateMemoryCandidate `json:"candidate"`
}

func (s *Store) ListPendingMemoryTrayCandidates(limit int) ([]MemoryTrayPendingCandidate, error) {
	if _, err := s.trayDestination(); err != nil {
		return nil, err
	}
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.listPendingMemoryTrayCandidatesForAuthority(
		limit, expectedBinding, expectedPolicy, expectedResolver,
	)
}

func (s *Store) ListPendingMemoryTrayCandidatesForVerifiedBinding(
	limit int,
	bindingDigest string,
	resolverEpoch int64,
) ([]MemoryTrayPendingCandidate, error) {
	if !trayBindingDigestPattern.MatchString(bindingDigest) || resolverEpoch < 1 {
		return nil, ErrProductRuntimeMismatch
	}
	return s.listPendingMemoryTrayCandidatesForAuthority(limit, bindingDigest, 0, resolverEpoch)
}

func (s *Store) listPendingMemoryTrayCandidatesForAuthority(
	limit int,
	expectedBinding string,
	expectedPolicy, expectedResolver int64,
) ([]MemoryTrayPendingCandidate, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT candidate.candidate_id, candidate.version, candidate.content_digest, candidate.payload_json
		  FROM memory_tray_candidates candidate
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 WHERE candidate.state='pending'
		   AND ledger.binding_digest=? AND ledger.policy_epoch=? AND ledger.resolver_epoch=?
		 ORDER BY candidate.updated_at DESC, candidate.candidate_id
		 LIMIT ?`, expectedBinding, expectedPolicy, expectedResolver, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]MemoryTrayPendingCandidate, 0)
	for rows.Next() {
		candidate, err := scanPendingMemoryTrayCandidate(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (s *Store) GetPendingMemoryTrayCandidate(candidateID string, version int) (MemoryTrayPendingCandidate, error) {
	if _, err := s.trayDestination(); err != nil {
		return MemoryTrayPendingCandidate{}, err
	}
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.getPendingMemoryTrayCandidateForAuthority(
		candidateID, version, expectedBinding, expectedPolicy, expectedResolver,
	)
}

func (s *Store) GetPendingMemoryTrayCandidateForVerifiedBinding(
	candidateID string,
	version int,
	bindingDigest string,
	resolverEpoch int64,
) (MemoryTrayPendingCandidate, error) {
	if !trayBindingDigestPattern.MatchString(bindingDigest) || resolverEpoch < 1 {
		return MemoryTrayPendingCandidate{}, ErrProductRuntimeMismatch
	}
	return s.getPendingMemoryTrayCandidateForAuthority(candidateID, version, bindingDigest, 0, resolverEpoch)
}

func (s *Store) getPendingMemoryTrayCandidateForAuthority(
	candidateID string,
	version int,
	expectedBinding string,
	expectedPolicy, expectedResolver int64,
) (MemoryTrayPendingCandidate, error) {
	if !validTrayIdentifier(candidateID) || version < 1 {
		return MemoryTrayPendingCandidate{}, errors.New("pending Memory Tray candidate identity is invalid")
	}
	return scanPendingMemoryTrayCandidate(s.db.QueryRow(`
		SELECT candidate.candidate_id, candidate.version, candidate.content_digest, candidate.payload_json
		  FROM memory_tray_candidates candidate
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 WHERE candidate.candidate_id=? AND candidate.version=? AND candidate.state='pending'
		   AND ledger.binding_digest=? AND ledger.policy_epoch=? AND ledger.resolver_epoch=?`,
		candidateID, version, expectedBinding, expectedPolicy, expectedResolver))
}

func scanPendingMemoryTrayCandidate(scanner interface{ Scan(...any) error }) (MemoryTrayPendingCandidate, error) {
	var candidate MemoryTrayPendingCandidate
	var payload string
	if err := scanner.Scan(&candidate.CandidateID, &candidate.Version, &candidate.ContentDigest, &payload); err != nil {
		return MemoryTrayPendingCandidate{}, err
	}
	if err := json.Unmarshal([]byte(payload), &candidate.Candidate); err != nil {
		return MemoryTrayPendingCandidate{}, err
	}
	return candidate, nil
}

func (s *Store) ListPendingMemoryTrayPage(afterCandidateID string, limit int) ([]MemoryTrayCandidateView, error) {
	if _, err := s.trayDestination(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT candidate.candidate_id, candidate.ledger_id, candidate.version,
		       candidate.state, candidate.operation, COALESCE(candidate.target_object_id, ''),
		       COALESCE(candidate.canonical_object_id, ''),
		       EXISTS(SELECT 1 FROM private_memory_objects object
		               WHERE object.created_from_candidate_id=candidate.candidate_id
		                 AND object.lifecycle='active'),
		       COALESCE((SELECT status FROM private_projection_outbox projection
		                  WHERE projection.object_id=candidate.canonical_object_id), 'waiting_for_commit'),
		       ledger.destination_class, candidate.content_digest,
		       candidate.grace_expires_at, candidate.payload_json
		  FROM memory_tray_candidates candidate
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 WHERE candidate.state='pending' AND candidate.candidate_id>?
		 ORDER BY candidate.candidate_id
		 LIMIT ?`, afterCandidateID, limit)
	if err != nil {
		return nil, err
	}
	return scanMemoryTrayViews(s.db, rows)
}

func scanMemoryTrayViews(db *sql.DB, rows *sql.Rows) ([]MemoryTrayCandidateView, error) {
	defer rows.Close()
	var views []MemoryTrayCandidateView
	for rows.Next() {
		var view MemoryTrayCandidateView
		var payload string
		var current int
		if err := rows.Scan(
			&view.CandidateID, &view.LedgerID, &view.Version, &view.State,
			&view.Operation, &view.TargetObjectID, &view.CanonicalObjectID, &current,
			&view.ProjectionStatus,
			&view.DestinationClass, &view.ContentDigest, &view.GraceExpiresAt, &payload,
		); err != nil {
			return nil, err
		}
		view.Current = current == 1
		if err := json.Unmarshal([]byte(payload), &view.Candidate); err != nil {
			return nil, err
		}
		historyObjectID := view.CanonicalObjectID
		if historyObjectID == "" {
			historyObjectID = view.TargetObjectID
		}
		history, err := listCandidateReceipts(db, view.CandidateID, historyObjectID, 50)
		if err != nil {
			return nil, err
		}
		if len(history) == 0 {
			return nil, errors.New("Memory Tray candidate has no durable receipt")
		}
		view.ReceiptHistory = history
		view.LatestReceipt = history[len(history)-1]
		views = append(views, view)
	}
	return views, rows.Err()
}

func listCandidateReceipts(db *sql.DB, candidateID, objectID string, limit int) ([]MemoryWriteReceipt, error) {
	rows, err := db.Query(`
		SELECT `+receiptColumns+` FROM (
		  SELECT rowid AS receipt_rowid, `+receiptColumns+` FROM memory_write_receipts
		   WHERE candidate_id=? OR (?!='' AND object_id=?)
		   ORDER BY rowid DESC LIMIT ?
		) ORDER BY receipt_rowid`, candidateID, objectID, objectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var receipts []MemoryWriteReceipt
	for rows.Next() {
		receipt, err := scanWriteReceipt(rows)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

func (s *Store) GetMemoryWriteReceipt(receiptID string) (MemoryWriteReceipt, error) {
	if _, err := s.trayDestination(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if !validTrayIdentifier(receiptID) {
		return MemoryWriteReceipt{}, errors.New("receipt ID is invalid")
	}
	return scanWriteReceipt(s.db.QueryRow(`
		SELECT `+receiptColumns+` FROM memory_write_receipts WHERE receipt_id=?`, receiptID))
}

func (s *Store) SetPrivateProjectionStatus(objectID, status string, now time.Time) error {
	if _, err := s.trayDestination(); err != nil {
		return err
	}
	if status != "complete" && status != "failed" {
		return errors.New("projection status is invalid")
	}
	result, err := s.db.Exec(`
		UPDATE private_projection_outbox SET status=?, updated_at=? WHERE object_id=?`,
		status, now.UTC().Format(time.RFC3339Nano), objectID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("projection object is missing")
	}
	return nil
}

func (s *Store) SetPendingPrivateProjectionStatus(status string, now time.Time) error {
	if _, err := s.trayDestination(); err != nil {
		return err
	}
	if status != "complete" && status != "failed" {
		return errors.New("projection status is invalid")
	}
	_, err := s.db.Exec(`
		UPDATE private_projection_outbox SET status=?, updated_at=?
		 WHERE status IN ('pending','processing','failed')`,
		status, now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListPendingPrivateProjectionReceipts(afterObjectID string, limit int) ([]MemoryWriteReceipt, error) {
	if _, err := s.trayDestination(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT `+qualifiedReceiptColumns+`
		  FROM memory_write_receipts receipt
		  JOIN private_projection_outbox projection ON projection.object_id=receipt.object_id
		 WHERE projection.status IN ('pending','failed') AND projection.object_id>?
		   AND receipt.rowid=(
		     SELECT latest.rowid FROM memory_write_receipts latest
		      WHERE latest.object_id=projection.object_id
		      ORDER BY latest.rowid DESC LIMIT 1
		   )
		 ORDER BY projection.object_id LIMIT ?`, afterObjectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var receipts []MemoryWriteReceipt
	for rows.Next() {
		receipt, err := scanWriteReceipt(rows)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

// EnsureMemoryTrayGraceDeadline upgrades pending candidates created by a
// pre-zero-touch runtime. The legacy column is set to "now"; it is not a
// review timer and never requires the user to open Memory Home.
func (s *Store) EnsureMemoryTrayGraceDeadline(
	candidateID string,
	expectedVersion int,
	now time.Time,
	grace time.Duration,
) (string, error) {
	if _, err := s.trayDestination(); err != nil {
		return "", err
	}
	if !validTrayIdentifier(candidateID) || expectedVersion < 1 {
		return "", errors.New("Memory Tray candidate identity is invalid")
	}
	if err := validateTrayGrace(grace); err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	row, err := loadTrayCandidateTx(tx, candidateID)
	if err != nil {
		return "", err
	}
	if row.version != expectedVersion {
		return "", ErrMemoryTrayVersionConflict
	}
	if row.state != "pending" {
		return "", ErrMemoryTrayTerminal
	}
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	if row.bindingDigest != expectedBinding || row.policyEpoch != expectedPolicy || row.resolverEpoch != expectedResolver {
		return "", ErrProductRuntimeMismatch
	}
	if row.graceExpires != "" {
		return row.graceExpires, nil
	}
	armedAt := now.UTC().Format(time.RFC3339Nano)
	deadline := armedAt
	result, err := tx.Exec(`
		UPDATE memory_tray_candidates
		   SET grace_expires_at=?, updated_at=?
		 WHERE candidate_id=? AND version=? AND state='pending' AND grace_expires_at=''`,
		deadline, armedAt, candidateID, expectedVersion,
	)
	if err != nil {
		return "", err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return "", ErrMemoryTrayVersionConflict
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return deadline, nil
}

func (s *Store) CommitDueMemoryTrayCandidates(now time.Time, limit int) ([]MemoryWriteReceipt, error) {
	if _, err := s.trayDestination(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT candidate_id, version FROM memory_tray_candidates
		 WHERE state='pending' AND grace_expires_at!='' AND grace_expires_at<=?
		 ORDER BY grace_expires_at, candidate_id LIMIT ?`, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	type dueCandidate struct {
		id      string
		version int
	}
	var due []dueCandidate
	for rows.Next() {
		var item dueCandidate
		if err := rows.Scan(&item.id, &item.version); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var receipts []MemoryWriteReceipt
	for _, item := range due {
		receipt, err := s.CommitMemoryTrayCandidate(item.id, item.version, now)
		if errors.Is(err, ErrMemoryTrayVersionConflict) || errors.Is(err, ErrMemoryTrayTerminal) {
			continue
		}
		if err != nil {
			return receipts, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

func (s *Store) insertHumanControlLedgerTx(
	tx *sql.Tx,
	action, objectID, idempotencyKey, destination string,
	policyEpoch, resolverEpoch int64,
	now time.Time,
) (string, error) {
	ledgerID, err := newOpaqueID("turn")
	if err != nil {
		return "", err
	}
	finalizeReceiptID, err := newOpaqueID("receipt")
	if err != nil {
		return "", err
	}
	requestHash := sha256.Sum256([]byte(action + "\x1f" + objectID + "\x1f" + idempotencyKey))
	key := hex.EncodeToString(requestHash[:])
	sessionID := opaqueTurnCorrelation("session", "local-control:"+action)
	turnID := opaqueTurnCorrelation("turn", action+":"+objectID+":"+idempotencyKey)
	eventKey := opaqueTurnCorrelation("event", action+":"+objectID+":"+idempotencyKey)
	idempotency := opaqueTurnCorrelation("idempotency", idempotencyKey)
	createdAt := now.UTC().Format(time.RFC3339Nano)
	bindingDigest, _, _ := s.productRuntimeAuthority()
	_, err = tx.Exec(`
		INSERT INTO turn_ledgers(
			ledger_id, finalize_receipt_id, host, session_id, turn_id, source_event_key,
			idempotency_key, binding_digest, destination_store_id, destination_class,
			policy_epoch, resolver_epoch, request_digest, state, created_at, finalized_at
		) VALUES (?, ?, 'pulse-cli', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'control', ?, ?)`,
		ledgerID, finalizeReceiptID, sessionID, turnID, eventKey, idempotency,
		bindingDigest, s.storeID, destination,
		policyEpoch, resolverEpoch, key, createdAt, createdAt,
	)
	if err != nil {
		return "", err
	}
	return ledgerID, nil
}

// MoveCommittedMemoryScope moves one active logical memory between its
// immutable origin project and Personal Global. The body and capture
// provenance do not change; scope, generation, receipt, and eligibility
// revision commit atomically.
func (s *Store) MoveCommittedMemoryScope(
	objectID string,
	expectedGeneration int,
	targetScope string,
	idempotencyKey string,
	now time.Time,
) (MemoryScopeMoveReceipt, error) {
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.moveCommittedMemoryScopeForAuthority(
		objectID, expectedGeneration, targetScope, idempotencyKey, now,
		expectedBinding, expectedPolicy, expectedResolver,
		s.currentPersonalMemoryScope(expectedBinding),
	)
}

func (s *Store) MoveCommittedMemoryScopeForVerifiedBinding(
	objectID string,
	expectedGeneration int,
	targetScope string,
	idempotencyKey string,
	now time.Time,
	bindingDigest, repositoryID string,
	resolverEpoch int64,
) (MemoryScopeMoveReceipt, error) {
	if !trayBindingDigestPattern.MatchString(bindingDigest) ||
		!validTrayIdentifier(repositoryID) || resolverEpoch < 1 {
		return MemoryScopeMoveReceipt{}, ErrProductRuntimeMismatch
	}
	return s.moveCommittedMemoryScopeForAuthority(
		objectID, expectedGeneration, targetScope, idempotencyKey, now,
		bindingDigest, 0, resolverEpoch, personalMemoryScopeForRepository(repositoryID),
	)
}

func (s *Store) moveCommittedMemoryScopeForAuthority(
	objectID string,
	expectedGeneration int,
	targetScope string,
	idempotencyKey string,
	now time.Time,
	expectedBinding string,
	expectedPolicy, expectedResolver int64,
	authority personalMemoryScope,
) (MemoryScopeMoveReceipt, error) {
	if _, err := s.trayDestination(); err != nil {
		return MemoryScopeMoveReceipt{}, err
	}
	if !validTrayIdentifier(objectID) || !validTrayIdentifier(idempotencyKey) ||
		expectedGeneration < 1 ||
		(targetScope != MemoryScopeProject && targetScope != MemoryScopePersonalGlobal) {
		return MemoryScopeMoveReceipt{}, errors.New("memory scope move is invalid")
	}
	requestHash := sha256.Sum256([]byte(strings.Join([]string{
		"pulse-memory-scope-move-v1", objectID, strconv.Itoa(expectedGeneration), targetScope,
	}, "\x1f")))
	requestDigest := hex.EncodeToString(requestHash[:])
	tx, err := s.db.Begin()
	if err != nil {
		return MemoryScopeMoveReceipt{}, err
	}
	defer tx.Rollback()

	var replayDigest, replayReceiptID string
	err = tx.QueryRow(`
		SELECT request_digest, receipt_id
		  FROM memory_write_idempotency
		 WHERE operation='move_scope' AND idempotency_key=?`, idempotencyKey,
	).Scan(&replayDigest, &replayReceiptID)
	if err == nil {
		if replayDigest != requestDigest {
			return MemoryScopeMoveReceipt{}, ErrMemoryScopeConflict
		}
		receipt, err := scanWriteReceipt(tx.QueryRow(`
			SELECT `+receiptColumns+` FROM memory_write_receipts WHERE receipt_id=?`,
			replayReceiptID,
		))
		if err != nil {
			return MemoryScopeMoveReceipt{}, err
		}
		return MemoryScopeMoveReceipt{
			WriteReceipt: receipt, ObjectID: objectID, Scope: targetScope,
			LogicalGeneration: expectedGeneration + 1,
		}, nil
	}
	if err != sql.ErrNoRows {
		return MemoryScopeMoveReceipt{}, err
	}

	var (
		kind, lifecycle, currentScope, projectNamespace, originalRepository string
		contentDigest, candidateID, destination, bindingDigest              string
		version, generation                                                 int
		policyEpoch, resolverEpoch                                          int64
	)
	err = tx.QueryRow(`
		SELECT object.candidate_kind, object.lifecycle, object.memory_scope,
		       object.project_namespace_id, object.original_repository_id,
		       object.content_digest, object.logical_generation,
		       candidate.candidate_id, candidate.version,
		       ledger.destination_class, ledger.binding_digest,
		       ledger.policy_epoch, ledger.resolver_epoch
		  FROM private_memory_objects object
		  JOIN memory_tray_candidates candidate
		    ON candidate.candidate_id=object.created_from_candidate_id
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 WHERE object.object_id=?`, objectID,
	).Scan(
		&kind, &lifecycle, &currentScope, &projectNamespace, &originalRepository,
		&contentDigest, &generation, &candidateID, &version, &destination,
		&bindingDigest, &policyEpoch, &resolverEpoch,
	)
	if err != nil {
		return MemoryScopeMoveReceipt{}, err
	}
	if bindingDigest != expectedBinding || policyEpoch != expectedPolicy || resolverEpoch != expectedResolver {
		return MemoryScopeMoveReceipt{}, ErrProductRuntimeMismatch
	}
	if lifecycle != "active" || originalRepository != authority.OriginalRepository ||
		projectNamespace != authority.ProjectNamespaceID {
		return MemoryScopeMoveReceipt{}, ErrProductRuntimeMismatch
	}
	if generation != expectedGeneration || currentScope == targetScope {
		return MemoryScopeMoveReceipt{}, ErrMemoryScopeConflict
	}
	var duplicateID string
	if targetScope == MemoryScopePersonalGlobal {
		err = tx.QueryRow(`
			SELECT object_id FROM private_memory_objects
			 WHERE memory_scope='personal_global' AND candidate_kind=?
			   AND content_digest=? AND lifecycle='active' AND object_id!=?`,
			kind, contentDigest, objectID,
		).Scan(&duplicateID)
	} else {
		err = tx.QueryRow(`
			SELECT object_id FROM private_memory_objects
			 WHERE memory_scope='project' AND project_namespace_id=?
			   AND candidate_kind=? AND content_digest=?
			   AND lifecycle='active' AND object_id!=?`,
			projectNamespace, kind, contentDigest, objectID,
		).Scan(&duplicateID)
	}
	if err == nil {
		return MemoryScopeMoveReceipt{}, ErrMemoryScopeConflict
	}
	if err != sql.ErrNoRows {
		return MemoryScopeMoveReceipt{}, err
	}

	ledgerID, err := s.insertHumanControlLedgerTx(
		tx, "move_scope", objectID, idempotencyKey, destination,
		policyEpoch, resolverEpoch, now,
	)
	if err != nil {
		return MemoryScopeMoveReceipt{}, err
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.Exec(`
		UPDATE private_memory_objects
		   SET memory_scope=?, logical_generation=logical_generation+1, modified_at=?
		 WHERE object_id=? AND lifecycle='active'
		   AND logical_generation=? AND memory_scope=?`,
		targetScope, createdAt, objectID, expectedGeneration, currentScope,
	)
	if err != nil {
		return MemoryScopeMoveReceipt{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return MemoryScopeMoveReceipt{}, ErrMemoryScopeConflict
	}
	if kind == PrivateMemoryCandidateSemanticDelta {
		if err := rebuildPrivateSemanticProjectionTx(tx); err != nil {
			return MemoryScopeMoveReceipt{}, fmt.Errorf("rebuild moved semantic projection: %w", err)
		}
	}
	reasonCode := "user_moved_to_project"
	if targetScope == MemoryScopePersonalGlobal {
		reasonCode = "user_moved_to_personal_global"
	}
	receipt, err := insertWriteReceiptTx(tx, MemoryWriteReceipt{
		LedgerID: ledgerID, CandidateID: candidateID, CandidateVersion: version,
		Status: MemoryWriteUpdated, Destination: capture.Destination(destination),
		ContentDigest: contentDigest, ObjectID: objectID, ReasonCode: reasonCode,
		PolicyEpoch: policyEpoch, ResolverEpoch: resolverEpoch,
		MeasurementMethod: "human_control_v1", CreatedAt: createdAt,
	})
	if err != nil {
		return MemoryScopeMoveReceipt{}, err
	}
	if err := insertWriteAuditTx(tx, receipt, "move_scope", MemoryWriteUpdated); err != nil {
		return MemoryScopeMoveReceipt{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO memory_write_idempotency(
			operation, idempotency_key, request_digest, receipt_id, object_id, created_at
		) VALUES ('move_scope', ?, ?, ?, ?, ?)`,
		idempotencyKey, requestDigest, receipt.ReceiptID, objectID, createdAt,
	); err != nil {
		return MemoryScopeMoveReceipt{}, err
	}
	if err := advancePersonalEligibilityTx(tx, now); err != nil {
		return MemoryScopeMoveReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryScopeMoveReceipt{}, err
	}
	return MemoryScopeMoveReceipt{
		WriteReceipt: receipt, ObjectID: objectID, Scope: targetScope,
		LogicalGeneration: expectedGeneration + 1,
	}, nil
}

// DeleteCommittedMemory is the product correction/delete path for a committed
// capsule. Canonical deletion, lifecycle state, receipt, idempotency, and audit
// commit together. Semantic-delta lineage deletion remains fail-closed until
// its contribution map is available.
func (s *Store) DeleteCommittedMemory(objectID, idempotencyKey string, now time.Time) (MemoryWriteReceipt, error) {
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.deleteCommittedMemoryForAuthority(
		objectID, nil, idempotencyKey, now,
		expectedBinding, expectedPolicy, expectedResolver,
		s.currentPersonalMemoryScope(expectedBinding),
	)
}

func (s *Store) DeleteCommittedMemoryGeneration(
	objectID string,
	expectedGeneration int,
	idempotencyKey string,
	now time.Time,
) (MemoryWriteReceipt, error) {
	if expectedGeneration < 1 {
		return MemoryWriteReceipt{}, errors.New("delete generation is invalid")
	}
	expectedBinding, expectedPolicy, expectedResolver := s.productRuntimeAuthority()
	return s.deleteCommittedMemoryForAuthority(
		objectID, &expectedGeneration, idempotencyKey, now,
		expectedBinding, expectedPolicy, expectedResolver,
		s.currentPersonalMemoryScope(expectedBinding),
	)
}

func (s *Store) DeleteCommittedMemoryGenerationForVerifiedBinding(
	objectID string,
	expectedGeneration int,
	idempotencyKey string,
	now time.Time,
	bindingDigest, repositoryID string,
	resolverEpoch int64,
) (MemoryWriteReceipt, error) {
	if expectedGeneration < 1 || !trayBindingDigestPattern.MatchString(bindingDigest) ||
		!validTrayIdentifier(repositoryID) || resolverEpoch < 1 {
		return MemoryWriteReceipt{}, ErrProductRuntimeMismatch
	}
	return s.deleteCommittedMemoryForAuthority(
		objectID, &expectedGeneration, idempotencyKey, now,
		bindingDigest, 0, resolverEpoch, personalMemoryScopeForRepository(repositoryID),
	)
}

func (s *Store) deleteCommittedMemoryForAuthority(
	objectID string,
	expectedGeneration *int,
	idempotencyKey string,
	now time.Time,
	expectedBinding string,
	expectedPolicy, expectedResolver int64,
	authority personalMemoryScope,
) (MemoryWriteReceipt, error) {
	if _, err := s.trayDestination(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if !validTrayIdentifier(idempotencyKey) || !validTrayIdentifier(objectID) {
		return MemoryWriteReceipt{}, errors.New("delete identity is invalid")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	defer tx.Rollback()
	var kind, lifecycle, memoryScope, projectNamespace, originalRepository string
	var candidateID, originalLedgerID, destination, bindingDigest string
	var version, generation int
	var policyEpoch, resolverEpoch int64
	err = tx.QueryRow(`
		SELECT object.candidate_kind, object.lifecycle, object.memory_scope,
		       object.project_namespace_id, object.original_repository_id,
		       object.logical_generation,
		       candidate.candidate_id,
		       candidate.version, ledger.ledger_id, ledger.destination_class, ledger.binding_digest,
		       ledger.policy_epoch, ledger.resolver_epoch
		  FROM private_memory_objects object
		  JOIN memory_tray_candidates candidate
		    ON candidate.candidate_id=object.created_from_candidate_id
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 WHERE object.object_id=?`, objectID,
	).Scan(
		&kind, &lifecycle, &memoryScope, &projectNamespace, &originalRepository,
		&generation, &candidateID, &version, &originalLedgerID,
		&destination, &bindingDigest, &policyEpoch, &resolverEpoch,
	)
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if bindingDigest != expectedBinding || policyEpoch != expectedPolicy || resolverEpoch != expectedResolver {
		return MemoryWriteReceipt{}, ErrProductRuntimeMismatch
	}
	if originalRepository != authority.OriginalRepository ||
		projectNamespace != authority.ProjectNamespaceID ||
		(memoryScope != MemoryScopeProject && memoryScope != MemoryScopePersonalGlobal) {
		return MemoryWriteReceipt{}, ErrProductRuntimeMismatch
	}
	if lifecycle == "deleted" {
		return scanWriteReceipt(tx.QueryRow(`
			SELECT `+receiptColumns+` FROM memory_write_receipts
			 WHERE candidate_id=? AND status='updated' AND reason_code='user_deleted'
			 ORDER BY rowid DESC LIMIT 1`, candidateID))
	}
	if expectedGeneration != nil && generation != *expectedGeneration {
		return MemoryWriteReceipt{}, ErrMemoryScopeConflict
	}
	ledgerID, err := s.insertHumanControlLedgerTx(
		tx, "delete", objectID, idempotencyKey, destination,
		policyEpoch, resolverEpoch, now,
	)
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	if kind == PrivateMemoryCandidateCapsule {
		if _, err := tx.Exec(`
			DELETE FROM events
			 WHERE id IN (SELECT event_id FROM memory_capsules
			               WHERE id=? AND event_id IS NOT NULL)`, objectID); err != nil {
			return MemoryWriteReceipt{}, err
		}
		deleted, err := tx.Exec(`DELETE FROM memory_capsules WHERE id=?`, objectID)
		if err != nil {
			return MemoryWriteReceipt{}, err
		}
		if affected, _ := deleted.RowsAffected(); affected != 1 {
			return MemoryWriteReceipt{}, errors.New("canonical memory object is missing")
		}
	} else if kind != PrivateMemoryCandidateSemanticDelta {
		return MemoryWriteReceipt{}, errors.New("canonical memory object kind is invalid")
	}
	if _, err := tx.Exec(`
		UPDATE private_memory_objects
		   SET lifecycle='deleted', deleted_at=?, modified_at=?,
		       logical_generation=logical_generation+1
		 WHERE object_id=? AND lifecycle='active'`, createdAt, createdAt, objectID); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if _, err := tx.Exec(`
		UPDATE memory_tray_candidates SET payload_json='{}', updated_at=?
		 WHERE canonical_object_id=? AND state='committed'`, createdAt, objectID); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if kind == PrivateMemoryCandidateSemanticDelta {
		if err := deletePrivateSemanticProjectionTx(tx, objectID); err != nil {
			return MemoryWriteReceipt{}, err
		}
	}
	if _, err := tx.Exec(`
		UPDATE private_projection_outbox SET status='complete', updated_at=?
		 WHERE object_id=? AND status IN ('pending','processing','failed')`, createdAt, objectID); err != nil {
		return MemoryWriteReceipt{}, err
	}
	receipt, err := insertWriteReceiptTx(tx, MemoryWriteReceipt{
		LedgerID: ledgerID, CandidateID: candidateID, CandidateVersion: version,
		Status: MemoryWriteUpdated, Destination: capture.Destination(destination), ObjectID: objectID,
		ReasonCode: "user_deleted", PolicyEpoch: policyEpoch, ResolverEpoch: resolverEpoch,
		MeasurementMethod: "human_control_v1", CreatedAt: createdAt,
	})
	if err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := insertWriteAuditTx(tx, receipt, "delete", MemoryWriteUpdated); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO memory_write_idempotency(
			operation, idempotency_key, request_digest, receipt_id, created_at
		) VALUES ('delete', ?, ?, ?, ?)`, idempotencyKey, objectID, receipt.ReceiptID, createdAt); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := advancePersonalEligibilityTx(tx, now); err != nil {
		return MemoryWriteReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryWriteReceipt{}, err
	}
	return receipt, nil
}

// WipeProductMemory is the explicit human-control escape hatch for a Personal
// Personal vault. Normal semantic writes cannot bypass Memory Tray, while the
// exact-confirmation HTTP surface can still remove the whole local product
// memory atomically.
func (s *Store) WipeProductMemory() error {
	if _, err := s.trayDestination(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.applyProductMemoryWipeTx(tx, s.clock().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) applyProductMemoryWipeTx(tx *sql.Tx, now time.Time) error {
	if _, err := tx.Exec(`
		DELETE FROM private_projection_outbox;
		DELETE FROM memory_write_audit;
		DELETE FROM memory_write_idempotency;
		DELETE FROM memory_write_receipts;
		DELETE FROM private_memory_objects;
		DELETE FROM memory_tray_candidates;
		DELETE FROM turn_ledgers;
		DELETE FROM memory_capsules;
		DELETE FROM continuity_observations;
		DELETE FROM continuity_checkpoints;
		DELETE FROM continuity_sessions;
		DELETE FROM continuity_threads;`); err != nil {
		return err
	}
	if err := wipeHostExtractedGraph(tx); err != nil {
		return err
	}
	return nil
}
