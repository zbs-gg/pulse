package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

type productTestEmbedder struct{}

func (productTestEmbedder) Embed(_ context.Context, texts []string, _ embed.InputType) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vectors[index] = []float32{1, 0, 0, 0}
	}
	return vectors, nil
}

func (productTestEmbedder) Model() string { return "product-test-embedder" }

type flakyProductTestEmbedder struct {
	mu    sync.Mutex
	calls int
}

func (embedder *flakyProductTestEmbedder) Embed(_ context.Context, texts []string, _ embed.InputType) ([][]float32, error) {
	embedder.mu.Lock()
	embedder.calls++
	call := embedder.calls
	embedder.mu.Unlock()
	if call == 1 {
		return nil, errors.New("transient embedder failure")
	}
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vectors[index] = []float32{1, 0, 0, 0}
	}
	return vectors, nil
}

func (*flakyProductTestEmbedder) Model() string { return "flaky-product-test-embedder" }

func newProductMemoryServer(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
	vault, err := store.OpenVault(
		filepath.Join(t.TempDir(), "desk.db"), store.StoreKindDesk, "store_desk_server_test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureProductRuntimeAuthority(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2, 4,
	); err != nil {
		vault.Close()
		t.Fatal(err)
	}
	srv, err := New(Config{
		IPCSecret: "secret", Store: vault,
		TrayGracePeriod: 10 * time.Second,
		Billing:         BillingStatus{Mode: "host-extracted", Host: "codex"},
	})
	if err != nil {
		vault.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	return vault, httptest.NewServer(srv.Handler())
}

func presentProductCandidate(t *testing.T, vault *store.Store, receipt store.MemoryWriteReceipt, now time.Time, grace time.Duration) {
	t.Helper()
	var bindingDigest string
	if err := vault.DB().QueryRow(`
		SELECT ledger.binding_digest
		  FROM memory_tray_candidates candidate
		  JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
		 WHERE candidate.candidate_id=?`,
		receipt.CandidateID,
	).Scan(&bindingDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.PresentMemoryTrayCandidate(store.MemoryPresentationRequest{
		CandidateID: receipt.CandidateID, CandidateVersion: receipt.CandidateVersion,
		ContentDigest: receipt.ContentDigest, BindingDigest: bindingDigest,
		TrustedSurfaceKind: "memory_home", TrustedSurfaceInstance: "test_home_" + receipt.CandidateID,
	}, now, grace); err != nil {
		t.Fatal(err)
	}
}

func TestTurnFinalizeTrayListAndCancelRoutes(t *testing.T) {
	_, ts := newProductMemoryServer(t)
	defer ts.Close()
	req := store.TurnFinalizeRequest{
		Schema: store.TurnFinalizeRequestSchema,
		Host:   "codex", SessionID: "session_route", TurnID: "turn_route",
		SourceEventKey: "codex:session_route:turn_route:stop", IdempotencyKey: "finalize_route",
		BindingDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PolicyEpoch: 2, ResolverEpoch: 4,
		Candidates: []store.PrivateMemoryCandidate{{
			Kind: store.PrivateMemoryCandidateCapsule,
			Capsule: &store.MemoryCapsule{
				Schema: store.MemoryCapsuleSchema,
				Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: "2026-07-14T09:00:00Z"},
				Items: []store.MemoryCapsuleItem{{
					Kind: "decision", RedactedSummary: "Show every private write before canonical commit.",
					Confidence: 0.97, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
				}},
			},
		}},
	}
	resp := pulseJSON(t, ts, http.MethodPost, "/turn/finalize", req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("finalize status=%d", resp.StatusCode)
	}
	var finalized store.TurnFinalizeResult
	if err := json.NewDecoder(resp.Body).Decode(&finalized); err != nil {
		t.Fatal(err)
	}
	if len(finalized.Receipts) != 1 || finalized.Receipts[0].Status != store.MemoryWritePending || finalized.Receipts[0].ObjectID != "" {
		t.Fatalf("finalize response: %#v", finalized)
	}

	resp = pulseJSON(t, ts, http.MethodGet, "/memory/tray", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tray status=%d", resp.StatusCode)
	}
	var tray struct {
		Candidates []store.MemoryTrayCandidateView `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tray); err != nil {
		t.Fatal(err)
	}
	if len(tray.Candidates) != 1 || tray.Candidates[0].Candidate.Capsule == nil ||
		tray.Candidates[0].Candidate.Capsule.Items[0].RedactedSummary != "Show every private write before canonical commit." {
		t.Fatalf("tray must show exact candidate: %#v", tray)
	}

	receipt := finalized.Receipts[0]
	resp = pulseJSON(t, ts, http.MethodPost, "/memory/tray/"+receipt.CandidateID+"/cancel", map[string]any{
		"expected_version": receipt.CandidateVersion,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status=%d", resp.StatusCode)
	}
	var canceled store.MemoryWriteReceipt
	if err := json.NewDecoder(resp.Body).Decode(&canceled); err != nil {
		t.Fatal(err)
	}
	if canceled.Status != store.MemoryWriteCanceled || canceled.ObjectID != "" {
		t.Fatalf("cancel receipt: %#v", canceled)
	}
}

func TestProductFinalizeRejectsUnknownAuthorityFieldsBeforeLedgerPersistence(t *testing.T) {
	vault, ts := newProductMemoryServer(t)
	defer ts.Close()
	body := []byte(`{
		"schema":"pulse.turn_finalize.v1","host":"codex","session_id":"session_strict","turn_id":"turn_strict",
		"source_event_key":"codex:session_strict:turn_strict:stop",
		"idempotency_key":"strict_01","binding_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"policy_epoch":1,"resolver_epoch":1,"team_id":"attacker_selected_team",
		"candidates":[]
	}`)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/turn/finalize", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Pulse-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown authority field status=%d", resp.StatusCode)
	}
	var ledgers int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM turn_ledgers`).Scan(&ledgers); err != nil || ledgers != 0 {
		t.Fatalf("unknown authority field reached ledger: count=%d err=%v", ledgers, err)
	}
}

func TestLegacyManualRoutesEnterTrayOnProductStore(t *testing.T) {
	vault, ts := newProductMemoryServer(t)
	defer ts.Close()
	capsule := map[string]any{
		"schema": store.MemoryCapsuleSchema,
		"source": map[string]any{"host": "codex", "conversation_scope": "current_turn", "timestamp": "2026-07-14T09:00:00Z"},
		"items": []map[string]any{{
			"kind": "decision", "redacted_summary": "Manual memory tools also enter the private Tray.",
			"confidence": 0.95, "evidence_hint": "current_turn", "privacy_tier": "normal", "retention": "project",
		}},
		"raw_input_included": false,
	}
	resp := pulseJSON(t, ts, http.MethodPost, "/memory/remember", capsule)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manual remember status=%d", resp.StatusCode)
	}
	var result store.TurnFinalizeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Receipts) != 1 || result.Receipts[0].Status != store.MemoryWritePending || result.Receipts[0].ObjectID != "" {
		t.Fatalf("manual write bypassed Tray: %#v", result)
	}
	var capsules int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&capsules); err != nil || capsules != 0 {
		t.Fatalf("manual pending write became canonical: count=%d err=%v", capsules, err)
	}

	resp = pulseJSON(t, ts, http.MethodPost, "/graph/delta", semanticDeltaBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manual semantic status=%d", resp.StatusCode)
	}
	result = store.TurnFinalizeResult{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Receipts) != 1 || result.Receipts[0].Status != store.MemoryWritePending {
		t.Fatalf("semantic write bypassed Tray: %#v", result)
	}
	var graphEvents int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM events WHERE scorer_version='host-extracted'`).Scan(&graphEvents); err != nil || graphEvents != 0 {
		t.Fatalf("pending semantic delta reached graph: count=%d err=%v", graphEvents, err)
	}
}

func TestManualHTTPIdempotencySurvivesLostResponseWithoutMakingCancelPermanent(t *testing.T) {
	_, ts := newProductMemoryServer(t)
	defer ts.Close()
	capsule := map[string]any{
		"schema": store.MemoryCapsuleSchema,
		"source": map[string]any{"host": "codex", "conversation_scope": "current_turn", "timestamp": "2026-07-14T09:00:00Z"},
		"items": []map[string]any{{
			"kind": "decision", "redacted_summary": "A lost HTTP response must converge on the same visible candidate.",
			"confidence": 1.0, "evidence_hint": "current_turn", "privacy_tier": "private", "retention": "project",
		}},
		"raw_input_included": false,
	}
	decode := func(response *http.Response) store.TurnFinalizeResult {
		t.Helper()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("manual response status=%d", response.StatusCode)
		}
		var result store.TurnFinalizeResult
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := decode(pulseJSONWithIdempotency(t, ts, http.MethodPost, "/memory/remember", capsule, "host_invocation_01"))
	retry := decode(pulseJSONWithIdempotency(t, ts, http.MethodPost, "/memory/remember", capsule, "host_invocation_01"))
	if first.LedgerID != retry.LedgerID || first.Receipts[0].CandidateID != retry.Receipts[0].CandidateID {
		t.Fatalf("lost-response retry diverged: first=%#v retry=%#v", first, retry)
	}
	canceled := pulseJSON(t, ts, http.MethodPost, "/memory/tray/"+first.Receipts[0].CandidateID+"/cancel", map[string]any{"expected_version": 1})
	if canceled.StatusCode != http.StatusOK {
		t.Fatalf("cancel status=%d", canceled.StatusCode)
	}
	second := decode(pulseJSONWithIdempotency(t, ts, http.MethodPost, "/memory/remember", capsule, "host_invocation_02"))
	if second.Receipts[0].CandidateID == first.Receipts[0].CandidateID {
		t.Fatal("new invocation after cancel reused the terminal candidate")
	}
}

func TestMemoryStatusSurfacesCaptureDisabledWithoutDeletingData(t *testing.T) {
	vault, ts := newProductMemoryServer(t)
	defer ts.Close()
	now := time.Now().UTC()
	prepared, err := vault.PrepareManualMemoryCapsule(store.MemoryCapsule{
		Schema: store.MemoryCapsuleSchema,
		Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339)},
		Items: []store.MemoryCapsuleItem{{
			Kind: "decision", RedactedSummary: "Disconnect preserves this committed memory.",
			Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "private", Retention: "project",
		}},
	}, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentProductCandidate(t, vault, prepared.Receipts[0], now, time.Second)
	if _, err := vault.CommitMemoryTrayCandidate(prepared.Receipts[0].CandidateID, 1, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	marker := []byte(`{"schema":"pulse.capture_state.v1","enabled":false,"reason":"host_disconnected"}`)
	if err := os.WriteFile(filepath.Join(filepath.Dir(vault.DBPath()), "capture-state.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	resp := pulseJSON(t, ts, http.MethodGet, "/memory/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var status struct {
		CaptureEnabled bool   `json:"capture_enabled"`
		CaptureState   string `json:"capture_state"`
		ItemCount      int    `json:"item_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.CaptureEnabled || status.CaptureState != "disabled" || status.ItemCount != 1 {
		t.Fatalf("disconnect status lost state/data: %#v", status)
	}
}

