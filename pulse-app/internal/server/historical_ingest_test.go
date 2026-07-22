package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/historicalingest"
	"github.com/nkkmnk/pulse/internal/store"
)

func writeHistoricalCodexFixture(t *testing.T, dir, id, timestamp, text string) {
	t.Helper()
	meta := map[string]any{"timestamp": timestamp, "type": "session_meta", "payload": map[string]any{"id": id}}
	message := map[string]any{"timestamp": timestamp, "type": "response_item", "payload": map[string]any{"id": "msg_" + id, "type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": text}}}}
	first, _ := json.Marshal(meta)
	second, _ := json.Marshal(message)
	if err := os.WriteFile(filepath.Join(dir, "rollout-"+id+".jsonl"), append(append(first, '\n'), append(second, '\n')...), 0o600); err != nil {
		t.Fatal(err)
	}
}

type historicalEvidenceFunc func(context.Context, historicalingest.WorkUnit) (HistoricalIngestWorkPayload, error)

func (function historicalEvidenceFunc) LoadHistoricalIngestEvidence(ctx context.Context, unit historicalingest.WorkUnit) (HistoricalIngestWorkPayload, error) {
	return function(ctx, unit)
}

func newHistoricalIngestServer(t *testing.T, evidence string) (*httptest.Server, *historicalingest.IngestManager, historicalingest.WorkUnit) {
	t.Helper()
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "personal.db"), store.StoreKindPersonal, "store_personal_historical_server")
	if err != nil {
		t.Fatal(err)
	}
	binding := strings.Repeat("a", 64)
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_historical_server"); err != nil {
		t.Fatal(err)
	}
	manager, err := historicalingest.NewIngestManager(historicalingest.IngestManagerConfig{
		RootDir: filepath.Join(filepath.Dir(vault.DBPath()), "historical-test"),
		Key:     []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	evidenceDigest := sha256.Sum256([]byte(evidence))
	snapshotDigest := strings.Repeat("b", 64)
	unit := historicalingest.WorkUnit{
		ID: "unit_server", RootID: "root_server", SnapshotDigest: snapshotDigest,
		EvidenceDigest: hex.EncodeToString(evidenceDigest[:]), EvidenceBytes: int64(len(evidence)), SourceAliases: []string{"source_0123456789abcdef"}, Ordinal: 0,
	}
	snapshot := historicalingest.SourceSnapshot{
		Digest: snapshotDigest, Cutoff: time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC), RootCount: 1,
		ParserVersion: historicalingest.CodexParserVersionV1,
		Files: []historicalingest.SourceFilePrefix{{
			Alias: unit.SourceAliases[0], CapturedBytes: int64(len(evidence)), PrefixDigest: strings.Repeat("c", 64),
			RootID: unit.RootID, ParserVersion: historicalingest.CodexParserVersionV1, RecordCount: 1, IncludedCount: 1,
		}},
	}
	contract := historicalingest.RunnerContract{
		Digest: strings.Repeat("d", 64), SchemaDigest: historicalingest.SchemaDigest(), ModelID: "gpt-5.6-luna",
		ModelEffort: "low", ParserVersion: historicalingest.CodexParserVersionV1, PromptVersion: "historical_prompt_v1",
	}
	if _, err := manager.StartJob("job_0123456789abcdef", snapshot, []historicalingest.WorkUnit{unit}, contract); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		IPCSecret: "secret", Store: vault, HistoricalIngestManager: manager,
		HistoricalIngestEvidence: historicalEvidenceFunc(func(context.Context, historicalingest.WorkUnit) (HistoricalIngestWorkPayload, error) {
			return HistoricalIngestWorkPayload{TrustedPrompt: "extract the bounded synthetic evidence", Evidence: evidence}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close(); _ = vault.Close() })
	return ts, manager, unit
}

