package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func testContinuityDeliveryDigest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func testContinuitySessionRef(value string) string {
	return "session:" + testContinuityDeliveryDigest(value)
}

func testContinuityInt(value int) *int { return &value }

func testContinuityEqualIDs(left, right []string) bool {
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

func openContinuityDeliveryStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenVault(filepath.Join(t.TempDir(), "personal.db"), StoreKindPersonal, "store_personal_delivery")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.ConfigureContinuityDeliveryAuthority(
		localStoreBindingDigest("store_personal_delivery"), "repository_pulse",
	); err != nil {
		t.Fatal(err)
	}
	return s
}

func testContinuityOfferRequest() ContinuityDeliveryOfferRequest {
	req := ContinuityDeliveryOfferRequest{
		Purpose:       ContinuityDeliveryPurposeSessionStart,
		BindingDigest: localStoreBindingDigest("store_personal_delivery"), RepositoryID: "repository_pulse",
		Host: "codex", SessionRef: testContinuitySessionRef("raw-session-must-not-persist"),
		PayloadDigest: testContinuityDeliveryDigest("exact final additionalContext bytes"),
		ObjectIDs:     []string{"pulse:memory_01"}, EvidenceIDs: []string{"pulse:pulse:memory_01"},
		MethodID: "utf8_bytes_div4_ceil", MethodVersion: "1",
		RenderedBytes: 800, PulseTokens: 200,
		SourceEquivalentTokens: testContinuityInt(800), BaselineKind: "canonical_structured_resume_v1",
		CoverageCounted: 1, CoverageTotal: 1,
		SourceEventDigest: testContinuityDeliveryDigest("session-start-event"),
	}
	req.ContextID = continuityDeliveryContextID(req)
	req.IdempotencyKey = continuityDeliveryOfferIdempotencyKey(req)
	return req
}

func TestContinuityDeliveryContextIDMatchesCrossRuntimeGolden(t *testing.T) {
	req := ContinuityDeliveryOfferRequest{
		BindingDigest: strings.Repeat("b", 64), RepositoryID: "repository_pulse", Host: "codex",
		SessionRef: "session:" + strings.Repeat("a", 64), Purpose: ContinuityDeliveryPurposeSessionStart,
		SourceEventDigest: strings.Repeat("c", 64), PayloadDigest: strings.Repeat("d", 64),
	}
	const want = "context_232b4f5b68ede5a9b550eaffbfbc68f5a7df136945507d3c6cf31200ffc7de30"
	if got := continuityDeliveryContextID(req); got != want {
		t.Fatalf("context id=%q, want cross-runtime golden %q", got, want)
	}
	const wantIdempotency = "continuity-offer:81ea0e854b6eabe026cb188c8b240c49268a59398eb74d696fee0703f2913e93"
	if got := continuityDeliveryOfferIdempotencyKey(req); got != wantIdempotency {
		t.Fatalf("idempotency key=%q, want cross-runtime golden %q", got, wantIdempotency)
	}
}

func TestValidateContinuityOfferEnforcesExactNonEmptyPayloadBounds(t *testing.T) {
	tests := []struct {
		name          string
		renderedBytes int
		pulseTokens   int
		wantValid     bool
	}{
		{name: "empty", renderedBytes: 0, pulseTokens: 0, wantValid: false},
		{name: "one byte", renderedBytes: 1, pulseTokens: 1, wantValid: true},
		{name: "four bytes", renderedBytes: 4, pulseTokens: 1, wantValid: true},
		{name: "five bytes", renderedBytes: 5, pulseTokens: 2, wantValid: true},
		{name: "maximum", renderedBytes: 1 << 20, pulseTokens: 1 << 18, wantValid: true},
		{name: "over maximum", renderedBytes: (1 << 20) + 1, pulseTokens: (1 << 18) + 1, wantValid: false},
		{name: "mismatched ceil", renderedBytes: 5, pulseTokens: 1, wantValid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := testContinuityOfferRequest()
			req.RenderedBytes = test.renderedBytes
			req.PulseTokens = test.pulseTokens
			err := validateContinuityOffer(req)
			if test.wantValid && err != nil {
				t.Fatalf("valid boundary rejected: %v", err)
			}
			if !test.wantValid && !errors.Is(err, ErrContinuityDeliveryInvalid) {
				t.Fatalf("invalid boundary error=%v, want %v", err, ErrContinuityDeliveryInvalid)
			}
		})
	}
}

