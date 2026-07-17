package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	ContinuityDeliveryReceiptSchema = "pulse.continuity_delivery_receipt.v1"

	ContinuityDeliveryOfferedToHost       = "offered_to_host"
	ContinuityDeliveryHostObserved        = "host_observed"
	continuityDeliveryProviderMeasurement = "provider_measurement"

	ContinuityDeliveryPurposeSessionStart  = "session_start"
	ContinuityDeliveryPurposeSubagentStart = "subagent_start"
)

var (
	ErrContinuityDeliveryUnavailable         = errors.New("continuity delivery ledger is unavailable for this store")
	ErrContinuityDeliveryInvalid             = errors.New("continuity delivery fact is invalid")
	ErrContinuityDeliveryAuthority           = errors.New("continuity delivery authority mismatch")
	ErrContinuityDeliveryIdempotencyConflict = errors.New("continuity delivery idempotency conflict")
	ErrContinuityDeliveryTransition          = errors.New("continuity delivery transition conflict")
)

var continuitySessionRefPattern = regexp.MustCompile(`^session:[a-f0-9]{64}$`)

type ContinuityDeliveryOfferRequest struct {
	ContextID              string   `json:"context_id"`
	IdempotencyKey         string   `json:"idempotency_key"`
	Purpose                string   `json:"purpose"`
	BindingDigest          string   `json:"binding_digest"`
	RepositoryID           string   `json:"repository_id"`
	Host                   string   `json:"host"`
	SessionRef             string   `json:"session_ref"`
	PayloadDigest          string   `json:"payload_digest"`
	ObjectIDs              []string `json:"object_ids"`
	EvidenceIDs            []string `json:"evidence_ids"`
	MethodID               string   `json:"method_id"`
	MethodVersion          string   `json:"method_version"`
	RenderedBytes          int      `json:"rendered_bytes"`
	PulseTokens            int      `json:"pulse_tokens"`
	BaselineKind           string   `json:"baseline_kind,omitempty"`
	SourceEquivalentTokens *int     `json:"source_equivalent_tokens,omitempty"`
	CoverageCounted        int      `json:"coverage_counted"`
	CoverageTotal          int      `json:"coverage_total"`
	SourceEventDigest      string   `json:"source_event_digest"`
}

type ContinuityDeliveryObservationRequest struct {
	ContextID         string `json:"context_id"`
	IdempotencyKey    string `json:"idempotency_key"`
	BindingDigest     string `json:"binding_digest"`
	RepositoryID      string `json:"repository_id"`
	Host              string `json:"host"`
	SessionRef        string `json:"session_ref"`
	SourceEventDigest string `json:"source_event_digest"`
}

type continuityProviderMeasurementRequest struct {
	ContextID                 string
	IdempotencyKey            string
	BindingDigest             string
	RepositoryID              string
	Host                      string
	SessionRef                string
	ProviderActualInputTokens int
	ProviderActualSource      string
	ProviderEvidenceDigest    string
}

type ContinuityDeliveryReceipt struct {
	Schema                    string   `json:"schema"`
	ReceiptSequence           int64    `json:"receipt_sequence"`
	ReceiptID                 string   `json:"receipt_id"`
	ContextID                 string   `json:"context_id"`
	ParentReceiptID           string   `json:"parent_receipt_id,omitempty"`
	State                     string   `json:"state"`
	Purpose                   string   `json:"purpose"`
	StoreID                   string   `json:"store_id"`
	RepositoryID              string   `json:"repository_id"`
	BindingDigest             string   `json:"binding_digest"`
	Host                      string   `json:"host"`
	SessionRef                string   `json:"session_ref"`
	PayloadDigest             string   `json:"payload_digest"`
	ObjectRefCount            int      `json:"object_ref_count"`
	EvidenceRefCount          int      `json:"evidence_ref_count"`
	RefsManifestDigest        string   `json:"refs_manifest_digest"`
	ObjectIDs                 []string `json:"object_ids"`
	EvidenceIDs               []string `json:"evidence_ids"`
	MethodID                  string   `json:"method_id"`
	MethodVersion             string   `json:"method_version"`
	RenderedBytes             int      `json:"rendered_bytes"`
	PulseTokens               int      `json:"pulse_tokens"`
	BaselineKind              string   `json:"baseline_kind,omitempty"`
	SourceEquivalentTokens    *int     `json:"source_equivalent_tokens,omitempty"`
	CoverageCounted           int      `json:"coverage_counted"`
	CoverageTotal             int      `json:"coverage_total"`
	SourceEventDigest         string   `json:"source_event_digest,omitempty"`
	ProviderActualInputTokens *int     `json:"provider_actual_input_tokens,omitempty"`
	ProviderActualSource      string   `json:"provider_actual_source,omitempty"`
	ProviderEvidenceDigest    string   `json:"provider_evidence_digest,omitempty"`
	CreatedAt                 string   `json:"created_at"`
}

