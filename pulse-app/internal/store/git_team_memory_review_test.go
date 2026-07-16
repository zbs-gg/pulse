package store

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func safeSharedReviewCandidate(statement string) GitTeamMemoryCandidateInput {
	return GitTeamMemoryCandidateInput{
		Kind: "decision", Statement: statement, Audience: "project", Confidence: 0.91,
		SourceReferences: []GitTeamMemorySourceReference{{
			SourceID: "source_placeholder", VersionDigest: strings.Repeat("b", 64),
		}},
		AdvisoryWarnings: []GitTeamMemoryWarning{{Code: "weak_evidence", Summary: "Confirm this conclusion with the project owner."}},
	}
}

func stageSharedReviewRequest(source ProjectSourceRegistrationResult, candidates ...GitTeamMemoryCandidateInput) GitTeamMemoryStageRequest {
	for index := range candidates {
		candidates[index].SourceReferences[0].SourceID = source.SourceID
		candidates[index].SourceReferences[0].VersionDigest = source.VersionDigest
	}
	raw := false
	return GitTeamMemoryStageRequest{
		Schema: "pulse.git_team_memory.stage.v1", PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		Host: "codex", TaskID: "task_git_memory_test", IdempotencyKey: "stage_git_memory_test",
		SourceID: source.SourceID, SourceVersionDigest: source.VersionDigest,
		Candidates: candidates, RawInputIncluded: &raw,
	}
}

func TestGitTeamMemoryStageIsAllOrNothingForUnsafeMixedBatch(t *testing.T) {
	vault, path := openGitMemoryDeskStore(t)
	source, err := vault.RegisterProjectSource(gitMemorySourceRegistration("notes/team.md", strings.Repeat("b", 64), 120), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	unsafeStatements := []string{
		"Use sk-abcdefghijklmnopqrstuvwxyz123456 for deployments.",
		"Read /Users/private/team.md before answering.",
		"User: keep this entire transcript line.",
	}
	for index, statement := range unsafeStatements {
		unsafe := safeSharedReviewCandidate(statement)
		req := stageSharedReviewRequest(source,
			safeSharedReviewCandidate("Use the approved project brief before drafting a launch page."), unsafe)
		req.IdempotencyKey += string(rune('a' + index))
		if _, err := vault.StageGitTeamMemoryReview(req, time.Now()); !errors.Is(err, ErrGitTeamMemoryUnsafeCandidate) {
			t.Fatalf("unsafe statement %q error = %v", statement, err)
		}
	}
	var batches, candidates int
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM git_memory_review_batches`).Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM git_memory_review_candidates`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if batches != 0 || candidates != 0 {
		t.Fatalf("unsafe mixed requests persisted batches=%d candidates=%d", batches, candidates)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		body, err := os.ReadFile(candidate)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, sentinel := range unsafeStatements {
			if strings.Contains(string(body), sentinel) {
				t.Fatalf("unsafe candidate persisted in %s", candidate)
			}
		}
	}
}

func TestGitTeamMemoryStageEditRejectLifecycleUsesExactVersions(t *testing.T) {
	vault, _ := openGitMemoryDeskStore(t)
	source, err := vault.RegisterProjectSource(gitMemorySourceRegistration("notes/team.md", strings.Repeat("b", 64), 120), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	staged, err := vault.StageGitTeamMemoryReview(stageSharedReviewRequest(source,
		safeSharedReviewCandidate("Use the approved project brief before drafting a launch page.")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if staged.State != "staged" || len(staged.Candidates) != 1 || staged.Candidates[0].Version != 1 || staged.Candidates[0].State != "staged" {
		t.Fatalf("staged = %#v", staged)
	}
	before := staged.Candidates[0]
	editedInput := safeSharedReviewCandidate("Use the approved project brief and current audience constraints before drafting a launch page.")
	editedInput.SourceReferences[0].SourceID = source.SourceID
	editedInput.SourceReferences[0].VersionDigest = source.VersionDigest
	edited, err := vault.EditGitTeamMemoryCandidate(GitTeamMemoryEditRequest{
		Schema: "pulse.git_team_memory.edit.v1", PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		CandidateID: before.CandidateID, ExpectedVersion: 1, Candidate: editedInput,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if edited.Version != 2 || edited.ContentDigest == before.ContentDigest || edited.Statement == before.Statement {
		t.Fatalf("edited = %#v; before=%#v", edited, before)
	}
	if _, err := vault.EditGitTeamMemoryCandidate(GitTeamMemoryEditRequest{
		Schema: "pulse.git_team_memory.edit.v1", PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		CandidateID: before.CandidateID, ExpectedVersion: 1, Candidate: editedInput,
	}, time.Now()); !errors.Is(err, ErrGitTeamMemoryVersionConflict) {
		t.Fatalf("stale edit error = %v", err)
	}
	rejected, err := vault.RejectGitTeamMemoryCandidate(GitTeamMemoryRejectRequest{
		Schema: "pulse.git_team_memory.reject.v1", PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		CandidateID: before.CandidateID, ExpectedVersion: 2, ReasonCode: "user_rejected",
	}, time.Now())
	if err != nil || rejected.State != "rejected" {
		t.Fatalf("reject = %#v, err=%v", rejected, err)
	}
	if _, err := vault.EditGitTeamMemoryCandidate(GitTeamMemoryEditRequest{
		Schema: "pulse.git_team_memory.edit.v1", PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		CandidateID: before.CandidateID, ExpectedVersion: 2, Candidate: editedInput,
	}, time.Now()); !errors.Is(err, ErrGitTeamMemoryTerminal) {
		t.Fatalf("terminal edit error = %v", err)
	}
	inspected, err := vault.InspectGitTeamMemoryReview(GitTeamMemoryInspectRequest{
		Schema: "pulse.git_team_memory.inspect.v1", PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding, BatchID: staged.BatchID,
	})
	if err != nil || len(inspected.Candidates) != 1 || inspected.Candidates[0].State != "rejected" {
		t.Fatalf("inspect = %#v, err=%v", inspected, err)
	}
}

func TestGitTeamMemoryStageRejectsStaleSourceBeforeDurableRows(t *testing.T) {
	vault, _ := openGitMemoryDeskStore(t)
	first, err := vault.RegisterProjectSource(gitMemorySourceRegistration("notes/team.md", strings.Repeat("b", 64), 120), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.RegisterProjectSource(gitMemorySourceRegistration("notes/team.md", strings.Repeat("c", 64), 140), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.StageGitTeamMemoryReview(stageSharedReviewRequest(first,
		safeSharedReviewCandidate("This is bound to a stale source version.")), time.Now()); !errors.Is(err, ErrGitTeamMemoryStaleSource) {
		t.Fatalf("stale source error = %v", err)
	}
	var count int
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM git_memory_review_candidates`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale source persisted %d candidates", count)
	}
}