func TestMigration036AppliesOnlyToPersonalWithContentFreeClosedSchema(t *testing.T) {
	personal, err := OpenVault(filepath.Join(t.TempDir(), "personal.db"), StoreKindPersonal, "store_personal_delivery")
	if err != nil {
		t.Fatal(err)
	}
	defer personal.Close()

	var disposition string
	if err := personal.DB().QueryRow(`
		SELECT disposition FROM schema_migration_applicability WHERE version=36`,
	).Scan(&disposition); err != nil || disposition != "applied" {
		t.Fatalf("personal v36 disposition=%q err=%v", disposition, err)
	}
	for _, table := range []string{
		"continuity_delivery_receipts",
		"continuity_delivery_object_refs",
		"continuity_delivery_evidence_refs",
		"continuity_delivery_ref_seals",
	} {
		var exists int
		if err := personal.DB().QueryRow(`
			SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("personal table %s exists=%d err=%v", table, exists, err)
		}
	}
	for index, want := range map[string]string{
		"idx_continuity_delivery_memory_home":            "repository_id,binding_digest,context_id,receipt_seq",
		"idx_continuity_delivery_memory_home_recent":     "repository_id,binding_digest,purpose,receipt_state,receipt_seq,context_id",
		"idx_turn_ledgers_memory_home_binding":           "binding_digest,ledger_id",
		"idx_memory_write_receipts_candidate_latest":     "candidate_id",
		"idx_memory_write_receipts_memory_home_rejected": "ledger_id,created_at,receipt_id",
	} {
		indexRows, err := personal.DB().Query(`PRAGMA index_info(` + index + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var indexColumns []string
		for indexRows.Next() {
			var sequence, cid int
			var name string
			if err := indexRows.Scan(&sequence, &cid, &name); err != nil {
				indexRows.Close()
				t.Fatal(err)
			}
			indexColumns = append(indexColumns, name)
		}
		if err := indexRows.Close(); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(indexColumns, ","); got != want {
			t.Fatalf("index %s columns=%q, want %q", index, got, want)
		}
	}

	rows, err := personal.DB().Query(`PRAGMA table_info(continuity_delivery_receipts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, strings.ToLower(name))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(columns, " ")
	for _, forbidden := range []string{
		"content", "prompt", "transcript", "raw", "path", "payload_json", "idempotency_key ", "session_id",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("continuity delivery schema contains forbidden content-bearing column %q: %s", forbidden, joined)
		}
	}

	preview, err := Open(filepath.Join(t.TempDir(), "preview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	if err := preview.DB().QueryRow(`
		SELECT disposition FROM schema_migration_applicability WHERE version=36`,
	).Scan(&disposition); err != nil || disposition != "skipped" {
		t.Fatalf("preview v36 disposition=%q err=%v", disposition, err)
	}
	var exists int
	if err := preview.DB().QueryRow(`
		SELECT count(*) FROM sqlite_master WHERE type='table' AND name='continuity_delivery_receipts'`,
	).Scan(&exists); err != nil || exists != 0 {
		t.Fatalf("preview unexpectedly has delivery ledger: exists=%d err=%v", exists, err)
	}

	policy, ok := postFoundationMigrationPolicies[36]
	if !ok || policy.StoreKinds[StoreKindLocalPreview] || !policy.StoreKinds[StoreKindPersonal] {
		t.Fatalf("v36 policy is not Personal-only: %#v", policy.StoreKinds)
	}
}

func TestMigration038PreservesContinuityRowsChildRefsAndGuards(t *testing.T) {
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "continuity-v37.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE store_identity(
			singleton INTEGER PRIMARY KEY, store_id TEXT UNIQUE,
			min_reader_version INTEGER, min_writer_version INTEGER
		);
		INSERT INTO store_identity(singleton, store_id, min_reader_version, min_writer_version)
		VALUES (1, 'store_personal_delivery', 45, 45);
		CREATE TABLE turn_ledgers(binding_digest TEXT, ledger_id TEXT);
		CREATE TABLE memory_write_receipts(
			candidate_id TEXT, ledger_id TEXT, created_at TEXT, receipt_id TEXT, status TEXT
		);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[35].SQL); err != nil {
		t.Fatalf("apply frozen v36 fixture: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO continuity_delivery_receipts(
			receipt_id, context_id, receipt_state, purpose, store_id, repository_id,
			binding_digest, host, session_ref, payload_digest,
			object_ref_count, evidence_ref_count, refs_manifest_digest,
			method_id, method_version, rendered_bytes, pulse_tokens,
			coverage_counted, coverage_total, source_event_digest,
			idempotency_key_hash, operation_digest, created_at
		) VALUES (
			'delivery_old', 'context_old', 'offered_to_host', 'session_start',
			'store_personal_delivery', 'repository_pulse', ?, 'codex', ?, ?,
			1, 1, ?, 'utf8_bytes_div4_ceil', '1', 4, 1, 0, 0, ?, ?, ?, ?
		);
		INSERT INTO continuity_delivery_object_refs(receipt_id, ordinal, ref_id)
		VALUES ('delivery_old', 0, 'pulse:memory_old');
		INSERT INTO continuity_delivery_evidence_refs(receipt_id, ordinal, ref_id)
		VALUES ('delivery_old', 0, 'pulse:evidence_old');
		INSERT INTO continuity_delivery_ref_seals(receipt_id, refs_manifest_digest)
		VALUES ('delivery_old', ?);`,
		strings.Repeat("b", 64), testContinuitySessionRef("old cursor migration"), strings.Repeat("c", 64),
		strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64),
		strings.Repeat("a", 64), "2026-07-17T09:00:00Z", strings.Repeat("d", 64)); err != nil {
		t.Fatalf("seed v37 continuity proof: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(migrations[37].SQL); err != nil {
		tx.Rollback()
		t.Fatalf("apply v38 cursor upgrade: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v38 cursor upgrade: %v", err)
	}

	for table, want := range map[string]int{
		"continuity_delivery_receipts":      1,
		"continuity_delivery_object_refs":   1,
		"continuity_delivery_evidence_refs": 1,
		"continuity_delivery_ref_seals":     1,
	} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s rows=%d err=%v", table, count, err)
		}
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		rows.Close()
		t.Fatal("v38 left a broken child reference")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE continuity_delivery_receipts SET host='cursor' WHERE receipt_id='delivery_old'`); err == nil {
		t.Fatal("v38 lost immutable receipt guard")
	}
	if _, err := db.Exec(`
		INSERT INTO continuity_delivery_object_refs(receipt_id, ordinal, ref_id)
		VALUES ('delivery_old', 1, 'pulse:memory_late')`); err == nil {
		t.Fatal("v38 lost sealed child-ref guard")
	}
}

func TestRecordContinuityOfferPersistsExactContentFreeFactAndOpaqueSession(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	now := time.Date(2026, 7, 16, 10, 0, 0, 123, time.UTC)
	req := testContinuityOfferRequest()

	receipt, err := s.RecordContinuityOffer(context.Background(), req, now)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Schema != ContinuityDeliveryReceiptSchema || receipt.State != ContinuityDeliveryOfferedToHost ||
		receipt.Purpose != ContinuityDeliveryPurposeSessionStart ||
		receipt.ContextID != req.ContextID || receipt.ParentReceiptID != "" || receipt.StoreID != s.StoreID() ||
		receipt.RepositoryID != req.RepositoryID || receipt.BindingDigest != req.BindingDigest ||
		receipt.Host != req.Host || receipt.SessionRef != req.SessionRef || receipt.PayloadDigest != req.PayloadDigest ||
		receipt.ObjectRefCount != len(req.ObjectIDs) || receipt.EvidenceRefCount != len(req.EvidenceIDs) ||
		receipt.RefsManifestDigest != continuityDeliveryRefsManifestDigest(req.ObjectIDs, req.EvidenceIDs) ||
		receipt.MethodID != req.MethodID || receipt.MethodVersion != req.MethodVersion ||
		receipt.RenderedBytes != req.RenderedBytes || receipt.PulseTokens != req.PulseTokens ||
		receipt.SourceEquivalentTokens == nil || *receipt.SourceEquivalentTokens != 800 ||
		receipt.BaselineKind != req.BaselineKind || receipt.CoverageCounted != 1 || receipt.CoverageTotal != 1 ||
		receipt.SourceEventDigest != req.SourceEventDigest ||
		!testContinuityEqualIDs(receipt.ObjectIDs, req.ObjectIDs) || !testContinuityEqualIDs(receipt.EvidenceIDs, req.EvidenceIDs) {
		t.Fatalf("offer receipt lost exact fact: %#v", receipt)
	}
	if receipt.CreatedAt != now.Format(time.RFC3339Nano) || receipt.ReceiptID == "" {
		t.Fatalf("offer receipt identity/time=%#v", receipt)
	}

	for _, path := range []string{s.DBPath(), s.DBPath() + "-wal"} {
		bytes, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(bytes), "raw-session-must-not-persist") || strings.Contains(string(bytes), "exact final additionalContext bytes") {
			t.Fatalf("delivery ledger persisted raw session or payload bytes in %s", path)
		}
	}
}

func testContinuityObservationRequest(offer ContinuityDeliveryOfferRequest) ContinuityDeliveryObservationRequest {
	request := ContinuityDeliveryObservationRequest{
		ContextID:     offer.ContextID,
		BindingDigest: offer.BindingDigest, RepositoryID: offer.RepositoryID,
		Host: offer.Host, SessionRef: offer.SessionRef,
		SourceEventDigest: testContinuityDeliveryDigest("later-trusted-lifecycle-event"),
	}
	request.IdempotencyKey = continuityDeliveryObservationIdempotencyKey(request)
	return request
}

func TestContinuityObservationCopiesExactOfferAndTransitionsOnce(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	offerRequest := testContinuityOfferRequest()
	offer, err := s.RecordContinuityOffer(context.Background(), offerRequest, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	observedRequest := testContinuityObservationRequest(offerRequest)
	observed, err := s.RecordContinuityHostObserved(context.Background(), observedRequest, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if observed.State != ContinuityDeliveryHostObserved || observed.ParentReceiptID != offer.ReceiptID ||
		observed.Purpose != offer.Purpose || observed.StoreID != offer.StoreID ||
		observed.RepositoryID != offer.RepositoryID || observed.BindingDigest != offer.BindingDigest ||
		observed.Host != offer.Host || observed.SessionRef != offer.SessionRef ||
		observed.PayloadDigest != offer.PayloadDigest || observed.ObjectRefCount != offer.ObjectRefCount ||
		observed.EvidenceRefCount != offer.EvidenceRefCount || observed.RefsManifestDigest != offer.RefsManifestDigest ||
		observed.MethodID != offer.MethodID ||
		observed.MethodVersion != offer.MethodVersion || observed.RenderedBytes != offer.RenderedBytes ||
		observed.PulseTokens != offer.PulseTokens || !testContinuityEqualIDs(observed.ObjectIDs, offer.ObjectIDs) ||
		!testContinuityEqualIDs(observed.EvidenceIDs, offer.EvidenceIDs) {
		t.Fatalf("observation did not copy exact offer: offer=%#v observed=%#v", offer, observed)
	}
	replay, err := s.RecordContinuityHostObserved(context.Background(), observedRequest, time.Now().Add(2*time.Second))
	if err != nil || replay.ReceiptID != observed.ReceiptID || replay.CreatedAt != observed.CreatedAt {
		t.Fatalf("exact observation replay=%#v err=%v", replay, err)
	}
}

func TestCursorContinuityOffersAndAcknowledgementsNeverBecomeProviderMeasurements(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	offerRequest := testContinuityOfferRequest()
	offerRequest.Host = "cursor"
	offerRequest.ContextID = continuityDeliveryContextID(offerRequest)
	offerRequest.IdempotencyKey = continuityDeliveryOfferIdempotencyKey(offerRequest)

	offer, err := s.RecordContinuityOffer(context.Background(), offerRequest, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	observedRequest := testContinuityObservationRequest(offerRequest)
	observed, err := s.RecordContinuityHostObserved(
		context.Background(), observedRequest, time.Now().Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if offer.Host != "cursor" || observed.Host != "cursor" ||
		offer.State != ContinuityDeliveryOfferedToHost || observed.State != ContinuityDeliveryHostObserved {
		t.Fatalf("cursor delivery receipts=%#v %#v", offer, observed)
	}

	provider := continuityProviderMeasurementRequest{
		ContextID: offerRequest.ContextID, IdempotencyKey: "cursor_provider_measurement_forbidden",
		BindingDigest: offerRequest.BindingDigest, RepositoryID: offerRequest.RepositoryID,
		Host: offerRequest.Host, SessionRef: offerRequest.SessionRef,
		ProviderActualInputTokens: 500, ProviderActualSource: "cursor_provider_usage_v1",
		ProviderEvidenceDigest: testContinuityDeliveryDigest("invented cursor provider receipt"),
	}
	if _, err := s.recordContinuityProviderMeasurement(context.Background(), provider, time.Now()); !errors.Is(err, ErrContinuityDeliveryInvalid) {
		t.Fatalf("cursor provider measurement err=%v, want %v", err, ErrContinuityDeliveryInvalid)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO continuity_delivery_receipts(
			receipt_id, context_id, parent_receipt_id, receipt_state, purpose,
			store_id, repository_id, binding_digest, host, session_ref, payload_digest,
			object_ref_count, evidence_ref_count, refs_manifest_digest, method_id, method_version,
			rendered_bytes, pulse_tokens, baseline_kind, source_equivalent_tokens,
			coverage_counted, coverage_total, source_event_digest,
			provider_actual_input_tokens, provider_actual_source, provider_evidence_digest,
			idempotency_key_hash, operation_digest, created_at
		)
		SELECT
			'delivery_cursor_forged', context_id, receipt_id, 'provider_measurement', purpose,
			store_id, repository_id, binding_digest, host, session_ref, payload_digest,
			object_ref_count, evidence_ref_count, refs_manifest_digest, method_id, method_version,
			rendered_bytes, pulse_tokens, baseline_kind, source_equivalent_tokens,
			coverage_counted, coverage_total, NULL,
			500, 'codex_provider_usage_v1', ?, ?, ?, ?
		  FROM continuity_delivery_receipts WHERE receipt_id=?`,
		testContinuityDeliveryDigest("forged cursor provider evidence"),
		testContinuityDeliveryDigest("forged cursor idempotency"),
		testContinuityDeliveryDigest("forged cursor operation"),
		"2026-07-17T10:00:00Z", observed.ReceiptID,
	); err == nil {
		t.Fatal("SQLite admitted a Cursor provider measurement mislabeled as Codex evidence")
	}
}

func TestContinuityObservationRequiresASeparateLaterLifecycleEvent(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	offerRequest := testContinuityOfferRequest()
	if _, err := s.RecordContinuityOffer(context.Background(), offerRequest, time.Now()); err != nil {
		t.Fatal(err)
	}
	observation := testContinuityObservationRequest(offerRequest)
	observation.SourceEventDigest = offerRequest.SourceEventDigest
	observation.IdempotencyKey = continuityDeliveryObservationIdempotencyKey(observation)
	if _, err := s.RecordContinuityHostObserved(
		context.Background(), observation, time.Now().Add(time.Second),
	); !errors.Is(err, ErrContinuityDeliveryTransition) {
		t.Fatalf("SessionStart event observed itself: %v", err)
	}
}

func TestContinuityDeliveryRejectsInvalidTransitionsAuthorityAndMeasurement(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	offerRequest := testContinuityOfferRequest()
	observation := testContinuityObservationRequest(offerRequest)
	if _, err := s.RecordContinuityHostObserved(context.Background(), observation, time.Now()); !errors.Is(err, ErrContinuityDeliveryTransition) {
		t.Fatalf("observation before offer err=%v", err)
	}

	invalidMethod := offerRequest
	invalidMethod.MethodID = "arbitrary_tokenizer"
	if _, err := s.RecordContinuityOffer(context.Background(), invalidMethod, time.Now()); !errors.Is(err, ErrContinuityDeliveryInvalid) {
		t.Fatalf("arbitrary method err=%v", err)
	}
	invalidCount := offerRequest
	invalidCount.PulseTokens++
	if _, err := s.RecordContinuityOffer(context.Background(), invalidCount, time.Now()); !errors.Is(err, ErrContinuityDeliveryInvalid) {
		t.Fatalf("inexact token count err=%v", err)
	}
	invalidCoverage := offerRequest
	invalidCoverage.IdempotencyKey = "delivery_offer_invalid_coverage"
	invalidCoverage.CoverageCounted = 0
	if _, err := s.RecordContinuityOffer(context.Background(), invalidCoverage, time.Now()); !errors.Is(err, ErrContinuityDeliveryInvalid) {
		t.Fatalf("zero counted baseline coverage err=%v", err)
	}
	rawSession := offerRequest
	rawSession.SessionRef = "raw-session"
	rawSession.IdempotencyKey = continuityDeliveryOfferIdempotencyKey(rawSession)
	if _, err := s.RecordContinuityOffer(context.Background(), rawSession, time.Now()); !errors.Is(err, ErrContinuityDeliveryInvalid) {
		t.Fatalf("raw session err=%v", err)
	}
	secretRef := offerRequest
	secretRef.ObjectIDs = []string{"sk-secret-looking-id"}
	if _, err := s.RecordContinuityOffer(context.Background(), secretRef, time.Now()); !errors.Is(err, ErrContinuityDeliveryInvalid) {
		t.Fatalf("secret-like ref err=%v", err)
	}
	tooMany := offerRequest
	tooMany.EvidenceIDs = make([]string, 65)
	for index := range tooMany.EvidenceIDs {
		tooMany.EvidenceIDs[index] = fmt.Sprintf("evidence_%02d", index)
	}
	if _, err := s.RecordContinuityOffer(context.Background(), tooMany, time.Now()); !errors.Is(err, ErrContinuityDeliveryInvalid) {
		t.Fatalf("oversized refs err=%v", err)
	}
	unsorted := offerRequest
	unsorted.IdempotencyKey = "delivery_offer_unsorted"
	unsorted.ObjectIDs = []string{"pulse:memory_02", "pulse:memory_01"}
	unsorted.ContextID = continuityDeliveryContextID(unsorted)
	if _, err := s.RecordContinuityOffer(context.Background(), unsorted, time.Now()); !errors.Is(err, ErrContinuityDeliveryInvalid) {
		t.Fatalf("non-canonical refs err=%v", err)
	}
	inventedContext := offerRequest
	inventedContext.IdempotencyKey = "delivery_offer_invented_context"
	inventedContext.ContextID = "context_invented"
	if _, err := s.RecordContinuityOffer(context.Background(), inventedContext, time.Now()); !errors.Is(err, ErrContinuityDeliveryInvalid) {
		t.Fatalf("invented context id err=%v", err)
	}
	wrongRepository := offerRequest
	wrongRepository.RepositoryID = "repository_other"
	wrongRepository.ContextID = continuityDeliveryContextID(wrongRepository)
	wrongRepository.IdempotencyKey = continuityDeliveryOfferIdempotencyKey(wrongRepository)
	if _, err := s.RecordContinuityOffer(context.Background(), wrongRepository, time.Now()); !errors.Is(err, ErrContinuityDeliveryAuthority) {
		t.Fatalf("wrong repository err=%v", err)
	}
}

func TestContinuityOfferIdempotencyIsExactAndConcurrent(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	req := testContinuityOfferRequest()
	const workers = 12
	type result struct {
		receipt ContinuityDeliveryReceipt
		err     error
	}
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := s.RecordContinuityOffer(context.Background(), req, time.Now())
			results <- result{receipt: receipt, err: err}
		}()
	}
	wg.Wait()
	close(results)
	var receiptID string
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent offer: %v", result.err)
		}
		if receiptID == "" {
			receiptID = result.receipt.ReceiptID
		} else if result.receipt.ReceiptID != receiptID {
			t.Fatalf("concurrent replay split receipts: %q != %q", result.receipt.ReceiptID, receiptID)
		}
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM continuity_delivery_receipts WHERE receipt_state='offered_to_host'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("offer rows=%d err=%v", count, err)
	}
	conflict := req
	conflict.PayloadDigest = testContinuityDeliveryDigest("changed payload")
	conflict.ContextID = continuityDeliveryContextID(conflict)
	if _, err := s.RecordContinuityOffer(context.Background(), conflict, time.Now()); !errors.Is(err, ErrContinuityDeliveryIdempotencyConflict) {
		t.Fatalf("same idempotency different operation err=%v", err)
	}
}