func TestTurnNoChangeRouteIsDurableAndIdempotent(t *testing.T) {
	_, ts := newProductMemoryServer(t)
	defer ts.Close()
	req := store.TurnNoChangeRequest{
		Schema: store.TurnNoChangeRequestSchema,
		Host:   "codex", SessionID: "session_nochange", TurnID: "turn_nochange",
		SourceEventKey: "codex:session_nochange:turn_nochange:stop", IdempotencyKey: "nochange_route",
		BindingDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PolicyEpoch: 2, ResolverEpoch: 4,
	}
	resp := pulseJSON(t, ts, http.MethodPost, "/turn/no-change", req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-change status=%d", resp.StatusCode)
	}
	var first store.TurnFinalizeResult
	if err := json.NewDecoder(resp.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	resp = pulseJSON(t, ts, http.MethodPost, "/turn/no-change", req)
	var second store.TurnFinalizeResult
	if err := json.NewDecoder(resp.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	if len(first.Receipts) != 0 || first.FinalizeReceipt.Status != store.TurnFinalizedNoChange ||
		first.FinalizeReceipt.Schema != store.TurnFinalizeReceiptSchema ||
		second.FinalizeReceipt.ReceiptID != first.FinalizeReceipt.ReceiptID {
		t.Fatalf("no-change retry diverged: first=%#v second=%#v", first, second)
	}
}

func TestProjectReadinessLifecycleRequiresOneRealTerminalMemoryAndMatchingFreshSessionFacts(t *testing.T) {
	terminal := TerminalMemoryReadinessFact{
		ReceiptID: "receipt_memory_01", PresentationReceiptID: "presentation_01", ObjectID: "pulse:memory_01",
		EvidenceIDs: []string{"pulse:pulse:memory_01"}, Status: string(store.MemoryWriteCreated),
		ContentDigest: strings.Repeat("c", 64),
		MemoryKind:    "decision", ConversationScope: "current_turn",
		BindingDigest: strings.Repeat("a", 64), RepositoryID: "repository-pulse",
		Host: "codex", SessionRef: "session:" + strings.Repeat("d", 64), CreatedAt: "2026-07-16T01:00:00Z",
		Active: true,
	}
	offered := ContextDeliveryReadinessFact{
		ContextID: "context_01", Acknowledgement: "offered_to_host", Purpose: "session_start",
		ObjectIDs: []string{terminal.ObjectID}, EvidenceIDs: []string{terminal.EvidenceIDs[0]},
		PayloadDigest: strings.Repeat("b", 64), BindingDigest: terminal.BindingDigest,
		RepositoryID: terminal.RepositoryID, Host: "codex", SessionRef: "session:" + strings.Repeat("e", 64),
		CreatedAt: "2026-07-16T01:01:00Z",
	}
	observed := offered
	observed.Acknowledgement = "host_observed"
	observed.CreatedAt = "2026-07-16T01:02:00Z"

	tests := []struct {
		name      string
		memories  []TerminalMemoryReadinessFact
		delivery  []ContextDeliveryReadinessFact
		wantState string
	}{
		{name: "no terminal memory", wantState: "first_memory_pending"},
		{name: "unpresented terminal receipt is not a proof", memories: []TerminalMemoryReadinessFact{func() TerminalMemoryReadinessFact {
			unpresented := terminal
			unpresented.PresentationReceiptID = ""
			return unpresented
		}()}, wantState: "first_memory_pending"},
		{name: "empty terminal identity is rejected", memories: []TerminalMemoryReadinessFact{func() TerminalMemoryReadinessFact {
			malformed := terminal
			malformed.ReceiptID = ""
			malformed.MemoryKind = ""
			return malformed
		}()}, wantState: "first_memory_pending"},
		{name: "whitespace terminal identity is rejected", memories: []TerminalMemoryReadinessFact{func() TerminalMemoryReadinessFact {
			malformed := terminal
			malformed.ReceiptID = " receipt_memory_01"
			malformed.Host = "codex "
			return malformed
		}()}, wantState: "first_memory_pending"},
		{name: "non canonical terminal time is rejected", memories: []TerminalMemoryReadinessFact{func() TerminalMemoryReadinessFact {
			malformed := terminal
			malformed.CreatedAt = "2026-07-16"
			return malformed
		}()}, wantState: "first_memory_pending"},
		{name: "non canonical fractional time is rejected", memories: []TerminalMemoryReadinessFact{func() TerminalMemoryReadinessFact {
			malformed := terminal
			malformed.CreatedAt = "2026-07-16T01:00:00.1000Z"
			return malformed
		}()}, wantState: "first_memory_pending"},
		{name: "empty evidence element is rejected", memories: []TerminalMemoryReadinessFact{func() TerminalMemoryReadinessFact {
			malformed := terminal
			malformed.EvidenceIDs = []string{""}
			return malformed
		}()}, wantState: "first_memory_pending"},
		{name: "install event is not a memory proof", memories: []TerminalMemoryReadinessFact{{
			ReceiptID: "receipt_install", PresentationReceiptID: "presentation_install",
			ObjectID: "pulse:install", Status: string(store.MemoryWriteCreated), ContentDigest: strings.Repeat("d", 64),
			MemoryKind: "system_event", ConversationScope: "install_event",
			BindingDigest: terminal.BindingDigest, RepositoryID: terminal.RepositoryID,
			Host: "pulse-cli", SessionRef: "session:" + strings.Repeat("f", 64), CreatedAt: "2026-07-16T00:00:00Z", Active: true,
		}}, wantState: "first_memory_pending"},
		{name: "terminal memory only", memories: []TerminalMemoryReadinessFact{terminal}, wantState: "context_offer_pending"},
		{name: "wrong project offer", memories: []TerminalMemoryReadinessFact{terminal}, delivery: []ContextDeliveryReadinessFact{func() ContextDeliveryReadinessFact {
			wrong := offered
			wrong.RepositoryID = "repository-other"
			return wrong
		}()}, wantState: "context_offer_pending"},
		{name: "same task offer is not continuity", memories: []TerminalMemoryReadinessFact{terminal}, delivery: []ContextDeliveryReadinessFact{func() ContextDeliveryReadinessFact {
			same := offered
			same.SessionRef = terminal.SessionRef
			return same
		}()}, wantState: "context_offer_pending"},
		{name: "subagent offer is not fresh task continuity", memories: []TerminalMemoryReadinessFact{terminal}, delivery: []ContextDeliveryReadinessFact{func() ContextDeliveryReadinessFact {
			subagent := offered
			subagent.Purpose = "subagent_start"
			return subagent
		}()}, wantState: "context_offer_pending"},
		{name: "wrong evidence is not continuity", memories: []TerminalMemoryReadinessFact{terminal}, delivery: []ContextDeliveryReadinessFact{func() ContextDeliveryReadinessFact {
			wrong := offered
			wrong.EvidenceIDs = []string{"pulse:other"}
			return wrong
		}()}, wantState: "context_offer_pending"},
		{name: "offered only", memories: []TerminalMemoryReadinessFact{terminal}, delivery: []ContextDeliveryReadinessFact{offered}, wantState: "host_observation_pending"},
		{name: "non canonical offer time is rejected", memories: []TerminalMemoryReadinessFact{terminal}, delivery: []ContextDeliveryReadinessFact{func() ContextDeliveryReadinessFact {
			malformed := offered
			malformed.CreatedAt = "2026-07-17"
			return malformed
		}()}, wantState: "context_offer_pending"},
		{name: "different context observation", memories: []TerminalMemoryReadinessFact{terminal}, delivery: []ContextDeliveryReadinessFact{offered, func() ContextDeliveryReadinessFact {
			wrong := observed
			wrong.ContextID = "context_other"
			return wrong
		}()}, wantState: "host_observation_pending"},
		{name: "matching observed fact", memories: []TerminalMemoryReadinessFact{terminal}, delivery: []ContextDeliveryReadinessFact{offered, observed}, wantState: "ready"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ProjectReadinessLifecycleInputs(test.memories, test.delivery)
			if got.Schema != ReadinessLifecycleInputsSchema || got.State != test.wantState {
				t.Fatalf("projection = %#v", got)
			}
			if test.wantState == "ready" {
				if got.TerminalMemory == nil || got.OfferedToHost == nil || got.HostObserved == nil ||
					got.TerminalMemory.ObjectID != terminal.ObjectID || got.OfferedToHost.ContextID != offered.ContextID ||
					got.HostObserved.ContextID != offered.ContextID {
					t.Fatalf("ready inputs do not preserve exact chain: %#v", got)
				}
			}
		})
	}
}

func TestMemoryTrayRecoveryScheduleSkipsUnpresentedCandidate(t *testing.T) {
	if _, ok, err := memoryTrayScheduleDelay("", time.Now().UTC()); err != nil || ok {
		t.Fatal("an unpresented candidate with no grace deadline became schedulable")
	}
}

func TestProductDeleteRouteFailsClosedWithoutOSPresence(t *testing.T) {
	vault, ts := newProductMemoryServer(t)
	defer ts.Close()
	now := time.Now().UTC()
	capsule := store.MemoryCapsule{
		Schema: store.MemoryCapsuleSchema,
		Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339)},
		Items: []store.MemoryCapsuleItem{{
			Kind: "decision", RedactedSummary: "Deletion must return a durable content-free receipt.",
			Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
		}},
	}
	prepared, err := vault.PrepareManualMemoryCapsule(capsule, now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentProductCandidate(t, vault, prepared.Receipts[0], now, 10*time.Second)
	created, err := vault.CommitMemoryTrayCandidate(
		prepared.Receipts[0].CandidateID, 1, now.Add(10*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	resp := pulseJSON(t, ts, http.MethodPost, "/memory/delete", map[string]any{"id": created.ObjectID})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}
	status, err := vault.MemoryStatus()
	if err != nil || status.ItemCount != 1 {
		t.Fatalf("denied product delete changed memory: status=%#v err=%v", status, err)
	}
}

func TestProductCorrectionRouteReturnsVisiblePendingCandidate(t *testing.T) {
	vault, ts := newProductMemoryServer(t)
	defer ts.Close()
	now := time.Now().UTC()
	prepared, err := vault.PrepareManualMemoryCapsule(store.MemoryCapsule{
		Schema: store.MemoryCapsuleSchema,
		Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339)},
		Items: []store.MemoryCapsuleItem{{
			Kind: "decision", RedactedSummary: "Old correction route value.",
			Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
		}},
	}, now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentProductCandidate(t, vault, prepared.Receipts[0], now, 10*time.Second)
	created, err := vault.CommitMemoryTrayCandidate(prepared.Receipts[0].CandidateID, 1, now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	resp := pulseJSON(t, ts, http.MethodPost, "/memory/"+created.ObjectID+"/correct", map[string]any{
		"candidate": map[string]any{
			"kind": store.PrivateMemoryCandidateCapsule,
			"capsule": map[string]any{
				"schema": store.MemoryCapsuleSchema,
				"source": map[string]any{"host": "claude-code", "conversation_scope": "current_turn", "timestamp": now.Format(time.RFC3339)},
				"items": []map[string]any{{
					"kind": "decision", "redacted_summary": "Corrected route value after the visible grace.",
					"confidence": 1, "evidence_hint": "user_confirmed", "privacy_tier": "normal", "retention": "project",
				}},
				"raw_input_included": false,
			},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("correction status=%d", resp.StatusCode)
	}
	var result store.TurnFinalizeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Receipts) != 1 || result.Receipts[0].Status != store.MemoryWritePending || result.Receipts[0].ObjectID != "" {
		t.Fatalf("correction route did not return pending: %#v", result)
	}
	tray, err := vault.ListMemoryTray(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range tray {
		if item.CandidateID == result.Receipts[0].CandidateID {
			found = item.Operation == "correct" && item.TargetObjectID == created.ObjectID
		}
	}
	if !found {
		t.Fatalf("correction not visible in Tray: %#v", tray)
	}
}

func TestProductCommitRecallDeleteInvalidatesLiveRetrievalImmediately(t *testing.T) {
	vault, err := store.OpenVault(
		filepath.Join(t.TempDir(), "desk-live-retrieval.db"), store.StoreKindDesk, "store_desk_live_retrieval_test",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	engine := retrieve.New(retrieve.Config{Store: vault, Embedder: productTestEmbedder{}})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault, Retrieval: engine})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	prepared, err := vault.PrepareManualMemoryCapsule(store.MemoryCapsule{
		Schema: store.MemoryCapsuleSchema,
		Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339)},
		Items: []store.MemoryCapsuleItem{{
			Kind: "decision", RedactedSummary: "The live retrieval deletion canary must disappear immediately.",
			Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "private", Retention: "project",
		}},
	}, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentProductCandidate(t, vault, prepared.Receipts[0], now, time.Second)
	created, err := vault.CommitMemoryTrayCandidate(prepared.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	srv.refreshProductRetrieval(created)
	before, err := engine.Retrieve(context.Background(), retrieve.RetrieveRequest{Query: "deletion canary", Mode: retrieve.ModeEmpathic, TopK: 3})
	if err != nil || len(before.EventIDs) != 1 {
		t.Fatalf("committed memory not live before delete: response=%#v err=%v", before, err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	deleted, err := vault.DeleteCommittedMemory(created.ObjectID, "delete:"+created.ObjectID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	srv.refreshProductRetrieval(deleted)
	after, err := engine.Retrieve(context.Background(), retrieve.RetrieveRequest{Query: "deletion canary", Mode: retrieve.ModeEmpathic, TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.EventIDs) != 0 {
		t.Fatalf("deleted memory remained in live retrieval: %v", after.EventIDs)
	}
}

func TestSemanticDeleteRebuildsAndReindexesRemainingPrivateContributions(t *testing.T) {
	vault, err := store.OpenVault(
		filepath.Join(t.TempDir(), "desk-semantic-delete.db"), store.StoreKindDesk, "store_desk_semantic_delete_test",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	engine := retrieve.New(retrieve.Config{Store: vault, Embedder: productTestEmbedder{}})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault, Retrieval: engine})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created := make([]store.MemoryWriteReceipt, 0, 2)
	for index := 0; index < 2; index++ {
		delta := store.SemanticDelta{
			Schema: store.SemanticDeltaSchema,
			Source: store.SemanticDeltaSource{
				Host: "codex", ConversationScope: "current_turn",
				Timestamp: now.Add(time.Duration(index) * time.Minute).Format(time.RFC3339),
				ThreadID:  "semantic-delete-rebuild", ProjectID: "pulse-product",
			},
			Events: []store.SemanticEvent{{
				ClientID:   fmt.Sprintf("event:semantic-delete-%d", index),
				Title:      fmt.Sprintf("Semantic delete event %d", index),
				Summary:    fmt.Sprintf("Private semantic contribution %d survives only while active.", index),
				Confidence: 1, PrivacyTier: "normal",
			}},
		}
		prepared, err := vault.PrepareManualSemanticDelta(delta, now.Add(time.Duration(index)*time.Minute), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		presentedAt := now.Add(time.Duration(index) * time.Minute)
		presentProductCandidate(t, vault, prepared.Receipts[0], presentedAt, time.Second)
		receipt, err := vault.CommitMemoryTrayCandidate(
			prepared.Receipts[0].CandidateID, 1, now.Add(time.Duration(index)*time.Minute+time.Second),
		)
		if err != nil {
			t.Fatal(err)
		}
		srv.refreshProductRetrieval(receipt)
		created = append(created, receipt)
	}
	before, err := engine.Retrieve(context.Background(), retrieve.RetrieveRequest{Query: "semantic delete", Mode: retrieve.ModeEmpathic, TopK: 5})
	if err != nil || len(before.EventIDs) != 2 {
		t.Fatalf("semantic fixture not live: result=%#v err=%v", before, err)
	}
	deleted, err := vault.DeleteCommittedMemory(created[0].ObjectID, "delete_semantic_live", now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	srv.refreshProductRetrieval(deleted)
	for _, objectID := range []string{created[0].ObjectID, created[1].ObjectID} {
		var projectionStatus string
		if err := vault.DB().QueryRow(`SELECT status FROM private_projection_outbox WHERE object_id=?`, objectID).Scan(&projectionStatus); err != nil {
			t.Fatal(err)
		}
		if projectionStatus != "complete" {
			t.Fatalf("projection %s status=%q after rebuild", objectID, projectionStatus)
		}
	}
	after, err := engine.Retrieve(context.Background(), retrieve.RetrieveRequest{Query: "semantic delete", Mode: retrieve.ModeEmpathic, TopK: 5})
	if err != nil || len(after.EventIDs) != 1 {
		t.Fatalf("remaining semantic contribution was lost/dark after rebuild: result=%#v err=%v", after, err)
	}
}

func TestProjectionStaysPendingWithoutEmbedderAndReplaysWhenEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desk-deferred-projection.db")
	vault, err := store.OpenVault(path, store.StoreKindDesk, "store_desk_deferred_projection_test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	prepared, err := vault.PrepareManualMemoryCapsule(store.MemoryCapsule{
		Schema: store.MemoryCapsuleSchema,
		Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339)},
		Items: []store.MemoryCapsuleItem{{
			Kind: "decision", RedactedSummary: "A memory captured before embedder setup must become retrievable later.",
			Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
		}},
	}, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentProductCandidate(t, vault, prepared.Receipts[0], now, time.Second)
	created, err := vault.CommitMemoryTrayCandidate(prepared.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	withoutEmbedder, err := New(Config{IPCSecret: "secret", Store: vault})
	if err != nil {
		t.Fatal(err)
	}
	withoutEmbedder.refreshProductRetrieval(created)
	var status string
	if err := vault.DB().QueryRow(`SELECT status FROM private_projection_outbox WHERE object_id=?`, created.ObjectID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("projection without embedder status=%q, want pending", status)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenVault(path, store.StoreKindDesk, "store_desk_deferred_projection_test")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	engine := retrieve.New(retrieve.Config{Store: reopened, Embedder: productTestEmbedder{}})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{IPCSecret: "secret", Store: reopened, Retrieval: engine}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DB().QueryRow(`SELECT status FROM private_projection_outbox WHERE object_id=?`, created.ObjectID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "complete" {
		t.Fatalf("replayed projection status=%q, want complete", status)
	}
	result, err := engine.Retrieve(context.Background(), retrieve.RetrieveRequest{Query: "embedder setup later", Mode: retrieve.ModeEmpathic, TopK: 3})
	if err != nil || len(result.EventIDs) != 1 {
		t.Fatalf("deferred memory did not become retrievable: result=%#v err=%v", result, err)
	}
}

func TestSemanticRebuildWithoutEmbedderReplaysSurvivorAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desk-semantic-restart.db")
	vault, err := store.OpenVault(path, store.StoreKindDesk, "store_desk_semantic_restart_test")
	if err != nil {
		t.Fatal(err)
	}
	engine := retrieve.New(retrieve.Config{Store: vault, Embedder: productTestEmbedder{}})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	withEmbedder, err := New(Config{IPCSecret: "secret", Store: vault, Retrieval: engine})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created := make([]store.MemoryWriteReceipt, 0, 2)
	for index := 0; index < 2; index++ {
		delta := store.SemanticDelta{
			Schema: store.SemanticDeltaSchema,
			Source: store.SemanticDeltaSource{
				Host: "codex", ConversationScope: "current_turn",
				Timestamp: now.Add(time.Duration(index) * time.Minute).Format(time.RFC3339),
				ThreadID:  "semantic-restart", ProjectID: "pulse-product",
			},
			Events: []store.SemanticEvent{{
				ClientID: fmt.Sprintf("event:restart-%d", index), Title: fmt.Sprintf("Restart survivor %d", index),
				Summary:    fmt.Sprintf("Semantic restart canary %d remains queryable after replay.", index),
				Confidence: 1, PrivacyTier: "private",
			}},
		}
		prepared, err := vault.PrepareManualSemanticDelta(delta, now.Add(time.Duration(index)*time.Minute), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		presentedAt := now.Add(time.Duration(index) * time.Minute)
		presentProductCandidate(t, vault, prepared.Receipts[0], presentedAt, time.Second)
		receipt, err := vault.CommitMemoryTrayCandidate(prepared.Receipts[0].CandidateID, 1, now.Add(time.Duration(index)*time.Minute+time.Second))
		if err != nil {
			t.Fatal(err)
		}
		withEmbedder.refreshProductRetrieval(receipt)
		created = append(created, receipt)
	}
	withoutEmbedder, err := New(Config{IPCSecret: "secret", Store: vault})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := vault.DeleteCommittedMemory(created[0].ObjectID, "delete_semantic_restart", now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	withoutEmbedder.refreshProductRetrieval(deleted)
	var status string
	if err := vault.DB().QueryRow(`SELECT status FROM private_projection_outbox WHERE object_id=?`, created[1].ObjectID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("survivor before restart status=%q, want pending", status)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenVault(path, store.StoreKindDesk, "store_desk_semantic_restart_test")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedEngine := retrieve.New(retrieve.Config{Store: reopened, Embedder: productTestEmbedder{}})
	if err := reopenedEngine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{IPCSecret: "secret", Store: reopened, Retrieval: reopenedEngine}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DB().QueryRow(`SELECT status FROM private_projection_outbox WHERE object_id=?`, created[1].ObjectID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "complete" {
		t.Fatalf("survivor after restart status=%q, want complete", status)
	}
	result, err := reopenedEngine.Retrieve(context.Background(), retrieve.RetrieveRequest{Query: "restart canary", Mode: retrieve.ModeEmpathic, TopK: 5})
	if err != nil || len(result.EventIDs) != 1 {
		t.Fatalf("replayed survivor retrieval=%#v err=%v", result, err)
	}
}

func TestProjectionRetriesTransientEmbedderFailureWithoutRestart(t *testing.T) {
	vault, err := store.OpenVault(
		filepath.Join(t.TempDir(), "desk-projection-retry.db"), store.StoreKindDesk, "store_desk_projection_retry_test",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	flaky := &flakyProductTestEmbedder{}
	engine := retrieve.New(retrieve.Config{Store: vault, Embedder: flaky})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault, Retrieval: engine})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	prepared, err := vault.PrepareManualMemoryCapsule(store.MemoryCapsule{
		Schema: store.MemoryCapsuleSchema,
		Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339)},
		Items: []store.MemoryCapsuleItem{{
			Kind: "decision", RedactedSummary: "Transient projection failure retries without restarting Pulse.",
			Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "private", Retention: "project",
		}},
	}, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentProductCandidate(t, vault, prepared.Receipts[0], now, time.Second)
	created, err := vault.CommitMemoryTrayCandidate(prepared.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	srv.refreshProductRetrieval(created)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := vault.DB().QueryRow(`SELECT status FROM private_projection_outbox WHERE object_id=?`, created.ObjectID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "complete" {
			result, err := engine.Retrieve(context.Background(), retrieve.RetrieveRequest{Query: "projection failure retries", Mode: retrieve.ModeEmpathic, TopK: 3})
			if err != nil || len(result.EventIDs) != 1 {
				t.Fatalf("retried projection result=%#v err=%v", result, err)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("transient projection failure did not retry to complete")
}

func TestProductServerAutomaticallyCommitsAfterVisibleGrace(t *testing.T) {
	vault, err := store.OpenVault(
		filepath.Join(t.TempDir(), "desk-auto.db"), store.StoreKindDesk, "store_desk_auto_test",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	srv, err := New(Config{IPCSecret: "secret", Store: vault, TrayGracePeriod: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := pulseJSON(t, ts, http.MethodPost, "/memory/remember", map[string]any{
		"schema": store.MemoryCapsuleSchema,
		"source": map[string]any{"host": "codex", "conversation_scope": "current_turn", "timestamp": time.Now().UTC().Format(time.RFC3339)},
		"items": []map[string]any{{
			"kind": "decision", "redacted_summary": "The daemon commits a safe candidate after the visible grace period.",
			"confidence": 1, "evidence_hint": "current_turn", "privacy_tier": "normal", "retention": "project",
		}},
		"raw_input_included": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prepare status=%d", resp.StatusCode)
	}
	var prepared store.TurnFinalizeResult
	if err := json.NewDecoder(resp.Body).Decode(&prepared); err != nil {
		t.Fatal(err)
	}
	presentProductCandidate(t, vault, prepared.Receipts[0], time.Now().UTC(), time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("candidate did not commit after the one-second grace period")
}

func TestProductServerTurnsAutomaticCommitErrorsIntoDurableFailedReceipts(t *testing.T) {
	vault, err := store.OpenVault(
		filepath.Join(t.TempDir(), "desk-auto-fail.db"), store.StoreKindDesk, "store_desk_auto_fail_test",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	srv, err := New(Config{IPCSecret: "secret", Store: vault, TrayGracePeriod: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp := pulseJSON(t, ts, http.MethodPost, "/memory/remember", map[string]any{
		"schema": store.MemoryCapsuleSchema,
		"source": map[string]any{"host": "codex", "conversation_scope": "current_turn", "timestamp": time.Now().UTC().Format(time.RFC3339)},
		"items": []map[string]any{{
			"kind": "decision", "redacted_summary": "The worker records a failed receipt instead of silently stalling.",
			"confidence": 1, "evidence_hint": "current_turn", "privacy_tier": "normal", "retention": "project",
		}},
		"raw_input_included": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prepare status=%d", resp.StatusCode)
	}
	var prepared store.TurnFinalizeResult
	if err := json.NewDecoder(resp.Body).Decode(&prepared); err != nil {
		t.Fatal(err)
	}
	candidateID := prepared.Receipts[0].CandidateID
	presentProductCandidate(t, vault, prepared.Receipts[0], time.Now().UTC(), time.Second)
	if _, err := vault.DB().Exec(`UPDATE memory_tray_candidates SET payload_json='{}' WHERE candidate_id=?`, candidateID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if err := vault.DB().QueryRow(`SELECT state FROM memory_tray_candidates WHERE candidate_id=?`, candidateID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "failed" {
			var status, reason string
			if err := vault.DB().QueryRow(`
				SELECT status, reason_code FROM memory_write_receipts
				 WHERE candidate_id=? ORDER BY rowid DESC LIMIT 1`, candidateID).Scan(&status, &reason); err != nil {
				t.Fatal(err)
			}
			if status != "failed" || reason != "commit_failed" {
				t.Fatalf("automatic failure receipt status=%q reason=%q", status, reason)
			}
			var memories int
			if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&memories); err != nil || memories != 0 {
				t.Fatalf("failed worker committed memory: count=%d err=%v", memories, err)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("automatic commit failure remained pending without a durable failed receipt")
}

func TestProductServerRestartLeavesUnpresentedPendingCandidateAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desk-restart.db")
	vault, err := store.OpenVault(path, store.StoreKindDesk, "store_desk_restart_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureProductRuntimeAuthority(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, 0,
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	prepared, err := vault.PrepareManualMemoryCapsule(store.MemoryCapsule{
		Schema: store.MemoryCapsuleSchema,
		Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339)},
		Items: []store.MemoryCapsuleItem{{
			Kind: "decision", RedactedSummary: "Restart recovery commits one already-expired pending candidate.",
			Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
		}},
	}, now, 10*time.Second)
	if err != nil || prepared.Receipts[0].Status != store.MemoryWritePending {
		t.Fatalf("prepare: %#v err=%v", prepared, err)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenVault(path, store.StoreKindDesk, "store_desk_restart_test")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.ConfigureProductRuntimeAuthority(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, 0,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{IPCSecret: "secret", Store: reopened}); err != nil {
		t.Fatal(err)
	}
	var state, graceExpiresAt string
	if err := reopened.DB().QueryRow(`
		SELECT state, grace_expires_at FROM memory_tray_candidates WHERE candidate_id=?`,
		prepared.Receipts[0].CandidateID,
	).Scan(&state, &graceExpiresAt); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reopened.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || graceExpiresAt != "" || count != 0 {
		t.Fatalf("unpresented recovery mutated candidate: state=%q grace=%q memories=%d", state, graceExpiresAt, count)
	}
}

func TestRestartDoesNotCommitCandidateFromStaleRuntimeAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desk-restart-stale-authority.db")
	vault, err := store.OpenVault(path, store.StoreKindDesk, "store_desk_restart_stale_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureProductRuntimeAuthority(strings.Repeat("a", 64), 1, 2); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	prepared, err := vault.PrepareManualMemoryCapsule(store.MemoryCapsule{
		Schema: store.MemoryCapsuleSchema,
		Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339)},
		Items: []store.MemoryCapsuleItem{{
			Kind: "decision", RedactedSummary: "A stale resolver epoch cannot commit after daemon restart.",
			Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "private", Retention: "project",
		}},
	}, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentProductCandidate(t, vault, prepared.Receipts[0], now, time.Second)
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenVault(path, store.StoreKindDesk, "store_desk_restart_stale_test")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.ConfigureProductRuntimeAuthority(strings.Repeat("b", 64), 1, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{IPCSecret: "secret", Store: reopened}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var state, payload string
		if err := reopened.DB().QueryRow(`SELECT state, payload_json FROM memory_tray_candidates WHERE candidate_id=?`, prepared.Receipts[0].CandidateID).Scan(&state, &payload); err != nil {
			t.Fatal(err)
		}
		var objects int
		if err := reopened.DB().QueryRow(`SELECT count(*) FROM private_memory_objects`).Scan(&objects); err != nil {
			t.Fatal(err)
		}
		if objects != 0 {
			t.Fatalf("stale authority recovery created %d objects", objects)
		}
		if state == "failed" {
			if payload != "{}" {
				t.Fatalf("stale authority failure retained payload=%q", payload)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("stale authority candidate did not reach a content-free terminal failure")
}

func TestProductServerRestartRecoversMoreThanOnePendingPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desk-restart-many.db")
	vault, err := store.OpenVault(path, store.StoreKindDesk, "store_desk_restart_many_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureProductRuntimeAuthority(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, 0,
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	createdCandidates := 0
	for batch := 0; batch < 11; batch++ {
		count := 20
		if batch == 10 {
			count = 5
		}
		candidates := make([]store.PrivateMemoryCandidate, 0, count)
		for item := 0; item < count; item++ {
			index := batch*20 + item
			candidates = append(candidates, store.PrivateMemoryCandidate{
				Kind: store.PrivateMemoryCandidateCapsule,
				Capsule: &store.MemoryCapsule{
					Schema: store.MemoryCapsuleSchema,
					Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339)},
					Items: []store.MemoryCapsuleItem{{
						Kind: "decision", RedactedSummary: fmt.Sprintf("Restart recovery page candidate %03d has a unique durable decision.", index),
						Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
					}},
				},
			})
		}
		result, err := vault.FinalizeTurn(store.TurnFinalizeRequest{
			Schema: store.TurnFinalizeRequestSchema,
			Host:   "codex", SessionID: fmt.Sprintf("session_many_%02d", batch), TurnID: fmt.Sprintf("turn_many_%02d", batch),
			SourceEventKey: fmt.Sprintf("codex:session_many_%02d:turn_many_%02d:stop", batch, batch),
			IdempotencyKey: fmt.Sprintf("finalize_many_%02d", batch),
			BindingDigest:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Candidates:     candidates,
		}, now, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, receipt := range result.Receipts {
			presentProductCandidate(t, vault, receipt, now, time.Second)
		}
		createdCandidates += len(result.Receipts)
	}
	if createdCandidates != 205 {
		t.Fatalf("fixture candidates=%d", createdCandidates)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenVault(path, store.StoreKindDesk, "store_desk_restart_many_test")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.ConfigureProductRuntimeAuthority(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, 0,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{IPCSecret: "secret", Store: reopened}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var pending, committed, failed int
		if err := reopened.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates WHERE state IN ('pending','committing')`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if err := reopened.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates WHERE state='committed'`).Scan(&committed); err != nil {
			t.Fatal(err)
		}
		if err := reopened.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates WHERE state='failed'`).Scan(&failed); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			if committed != 205 || failed != 0 {
				t.Fatalf("multi-page recovery committed=%d failed=%d", committed, failed)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("multi-page restart recovery left candidates pending")
}