func (s *Store) ConfigureContinuityDeliveryAuthority(bindingDigest, repositoryID string) error {
	if s == nil || !s.productTrayRequired() || !trayBindingDigestPattern.MatchString(bindingDigest) ||
		!validTrayIdentifier(repositoryID) {
		return ErrContinuityDeliveryInvalid
	}
	expectedBinding, _, _ := s.productRuntimeAuthority()
	if expectedBinding != bindingDigest {
		return ErrContinuityDeliveryAuthority
	}
	s.continuityAuthorityMu.Lock()
	s.continuityRepository = repositoryID
	s.continuityAuthorityMu.Unlock()
	return nil
}

func (s *Store) continuityDeliveryAuthority() (string, string, bool) {
	if s == nil || !s.productTrayRequired() {
		return "", "", false
	}
	binding, _, _ := s.productRuntimeAuthority()
	s.continuityAuthorityMu.RLock()
	repository := s.continuityRepository
	s.continuityAuthorityMu.RUnlock()
	return binding, repository, trayBindingDigestPattern.MatchString(binding) && validTrayIdentifier(repository)
}

// ProductRuntimeBoundary returns the content-free workspace boundary used by
// Memory Home. It intentionally exposes neither policy credentials nor any
// stored memory body; callers receive only the already configured binding and
// repository identifiers.
func (s *Store) ProductRuntimeBoundary() (bindingDigest, repositoryID string, ok bool) {
	return s.continuityDeliveryAuthority()
}

func (s *Store) validateContinuityDeliveryAuthority(bindingDigest, repositoryID string) error {
	binding, repository, ok := s.continuityDeliveryAuthority()
	if !ok {
		return ErrContinuityDeliveryAuthority
	}
	if binding != bindingDigest || repository != repositoryID {
		return ErrContinuityDeliveryAuthority
	}
	return nil
}

func validContinuityDeliveryPurpose(value string) bool {
	return value == ContinuityDeliveryPurposeSessionStart || value == ContinuityDeliveryPurposeSubagentStart
}

func validContinuityDeliveryHost(value string) bool {
	return value == "codex" || value == "claude-code" || value == "cursor"
}

