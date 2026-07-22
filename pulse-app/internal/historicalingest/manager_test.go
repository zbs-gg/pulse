package historicalingest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/platform"
)

func testIngestManager(t *testing.T, clock func() time.Time) (*IngestManager, IngestManagerConfig) {
	t.Helper()
	sequence := 0
	cfg := IngestManagerConfig{
		RootDir: filepath.Join(t.TempDir(), "historical-ingest"),
		Key:     []byte(strings.Repeat("k", 32)),
		Clock:   clock,
		NewLeaseID: func() string {
			sequence++
			return "lease_" + strings.Repeat(string(rune('a'+sequence)), 32)
		},
		LeaseTTL: 5 * time.Minute,
	}
	manager, err := NewIngestManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return manager, cfg
}

func testSnapshot() SourceSnapshot {
	return SourceSnapshot{
		Digest: strings.Repeat("a", 64), Cutoff: time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC),
		RootCount: 2, ParserVersion: CodexParserVersionV1,
		Files: []SourceFilePrefix{
			{Alias: "source_0123456789abcdef", CapturedBytes: 100, PrefixDigest: strings.Repeat("b", 64), RootID: "root_a", ParserVersion: CodexParserVersionV1, RecordCount: 2, IncludedCount: 1, ExcludedCount: 1},
			{Alias: "source_fedcba9876543210", CapturedBytes: 80, PrefixDigest: strings.Repeat("c", 64), RootID: "root_b", ParserVersion: CodexParserVersionV1, RecordCount: 1, IncludedCount: 1},
		},
	}
}

func testSnapshotOne() SourceSnapshot {
	snapshot := testSnapshot()
	snapshot.RootCount = 1
	snapshot.Files = snapshot.Files[:1]
	return snapshot
}

func testUnits() []WorkUnit {
	return []WorkUnit{
		{ID: "unit_a", RootID: "root_a", SnapshotDigest: strings.Repeat("a", 64), EvidenceDigest: strings.Repeat("d", 64), SourceAliases: []string{"source_0123456789abcdef"}, Ordinal: 0},
		{ID: "unit_b", RootID: "root_b", SnapshotDigest: strings.Repeat("a", 64), EvidenceDigest: strings.Repeat("e", 64), SourceAliases: []string{"source_fedcba9876543210"}, Ordinal: 0},
	}
}

func testRunnerContract() RunnerContract {
	return RunnerContract{
		Digest: strings.Repeat("f", 64), SchemaDigest: SchemaDigest(), ModelID: "gpt-5.6-luna",
		ModelEffort: "low", ParserVersion: CodexParserVersionV1, PromptVersion: "historical_prompt_v1",
	}
}

func testUnitResult(unit WorkUnit, summary string) WorkUnitResult {
	return WorkUnitResult{
		SchemaVersion: SchemaVersionV1, WorkUnitID: unit.ID, EvidenceDigest: unit.EvidenceDigest,
		Items: []MaterialItem{{
			CandidateID: "candidate_0123456789abcdef", Kind: MaterialKindDecision, Confidence: .9,
			Privacy: PrivacyPrivate, EpistemicStatus: EpistemicExplicit, Derivation: DerivationDirect,
			ValidTime: ValidTime{From: time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)}, Scope: Scope{Kind: ScopeGlobal},
			SourceRefs: []SourceRef{{Alias: unit.SourceAliases[0], PrefixDigest: map[string]string{"unit_a": strings.Repeat("b", 64), "unit_b": strings.Repeat("c", 64)}[unit.ID], RecordLocator: "record:1"}},
			Payload:    MaterialPayload{Summary: summary},
		}},
	}
}

