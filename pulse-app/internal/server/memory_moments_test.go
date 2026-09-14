package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func momentServerRequest(count int) store.TurnFinalizeRequest {
	req := store.TurnFinalizeRequest{Schema: store.TurnFinalizeRequestSchema, Host: "codex", SessionID: "moment_session", TurnID: "moment_turn", SourceEventKey: "moment_event", IdempotencyKey: "moment_write", BindingDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PolicyEpoch: 2, ResolverEpoch: 4}
	for i := 0; i < count; i++ {
		req.Candidates = append(req.Candidates, store.PrivateMemoryCandidate{Kind: store.PrivateMemoryCandidateCapsule, MemoryScope: "personal_global", Capsule: &store.MemoryCapsule{Schema: store.MemoryCapsuleSchema, Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: "2026-09-14T00:00:00Z"}, Items: []store.MemoryCapsuleItem{{Kind: "preference", RedactedSummary: fmt.Sprintf("Meaningful part %d of a complex remembered moment.", i), Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "sensitive", Retention: "long_term"}}}})
	}
	return req
}
func TestMemoryMomentHTTPAllPartsAndReplay(t *testing.T) {
	_, ts := newProductMemoryServer(t)
	defer ts.Close()
	req := momentServerRequest(61)
	var first store.TurnFinalizeResult
	for attempt := 0; attempt < 2; attempt++ {
		resp := pulseJSON(t, ts, http.MethodPost, "/memory/moments", req)
		if resp.StatusCode != 200 {
			t.Fatalf("write: %s", resp.Status)
		}
		var result store.TurnFinalizeResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if len(result.Receipts) != 61 || result.MomentID == "" {
			t.Fatal("incomplete result")
		}
		for _, r := range result.Receipts {
			if r.Status != store.MemoryWriteCreated || r.ObjectID == "" {
				t.Fatal(r)
			}
		}
		if attempt == 0 {
			first = result
		} else if first.LedgerID != result.LedgerID {
			t.Fatal("replay duplicated moment")
		}
	}
	total := 0
	for cursor := 0; ; {
		resp := pulseJSON(t, ts, http.MethodGet, fmt.Sprintf("/memory/moments/%s?cursor=%d", first.MomentID, cursor), nil)
		var page store.MemoryMomentPage
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		total += len(page.Items)
		if page.Next == 0 {
			break
		}
		cursor = page.Next
	}
	if total != 61 {
		t.Fatalf("retrieved %d of 61 parts", total)
	}
}
func TestMemoryMomentAcceptedPartsRecoverBeforeServing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	vault, err := store.OpenVault(path, store.StoreKindPersonal, "store_personal_server_test")
	if err != nil {
		t.Fatal(err)
	}
	req := momentServerRequest(21)
	if err := vault.ConfigureProductRuntimeAuthority(req.BindingDigest, req.PolicyEpoch, req.ResolverEpoch); err != nil {
		t.Fatal(err)
	}
	accepted, err := vault.FinalizeMemoryMoment(req, time.Now(), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Commit a prefix, then actually close the database: startup must recover only
	// the pending remainder, without duplicating already materialized objects.
	for _, receipt := range accepted.Receipts[:4] {
		if _, err := vault.CommitMemoryTrayCandidate(receipt.CandidateID, receipt.CandidateVersion, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	vault, err = store.OpenVault(path, store.StoreKindPersonal, "store_personal_server_test")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	if err := vault.ConfigureProductRuntimeAuthority(req.BindingDigest, req.PolicyEpoch, req.ResolverEpoch); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault, TrayGracePeriod: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_ = srv
	status, err := vault.MemoryMomentStatus(accepted.MomentID, accepted.FinalizeReceipt.SafeProvenance.Host)
	// Query with the actual binding; a host name is never an authority.
	if err == nil {
		t.Fatal("status accepted a host name as binding")
	}
	status, err = vault.MemoryMomentStatus(accepted.MomentID, momentServerRequest(1).BindingDigest)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range status.Receipts {
		if r.ObjectID == "" || r.Status != store.MemoryWriteCreated {
			t.Fatal(r)
		}
	}
}

func TestSignedMemoryMomentRecoversItsAcceptedRepositoryAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signed-restart.db")
	vault, err := store.OpenVault(path, store.StoreKindPersonal, "store_personal_signed_restart")
	if err != nil {
		t.Fatal(err)
	}
	req := momentServerRequest(21)
	req.PolicyEpoch = 0
	for i := range req.Candidates {
		req.Candidates[i].MemoryScope = "project"
		req.Candidates[i].Capsule.Items[0].Retention = "project"
	}
	// This store API receives the authority already verified by the HTTP handler.
	accepted, err := vault.FinalizeMemoryMoment(req, time.Now(), req.BindingDigest, "repository_signed_original", req.ResolverEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err = vault.Close(); err != nil {
		t.Fatal(err)
	}
	vault, err = store.OpenVault(path, store.StoreKindPersonal, "store_personal_signed_restart")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	if _, err = New(Config{IPCSecret: "secret", Store: vault, ProductBindingVerifier: &productBindingVerifierStub{}}); err != nil {
		t.Fatal(err)
	}
	result, err := vault.MemoryMomentStatus(accepted.MomentID, req.BindingDigest)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range result.Receipts {
		if r.Status != store.MemoryWriteCreated || r.ObjectID == "" {
			t.Fatal(r)
		}
	}
	page, err := vault.ReadMemoryMoment(accepted.MomentID, req.BindingDigest, "repository_signed_original", 0)
	if err != nil || len(page.Items) != 20 || page.Next != 20 {
		t.Fatalf("signed recovery incomplete: %+v %v", page, err)
	}
	foreign, err := vault.ReadMemoryMoment(accepted.MomentID, req.BindingDigest, "repository_unrelated", 0)
	if err != nil || len(foreign.Items) != 0 {
		t.Fatalf("signed recovery changed scope: %+v %v", foreign, err)
	}
}

func TestMemoryMomentHTTPPreservesNamedCoexistingFeelings(t *testing.T) {
	vault, ts := newProductMemoryServer(t)
	defer ts.Close()
	req := momentServerRequest(1)
	req.Candidates = nil
	for i := 0; i < 61; i++ {
		label, name, derivation := "trust", "Теплота", "explicit"
		if i%2 == 1 {
			label, name, derivation = "sadness", "Светлая грусть", "inferred"
		}
		event := store.SemanticEvent{ClientID: fmt.Sprintf("feeling_%d", i), Title: "Emotional moment: " + name, Summary: fmt.Sprintf("Подробность %d: несколько чувств сосуществуют в одном важном моменте.", i), Confidence: 0.8, PrivacyTier: "sensitive", EmotionalWeight: 0.7, Emotions: map[string]float64{label: 0.7}, ObservedLabel: name, EmotionDerivation: derivation, EmotionConfidence: 0.8, Trigger: &store.SemanticEmotionTrigger{Summary: "Значимое воспоминание.", Derivation: derivation, Confidence: 0.8, Confirmed: derivation == "explicit"}}
		req.Candidates = append(req.Candidates, store.PrivateMemoryCandidate{Kind: store.PrivateMemoryCandidateSemanticDelta, MemoryScope: "personal_global", SemanticDelta: &store.SemanticDelta{Schema: store.SemanticDeltaSchema, Source: store.SemanticDeltaSource{Host: "codex", SessionID: req.SessionID, ConversationScope: "current_turn", Timestamp: "2026-09-14T00:00:00Z"}, Events: []store.SemanticEvent{event}}})
	}
	resp := pulseJSON(t, ts, http.MethodPost, "/memory/moments", req)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("emotional write rejected: %s", resp.Status)
	}
	var result store.TurnFinalizeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Receipts) != 61 || len(result.EventIDs) != 61 {
		t.Fatalf("lost feelings: receipts=%d events=%d", len(result.Receipts), len(result.EventIDs))
	}
	page, err := vault.ReadMemoryMoment(result.MomentID, req.BindingDigest, "", 0)
	if err != nil || len(page.Items) != 20 {
		t.Fatalf("read feelings: %v", err)
	}
	for i, item := range page.Items {
		actual := item.Candidate.SemanticDelta.Events[0]
		expected := req.Candidates[i].SemanticDelta.Events[0]
		if actual.ObservedLabel != expected.ObservedLabel || actual.EmotionDerivation != expected.EmotionDerivation || actual.Trigger.Summary != expected.Trigger.Summary {
			t.Fatalf("emotional detail changed: %+v", actual)
		}
	}
	if ref := vault.MomentForEvent(result.EventIDs[0], req.BindingDigest, ""); ref != result.MomentID {
		t.Fatalf("recall lost full-moment reference: %q", ref)
	}
}

