package server

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

const (
	testMemoryPresentationOrigin = "http://127.0.0.1:47821"
	testMemoryPresentationPath   = "/home/memory/present"
)

type recordingMemoryPresentationStore struct {
	mu       sync.Mutex
	requests []store.MemoryPresentationRequest
	times    []time.Time
	graces   []time.Duration
	receipt  store.MemoryPresentationReceipt
	err      error
}

func (s *recordingMemoryPresentationStore) PresentMemoryTrayCandidate(
	req store.MemoryPresentationRequest,
	now time.Time,
	grace time.Duration,
) (store.MemoryPresentationReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	s.times = append(s.times, now)
	s.graces = append(s.graces, grace)
	return s.receipt, s.err
}

func (s *recordingMemoryPresentationStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func TestMemoryPresentationCapabilityPresentsOnlyItsExactHomeCard(t *testing.T) {
	now := time.Date(2026, 7, 16, 3, 15, 0, 0, time.UTC)
	backend := &recordingMemoryPresentationStore{receipt: store.MemoryPresentationReceipt{
		Schema:                 "pulse.memory_presentation_receipt.v1",
		ReceiptID:              "presentation_0123456789abcdef0123456789abcdef",
		CandidateID:            "candidate_0123456789abcdef0123456789abcdef",
		CandidateVersion:       3,
		ContentDigest:          strings.Repeat("a", 64),
		BindingDigest:          strings.Repeat("b", 64),
		TrustedSurfaceKind:     MemoryPresentationSurfaceHome,
		TrustedSurfaceInstance: testOpaqueBrowserValue("surface"),
		PresentedAt:            now.Format(time.RFC3339Nano),
		GraceExpiresAt:         now.Add(10 * time.Second).Format(time.RFC3339Nano),
	}}
	service := newTestMemoryPresentationService(t, backend, func() time.Time { return now })
	binding := testMemoryPresentationBinding()

	capability, err := service.IssueCapability(binding)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.Present(
		context.Background(),
		testMemoryPresentationRequest(),
		MemoryPresentationAttempt{
			Authority:  MemoryPresentationAuthorityHomeBrowser,
			Capability: capability,
			Binding:    binding,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ReceiptID != backend.receipt.ReceiptID {
		t.Fatalf("receipt = %#v", receipt)
	}
	if len(backend.requests) != 1 || len(backend.times) != 1 || len(backend.graces) != 1 {
		t.Fatalf("store calls = requests:%d times:%d graces:%d", len(backend.requests), len(backend.times), len(backend.graces))
	}
	request := backend.requests[0]
	if request.CandidateID != binding.CandidateID || request.CandidateVersion != binding.CandidateVersion ||
		request.ContentDigest != binding.ContentDigest || request.BindingDigest != binding.WorkspaceBindingDigest ||
		request.TrustedSurfaceKind != MemoryPresentationSurfaceHome ||
		request.TrustedSurfaceInstance != binding.TrustedSurfaceInstance {
		t.Fatalf("store request = %#v", request)
	}
	if !backend.times[0].Equal(now) || backend.graces[0] != 10*time.Second {
		t.Fatalf("presentation clock/grace = %s/%s", backend.times[0], backend.graces[0])
	}
}

func TestMemoryPresentationCapabilityCallsTheRealStoreBoundary(t *testing.T) {
	vault, daemon := newProductMemoryServer(t)
	defer daemon.Close()
	now := time.Date(2026, 7, 16, 3, 17, 0, 0, time.UTC)
	finalized, err := vault.FinalizeTurn(store.TurnFinalizeRequest{
		Schema: store.TurnFinalizeRequestSchema, Host: "codex",
		SessionID: "session_presentation_service", TurnID: "turn_presentation_service",
		SourceEventKey: "event_presentation_service", IdempotencyKey: "idempotency_presentation_service",
		BindingDigest: strings.Repeat("a", 64), PolicyEpoch: 2, ResolverEpoch: 4,
		Candidates: []store.PrivateMemoryCandidate{{
			Kind: store.PrivateMemoryCandidateCapsule,
			Capsule: &store.MemoryCapsule{
				Schema: store.MemoryCapsuleSchema,
				Source: store.CapsuleSource{
					Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339Nano),
				},
				Items: []store.MemoryCapsuleItem{{
					Kind: "decision", RedactedSummary: "A Home render adds an audit receipt without delaying durable memory.",
					Confidence: 0.98, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
				}},
			},
		}},
	}, now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalized.Receipts) != 1 {
		t.Fatalf("finalized receipts = %d", len(finalized.Receipts))
	}
	pending := finalized.Receipts[0]
	binding := MemoryPresentationBinding{
		BrowserSessionID: testOpaqueBrowserValue("real-session"), CSRFToken: testOpaqueBrowserValue("real-csrf"),
		WorkspaceBindingDigest: strings.Repeat("a", 64), CandidateID: pending.CandidateID,
		CandidateVersion: pending.CandidateVersion, ContentDigest: pending.ContentDigest,
		TrustedSurfaceInstance: testOpaqueBrowserValue("real-surface"),
	}
	now = now.Add(time.Second)
	var scheduledReceipts []store.MemoryPresentationReceipt
	var scheduledDelays []time.Duration
	service, err := NewMemoryPresentationService(MemoryPresentationServiceConfig{
		Store: vault,
		Schedule: func(receipt store.MemoryPresentationReceipt, delay time.Duration) {
			scheduledReceipts = append(scheduledReceipts, receipt)
			scheduledDelays = append(scheduledDelays, delay)
		},
		ExpectedOrigin: testMemoryPresentationOrigin, ExpectedPath: testMemoryPresentationPath,
		GracePeriod: 10 * time.Second, CapabilityTTL: 45 * time.Second,
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := service.IssueCapability(binding)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.Present(context.Background(), testMemoryPresentationRequest(), MemoryPresentationAttempt{
		Authority: MemoryPresentationAuthorityHomeBrowser, Capability: capability, Binding: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.CandidateID != pending.CandidateID || receipt.ContentDigest != pending.ContentDigest ||
		receipt.TrustedSurfaceInstance != binding.TrustedSurfaceInstance {
		t.Fatalf("presentation receipt = %#v", receipt)
	}
	var durableReceipts int
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM memory_presentation_receipts WHERE receipt_id=?`, receipt.ReceiptID).Scan(&durableReceipts); err != nil {
		t.Fatal(err)
	}
	if durableReceipts != 1 {
		t.Fatalf("durable presentation receipts = %d", durableReceipts)
	}
	if len(scheduledReceipts) != 1 || scheduledReceipts[0].ReceiptID != receipt.ReceiptID ||
		len(scheduledDelays) != 1 || scheduledDelays[0] != 0 {
		t.Fatalf("first presentation schedule = receipts:%#v delays:%#v", scheduledReceipts, scheduledDelays)
	}

	// A later authenticated Home surface remains valid audit evidence. It never
	// creates or extends a save delay.
	now = now.Add(11 * time.Second)
	secondBinding := binding
	secondBinding.TrustedSurfaceInstance = testOpaqueBrowserValue("later-surface")
	secondCapability, err := service.IssueCapability(secondBinding)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Present(context.Background(), testMemoryPresentationRequest(), MemoryPresentationAttempt{
		Authority: MemoryPresentationAuthorityHomeBrowser, Capability: secondCapability, Binding: secondBinding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.PresentedAt != now.Format(time.RFC3339Nano) || second.GraceExpiresAt != receipt.GraceExpiresAt {
		t.Fatalf("later presentation extended or rewrote grace: first=%#v second=%#v", receipt, second)
	}
	if len(scheduledReceipts) != 2 || len(scheduledDelays) != 2 || scheduledDelays[1] != 0 {
		t.Fatalf("late presentation schedule = receipts:%#v delays:%#v", scheduledReceipts, scheduledDelays)
	}
}

func TestMemoryPresentationServiceRequiresCommitScheduler(t *testing.T) {
	_, err := NewMemoryPresentationService(MemoryPresentationServiceConfig{
		Store: &recordingMemoryPresentationStore{}, ExpectedOrigin: testMemoryPresentationOrigin,
		ExpectedPath: testMemoryPresentationPath, GracePeriod: 10 * time.Second,
		CapabilityTTL: 45 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "scheduler") {
		t.Fatalf("missing scheduler error = %v", err)
	}
}

func TestMemoryTrayWorkerCommitsWithoutHomePresentation(t *testing.T) {
	vault, err := store.OpenVault(
		filepath.Join(t.TempDir(), "personal-presentation-schedule.db"),
		store.StoreKindPersonal, "store_personal_presentation_schedule",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	bindingDigest := strings.Repeat("a", 64)
	if err := vault.ConfigureProductRuntimeAuthority(bindingDigest, 0, 0); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault, TrayGracePeriod: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	finalized, err := vault.FinalizeTurn(store.TurnFinalizeRequest{
		Schema: store.TurnFinalizeRequestSchema, Host: "codex",
		SessionID: "session_late_home", TurnID: "turn_late_home",
		SourceEventKey: "event_late_home", IdempotencyKey: "idempotency_late_home",
		BindingDigest: bindingDigest,
		Candidates: []store.PrivateMemoryCandidate{{
			Kind: store.PrivateMemoryCandidateCapsule,
			Capsule: &store.MemoryCapsule{
				Schema: store.MemoryCapsuleSchema,
				Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339Nano)},
				Items: []store.MemoryCapsuleItem{{
					Kind: "decision", RedactedSummary: "Late Home presentation wakes the commit worker.",
					Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
				}},
			},
		}},
	}, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pending := finalized.Receipts[0]

	// Compatibility/recovery workers must never wait for Home to render.
	srv.scheduleReceipt(pending, 0)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count, presentationCount int
		if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM memory_capsules`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM memory_presentation_receipts WHERE candidate_id=?`, pending.CandidateID).Scan(&presentationCount); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			srv.trayScheduleMu.Lock()
			remainingSchedules := len(srv.traySchedules)
			srv.trayScheduleMu.Unlock()
			if remainingSchedules != 0 {
				t.Fatalf("terminal candidate retained %d schedule entries", remainingSchedules)
			}
			if presentationCount != 0 {
				t.Fatalf("automatic commit fabricated %d Home presentation receipts", presentationCount)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("unpresented compatibility candidate did not commit")
}

func TestPresentationScheduleHandoffSurvivesConcurrentNotPresentedExit(t *testing.T) {
	srv := &Server{traySchedules: make(map[memoryTrayScheduleKey]*memoryTrayScheduleState)}
	key := memoryTrayScheduleKey{candidateID: "candidate_0123456789abcdef0123456789abcdef", candidateVersion: 1}
	now := time.Date(2026, 7, 16, 4, 0, 0, 0, time.UTC)
	deadline := now.Add(10 * time.Second)

	// Force the lost-wakeup ordering: the speculative worker has already
	// received ErrMemoryTrayNotPresented, but Home records its presentation
	// before that worker removes the coalescing entry.
	state, claimed := srv.claimReceiptSchedule(key)
	if !claimed {
		t.Fatal("speculative worker did not claim schedule")
	}
	presentedState, created := srv.claimPresentedReceiptSchedule(key, deadline)
	if created || presentedState != state {
		t.Fatal("presentation did not hand its deadline to the in-flight worker")
	}
	delay, restart := srv.handoffPresentedScheduleAfterNotPresented(key, state, now)
	if !restart || delay != 10*time.Second {
		t.Fatalf("presentation wake-up was lost: restart=%v delay=%s", restart, delay)
	}
	srv.trayScheduleMu.Lock()
	retained := srv.traySchedules[key]
	srv.trayScheduleMu.Unlock()
	if retained != state {
		t.Fatal("handoff removed the active worker generation")
	}

	// The opposite ordering is safe too: once the old worker exits, Home must
	// install a fresh generation rather than attach to the deleted one.
	srv.finishReceiptSchedule(key, state)
	secondState, claimed := srv.claimReceiptSchedule(key)
	if !claimed {
		t.Fatal("second speculative worker did not claim schedule")
	}
	if _, restart := srv.handoffPresentedScheduleAfterNotPresented(key, secondState, now); restart {
		t.Fatal("unpresented worker unexpectedly restarted")
	}
	freshState, created := srv.claimPresentedReceiptSchedule(key, deadline)
	if !created || freshState == secondState {
		t.Fatal("late presentation did not create a fresh worker generation")
	}
}

func TestMemoryPresentationCapabilityRejectsEveryNonHomeAuthorityBeforeStore(t *testing.T) {
	authorities := []MemoryPresentationAuthority{
		"", "ambient_localhost", "mcp", "tool", "hook", "model",
	}
	for _, authority := range authorities {
		t.Run(string(authority), func(t *testing.T) {
			now := time.Date(2026, 7, 16, 3, 20, 0, 0, time.UTC)
			backend := &recordingMemoryPresentationStore{}
			service := newTestMemoryPresentationService(t, backend, func() time.Time { return now })
			binding := testMemoryPresentationBinding()
			capability, err := service.IssueCapability(binding)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Present(context.Background(), testMemoryPresentationRequest(), MemoryPresentationAttempt{
				Authority: authority, Capability: capability, Binding: binding,
			})
			if !errors.Is(err, ErrMemoryPresentationUnauthorized) {
				t.Fatalf("error = %v", err)
			}
			if backend.callCount() != 0 {
				t.Fatal("non-Home authority reached the store")
			}
		})
	}
}

func TestMemoryPresentationCapabilityRejectsDaemonCredentialsAndNonRenderFetches(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"ipc key", func(r *http.Request) { r.Header.Set("X-Pulse-Key", "daemon-secret") }},
		{"authorization", func(r *http.Request) { r.Header.Set("Authorization", "Bearer daemon") }},
		{"principal assertion", func(r *http.Request) { r.Header.Set("X-Pulse-Principal", "agent") }},
		{"gateway assertion", func(r *http.Request) { r.Header.Set("X-Pulse-Gateway-Assertion", "agent") }},
		{"mcp session", func(r *http.Request) { r.Header.Set("Mcp-Session-Id", "session") }},
		{"preflight", func(r *http.Request) {
			r.Method = http.MethodOptions
			r.Header.Set("Access-Control-Request-Method", "POST")
		}},
		{"get", func(r *http.Request) { r.Method = http.MethodGet }},
		{"query", func(r *http.Request) { r.URL.RawQuery = "capability=leak" }},
		{"cross origin", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:47822") }},
		{"cross host", func(r *http.Request) { r.Host = "127.0.0.1:47822" }},
		{"cross site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"navigate", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }},
		{"iframe", func(r *http.Request) { r.Header.Set("Sec-Fetch-Dest", "iframe") }},
		{"prefetch purpose", func(r *http.Request) { r.Header.Set("Purpose", "prefetch") }},
		{"prefetch sec purpose", func(r *http.Request) { r.Header.Set("Sec-Purpose", "prefetch") }},
		{"missing fetch metadata", func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, 7, 16, 3, 25, 0, 0, time.UTC)
			backend := &recordingMemoryPresentationStore{}
			service := newTestMemoryPresentationService(t, backend, func() time.Time { return now })
			binding := testMemoryPresentationBinding()
			capability, err := service.IssueCapability(binding)
			if err != nil {
				t.Fatal(err)
			}
			request := testMemoryPresentationRequest()
			testCase.mutate(request)
			_, err = service.Present(context.Background(), request, MemoryPresentationAttempt{
				Authority: MemoryPresentationAuthorityHomeBrowser, Capability: capability, Binding: binding,
			})
			if !errors.Is(err, ErrMemoryPresentationUnauthorized) {
				t.Fatalf("error = %v", err)
			}
			if backend.callCount() != 0 {
				t.Fatal("rejected browser request reached the store")
			}
		})
	}
}

func TestMemoryPresentationCapabilityRejectsEveryChangedBindingBeforeStore(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*MemoryPresentationBinding)
	}{
		{"browser session", func(b *MemoryPresentationBinding) { b.BrowserSessionID = testOpaqueBrowserValue("other-session") }},
		{"csrf", func(b *MemoryPresentationBinding) { b.CSRFToken = testOpaqueBrowserValue("other-csrf") }},
		{"workspace", func(b *MemoryPresentationBinding) { b.WorkspaceBindingDigest = strings.Repeat("c", 64) }},
		{"candidate", func(b *MemoryPresentationBinding) { b.CandidateID = "candidate_abcdefabcdefabcdefabcdefabcdefab" }},
		{"version", func(b *MemoryPresentationBinding) { b.CandidateVersion++ }},
		{"digest", func(b *MemoryPresentationBinding) { b.ContentDigest = strings.Repeat("d", 64) }},
		{"surface", func(b *MemoryPresentationBinding) { b.TrustedSurfaceInstance = testOpaqueBrowserValue("other-surface") }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, 7, 16, 3, 30, 0, 0, time.UTC)
			backend := &recordingMemoryPresentationStore{}
			service := newTestMemoryPresentationService(t, backend, func() time.Time { return now })
			binding := testMemoryPresentationBinding()
			capability, err := service.IssueCapability(binding)
			if err != nil {
				t.Fatal(err)
			}
			testCase.mutate(&binding)
			_, err = service.Present(context.Background(), testMemoryPresentationRequest(), MemoryPresentationAttempt{
				Authority: MemoryPresentationAuthorityHomeBrowser, Capability: capability, Binding: binding,
			})
			if !errors.Is(err, ErrMemoryPresentationUnauthorized) {
				t.Fatalf("error = %v", err)
			}
			if backend.callCount() != 0 {
				t.Fatal("cross-bound capability reached the store")
			}
		})
	}
}

func TestMemoryPresentationCapabilityExpiresAndCannotCrossServiceInstances(t *testing.T) {
	now := time.Date(2026, 7, 16, 3, 35, 0, 0, time.UTC)
	backend := &recordingMemoryPresentationStore{}
	service := newTestMemoryPresentationService(t, backend, func() time.Time { return now })
	binding := testMemoryPresentationBinding()
	capability, err := service.IssueCapability(binding)
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(46 * time.Second)
	_, err = service.Present(context.Background(), testMemoryPresentationRequest(), MemoryPresentationAttempt{
		Authority: MemoryPresentationAuthorityHomeBrowser, Capability: capability, Binding: binding,
	})
	if !errors.Is(err, ErrMemoryPresentationExpired) {
		t.Fatalf("expired error = %v", err)
	}

	other := newTestMemoryPresentationService(t, backend, func() time.Time { return now.Add(-46 * time.Second) })
	_, err = other.Present(context.Background(), testMemoryPresentationRequest(), MemoryPresentationAttempt{
		Authority: MemoryPresentationAuthorityHomeBrowser, Capability: capability, Binding: binding,
	})
	if !errors.Is(err, ErrMemoryPresentationUnauthorized) {
		t.Fatalf("cross-service error = %v", err)
	}
	if backend.callCount() != 0 {
		t.Fatal("expired or prior-instance capability reached the store")
	}
}

func TestMemoryPresentationCapabilityIsOneShotEvenWhenStoreRejectsStaleCandidate(t *testing.T) {
	now := time.Date(2026, 7, 16, 3, 40, 0, 0, time.UTC)
	backend := &recordingMemoryPresentationStore{err: store.ErrMemoryTrayVersionConflict}
	service := newTestMemoryPresentationService(t, backend, func() time.Time { return now })
	binding := testMemoryPresentationBinding()
	capability, err := service.IssueCapability(binding)
	if err != nil {
		t.Fatal(err)
	}
	attempt := MemoryPresentationAttempt{
		Authority: MemoryPresentationAuthorityHomeBrowser, Capability: capability, Binding: binding,
	}

	if _, err := service.Present(context.Background(), testMemoryPresentationRequest(), attempt); !errors.Is(err, store.ErrMemoryTrayVersionConflict) {
		t.Fatalf("first error = %v", err)
	}
	if _, err := service.Present(context.Background(), testMemoryPresentationRequest(), attempt); !errors.Is(err, ErrMemoryPresentationReplay) {
		t.Fatalf("replay error = %v", err)
	}
	if backend.callCount() != 1 {
		t.Fatalf("store calls = %d", backend.callCount())
	}
}

func TestMemoryPresentationCapabilityConcurrentReplayReachesStoreOnce(t *testing.T) {
	now := time.Date(2026, 7, 16, 3, 42, 0, 0, time.UTC)
	binding := testMemoryPresentationBinding()
	backend := &recordingMemoryPresentationStore{receipt: store.MemoryPresentationReceipt{
		Schema:                 store.MemoryPresentationReceiptSchema,
		ReceiptID:              "presentation_0123456789abcdef0123456789abcdef",
		CandidateID:            binding.CandidateID,
		CandidateVersion:       binding.CandidateVersion,
		ContentDigest:          binding.ContentDigest,
		BindingDigest:          binding.WorkspaceBindingDigest,
		TrustedSurfaceKind:     MemoryPresentationSurfaceHome,
		TrustedSurfaceInstance: binding.TrustedSurfaceInstance,
		PresentedAt:            now.Format(time.RFC3339Nano),
		GraceExpiresAt:         now.Add(10 * time.Second).Format(time.RFC3339Nano),
	}}
	service := newTestMemoryPresentationService(t, backend, func() time.Time { return now })
	capability, err := service.IssueCapability(binding)
	if err != nil {
		t.Fatal(err)
	}
	attempt := MemoryPresentationAttempt{
		Authority: MemoryPresentationAuthorityHomeBrowser, Capability: capability, Binding: binding,
	}

	const callers = 16
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.Present(context.Background(), testMemoryPresentationRequest(), attempt)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes, replays := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrMemoryPresentationReplay):
			replays++
		default:
			t.Fatalf("unexpected concurrent error = %v", err)
		}
	}
	if successes != 1 || replays != callers-1 || backend.callCount() != 1 {
		t.Fatalf("successes=%d replays=%d store_calls=%d", successes, replays, backend.callCount())
	}
}

func TestMemoryPresentationCapabilityRejectsTamperingBeforeStore(t *testing.T) {
	now := time.Date(2026, 7, 16, 3, 44, 0, 0, time.UTC)
	backend := &recordingMemoryPresentationStore{}
	service := newTestMemoryPresentationService(t, backend, func() time.Time { return now })
	binding := testMemoryPresentationBinding()
	capability, err := service.IssueCapability(binding)
	if err != nil {
		t.Fatal(err)
	}
	replacement := byte('A')
	if capability[len(capability)-1] == replacement {
		replacement = 'B'
	}
	capability = capability[:len(capability)-1] + string(replacement)

	_, err = service.Present(context.Background(), testMemoryPresentationRequest(), MemoryPresentationAttempt{
		Authority: MemoryPresentationAuthorityHomeBrowser, Capability: capability, Binding: binding,
	})
	if !errors.Is(err, ErrMemoryPresentationUnauthorized) {
		t.Fatalf("tamper error = %v", err)
	}
	if backend.callCount() != 0 {
		t.Fatal("tampered capability reached the store")
	}
}

func TestMemoryPresentationCapabilityPayloadStoresNoBrowserCredential(t *testing.T) {
	now := time.Date(2026, 7, 16, 3, 45, 0, 0, time.UTC)
	service := newTestMemoryPresentationService(t, &recordingMemoryPresentationStore{}, func() time.Time { return now })
	binding := testMemoryPresentationBinding()
	capability, err := service.IssueCapability(binding)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(capability, ".")
	if len(parts) != 2 {
		t.Fatalf("capability segments = %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), binding.BrowserSessionID) || strings.Contains(string(payload), binding.CSRFToken) {
		t.Fatalf("capability payload retained a browser credential: %s", payload)
	}
}

func newTestMemoryPresentationService(
	t *testing.T,
	backend MemoryPresentationStore,
	clock func() time.Time,
) *MemoryPresentationService {
	t.Helper()
	service, err := NewMemoryPresentationService(MemoryPresentationServiceConfig{
		Store:          backend,
		Schedule:       func(store.MemoryPresentationReceipt, time.Duration) {},
		ExpectedOrigin: testMemoryPresentationOrigin,
		ExpectedPath:   testMemoryPresentationPath,
		GracePeriod:    10 * time.Second,
		CapabilityTTL:  45 * time.Second,
		Clock:          clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testMemoryPresentationBinding() MemoryPresentationBinding {
	return MemoryPresentationBinding{
		BrowserSessionID:       testOpaqueBrowserValue("session"),
		CSRFToken:              testOpaqueBrowserValue("csrf"),
		WorkspaceBindingDigest: strings.Repeat("b", 64),
		CandidateID:            "candidate_0123456789abcdef0123456789abcdef",
		CandidateVersion:       3,
		ContentDigest:          strings.Repeat("a", 64),
		TrustedSurfaceInstance: testOpaqueBrowserValue("surface"),
	}
}

func testOpaqueBrowserValue(seed string) string {
	raw := make([]byte, 32)
	copy(raw, []byte(seed))
	return base64.RawURLEncoding.EncodeToString(raw)
}

func testMemoryPresentationRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, testMemoryPresentationOrigin+testMemoryPresentationPath, nil)
	req.Host = "127.0.0.1:47821"
	req.Header.Set("Origin", testMemoryPresentationOrigin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	return req
}

var _ MemoryPresentationStore = (*recordingMemoryPresentationStore)(nil)
var _ MemoryPresentationStore = (*store.Store)(nil)
var _ = errors.Is
