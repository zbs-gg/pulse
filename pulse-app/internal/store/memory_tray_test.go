package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testTrayBindingDigest = localStoreBindingDigest("store_personal_test")

func openPersonalTrayStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "personal.db")
	s, err := OpenVault(path, StoreKindPersonal, "store_personal_test")
	if err != nil {
		t.Fatalf("open personal: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func safeTrayCapsule(summary string) MemoryCapsule {
	return MemoryCapsule{
		Schema: MemoryCapsuleSchema,
		Source: CapsuleSource{
			Host: "codex", ConversationScope: "current_turn", Timestamp: "2026-07-14T09:00:00Z",
		},
		Items: []MemoryCapsuleItem{{
			Kind: "decision", RedactedSummary: summary, Confidence: 0.94,
			EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
			Tags: []string{"pulse", "tray"},
		}},
	}
}

func trayFinalizeRequest(summary string) TurnFinalizeRequest {
	return TurnFinalizeRequest{
		Schema: TurnFinalizeRequestSchema,
		Host:   "codex", SessionID: "session_01", TurnID: "turn_01",
		SourceEventKey: "codex:session_01:turn_01:stop", IdempotencyKey: "finalize_01",
		BindingDigest: testTrayBindingDigest, PolicyEpoch: 0, ResolverEpoch: 0,
		Candidates: []PrivateMemoryCandidate{{
			Kind: PrivateMemoryCandidateCapsule, Capsule: ptr(safeTrayCapsule(summary)),
		}},
	}
}

func ptr[T any](value T) *T { return &value }

func presentTrayReceipt(t *testing.T, s *Store, receipt MemoryWriteReceipt, now time.Time, grace time.Duration) MemoryPresentationReceipt {
	t.Helper()
	presented, err := s.PresentMemoryTrayCandidate(MemoryPresentationRequest{
		CandidateID: receipt.CandidateID, CandidateVersion: receipt.CandidateVersion,
		ContentDigest: receipt.ContentDigest, BindingDigest: testTrayBindingDigest,
		TrustedSurfaceKind: "memory_home", TrustedSurfaceInstance: "home_session_test",
	}, now, grace)
	if err != nil {
		t.Fatalf("present candidate %s v%d: %v", receipt.CandidateID, receipt.CandidateVersion, err)
	}
	return presented
}

func reopenPersonalTrayWithStalePendingCandidate(t *testing.T) (*Store, MemoryWriteReceipt) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "personal-binding-scope.db")
	const storeID = "store_personal_binding_scope_test"
	bindingA := strings.Repeat("a", 64)
	first, err := OpenVault(path, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatalf("open binding A: %v", err)
	}
	if err := first.ConfigureProductRuntimeAuthority(bindingA, 1, 2); err != nil {
		_ = first.Close()
		t.Fatalf("configure binding A: %v", err)
	}
	req := trayFinalizeRequest("Binding A private candidate must never appear under binding B.")
	req.BindingDigest = bindingA
	req.PolicyEpoch = 1
	req.ResolverEpoch = 2
	result, err := first.FinalizeTurn(req, time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC), 10*time.Second)
	if err != nil {
		_ = first.Close()
		t.Fatalf("finalize binding A: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close binding A: %v", err)
	}

	second, err := OpenVault(path, StoreKindPersonal, storeID)
	if err != nil {
		t.Fatalf("open binding B: %v", err)
	}
	if err := second.ConfigureProductRuntimeAuthority(strings.Repeat("b", 64), 3, 4); err != nil {
		_ = second.Close()
		t.Fatalf("configure binding B: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	return second, result.Receipts[0]
}

func TestPendingMemoryTrayCandidateReadsStayBoundedAndReceiptFree(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	result, err := s.FinalizeTurn(trayFinalizeRequest("Render this pending memory in Home."), now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := result.Receipts[0]

	pending, err := s.ListPendingMemoryTrayCandidates(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].CandidateID != want.CandidateID || pending[0].Version != want.CandidateVersion ||
		pending[0].ContentDigest != want.ContentDigest || pending[0].Candidate.Capsule == nil {
		t.Fatalf("pending candidates=%#v", pending)
	}
	exact, err := s.GetPendingMemoryTrayCandidate(want.CandidateID, want.CandidateVersion)
	if err != nil || exact.CandidateID != want.CandidateID || exact.ContentDigest != want.ContentDigest {
		t.Fatalf("exact pending=%#v err=%v", exact, err)
	}
	if _, err := s.GetPendingMemoryTrayCandidate(want.CandidateID, want.CandidateVersion+1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong version error=%v, want sql.ErrNoRows", err)
	}
	if _, err := s.CancelMemoryTrayCandidate(want.CandidateID, want.CandidateVersion, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.ListPendingMemoryTrayCandidates(10); err != nil || len(pending) != 0 {
		t.Fatalf("canceled pending=%#v err=%v", pending, err)
	}
}

func TestPendingMemoryTrayHomeReadsHideStaleRuntimeAuthority(t *testing.T) {
	s, stale := reopenPersonalTrayWithStalePendingCandidate(t)

	pending, err := s.ListPendingMemoryTrayCandidates(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("foreign runtime candidate leaked from list: %#v", pending)
	}
	if candidate, err := s.GetPendingMemoryTrayCandidate(stale.CandidateID, stale.CandidateVersion); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign runtime get returned candidate=%#v err=%v, want sql.ErrNoRows", candidate, err)
	}

	current := trayFinalizeRequest("Binding B current candidate remains visible and controllable.")
	current.SessionID = "session_binding_b"
	current.TurnID = "turn_binding_b"
	current.SourceEventKey = "codex:session_binding_b:turn_binding_b:stop"
	current.IdempotencyKey = "finalize_binding_b"
	current.BindingDigest = strings.Repeat("b", 64)
	current.PolicyEpoch = 3
	current.ResolverEpoch = 4
	result, err := s.FinalizeTurn(current, time.Date(2026, 7, 16, 10, 1, 0, 0, time.UTC), 10*time.Second)
	if err != nil {
		t.Fatalf("finalize current binding: %v", err)
	}
	pending, err = s.ListPendingMemoryTrayCandidates(10)
	if err != nil || len(pending) != 1 || pending[0].CandidateID != result.Receipts[0].CandidateID {
		t.Fatalf("current binding pending=%#v err=%v", pending, err)
	}
	if exact, err := s.GetPendingMemoryTrayCandidate(result.Receipts[0].CandidateID, 1); err != nil || exact.CandidateID != result.Receipts[0].CandidateID {
		t.Fatalf("current binding exact=%#v err=%v", exact, err)
	}
}

func TestPendingMemoryTrayControlsRejectStaleRuntimeAuthorityBeforeMutation(t *testing.T) {
	type candidateSnapshot struct {
		State, Digest, Payload, GraceExpires, UpdatedAt string
		Version                                         int
	}
	readSnapshot := func(t *testing.T, s *Store, candidateID string) candidateSnapshot {
		t.Helper()
		var snapshot candidateSnapshot
		if err := s.DB().QueryRow(`
			SELECT state, version, content_digest, payload_json, grace_expires_at, updated_at
			  FROM memory_tray_candidates WHERE candidate_id=?`, candidateID,
		).Scan(&snapshot.State, &snapshot.Version, &snapshot.Digest, &snapshot.Payload, &snapshot.GraceExpires, &snapshot.UpdatedAt); err != nil {
			t.Fatalf("read candidate snapshot: %v", err)
		}
		return snapshot
	}

	for _, control := range []struct {
		name string
		run  func(*Store, MemoryWriteReceipt) error
	}{
		{
			name: "edit",
			run: func(s *Store, stale MemoryWriteReceipt) error {
				_, err := s.EditMemoryTrayCandidate(
					stale.CandidateID, stale.CandidateVersion,
					PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: ptr(safeTrayCapsule("Foreign binding edit must fail closed."))},
					time.Date(2026, 7, 16, 10, 2, 0, 0, time.UTC), 10*time.Second,
				)
				return err
			},
		},
		{
			name: "cancel",
			run: func(s *Store, stale MemoryWriteReceipt) error {
				_, err := s.CancelMemoryTrayCandidate(
					stale.CandidateID, stale.CandidateVersion,
					time.Date(2026, 7, 16, 10, 2, 0, 0, time.UTC),
				)
				return err
			},
		},
	} {
		t.Run(control.name, func(t *testing.T) {
			s, stale := reopenPersonalTrayWithStalePendingCandidate(t)
			before := readSnapshot(t, s, stale.CandidateID)
			if err := control.run(s, stale); !errors.Is(err, ErrProductRuntimeMismatch) {
				t.Fatalf("foreign runtime %s error=%v, want ErrProductRuntimeMismatch", control.name, err)
			}
			after := readSnapshot(t, s, stale.CandidateID)
			if after != before {
				t.Fatalf("foreign runtime %s mutated candidate: before=%#v after=%#v", control.name, before, after)
			}
		})
	}
}

func TestFinalizeThenImmediateAtomicCommitReturnsTruthfulStableReceipts(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	req := trayFinalizeRequest("Use one durable receipt chain for every private memory write.")

	first, err := s.FinalizeTurn(req, now, 10*time.Second)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if first.Status != TurnFinalizedCandidates || len(first.Receipts) != 1 {
		t.Fatalf("unexpected finalize: %#v", first)
	}
	pending := first.Receipts[0]
	if pending.Status != MemoryWritePending || pending.ReceiptID == "" || pending.ObjectID != "" {
		t.Fatalf("pending receipt lies about object: %#v", pending)
	}
	if pending.DestinationStoreID != s.StoreID() || pending.SafeProvenance.Host != req.Host ||
		pending.SafeProvenance.SessionID != opaqueTurnCorrelation("session", req.SessionID) ||
		pending.SafeProvenance.TurnID != opaqueTurnCorrelation("turn", req.TurnID) ||
		pending.SafeProvenance.SourceEventKey != opaqueTurnCorrelation("event", req.SourceEventKey) {
		t.Fatalf("pending receipt lost safe provenance: %#v", pending)
	}
	var rawIdentifiers int
	if err := s.DB().QueryRow(`
		SELECT count(*) FROM turn_ledgers
		 WHERE session_id=? OR turn_id=? OR source_event_key=? OR idempotency_key=?`,
		req.SessionID, req.TurnID, req.SourceEventKey, req.IdempotencyKey,
	).Scan(&rawIdentifiers); err != nil || rawIdentifiers != 0 {
		t.Fatalf("raw envelope identifiers survived: count=%d err=%v", rawIdentifiers, err)
	}
	var canonical int
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&canonical); err != nil || canonical != 0 {
		t.Fatalf("pending reached canonical storage: count=%d err=%v", canonical, err)
	}
	committed, err := s.CommitMemoryTrayCandidate(pending.CandidateID, pending.CandidateVersion, now)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if committed.Status != MemoryWriteCreated || committed.ObjectID == "" || committed.ReceiptID == pending.ReceiptID {
		t.Fatalf("unexpected committed receipt: %#v", committed)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&canonical); err != nil || canonical != 1 {
		t.Fatalf("canonical commit count=%d err=%v", canonical, err)
	}
	var outbox, audit, idempotency int
	if err := s.DB().QueryRow(`SELECT count(*) FROM private_projection_outbox WHERE object_id=?`, committed.ObjectID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_write_audit WHERE receipt_id=?`, committed.ReceiptID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_write_idempotency WHERE receipt_id=?`, committed.ReceiptID).Scan(&idempotency); err != nil {
		t.Fatal(err)
	}
	if outbox != 1 || audit != 1 || idempotency != 1 {
		t.Fatalf("non-atomic metadata: outbox=%d audit=%d idempotency=%d", outbox, audit, idempotency)
	}

	retry, err := s.FinalizeTurn(req, now.Add(time.Minute), 10*time.Second)
	if err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	if len(retry.Receipts) != 1 || retry.Receipts[0].ReceiptID != committed.ReceiptID || retry.Receipts[0].ObjectID != committed.ObjectID {
		t.Fatalf("response-loss retry diverged: first=%#v retry=%#v", committed, retry)
	}
}