func validContinuityDeliveryIDs(values []string) bool {
	if len(values) > 64 || !sort.StringsAreSorted(values) {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validTrayIdentifier(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func continuityDeliveryContextID(req ContinuityDeliveryOfferRequest) string {
	canonical := strings.Join([]string{
		"pulse-continuity-context-v1", req.BindingDigest, req.RepositoryID, req.Host,
		req.SessionRef, req.Purpose, req.SourceEventDigest, req.PayloadDigest,
	}, "\x1f")
	digest := sha256.Sum256([]byte(canonical))
	return "context_" + hex.EncodeToString(digest[:])
}

func continuityDeliveryOfferIdempotencyKey(req ContinuityDeliveryOfferRequest) string {
	canonical := strings.Join([]string{
		"pulse.continuity_delivery.v1", req.Purpose, req.BindingDigest, req.RepositoryID,
		req.Host, req.SessionRef, req.SourceEventDigest,
	}, "\x1f")
	digest := sha256.Sum256([]byte(canonical))
	return "continuity-offer:" + hex.EncodeToString(digest[:])
}

func validContinuityDeliveryBaseline(kind string, source *int, counted, total int) bool {
	if kind == "" && source == nil && counted == 0 && total == 0 {
		return true
	}
	return kind == "canonical_structured_resume_v1" && source != nil && *source >= 0 && *source <= 10_485_760 &&
		total >= 1 && total <= 1_000_000 && counted >= 1 && counted <= total
}

func validateContinuityOffer(req ContinuityDeliveryOfferRequest) error {
	if !validTrayIdentifier(req.ContextID) || !validTrayIdentifier(req.IdempotencyKey) ||
		req.ContextID != continuityDeliveryContextID(req) ||
		req.IdempotencyKey != continuityDeliveryOfferIdempotencyKey(req) ||
		!validContinuityDeliveryPurpose(req.Purpose) || !trayBindingDigestPattern.MatchString(req.BindingDigest) ||
		!validTrayIdentifier(req.RepositoryID) || !validContinuityDeliveryHost(req.Host) ||
		!continuitySessionRefPattern.MatchString(req.SessionRef) ||
		!trayBindingDigestPattern.MatchString(req.PayloadDigest) ||
		!validContinuityDeliveryIDs(req.ObjectIDs) || !validContinuityDeliveryIDs(req.EvidenceIDs) ||
		req.MethodID != "utf8_bytes_div4_ceil" || req.MethodVersion != "1" ||
		req.RenderedBytes < 1 || req.RenderedBytes > 1_048_576 || req.PulseTokens < 1 || req.PulseTokens > 1_048_576 ||
		req.PulseTokens != (req.RenderedBytes+3)/4 ||
		!validContinuityDeliveryBaseline(req.BaselineKind, req.SourceEquivalentTokens, req.CoverageCounted, req.CoverageTotal) ||
		!trayBindingDigestPattern.MatchString(req.SourceEventDigest) {
		return ErrContinuityDeliveryInvalid
	}
	return nil
}

func validateContinuityObservation(req ContinuityDeliveryObservationRequest) error {
	if !validTrayIdentifier(req.ContextID) || !validTrayIdentifier(req.IdempotencyKey) ||
		!trayBindingDigestPattern.MatchString(req.BindingDigest) || !validTrayIdentifier(req.RepositoryID) ||
		!validContinuityDeliveryHost(req.Host) || !continuitySessionRefPattern.MatchString(req.SessionRef) ||
		!trayBindingDigestPattern.MatchString(req.SourceEventDigest) {
		return ErrContinuityDeliveryInvalid
	}
	return nil
}

func continuityDeliveryIdempotencyHash(key string) string {
	digest := sha256.Sum256([]byte("pulse-continuity-delivery-idempotency-v1\x00" + key))
	return hex.EncodeToString(digest[:])
}

func continuityDeliveryOperationDigest(state string, value any) (string, error) {
	bytes, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("pulse-continuity-delivery-operation-v1\x00"+state+"\x00"), bytes...))
	return hex.EncodeToString(digest[:]), nil
}

const continuityDeliveryReceiptColumns = `
	receipt_seq, receipt_id, context_id, COALESCE(parent_receipt_id, ''), receipt_state, purpose,
	store_id, repository_id, binding_digest, host, session_ref, payload_digest,
	object_ref_count, evidence_ref_count, refs_manifest_digest,
	method_id, method_version, rendered_bytes, pulse_tokens, COALESCE(baseline_kind, ''),
	source_equivalent_tokens, coverage_counted, coverage_total, COALESCE(source_event_digest, ''),
	provider_actual_input_tokens, COALESCE(provider_actual_source, ''), COALESCE(provider_evidence_digest, ''), created_at`

type continuityDeliveryScanner interface{ Scan(...any) error }

func scanContinuityDeliveryReceipt(scanner continuityDeliveryScanner) (ContinuityDeliveryReceipt, error) {
	receipt := ContinuityDeliveryReceipt{Schema: ContinuityDeliveryReceiptSchema}
	err := scanner.Scan(
		&receipt.ReceiptSequence, &receipt.ReceiptID, &receipt.ContextID, &receipt.ParentReceiptID,
		&receipt.State, &receipt.Purpose, &receipt.StoreID, &receipt.RepositoryID, &receipt.BindingDigest,
		&receipt.Host, &receipt.SessionRef, &receipt.PayloadDigest,
		&receipt.ObjectRefCount, &receipt.EvidenceRefCount, &receipt.RefsManifestDigest,
		&receipt.MethodID, &receipt.MethodVersion,
		&receipt.RenderedBytes, &receipt.PulseTokens, &receipt.BaselineKind, &receipt.SourceEquivalentTokens,
		&receipt.CoverageCounted, &receipt.CoverageTotal, &receipt.SourceEventDigest,
		&receipt.ProviderActualInputTokens, &receipt.ProviderActualSource, &receipt.ProviderEvidenceDigest,
		&receipt.CreatedAt,
	)
	return receipt, err
}

func loadContinuityDeliveryReceiptByIdempotencyTx(
	tx *sql.Tx, state, idempotencyHash string,
) (ContinuityDeliveryReceipt, error) {
	return scanContinuityDeliveryReceipt(tx.QueryRow(`SELECT `+continuityDeliveryReceiptColumns+`
		FROM continuity_delivery_receipts WHERE receipt_state=? AND idempotency_key_hash=?`, state, idempotencyHash))
}

func loadContinuityDeliveryReceiptByContextTx(
	tx *sql.Tx, contextID, state string,
) (ContinuityDeliveryReceipt, error) {
	return scanContinuityDeliveryReceipt(tx.QueryRow(`SELECT `+continuityDeliveryReceiptColumns+`
		FROM continuity_delivery_receipts WHERE context_id=? AND receipt_state=?`, contextID, state))
}

type continuityDeliveryQueryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func loadContinuityDeliveryRefs(queryer continuityDeliveryQueryer, table, receiptID string) ([]string, error) {
	if table != "continuity_delivery_object_refs" && table != "continuity_delivery_evidence_refs" {
		return nil, ErrContinuityDeliveryInvalid
	}
	rows, err := queryer.Query(`SELECT ref_id FROM `+table+` WHERE receipt_id=? ORDER BY ordinal`, receiptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []string{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func hydrateContinuityDeliveryRefsTx(tx *sql.Tx, receipt *ContinuityDeliveryReceipt) error {
	receipts := []ContinuityDeliveryReceipt{*receipt}
	if err := hydrateContinuityDeliveryRefsBatchTx(tx, receipts); err != nil {
		return err
	}
	*receipt = receipts[0]
	return nil
}

// hydrateContinuityDeliveryRefsBatchTx validates normalized references and
// their immutable seals with three bounded queries regardless of receipt count.
// Callers must keep the batch below SQLite's host-parameter limit.
func hydrateContinuityDeliveryRefsBatchTx(tx *sql.Tx, receipts []ContinuityDeliveryReceipt) error {
	if len(receipts) == 0 {
		return nil
	}
	indexes := make(map[string]int, len(receipts))
	arguments := make([]any, len(receipts))
	for index := range receipts {
		receiptID := receipts[index].ReceiptID
		if !validTrayIdentifier(receiptID) {
			return ErrContinuityDeliveryTransition
		}
		if _, duplicate := indexes[receiptID]; duplicate {
			return ErrContinuityDeliveryTransition
		}
		indexes[receiptID] = index
		arguments[index] = receiptID
		receipts[index].ObjectIDs = []string{}
		receipts[index].EvidenceIDs = []string{}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(receipts)), ",")
	for _, refSet := range []struct {
		table string
		set   func(*ContinuityDeliveryReceipt, string)
	}{
		{"continuity_delivery_object_refs", func(receipt *ContinuityDeliveryReceipt, ref string) {
			receipt.ObjectIDs = append(receipt.ObjectIDs, ref)
		}},
		{"continuity_delivery_evidence_refs", func(receipt *ContinuityDeliveryReceipt, ref string) {
			receipt.EvidenceIDs = append(receipt.EvidenceIDs, ref)
		}},
	} {
		rows, err := tx.Query(`SELECT receipt_id, ref_id FROM `+refSet.table+`
			WHERE receipt_id IN (`+placeholders+`) ORDER BY receipt_id, ordinal`, arguments...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var receiptID, ref string
			if err := rows.Scan(&receiptID, &ref); err != nil {
				rows.Close()
				return err
			}
			index, ok := indexes[receiptID]
			if !ok {
				rows.Close()
				return ErrContinuityDeliveryTransition
			}
			refSet.set(&receipts[index], ref)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}

	seals := make(map[string]string, len(receipts))
	rows, err := tx.Query(`SELECT receipt_id, refs_manifest_digest FROM continuity_delivery_ref_seals
		WHERE receipt_id IN (`+placeholders+`)`, arguments...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var receiptID, digest string
		if err := rows.Scan(&receiptID, &digest); err != nil {
			rows.Close()
			return err
		}
		if _, ok := indexes[receiptID]; !ok {
			rows.Close()
			return ErrContinuityDeliveryTransition
		}
		seals[receiptID] = digest
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range receipts {
		receipt := &receipts[index]
		sealedDigest, sealed := seals[receipt.ReceiptID]
		if !sealed || sealedDigest != receipt.RefsManifestDigest ||
			len(receipt.ObjectIDs) != receipt.ObjectRefCount || len(receipt.EvidenceIDs) != receipt.EvidenceRefCount ||
			continuityDeliveryRefsManifestDigest(receipt.ObjectIDs, receipt.EvidenceIDs) != receipt.RefsManifestDigest {
			return ErrContinuityDeliveryTransition
		}
	}
	return nil
}

func continuityDeliveryRefsManifestDigest(objectIDs, evidenceIDs []string) string {
	manifest := struct {
		ObjectIDs   []string `json:"object_ids"`
		EvidenceIDs []string `json:"evidence_ids"`
	}{objectIDs, evidenceIDs}
	encoded, _ := json.Marshal(manifest)
	digest := sha256.Sum256(append([]byte("pulse-continuity-delivery-refs-v1\x00"), encoded...))
	return hex.EncodeToString(digest[:])
}

func replayContinuityDeliveryTx(
	tx *sql.Tx, state, idempotencyHash, operationDigest string,
) (ContinuityDeliveryReceipt, bool, error) {
	receipt, err := loadContinuityDeliveryReceiptByIdempotencyTx(tx, state, idempotencyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ContinuityDeliveryReceipt{}, false, nil
	}
	if err != nil {
		return ContinuityDeliveryReceipt{}, false, err
	}
	var storedDigest string
	if err := tx.QueryRow(`SELECT operation_digest FROM continuity_delivery_receipts WHERE receipt_id=?`, receipt.ReceiptID).Scan(&storedDigest); err != nil {
		return ContinuityDeliveryReceipt{}, false, err
	}
	if storedDigest != operationDigest {
		return ContinuityDeliveryReceipt{}, true, ErrContinuityDeliveryIdempotencyConflict
	}
	if err := hydrateContinuityDeliveryRefsTx(tx, &receipt); err != nil {
		return ContinuityDeliveryReceipt{}, true, err
	}
	return receipt, true, nil
}

func insertContinuityDeliveryRefsTx(tx *sql.Tx, table, receiptID string, refs []string) error {
	if table != "continuity_delivery_object_refs" && table != "continuity_delivery_evidence_refs" {
		return ErrContinuityDeliveryInvalid
	}
	for ordinal, ref := range refs {
		if _, err := tx.Exec(`INSERT INTO `+table+`(receipt_id, ordinal, ref_id) VALUES (?, ?, ?)`, receiptID, ordinal, ref); err != nil {
			return err
		}
	}
	return nil
}

func copyContinuityDeliveryRefsTx(tx *sql.Tx, table, fromReceiptID, toReceiptID string) error {
	if table != "continuity_delivery_object_refs" && table != "continuity_delivery_evidence_refs" {
		return ErrContinuityDeliveryInvalid
	}
	_, err := tx.Exec(`INSERT INTO `+table+`(receipt_id, ordinal, ref_id)
		SELECT ?, ordinal, ref_id FROM `+table+` WHERE receipt_id=? ORDER BY ordinal`, toReceiptID, fromReceiptID)
	return err
}

func copyAndSealContinuityDeliveryRefsTx(tx *sql.Tx, fromReceiptID, toReceiptID string) error {
	for _, table := range []string{"continuity_delivery_object_refs", "continuity_delivery_evidence_refs"} {
		if err := copyContinuityDeliveryRefsTx(tx, table, fromReceiptID, toReceiptID); err != nil {
			return err
		}
	}
	return sealContinuityDeliveryRefsTx(tx, toReceiptID)
}

func sealContinuityDeliveryRefsTx(tx *sql.Tx, receiptID string) error {
	var objectCount, evidenceCount int
	var manifestDigest string
	if err := tx.QueryRow(`
		SELECT object_ref_count, evidence_ref_count, refs_manifest_digest
		  FROM continuity_delivery_receipts WHERE receipt_id=?`, receiptID,
	).Scan(&objectCount, &evidenceCount, &manifestDigest); err != nil {
		return err
	}
	objectIDs, err := loadContinuityDeliveryRefs(tx, "continuity_delivery_object_refs", receiptID)
	if err != nil {
		return err
	}
	evidenceIDs, err := loadContinuityDeliveryRefs(tx, "continuity_delivery_evidence_refs", receiptID)
	if err != nil {
		return err
	}
	if len(objectIDs) != objectCount || len(evidenceIDs) != evidenceCount ||
		continuityDeliveryRefsManifestDigest(objectIDs, evidenceIDs) != manifestDigest {
		return ErrContinuityDeliveryTransition
	}
	_, err = tx.Exec(`
		INSERT INTO continuity_delivery_ref_seals(receipt_id, refs_manifest_digest) VALUES (?, ?)`,
		receiptID, manifestDigest,
	)
	return err
}

func (s *Store) RecordContinuityOffer(
	ctx context.Context, req ContinuityDeliveryOfferRequest, now time.Time,
) (ContinuityDeliveryReceipt, error) {
	if !s.productTrayRequired() {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryUnavailable
	}
	if err := validateContinuityOffer(req); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := s.validateContinuityDeliveryAuthority(req.BindingDigest, req.RepositoryID); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	operationDigest, err := continuityDeliveryOperationDigest(ContinuityDeliveryOfferedToHost, req)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	idempotencyHash := continuityDeliveryIdempotencyHash(req.IdempotencyKey)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	defer tx.Rollback()
	if replay, found, err := replayContinuityDeliveryTx(tx, ContinuityDeliveryOfferedToHost, idempotencyHash, operationDigest); found || err != nil {
		return replay, err
	}
	if _, err := loadContinuityDeliveryReceiptByContextTx(tx, req.ContextID, ContinuityDeliveryOfferedToHost); err == nil {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryTransition
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ContinuityDeliveryReceipt{}, err
	}
	receiptID, err := newOpaqueID("delivery")
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	createdAt := now.UTC().Format(time.RFC3339Nano)
	refsManifestDigest := continuityDeliveryRefsManifestDigest(req.ObjectIDs, req.EvidenceIDs)
	if _, err := tx.Exec(`
		INSERT INTO continuity_delivery_receipts(
			receipt_id, context_id, receipt_state, purpose, store_id, repository_id, binding_digest,
			host, session_ref, payload_digest, object_ref_count, evidence_ref_count, refs_manifest_digest,
			method_id, method_version, rendered_bytes, pulse_tokens,
			baseline_kind, source_equivalent_tokens, coverage_counted, coverage_total,
			source_event_digest, idempotency_key_hash, operation_digest, created_at
		) VALUES (?, ?, 'offered_to_host', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		receiptID, req.ContextID, req.Purpose, s.storeID, req.RepositoryID, req.BindingDigest,
		req.Host, req.SessionRef, req.PayloadDigest, len(req.ObjectIDs), len(req.EvidenceIDs), refsManifestDigest,
		req.MethodID, req.MethodVersion,
		req.RenderedBytes, req.PulseTokens, nullableContinuityDeliveryString(req.BaselineKind),
		req.SourceEquivalentTokens, req.CoverageCounted, req.CoverageTotal,
		req.SourceEventDigest, idempotencyHash, operationDigest, createdAt,
	); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := insertContinuityDeliveryRefsTx(tx, "continuity_delivery_object_refs", receiptID, req.ObjectIDs); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := insertContinuityDeliveryRefsTx(tx, "continuity_delivery_evidence_refs", receiptID, req.EvidenceIDs); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := sealContinuityDeliveryRefsTx(tx, receiptID); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	receipt, err := loadContinuityDeliveryReceiptByIdempotencyTx(tx, ContinuityDeliveryOfferedToHost, idempotencyHash)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := hydrateContinuityDeliveryRefsTx(tx, &receipt); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	return receipt, nil
}

func nullableContinuityDeliveryString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func continuityDeliveryTimeAfter(now time.Time, parentCreatedAt string) string {
	value := now.UTC()
	if parent, err := time.Parse(time.RFC3339Nano, parentCreatedAt); err == nil && !value.After(parent) {
		value = parent.Add(time.Nanosecond)
	}
	return value.Format(time.RFC3339Nano)
}

func continuityObservationMatchesOffer(req ContinuityDeliveryObservationRequest, offer ContinuityDeliveryReceipt) bool {
	return offer.ContextID == req.ContextID && offer.BindingDigest == req.BindingDigest &&
		offer.RepositoryID == req.RepositoryID && offer.Host == req.Host && offer.SessionRef == req.SessionRef
}

func (s *Store) RecordContinuityHostObserved(
	ctx context.Context, req ContinuityDeliveryObservationRequest, now time.Time,
) (ContinuityDeliveryReceipt, error) {
	if !s.productTrayRequired() {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryUnavailable
	}
	if err := validateContinuityObservation(req); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := s.validateContinuityDeliveryAuthority(req.BindingDigest, req.RepositoryID); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	operationDigest, err := continuityDeliveryOperationDigest(ContinuityDeliveryHostObserved, req)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	idempotencyHash := continuityDeliveryIdempotencyHash(req.IdempotencyKey)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	defer tx.Rollback()
	if replay, found, err := replayContinuityDeliveryTx(tx, ContinuityDeliveryHostObserved, idempotencyHash, operationDigest); found || err != nil {
		return replay, err
	}
	offer, err := loadContinuityDeliveryReceiptByContextTx(tx, req.ContextID, ContinuityDeliveryOfferedToHost)
	if errors.Is(err, sql.ErrNoRows) {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryTransition
	}
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if !continuityObservationMatchesOffer(req, offer) {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryAuthority
	}
	if req.SourceEventDigest == offer.SourceEventDigest {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryTransition
	}
	if _, err := loadContinuityDeliveryReceiptByContextTx(tx, req.ContextID, ContinuityDeliveryHostObserved); err == nil {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryTransition
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ContinuityDeliveryReceipt{}, err
	}
	receiptID, err := newOpaqueID("delivery")
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	createdAt := continuityDeliveryTimeAfter(now, offer.CreatedAt)
	result, err := tx.Exec(`
		INSERT INTO continuity_delivery_receipts(
			receipt_id, context_id, parent_receipt_id, receipt_state, purpose, store_id, repository_id,
			binding_digest, host, session_ref, payload_digest,
			object_ref_count, evidence_ref_count, refs_manifest_digest, method_id, method_version,
			rendered_bytes, pulse_tokens, baseline_kind, source_equivalent_tokens,
			coverage_counted, coverage_total, source_event_digest,
			idempotency_key_hash, operation_digest, created_at
		)
		SELECT ?, context_id, receipt_id, 'host_observed', purpose, store_id, repository_id,
		       binding_digest, host, session_ref, payload_digest,
		       object_ref_count, evidence_ref_count, refs_manifest_digest, method_id, method_version,
		       rendered_bytes, pulse_tokens, baseline_kind, source_equivalent_tokens,
		       coverage_counted, coverage_total, ?, ?, ?, ?
		  FROM continuity_delivery_receipts
		 WHERE receipt_id=? AND receipt_state='offered_to_host'`,
		receiptID, req.SourceEventDigest, idempotencyHash, operationDigest, createdAt, offer.ReceiptID,
	)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryTransition
	}
	if err := copyAndSealContinuityDeliveryRefsTx(tx, offer.ReceiptID, receiptID); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	receipt, err := loadContinuityDeliveryReceiptByIdempotencyTx(tx, ContinuityDeliveryHostObserved, idempotencyHash)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := hydrateContinuityDeliveryRefsTx(tx, &receipt); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	return receipt, nil
}

func (s *Store) recordContinuityProviderMeasurement(
	ctx context.Context, req continuityProviderMeasurementRequest, now time.Time,
) (ContinuityDeliveryReceipt, error) {
	if !s.productTrayRequired() || !validTrayIdentifier(req.ContextID) || !validTrayIdentifier(req.IdempotencyKey) ||
		!trayBindingDigestPattern.MatchString(req.BindingDigest) || !validTrayIdentifier(req.RepositoryID) ||
		!validContinuityDeliveryHost(req.Host) || !continuitySessionRefPattern.MatchString(req.SessionRef) ||
		req.ProviderActualInputTokens < 0 || req.ProviderActualInputTokens > 10_485_760 ||
		!trayBindingDigestPattern.MatchString(req.ProviderEvidenceDigest) ||
		!((req.Host == "codex" && req.ProviderActualSource == "codex_provider_usage_v1") ||
			(req.Host == "claude-code" && req.ProviderActualSource == "claude_provider_usage_v1")) {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryInvalid
	}
	if err := s.validateContinuityDeliveryAuthority(req.BindingDigest, req.RepositoryID); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	operationDigest, err := continuityDeliveryOperationDigest(continuityDeliveryProviderMeasurement, req)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	idempotencyHash := continuityDeliveryIdempotencyHash(req.IdempotencyKey)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	defer tx.Rollback()
	if replay, found, err := replayContinuityDeliveryTx(tx, continuityDeliveryProviderMeasurement, idempotencyHash, operationDigest); found || err != nil {
		return replay, err
	}
	observed, err := loadContinuityDeliveryReceiptByContextTx(tx, req.ContextID, ContinuityDeliveryHostObserved)
	if errors.Is(err, sql.ErrNoRows) {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryTransition
	}
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if observed.BindingDigest != req.BindingDigest || observed.RepositoryID != req.RepositoryID ||
		observed.Host != req.Host || observed.SessionRef != req.SessionRef {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryAuthority
	}
	if _, err := loadContinuityDeliveryReceiptByContextTx(tx, req.ContextID, continuityDeliveryProviderMeasurement); err == nil {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryTransition
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ContinuityDeliveryReceipt{}, err
	}
	receiptID, err := newOpaqueID("delivery")
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	createdAt := continuityDeliveryTimeAfter(now, observed.CreatedAt)
	result, err := tx.Exec(`
		INSERT INTO continuity_delivery_receipts(
			receipt_id, context_id, parent_receipt_id, receipt_state, purpose, store_id, repository_id,
			binding_digest, host, session_ref, payload_digest,
			object_ref_count, evidence_ref_count, refs_manifest_digest, method_id, method_version,
			rendered_bytes, pulse_tokens, baseline_kind, source_equivalent_tokens,
			coverage_counted, coverage_total, provider_actual_input_tokens,
			provider_actual_source, provider_evidence_digest,
			idempotency_key_hash, operation_digest, created_at
		)
		SELECT ?, context_id, receipt_id, 'provider_measurement', purpose, store_id, repository_id,
		       binding_digest, host, session_ref, payload_digest,
		       object_ref_count, evidence_ref_count, refs_manifest_digest, method_id, method_version,
		       rendered_bytes, pulse_tokens, baseline_kind, source_equivalent_tokens,
		       coverage_counted, coverage_total, ?, ?, ?, ?, ?, ?
		  FROM continuity_delivery_receipts
		 WHERE receipt_id=? AND receipt_state='host_observed'`,
		receiptID, req.ProviderActualInputTokens, req.ProviderActualSource, req.ProviderEvidenceDigest,
		idempotencyHash, operationDigest, createdAt, observed.ReceiptID,
	)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ContinuityDeliveryReceipt{}, ErrContinuityDeliveryTransition
	}
	if err := copyAndSealContinuityDeliveryRefsTx(tx, observed.ReceiptID, receiptID); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	receipt, err := loadContinuityDeliveryReceiptByIdempotencyTx(tx, continuityDeliveryProviderMeasurement, idempotencyHash)
	if err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := hydrateContinuityDeliveryRefsTx(tx, &receipt); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContinuityDeliveryReceipt{}, err
	}
	return receipt, nil
}

func (r ContinuityDeliveryReceipt) String() string {
	return fmt.Sprintf("%s:%s", r.ContextID, r.State)
}
