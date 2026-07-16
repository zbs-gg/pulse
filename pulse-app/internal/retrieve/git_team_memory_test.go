package retrieve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

const gitMemoryProjectID = "project_0123456789abcdef0123456789abcdef"

func openGitMemoryRetrieveStore(t *testing.T, name, binding, repository string) *store.Store {
	t.Helper()
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), name+".db"), store.StoreKindDesk, "store_"+name)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureProductRuntimeAuthority(binding, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, repository); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	return vault
}

func approvedGitMemoryFiles(t *testing.T) []store.GitTeamMemoryPublicationFile {
	t.Helper()
	binding := strings.Repeat("a", 64)
	repository := "repository_publisher_checkout"
	vault := openGitMemoryRetrieveStore(t, "publisher", binding, repository)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	source, err := vault.RegisterProjectSource(store.ProjectSourceRegistration{
		Schema: "pulse.project_source.register.v1", PortableProjectID: gitMemoryProjectID,
		RepositoryID: repository, BindingDigest: binding, SourceKind: "repository_text",
		Locator: "notes/team.md", VersionDigest: strings.Repeat("b", 64), ByteCount: 120,
		ObservedAt: now.Format(time.RFC3339Nano),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw := false
	staged, err := vault.StageGitTeamMemoryReview(store.GitTeamMemoryStageRequest{
		Schema: store.GitTeamMemoryStageSchema, PortableProjectID: gitMemoryProjectID,
		RepositoryID: repository, BindingDigest: binding, Host: "codex",
		TaskID: "task_second_checkout", IdempotencyKey: "stage_second_checkout",
		SourceID: source.SourceID, SourceVersionDigest: source.VersionDigest,
		Candidates: []store.GitTeamMemoryCandidateInput{{
			Kind: "decision", Statement: "Use the approved project brief before every launch.",
			Audience: "project", Confidence: 0.94,
			SourceReferences: []store.GitTeamMemorySourceReference{{
				SourceID: source.SourceID, VersionDigest: source.VersionDigest,
			}},
		}},
		RawInputIncluded: &raw,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	label := "Nikita"
	labelHash := sha256.Sum256([]byte("pulse-git-memory-approver-label-v1\x00" + label))
	_, err = vault.PresentGitTeamMemoryCards(store.GitTeamMemoryPresentationRequest{
		Schema: store.GitTeamMemoryPresentationSchema, PortableProjectID: gitMemoryProjectID,
		RepositoryID: repository, BindingDigest: binding, BatchID: staged.BatchID,
		BatchGeneration: staged.Generation, Host: "codex", TaskID: "task_second_checkout",
		SessionRef: "session:" + strings.Repeat("1", 64), TurnRef: "turn:" + strings.Repeat("2", 64),
		SourceEventDigest: strings.Repeat("3", 64), CardBlockDigest: strings.Repeat("4", 64),
		CandidateDigests:    []string{staged.Candidates[0].ContentDigest},
		ApproverLabelDigest: hex.EncodeToString(labelHash[:]),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := vault.ApproveExactGitTeamMemoryOK(store.GitTeamMemoryExactOKRequest{
		Schema: store.GitTeamMemoryExactOKSchema, PortableProjectID: gitMemoryProjectID,
		RepositoryID: repository, BindingDigest: binding, Host: "codex",
		SessionRef: "session:" + strings.Repeat("1", 64), PromptEventDigest: strings.Repeat("5", 64),
	}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	publication, err := vault.BeginGitTeamMemoryPublication(store.GitTeamMemoryPublicationStartRequest{
		Schema: store.GitTeamMemoryPublicationStartSchema, PortableProjectID: gitMemoryProjectID,
		RepositoryID: repository, BindingDigest: binding, ApprovalLeaseID: lease.LeaseID,
		ApproverLabel: label, ExpectedParent: strings.Repeat("6", 40),
	}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return publication.Files
}

func TestGitTeamMemorySecondCheckoutUsesExistingStateAwareEngineWithProvenance(t *testing.T) {
	files := approvedGitMemoryFiles(t)
	binding := strings.Repeat("f", 64)
	repository := "repository_second_checkout"
	teammate := openGitMemoryRetrieveStore(t, "teammate", binding, repository)
	commit := strings.Repeat("7", 40)
	indexFiles := make([]store.GitTeamMemoryIndexFile, len(files))
	for index, file := range files {
		indexFiles[index] = store.GitTeamMemoryIndexFile{
			Path: file.Path, Content: file.Content, SHA256: file.SHA256, CommitHash: commit,
		}
	}
	sort.Slice(indexFiles, func(i, j int) bool { return indexFiles[i].Path < indexFiles[j].Path })
	receipt, docs, err := teammate.ReconcileGitTeamMemoryIndex(store.GitTeamMemoryIndexRequest{
		Schema: store.GitTeamMemoryIndexSchema, PortableProjectID: gitMemoryProjectID,
		RepositoryID: repository, BindingDigest: binding, HeadCommit: commit, Files: indexFiles,
	}, time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC))
	if err != nil || receipt.ActiveCount != 1 || len(docs) != 1 {
		t.Fatalf("index receipt=%#v docs=%#v err=%v", receipt, docs, err)
	}
	embedder := &fakeEmbedder{dim: 32}
	engine := New(Config{Store: teammate, Embedder: embedder})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if indexed, err := engine.EmbedAndPersistEvents(context.Background(), []IndexEventDoc{{
		EventID: docs[0].EventID, Text: docs[0].Text,
	}}); err != nil || indexed != 1 {
		t.Fatalf("indexed=%d err=%v", indexed, err)
	}
	if err := engine.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Retrieve(context.Background(), RetrieveRequest{
		Query: "approved project brief launch", Mode: ModeEmpathic, TopK: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.EventIDs) != 1 || result.EventIDs[0] != docs[0].EventID {
		t.Fatalf("retrieved events=%v", result.EventIDs)
	}
	breakdown, ok := result.ScoreBreakdowns[docs[0].EventID]
	if !ok || breakdown.Cosine == 0 || breakdown.Recency == 0 {
		t.Fatalf("ranking reasons=%#v", result.ScoreBreakdowns)
	}
	provenance, ok := result.ProjectMemory[docs[0].EventID]
	if !ok || provenance.Visibility != "project" || provenance.PortableProjectID != gitMemoryProjectID ||
		provenance.ApproverLabel != "Nikita" || provenance.ObjectCommit != commit ||
		len(provenance.SourceReferences) != 1 {
		t.Fatalf("project provenance=%#v", result.ProjectMemory)
	}
	var personalItems int
	if err := teammate.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&personalItems); err != nil || personalItems != 0 {
		t.Fatalf("teammate personal items=%d err=%v", personalItems, err)
	}
}