func TestUnsafeFinalizePersistsOnlyContentFreeRejection(t *testing.T) {
	s, path := openPersonalTrayStore(t)
	secret := "sk-ULTRAPRIVATE-NEVER-PERSIST-123456789"
	req := trayFinalizeRequest(secret)
	result, err := s.FinalizeTurn(req, time.Now().UTC(), 10*time.Second)
	if err != nil {
		t.Fatalf("unsafe finalize should return a durable rejection: %v", err)
	}
	if len(result.Receipts) != 1 || result.Receipts[0].Status != MemoryWriteRejected || result.Receipts[0].ReasonCode != "unsafe_payload" {
		t.Fatalf("bad rejection: %#v", result)
	}
	var candidates int
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_tray_candidates`).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("unsafe candidate persisted: count=%d err=%v", candidates, err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		body, err := os.ReadFile(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("unsafe payload bytes survived in %s", candidate)
		}
	}
}

func TestSingleRawRoleMessageIsRejectedBeforePersistence(t *testing.T) {
	for _, raw := range []string{
		"User: paste the complete private prompt here verbatim",
		"Assistant: here is the complete answer copied verbatim",
		`{"role":"user","content":"paste the private prompt verbatim"}`,
	} {
		t.Run(raw[:min(len(raw), 12)], func(t *testing.T) {
			s, _ := openPersonalTrayStore(t)
			result, err := s.FinalizeTurn(trayFinalizeRequest(raw), time.Now().UTC(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != TurnFinalizedRejected || len(result.Receipts) != 1 || result.Receipts[0].Status != MemoryWriteRejected {
				t.Fatalf("raw role message was accepted: %#v", result)
			}
			var candidates int
			if err := s.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates`).Scan(&candidates); err != nil || candidates != 0 {
				t.Fatalf("raw role message persisted: count=%d err=%v", candidates, err)
			}
		})
	}
}