func TestContinuityProviderMeasurementRequiresObservedParentAndVerifiedSource(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	offerRequest := testContinuityOfferRequest()
	provider := continuityProviderMeasurementRequest{
		ContextID: offerRequest.ContextID, IdempotencyKey: "provider_measurement_01",
		BindingDigest: offerRequest.BindingDigest, RepositoryID: offerRequest.RepositoryID,
		Host: offerRequest.Host, SessionRef: offerRequest.SessionRef,
		ProviderActualInputTokens: 1200, ProviderActualSource: "codex_provider_usage_v1",
		ProviderEvidenceDigest: testContinuityDeliveryDigest("provider receipt"),
	}
	if _, err := s.recordContinuityProviderMeasurement(context.Background(), provider, time.Now()); !errors.Is(err, ErrContinuityDeliveryTransition) {
		t.Fatalf("provider before observed err=%v", err)
	}
	if _, err := s.RecordContinuityOffer(context.Background(), offerRequest, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordContinuityHostObserved(context.Background(), testContinuityObservationRequest(offerRequest), time.Now()); err != nil {
		t.Fatal(err)
	}
	measurement, err := s.recordContinuityProviderMeasurement(context.Background(), provider, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if measurement.State != continuityDeliveryProviderMeasurement || measurement.ProviderActualInputTokens == nil ||
		*measurement.ProviderActualInputTokens != 1200 || measurement.ProviderActualSource != provider.ProviderActualSource ||
		measurement.ProviderEvidenceDigest != provider.ProviderEvidenceDigest || measurement.ParentReceiptID == "" {
		t.Fatalf("provider measurement=%#v", measurement)
	}
	wrongSource := provider
	wrongSource.IdempotencyKey = "provider_measurement_wrong"
	wrongSource.ProviderActualSource = "caller_claimed_actual"
	if _, err := s.recordContinuityProviderMeasurement(context.Background(), wrongSource, time.Now()); !errors.Is(err, ErrContinuityDeliveryInvalid) {
		t.Fatalf("unverified provider source err=%v", err)
	}
}

func TestContinuityDeliveryRowsAndRefsAreAppendOnly(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	receipt, err := s.RecordContinuityOffer(context.Background(), testContinuityOfferRequest(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE continuity_delivery_receipts SET host='claude-code' WHERE receipt_id=?`, receipt.ReceiptID); err == nil {
		t.Fatal("delivery receipt update succeeded")
	}
	if _, err := s.DB().Exec(`DELETE FROM continuity_delivery_object_refs WHERE receipt_id=?`, receipt.ReceiptID); err == nil {
		t.Fatal("delivery ref delete succeeded")
	}
	if _, err := s.DB().Exec(`DELETE FROM continuity_delivery_receipts WHERE receipt_id=?`, receipt.ReceiptID); err == nil {
		t.Fatal("delivery receipt delete succeeded")
	}
	if _, err := s.DB().Exec(`
		INSERT INTO continuity_delivery_object_refs(receipt_id, ordinal, ref_id)
		VALUES (?, 1, 'pulse:memory_late')`, receipt.ReceiptID); err == nil {
		t.Fatal("late delivery ref insert mutated a sealed receipt")
	}
}

func TestContinuityDeliveryReaderRejectsAnUnsealedManifest(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	req := testContinuityOfferRequest()
	receipt, err := s.RecordContinuityOffer(context.Background(), req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DROP TRIGGER continuity_delivery_ref_seals_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM continuity_delivery_ref_seals WHERE receipt_id=?`, receipt.ReceiptID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadMemoryHomeDeliveryFacts(req.RepositoryID, req.BindingDigest, 10); !errors.Is(err, ErrContinuityDeliveryTransition) {
		t.Fatalf("unsealed delivery manifest read err=%v", err)
	}
}

func TestReadMemoryHomeDeliveryFactsReturnsBoundOffersAndObservedProviderEvidence(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	req := testContinuityOfferRequest()
	at := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	offer, err := s.RecordContinuityOffer(context.Background(), req, at)
	if err != nil {
		t.Fatal(err)
	}
	observedRequest := testContinuityObservationRequest(req)
	observed, err := s.RecordContinuityHostObserved(context.Background(), observedRequest, at)
	if err != nil {
		t.Fatal(err)
	}
	provider := continuityProviderMeasurementRequest{
		ContextID: req.ContextID, IdempotencyKey: "provider_measurement_read_01",
		BindingDigest: req.BindingDigest, RepositoryID: req.RepositoryID,
		Host: req.Host, SessionRef: req.SessionRef,
		ProviderActualInputTokens: 500, ProviderActualSource: "codex_provider_usage_v1",
		ProviderEvidenceDigest: testContinuityDeliveryDigest("verified provider receipt"),
	}
	if _, err := s.recordContinuityProviderMeasurement(context.Background(), provider, at); err != nil {
		t.Fatal(err)
	}
	second := testContinuityOfferRequest()
	second.SessionRef = testContinuitySessionRef("second delivery context")
	second.SourceEventDigest = testContinuityDeliveryDigest("second delivery event")
	second.ContextID = continuityDeliveryContextID(second)
	second.IdempotencyKey = continuityDeliveryOfferIdempotencyKey(second)
	secondOffer, err := s.RecordContinuityOffer(context.Background(), second, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	facts, err := s.ReadMemoryHomeDeliveryFacts(req.RepositoryID, req.BindingDigest, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 3 {
		t.Fatalf("facts=%#v, want two offers+observed", facts)
	}
	var gotOffer, gotSecondOffer, gotObserved *MemoryHomeDeliveryFact
	for index := range facts {
		switch facts[index].Acknowledgement {
		case MemoryHomeDeliveryOfferedToHost:
			if facts[index].ReceiptID == secondOffer.ReceiptID {
				gotSecondOffer = &facts[index]
			} else {
				gotOffer = &facts[index]
			}
		case MemoryHomeDeliveryHostObserved:
			gotObserved = &facts[index]
		}
	}
	if gotOffer == nil || gotSecondOffer == nil || gotObserved == nil || gotOffer.ReceiptID != offer.ReceiptID ||
		!slices.Equal(gotSecondOffer.ObjectIDs, second.ObjectIDs) || !slices.Equal(gotSecondOffer.EvidenceIDs, second.EvidenceIDs) ||
		gotObserved.ReceiptID != observed.ReceiptID || !memoryHomeTimeAfter(gotObserved.CreatedAt, gotOffer.CreatedAt) ||
		!gotObserved.ProviderEvidenceVerified || gotObserved.ProviderActualInputTokens != 500 ||
		gotObserved.ProviderActualSource != provider.ProviderActualSource ||
		gotObserved.ProviderEvidenceDigest != provider.ProviderEvidenceDigest {
		t.Fatalf("memory home facts did not preserve proof: offer=%#v observed=%#v", gotOffer, gotObserved)
	}
	if _, err := s.ReadMemoryHomeDeliveryFacts("repository_other", req.BindingDigest, 10); !errors.Is(err, ErrContinuityDeliveryAuthority) {
		t.Fatalf("cross-repository delivery read err=%v", err)
	}
}
