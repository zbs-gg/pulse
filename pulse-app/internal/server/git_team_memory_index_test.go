package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

type gitTeamMemoryServerEmbedder struct{}

func (gitTeamMemoryServerEmbedder) Model() string { return "git-team-server-test" }
func (gitTeamMemoryServerEmbedder) Embed(_ context.Context, texts []string, _ embed.InputType) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for index := range result {
		result[index] = []float32{1, 0, 0, 0}
	}
	return result, nil
}

func canonicalServerGitMemory(t *testing.T, value any) string {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(body) + "\n"
}

func serverGitMemoryIndexRequest(t *testing.T, binding, repository string) store.GitTeamMemoryIndexRequest {
	t.Helper()
	projectID := "project_0123456789abcdef0123456789abcdef"
	memoryID := "shared_memory_" + strings.Repeat("d", 32)
	commit := strings.Repeat("7", 40)
	refs := []store.GitTeamMemorySourceReference{{
		SourceID: "source_project_notes", VersionDigest: strings.Repeat("b", 64),
	}}
	warnings := []store.GitTeamMemoryWarning{}
	candidateBytes, err := json.Marshal(struct {
		Kind             string                               `json:"kind"`
		Statement        string                               `json:"statement"`
		Audience         string                               `json:"audience"`
		Confidence       float64                              `json:"confidence"`
		SourceReferences []store.GitTeamMemorySourceReference `json:"source_references"`
		Warnings         []store.GitTeamMemoryWarning         `json:"warnings"`
	}{
		Kind: "decision", Statement: "Use the approved server brief before launch.",
		Audience: "project", Confidence: 0.9, SourceReferences: refs, Warnings: warnings,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidateDigest := sha256.Sum256(candidateBytes)
	object := canonicalServerGitMemory(t, struct {
		Schema            string                               `json:"schema"`
		MemoryID          string                               `json:"memory_id"`
		Version           int                                  `json:"version"`
		Status            string                               `json:"status"`
		Kind              string                               `json:"kind"`
		Content           string                               `json:"content"`
		Confidence        float64                              `json:"confidence"`
		CandidateDigest   string                               `json:"candidate_digest"`
		ApproverLabel     string                               `json:"approver_label"`
		ApprovedAt        string                               `json:"approved_at"`
		ApprovalAuthority string                               `json:"approval_authority"`
		SourceReferences  []store.GitTeamMemorySourceReference `json:"source_references"`
		Warnings          []store.GitTeamMemoryWarning         `json:"warnings"`
	}{
		Schema: "pulse.git_team_memory.object.v1", MemoryID: memoryID, Version: 1,
		Status: "active", Kind: "decision", Content: "Use the approved server brief before launch.",
		Confidence: 0.9, CandidateDigest: hex.EncodeToString(candidateDigest[:]), ApproverLabel: "Nikita",
		ApprovedAt: "2026-07-16T12:00:00Z", ApprovalAuthority: strings.Repeat("c", 64),
		SourceReferences: refs, Warnings: warnings,
	})
	objectPath := "pulse-memory/memories/" + memoryID + ".json"
	objectSHA := sha256.Sum256([]byte(object))
	manifest := canonicalServerGitMemory(t, struct {
		Schema            string `json:"schema"`
		PublicationID     string `json:"publication_id"`
		BatchID           string `json:"batch_id"`
		ProjectID         string `json:"project_id"`
		ProjectPath       string `json:"project_path"`
		ApprovalAuthority string `json:"approval_authority"`
		ApprovedAt        string `json:"approved_at"`
		Objects           []struct {
			MemoryID string `json:"memory_id"`
			Path     string `json:"path"`
			SHA256   string `json:"sha256"`
		} `json:"objects"`
	}{
		Schema: "pulse.git_team_memory.publication.v1", PublicationID: "shared_publication_server",
		BatchID: "review_batch_server", ProjectID: projectID, ProjectPath: "pulse-memory/project.json",
		ApprovalAuthority: strings.Repeat("c", 64), ApprovedAt: "2026-07-16T12:00:00Z",
		Objects: []struct {
			MemoryID string `json:"memory_id"`
			Path     string `json:"path"`
			SHA256   string `json:"sha256"`
		}{{MemoryID: memoryID, Path: objectPath, SHA256: hex.EncodeToString(objectSHA[:])}},
	})
	project := canonicalServerGitMemory(t, struct {
		Schema    string `json:"schema"`
		ProjectID string `json:"project_id"`
	}{Schema: "pulse.git_team_memory.project.v1", ProjectID: projectID})
	file := func(path, content string) store.GitTeamMemoryIndexFile {
		digest := sha256.Sum256([]byte(content))
		return store.GitTeamMemoryIndexFile{
			Path: path, Content: content, SHA256: hex.EncodeToString(digest[:]), CommitHash: commit,
		}
	}
	files := []store.GitTeamMemoryIndexFile{
		file("pulse-memory/project.json", project), file(objectPath, object),
		file("pulse-memory/publications/review_batch_server.json", manifest),
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return store.GitTeamMemoryIndexRequest{
		Schema: store.GitTeamMemoryIndexSchema, PortableProjectID: projectID,
		RepositoryID: repository, BindingDigest: binding, HeadCommit: commit, Files: files,
	}
}

func TestGitTeamMemoryIndexRouteIndexesAndRetrievesCommittedProjectMemory(t *testing.T) {
	binding := strings.Repeat("a", 64)
	repository := "repository_server_index"
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "desk.db"), store.StoreKindDesk, "store_server_index")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	if err := vault.ConfigureProductRuntimeAuthority(binding, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, repository); err != nil {
		t.Fatal(err)
	}
	engine := retrieve.New(retrieve.Config{Store: vault, Embedder: gitTeamMemoryServerEmbedder{}})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{IPCSecret: "secret", Store: vault, Retrieval: engine})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(serverGitMemoryIndexRequest(t, binding, repository))
	req := httptest.NewRequest(http.MethodPost, "/project/shared-memory/index", bytes.NewReader(body))
	req.Header.Set("X-Pulse-Key", "secret")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("index status=%d body=%s", rec.Code, rec.Body.String())
	}
	var receipt store.GitTeamMemoryIndexReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil || receipt.State != "indexed" ||
		receipt.ActiveCount != 1 || receipt.IndexedCount != 1 {
		t.Fatalf("index receipt=%#v err=%v", receipt, err)
	}
	retrieveBody := bytes.NewBufferString(`{"query":"approved server brief launch","mode":"empathic","top_k":3}`)
	req = httptest.NewRequest(http.MethodPost, "/retrieve", retrieveBody)
	req.Header.Set("X-Pulse-Key", "secret")
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("retrieve status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result struct {
		EventIDs      []int64                                  `json:"event_ids"`
		ProjectMemory map[string]store.GitTeamMemoryProvenance `json:"project_memory"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || len(result.EventIDs) != 1 ||
		len(result.ProjectMemory) != 1 {
		t.Fatalf("retrieve=%s err=%v", rec.Body.String(), err)
	}
}