func TestHistoricalIngestWorkerRoutesLeaseSubmitAndFinalize(t *testing.T) {
	ts, _, unit := newHistoricalIngestServer(t, "bounded synthetic evidence")
	response := pulseJSON(t, ts, http.MethodPost, "/memory/historical-ingest/jobs/job_0123456789abcdef/lease", map[string]any{})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("lease status=%d", response.StatusCode)
	}
	var lease historicalWorkerLease
	if err := json.NewDecoder(response.Body).Decode(&lease); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if lease.Unit.ID != unit.ID || lease.Evidence != "bounded synthetic evidence" || lease.RunnerContractDigest != strings.Repeat("d", 64) {
		t.Fatalf("lease=%+v", lease)
	}
	result := historicalingest.WorkUnitResult{
		SchemaVersion: historicalingest.SchemaVersionV1, WorkUnitID: unit.ID, EvidenceDigest: unit.EvidenceDigest,
		Items: []historicalingest.MaterialItem{{
			CandidateID: "candidate_0123456789abcdef", Kind: historicalingest.MaterialKindDecision, Confidence: .9,
			Privacy: historicalingest.PrivacyPrivate, EpistemicStatus: historicalingest.EpistemicExplicit, Derivation: historicalingest.DerivationDirect,
			ValidTime: timeRangeAt("2026-07-22T01:00:00Z"), Scope: historicalingest.Scope{Kind: historicalingest.ScopeGlobal},
			SourceRefs: []historicalingest.SourceRef{{Alias: unit.SourceAliases[0], PrefixDigest: strings.Repeat("c", 64), RecordLocator: "record:1"}},
			Payload:    historicalingest.MaterialPayload{Summary: "A synthetic decision."},
		}},
	}
	submitted := pulseJSON(t, ts, http.MethodPost, "/memory/historical-ingest/jobs/job_0123456789abcdef/submit", map[string]any{
		"unit_id": unit.ID, "lease_token": lease.LeaseToken, "runner_contract_digest": strings.Repeat("d", 64),
		"result": result, "usage": historicalingest.TokenUsage{InputTokens: 100, CachedInputTokens: 10, OutputTokens: 20, ReasoningTokens: 2},
	})
	if submitted.StatusCode != http.StatusOK {
		t.Fatalf("submit status=%d", submitted.StatusCode)
	}
	_ = submitted.Body.Close()
	done := pulseJSON(t, ts, http.MethodPost, "/memory/historical-ingest/jobs/job_0123456789abcdef/lease", map[string]any{})
	if done.StatusCode != http.StatusOK {
		t.Fatalf("finalize status=%d", done.StatusCode)
	}
	var terminal struct {
		Done   bool                       `json:"done"`
		Status historicalingest.JobStatus `json:"status"`
	}
	if err := json.NewDecoder(done.Body).Decode(&terminal); err != nil {
		t.Fatal(err)
	}
	_ = done.Body.Close()
	if !terminal.Done || terminal.Status.State != historicalingest.JobManifestReady || terminal.Status.AcceptedUnits != 1 {
		t.Fatalf("terminal=%+v", terminal)
	}
}