type blockedMomentEmbedder struct {
	gate    <-chan struct{}
	entered chan<- struct{}
}

func (e blockedMomentEmbedder) Embed(_ context.Context, texts []string, _ embed.InputType) ([][]float32, error) {
	select {
	case e.entered <- struct{}{}:
	default:
	}
	<-e.gate
	return productTestEmbedder{}.Embed(context.Background(), texts, embed.InputType("document"))
}
func (blockedMomentEmbedder) Model() string { return "blocked-moment-test" }

func TestMemoryMomentReceiptAndFullReadDoNotWaitForSearchIndex(t *testing.T) {
	vault, initial := newProductMemoryServer(t)
	initial.Close()
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	var release sync.Once
	defer release.Do(func() { close(gate) })
	engine := retrieve.New(retrieve.Config{Store: vault, Embedder: blockedMomentEmbedder{gate: gate, entered: entered}})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault, Retrieval: engine})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	body, _ := json.Marshal(momentServerRequest(4))
	request, _ := http.NewRequest(http.MethodPost, ts.URL+"/memory/moments", bytes.NewReader(body))
	request.Header.Set("X-Pulse-Key", "secret")
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("write waited for blocked search index: %v", err)
	}
	defer response.Body.Close()
	var result store.TurnFinalizeResult
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	for _, receipt := range result.Receipts {
		if receipt.Status != store.MemoryWriteCreated {
			t.Fatal(receipt)
		}
	}
	page, err := vault.ReadMemoryMoment(result.MomentID, momentServerRequest(1).BindingDigest, "", 0)
	if err != nil || len(page.Items) != 4 {
		t.Fatalf("canonical moment unavailable before indexing: %+v %v", page, err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background indexing never started")
	}
	pending, err := vault.ListPendingPrivateProjectionReceipts("", 200)
	if err != nil || len(pending) != 4 {
		t.Fatalf("unindexed receipts lost: %d %v", len(pending), err)
	}
	gate <- struct{}{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("next part never reached indexing")
	}
	pending, err = vault.ListPendingPrivateProjectionReceipts("", 200)
	if err != nil || len(pending) != 3 {
		t.Fatalf("indexing one part incorrectly completed the others: %d %v", len(pending), err)
	}
	release.Do(func() { close(gate) })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, err = vault.ListPendingPrivateProjectionReceipts("", 200)
		if err == nil && len(pending) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background indexing did not complete")
}
