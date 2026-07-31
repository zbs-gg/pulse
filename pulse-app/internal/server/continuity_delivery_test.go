package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

func continuityDeliveryServerFixture(t *testing.T) (*store.Store, *httptest.Server, string, string) {
	t.Helper()
	binding := strings.Repeat("a", 64)
	repository := "repository_pulse"
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "personal.db"), store.StoreKindPersonal, "store_personal_delivery_server")
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureProductRuntimeAuthority(binding, 2, 4); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, repository); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); _ = vault.Close() })
	return vault, ts, binding, repository
}

func continuityDeliveryHash(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func continuityDeliveryOfferBodyFixture(binding, repository string) map[string]any {
	sessionRef := "session:" + continuityDeliveryHash("session-01")
	sourceEventDigest := continuityDeliveryHash("event-01")
	payloadDigest := continuityDeliveryHash("exact additionalContext")
	contextID := "context_" + continuityDeliveryHash(strings.Join([]string{
		"pulse-continuity-context-v1", binding, repository, "codex", sessionRef,
		"session_start", sourceEventDigest, payloadDigest,
	}, "\x1f"))
	return map[string]any{
		"schema": "pulse.continuity_delivery.v1", "context_id": contextID, "purpose": "session_start",
		"binding_digest": binding, "repository_id": repository, "host": "codex",
		"session_ref": sessionRef, "source_event_digest": sourceEventDigest,
		"payload_digest": payloadDigest,
		"object_ids":     []string{"pulse:memory_01"}, "evidence_ids": []string{"pulse:pulse:memory_01"},
		"method_id": "utf8_bytes_div4_ceil", "method_version": "1",
		"rendered_bytes": 800, "pulse_tokens": 200,
		"coverage_counted": 0, "coverage_total": 0,
	}
}

func continuityDeliveryOfferKey(body map[string]any) string {
	return "continuity-offer:" + continuityDeliveryHash(strings.Join([]string{
		body["schema"].(string), body["purpose"].(string), body["binding_digest"].(string),
		body["repository_id"].(string), body["host"].(string), body["session_ref"].(string),
		body["source_event_digest"].(string),
	}, "\x1f"))
}

func continuityDeliveryObservationKey(body map[string]any) string {
	return "continuity-observation:" + continuityDeliveryHash(strings.Join([]string{
		body["schema"].(string), body["context_id"].(string), body["binding_digest"].(string),
		body["repository_id"].(string), body["host"].(string), body["session_ref"].(string),
		body["source_event_digest"].(string),
	}, "\x1f"))
}

func TestContinuityDeliveryOfferRouteAcceptsOnlyCompleteComparableBaseline(t *testing.T) {
	_, ts, binding, repository := continuityDeliveryServerFixture(t)
	body := continuityDeliveryOfferBodyFixture(binding, repository)
	body["baseline_kind"] = "canonical_structured_resume_v1"
	body["source_equivalent_tokens"] = 320
	body["coverage_counted"] = 2
	body["coverage_total"] = 3
	response := pulseJSONWithIdempotency(t, ts, http.MethodPost, "/continuity/delivery/offers", body, continuityDeliveryOfferKey(body))
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		t.Fatalf("complete baseline status=%d", response.StatusCode)
	}
	receipt := decodeContinuityDeliveryReceipt(t, response)
	if receipt.BaselineKind != "canonical_structured_resume_v1" || receipt.SourceEquivalentTokens == nil ||
		*receipt.SourceEquivalentTokens != 320 || receipt.CoverageCounted != 2 || receipt.CoverageTotal != 3 {
		t.Fatalf("baseline receipt mismatch: %#v", receipt)
	}

	incomplete := continuityDeliveryOfferBodyFixture(binding, repository)
	incomplete["baseline_kind"] = "canonical_structured_resume_v1"
	incomplete["coverage_counted"] = 1
	incomplete["coverage_total"] = 1
	response = pulseJSONWithIdempotency(t, ts, http.MethodPost, "/continuity/delivery/offers", incomplete, "offer-baseline-02")
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("incomplete baseline status=%d", response.StatusCode)
	}
}

