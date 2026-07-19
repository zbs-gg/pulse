package server

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/userpresence"
)

func TestBuildMemoryHomeUsesServerSideProductBoundaryAndSafeNextTaskPreview(t *testing.T) {
	t.Parallel()
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "personal.db"), store.StoreKindPersonal, "store_personal_server_home")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	binding := strings.Repeat("a", 64)
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_pulse"); err != nil {
		t.Fatal(err)
	}
	withoutRetrieval := &Server{cfg: Config{Store: vault}}
	ready := personalLiveReadinessForReason("personal_live_ready", "2026-07-16T09:58:00Z")
	unavailable, err := withoutRetrieval.buildMemoryHome(time.Date(2026, 7, 16, 9, 59, 0, 0, time.UTC), ready)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.Readiness.Outcome != store.MemoryHomeReadinessActionRequired || unavailable.Readiness.ReasonCode != "full_retrieval_unavailable" {
		t.Fatalf("Home claimed readiness without retrieval: %#v", unavailable.Readiness)
	}
	retrieval := retrieve.New(retrieve.Config{Store: vault, Embedder: productTestEmbedder{}})
	srv := &Server{cfg: Config{Store: vault, Retrieval: retrieval}}
	const expectedDigest = "f390ae2d10ccb4a4e9e396427d3cbd64a7c63fbbccb1cb514fa717576e2b3481"
	const expectedPreview = "Pulse next-task preview · Where we left off: No Pulse checkpoint exists for this thread yet. · Suggested next step: Ask what changed since the last Pulse checkpoint, then continue from the current user request."
	const expectedRenderedBytes = 212
	const expectedPulseTokens = 53
	home, err := srv.buildMemoryHome(time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC), ready)
	if err != nil {
		t.Fatal(err)
	}
	if home.Boundary.RepositoryID != "repository_pulse" || home.Boundary.BindingDigest != binding ||
		home.Readiness.Outcome != store.MemoryHomeReadinessActionRequired || home.Readiness.ReasonCode != "first_memory_required" {
		t.Fatalf("unexpected Home boundary/readiness: %#v", home)
	}
	if home.NextTaskPreview == nil || home.NextTaskPreview.Status != "preview_only" ||
		home.NextTaskPreview.RedactedResume != expectedPreview ||
		home.NextTaskPreview.PayloadDigest != expectedDigest ||
		home.NextTaskPreview.MethodID != store.MemoryHomeCountMethodUTF8BytesDiv4Ceil ||
		home.NextTaskPreview.MethodVersion != "1" ||
		home.NextTaskPreview.RenderedBytes != expectedRenderedBytes ||
		home.NextTaskPreview.PulseTokens != expectedPulseTokens {
		t.Fatalf("missing safe next-task preview: %#v", home.NextTaskPreview)
	}
}

