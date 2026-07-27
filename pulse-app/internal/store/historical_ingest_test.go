package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/historicalingest"
)

func historicalApplyFixture(t *testing.T) (*Store, historicalingest.ApplySource, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "personal.db")
	vault, err := OpenVault(path, StoreKindPersonal, "store_personal_history_test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	binding := strings.Repeat("a", 64)
	repository := "repository_history_test"
	if err := vault.ConfigureProductRuntimeAuthority(binding, 7, 11); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, repository); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	item := historicalingest.MaterialItem{
		CandidateID: "candidate_0123456789abcdef", Kind: historicalingest.MaterialKindDecision,
		Confidence: .97, Privacy: historicalingest.PrivacyPrivate,
		EpistemicStatus: historicalingest.EpistemicExplicit, Derivation: historicalingest.DerivationDirect,
		ValidTime:  historicalingest.ValidTime{From: now},
		Scope:      historicalingest.Scope{Kind: historicalingest.ScopeProject, ProjectID: stableProjectNamespace(repository)},
		SourceRefs: []historicalingest.SourceRef{{Alias: "source_0123456789abcdef", PrefixDigest: strings.Repeat("b", 64), RecordLocator: "record:1"}},
		Payload:    historicalingest.MaterialPayload{Summary: "Keep the exact reviewed historical decision available in the current project."},
	}
	manifest := historicalingest.Manifest{
		SchemaVersion: historicalingest.SchemaVersionV1, JobID: "job_0123456789abcdef", Revision: 3,
		SourceSnapshotDigest: strings.Repeat("c", 64), Items: []historicalingest.MaterialItem{item},
	}
	manifestBytes, err := historicalingest.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestSum := sha256.Sum256(manifestBytes)
	manifestDigest := hex.EncodeToString(manifestSum[:])
	source := historicalingest.ApplySource{
		Manifest: manifest, ManifestDigest: manifestDigest,
		Snapshot: historicalingest.SourceSnapshot{
			Digest: manifest.SourceSnapshotDigest, Cutoff: now, RootCount: 1,
			ParserVersion: historicalingest.CodexParserVersionV1,
			Files: []historicalingest.SourceFilePrefix{{
				Alias: item.SourceRefs[0].Alias, CapturedBytes: 128, PrefixDigest: item.SourceRefs[0].PrefixDigest,
				RootID: "root_history", ParserVersion: historicalingest.CodexParserVersionV1,
				RecordCount: 1, IncludedCount: 1,
			}},
		},
		Contract: historicalingest.RunnerContract{
			Digest: strings.Repeat("d", 64), SchemaDigest: historicalingest.SchemaDigest(),
			ModelID: "gpt-5.6-luna", ModelEffort: "low", ParserVersion: historicalingest.CodexParserVersionV1,
			PromptVersion: "historical_prompt_v1",
		},
		Dispositions: map[string]historicalingest.ReviewDisposition{item.CandidateID: historicalingest.ReviewKept},
	}
	return vault, source, binding, repository
}

