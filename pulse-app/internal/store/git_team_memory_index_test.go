package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

func gitTeamIndexFile(t *testing.T, path, content, commit string) GitTeamMemoryIndexFile {
	t.Helper()
	digest := sha256.Sum256([]byte(content))
	return GitTeamMemoryIndexFile{
		Path: path, Content: content, SHA256: hex.EncodeToString(digest[:]), CommitHash: commit,
	}
}

func gitTeamIndexFixture(
	t *testing.T,
	content, status string,
	version int,
	commit, batchID string,
) GitTeamMemoryIndexRequest {
	t.Helper()
	sourceRefs := []GitTeamMemorySourceReference{{
		SourceID: "source_project_notes", VersionDigest: strings.Repeat("b", 64),
	}}
	prepared, err := prepareGitTeamMemoryCandidate(GitTeamMemoryCandidateInput{
		Kind: "decision", Statement: content, Audience: "project", Confidence: 0.91,
		SourceReferences: sourceRefs, AdvisoryWarnings: []GitTeamMemoryWarning{},
	}, sourceRefs[0].SourceID, sourceRefs[0].VersionDigest)
	if err != nil {
		t.Fatal(err)
	}
	memoryID := "shared_memory_" + strings.Repeat("d", 32)
	approvedAt := time.Date(2026, 7, 16, 12, version, 0, 0, time.UTC).Format(time.RFC3339Nano)
	objectContent, err := canonicalGitTeamMemoryJSON(gitTeamMemoryObjectDocument{
		Schema: "pulse.git_team_memory.object.v1", MemoryID: memoryID, Version: version,
		Status: status, Kind: "decision", Content: content, Confidence: 0.91,
		CandidateDigest: prepared.digest, ApproverLabel: "Nikita", ApprovedAt: approvedAt,
		ApprovalAuthority: strings.Repeat("c", 64), SourceReferences: sourceRefs, Warnings: []GitTeamMemoryWarning{},
	})
	if err != nil {
		t.Fatal(err)
	}
	objectPath := "pulse-memory/memories/" + memoryID + ".json"
	objectFile := gitTeamIndexFile(t, objectPath, objectContent, commit)
	manifestContent, err := canonicalGitTeamMemoryJSON(gitTeamMemoryPublicationDocument{
		Schema: "pulse.git_team_memory.publication.v1", PublicationID: "shared_publication_" + batchID,
		BatchID: batchID, ProjectID: testGitMemoryProject, ProjectPath: "pulse-memory/project.json",
		ApprovalAuthority: strings.Repeat("c", 64), ApprovedAt: approvedAt,
		Objects: []gitTeamMemoryManifestObject{{MemoryID: memoryID, Path: objectPath, SHA256: objectFile.SHA256}},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectContent, err := canonicalGitTeamMemoryJSON(gitTeamMemoryProjectDocument{
		Schema: "pulse.git_team_memory.project.v1", ProjectID: testGitMemoryProject,
	})
	if err != nil {
		t.Fatal(err)
	}
	files := []GitTeamMemoryIndexFile{
		gitTeamIndexFile(t, "pulse-memory/project.json", projectContent, commit),
		objectFile,
		gitTeamIndexFile(t, "pulse-memory/publications/"+batchID+".json", manifestContent, commit),
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return GitTeamMemoryIndexRequest{
		Schema: GitTeamMemoryIndexSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		HeadCommit: commit, Files: files,
	}
}

func TestGitTeamMemoryIndexReconcilesApprovedObjectsIdempotently(t *testing.T) {
	vault, _ := openGitMemoryDeskStore(t)
	now := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
	req := gitTeamIndexFixture(t, "Use the approved project brief for every launch.", "active", 1,
		strings.Repeat("a", 40), "review_batch_index_v1")
	receipt, docs, err := vault.ReconcileGitTeamMemoryIndex(req, now)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "reconciled" || receipt.ActiveCount != 1 || receipt.ChangedCount != 1 || len(docs) != 1 {
		t.Fatalf("first index receipt=%#v docs=%#v", receipt, docs)
	}
	provenance, err := vault.GitTeamMemoryProvenanceForEvents([]int64{docs[0].EventID})
	if err != nil || provenance[docs[0].EventID].Visibility != "project" ||
		provenance[docs[0].EventID].ApproverLabel != "Nikita" ||
		provenance[docs[0].EventID].ObjectCommit != strings.Repeat("a", 40) {
		t.Fatalf("provenance=%#v err=%v", provenance, err)
	}
	replayed, replayDocs, err := vault.ReconcileGitTeamMemoryIndex(req, now.Add(time.Minute))
	if err != nil || replayed.ChangedCount != 0 || replayed.ActiveCount != 1 || len(replayDocs) != 0 {
		t.Fatalf("replayed receipt=%#v docs=%#v err=%v", replayed, replayDocs, err)
	}
	var personalCapsules int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&personalCapsules); err != nil || personalCapsules != 0 {
		t.Fatalf("personal capsules=%d err=%v", personalCapsules, err)
	}
	if err := vault.WipeProductMemory(); err != nil {
		t.Fatalf("wipe indexed product memory: %v", err)
	}
	var sharedRows, sharedEvents int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM git_memory_shared_projection`).Scan(&sharedRows); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM events WHERE scorer_version='host-extracted'`).Scan(&sharedEvents); err != nil {
		t.Fatal(err)
	}
	if sharedRows != 0 || sharedEvents != 0 {
		t.Fatalf("wipe left shared rows=%d events=%d", sharedRows, sharedEvents)
	}
}

func TestGitTeamMemoryIndexCorrectionPreservesHistoryAndDeletesStaleEvent(t *testing.T) {
	vault, _ := openGitMemoryDeskStore(t)
	now := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
	first := gitTeamIndexFixture(t, "Use the old launch brief.", "active", 1,
		strings.Repeat("a", 40), "review_batch_index_old")
	_, _, err := vault.ReconcileGitTeamMemoryIndex(first, now)
	if err != nil {
		t.Fatal(err)
	}
	second := gitTeamIndexFixture(t, "Use the corrected launch brief.", "active", 2,
		strings.Repeat("b", 40), "review_batch_index_new")
	receipt, secondDocs, err := vault.ReconcileGitTeamMemoryIndex(second, now.Add(time.Minute))
	if err != nil || receipt.ChangedCount != 1 || len(secondDocs) != 1 {
		t.Fatalf("correction receipt=%#v docs=%#v err=%v", receipt, secondDocs, err)
	}
	var staleEvents, versions int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM events WHERE description='Use the old launch brief.'`).Scan(&staleEvents); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM git_memory_shared_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if staleEvents != 0 || versions != 2 {
		t.Fatalf("stale events=%d versions=%d", staleEvents, versions)
	}
	// A committed deletion removes the object file but keeps its historical
	// manifest. Reconciliation must remove the current event without deleting
	// either approved version row.
	deleted := second
	deleted.HeadCommit = strings.Repeat("e", 40)
	filtered := deleted.Files[:0]
	for _, file := range deleted.Files {
		if !strings.Contains(file.Path, "/memories/") {
			file.CommitHash = deleted.HeadCommit
			filtered = append(filtered, file)
		}
	}
	deleted.Files = filtered
	removed, removedDocs, err := vault.ReconcileGitTeamMemoryIndex(deleted, now.Add(2*time.Minute))
	if err != nil || removed.RemovedCount != 1 || removed.ActiveCount != 0 || len(removedDocs) != 0 {
		t.Fatalf("removed receipt=%#v docs=%#v err=%v", removed, removedDocs, err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM events WHERE id=?`, secondDocs[0].EventID).Scan(&staleEvents); err != nil || staleEvents != 0 {
		t.Fatalf("corrected event survived removal count=%d err=%v", staleEvents, err)
	}
}

func TestGitTeamMemoryIndexRejectsNonCanonicalOrUnapprovedObjectAtomically(t *testing.T) {
	vault, _ := openGitMemoryDeskStore(t)
	req := gitTeamIndexFixture(t, "Use only approved launch evidence.", "active", 1,
		strings.Repeat("a", 40), "review_batch_index_invalid")
	for index := range req.Files {
		if strings.Contains(req.Files[index].Path, "/memories/") {
			req.Files[index].Content = strings.Replace(req.Files[index].Content, "  \"schema\"", " \"schema\"", 1)
			digest := sha256.Sum256([]byte(req.Files[index].Content))
			req.Files[index].SHA256 = hex.EncodeToString(digest[:])
		}
	}
	if _, _, err := vault.ReconcileGitTeamMemoryIndex(req, time.Now()); !errors.Is(err, ErrGitTeamMemoryIndexConflict) {
		t.Fatalf("non-canonical index error=%v", err)
	}
	var projections, events int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM git_memory_shared_projection`).Scan(&projections); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if projections != 0 || events != 0 {
		t.Fatalf("invalid pack persisted projections=%d events=%d", projections, events)
	}
}