func TestHistoricalIngestStartFreezesCodexCohortAndWaitsForHomeEgressConsent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeHistoricalCodexFixture(t, root, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "2026-07-21T10:00:00Z", "Keep this decision.")
	writeHistoricalCodexFixture(t, root, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "2026-07-22T10:00:00Z", "Importer must be excluded.")
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "personal.db"), store.StoreKindPersonal, "store_personal_historical_start")
	if err != nil {
		t.Fatal(err)
	}
	binding := strings.Repeat("a", 64)
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_historical_start"); err != nil {
		t.Fatal(err)
	}
	manager, err := historicalingest.NewIngestManager(historicalingest.IngestManagerConfig{RootDir: filepath.Join(t.TempDir(), "manager"), Key: []byte(strings.Repeat("m", 32))})
	if err != nil {
		t.Fatal(err)
	}
	source, err := historicalingest.NewCodexSourceStore(historicalingest.CodexSourceStoreConfig{RootDir: filepath.Join(t.TempDir(), "source-index"), Key: []byte(strings.Repeat("s", 32)), SourceRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault, HistoricalIngestManager: manager, HistoricalCodexSource: source})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close(); _ = vault.Close() })
	response := pulseJSON(t, ts, http.MethodPost, "/memory/historical-ingest/jobs", map[string]any{"source": "codex", "root_limit": 1, "excluded_session_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("start status=%d", response.StatusCode)
	}
	var status historicalingest.JobStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if status.State != historicalingest.JobAwaitingEgress || status.SourceRootCount != 1 || status.EgressAuthorized || status.RunnerContract != codexSubscriptionRunnerContractDigestV1 {
		t.Fatalf("start status=%+v", status)
	}
	blocked := pulseJSON(t, ts, http.MethodPost, "/memory/historical-ingest/jobs/"+status.JobID+"/lease", map[string]any{})
	if blocked.StatusCode != http.StatusConflict {
		t.Fatalf("lease before consent status=%d", blocked.StatusCode)
	}
	_ = blocked.Body.Close()
	if _, err := manager.AuthorizeEgress(status.JobID, status.SnapshotDigest, status.RunnerContract); err != nil {
		t.Fatal(err)
	}
	probe, err := manager.LeaseNext(status.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.Load(probe.Unit); err != nil {
		t.Fatalf("source load: %v", err)
	}
	if _, err := manager.FailLease(status.JobID, probe.Unit.ID, probe.Token, "test_probe"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResumeJob(status.JobID); err != nil {
		t.Fatal(err)
	}
	leaseResponse := pulseJSON(t, ts, http.MethodPost, "/memory/historical-ingest/jobs/"+status.JobID+"/lease", map[string]any{})
	if leaseResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(leaseResponse.Body)
		t.Fatalf("lease after consent status=%d body=%s", leaseResponse.StatusCode, body)
	}
	var lease historicalWorkerLease
	if err := json.NewDecoder(leaseResponse.Body).Decode(&lease); err != nil {
		t.Fatal(err)
	}
	_ = leaseResponse.Body.Close()
	if lease.Unit.RootID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" || !strings.Contains(lease.Evidence, "Keep this decision") || strings.Contains(lease.Evidence, root) {
		t.Fatalf("unsafe or wrong lease: %+v", lease)
	}
}

func TestHistoricalIngestQuotaAndResumeRoutesPreserveCheckpoint(t *testing.T) {
	ts, _, unit := newHistoricalIngestServer(t, "bounded quota evidence")
	leaseResponse := pulseJSON(t, ts, http.MethodPost, "/memory/historical-ingest/jobs/job_0123456789abcdef/lease", map[string]any{})
	var lease historicalWorkerLease
	if err := json.NewDecoder(leaseResponse.Body).Decode(&lease); err != nil {
		t.Fatal(err)
	}
	_ = leaseResponse.Body.Close()
	paused := pulseJSON(t, ts, http.MethodPost, "/memory/historical-ingest/jobs/job_0123456789abcdef/quota", map[string]any{"unit_id": unit.ID, "lease_token": lease.LeaseToken})
	if paused.StatusCode != http.StatusOK {
		t.Fatalf("quota status=%d", paused.StatusCode)
	}
	_ = paused.Body.Close()
	resumed := pulseJSON(t, ts, http.MethodPost, "/memory/historical-ingest/jobs/job_0123456789abcdef/resume", map[string]any{})
	if resumed.StatusCode != http.StatusOK {
		t.Fatalf("resume status=%d", resumed.StatusCode)
	}
	var status historicalingest.JobStatus
	if err := json.NewDecoder(resumed.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	_ = resumed.Body.Close()
	if status.State != historicalingest.JobExtracting || status.AcceptedUnits != 0 || status.PendingUnits != 1 {
		t.Fatalf("status=%+v", status)
	}
}

func timeRangeAt(value string) historicalingest.ValidTime {
	parsed, _ := time.Parse(time.RFC3339, value)
	return historicalingest.ValidTime{From: parsed}
}