func TestRenderMemoryHomePutsPendingWriteAndTruthfulProofAboveHistory(t *testing.T) {
	t.Parallel()
	estimated := 840
	reduction := 72.4
	page := memoryHomePage{
		Data: store.MemoryHomeData{
			Schema: store.MemoryHomeDataSchema,
			Boundary: store.MemoryHomeBoundary{
				StoreID: "/Users/private/pulse.db", StoreKind: string(store.StoreKindPersonal), RepositoryID: "repository_pulse",
				Locality: "device_local", Privacy: "private",
			},
			Readiness: store.MemoryHomeReadinessSnapshot{
				Outcome: store.MemoryHomeReadinessPartial, ReasonCode: "host_observation_required",
				NextAction: store.MemoryHomeNextAction{Code: "continue_fresh_task", Label: "Continue the fresh task"},
			},
			Memories: store.MemoryHomeMemories{ActiveCount: 3, LatestActive: []store.MemoryHomeActiveMemory{{
				ObjectID: "object_saved", Kind: "decision", RedactedSummary: "Use receipt-backed continuity.",
				Host: "claude-code", SessionRef: "session_fresh", CreatedAt: "2026-07-16T10:00:00Z",
				TerminalReceiptID: "receipt_saved", PresentationReceiptID: "presentation_saved",
			}}},
			Receipts: store.MemoryHomeReceipts{Attempts: []store.MemoryHomeAttempt{{
				CandidateID: "candidate_canceled", State: "canceled", Kind: "decision",
				RedactedSummary: "Save canceled before canonical memory.", ReceiptID: "receipt_attempt",
				ReceiptStatus: "canceled", CreatedAt: "2026-07-16T09:00:00Z",
			}}},
			Context: store.MemoryHomeContext{Selection: "last_task", LatestDelivery: &store.MemoryHomeDeliverySummary{
				ContextID: "context_01", OfferReceiptID: "offer_01", Acknowledgement: store.MemoryHomeDeliveryOfferedToHost,
				Host: "codex",
			}},
			Economy: store.MemoryHomeEconomy{
				State: store.MemoryHomeEconomyEstimated, EstimatedAvoidedTokens: &estimated,
				EstimatedReductionPercent: &reduction,
			},
			NextTaskPreview: &store.MemoryHomeNextTaskPreview{
				Status: "preview_only", RedactedResume: "Continue from the verified delivery receipt.", PulseTokens: 11,
			},
		},
		Pending: []memoryHomePendingCard{{
			CandidateID: "candidate_pending", Version: 2, Kind: "decision",
			Summary:       "Show this before saving. <script>alert(1)</script>",
			CandidateJSON: `{"kind":"memory_capsule"}`,
		}},
		CSRFToken: "csrf-test-value",
	}

	html, err := renderMemoryHomeHTML(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Pulse is remembering, but the harness has not confirmed receipt yet",
		"3", "840", "72.4%", "Context offered", "codex", "Continue the fresh task",
		"Current project", "repository_pulse",
		"Show this before saving.", "Use receipt-backed continuity.",
		"object_saved", "claude-code", "codex", "session_fresh", "receipt_saved", "presentation_saved",
		"Recent save attempts", "Save canceled before canonical memory.", "receipt_attempt", "canceled",
		"What the next task receives", "Continue from the verified delivery receipt.",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("Home HTML missing %q", want)
		}
	}
	if strings.Index(html, "Show this before saving.") > strings.Index(html, "Use receipt-backed continuity.") {
		t.Fatal("pending write appeared below canonical history")
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatal("stored summary was not escaped")
	}
	if strings.Contains(html, "/Users/private/pulse.db") {
		t.Fatal("Home rendered a path instead of the opaque repository ID")
	}
	if strings.Contains(html, "key=") || strings.Contains(html, "X-Pulse-Key") || strings.Contains(html, "Authorization") {
		t.Fatal("Home HTML exposed daemon authority")
	}
}

func TestMemoryHomeMeasuredEconomyNeverLabelsAnEstimatedPercentageAsMeasured(t *testing.T) {
	measured := 320
	estimatedPercent := 72.4
	value, detail, percent := memoryHomeEconomyCopy(store.MemoryHomeEconomy{
		State: store.MemoryHomeEconomyMeasured, MeasuredAvoidedTokens: &measured,
		EstimatedReductionPercent: &estimatedPercent,
	})
	if value != "320" || detail != "Measured avoided input tokens" || percent != "" {
		t.Fatalf("measured copy mixed estimated percentage: value=%q detail=%q percent=%q", value, detail, percent)
	}
}