func decodeContinuityDeliveryReceipt(t *testing.T, response *http.Response) store.ContinuityDeliveryReceipt {
	t.Helper()
	defer response.Body.Close()
	var receipt store.ContinuityDeliveryReceipt
	if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestContinuityDeliveryOfferRouteRecordsExactIdempotentContentFreeReceipt(t *testing.T) {
	_, ts, binding, repository := continuityDeliveryServerFixture(t)
	body := continuityDeliveryOfferBodyFixture(binding, repository)
	key := continuityDeliveryOfferKey(body)
	firstResponse := pulseJSONWithIdempotency(t, ts, http.MethodPost, "/continuity/delivery/offers", body, key)
	if firstResponse.StatusCode != http.StatusOK {
		defer firstResponse.Body.Close()
		t.Fatalf("status=%d", firstResponse.StatusCode)
	}
	first := decodeContinuityDeliveryReceipt(t, firstResponse)
	second := decodeContinuityDeliveryReceipt(t, pulseJSONWithIdempotency(
		t, ts, http.MethodPost, "/continuity/delivery/offers", body, key,
	))
	if first.ReceiptID == "" || second.ReceiptID != first.ReceiptID || first.State != store.ContinuityDeliveryOfferedToHost ||
		first.SessionRef != body["session_ref"] || first.PayloadDigest != body["payload_digest"] {
		t.Fatalf("offer receipt mismatch: first=%#v second=%#v", first, second)
	}
}

func TestContinuityDeliveryObservationRoutePromotesOnlyTheExactLaterHostSession(t *testing.T) {
	_, ts, binding, repository := continuityDeliveryServerFixture(t)
	offerBody := continuityDeliveryOfferBodyFixture(binding, repository)
	offer := decodeContinuityDeliveryReceipt(t, pulseJSONWithIdempotency(
		t, ts, http.MethodPost, "/continuity/delivery/offers", offerBody, continuityDeliveryOfferKey(offerBody),
	))
	observation := map[string]any{
		"schema": "pulse.continuity_delivery_observation.v1", "context_id": offer.ContextID,
		"binding_digest": binding, "repository_id": repository, "host": offer.Host,
		"session_ref": offer.SessionRef, "source_event_digest": continuityDeliveryHash("later-prompt-event"),
	}
	key := continuityDeliveryObservationKey(observation)
	first := decodeContinuityDeliveryReceipt(t, pulseJSONWithIdempotency(
		t, ts, http.MethodPost, "/continuity/delivery/observations", observation, key,
	))
	second := decodeContinuityDeliveryReceipt(t, pulseJSONWithIdempotency(
		t, ts, http.MethodPost, "/continuity/delivery/observations", observation, key,
	))
	if first.State != store.ContinuityDeliveryHostObserved || first.ParentReceiptID != offer.ReceiptID ||
		first.ContextID != offer.ContextID || first.Host != offer.Host || first.SessionRef != offer.SessionRef ||
		second.ReceiptID != first.ReceiptID {
		t.Fatalf("observation receipt mismatch: offer=%#v first=%#v second=%#v", offer, first, second)
	}
}

func TestContinuityDeliveryObservationRouteRejectsForgedOrContentShapedFacts(t *testing.T) {
	vault, ts, binding, repository := continuityDeliveryServerFixture(t)
	offerBody := continuityDeliveryOfferBodyFixture(binding, repository)
	offer := decodeContinuityDeliveryReceipt(t, pulseJSONWithIdempotency(
		t, ts, http.MethodPost, "/continuity/delivery/offers", offerBody, continuityDeliveryOfferKey(offerBody),
	))
	observation := map[string]any{
		"schema": "pulse.continuity_delivery_observation.v1", "context_id": offer.ContextID,
		"binding_digest": binding, "repository_id": repository, "host": "cursor",
		"session_ref": offer.SessionRef, "source_event_digest": continuityDeliveryHash("later-prompt-event"),
	}
	response := pulseJSONWithIdempotency(
		t, ts, http.MethodPost, "/continuity/delivery/observations", observation, continuityDeliveryObservationKey(observation),
	)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("forged host status=%d", response.StatusCode)
	}
	observation["host"] = offer.Host
	observation["prompt"] = "must never enter the continuity ledger"
	response = pulseJSONWithIdempotency(
		t, ts, http.MethodPost, "/continuity/delivery/observations", observation, continuityDeliveryObservationKey(observation),
	)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("content-shaped observation status=%d", response.StatusCode)
	}
	var observed int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM continuity_delivery_receipts WHERE receipt_state='host_observed'`).Scan(&observed); err != nil || observed != 0 {
		t.Fatalf("rejected observations mutated ledger: count=%d err=%v", observed, err)
	}
}

func TestContinuityDeliveryOfferRouteRejectsAuthorityDriftAndContentShapedFields(t *testing.T) {
	vault, ts, binding, repository := continuityDeliveryServerFixture(t)
	badAuthority := continuityDeliveryOfferBodyFixture(binding, "repository_other")
	response := pulseJSONWithIdempotency(t, ts, http.MethodPost, "/continuity/delivery/offers", badAuthority, continuityDeliveryOfferKey(badAuthority))
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong repository status=%d", response.StatusCode)
	}

	contentBody := continuityDeliveryOfferBodyFixture(binding, repository)
	contentBody["prompt"] = "this content must never enter the ledger"
	response = pulseJSONWithIdempotency(t, ts, http.MethodPost, "/continuity/delivery/offers", contentBody, "offer-event-03")
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown content field status=%d", response.StatusCode)
	}
	var count int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM continuity_delivery_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected delivery mutated ledger: count=%d err=%v", count, err)
	}
}

func TestContinuityDeliveryOfferRouteCannotMintObservation(t *testing.T) {
	_, ts, binding, repository := continuityDeliveryServerFixture(t)
	body := continuityDeliveryOfferBodyFixture(binding, repository)
	body["acknowledgement"] = "host_observed"
	response := pulseJSONWithIdempotency(t, ts, http.MethodPost, "/continuity/delivery/offers", body, "offer-event-04")
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("caller-selected acknowledgement status=%d", response.StatusCode)
	}
}

func TestContinuityDeliveryOfferRouteReturnsServerErrorForClosedStore(t *testing.T) {
	vault, ts, binding, repository := continuityDeliveryServerFixture(t)
	body := continuityDeliveryOfferBodyFixture(binding, repository)
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	response := pulseJSONWithIdempotency(
		t, ts, http.MethodPost, "/continuity/delivery/offers", body, continuityDeliveryOfferKey(body),
	)
	defer response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("closed store status=%d, want %d", response.StatusCode, http.StatusInternalServerError)
	}
}