func TestDangerousPathsAndCredentialsNeverReachCandidateOrWAL(t *testing.T) {
	unsafeValues := []string{
		"/Volumes/Private/client/project.txt",
		"/home/nik/.ssh/id_ed25519",
		"../../.ssh/config",
		`C:\Users\nik\.ssh\id_rsa`,
		"Authorization: Bearer abcDEF1234567890.token",
		"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJwcml2YXRlIn0.c2lnbmF0dXJlMTIzNDU2",
		"AIzaSyD3m0CredentialThatMustNeverPersist",
		"gho_1234567890abcdefghijklmnopqrstuvwxyz",
		"Configuration is stored in /etc/pulse/config.json.",
		"Temporary state is stored in /tmp/pulse/private-state.json.",
		"Decision is documented at /acme/internal/repo/plan.md.",
		"Decision is documented at /acme/plan.md.",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"Hidden\u0000control byte",
		"Direction override \u202esecret.txt",
		"Zero width credential gho\u200b_1234567890abcdefghijklmnopqrstuvwxyz",
	}
	for index, unsafeValue := range unsafeValues {
		t.Run(fmt.Sprintf("case_%02d", index), func(t *testing.T) {
			s, path := openPersonalTrayStore(t)
			result, err := s.FinalizeTurn(trayFinalizeRequest(unsafeValue), time.Now().UTC(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != TurnFinalizedRejected || len(result.Receipts) != 1 || result.Receipts[0].Status != MemoryWriteRejected {
				t.Fatalf("unsafe value was accepted: %#v", result)
			}
			var candidates int
			if err := s.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates`).Scan(&candidates); err != nil || candidates != 0 {
				t.Fatalf("unsafe candidate persisted: count=%d err=%v", candidates, err)
			}
			for _, databasePath := range []string{path, path + "-wal", path + "-shm"} {
				body, err := os.ReadFile(databasePath)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					t.Fatal(err)
				}
				if bytes.Contains(body, []byte(unsafeValue)) {
					t.Fatalf("unsafe bytes reached %s", databasePath)
				}
			}
		})
	}
	s, _ := openPersonalTrayStore(t)
	for index, benign := range []string{
		"Use read/write terminology and A/B testing in the plan.",
		"The public endpoint /api/v1 remains stable.",
	} {
		req := trayFinalizeRequest(benign)
		req.TurnID = fmt.Sprintf("turn_benign_%d", index)
		req.SourceEventKey = fmt.Sprintf("codex:session_01:turn_benign_%d:stop", index)
		req.IdempotencyKey = fmt.Sprintf("finalize_benign_%d", index)
		result, err := s.FinalizeTurn(req, time.Now().UTC(), time.Second)
		if err != nil || result.Status != TurnFinalizedCandidates {
			t.Fatalf("benign slash prose rejected: value=%q result=%#v err=%v", benign, result, err)
		}
	}
}

func TestUnsafeSemanticMetadataAndBiometricsAreContentFreeRejected(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SemanticDelta)
	}{
		{"source_project_path", func(delta *SemanticDelta) { delta.Source.ProjectID = "../../.ssh/config" }},
		{"source_thread_secret", func(delta *SemanticDelta) { delta.Source.ThreadID = "gho_1234567890abcdefghijklmnopqrstuvwxyz" }},
		{"biometric_string", func(delta *SemanticDelta) {
			trend := "/Users/nik/private-health.txt"
			delta.Events[0].Biometrics = &SemanticBiometrics{HRTrend: &trend}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, _ := openPersonalTrayStore(t)
			req := trayFinalizeRequest("safe placeholder")
			delta := validSemanticDelta()
			delta.Source.Host = "codex"
			delta.Source.SessionID = req.SessionID
			test.mutate(&delta)
			req.Candidates = []PrivateMemoryCandidate{{Kind: PrivateMemoryCandidateSemanticDelta, SemanticDelta: &delta}}
			result, err := s.FinalizeTurn(req, time.Now().UTC(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != TurnFinalizedRejected || result.Receipts[0].Status != MemoryWriteRejected {
				t.Fatalf("unsafe semantic metadata accepted: %#v", result)
			}
			var candidates int
			if err := s.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates`).Scan(&candidates); err != nil || candidates != 0 {
				t.Fatalf("unsafe semantic candidate persisted: count=%d err=%v", candidates, err)
			}
		})
	}
}

func TestProductSemanticClaimsFailClosedInsteadOfSilentlyDroppingProjection(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	req := trayFinalizeRequest("safe placeholder")
	delta := validSemanticDelta()
	delta.Source.Host = "codex"
	delta.Source.SessionID = req.SessionID
	delta.Events[0].Claims = []SemanticClaim{{
		Subject: "Pulse", Predicate: "status", Object: "private-pilot-ready", ChangeCue: true,
	}}
	req.Candidates = []PrivateMemoryCandidate{{Kind: PrivateMemoryCandidateSemanticDelta, SemanticDelta: &delta}}
	result, err := s.FinalizeTurn(req, time.Now().UTC(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != TurnFinalizedRejected || result.Receipts[0].Status != MemoryWriteRejected {
		t.Fatalf("claims were accepted without a governed projection: %#v", result)
	}
	var candidates, assertions int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM assertions`).Scan(&assertions); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 || assertions != 0 {
		t.Fatalf("rejected claims persisted candidates=%d assertions=%d", candidates, assertions)
	}
}

func TestPendingEditAndCancelCASIsTerminal(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	result, err := s.FinalizeTurn(trayFinalizeRequest("Keep the first candidate private."), now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pending := result.Receipts[0]
	edited, err := s.EditMemoryTrayCandidate(
		pending.CandidateID, pending.CandidateVersion,
		PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: ptr(safeTrayCapsule("Keep the edited candidate private and inspectable."))},
		now.Add(5*time.Second), 10*time.Second,
	)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Status != MemoryWritePending || edited.CandidateVersion != pending.CandidateVersion+1 || edited.ContentDigest == pending.ContentDigest {
		t.Fatalf("edit did not create a new version/digest: %#v", edited)
	}
	editRetry, err := s.EditMemoryTrayCandidate(
		pending.CandidateID, pending.CandidateVersion,
		PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: ptr(safeTrayCapsule("Keep the edited candidate private and inspectable."))},
		now.Add(5*time.Second), 10*time.Second,
	)
	if err != nil || editRetry.ReceiptID != edited.ReceiptID {
		t.Fatalf("lost-response edit retry diverged: retry=%#v err=%v", editRetry, err)
	}
	if _, err := s.CommitMemoryTrayCandidate(edited.CandidateID, pending.CandidateVersion, now.Add(20*time.Second)); !errors.Is(err, ErrMemoryTrayVersionConflict) {
		t.Fatalf("stale commit err=%v, want version conflict", err)
	}
	canceled, err := s.CancelMemoryTrayCandidate(edited.CandidateID, edited.CandidateVersion, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != MemoryWriteCanceled || canceled.ObjectID != "" {
		t.Fatalf("cancel receipt: %#v", canceled)
	}
	cancelRetry, err := s.CancelMemoryTrayCandidate(edited.CandidateID, edited.CandidateVersion, now.Add(7*time.Second))
	if err != nil || cancelRetry.ReceiptID != canceled.ReceiptID {
		t.Fatalf("lost-response cancel retry diverged: retry=%#v err=%v", cancelRetry, err)
	}
	if _, err := s.CommitMemoryTrayCandidate(edited.CandidateID, edited.CandidateVersion, now.Add(time.Minute)); !errors.Is(err, ErrMemoryTrayTerminal) {
		t.Fatalf("commit after cancel err=%v, want terminal", err)
	}
}

func TestReceiptHistoryKeepsNewestFiftyIncludingTerminalOutcome(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	result, err := s.FinalizeTurn(trayFinalizeRequest("Receipt history version 00 remains bounded."), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	receipt := result.Receipts[0]
	for index := 1; index <= 55; index++ {
		receipt, err = s.EditMemoryTrayCandidate(
			receipt.CandidateID, receipt.CandidateVersion,
			PrivateMemoryCandidate{
				Kind:    PrivateMemoryCandidateCapsule,
				Capsule: ptr(safeTrayCapsule(fmt.Sprintf("Receipt history version %02d remains bounded.", index))),
			},
			now.Add(time.Duration(index)*time.Millisecond), time.Second,
		)
		if err != nil {
			t.Fatalf("edit %d: %v", index, err)
		}
	}
	canceled, err := s.CancelMemoryTrayCandidate(
		receipt.CandidateID, receipt.CandidateVersion, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	tray, err := s.ListMemoryTray(10)
	if err != nil || len(tray) != 1 {
		t.Fatalf("tray=%#v err=%v", tray, err)
	}
	if len(tray[0].ReceiptHistory) != 50 {
		t.Fatalf("receipt history length=%d, want 50", len(tray[0].ReceiptHistory))
	}
	latest := tray[0].ReceiptHistory[len(tray[0].ReceiptHistory)-1]
	if latest.ReceiptID != canceled.ReceiptID || latest.Status != MemoryWriteCanceled {
		t.Fatalf("terminal receipt fell out of bounded history: latest=%#v canceled=%#v", latest, canceled)
	}
	if tray[0].ReceiptHistory[0].CandidateVersion >= latest.CandidateVersion {
		t.Fatalf("receipt history is not chronological: first=%#v latest=%#v", tray[0].ReceiptHistory[0], latest)
	}
}

func TestNoChangeAndCandidateFinalizationCannotCoexist(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Now().UTC()
	req := trayFinalizeRequest("A candidate exists for this turn.")
	if _, err := s.FinalizeTurn(req, now, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinalizeTurnNoChange(TurnNoChangeRequest{
		Schema: TurnNoChangeRequestSchema,
		Host:   req.Host, SessionID: req.SessionID, TurnID: req.TurnID,
		SourceEventKey: req.SourceEventKey, IdempotencyKey: "no_change_01",
		BindingDigest: req.BindingDigest, PolicyEpoch: req.PolicyEpoch, ResolverEpoch: req.ResolverEpoch,
	}, now.Add(time.Second)); !errors.Is(err, ErrTurnAlreadyFinalized) {
		t.Fatalf("no-change after candidate err=%v", err)
	}
}

func TestCommitRevalidatesPreviewedPayloadAndFailsClosedOnTamper(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Now().UTC()
	finalized, err := s.FinalizeTurn(trayFinalizeRequest("The previewed value is canonical."), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	candidateID := finalized.Receipts[0].CandidateID
	presentTrayReceipt(t, s, finalized.Receipts[0], now, time.Second)
	tampered := PrivateMemoryCandidate{
		Kind:    PrivateMemoryCandidateCapsule,
		Capsule: ptr(safeTrayCapsule("A different but still safe value was inserted after preview.")),
	}
	payload, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE memory_tray_candidates SET payload_json=? WHERE candidate_id=?`, string(payload), candidateID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitMemoryTrayCandidate(candidateID, 1, now.Add(2*time.Second)); err == nil {
		t.Fatal("tampered candidate committed under the previewed digest")
	}
	var objects, capsules int
	if err := s.DB().QueryRow(`SELECT count(*) FROM private_memory_objects`).Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&capsules); err != nil {
		t.Fatal(err)
	}
	if objects != 0 || capsules != 0 {
		t.Fatalf("tampered commit leaked canonical rows: objects=%d capsules=%d", objects, capsules)
	}
}

func TestCommitWorkerFailureIsDurableContentFreeAndIdempotent(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Now().UTC()
	finalized, err := s.FinalizeTurn(trayFinalizeRequest("A worker failure must terminate truthfully."), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pending := finalized.Receipts[0]
	failed, err := s.FailMemoryTrayCandidate(pending.CandidateID, pending.CandidateVersion, "commit_failed", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != MemoryWriteFailed || failed.ObjectID != "" || failed.ReasonCode != "commit_failed" {
		t.Fatalf("failed receipt: %#v", failed)
	}
	var state, payload string
	if err := s.DB().QueryRow(`SELECT state, payload_json FROM memory_tray_candidates WHERE candidate_id=?`, pending.CandidateID).Scan(&state, &payload); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || payload != "{}" {
		t.Fatalf("failed candidate retained payload: state=%q payload=%q", state, payload)
	}
	retry, err := s.FailMemoryTrayCandidate(pending.CandidateID, pending.CandidateVersion, "commit_failed", now.Add(2*time.Second))
	if err != nil || retry.ReceiptID != failed.ReceiptID {
		t.Fatalf("failure retry diverged: retry=%#v err=%v", retry, err)
	}
}

func TestTurnEnvelopeAndCandidateProvenanceFailClosed(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Now().UTC()
	secretEnvelope := trayFinalizeRequest("This candidate itself is safe.")
	secretEnvelope.SessionID = "sk-ULTRAPRIVATE0123456789"
	if _, err := s.FinalizeTurn(secretEnvelope, now, time.Second); err == nil {
		t.Fatal("secret-like session identifier was accepted")
	}
	var ledgers int
	if err := s.DB().QueryRow(`SELECT count(*) FROM turn_ledgers`).Scan(&ledgers); err != nil || ledgers != 0 {
		t.Fatalf("invalid envelope reached ledger: count=%d err=%v", ledgers, err)
	}

	mismatch := trayFinalizeRequest("Candidate source must match trusted turn provenance.")
	mismatch.Candidates[0].Capsule.Source.Host = "claude-code"
	result, err := s.FinalizeTurn(mismatch, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != TurnFinalizedRejected || len(result.Receipts) != 1 || result.Receipts[0].Status != MemoryWriteRejected {
		t.Fatalf("provenance mismatch was not content-free rejected: %#v", result)
	}
	var pending int
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_tray_candidates`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("provenance mismatch persisted a candidate: count=%d err=%v", pending, err)
	}
}

func TestTurnAuthorityCannotBeCrossSentOrForgeEpochs(t *testing.T) {
	first, err := OpenVault(filepath.Join(t.TempDir(), "first.db"), StoreKindPersonal, "store_personal_first")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenVault(filepath.Join(t.TempDir(), "second.db"), StoreKindPersonal, "store_personal_second")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	firstDigest := strings.Repeat("a", 64)
	secondDigest := strings.Repeat("b", 64)
	if err := first.ConfigureProductRuntimeAuthority(firstDigest, 3, 7); err != nil {
		t.Fatal(err)
	}
	if err := second.ConfigureProductRuntimeAuthority(secondDigest, 3, 7); err != nil {
		t.Fatal(err)
	}

	req := trayFinalizeRequest("Cross-sent authority must fail before persistence.")
	req.BindingDigest, req.PolicyEpoch, req.ResolverEpoch = secondDigest, 3, 7
	if _, err := first.FinalizeTurn(req, time.Now().UTC(), time.Second); !errors.Is(err, ErrProductRuntimeMismatch) {
		t.Fatalf("cross-sent authority error=%v", err)
	}
	req.BindingDigest, req.PolicyEpoch, req.ResolverEpoch = firstDigest, 4, 7
	if _, err := first.FinalizeTurn(req, time.Now().UTC(), time.Second); !errors.Is(err, ErrProductRuntimeMismatch) {
		t.Fatalf("forged policy epoch error=%v", err)
	}
	var ledgers int
	if err := first.DB().QueryRow(`SELECT count(*) FROM turn_ledgers`).Scan(&ledgers); err != nil || ledgers != 0 {
		t.Fatalf("mismatched authority reached ledger: count=%d err=%v", ledgers, err)
	}
}

func TestImmediateCommitRechecksRuntimeAuthority(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Now().UTC()
	prepared, err := s.FinalizeTurn(trayFinalizeRequest("Stale authority must never reach canonical memory."), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureProductRuntimeAuthority(strings.Repeat("c", 64), 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitMemoryTrayCandidate(prepared.Receipts[0].CandidateID, 1, now); !errors.Is(err, ErrProductRuntimeMismatch) {
		t.Fatalf("stale authority commit error=%v", err)
	}
	var objects int
	if err := s.DB().QueryRow(`SELECT count(*) FROM private_memory_objects`).Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("stale authority created objects=%d err=%v", objects, err)
	}
}

func TestProductStoresRejectLegacyDirectSemanticWrites(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	if _, err := s.RememberCapsule(safeTrayCapsule("This must enter the Tray.")); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct capsule write err=%v", err)
	}
	if _, err := s.SaveSemanticDelta(validSemanticDelta()); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct graph write err=%v", err)
	}
	if _, err := s.ImportMemory(MemoryExport{}); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct import err=%v", err)
	}
	if err := s.SaveCheckpoint(ContinuityCheckpoint{}); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct checkpoint err=%v", err)
	}
	if err := s.SaveObservation(ContinuityObservation{}, false); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct observation err=%v", err)
	}
	if err := s.HideGraphEntity(1); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct graph hide err=%v", err)
	}
	if err := s.RestoreGraphEntity(1); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct graph restore err=%v", err)
	}
	if err := s.DeleteMemory("pulse:any"); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct delete err=%v", err)
	}
	if err := s.WipeMemory(); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct wipe err=%v", err)
	}
	if err := s.WipeContinuity(); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct continuity wipe err=%v", err)
	}
	if _, err := s.ConsolidateCapsules(ConsolidateOptions{}); !errors.Is(err, ErrMemoryTrayRequired) {
		t.Fatalf("direct consolidate err=%v", err)
	}
}

func TestPulseCLIManualWriteCanEnterProductTray(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	capsule := safeTrayCapsule("Pulse CLI install feedback is visible before commit.")
	capsule.Source.Host = "claude-code" // untrusted model metadata cannot choose receipt provenance
	result, err := s.PrepareManualMemoryCapsule(capsule, time.Now().UTC(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Receipts) != 1 || result.Receipts[0].Status != MemoryWritePending {
		t.Fatalf("pulse-cli manual result: %#v", result)
	}
	if result.Receipts[0].SafeProvenance.Host != "pulse-cli" {
		t.Fatalf("manual source.host spoofed provenance: %#v", result.Receipts[0])
	}
}

func TestSemanticDeltaCommitsAtomicallyImmediately(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	delta := validSemanticDelta()
	delta.Source.Host = "codex"
	result, err := s.PrepareManualSemanticDelta(delta, now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pending := result.Receipts[0]
	for table, query := range map[string]string{
		"events":     `SELECT count(*) FROM events WHERE scorer_version='host-extracted'`,
		"entities":   `SELECT count(*) FROM entities WHERE scorer_version='host-extracted'`,
		"continuity": `SELECT count(*) FROM continuity_checkpoints`,
	} {
		var count int
		if err := s.DB().QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("pending semantic delta reached %s: count=%d err=%v", table, count, err)
		}
	}
	committed, err := s.CommitMemoryTrayCandidate(
		pending.CandidateID, pending.CandidateVersion, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Status != MemoryWriteCreated || committed.ObjectID == "" {
		t.Fatalf("semantic commit receipt: %#v", committed)
	}
	var events, entities, checkpoints int
	if err := s.DB().QueryRow(`SELECT count(*) FROM events WHERE scorer_version='host-extracted'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM entities WHERE scorer_version='host-extracted'`).Scan(&entities); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM continuity_checkpoints`).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if events != len(delta.Events) || entities != len(delta.Nodes) || checkpoints != 1 {
		t.Fatalf("semantic materialization incomplete: events=%d entities=%d checkpoints=%d", events, entities, checkpoints)
	}
}

func TestPrivateSemanticProjectionMergesContributionsAndPreservesLegacyRows(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)

	legacy := validSemanticDelta()
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	legacyResult, err := saveSemanticDeltaTx(tx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var legacyPulseID int64
	if err := s.DB().QueryRow(`
		SELECT id FROM entities
		 WHERE canonical_name='Pulse' AND extractor_version='pulse.semantic_delta.v1'`,
	).Scan(&legacyPulseID); err != nil {
		t.Fatal(err)
	}

	firstDelta := validSemanticDelta()
	firstDelta.Source.Host = "codex"
	firstDelta.Source.Timestamp = now.Format(time.RFC3339)
	first, err := s.PrepareManualSemanticDelta(firstDelta, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, first.Receipts[0], now, time.Second)
	firstCreated, err := s.CommitMemoryTrayCandidate(
		first.Receipts[0].CandidateID, first.Receipts[0].CandidateVersion, now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	secondDelta := firstDelta
	secondDelta.Source.Timestamp = now.Add(time.Minute).Format(time.RFC3339)
	secondDelta.Nodes = append([]SemanticNode(nil), firstDelta.Nodes...)
	secondDelta.Nodes[0].Aliases = append(secondDelta.Nodes[0].Aliases, "Pulse Memory")
	secondDelta.Nodes[0].Summary = "Pulse now merges private semantic contributions without touching legacy rows."
	secondDelta.Events = append([]SemanticEvent(nil), firstDelta.Events...)
	secondDelta.Events[0].ClientID = "event:pulse-private-contribution-update"
	secondDelta.Events[0].Title = "Pulse private contribution update"
	secondDelta.Events[0].Summary = "A second private delta updated the same logical product graph."
	second, err := s.PrepareManualSemanticDelta(secondDelta, now.Add(time.Minute), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, second.Receipts[0], now.Add(time.Minute), time.Second)
	secondCreated, err := s.CommitMemoryTrayCandidate(
		second.Receipts[0].CandidateID, second.Receipts[0].CandidateVersion, now.Add(time.Minute+time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	var productEntities, productRelations, productFacts, productThreads, productSessions, productCheckpoints int
	for label, query := range map[string]struct {
		value *int
		query string
	}{
		"entities":    {&productEntities, `SELECT COUNT(DISTINCT row_ref) FROM private_semantic_projection_rows WHERE row_kind='entity'`},
		"relations":   {&productRelations, `SELECT COUNT(DISTINCT row_ref) FROM private_semantic_projection_rows WHERE row_kind='relation'`},
		"facts":       {&productFacts, `SELECT COUNT(DISTINCT row_ref) FROM private_semantic_projection_rows WHERE row_kind='fact'`},
		"threads":     {&productThreads, `SELECT COUNT(DISTINCT row_ref) FROM private_semantic_projection_rows WHERE row_kind='thread'`},
		"sessions":    {&productSessions, `SELECT COUNT(DISTINCT row_ref) FROM private_semantic_projection_rows WHERE row_kind='session'`},
		"checkpoints": {&productCheckpoints, `SELECT COUNT(DISTINCT row_ref) FROM private_semantic_projection_rows WHERE row_kind='checkpoint'`},
	} {
		if err := s.DB().QueryRow(query.query).Scan(query.value); err != nil {
			t.Fatalf("count %s: %v", label, err)
		}
	}
	if productEntities != len(firstDelta.Nodes) || productRelations != 1 || productFacts != 1 ||
		productThreads != 1 || productSessions != 1 || productCheckpoints != 2 {
		t.Fatalf("private contributions did not merge: entities=%d relations=%d facts=%d threads=%d sessions=%d checkpoints=%d",
			productEntities, productRelations, productFacts, productThreads, productSessions, productCheckpoints)
	}
	var legacyStillPresent int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM entities WHERE id=?`, legacyPulseID).Scan(&legacyStillPresent); err != nil || legacyStillPresent != 1 {
		t.Fatalf("legacy entity changed during private projection: count=%d err=%v", legacyStillPresent, err)
	}
	if len(legacyResult.EventIDs) != 1 {
		t.Fatalf("legacy fixture event IDs=%v", legacyResult.EventIDs)
	}
	assertStableSourceRef := func(objectID string) {
		t.Helper()
		var refs string
		if err := s.DB().QueryRow(`
			SELECT checkpoint.source_refs_json
			  FROM private_semantic_projection_rows projection
			  JOIN continuity_checkpoints checkpoint
			    ON checkpoint.id=CAST(projection.row_ref AS INTEGER)
			 WHERE projection.object_id=? AND projection.row_kind='checkpoint'`, objectID).Scan(&refs); err != nil {
			t.Fatal(err)
		}
		expected := `["pulse:private-memory:` + objectID + `"]`
		if refs != expected {
			t.Fatalf("semantic continuity provenance=%q want=%q", refs, expected)
		}
	}
	assertStableSourceRef(firstCreated.ObjectID)
	assertStableSourceRef(secondCreated.ObjectID)

	if _, err := s.DeleteCommittedMemory(firstCreated.ObjectID, "delete_semantic_first", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertStableSourceRef(secondCreated.ObjectID)
	var remainingProductEvents, remainingCheckpoints int
	if err := s.DB().QueryRow(`SELECT COUNT(DISTINCT row_ref) FROM private_semantic_projection_rows WHERE row_kind='event'`).Scan(&remainingProductEvents); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(DISTINCT row_ref) FROM private_semantic_projection_rows WHERE row_kind='checkpoint'`).Scan(&remainingCheckpoints); err != nil {
		t.Fatal(err)
	}
	if remainingProductEvents != 1 || remainingCheckpoints != 1 {
		t.Fatalf("deleting one contribution removed too much or too little: events=%d checkpoints=%d", remainingProductEvents, remainingCheckpoints)
	}

	if _, err := s.DeleteCommittedMemory(secondCreated.ObjectID, "delete_semantic_second", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var projectionRows, privateThreads, privateSessions int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM private_semantic_projection_rows`).Scan(&projectionRows); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM continuity_threads WHERE thread_id LIKE 'private:%'`).Scan(&privateThreads); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM continuity_sessions WHERE session_id LIKE 'private:%'`).Scan(&privateSessions); err != nil {
		t.Fatal(err)
	}
	if projectionRows != 0 || privateThreads != 0 || privateSessions != 0 {
		t.Fatalf("deleted product semantic rows survived: projection=%d threads=%d sessions=%d", projectionRows, privateThreads, privateSessions)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM entities WHERE id=?`, legacyPulseID).Scan(&legacyStillPresent); err != nil || legacyStillPresent != 1 {
		t.Fatalf("legacy entity lost after private delete: count=%d err=%v", legacyStillPresent, err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE id=?`, legacyResult.EventIDs[0]).Scan(&legacyStillPresent); err != nil || legacyStillPresent != 1 {
		t.Fatalf("legacy event lost after private delete: count=%d err=%v", legacyStillPresent, err)
	}
}

func TestPrivateSemanticProjectionDoesNotMergeCanonicalNodesAcrossProjectNamespaces(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 25, 13, 0, 0, 0, time.UTC)
	if err := s.ConfigureContinuityDeliveryAuthority(testTrayBindingDigest, "repository_scope_a"); err != nil {
		t.Fatal(err)
	}

	firstDelta := validSemanticDelta()
	firstDelta.Source.Host = "codex"
	firstDelta.Source.Timestamp = now.Format(time.RFC3339)
	firstDelta.Source.ThreadID = "shared-thread-name"
	firstDelta.Source.SessionID = "scope-a-session"
	firstDelta.Continuity.Summary = "Project A owns this continuity."
	first, err := s.PrepareManualSemanticDelta(firstDelta, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstCreated, err := s.CommitMemoryTrayCandidate(
		first.Receipts[0].CandidateID, first.Receipts[0].CandidateVersion, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.ConfigureContinuityDeliveryAuthority(testTrayBindingDigest, "repository_scope_b"); err != nil {
		t.Fatal(err)
	}
	secondDelta := validSemanticDelta()
	secondDelta.Source.Host = "codex"
	secondDelta.Source.Timestamp = now.Add(time.Minute).Format(time.RFC3339)
	secondDelta.Source.ThreadID = "shared-thread-name"
	secondDelta.Source.SessionID = "scope-b-session"
	secondDelta.Continuity.Summary = "Project B owns this continuity."
	second, err := s.PrepareManualSemanticDelta(secondDelta, now.Add(time.Minute), 0)
	if err != nil {
		t.Fatal(err)
	}
	secondCreated, err := s.CommitMemoryTrayCandidate(
		second.Receipts[0].CandidateID, second.Receipts[0].CandidateVersion, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}

	var distinctEntities int
	if err := s.DB().QueryRow(`
		SELECT COUNT(DISTINCT projection.row_ref)
		  FROM private_semantic_projection_rows projection
		 WHERE projection.row_kind='entity'
		   AND projection.object_id IN (?, ?)`,
		firstCreated.ObjectID, secondCreated.ObjectID,
	).Scan(&distinctEntities); err != nil {
		t.Fatal(err)
	}
	if distinctEntities != len(firstDelta.Nodes)+len(secondDelta.Nodes) {
		t.Fatalf("cross-project canonical nodes merged: distinct=%d want=%d",
			distinctEntities, len(firstDelta.Nodes)+len(secondDelta.Nodes))
	}

	threadID := "private:" + normalizeThreadID(
		secondDelta.Source.ThreadID, secondDelta.Source.ProjectID, secondDelta.Source.SessionID,
	)
	projectBResume, err := s.BuildResume(ResumeQuery{
		ThreadID: threadID, ProjectID: "repository_scope_b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(projectBResume.ResumeMarkdown, "Project B owns this continuity.") ||
		strings.Contains(projectBResume.ResumeMarkdown, "Project A owns this continuity.") {
		t.Fatalf("project B continuity scope=%q", projectBResume.ResumeMarkdown)
	}
}

func TestProductMemoryStatusCountsActiveCapsuleAndSemanticObjects(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	capsule, err := s.FinalizeTurn(trayFinalizeRequest("Memory status counts the committed capsule."), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, capsule.Receipts[0], now, time.Second)
	capsuleReceipt, err := s.CommitMemoryTrayCandidate(capsule.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	delta := validSemanticDelta()
	delta.Source.Host = "codex"
	semantic, err := s.PrepareManualSemanticDelta(delta, now.Add(time.Minute), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, semantic.Receipts[0], now.Add(time.Minute), time.Second)
	semanticReceipt, err := s.CommitMemoryTrayCandidate(semantic.Receipts[0].CandidateID, 1, now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.MemoryStatus()
	if err != nil || status.ItemCount != 2 || status.LastWrite == "" {
		t.Fatalf("product status=%#v err=%v", status, err)
	}
	if _, err := s.DeleteCommittedMemory(semanticReceipt.ObjectID, "delete_status_semantic", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, err = s.MemoryStatus()
	if err != nil || status.ItemCount != 1 {
		t.Fatalf("status after semantic delete=%#v err=%v", status, err)
	}
	if _, err := s.DeleteCommittedMemory(capsuleReceipt.ObjectID, "delete_status_capsule", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, err = s.MemoryStatus()
	if err != nil || status.ItemCount != 0 || status.LastWrite != "" {
		t.Fatalf("status after all deletes=%#v err=%v", status, err)
	}
}

func TestCommittedCapsuleCorrectionUsesTrayAndKeepsStableObjectID(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	createdTurn, err := s.FinalizeTurn(trayFinalizeRequest("Use the old project rule."), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, createdTurn.Receipts[0], now, time.Second)
	created, err := s.CommitMemoryTrayCandidate(createdTurn.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replacement := safeTrayCapsule("Use the corrected project rule in every new session.")
	replacement.Source.Host = "claude-code" // caller-controlled and intentionally ignored
	prepared, err := s.PrepareMemoryCorrection(
		created.ObjectID,
		PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &replacement},
		now.Add(time.Minute), time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending := prepared.Receipts[0]
	if pending.Status != MemoryWritePending || pending.ObjectID != "" {
		t.Fatalf("correction was not pending first: %#v", pending)
	}
	tray, err := s.ListMemoryTray(10)
	if err != nil {
		t.Fatal(err)
	}
	var correctionView *MemoryTrayCandidateView
	for index := range tray {
		if tray[index].CandidateID == pending.CandidateID {
			correctionView = &tray[index]
			break
		}
	}
	if correctionView == nil || correctionView.Operation != "correct" || correctionView.TargetObjectID != created.ObjectID {
		t.Fatalf("correction Tray metadata missing: %#v", correctionView)
	}
	wrongKind := validSemanticDelta()
	wrongKind.Source.Host = "pulse-cli"
	wrongKind.Source.SessionID = ""
	if _, err := s.EditMemoryTrayCandidate(
		pending.CandidateID, pending.CandidateVersion,
		PrivateMemoryCandidate{Kind: PrivateMemoryCandidateSemanticDelta, SemanticDelta: &wrongKind},
		now.Add(time.Minute), time.Second,
	); err == nil {
		t.Fatal("correction edit switched canonical memory kind")
	}
	var before string
	if err := s.DB().QueryRow(`SELECT redacted_summary FROM memory_capsules WHERE id=?`, created.ObjectID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != "Use the old project rule." {
		t.Fatalf("pending correction changed canonical content: %q", before)
	}
	presentTrayReceipt(t, s, pending, now.Add(time.Minute), time.Second)
	updated, err := s.CommitMemoryTrayCandidate(pending.CandidateID, pending.CandidateVersion, now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != MemoryWriteUpdated || updated.ObjectID != created.ObjectID || updated.LedgerID == created.LedgerID {
		t.Fatalf("correction receipt is not stable/truthful: created=%#v updated=%#v", created, updated)
	}
	if updated.SafeProvenance.Host != "pulse-cli" {
		t.Fatalf("manual correction provenance was spoofed: %#v", updated.SafeProvenance)
	}
	var after, sourceHost string
	if err := s.DB().QueryRow(`SELECT redacted_summary, source_host FROM memory_capsules WHERE id=?`, created.ObjectID).Scan(&after, &sourceHost); err != nil {
		t.Fatal(err)
	}
	if after != replacement.Items[0].RedactedSummary || sourceHost != "pulse-cli" {
		t.Fatalf("correction content/provenance mismatch: summary=%q host=%q", after, sourceHost)
	}
	tray, err = s.ListMemoryTray(10)
	if err != nil {
		t.Fatal(err)
	}
	var currentRows, historicalRows int
	for _, item := range tray {
		if item.CanonicalObjectID != created.ObjectID {
			continue
		}
		if item.Current {
			currentRows++
		} else {
			historicalRows++
		}
		if item.LatestReceipt.ReceiptID != updated.ReceiptID {
			t.Fatalf("object lifecycle chain is fragmented: item=%#v updated=%#v", item, updated)
		}
	}
	if currentRows != 1 || historicalRows != 1 {
		t.Fatalf("correction current/history rows current=%d historical=%d tray=%#v", currentRows, historicalRows, tray)
	}
}

func TestCorrectionCommitReplayReturnsOriginalReceiptAfterLaterDelete(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Now().UTC()
	createdTurn, err := s.FinalizeTurn(trayFinalizeRequest("Original value for replay ordering."), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, createdTurn.Receipts[0], now, time.Second)
	created, err := s.CommitMemoryTrayCandidate(createdTurn.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replacement := safeTrayCapsule("Corrected value retains its own durable receipt after delete.")
	correction, err := s.PrepareMemoryCorrectionWithInvocation(
		created.ObjectID,
		PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &replacement},
		"correction_replay_01", now.Add(time.Minute), time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, correction.Receipts[0], now.Add(time.Minute), time.Second)
	updated, err := s.CommitMemoryTrayCandidate(correction.Receipts[0].CandidateID, 1, now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteCommittedMemory(created.ObjectID, "delete_after_correction", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ReceiptID == updated.ReceiptID || deleted.ReasonCode != "user_deleted" {
		t.Fatalf("delete fixture=%#v updated=%#v", deleted, updated)
	}
	replayed, err := s.CommitMemoryTrayCandidate(correction.Receipts[0].CandidateID, 1, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ReceiptID != updated.ReceiptID || replayed.ReasonCode != "user_corrected" {
		t.Fatalf("commit replay returned later control receipt: updated=%#v replayed=%#v", updated, replayed)
	}
}

func TestCommittedSemanticCorrectionRebuildsOnlyStableTargetContribution(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	delta := validSemanticDelta()
	createdTurn, err := s.PrepareManualSemanticDelta(delta, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, createdTurn.Receipts[0], now, time.Second)
	created, err := s.CommitMemoryTrayCandidate(createdTurn.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replacement := validSemanticDelta()
	replacement.Events = append([]SemanticEvent(nil), replacement.Events...)
	replacement.Events[0].ClientID = "event:corrected-private-graph"
	replacement.Events[0].Title = "Corrected private graph event"
	replacement.Events[0].Summary = "The corrected semantic contribution replaced the old event."
	prepared, err := s.PrepareMemoryCorrection(
		created.ObjectID,
		PrivateMemoryCandidate{Kind: PrivateMemoryCandidateSemanticDelta, SemanticDelta: &replacement},
		now.Add(time.Minute), time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, prepared.Receipts[0], now.Add(time.Minute), time.Second)
	updated, err := s.CommitMemoryTrayCandidate(prepared.Receipts[0].CandidateID, 1, now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != MemoryWriteUpdated || updated.ObjectID != created.ObjectID {
		t.Fatalf("semantic correction receipt=%#v", updated)
	}
	var oldEvents, newEvents, objectCount int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE title='Pulse graph ingestion decision'`).Scan(&oldEvents); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE title='Corrected private graph event'`).Scan(&newEvents); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM private_memory_objects WHERE object_id=? AND lifecycle='active'`, created.ObjectID).Scan(&objectCount); err != nil {
		t.Fatal(err)
	}
	if oldEvents != 0 || newEvents != 1 || objectCount != 1 {
		t.Fatalf("semantic correction projection old=%d new=%d objects=%d", oldEvents, newEvents, objectCount)
	}
}

func TestConcurrentCorrectionsDetectStaleTargetInsteadOfSilentlyLastWriteWins(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	createdTurn, err := s.FinalizeTurn(trayFinalizeRequest("Original value before competing corrections."), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, createdTurn.Receipts[0], now, time.Second)
	created, err := s.CommitMemoryTrayCandidate(createdTurn.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	firstCapsule := safeTrayCapsule("First reviewed correction wins.")
	first, err := s.PrepareMemoryCorrection(
		created.ObjectID,
		PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &firstCapsule},
		now.Add(time.Minute), time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondCapsule := safeTrayCapsule("Second stale correction must conflict.")
	second, err := s.PrepareMemoryCorrection(
		created.ObjectID,
		PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &secondCapsule},
		now.Add(time.Minute), time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, first.Receipts[0], now.Add(time.Minute), time.Second)
	presentTrayReceipt(t, s, second.Receipts[0], now.Add(time.Minute), time.Second)
	if _, err := s.CommitMemoryTrayCandidate(first.Receipts[0].CandidateID, 1, now.Add(time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitMemoryTrayCandidate(second.Receipts[0].CandidateID, 1, now.Add(time.Minute+time.Second)); !errors.Is(err, ErrMemoryCorrectionConflict) {
		t.Fatalf("stale correction err=%v, want conflict", err)
	}
	var summary string
	if err := s.DB().QueryRow(`SELECT redacted_summary FROM memory_capsules WHERE id=?`, created.ObjectID).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if summary != firstCapsule.Items[0].RedactedSummary {
		t.Fatalf("stale correction overwrote current content: %q", summary)
	}
}

func TestCanceledCorrectionCanBeProposedAgainAndContentCanCycle(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	createdTurn, err := s.FinalizeTurn(trayFinalizeRequest("Value A before correction cycles."), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, createdTurn.Receipts[0], now, time.Second)
	created, err := s.CommitMemoryTrayCandidate(createdTurn.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	valueB := safeTrayCapsule("Value B can be canceled, retried, and restored later.")
	canceledAttempt, err := s.PrepareMemoryCorrection(
		created.ObjectID, PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &valueB},
		now.Add(time.Minute), time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelMemoryTrayCandidate(canceledAttempt.Receipts[0].CandidateID, 1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	retryB, err := s.PrepareMemoryCorrection(
		created.ObjectID, PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &valueB},
		now.Add(2*time.Minute), time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if retryB.Receipts[0].CandidateID == canceledAttempt.Receipts[0].CandidateID {
		t.Fatal("canceled correction reused a permanently terminal candidate")
	}
	presentTrayReceipt(t, s, retryB.Receipts[0], now.Add(2*time.Minute), time.Second)
	if _, err := s.CommitMemoryTrayCandidate(retryB.Receipts[0].CandidateID, 1, now.Add(2*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	valueC := safeTrayCapsule("Value C temporarily supersedes value B.")
	toC, err := s.PrepareMemoryCorrection(
		created.ObjectID, PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &valueC},
		now.Add(3*time.Minute), time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, toC.Receipts[0], now.Add(3*time.Minute), time.Second)
	if _, err := s.CommitMemoryTrayCandidate(toC.Receipts[0].CandidateID, 1, now.Add(3*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	backToB, err := s.PrepareMemoryCorrection(
		created.ObjectID, PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &valueB},
		now.Add(4*time.Minute), time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, backToB.Receipts[0], now.Add(4*time.Minute), time.Second)
	updated, err := s.CommitMemoryTrayCandidate(backToB.Receipts[0].CandidateID, 1, now.Add(4*time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != MemoryWriteUpdated || updated.ObjectID != created.ObjectID {
		t.Fatalf("B→C→B correction did not update stable object: %#v", updated)
	}
	var summary string
	if err := s.DB().QueryRow(`SELECT redacted_summary FROM memory_capsules WHERE id=?`, created.ObjectID).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if summary != valueB.Items[0].RedactedSummary {
		t.Fatalf("B→C→B returned stale receipt instead of restoring B: %q", summary)
	}
}

func TestUnsafeCorrectionReturnsDurableContentFreeRejection(t *testing.T) {
	s, path := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	createdTurn, err := s.FinalizeTurn(trayFinalizeRequest("Safe value before unsafe correction."), now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, createdTurn.Receipts[0], now, time.Second)
	created, err := s.CommitMemoryTrayCandidate(createdTurn.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "gho_unsafeCorrectionSecret1234567890abcdef"
	unsafe := safeTrayCapsule(secret)
	result, err := s.PrepareMemoryCorrection(
		created.ObjectID, PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &unsafe},
		now.Add(time.Minute), time.Second,
	)
	if err != nil {
		t.Fatalf("unsafe correction must return a durable rejection, got error: %v", err)
	}
	if result.Status != TurnFinalizedRejected || len(result.Receipts) != 1 || result.Receipts[0].Status != MemoryWriteRejected {
		t.Fatalf("unsafe correction receipt=%#v", result)
	}
	var unsafeCandidates int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates WHERE ledger_id=?`, result.LedgerID).Scan(&unsafeCandidates); err != nil || unsafeCandidates != 0 {
		t.Fatalf("unsafe correction candidate persisted: count=%d err=%v", unsafeCandidates, err)
	}
	for _, databasePath := range []string{path, path + "-wal", path + "-shm"} {
		body, err := os.ReadFile(databasePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("unsafe correction bytes reached %s", databasePath)
		}
	}
}

func TestDuplicateContentDeduplicatesToExistingCanonicalObject(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	firstRequest := trayFinalizeRequest("The same private decision must have one canonical object.")
	first, err := s.FinalizeTurn(firstRequest, now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, first.Receipts[0], now, 10*time.Second)
	created, err := s.CommitMemoryTrayCandidate(first.Receipts[0].CandidateID, 1, now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := firstRequest
	secondRequest.TurnID = "turn_02"
	secondRequest.SourceEventKey = "codex:session_01:turn_02:stop"
	secondRequest.IdempotencyKey = "finalize_02"
	second, err := s.FinalizeTurn(secondRequest, now.Add(time.Minute), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, second.Receipts[0], now.Add(time.Minute), 10*time.Second)
	deduplicated, err := s.CommitMemoryTrayCandidate(second.Receipts[0].CandidateID, 1, now.Add(70*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if deduplicated.Status != MemoryWriteDeduplicated || deduplicated.ObjectID != created.ObjectID {
		t.Fatalf("dedup receipt created=%#v dedup=%#v", created, deduplicated)
	}
	var capsules int
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&capsules); err != nil || capsules != 1 {
		t.Fatalf("dedup canonical count=%d err=%v", capsules, err)
	}
}

func TestEquivalentCrossHarnessContentDeduplicatesDespiteDifferentProvenance(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	firstRequest := trayFinalizeRequest("One durable decision must stay one object across harnesses.")
	first, err := s.FinalizeTurn(firstRequest, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	presentTrayReceipt(t, s, first.Receipts[0], now, time.Second)
	created, err := s.CommitMemoryTrayCandidate(first.Receipts[0].CandidateID, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	secondRequest := firstRequest
	secondRequest.Host = "claude-code"
	secondRequest.SessionID = "claude_session_01"
	secondRequest.TurnID = "prompt_01"
	secondRequest.SourceEventKey = "claude:claude_session_01:prompt_01:stop"
	secondRequest.IdempotencyKey = "claude_finalize_01"
	secondCapsule := *secondRequest.Candidates[0].Capsule
	secondCapsule.Source.Host = "claude-code"
	secondCapsule.Source.Timestamp = "2026-07-14T10:05:00Z"
	secondRequest.Candidates = []PrivateMemoryCandidate{{
		Kind: PrivateMemoryCandidateCapsule, Capsule: &secondCapsule,
	}}
	second, err := s.FinalizeTurn(secondRequest, now.Add(time.Minute), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if second.Receipts[0].ContentDigest != first.Receipts[0].ContentDigest {
		t.Fatalf("semantic identity drifted by provenance: codex=%s claude=%s",
			first.Receipts[0].ContentDigest, second.Receipts[0].ContentDigest)
	}
	presentTrayReceipt(t, s, second.Receipts[0], now.Add(time.Minute), time.Second)
	deduplicated, err := s.CommitMemoryTrayCandidate(second.Receipts[0].CandidateID, 1, now.Add(61*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if deduplicated.Status != MemoryWriteDeduplicated || deduplicated.ObjectID != created.ObjectID {
		t.Fatalf("cross-harness dedup receipt created=%#v dedup=%#v", created, deduplicated)
	}
}

func TestConcurrentDuplicateFinalizeConvergesOnOneLedgerAndReceipt(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	req := trayFinalizeRequest("Concurrent Stop hooks converge on one pending receipt.")
	now := time.Now().UTC()
	results := make([]TurnFinalizeResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = s.FinalizeTurn(req, now, 10*time.Second)
		}(index)
	}
	close(start)
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("finalize[%d]: %v", index, err)
		}
	}
	if results[0].LedgerID != results[1].LedgerID ||
		results[0].Receipts[0].ReceiptID != results[1].Receipts[0].ReceiptID {
		t.Fatalf("concurrent finalize diverged: %#v %#v", results[0], results[1])
	}
}

func TestCommittedCapsuleDeleteIsAtomicReceiptedAndIdempotent(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	const deletedSummary = "A committed private memory can be deleted with a durable receipt."
	finalized, err := s.FinalizeTurn(
		trayFinalizeRequest(deletedSummary),
		now, 10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CommitMemoryTrayCandidate(finalized.Receipts[0].CandidateID, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	dedupRequest := trayFinalizeRequest(deletedSummary)
	dedupRequest.SessionID = "session_02"
	dedupRequest.TurnID = "turn_02"
	dedupRequest.SourceEventKey = "codex:session_02:turn_02:stop"
	dedupRequest.IdempotencyKey = "finalize_02"
	dedupFinalized, err := s.FinalizeTurn(dedupRequest, now.Add(20*time.Second), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	deduplicated, err := s.CommitMemoryTrayCandidate(dedupFinalized.Receipts[0].CandidateID, 1, now.Add(20*time.Second))
	if err != nil || deduplicated.Status != MemoryWriteDeduplicated || deduplicated.ObjectID != created.ObjectID {
		t.Fatalf("deduplicate: receipt=%#v err=%v", deduplicated, err)
	}
	deleted, err := s.DeleteCommittedMemory(created.ObjectID, "delete_test_01", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Status != MemoryWriteUpdated || deleted.ObjectID != created.ObjectID || deleted.ReasonCode != "user_deleted" {
		t.Fatalf("delete receipt: %#v", deleted)
	}
	if deleted.LedgerID == created.LedgerID || deleted.SafeProvenance.Host != "pulse-cli" {
		t.Fatalf("delete reused creation provenance: created=%#v deleted=%#v", created, deleted)
	}
	var capsules int
	if err := s.DB().QueryRow(`SELECT count(*) FROM memory_capsules WHERE id=?`, created.ObjectID).Scan(&capsules); err != nil || capsules != 0 {
		t.Fatalf("deleted capsule remains: count=%d err=%v", capsules, err)
	}
	var lifecycle string
	if err := s.DB().QueryRow(`SELECT lifecycle FROM private_memory_objects WHERE object_id=?`, created.ObjectID).Scan(&lifecycle); err != nil || lifecycle != "deleted" {
		t.Fatalf("object lifecycle=%q err=%v", lifecycle, err)
	}
	var visiblePayloads int
	if err := s.DB().QueryRow(`
		SELECT count(*) FROM memory_tray_candidates
		 WHERE canonical_object_id=? AND payload_json!='{}'`, created.ObjectID).Scan(&visiblePayloads); err != nil || visiblePayloads != 0 {
		t.Fatalf("deleted dedup payloads remain=%d err=%v", visiblePayloads, err)
	}
	tray, err := s.ListMemoryTray(10)
	if err != nil || len(tray) != 2 {
		t.Fatalf("deleted Tray rows: tray=%#v err=%v", tray, err)
	}
	for _, item := range tray {
		payload, _ := json.Marshal(item.Candidate)
		if item.Candidate.Capsule != nil || bytes.Contains(payload, []byte(deletedSummary)) {
			t.Fatalf("deleted content remains visible in Tray: item=%#v", item)
		}
	}
	retry, err := s.DeleteCommittedMemory(created.ObjectID, "delete_test_01", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if retry.ReceiptID != deleted.ReceiptID {
		t.Fatalf("delete retry diverged: first=%#v retry=%#v", deleted, retry)
	}
}

func TestCommittedMemoryMovesProjectGlobalProjectWithOneLogicalHead(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	if err := s.ConfigureContinuityDeliveryAuthority(testTrayBindingDigest, "repository_pulse"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	finalized, err := s.FinalizeTurn(
		trayFinalizeRequest("Keep scope changes explicit and reversible."),
		now,
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending := finalized.Receipts[0]
	created, err := s.CommitMemoryTrayCandidate(
		pending.CandidateID, pending.CandidateVersion, now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}

	movedGlobal, err := s.MoveCommittedMemoryScope(
		created.ObjectID, 1, MemoryScopePersonalGlobal, "move_global_01", now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if movedGlobal.Scope != MemoryScopePersonalGlobal || movedGlobal.LogicalGeneration != 2 ||
		movedGlobal.WriteReceipt.ReasonCode != "user_moved_to_personal_global" {
		t.Fatalf("global move=%#v", movedGlobal)
	}
	requestBindingB := strings.Repeat("b", 64)
	requestScopeB, err := s.PersonalMemoryScopeSnapshotForBinding(
		requestBindingB, "repository_project_b",
	)
	if err != nil {
		t.Fatal(err)
	}
	globalSnapshotDigest := requestScopeB.Digest()
	if !trayBindingDigestPattern.MatchString(globalSnapshotDigest) {
		t.Fatalf("invalid content-free snapshot digest: %q", globalSnapshotDigest)
	}
	requestGlobalResume, err := s.BuildResumeForPersonalScope(ResumeQuery{
		ThreadID: "project-b-request", ProjectID: "repository_project_b",
	}, requestScopeB)
	if err != nil || !strings.Contains(
		requestGlobalResume.ResumeMarkdown, "Keep scope changes explicit and reversible.",
	) {
		t.Fatalf("request-scoped global resume=%q err=%v", requestGlobalResume.ResumeMarkdown, err)
	}
	if requestGlobalResume.MemorySnapshotDigest != globalSnapshotDigest {
		t.Fatalf("resume snapshot=%q want %q", requestGlobalResume.MemorySnapshotDigest, globalSnapshotDigest)
	}
	replay, err := s.MoveCommittedMemoryScope(
		created.ObjectID, 1, MemoryScopePersonalGlobal, "move_global_01", now.Add(3*time.Second),
	)
	if err != nil || replay.WriteReceipt.ReceiptID != movedGlobal.WriteReceipt.ReceiptID {
		t.Fatalf("global replay=%#v err=%v", replay, err)
	}
	if _, err := s.MoveCommittedMemoryScope(
		created.ObjectID, 1, MemoryScopeProject, "move_stale_01", now.Add(4*time.Second),
	); !errors.Is(err, ErrMemoryScopeConflict) {
		t.Fatalf("stale move error=%v", err)
	}
	if err := s.ConfigureContinuityDeliveryAuthority(testTrayBindingDigest, "repository_project_b"); err != nil {
		t.Fatal(err)
	}
	globalRecall, err := s.RecallMemory(RecallMemoryQuery{
		Query: "scope changes explicit", Limit: 10, PrivacyCeiling: "normal",
	})
	if err != nil || len(globalRecall) != 1 || globalRecall[0].ID != created.ObjectID {
		t.Fatalf("global recall from project B=%#v err=%v", globalRecall, err)
	}
	globalResume, err := s.BuildResume(ResumeQuery{
		ThreadID: "project-b-thread", ProjectID: "repository_project_b",
	})
	if err != nil || !strings.Contains(globalResume.ResumeMarkdown, "Keep scope changes explicit and reversible.") {
		t.Fatalf("global resume from project B=%q err=%v", globalResume.ResumeMarkdown, err)
	}
	if len(globalResume.IncludedObjectIDs) != 1 ||
		globalResume.IncludedObjectIDs[0] != created.ObjectID ||
		globalResume.CoverageCounted < 1 ||
		globalResume.CoverageCounted != globalResume.CoverageTotal ||
		globalResume.SourceEquivalentTokens == nil {
		t.Fatalf("global token evidence=%#v", globalResume)
	}
	if err := s.ConfigureContinuityDeliveryAuthority(testTrayBindingDigest, "repository_pulse"); err != nil {
		t.Fatal(err)
	}

	movedProject, err := s.MoveCommittedMemoryScope(
		created.ObjectID, 2, MemoryScopeProject, "move_project_01", now.Add(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if movedProject.Scope != MemoryScopeProject || movedProject.LogicalGeneration != 3 ||
		movedProject.WriteReceipt.ReasonCode != "user_moved_to_project" {
		t.Fatalf("project move=%#v", movedProject)
	}
	requestScopeB, err = s.PersonalMemoryScopeSnapshotForBinding(
		requestBindingB, "repository_project_b",
	)
	if err != nil {
		t.Fatal(err)
	}
	if requestScopeB.Digest() == globalSnapshotDigest {
		t.Fatal("scope mutation did not invalidate the content-free memory snapshot")
	}
	requestProjectResume, err := s.BuildResumeForPersonalScope(ResumeQuery{
		ThreadID: "project-b-request", ProjectID: "repository_project_b",
	}, requestScopeB)
	if err != nil || strings.Contains(
		requestProjectResume.ResumeMarkdown, "Keep scope changes explicit and reversible.",
	) {
		t.Fatalf("request-scoped project leak=%q err=%v", requestProjectResume.ResumeMarkdown, err)
	}
	if len(requestProjectResume.IncludedObjectIDs) != 0 ||
		requestProjectResume.CoverageCounted != 0 || requestProjectResume.CoverageTotal != 0 ||
		requestProjectResume.SourceEquivalentTokens != nil {
		t.Fatalf("foreign request scope influenced token evidence=%#v", requestProjectResume)
	}
	if err := s.ConfigureContinuityDeliveryAuthority(testTrayBindingDigest, "repository_project_b"); err != nil {
		t.Fatal(err)
	}
	projectBRecall, err := s.RecallMemory(RecallMemoryQuery{
		Query: "scope changes explicit", Limit: 10, PrivacyCeiling: "normal",
	})
	if err != nil || len(projectBRecall) != 0 {
		t.Fatalf("project A memory leaked into project B recall=%#v err=%v", projectBRecall, err)
	}
	projectBResume, err := s.BuildResume(ResumeQuery{
		ThreadID: "project-b-thread", ProjectID: "repository_project_b",
	})
	if err != nil || strings.Contains(projectBResume.ResumeMarkdown, "Keep scope changes explicit and reversible.") {
		t.Fatalf("project A memory leaked into project B resume=%q err=%v", projectBResume.ResumeMarkdown, err)
	}
	if len(projectBResume.IncludedObjectIDs) != 0 ||
		projectBResume.CoverageCounted != 0 || projectBResume.CoverageTotal != 0 ||
		projectBResume.SourceEquivalentTokens != nil {
		t.Fatalf("foreign memory influenced project B token evidence=%#v", projectBResume)
	}
	if err := s.ConfigureContinuityDeliveryAuthority(testTrayBindingDigest, "repository_pulse"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteCommittedMemoryGeneration(
		created.ObjectID, 2, "delete_stale_generation_01", now.Add(6*time.Second),
	); !errors.Is(err, ErrMemoryScopeConflict) {
		t.Fatalf("stale generation delete error=%v", err)
	}
	var scope, namespace, origin, summary, captureHost, captureSession, capturedAt string
	var generation, activeHeads int
	if err := s.DB().QueryRow(`
		SELECT object.memory_scope, object.project_namespace_id, object.original_repository_id,
		       object.logical_generation, capsule.redacted_summary, object.capture_host,
		       object.capture_session_ref, object.captured_at
		  FROM private_memory_objects object
		  JOIN memory_capsules capsule ON capsule.id=object.object_id
		 WHERE object.object_id=?`,
		created.ObjectID,
	).Scan(
		&scope, &namespace, &origin, &generation, &summary,
		&captureHost, &captureSession, &capturedAt,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`
		SELECT COUNT(*) FROM private_memory_objects
		 WHERE logical_memory_id=? AND lifecycle='active'`,
		created.ObjectID,
	).Scan(&activeHeads); err != nil {
		t.Fatal(err)
	}
	if scope != MemoryScopeProject || namespace != stableProjectNamespace("repository_pulse") ||
		origin != "repository_pulse" || generation != 3 || activeHeads != 1 ||
		summary != "Keep scope changes explicit and reversible." ||
		captureHost != "codex" || captureSession == "" ||
		capturedAt != now.Add(time.Second).Format(time.RFC3339Nano) {
		t.Fatalf(
			"head scope=%q namespace=%q origin=%q generation=%d active=%d summary=%q capture=%q/%q/%q",
			scope, namespace, origin, generation, activeHeads, summary,
			captureHost, captureSession, capturedAt,
		)
	}
}

func TestCandidateScopeCreatesPersonalGlobalWithoutASecondMove(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	binding := strings.Repeat("c", 64)
	req := trayFinalizeRequest("Nick prefers concise technical reports across every project.")
	req.BindingDigest = binding
	req.ResolverEpoch = 1
	req.Candidates[0].MemoryScope = MemoryScopePersonalGlobal
	finalized, err := s.FinalizeTurnForVerifiedBinding(
		req, now, 0, binding, "repository_project_a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CommitMemoryTrayCandidateForVerifiedBinding(
		finalized.Receipts[0].CandidateID, finalized.Receipts[0].CandidateVersion,
		now.Add(time.Second), binding, "repository_project_a", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	var scope string
	if err := s.DB().QueryRow(
		`SELECT memory_scope FROM private_memory_objects WHERE object_id=?`, created.ObjectID,
	).Scan(&scope); err != nil || scope != MemoryScopePersonalGlobal {
		t.Fatalf("created scope=%q err=%v", scope, err)
	}
	foreign, err := s.PersonalMemoryScopeSnapshotForBinding(
		strings.Repeat("d", 64), "repository_project_b",
	)
	if err != nil {
		t.Fatal(err)
	}
	resume, err := s.BuildResumeForPersonalScope(ResumeQuery{
		ThreadID: "project-b", ProjectID: "repository_project_b",
	}, foreign)
	if err != nil || !strings.Contains(resume.ResumeMarkdown, "concise technical reports") {
		t.Fatalf("personal memory did not cross projects: %q err=%v", resume.ResumeMarkdown, err)
	}
}

func TestConcurrentEditCancelAndImmediateCommitHaveOneWinner(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	finalized, err := s.FinalizeTurn(
		trayFinalizeRequest("The initial candidate participates in a three-way CAS race."),
		now, 10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending := finalized.Receipts[0]
	start := make(chan struct{})
	type outcome struct {
		receipt MemoryWriteReceipt
		err     error
	}
	outcomes := make(chan outcome, 3)
	go func() {
		<-start
		receipt, err := s.EditMemoryTrayCandidate(
			pending.CandidateID, 1,
			PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: ptr(safeTrayCapsule("The edited candidate won the internal CAS race."))},
			now, 10*time.Second,
		)
		outcomes <- outcome{receipt, err}
	}()
	go func() {
		<-start
		receipt, err := s.CancelMemoryTrayCandidate(pending.CandidateID, 1, now)
		outcomes <- outcome{receipt, err}
	}()
	go func() {
		<-start
		receipt, err := s.CommitMemoryTrayCandidate(pending.CandidateID, 1, now)
		outcomes <- outcome{receipt, err}
	}()
	close(start)
	successes := 0
	for i := 0; i < 3; i++ {
		result := <-outcomes
		if result.err == nil {
			successes++
			continue
		}
		if !errors.Is(result.err, ErrMemoryTrayVersionConflict) && !errors.Is(result.err, ErrMemoryTrayTerminal) {
			t.Fatalf("unexpected CAS loser error: %v", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("CAS race successes=%d, want exactly one", successes)
	}
	tray, err := s.ListMemoryTray(10)
	if err != nil || len(tray) != 1 {
		t.Fatalf("tray after CAS race: %#v err=%v", tray, err)
	}
	if tray[0].State != "pending" && tray[0].State != "canceled" && tray[0].State != "committed" {
		t.Fatalf("inconsistent post-race state: %#v", tray[0])
	}
}

func TestMemoryCandidateIsImmediatelyCommittableWithoutPresentation(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	finalized, err := s.FinalizeTurn(
		trayFinalizeRequest("A normal memory becomes durable without opening Memory Home."),
		now, 10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending := finalized.Receipts[0]
	committed, err := s.CommitMemoryTrayCandidate(pending.CandidateID, pending.CandidateVersion, now)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Status != MemoryWriteCreated || committed.ObjectID == "" {
		t.Fatalf("immediate commit receipt=%#v", committed)
	}
	var presentations int
	if err := s.DB().QueryRow(`
		SELECT COUNT(*) FROM memory_presentation_receipts WHERE candidate_id=?`,
		pending.CandidateID,
	).Scan(&presentations); err != nil {
		t.Fatal(err)
	}
	if presentations != 0 {
		t.Fatalf("immediate commit invented %d presentation receipts", presentations)
	}
}

func TestPresentMemoryTrayCandidateIsOptionalImmutableAudit(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	finalized, err := s.FinalizeTurn(
		trayFinalizeRequest("Memory Home may record an exact audit without authorizing persistence."),
		now, 10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending := finalized.Receipts[0]
	req := MemoryPresentationRequest{
		CandidateID: pending.CandidateID, CandidateVersion: pending.CandidateVersion,
		ContentDigest: pending.ContentDigest, BindingDigest: testTrayBindingDigest,
		TrustedSurfaceKind: "memory_home", TrustedSurfaceInstance: "home_session_01",
	}
	presentedAt := now
	receipt, err := s.PresentMemoryTrayCandidate(req, presentedAt, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.CandidateID != pending.CandidateID || receipt.CandidateVersion != 1 ||
		receipt.ContentDigest != pending.ContentDigest || receipt.BindingDigest != testTrayBindingDigest ||
		receipt.TrustedSurfaceKind != "memory_home" || receipt.TrustedSurfaceInstance != "home_session_01" ||
		receipt.PresentedAt != presentedAt.Format(time.RFC3339Nano) {
		t.Fatalf("presentation receipt lost exact binding: %#v", receipt)
	}
	replayed, err := s.PresentMemoryTrayCandidate(req, presentedAt.Add(5*time.Second), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != receipt {
		t.Fatalf("exact concurrent/retry presentation was not idempotent: first=%#v retry=%#v", receipt, replayed)
	}
	committed, err := s.CommitMemoryTrayCandidate(pending.CandidateID, 1, presentedAt)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Status != MemoryWriteCreated {
		t.Fatalf("committed status=%q", committed.Status)
	}
}

func TestTerminalMemoryReadinessFactsDoNotRequirePresentation(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	req := trayFinalizeRequest("Readiness uses the real terminal memory chain without a Home gate.")
	finalized, err := s.FinalizeTurn(req, now, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pending := finalized.Receipts[0]
	if facts, err := s.TerminalMemoryReadinessFacts("repository_pulse", testTrayBindingDigest, 10); err != nil || len(facts) != 0 {
		t.Fatalf("pending readiness facts=%#v err=%v", facts, err)
	}
	committed, err := s.CommitMemoryTrayCandidate(pending.CandidateID, pending.CandidateVersion, now)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := s.TerminalMemoryReadinessFacts("repository_pulse", testTrayBindingDigest, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("terminal readiness facts=%#v", facts)
	}
	fact := facts[0]
	if fact.ReceiptID != committed.ReceiptID || fact.PresentationReceiptID != "" ||
		fact.ObjectID != committed.ObjectID || fact.Status != string(MemoryWriteCreated) ||
		fact.ContentDigest != pending.ContentDigest || fact.MemoryKind != "decision" ||
		fact.ConversationScope != "current_turn" || fact.BindingDigest != testTrayBindingDigest ||
		fact.RepositoryID != "repository_pulse" || fact.Host != req.Host ||
		fact.SessionRef != opaqueTurnCorrelation("session", req.SessionID) || !fact.Active || len(fact.EvidenceIDs) != 0 {
		t.Fatalf("terminal readiness fact lost immutable chain: %#v", fact)
	}
	otherBinding := strings.Repeat("b", 64)
	if err := s.ConfigureProductRuntimeAuthority(otherBinding, 0, 0); err != nil {
		t.Fatal(err)
	}
	if facts, err := s.TerminalMemoryReadinessFacts("repository_other", otherBinding, 10); err != nil || len(facts) != 0 {
		t.Fatalf("cross-binding readiness facts=%#v err=%v", facts, err)
	}
	if _, err := s.TerminalMemoryReadinessFacts("repository_pulse", testTrayBindingDigest, 10); !errors.Is(err, ErrProductRuntimeMismatch) {
		t.Fatalf("stale signed binding error=%v", err)
	}
	if err := s.ConfigureProductRuntimeAuthority(testTrayBindingDigest, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteCommittedMemory(committed.ObjectID, "delete_readiness_fact", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if facts, err := s.TerminalMemoryReadinessFacts("repository_pulse", testTrayBindingDigest, 10); err != nil || len(facts) != 0 {
		t.Fatalf("deleted readiness facts=%#v err=%v", facts, err)
	}
	if _, err := s.TerminalMemoryReadinessFacts("", testTrayBindingDigest, 10); err == nil {
		t.Fatal("empty repository identity was accepted")
	}
	if _, err := s.TerminalMemoryReadinessFacts("repository_pulse", testTrayBindingDigest, 0); err == nil {
		t.Fatal("unbounded readiness query was accepted")
	}
}

func TestConcurrentExactPresentationsConvergeOnOneImmutableReceipt(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	finalized, err := s.FinalizeTurn(
		trayFinalizeRequest("Concurrent Home render acknowledgements converge exactly once."), now, 10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending := finalized.Receipts[0]
	req := MemoryPresentationRequest{
		CandidateID: pending.CandidateID, CandidateVersion: pending.CandidateVersion,
		ContentDigest: pending.ContentDigest, BindingDigest: testTrayBindingDigest,
		TrustedSurfaceKind: "memory_home", TrustedSurfaceInstance: "home_session_race",
	}
	type result struct {
		receipt MemoryPresentationReceipt
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			receipt, err := s.PresentMemoryTrayCandidate(req, now, 10*time.Second)
			results <- result{receipt: receipt, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent presentation errors: first=%v second=%v", first.err, second.err)
	}
	if first.receipt != second.receipt {
		t.Fatalf("concurrent presentations diverged: first=%#v second=%#v", first.receipt, second.receipt)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM memory_presentation_receipts WHERE candidate_id=?`, pending.CandidateID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent exact presentation receipt count=%d, want 1", count)
	}
	if _, err := s.DB().Exec(`UPDATE memory_presentation_receipts SET presented_at=? WHERE receipt_id=?`, now.Add(time.Second).Format(time.RFC3339Nano), first.receipt.ReceiptID); err == nil {
		t.Fatal("presentation receipt was mutable")
	}
	if _, err := s.DB().Exec(`DELETE FROM memory_presentation_receipts WHERE receipt_id=?`, first.receipt.ReceiptID); err == nil {
		t.Fatal("presentation receipt was deletable")
	}
}

func TestConcurrentOptionalPresentationEditAndCancelPreserveCAS(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	finalized, err := s.FinalizeTurn(
		trayFinalizeRequest("Optional presentation races stay bound to the exact candidate version."), now, 10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending := finalized.Receipts[0]
	presentReq := MemoryPresentationRequest{
		CandidateID: pending.CandidateID, CandidateVersion: 1, ContentDigest: pending.ContentDigest,
		BindingDigest: testTrayBindingDigest, TrustedSurfaceKind: "memory_home",
		TrustedSurfaceInstance: "home_session_control_race",
	}
	start := make(chan struct{})
	errs := make(chan error, 3)
	go func() {
		<-start
		_, err := s.PresentMemoryTrayCandidate(presentReq, now, 10*time.Second)
		errs <- err
	}()
	go func() {
		<-start
		_, err := s.EditMemoryTrayCandidate(
			pending.CandidateID, 1,
			PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: ptr(safeTrayCapsule("The edited race winner remains immediately committable."))},
			now, 10*time.Second,
		)
		errs <- err
	}()
	go func() {
		<-start
		_, err := s.CancelMemoryTrayCandidate(pending.CandidateID, 1, now)
		errs <- err
	}()
	close(start)
	for range 3 {
		err := <-errs
		if err != nil && !errors.Is(err, ErrMemoryTrayVersionConflict) && !errors.Is(err, ErrMemoryTrayTerminal) {
			t.Fatalf("unexpected presentation/control race error: %v", err)
		}
	}
	var state, digest string
	var version int
	if err := s.DB().QueryRow(`
		SELECT state, version, content_digest
		  FROM memory_tray_candidates WHERE candidate_id=?`, pending.CandidateID,
	).Scan(&state, &version, &digest); err != nil {
		t.Fatal(err)
	}
	switch {
	case state == "canceled":
		if _, err := s.CommitMemoryTrayCandidate(pending.CandidateID, version, now); !errors.Is(err, ErrMemoryTrayTerminal) {
			t.Fatalf("canceled race winner became committable: %v", err)
		}
	case state == "pending" && version == 1:
		if digest != pending.ContentDigest {
			t.Fatalf("original race winner changed digest: state=%q version=%d digest=%q", state, version, digest)
		}
		if _, err := s.CommitMemoryTrayCandidate(pending.CandidateID, version, now); err != nil {
			t.Fatalf("unmodified race winner was not immediately committable: %v", err)
		}
	case state == "pending" && version == 2:
		if digest == pending.ContentDigest {
			t.Fatalf("edited race winner retained original digest %q", digest)
		}
		if _, err := s.CommitMemoryTrayCandidate(pending.CandidateID, version, now); err != nil {
			t.Fatalf("edited race winner was not immediately committable: %v", err)
		}
	default:
		t.Fatalf("unexpected presentation/control race state=%q version=%d", state, version)
	}
}

func TestDurableMemoryCorrectionAndDeleteDoNotRequirePresentation(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	finalized, err := s.FinalizeTurn(
		trayFinalizeRequest("The original durable memory can be corrected from Home."), now, 10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CommitMemoryTrayCandidate(
		finalized.Receipts[0].CandidateID, finalized.Receipts[0].CandidateVersion, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	replacement := safeTrayCapsule("The corrected durable memory is used in later sessions.")
	correction, err := s.PrepareMemoryCorrection(
		created.ObjectID,
		PrivateMemoryCandidate{Kind: PrivateMemoryCandidateCapsule, Capsule: &replacement},
		now.Add(time.Second), 10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.CommitMemoryTrayCandidate(
		correction.Receipts[0].CandidateID, correction.Receipts[0].CandidateVersion, now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != MemoryWriteUpdated || updated.ObjectID != created.ObjectID {
		t.Fatalf("correction receipt created=%#v updated=%#v", created, updated)
	}
	var summary string
	if err := s.DB().QueryRow(`
		SELECT redacted_summary FROM memory_capsules WHERE id=?`,
		created.ObjectID,
	).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if summary != replacement.Items[0].RedactedSummary {
		t.Fatalf("corrected summary=%q", summary)
	}
	deleted, err := s.DeleteCommittedMemory(created.ObjectID, "delete_corrected_memory_01", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Status != MemoryWriteUpdated || deleted.ObjectID != created.ObjectID || deleted.ReasonCode != "user_deleted" {
		t.Fatalf("delete receipt=%#v", deleted)
	}
	var lifecycle string
	if err := s.DB().QueryRow(`
		SELECT lifecycle FROM private_memory_objects WHERE object_id=?`,
		created.ObjectID,
	).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "deleted" {
		t.Fatalf("deleted lifecycle=%q", lifecycle)
	}
}

func TestMemoryHomeSummaryEditIsImmediateAndCannotCrossProjectBinding(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	bindingA := strings.Repeat("a", 64)
	if err := s.ConfigureProductRuntimeAuthority(bindingA, 4, 7); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	req := trayFinalizeRequest("The original project-scoped Home memory.")
	req.BindingDigest = bindingA
	req.PolicyEpoch = 4
	req.ResolverEpoch = 7
	finalized, err := s.FinalizeTurn(req, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CommitMemoryTrayCandidate(
		finalized.Receipts[0].CandidateID, finalized.Receipts[0].CandidateVersion, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	correction, err := s.PrepareMemorySummaryCorrectionWithInvocation(
		created.ObjectID, "The corrected project-scoped Home memory.",
		"home_summary_edit_01", now.Add(time.Second), 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(correction.Receipts) != 1 {
		t.Fatalf("correction receipts=%d, want 1", len(correction.Receipts))
	}
	updated, err := s.CommitMemoryTrayCandidate(
		correction.Receipts[0].CandidateID,
		correction.Receipts[0].CandidateVersion,
		now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != MemoryWriteUpdated || updated.ObjectID != created.ObjectID ||
		updated.ReasonCode != "user_corrected" {
		t.Fatalf("updated receipt=%#v", updated)
	}
	var summary string
	if err := s.DB().QueryRow(
		`SELECT redacted_summary FROM memory_capsules WHERE id=?`, created.ObjectID,
	).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if summary != "The corrected project-scoped Home memory." {
		t.Fatalf("summary=%q", summary)
	}

	if err := s.ConfigureProductRuntimeAuthority(strings.Repeat("b", 64), 8, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareMemorySummaryCorrectionWithInvocation(
		created.ObjectID, "This project must not edit another project.",
		"home_summary_edit_cross_binding", now.Add(2*time.Second), 0,
	); !errors.Is(err, ErrProductRuntimeMismatch) {
		t.Fatalf("cross-binding edit error=%v, want %v", err, ErrProductRuntimeMismatch)
	}
}

func TestPresentMemoryTrayCandidateRejectsStaleDigestBindingAndUntrustedSurface(t *testing.T) {
	s, _ := openPersonalTrayStore(t)
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	finalized, err := s.FinalizeTurn(
		trayFinalizeRequest("Presentation authority is narrower than daemon authority."), now, 10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending := finalized.Receipts[0]
	base := MemoryPresentationRequest{
		CandidateID: pending.CandidateID, CandidateVersion: 1, ContentDigest: pending.ContentDigest,
		BindingDigest: testTrayBindingDigest, TrustedSurfaceKind: "memory_home",
		TrustedSurfaceInstance: "home_session_01",
	}
	stale := base
	stale.ContentDigest = strings.Repeat("0", 64)
	if _, err := s.PresentMemoryTrayCandidate(stale, now, 10*time.Second); !errors.Is(err, ErrMemoryPresentationConflict) {
		t.Fatalf("stale digest err=%v", err)
	}
	wrongBinding := base
	wrongBinding.BindingDigest = strings.Repeat("1", 64)
	if _, err := s.PresentMemoryTrayCandidate(wrongBinding, now, 10*time.Second); !errors.Is(err, ErrProductRuntimeMismatch) {
		t.Fatalf("wrong binding err=%v", err)
	}
	untrusted := base
	untrusted.TrustedSurfaceKind = "mcp"
	if _, err := s.PresentMemoryTrayCandidate(untrusted, now, 10*time.Second); err == nil {
		t.Fatal("untrusted MCP surface minted presentation")
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM memory_presentation_receipts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected presentation persisted %d receipts", count)
	}
}

func TestUnassignedAssignmentTurnCorrelationGoldenVector(t *testing.T) {
	invocationID, err := normalizeManualInvocation("unassigned", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	got := opaqueTurnCorrelation("turn", "unassigned_turn_"+invocationID)
	const want = "turn:07f170a4518a07651e47c22799e808411ce177ba80a8db548f7d8b3ceec678a3"
	if got != want {
		t.Fatalf("protected Unassigned turn = %q, want %q", got, want)
	}
}