func TestHistoricalApplyCompilesBacksUpCommitsAndReplaysExactly(t *testing.T) {
	vault, source, binding, repository := historicalApplyFixture(t)
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	set, writeSetDigest, err := vault.CompileHistoricalWriteSet(source, binding, repository)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(set.Items) != 1 || set.Items[0].Target.Outcome != historicalingest.ItemCreated || writeSetDigest == "" {
		t.Fatalf("write set=%+v digest=%q", set, writeSetDigest)
	}
	backup, err := vault.CreateHistoricalBackup(set.JobID, writeSetDigest)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if !backup.IntegrityOK || !backup.ForeignKeysOK {
		t.Fatalf("backup receipt=%+v", backup)
	}
	if info, err := os.Stat(backup.path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file info=%v err=%v", info, err)
	}
	restored, err := RestoreHistoricalBackupTo(backup, filepath.Join(t.TempDir(), "restored", "personal.db"))
	if err != nil || !restored.IntegrityOK || !restored.ForeignKeysOK {
		t.Fatalf("restore proof=%+v err=%v", restored, err)
	}
	if info, err := os.Stat(restored.path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored file info=%v err=%v", info, err)
	}
	capability, err := vault.AuthorizeHistoricalApply(set.JobID, writeSetDigest, set.DestinationGeneration, now)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	receipt, err := vault.ApplyHistoricalWriteSet(capability, now.Add(time.Second))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if receipt.CreatedCount != 1 || receipt.DeduplicatedCount != 0 || len(receipt.Outcomes) != 1 ||
		receipt.Outcomes[0].ObjectID != set.Items[0].Target.ObjectID || receipt.WriteSetDigest != writeSetDigest {
		t.Fatalf("receipt=%+v", receipt)
	}
	var capsules, objects, itemReceipts, outbox int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules WHERE id=?`, receipt.Outcomes[0].ObjectID).Scan(&capsules); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM private_memory_objects WHERE object_id=? AND lifecycle='active'`, receipt.Outcomes[0].ObjectID).Scan(&objects); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM historical_ingest_item_receipts WHERE receipt_id=?`, receipt.ReceiptID).Scan(&itemReceipts); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM historical_ingest_projection_outbox WHERE receipt_id=?`, receipt.ReceiptID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if capsules != 1 || objects != 1 || itemReceipts != 1 || outbox != 1 {
		t.Fatalf("canonical state capsules=%d objects=%d item_receipts=%d outbox=%d", capsules, objects, itemReceipts, outbox)
	}
	recalled, err := vault.RecallMemory(RecallMemoryQuery{Query: "reviewed historical decision", PrivacyCeiling: "private", Limit: 5})
	if err != nil || len(recalled) != 1 || recalled[0].ID != receipt.Outcomes[0].ObjectID {
		t.Fatalf("current project recall=%+v err=%v", recalled, err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_other_project"); err != nil {
		t.Fatal(err)
	}
	recalled, err = vault.RecallMemory(RecallMemoryQuery{Query: "reviewed historical decision", PrivacyCeiling: "private", Limit: 5})
	if err != nil || len(recalled) != 0 {
		t.Fatalf("other project leaked history=%+v err=%v", recalled, err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, repository); err != nil {
		t.Fatal(err)
	}
	replay, err := vault.ApplyHistoricalWriteSet(capability, now.Add(2*time.Second))
	if err != nil || replay.ReceiptID != receipt.ReceiptID || len(replay.Outcomes) != 1 {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules WHERE id=?`, receipt.Outcomes[0].ObjectID).Scan(&capsules); err != nil || capsules != 1 {
		t.Fatalf("replay capsule count=%d err=%v", capsules, err)
	}
}

func TestHistoricalProjectScopeDoesNotTrustSemanticProjectIDAsCurrentRepository(t *testing.T) {
	repository := "repository_current"
	current := historicalPersonalScope(historicalingest.Scope{Kind: historicalingest.ScopeProject, ProjectID: stableProjectNamespace(repository)}, "job_0123456789abcdef", repository)
	if current.ProjectNamespaceID != stableProjectNamespace(repository) || current.OriginalRepository != repository {
		t.Fatalf("current scope=%+v", current)
	}
	unmapped := historicalPersonalScope(historicalingest.Scope{Kind: historicalingest.ScopeProject, ProjectID: "project_pulse"}, "job_0123456789abcdef", repository)
	if unmapped.ProjectNamespaceID == current.ProjectNamespaceID || unmapped.OriginalRepository == repository {
		t.Fatalf("unmapped historical project leaked into current scope: %+v", unmapped)
	}
}

func TestHistoricalApplyRejectsDestinationGenerationDriftBeforeMutation(t *testing.T) {
	vault, source, binding, repository := historicalApplyFixture(t)
	set, writeSetDigest, err := vault.CompileHistoricalWriteSet(source, binding, repository)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	capability, err := vault.AuthorizeHistoricalApply(set.JobID, writeSetDigest, set.DestinationGeneration, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.DB().Exec(`UPDATE personal_memory_scope_state SET eligibility_revision=eligibility_revision+1`); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.ApplyHistoricalWriteSet(capability, now.Add(time.Second)); !errors.Is(err, historicalingest.ErrApplyDestination) {
		t.Fatalf("apply error=%v", err)
	}
	var capsules, receipts int
	_ = vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&capsules)
	_ = vault.DB().QueryRow(`SELECT count(*) FROM historical_ingest_batch_receipts`).Scan(&receipts)
	if capsules != 0 || receipts != 0 {
		t.Fatalf("partial mutation capsules=%d receipts=%d", capsules, receipts)
	}
	refreshed, refreshedDigest, err := vault.CompileHistoricalWriteSet(source, binding, repository)
	if err != nil || refreshed.DestinationGeneration <= set.DestinationGeneration || refreshedDigest == writeSetDigest {
		t.Fatalf("refreshed set=%+v digest=%q err=%v", refreshed, refreshedDigest, err)
	}
	refreshedCapability, err := vault.AuthorizeHistoricalApply(refreshed.JobID, refreshedDigest, refreshed.DestinationGeneration, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.ApplyHistoricalWriteSet(refreshedCapability, now.Add(time.Second)); err != nil {
		t.Fatalf("refreshed apply: %v", err)
	}
}

func TestHistoricalApplyRejectsWrongOrExpiredServerCapability(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(HistoricalApplyCapability, time.Time) (HistoricalApplyCapability, time.Time)
	}{
		{name: "wrong token", mutate: func(capability HistoricalApplyCapability, now time.Time) (HistoricalApplyCapability, time.Time) {
			capability.Token = strings.Repeat("0", len(capability.Token))
			return capability, now.Add(time.Second)
		}},
		{name: "expired", mutate: func(capability HistoricalApplyCapability, now time.Time) (HistoricalApplyCapability, time.Time) {
			return capability, now.Add(historicalingest.ApplyAuthorizationTTL + time.Second)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			vault, source, binding, repository := historicalApplyFixture(t)
			set, digest, err := vault.CompileHistoricalWriteSet(source, binding, repository)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
			capability, err := vault.AuthorizeHistoricalApply(set.JobID, digest, set.DestinationGeneration, now)
			if err != nil {
				t.Fatal(err)
			}
			capability, applyAt := test.mutate(capability, now)
			if _, err := vault.ApplyHistoricalWriteSet(capability, applyAt); !errors.Is(err, historicalingest.ErrApplyAuthorization) {
				t.Fatalf("apply error=%v", err)
			}
			var receipts int
			_ = vault.DB().QueryRow(`SELECT count(*) FROM historical_ingest_batch_receipts`).Scan(&receipts)
			if receipts != 0 {
				t.Fatalf("unauthorized apply wrote %d receipts", receipts)
			}
		})
	}
}
