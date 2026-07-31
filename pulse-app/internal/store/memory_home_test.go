package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestPersonalProjectLabelsStayHumanAndPathFree(t *testing.T) {
	s, err := OpenVault(filepath.Join(t.TempDir(), "personal.db"), StoreKindPersonal, "store_personal_labels")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RegisterPersonalProjectLabel("repository_pulse", "Pulse"); err != nil {
		t.Fatal(err)
	}
	if got := memoryHomeProjectLabel(s.DB(), "repository_pulse"); got != "Pulse" {
		t.Fatalf("project label=%q, want Pulse", got)
	}
	for _, unsafe := range []string{"", ".", "..", "/Users/example/pulse", `C:\Users\example\pulse`, "pulse\nsecret"} {
		if err := s.RegisterPersonalProjectLabel("repository_pulse", unsafe); err == nil {
			t.Fatalf("unsafe project label %q was accepted", unsafe)
		}
	}
	if got := memoryHomeProjectLabel(s.DB(), "repository_pulse"); got != "Pulse" {
		t.Fatalf("unsafe update changed project label to %q", got)
	}
}

func TestBuildMemoryHomeDataTracksPendingCommittedAndDeletedCanonicalMemory(t *testing.T) {
	s, err := OpenVault(filepath.Join(t.TempDir(), "personal.db"), StoreKindPersonal, "store_personal_home")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	binding := localStoreBindingDigest("store_personal_home")
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	req := trayFinalizeRequest("The Home reads this canonical private decision.")
	req.BindingDigest = binding
	finalized, err := s.FinalizeTurn(req, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	query := MemoryHomeQuery{
		RepositoryID: "repository_pulse", BindingDigest: binding, GeneratedAt: now.Add(time.Minute),
		LiveReadiness: MemoryHomeLiveReadiness{Outcome: MemoryHomeReadinessReady, ReasonCode: "live_product_ready",
			NextAction: MemoryHomeNextAction{Code: "continue_working", Label: "Continue working"}},
	}
	pendingHome, err := s.BuildMemoryHomeData(query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pendingHome.Memories.ActiveCount != 0 || len(pendingHome.Receipts.Attempts) != 1 ||
		pendingHome.Receipts.Attempts[0].State != "pending" {
		t.Fatalf("pending Home=%#v", pendingHome)
	}
	pending := finalized.Receipts[0]
	if _, err := s.PresentMemoryTrayCandidate(MemoryPresentationRequest{
		CandidateID: pending.CandidateID, CandidateVersion: pending.CandidateVersion,
		ContentDigest: pending.ContentDigest, BindingDigest: binding,
		TrustedSurfaceKind: "memory_home", TrustedSurfaceInstance: "home_test_session",
	}, now, time.Second); err != nil {
		t.Fatal(err)
	}
	committed, err := s.CommitMemoryTrayCandidate(pending.CandidateID, pending.CandidateVersion, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	committedHome, err := s.BuildMemoryHomeData(query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if committedHome.Memories.ActiveCount != 1 || len(committedHome.Memories.LatestActive) != 1 ||
		committedHome.Memories.LatestActive[0].ObjectID != committed.ObjectID ||
		committedHome.Memories.LatestActive[0].RedactedSummary != "The Home reads this canonical private decision." ||
		committedHome.Memories.LatestActive[0].EditableSummary != "The Home reads this canonical private decision." ||
		len(committedHome.Receipts.Attempts) != 0 {
		t.Fatalf("committed Home=%#v", committedHome)
	}
	if _, err := s.DeleteCommittedMemory(committed.ObjectID, "delete_home_test", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	deletedHome, err := s.BuildMemoryHomeData(query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if deletedHome.Memories.ActiveCount != 0 || len(deletedHome.Memories.LatestActive) != 0 {
		t.Fatalf("deleted canonical memory remained in Home: %#v", deletedHome.Memories)
	}
}

func TestBuildMemoryHomeDataFiltersAttemptStatesBeforeItsBoundedLimit(t *testing.T) {
	s, err := OpenVault(filepath.Join(t.TempDir(), "personal.db"), StoreKindPersonal, "store_personal_attempts")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	binding := localStoreBindingDigest("store_personal_attempts")
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)

	pendingReq := memoryHomeFinalizeRequest("Pending memory remains visible.", 1, binding)
	pending, err := s.FinalizeTurn(pendingReq, base, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	canceledReq := memoryHomeFinalizeRequest("Canceled memory body is erased.", 2, binding)
	canceled, err := s.FinalizeTurn(canceledReq, base.Add(time.Second), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelMemoryTrayCandidate(canceled.Receipts[0].CandidateID, canceled.Receipts[0].CandidateVersion, base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	failedReq := memoryHomeFinalizeRequest("Failed memory body is erased.", 3, binding)
	failed, err := s.FinalizeTurn(failedReq, base.Add(3*time.Second), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailMemoryTrayCandidate(failed.Receipts[0].CandidateID, failed.Receipts[0].CandidateVersion, "commit_failed", base.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	rejectedReq := memoryHomeFinalizeRequest("/Users/example/.ssh/id_rsa", 4, binding)
	rejected, err := s.FinalizeTurn(rejectedReq, base.Add(5*time.Second), time.Second)
	if err != nil || rejected.Status != TurnFinalizedRejected {
		t.Fatalf("rejected=%#v err=%v", rejected, err)
	}

	for index := 0; index < 20; index++ {
		req := memoryHomeFinalizeRequest(fmt.Sprintf("Committed memory %02d.", index), 100+index, binding)
		at := base.Add(time.Duration(10+index) * time.Second)
		finalized, err := s.FinalizeTurn(req, at, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		receipt := finalized.Receipts[0]
		if _, err := s.PresentMemoryTrayCandidate(MemoryPresentationRequest{
			CandidateID: receipt.CandidateID, CandidateVersion: receipt.CandidateVersion,
			ContentDigest: receipt.ContentDigest, BindingDigest: binding,
			TrustedSurfaceKind: "memory_home", TrustedSurfaceInstance: fmt.Sprintf("attempts_%02d", index),
		}, at, time.Second); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CommitMemoryTrayCandidate(receipt.CandidateID, receipt.CandidateVersion, at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	home, err := s.BuildMemoryHomeData(MemoryHomeQuery{
		RepositoryID: "repository_pulse", BindingDigest: binding, GeneratedAt: base.Add(time.Hour),
		LiveReadiness: MemoryHomeLiveReadiness{Outcome: MemoryHomeReadinessReady},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]bool{}
	for _, attempt := range home.Receipts.Attempts {
		states[attempt.State] = true
		if strings.Contains(attempt.RedactedSummary, "/Users/") {
			t.Fatalf("attempt leaked rejected source content: %#v", attempt)
		}
	}
	for _, state := range []string{"pending", "canceled", "failed", "rejected"} {
		if !states[state] {
			t.Fatalf("attempt state %q was hidden by newer committed candidates: %#v", state, home.Receipts.Attempts)
		}
	}
	if pending.Receipts[0].CandidateID == "" {
		t.Fatal("pending fixture lost candidate identity")
	}
}

func TestBuildMemoryHomeDataScopesAttemptsToExactBinding(t *testing.T) {
	s, err := OpenVault(filepath.Join(t.TempDir(), "personal.db"), StoreKindPersonal, "store_personal_attempt_scope")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	binding := localStoreBindingDigest("store_personal_attempt_scope")
	otherBinding := strings.Repeat("f", 64)
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	insertMemoryHomePendingAttemptFixture(t, s, binding, "wanted", base)
	for index := 0; index < 250; index++ {
		insertMemoryHomePendingAttemptFixture(
			t, s, otherBinding, fmt.Sprintf("other_%03d", index), base.Add(time.Duration(index+1)*time.Minute),
		)
	}

	home, err := s.BuildMemoryHomeData(MemoryHomeQuery{
		RepositoryID: "repository_pulse", BindingDigest: binding, GeneratedAt: base.Add(time.Hour),
		LiveReadiness: MemoryHomeLiveReadiness{Outcome: MemoryHomeReadinessReady},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(home.Receipts.Attempts) != 1 || home.Receipts.Attempts[0].CandidateID != "candidate_wanted" {
		t.Fatalf("cross-binding attempt leaked into Home: %#v", home.Receipts.Attempts)
	}
}

func TestMemoryHomeQueriesUseBoundedScopeFirstIndexes(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	binding := localStoreBindingDigest(s.StoreID())
	repository := "repository_pulse"

	candidatePlan := memoryHomeQueryPlanDetails(t, s.DB(), memoryHomeCandidateAttemptsQuery, binding, 10)
	assertMemoryHomePlanUses(t, candidatePlan,
		"idx_turn_ledgers_memory_home_binding", "idx_memory_write_receipts_candidate_latest",
	)
	rejectedPlan := memoryHomeQueryPlanDetails(t, s.DB(), memoryHomeRejectedAttemptsQuery, binding, 10)
	assertMemoryHomePlanUses(t, rejectedPlan,
		"idx_turn_ledgers_memory_home_binding", "idx_memory_write_receipts_memory_home_rejected",
	)
	deliveryPlan := memoryHomeQueryPlanDetails(
		t, s.DB(), memoryHomeDeliveryFactsQuery, repository, binding, repository, binding, 100,
	)
	assertMemoryHomePlanUses(t, deliveryPlan,
		"idx_continuity_delivery_memory_home", "idx_continuity_delivery_memory_home_recent",
	)
	if strings.Contains(strings.ToUpper(memoryHomeDeliveryFactsQuery), "GROUP BY") {
		t.Fatal("Memory Home delivery query regressed to a full-ledger GROUP BY")
	}
	for name, plan := range map[string]string{
		"candidate attempts": candidatePlan,
		"rejected attempts":  rejectedPlan,
		"delivery facts":     deliveryPlan,
	} {
		for _, line := range strings.Split(plan, "\n") {
			if strings.Contains(line, "SCAN receipt") && !strings.Contains(line, "USING INDEX") {
				t.Fatalf("%s query performs an unindexed receipt scan:\n%s", name, plan)
			}
		}
	}
}

func memoryHomeQueryPlanDetails(t *testing.T, db *sql.DB, query string, arguments ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "\n")
}

func assertMemoryHomePlanUses(t *testing.T, plan string, indexes ...string) {
	t.Helper()
	for _, index := range indexes {
		if !strings.Contains(plan, index) {
			t.Fatalf("query plan does not use %s:\n%s", index, plan)
		}
	}
}

func insertMemoryHomePendingAttemptFixture(
	t *testing.T, s *Store, binding, suffix string, createdAt time.Time,
) {
	t.Helper()
	ledgerID := "ledger_" + suffix
	candidateID := "candidate_" + suffix
	receiptID := "receipt_" + suffix
	at := createdAt.UTC().Format(time.RFC3339Nano)
	if _, err := s.DB().Exec(`
		INSERT INTO turn_ledgers(
			ledger_id, finalize_receipt_id, host, session_id, turn_id, source_event_key,
			idempotency_key, binding_digest, destination_store_id, destination_class,
			policy_epoch, resolver_epoch, request_digest, state, created_at, finalized_at
		) VALUES (?, ?, 'codex', ?, ?, ?, ?, ?, ?, 'personal', 0, 0, ?, 'candidates', ?, ?)`,
		ledgerID, "finalize_"+suffix, "session_"+suffix, "turn_"+suffix,
		"event_"+suffix, "idempotency_"+suffix, binding, s.StoreID(),
		strings.Repeat("a", 64), at, at,
	); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"kind":"memory_capsule","capsule":{"items":[{"kind":"decision","redacted_summary":"%s"}]}}`, suffix)
	if _, err := s.DB().Exec(`
		INSERT INTO memory_tray_candidates(
			candidate_id, ledger_id, candidate_kind, version, content_digest, payload_json,
			state, grace_expires_at, created_at, updated_at
		) VALUES (?, ?, 'memory_capsule', 1, ?, ?, 'pending', ?, ?, ?)`,
		candidateID, ledgerID, strings.Repeat("b", 64), payload, at, at, at,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO memory_write_receipts(
			receipt_id, ledger_id, candidate_id, candidate_version, status,
			destination_class, destination_store_id, provenance_host,
			provenance_session_id, provenance_turn_id, provenance_source_event_key,
			content_digest, policy_epoch, resolver_epoch, measurement_method, created_at
		) VALUES (?, ?, ?, 1, 'pending', 'personal', ?, 'codex', ?, ?, ?, ?, 0, 0, 'not_measured', ?)`,
		receiptID, ledgerID, candidateID, s.StoreID(), "session_"+suffix, "turn_"+suffix,
		"event_"+suffix, strings.Repeat("b", 64), at,
	); err != nil {
		t.Fatal(err)
	}
}

func TestReadMemoryHomeDeliveryFactsIgnoresNewerSubagentTraffic(t *testing.T) {
	s := openContinuityDeliveryStore(t)
	base := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	sessionOffer := testContinuityOfferRequest()
	if _, err := s.RecordContinuityOffer(context.Background(), sessionOffer, base); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordContinuityHostObserved(
		context.Background(), testContinuityObservationRequest(sessionOffer), base.Add(time.Nanosecond),
	); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 101; index++ {
		subagent := testContinuityOfferRequest()
		subagent.Purpose = ContinuityDeliveryPurposeSubagentStart
		subagent.SessionRef = testContinuitySessionRef(fmt.Sprintf("subagent-session-%03d", index))
		subagent.SourceEventDigest = testContinuityDeliveryDigest(fmt.Sprintf("subagent-event-%03d", index))
		subagent.PayloadDigest = testContinuityDeliveryDigest(fmt.Sprintf("subagent-payload-%03d", index))
		subagent.ContextID = continuityDeliveryContextID(subagent)
		subagent.IdempotencyKey = continuityDeliveryOfferIdempotencyKey(subagent)
		if _, err := s.RecordContinuityOffer(context.Background(), subagent, base.Add(time.Duration(index+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	facts, err := s.ReadMemoryHomeDeliveryFacts(sessionOffer.RepositoryID, sessionOffer.BindingDigest, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("session proof facts=%d, want offer and observation: %#v", len(facts), facts)
	}
	for _, fact := range facts {
		if fact.ContextID != sessionOffer.ContextID || fact.Purpose != ContinuityDeliveryPurposeSessionStart {
			t.Fatalf("subagent delivery entered Memory Home proof: %#v", fact)
		}
	}
}

func memoryHomeFinalizeRequest(summary string, sequence int, binding string) TurnFinalizeRequest {
	req := trayFinalizeRequest(summary)
	req.SessionID = fmt.Sprintf("home_session_%03d", sequence)
	req.TurnID = fmt.Sprintf("home_turn_%03d", sequence)
	req.SourceEventKey = fmt.Sprintf("codex:home_session_%03d:home_turn_%03d:stop", sequence, sequence)
	req.IdempotencyKey = fmt.Sprintf("home_finalize_%03d", sequence)
	req.BindingDigest = binding
	return req
}

func TestProjectMemoryHomeDataKeepsEmptyVaultHonestAndActionable(t *testing.T) {
	got := ProjectMemoryHomeData(MemoryHomeProjectionInput{
		GeneratedAt: "2026-07-16T09:00:00Z",
		Boundary: MemoryHomeBoundary{
			StoreID: "store_personal_test", StoreKind: "personal",
			RepositoryID: "repository_pulse", BindingDigest: testMemoryHomeDigest("b"),
			Locality: "device_local", Privacy: "private",
		},
		LiveReadiness: MemoryHomeLiveReadiness{
			Outcome: MemoryHomeReadinessReady, ReasonCode: "live_product_ready",
			NextAction: MemoryHomeNextAction{Code: "continue_working", Label: "Continue working"},
		},
	})
	if got.Schema != MemoryHomeDataSchema || got.Memories.ActiveCount != 0 || len(got.Memories.LatestActive) != 0 {
		t.Fatalf("empty Home invented memory: %#v", got)
	}
	if got.Readiness.Outcome != MemoryHomeReadinessActionRequired || got.Readiness.ReasonCode != "first_memory_required" ||
		got.Readiness.NextAction.Code != "save_first_memory" {
		t.Fatalf("empty Home readiness=%#v", got.Readiness)
	}
	if got.Economy.State != MemoryHomeEconomyCollectingBaseline || got.Boundary.Privacy != "private" ||
		got.Boundary.RepositoryID != "repository_pulse" {
		t.Fatalf("empty Home boundary/economy=%#v", got)
	}
}

func TestProjectMemoryHomeDataRequiresExactFreshTaskOfferAndObservationForReady(t *testing.T) {
	active := MemoryHomeActiveMemory{
		ObjectID: "object_01", Kind: "decision", RedactedSummary: "Use receipt-backed continuity.",
		Host: "claude-code", SessionRef: "session_ref_memory", CreatedAt: "2026-07-16T08:00:00Z",
		TerminalReceiptID: "memory_receipt_01", PresentationReceiptID: "presentation_receipt_01",
	}
	delivery := memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T09:00:00Z")
	delivery[0].ObjectIDs = []string{active.ObjectID}
	delivery[1].ObjectIDs = []string{active.ObjectID}
	preview := &MemoryHomeNextTaskPreview{
		Status: "preview_only", PayloadDigest: testMemoryHomeDigest("d"), ObjectIDs: []string{active.ObjectID},
		RedactedResume: "Remember: Use receipt-backed continuity.",
		MethodID:       MemoryHomeCountMethodUTF8BytesDiv4Ceil, MethodVersion: "1",
	}
	preview.RenderedBytes = len([]byte(preview.RedactedResume))
	preview.PulseTokens = (preview.RenderedBytes + 3) / 4
	got := ProjectMemoryHomeData(MemoryHomeProjectionInput{
		GeneratedAt: "2026-07-16T09:02:00Z",
		Boundary: MemoryHomeBoundary{StoreID: "store_personal_test", StoreKind: "personal",
			RepositoryID: "repository_pulse", BindingDigest: testMemoryHomeDigest("b"), Locality: "device_local", Privacy: "private"},
		LiveReadiness: MemoryHomeLiveReadiness{Outcome: MemoryHomeReadinessReady, ReasonCode: "live_product_ready",
			NextAction: MemoryHomeNextAction{Code: "continue_working", Label: "Continue working"}},
		ActiveMemories: []MemoryHomeActiveMemory{active}, Deliveries: delivery,
		CurrentSessionRef: delivery[0].SessionRef, NextTaskPreview: preview,
	})
	if got.Memories.ActiveCount != 1 || len(got.Memories.LatestActive) != 1 ||
		len(got.Receipts.LatestTerminal) != 1 || got.Memories.LatestActive[0].ObjectID != active.ObjectID {
		t.Fatalf("canonical memory/receipt projection=%#v", got)
	}
	if got.Readiness.Outcome != MemoryHomeReadinessReady || got.Readiness.ReasonCode != "memory_continuity_ready" {
		t.Fatalf("readiness=%#v", got.Readiness)
	}
	if got.Readiness.Proof.TerminalReceiptID != active.TerminalReceiptID ||
		got.Readiness.Proof.PresentationReceiptID != active.PresentationReceiptID ||
		got.Readiness.Proof.ContextOfferReceiptID != delivery[0].ReceiptID ||
		got.Readiness.Proof.ContextAckReceiptID != delivery[1].ReceiptID ||
		got.Readiness.Proof.MemoryHost != "claude-code" || got.Readiness.Proof.DeliveryHost != "codex" {
		t.Fatalf("readiness proof lost immutable receipt chain: %#v", got.Readiness.Proof)
	}
	if got.Context.Selection != "current_task" || got.Context.LatestDelivery == nil ||
		got.Context.LatestDelivery.Acknowledgement != MemoryHomeDeliveryHostObserved ||
		got.Context.LatestDelivery.AckReceiptID != delivery[1].ReceiptID ||
		got.Context.LatestDelivery.Host != "codex" {
		t.Fatalf("current delivery=%#v", got.Context)
	}
	if got.NextTaskPreview == preview || got.NextTaskPreview == nil || got.NextTaskPreview.Status != "preview_only" {
		t.Fatalf("preview=%#v", got.NextTaskPreview)
	}
	preview.ObjectIDs[0] = "object_mutated"
	preview.RedactedResume = "mutated after projection"
	if got.NextTaskPreview.ObjectIDs[0] != active.ObjectID ||
		got.NextTaskPreview.RedactedResume != "Remember: Use receipt-backed continuity." {
		t.Fatalf("projection retained caller-owned preview memory: %#v", got.NextTaskPreview)
	}
}

func TestProjectMemoryHomeReadinessUsesExactProofOutsideLatestTwenty(t *testing.T) {
	latest := make([]MemoryHomeActiveMemory, 20)
	for index := range latest {
		latest[index] = MemoryHomeActiveMemory{
			ObjectID: fmt.Sprintf("object_latest_%02d", index), Host: "codex",
			SessionRef: fmt.Sprintf("session_latest_%02d", index), CreatedAt: "2026-07-16T10:00:00Z",
			TerminalReceiptID:     fmt.Sprintf("receipt_latest_%02d", index),
			PresentationReceiptID: fmt.Sprintf("presentation_latest_%02d", index),
		}
	}
	proven := MemoryHomeActiveMemory{
		ObjectID: "object_first_proven", Host: "codex", SessionRef: "session_first",
		CreatedAt: "2026-07-16T08:00:00Z", TerminalReceiptID: "receipt_first",
		PresentationReceiptID: "presentation_first",
	}
	deliveries := memoryHomeComparablePair("proof", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T11:00:00Z")
	deliveries[0].ObjectIDs = []string{proven.ObjectID}
	deliveries[1].ObjectIDs = []string{proven.ObjectID}
	deliveries[1].CreatedAt = "2026-07-16T11:00:00.1Z"

	got := ProjectMemoryHomeData(MemoryHomeProjectionInput{
		GeneratedAt:   "2026-07-16T12:00:00Z",
		Boundary:      MemoryHomeBoundary{RepositoryID: "repository_pulse", BindingDigest: testMemoryHomeDigest("b")},
		LiveReadiness: MemoryHomeLiveReadiness{Outcome: MemoryHomeReadinessReady},
		ActiveCount:   21, ActiveMemories: latest, ReadinessMemories: []MemoryHomeActiveMemory{proven},
		Deliveries: deliveries,
	})
	if got.Readiness.Outcome != MemoryHomeReadinessReady ||
		got.Readiness.Proof.TerminalReceiptID != proven.TerminalReceiptID ||
		got.Readiness.Proof.ContextAckReceiptID != deliveries[1].ReceiptID {
		t.Fatalf("older proven memory fell out of readiness: %#v", got.Readiness)
	}
	for _, memory := range got.Memories.LatestActive {
		if memory.ObjectID == proven.ObjectID {
			t.Fatal("fixture did not keep proven memory outside latest twenty")
		}
	}
}

func TestProjectMemoryHomeDataOmitsUnsafeOrInexactNextTaskPreview(t *testing.T) {
	preview := &MemoryHomeNextTaskPreview{
		Status: "preview_only", PayloadDigest: testMemoryHomeDigest("d"),
		RedactedResume: "/Users/example/.ssh/id_rsa", MethodID: MemoryHomeCountMethodUTF8BytesDiv4Ceil,
		MethodVersion: "1", RenderedBytes: len("/Users/example/.ssh/id_rsa"),
		PulseTokens: (len("/Users/example/.ssh/id_rsa") + 3) / 4,
	}
	got := ProjectMemoryHomeData(MemoryHomeProjectionInput{NextTaskPreview: preview})
	if got.NextTaskPreview != nil {
		t.Fatalf("unsafe preview crossed the read-model boundary: %#v", got.NextTaskPreview)
	}
}

func TestQueryMemoryHomeCanonicalFactsReadsOnlyActiveBoundPresentedObjects(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE turn_ledgers (ledger_id TEXT PRIMARY KEY, binding_digest TEXT NOT NULL)`,
		`CREATE TABLE memory_tray_candidates (candidate_id TEXT PRIMARY KEY, ledger_id TEXT NOT NULL, version INTEGER NOT NULL, content_digest TEXT NOT NULL, payload_json TEXT NOT NULL)`,
		`CREATE TABLE private_memory_objects (
			object_id TEXT PRIMARY KEY, candidate_kind TEXT NOT NULL, content_digest TEXT NOT NULL,
			created_from_candidate_id TEXT NOT NULL, created_at TEXT NOT NULL, lifecycle TEXT NOT NULL,
			memory_scope TEXT NOT NULL, project_namespace_id TEXT NOT NULL,
			original_repository_id TEXT NOT NULL, logical_generation INTEGER NOT NULL,
			modified_at TEXT NOT NULL, capture_host TEXT NOT NULL,
			capture_session_ref TEXT NOT NULL, captured_at TEXT NOT NULL
		)`,
		`CREATE TABLE memory_write_receipts (receipt_id TEXT PRIMARY KEY, candidate_id TEXT NOT NULL, candidate_version INTEGER NOT NULL, content_digest TEXT NOT NULL, object_id TEXT, status TEXT NOT NULL, provenance_host TEXT NOT NULL, provenance_session_id TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE memory_presentation_receipts (receipt_id TEXT PRIMARY KEY, candidate_id TEXT NOT NULL, candidate_version INTEGER NOT NULL, content_digest TEXT NOT NULL, binding_digest TEXT NOT NULL, presented_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	binding := testMemoryHomeDigest("b")
	projectNamespace := stableProjectNamespace("repository_pulse")
	payload := `{"kind":"memory_capsule","capsule":{"source":{"host":"codex","conversation_scope":"current_turn","timestamp":"2026-07-16T08:00:00Z"},"items":[{"kind":"decision","redacted_summary":"Use only canonical private memory.","privacy_tier":"normal"}]}}`
	if _, err := db.Exec(`INSERT INTO turn_ledgers VALUES ('ledger_01', ?), ('ledger_other', ?)`, binding, testMemoryHomeDigest("c")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_tray_candidates VALUES
		('candidate_01','ledger_01',1,?,?),
		('candidate_deleted','ledger_01',1,?,?),
		('candidate_other','ledger_other',1,?,?)`,
		testMemoryHomeDigest("1"), payload, testMemoryHomeDigest("2"), payload, testMemoryHomeDigest("3"), payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO private_memory_objects VALUES
		('object_01','memory_capsule',?,'candidate_01','2026-07-16T08:00:00Z','active','project',?,'repository_pulse',1,'2026-07-16T08:01:00Z','codex','session:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','2026-07-16T08:01:00Z'),
		('object_deleted','memory_capsule',?,'candidate_deleted','2026-07-16T08:00:00Z','deleted','project',?,'repository_pulse',2,'2026-07-16T08:01:00Z','codex','session:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','2026-07-16T08:01:00Z'),
		('object_other','memory_capsule',?,'candidate_other','2026-07-16T08:00:00Z','active','project',?,'repository_other',1,'2026-07-16T08:01:00Z','codex','session:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc','2026-07-16T08:01:00Z')`,
		testMemoryHomeDigest("1"), projectNamespace,
		testMemoryHomeDigest("2"), projectNamespace,
		testMemoryHomeDigest("3"), stableProjectNamespace("repository_other")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_write_receipts VALUES
		('write_01','candidate_01',1,?,'object_01','created','codex','session:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','2026-07-16T08:01:00Z'),
		('write_deleted','candidate_deleted',1,?,'object_deleted','created','codex','session:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','2026-07-16T08:01:00Z'),
		('write_other','candidate_other',1,?,'object_other','created','codex','session:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc','2026-07-16T08:01:00Z')`,
		testMemoryHomeDigest("1"), testMemoryHomeDigest("2"), testMemoryHomeDigest("3")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_presentation_receipts VALUES
		('presentation_01','candidate_01',1,?,?,'2026-07-16T08:00:30Z'),
		('presentation_deleted','candidate_deleted',1,?,?,'2026-07-16T08:00:30Z'),
		('presentation_other','candidate_other',1,?,?,'2026-07-16T08:00:30Z')`,
		testMemoryHomeDigest("1"), binding,
		testMemoryHomeDigest("2"), binding,
		testMemoryHomeDigest("3"), testMemoryHomeDigest("c")); err != nil {
		t.Fatal(err)
	}

	count, facts, err := queryMemoryHomeCanonicalFacts(db, binding, projectNamespace, 20)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(facts) != 1 {
		t.Fatalf("count=%d facts=%#v, want one bound active object", count, facts)
	}
	got := facts[0]
	if got.ObjectID != "object_01" || got.Kind != "decision" || got.RedactedSummary != "Use only canonical private memory." ||
		got.TerminalReceiptID != "write_01" || got.PresentationReceiptID != "presentation_01" ||
		got.SessionRef != "session:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("canonical Home fact lost receipt/provenance: %#v", got)
	}
	proofFacts, err := queryMemoryHomeReadinessFacts(db, binding, projectNamespace, []MemoryHomeDeliveryFact{{
		Acknowledgement: MemoryHomeDeliveryOfferedToHost, Purpose: MemoryHomeDeliveryPurposeSessionStart,
		BindingDigest: binding, ObjectIDs: []string{"object_01"},
	}})
	if err != nil || len(proofFacts) != 1 || proofFacts[0].ObjectID != "object_01" {
		t.Fatalf("exact readiness proof facts=%#v err=%v", proofFacts, err)
	}
}

func TestProjectMemoryHomeEconomyStartsCollectingWithoutReceipts(t *testing.T) {
	got := ProjectMemoryHomeEconomy(nil)
	if got.State != MemoryHomeEconomyCollectingBaseline {
		t.Fatalf("state=%q, want %q", got.State, MemoryHomeEconomyCollectingBaseline)
	}
	if got.Coverage.ComparablePairs != 0 || got.EstimatedAvoidedTokens != nil ||
		got.EstimatedReductionPercent != nil || got.Trend != "" || got.MeasuredAvoidedTokens != nil {
		t.Fatalf("empty economy invented evidence: %#v", got)
	}
}

func TestProjectMemoryHomeEconomyShowsExactLocalOfferWhileCollectingBaseline(t *testing.T) {
	got := ProjectMemoryHomeEconomy([]MemoryHomeDeliveryFact{{
		ReceiptID: "offer_receipt_01", ContextID: "context_01",
		Acknowledgement: MemoryHomeDeliveryOfferedToHost,
		Purpose:         MemoryHomeDeliveryPurposeSessionStart,
		PayloadDigest:   testMemoryHomeDigest("a"),
		MethodID:        MemoryHomeCountMethodUTF8BytesDiv4Ceil, MethodVersion: "1",
		RenderedBytes: 800, PulseTokens: 200,
		CreatedAt: "2026-07-16T09:00:00Z",
	}})
	if got.State != MemoryHomeEconomyCollectingBaseline || got.LatestOffer == nil {
		t.Fatalf("economy=%#v, want collecting with latest offer", got)
	}
	if got.LatestOffer.RenderedBytes != 800 || got.LatestOffer.PulseTokens != 200 ||
		got.LatestOffer.MethodID != MemoryHomeCountMethodUTF8BytesDiv4Ceil || got.LatestOffer.MethodVersion != "1" {
		t.Fatalf("latest exact offer lost: %#v", got.LatestOffer)
	}
	if got.EstimatedAvoidedTokens != nil {
		t.Fatalf("offer without baseline invented estimate: %#v", got)
	}
}

func TestProjectMemoryHomeEconomyUnlocksOnlyLabeledEstimateForOneComparablePair(t *testing.T) {
	facts := memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T09:00:00Z")
	got := ProjectMemoryHomeEconomy(facts)
	if got.State != MemoryHomeEconomyEstimated || got.Coverage.ComparablePairs != 1 {
		t.Fatalf("economy=%#v, want one estimated pair", got)
	}
	if got.EstimatedAvoidedTokens == nil || *got.EstimatedAvoidedTokens != 600 {
		t.Fatalf("estimated avoided=%v, want 600", got.EstimatedAvoidedTokens)
	}
	if got.LatestOffer != nil || got.Aggregate == nil ||
		got.Aggregate.MethodID != MemoryHomeCountMethodUTF8BytesDiv4Ceil || got.Aggregate.MethodVersion != "1" ||
		got.Aggregate.BaselineKind != MemoryHomeBaselineCanonicalStructured ||
		got.Aggregate.WindowStart != "2026-07-16T09:00:00Z" || got.Aggregate.WindowEnd != "2026-07-16T09:00:00Z" ||
		got.Aggregate.ComparablePairs != 1 || got.Aggregate.SourceEquivalentTokens != 800 ||
		got.Aggregate.PulseTokens != 200 || got.Aggregate.CountedObjects != 1 || got.Aggregate.TotalObjects != 1 {
		t.Fatalf("estimate lacks its own exact cohort explanation: %#v", got)
	}
	if got.EstimatedReductionPercent != nil || got.Trend != "" {
		t.Fatalf("one pair exposed aggregate claim: %#v", got)
	}
}

func TestProjectMemoryHomeEconomyOrdersFractionalRFC3339NanoTimestampsChronologically(t *testing.T) {
	facts := memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T12:00:00Z")
	facts[1].CreatedAt = "2026-07-16T12:00:00.1Z"

	got := ProjectMemoryHomeEconomy(facts)
	if got.State != MemoryHomeEconomyEstimated || got.Coverage.ComparablePairs != 1 {
		t.Fatalf("fractional observation was ordered lexically instead of chronologically: %#v", got)
	}
}

func TestProjectMemoryHomeEconomyDoesNotAttachUnrelatedLatestOfferToAggregate(t *testing.T) {
	facts := memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T08:00:00Z")
	facts = append(facts, MemoryHomeDeliveryFact{
		ReceiptID: "offer_unrelated", ContextID: "context_unrelated",
		Acknowledgement: MemoryHomeDeliveryOfferedToHost, Purpose: MemoryHomeDeliveryPurposeSessionStart,
		PayloadDigest: testMemoryHomeDigest("f"), MethodID: MemoryHomeCountMethodUTF8BytesDiv4Ceil,
		MethodVersion: "1", RenderedBytes: 4000, PulseTokens: 1000, CreatedAt: "2026-07-16T10:00:00Z",
	})

	got := ProjectMemoryHomeEconomy(facts)
	if got.LatestOffer != nil || got.Aggregate == nil || got.Aggregate.PulseTokens != 200 ||
		got.Aggregate.WindowEnd != "2026-07-16T08:00:00Z" {
		t.Fatalf("unrelated latest offer was presented as aggregate evidence: %#v", got)
	}
}

func TestProjectMemoryHomeEconomyRejectsObservationThatChangesOfferAccounting(t *testing.T) {
	facts := memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T09:00:00Z")
	facts[1].MethodVersion = "2"
	facts[1].PulseTokens = 1

	got := ProjectMemoryHomeEconomy(facts)
	if got.State != MemoryHomeEconomyCollectingBaseline || got.Coverage.ComparablePairs != 0 ||
		got.EstimatedAvoidedTokens != nil {
		t.Fatalf("mismatched observation became a comparable pair: %#v", got)
	}
}

func TestProjectMemoryHomeEconomyUnlocksPercentageAndTrendAtThreeComparablePairs(t *testing.T) {
	var facts []MemoryHomeDeliveryFact
	facts = append(facts, memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T08:00:00Z")...)
	facts = append(facts, memoryHomeComparablePair("02", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 250, 1000, "2026-07-16T08:10:00Z")...)
	facts = append(facts, memoryHomeComparablePair("03", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 300, 1200, "2026-07-16T08:20:00Z")...)

	got := ProjectMemoryHomeEconomy(facts)
	if got.State != MemoryHomeEconomyEstimated || got.Coverage.ComparablePairs != 3 {
		t.Fatalf("economy=%#v, want three estimated pairs", got)
	}
	if got.EstimatedReductionPercent == nil || *got.EstimatedReductionPercent != 75 {
		t.Fatalf("reduction percent=%v, want 75", got.EstimatedReductionPercent)
	}
	if got.Trend != MemoryHomeEconomyTrendFlat {
		t.Fatalf("trend=%q, want flat", got.Trend)
	}
}

func TestProjectMemoryHomeEconomyTrendUsesReceiptTimeNotReaderOrder(t *testing.T) {
	var facts []MemoryHomeDeliveryFact
	facts = append(facts, memoryHomeComparablePair("03", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 1000, "2026-07-16T08:20:00Z")...)
	facts = append(facts, memoryHomeComparablePair("02", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 300, 1000, "2026-07-16T08:10:00Z")...)
	facts = append(facts, memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 400, 800, "2026-07-16T08:00:00Z")...)

	got := ProjectMemoryHomeEconomy(facts)
	if got.Trend != MemoryHomeEconomyTrendUp {
		t.Fatalf("trend=%q, want up from chronological receipt order", got.Trend)
	}
}

func TestProjectMemoryHomeEconomyReportsDownwardTrend(t *testing.T) {
	var facts []MemoryHomeDeliveryFact
	facts = append(facts, memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 1000, "2026-07-16T08:00:00Z")...)
	facts = append(facts, memoryHomeComparablePair("02", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 300, 1000, "2026-07-16T08:10:00Z")...)
	facts = append(facts, memoryHomeComparablePair("03", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 400, 800, "2026-07-16T08:20:00Z")...)

	got := ProjectMemoryHomeEconomy(facts)
	if got.Trend != MemoryHomeEconomyTrendDown {
		t.Fatalf("trend=%q, want down", got.Trend)
	}
}

func TestProjectMemoryHomeEconomyExcludesIncompatibleMethodAndBaselineCohorts(t *testing.T) {
	facts := memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T08:00:00Z")
	facts = append(facts, memoryHomeComparablePair("02", "legacy_guess", "1", 10, 1000, "2026-07-16T08:10:00Z")...)
	otherBaseline := memoryHomeComparablePair("03", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 10, 1000, "2026-07-16T08:20:00Z")
	otherBaseline[0].BaselineKind = "legacy_import_guess"
	otherBaseline[1].BaselineKind = "legacy_import_guess"
	facts = append(facts, otherBaseline...)

	got := ProjectMemoryHomeEconomy(facts)
	if got.State != MemoryHomeEconomyEstimated || got.Coverage.ComparablePairs != 1 || got.Coverage.ExcludedPairs != 2 {
		t.Fatalf("economy cohort accounting=%#v, want 1 compatible and 2 excluded", got)
	}
	if got.EstimatedAvoidedTokens == nil || *got.EstimatedAvoidedTokens != 600 ||
		got.EstimatedReductionPercent != nil || got.Trend != "" {
		t.Fatalf("mixed cohorts leaked into estimate: %#v", got)
	}
}

func TestProjectMemoryHomeEconomyIsUnavailableWhenOnlyIncompatiblePairsExist(t *testing.T) {
	facts := memoryHomeComparablePair("01", "legacy_guess", "1", 200, 800, "2026-07-16T08:00:00Z")

	got := ProjectMemoryHomeEconomy(facts)
	if got.State != MemoryHomeEconomyUnavailable || got.Coverage.ComparablePairs != 0 || got.Coverage.ExcludedPairs != 1 ||
		got.EstimatedAvoidedTokens != nil {
		t.Fatalf("unsupported receipt method was presented as comparable: %#v", got)
	}
}

func TestProjectMemoryHomeEconomyPromotesOnlyVerifiedProviderEvidenceToMeasured(t *testing.T) {
	facts := memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T08:00:00Z")
	facts[1].ProviderActualInputTokens = 190
	facts[1].ProviderActualSource = "codex_provider_usage_v1"
	facts[1].ProviderEvidenceDigest = testMemoryHomeDigest("c")
	facts[1].ProviderEvidenceVerified = true

	got := ProjectMemoryHomeEconomy(facts)
	if got.State != MemoryHomeEconomyMeasured || got.MeasuredAvoidedTokens == nil || *got.MeasuredAvoidedTokens != 610 {
		t.Fatalf("verified provider evidence was not measured: %#v", got)
	}
	if got.MeasuredSource != "codex_provider_usage_v1" {
		t.Fatalf("measured source=%q", got.MeasuredSource)
	}
}

func TestProjectMemoryHomeEconomyExcludesMixedVerifiedProviderSources(t *testing.T) {
	facts := memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T08:00:00Z")
	second := memoryHomeComparablePair("02", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T08:10:00Z")
	facts[1].ProviderActualInputTokens = 190
	facts[1].ProviderActualSource = "codex_provider_usage_v1"
	facts[1].ProviderEvidenceDigest = testMemoryHomeDigest("c")
	facts[1].ProviderEvidenceVerified = true
	second[1].ProviderActualInputTokens = 180
	second[1].ProviderActualSource = "claude_provider_usage_v1"
	second[1].ProviderEvidenceDigest = testMemoryHomeDigest("d")
	second[1].ProviderEvidenceVerified = true
	facts = append(facts, second...)

	got := ProjectMemoryHomeEconomy(facts)
	if got.State != MemoryHomeEconomyMeasured || got.Coverage.ComparablePairs != 2 || got.Coverage.MeasuredPairs != 1 ||
		got.MeasuredSource != "codex_provider_usage_v1" || got.MeasuredAvoidedTokens == nil || *got.MeasuredAvoidedTokens != 610 {
		t.Fatalf("mixed verified provider sources leaked into one measured cohort: %#v", got)
	}
}

func TestProjectMemoryHomeEconomyDoesNotPromoteUnverifiedProviderNumbers(t *testing.T) {
	facts := memoryHomeComparablePair("01", MemoryHomeCountMethodUTF8BytesDiv4Ceil, "1", 200, 800, "2026-07-16T08:00:00Z")
	facts[1].ProviderActualInputTokens = 1
	facts[1].ProviderActualSource = ""
	facts[1].ProviderEvidenceDigest = ""
	facts[1].ProviderEvidenceVerified = false

	got := ProjectMemoryHomeEconomy(facts)
	if got.State != MemoryHomeEconomyEstimated || got.MeasuredAvoidedTokens != nil || got.MeasuredSource != "" {
		t.Fatalf("unverified provider numbers became measured: %#v", got)
	}
}

func memoryHomeComparablePair(id, methodID, methodVersion string, pulseTokens, sourceTokens int, at string) []MemoryHomeDeliveryFact {
	offer := MemoryHomeDeliveryFact{
		ReceiptID: "offer_receipt_" + id, ContextID: "context_" + id,
		Acknowledgement: MemoryHomeDeliveryOfferedToHost,
		Purpose:         MemoryHomeDeliveryPurposeSessionStart,
		PayloadDigest:   testMemoryHomeDigest("a"),
		BindingDigest:   testMemoryHomeDigest("b"), RepositoryID: "repository_pulse",
		Host: "codex", SessionRef: "session_ref_" + id,
		ObjectIDs: []string{"object_" + id}, EvidenceIDs: []string{"evidence_" + id},
		MethodID: methodID, MethodVersion: methodVersion,
		RenderedBytes: pulseTokens * 4, PulseTokens: pulseTokens,
		SourceEquivalentTokens: sourceTokens, BaselineKind: MemoryHomeBaselineCanonicalStructured,
		CoverageCounted: 1, CoverageTotal: 1,
		CreatedAt: at,
	}
	observed := offer
	observed.ReceiptID = "observed_receipt_" + id
	observed.Acknowledgement = MemoryHomeDeliveryHostObserved
	observed.CreatedAt = "2026-07-16T09:01:00Z"
	return []MemoryHomeDeliveryFact{offer, observed}
}

func testMemoryHomeDigest(character string) string {
	result := ""
	for len(result) < 64 {
		result += character
	}
	return result
}