func TestConcurrentWorkersCannotLeaseSameUnit(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	manager, _ := testIngestManager(t, func() time.Time { return now })
	if _, err := manager.StartJob("job_0123456789abcdef", testSnapshotOne(), testUnits()[:1], testRunnerContract()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	leases := []WorkLease{}
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := manager.LeaseNext("job_0123456789abcdef")
			if err == nil {
				mu.Lock()
				leases = append(leases, lease)
				mu.Unlock()
			} else if !errors.Is(err, ErrNoWorkAvailable) {
				t.Errorf("unexpected lease error: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(leases) != 1 || leases[0].Unit.ID != "unit_a" {
		t.Fatalf("leases=%+v", leases)
	}
}

func TestAcceptedResultIsDurableIdempotentAndBuildsDeterministicManifest(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	manager, cfg := testIngestManager(t, func() time.Time { return now })
	jobID := "job_0123456789abcdef"
	if _, err := manager.StartJob(jobID, testSnapshot(), testUnits(), testRunnerContract()); err != nil {
		t.Fatal(err)
	}
	usage := TokenUsage{InputTokens: 100, CachedInputTokens: 10, OutputTokens: 20, ReasoningTokens: 2}
	for index, unit := range testUnits() {
		lease, err := manager.LeaseNext(jobID)
		if err != nil || lease.Unit.ID != unit.ID {
			t.Fatalf("lease %d: %+v %v", index, lease, err)
		}
		receipt, err := manager.SubmitResult(jobID, unit.ID, lease.Token, testUnitResult(unit, "decision "+unit.ID), usage, testRunnerContract().Digest)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := manager.SubmitResult(jobID, unit.ID, lease.Token, testUnitResult(unit, "decision "+unit.ID), usage, testRunnerContract().Digest)
		if err != nil || replayed != receipt {
			t.Fatalf("replay=%+v receipt=%+v err=%v", replayed, receipt, err)
		}
	}
	manifest, digest, aggregate, err := manager.BuildManifest(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Items) != 2 || !strings.HasPrefix(manifest.Items[0].CandidateID, "candidate_") || digest == "" {
		t.Fatalf("manifest=%+v digest=%s", manifest, digest)
	}
	if aggregate.InputTokens != 200 || aggregate.OutputTokens != 40 {
		t.Fatalf("usage=%+v", aggregate)
	}
	reloaded, err := NewIngestManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	replayedManifest, replayedDigest, replayedUsage, err := reloaded.BuildManifest(jobID)
	if err != nil || replayedDigest != digest || replayedUsage != aggregate || len(replayedManifest.Items) != len(manifest.Items) {
		t.Fatalf("reloaded digest=%s usage=%+v err=%v", replayedDigest, replayedUsage, err)
	}
}

func TestCrashBeforeSubmitAndDuringCheckpointWriteResumeWithoutDuplicateWork(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	manager, cfg := testIngestManager(t, func() time.Time { return now })
	jobID := "job_0123456789abcdef"
	if _, err := manager.StartJob(jobID, testSnapshotOne(), testUnits()[:1], testRunnerContract()); err != nil {
		t.Fatal(err)
	}
	first, err := manager.LeaseNext(jobID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate process loss before submit. Reload cannot duplicate the live
	// lease; after its exact expiry it re-leases only the same uncommitted unit.
	reloaded, err := NewIngestManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.LeaseNext(jobID); !errors.Is(err, ErrNoWorkAvailable) {
		t.Fatalf("live lease duplicated after restart: %v", err)
	}
	now = now.Add(6 * time.Minute)
	second, err := reloaded.LeaseNext(jobID)
	if err != nil || second.Unit.ID != first.Unit.ID || second.Token == first.Token {
		t.Fatalf("expired lease resume=%+v err=%v", second, err)
	}

	writeCount := 0
	faultRoot := filepath.Join(t.TempDir(), "checkpoint-fault")
	faultCfg := cfg
	faultCfg.RootDir = faultRoot
	faultCfg.CheckpointWrite = func(path string, payload []byte) error {
		writeCount++
		if writeCount == 2 {
			return errors.New("injected checkpoint interruption")
		}
		_, err := platform.CreatePrivateFileExclusive(path, payload)
		return err
	}
	faulted, err := NewIngestManager(faultCfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := faulted.StartJob(jobID, testSnapshotOne(), testUnits()[:1], testRunnerContract()); err != nil {
		t.Fatal(err)
	}
	if _, err := faulted.LeaseNext(jobID); err == nil {
		t.Fatal("injected checkpoint write succeeded")
	}
	recovered, err := NewIngestManager(faultCfg)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := recovered.LeaseNext(jobID)
	if err != nil || lease.Unit.ID != "unit_a" {
		t.Fatalf("checkpoint recovery lease=%+v err=%v", lease, err)
	}
}

func TestQuotaPausePreservesAcceptedCheckpointAndResumesExactNextUnit(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	manager, _ := testIngestManager(t, func() time.Time { return now })
	jobID := "job_0123456789abcdef"
	if _, err := manager.StartJob(jobID, testSnapshot(), testUnits(), testRunnerContract()); err != nil {
		t.Fatal(err)
	}
	first, _ := manager.LeaseNext(jobID)
	if _, err := manager.SubmitResult(jobID, first.Unit.ID, first.Token, testUnitResult(first.Unit, "first"), TokenUsage{InputTokens: 10}, testRunnerContract().Digest); err != nil {
		t.Fatal(err)
	}
	second, _ := manager.LeaseNext(jobID)
	status, err := manager.PauseQuota(jobID, second.Unit.ID, second.Token)
	if err != nil || status.State != JobPausedQuota || status.AcceptedUnits != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err := manager.LeaseNext(jobID); !errors.Is(err, ErrJobNotExtracting) {
		t.Fatalf("lease while paused: %v", err)
	}
	if _, err := manager.ResumeJob(jobID); err != nil {
		t.Fatal(err)
	}
	resumed, err := manager.LeaseNext(jobID)
	if err != nil || resumed.Unit.ID != second.Unit.ID || resumed.Token == second.Token {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
}

func TestSubmitRejectsCrossUnitProvenanceAndSensitiveOutput(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	manager, _ := testIngestManager(t, func() time.Time { return now })
	jobID := "job_0123456789abcdef"
	if _, err := manager.StartJob(jobID, testSnapshot(), testUnits(), testRunnerContract()); err != nil {
		t.Fatal(err)
	}
	lease, err := manager.LeaseNext(jobID)
	if err != nil {
		t.Fatal(err)
	}
	cross := testUnitResult(lease.Unit, "cross source")
	cross.Items[0].SourceRefs[0] = SourceRef{Alias: "source_fedcba9876543210", PrefixDigest: strings.Repeat("c", 64), RecordLocator: "record:1"}
	if _, err := manager.SubmitResult(jobID, lease.Unit.ID, lease.Token, cross, TokenUsage{}, testRunnerContract().Digest); !errors.Is(err, ErrResultConflict) {
		t.Fatalf("cross-unit provenance error=%v", err)
	}
	sensitive := testUnitResult(lease.Unit, "authorization: bearer secret")
	if _, err := manager.SubmitResult(jobID, lease.Unit.ID, lease.Token, sensitive, TokenUsage{}, testRunnerContract().Digest); !errors.Is(err, ErrResultConflict) {
		t.Fatalf("sensitive result error=%v", err)
	}
}

func TestTamperedCheckpointAndUnsafeCleanupFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	manager, cfg := testIngestManager(t, func() time.Time { return now })
	jobID := "job_0123456789abcdef"
	if _, err := manager.StartJob(jobID, testSnapshotOne(), testUnits()[:1], testRunnerContract()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cfg.RootDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "checkpoint-") {
			if err := os.WriteFile(filepath.Join(cfg.RootDir, entry.Name()), []byte(`{"tampered":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if _, err := NewIngestManager(cfg); !errors.Is(err, ErrIngestCheckpointIntegrity) {
		t.Fatalf("reload error=%v", err)
	}
	unsafe := filepath.Join(cfg.RootDir, "result-"+strings.Repeat("f", 64)+".json")
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), unsafe); err != nil {
		t.Fatal(err)
	}
	if err := manager.CleanupJob(jobID, RetentionCanceled); err == nil {
		t.Fatal("cleanup accepted symlink substitution")
	}
}

func TestBuildManifestMarksChangedSourceSnapshotStale(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	manager, cfg := testIngestManager(t, func() time.Time { return now })
	cfg.VerifySnapshot = func(SourceSnapshot) error { return errors.New("source prefix changed") }
	manager, err := NewIngestManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	jobID := "job_0123456789abcdef"
	if _, err := manager.StartJob(jobID, testSnapshotOne(), testUnits()[:1], testRunnerContract()); err != nil {
		t.Fatal(err)
	}
	lease, err := manager.LeaseNext(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SubmitResult(jobID, lease.Unit.ID, lease.Token, testUnitResult(lease.Unit, "decision"), TokenUsage{}, testRunnerContract().Digest); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := manager.BuildManifest(jobID); err == nil {
		t.Fatal("changed source snapshot produced a manifest")
	}
	status, err := manager.Status(jobID)
	if err != nil || status.State != JobStale || status.ManifestDigest != "" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestCleanupRetentionMatrix(t *testing.T) {
	for _, test := range []struct {
		reason RetentionReason
		keeps  bool
	}{
		{RetentionSuccess, true},
		{RetentionExported, true},
		{RetentionCanceled, false},
		{RetentionFailure, false},
		{RetentionSuperseded, false},
		{RetentionExpired, false},
		{RetentionDestructive, false},
	} {
		t.Run(string(test.reason), func(t *testing.T) {
			now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
			manager, cfg := testIngestManager(t, func() time.Time { return now })
			jobID := "job_0123456789abcdef"
			if _, err := manager.StartJob(jobID, testSnapshotOne(), testUnits()[:1], testRunnerContract()); err != nil {
				t.Fatal(err)
			}
			lease, err := manager.LeaseNext(jobID)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := manager.SubmitResult(jobID, lease.Unit.ID, lease.Token, testUnitResult(lease.Unit, "decision"), TokenUsage{}, testRunnerContract().Digest)
			if err != nil {
				t.Fatal(err)
			}
			_, manifestDigest, _, err := manager.BuildManifest(jobID)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.CleanupJob(jobID, test.reason); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"result-" + receipt.ResultDigest + ".json", "manifest-" + manifestDigest + ".json"} {
				_, err := os.Lstat(filepath.Join(cfg.RootDir, name))
				if test.keeps && err != nil {
					t.Fatalf("%s should be retained: %v", name, err)
				}
				if !test.keeps && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s should be removed: %v", name, err)
				}
			}
		})
	}
}