func TestRenderMemoryHomeOnlyOffersAccessibleProtectedWipeForAnExactAuthorityProfile(t *testing.T) {
	t.Parallel()
	base := memoryHomePage{Data: store.MemoryHomeData{
		Boundary: store.MemoryHomeBoundary{
			StoreKind: string(store.StoreKindPersonal), RepositoryID: "repository_pulse",
		},
		Readiness: store.MemoryHomeReadinessSnapshot{
			Outcome:    store.MemoryHomeReadinessReady,
			NextAction: store.MemoryHomeNextAction{Label: "Continue working"},
		},
	}, CSRFToken: "csrf-test-value"}
	base.EnhancedPresenceProfile = userpresence.EnhancedPresenceProfile{
		Schema: userpresence.EnhancedPresenceProfileSchemaV1, Version: 1,
		Kind: userpresence.EnhancedPresenceMacOSNative, Available: true,
		ProtectedActions: []userpresence.Action{userpresence.ActionVaultWipe},
	}

	html, err := renderMemoryHomeHTML(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`data-protected-wipe`,
		`type="button" data-protected-wipe-begin`,
		`Review exact stored records`,
		`data-protected-wipe-review hidden tabindex="-1"`,
		`<strong>Permanent deletion.</strong>`,
		`stored records across memory and continuity`,
		`data-protected-wipe-countdown aria-hidden="true"`,
		`data-protected-wipe-countdown-live class="sr-only" role="status" aria-live="polite"`,
		`type="button" class="danger" data-protected-wipe-complete`,
		`Verify with this device and delete exact records`,
		`type="button" data-protected-wipe-cancel`,
		`data-protected-wipe-status role="status" aria-live="polite" aria-atomic="true"`,
		`data-protected-wipe-receipt hidden tabindex="-1"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("protected wipe card missing %q: %s", want, html)
		}
	}

	invalidProfiles := []userpresence.EnhancedPresenceProfile{
		{
			Schema: userpresence.EnhancedPresenceProfileSchemaV1, Version: 1,
			Kind: userpresence.EnhancedPresenceMacOSNative, Available: false,
			ProtectedActions: []userpresence.Action{userpresence.ActionVaultWipe},
		},
		{
			Schema: "pulse.enhanced_presence.profile.v0", Version: 1,
			Kind: userpresence.EnhancedPresenceMacOSNative, Available: true,
			ProtectedActions: []userpresence.Action{userpresence.ActionVaultWipe},
		},
		{
			Schema: userpresence.EnhancedPresenceProfileSchemaV1, Version: 2,
			Kind: userpresence.EnhancedPresenceMacOSNative, Available: true,
			ProtectedActions: []userpresence.Action{userpresence.ActionVaultWipe},
		},
		{
			Schema: userpresence.EnhancedPresenceProfileSchemaV1, Version: 1,
			Kind: userpresence.EnhancedPresenceUnavailable, Available: true,
			ProtectedActions: []userpresence.Action{userpresence.ActionVaultWipe},
		},
		{
			Schema: userpresence.EnhancedPresenceProfileSchemaV1, Version: 1,
			Kind: userpresence.EnhancedPresenceMacOSNative, Available: true,
			ProtectedActions: []userpresence.Action{userpresence.ActionBindingChange},
		},
		{
			Schema: userpresence.EnhancedPresenceProfileSchemaV1, Version: 1,
			Kind: userpresence.EnhancedPresenceMacOSNative, Available: true,
			ProtectedActions: []userpresence.Action{userpresence.ActionVaultWipe, "memory.export"},
		},
		{
			Schema: userpresence.EnhancedPresenceProfileSchemaV1, Version: 1,
			Kind: userpresence.EnhancedPresenceMacOSNative, Available: true,
			ProtectedActions: []userpresence.Action{userpresence.ActionVaultWipe, userpresence.ActionVaultWipe},
		},
	}
	for index, profile := range invalidProfiles {
		base.EnhancedPresenceProfile = profile
		html, err = renderMemoryHomeHTML(base)
		if err != nil {
			t.Fatalf("profile %d render: %v", index, err)
		}
		if strings.Contains(html, `data-protected-wipe`) {
			t.Fatalf("profile %d exposed actionable protected wipe: %s", index, html)
		}
	}
}

func TestRenderMemoryHomeShowsUnassignedEmptyAndUnavailableWithoutClaimingMemory(t *testing.T) {
	t.Parallel()
	base := memoryHomePage{
		Data: store.MemoryHomeData{
			Boundary:  store.MemoryHomeBoundary{BindingDigest: strings.Repeat("a", 64), RepositoryID: "repository_pulse"},
			Readiness: store.MemoryHomeReadinessSnapshot{NextAction: store.MemoryHomeNextAction{Label: "Continue working"}},
		},
		UnassignedEnabled: true,
	}
	empty, err := renderMemoryHomeHTML(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty, "No unassigned memories") || strings.Contains(empty, "Inbox is unavailable") {
		t.Fatalf("empty Inbox state is not explicit: %s", empty)
	}
	base.UnassignedUnavailable = true
	unavailable, err := renderMemoryHomeHTML(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unavailable, "Inbox is unavailable") ||
		!strings.Contains(unavailable, "No queued content was read or moved") {
		t.Fatalf("unavailable Inbox state is not fail-closed: %s", unavailable)
	}
}

func TestRenderMemoryHomeBoundsAttemptsOutsideCanonicalMemory(t *testing.T) {
	t.Parallel()
	attempts := make([]store.MemoryHomeAttempt, 11)
	for index := range attempts {
		attempts[index] = store.MemoryHomeAttempt{
			State: "failed", Kind: "decision", RedactedSummary: fmt.Sprintf("Attempt summary %02d", index),
			ReceiptID: fmt.Sprintf("receipt_attempt_%02d", index), ReceiptStatus: "failed",
		}
	}
	page := memoryHomePage{Data: store.MemoryHomeData{
		Boundary: store.MemoryHomeBoundary{StoreKind: string(store.StoreKindPersonal), RepositoryID: "repository_pulse"},
		Readiness: store.MemoryHomeReadinessSnapshot{
			Outcome: store.MemoryHomeReadinessActionRequired, ReasonCode: "first_memory_required",
			NextAction: store.MemoryHomeNextAction{Label: "Save the first memory"},
		},
		Receipts: store.MemoryHomeReceipts{Attempts: attempts},
	}}
	html, err := renderMemoryHomeHTML(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(html, `class="memory attempt"`) != 10 ||
		!strings.Contains(html, "Attempt summary 00") || !strings.Contains(html, "Attempt summary 09") ||
		strings.Contains(html, "Attempt summary 10") {
		t.Fatalf("Home did not keep attempts separate and bounded: %s", html)
	}
	if strings.Index(html, "Recent save attempts") < strings.Index(html, "Latest memories") {
		t.Fatal("attempts were mixed into canonical memory")
	}
}

func TestRenderMemoryHomeUsesReasonSpecificReadinessCopy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		readiness store.MemoryHomeReadinessSnapshot
		want      []string
		forbidden string
	}{
		{
			name: "full retrieval unavailable",
			readiness: store.MemoryHomeReadinessSnapshot{
				Outcome: store.MemoryHomeReadinessPartial, ReasonCode: "full_retrieval_unavailable",
				NextAction: store.MemoryHomeNextAction{Label: "Run pulse repair"},
			},
			want:      []string{"Full retrieval is not enabled", "fallback keyword recall", "Pulse retrieval is not being claimed"},
			forbidden: "context offer is recorded",
		},
		{
			name: "embedder warming",
			readiness: store.MemoryHomeReadinessSnapshot{
				Outcome: store.MemoryHomeReadinessWarming, ReasonCode: "local_embedder_warming",
				NextAction: store.MemoryHomeNextAction{Label: "Keep Pulse open"},
			},
			want:      []string{"Pulse is warming up", "local memory engine is starting"},
			forbidden: "context offer is recorded",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			html, err := renderMemoryHomeHTML(memoryHomePage{Data: store.MemoryHomeData{
				Boundary:  store.MemoryHomeBoundary{StoreKind: string(store.StoreKindPersonal), RepositoryID: "repository_pulse"},
				Readiness: test.readiness,
			}})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.want {
				if !strings.Contains(html, want) {
					t.Fatalf("Home HTML missing reason-specific copy %q: %s", want, html)
				}
			}
			if strings.Contains(strings.ToLower(html), test.forbidden) {
				t.Fatalf("Home HTML made unrelated readiness claim %q: %s", test.forbidden, html)
			}
		})
	}
}

func TestRenderMemoryHomeCollectingBaselineNeverFabricatesSavings(t *testing.T) {
	t.Parallel()
	page := memoryHomePage{Data: store.MemoryHomeData{
		Schema:   store.MemoryHomeDataSchema,
		Boundary: store.MemoryHomeBoundary{StoreKind: string(store.StoreKindDesk), Locality: "device_local", Privacy: "private"},
		Readiness: store.MemoryHomeReadinessSnapshot{
			Outcome: store.MemoryHomeReadinessActionRequired, ReasonCode: "first_memory_required",
			NextAction: store.MemoryHomeNextAction{Code: "save_first_memory", Label: "Save the first memory"},
		},
		Economy: store.MemoryHomeEconomy{
			State: store.MemoryHomeEconomyCollectingBaseline,
			LatestOffer: &store.MemoryHomeLocalOffer{
				RenderedBytes: 145, PulseTokens: 37,
				MethodID: store.MemoryHomeCountMethodUTF8BytesDiv4Ceil, MethodVersion: "1",
			},
		},
	}}
	html, err := renderMemoryHomeHTML(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Pulse is connected", "Save the first memory", "Collecting a comparable baseline", "No estimate yet",
		"Latest offer: 37 Pulse tokens", "145 rendered bytes", "utf8_bytes_div4_ceil v1",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("Home HTML missing %q", want)
		}
	}
	for _, forbidden := range []string{">100%<", "saved 0", "tokens saved: 0"} {
		if strings.Contains(strings.ToLower(html), forbidden) {
			t.Fatalf("Home fabricated savings with %q", forbidden)
		}
	}
}
