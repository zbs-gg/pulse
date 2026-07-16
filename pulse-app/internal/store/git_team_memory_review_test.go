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

func TestGitTeamMemoryTrustedPresentationAndExactOKIssueSingleUseLease(t *testing.T) {
	vault, _ := openGitMemoryDeskStore(t)
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	source, err := vault.RegisterProjectSource(
		gitMemorySourceRegistration("notes/team.md", strings.Repeat("b", 64), 120), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	stage := stageSharedReviewRequest(source,
		safeSharedReviewCandidate("Use the approved project brief before drafting a launch page."))
	stage.TaskID = "session_git_memory_approval"
	staged, err := vault.StageGitTeamMemoryReview(stage, now)
	if err != nil {
		t.Fatal(err)
	}
	candidate := staged.Candidates[0]
	presented, err := vault.PresentGitTeamMemoryCards(GitTeamMemoryPresentationRequest{
		Schema: GitTeamMemoryPresentationSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		BatchID: staged.BatchID, BatchGeneration: staged.Generation, Host: "codex",
		TaskID: stage.TaskID, SessionRef: "session:" + strings.Repeat("1", 64),
		TurnRef: "turn:" + strings.Repeat("2", 64), SourceEventDigest: strings.Repeat("3", 64),
		CardBlockDigest: strings.Repeat("4", 64), CandidateDigests: []string{candidate.ContentDigest},
	}, now)
	if err != nil || presented.State != "presented" || presented.BatchID != staged.BatchID {
		t.Fatalf("presentation = %#v, err=%v", presented, err)
	}
	lease, err := vault.ApproveExactGitTeamMemoryOK(GitTeamMemoryExactOKRequest{
		Schema: GitTeamMemoryExactOKSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		Host: "codex", SessionRef: "session:" + strings.Repeat("1", 64),
		PromptEventDigest: strings.Repeat("5", 64),
	}, now.Add(time.Second))
	if err != nil || lease.State != "issued" || lease.BatchID != staged.BatchID ||
		len(lease.CandidateDigests) != 1 || lease.CandidateDigests[0] != candidate.ContentDigest {
		t.Fatalf("lease = %#v, err=%v", lease, err)
	}
	inspected, err := vault.InspectGitTeamMemoryReview(GitTeamMemoryInspectRequest{
		Schema: GitTeamMemoryInspectSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding, BatchID: staged.BatchID,
	})
	if err != nil || inspected.State != "approved" || inspected.Candidates[0].State != "approved" {
		t.Fatalf("approved batch = %#v, err=%v", inspected, err)
	}
	consumed, err := vault.ConsumeGitTeamMemoryApprovalLease(lease.LeaseID, now.Add(2*time.Second))
	if err != nil || consumed.State != "consumed" {
		t.Fatalf("consume = %#v, err=%v", consumed, err)
	}
	if _, err := vault.ConsumeGitTeamMemoryApprovalLease(lease.LeaseID, now.Add(3*time.Second)); !errors.Is(err, ErrGitTeamMemoryApprovalUnavailable) {
		t.Fatalf("replayed lease error = %v", err)
	}
}

func TestGitTeamMemoryEditInvalidatesTrustedPresentation(t *testing.T) {
	vault, _ := openGitMemoryDeskStore(t)
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	source, err := vault.RegisterProjectSource(
		gitMemorySourceRegistration("notes/team.md", strings.Repeat("b", 64), 120), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	stage := stageSharedReviewRequest(source,
		safeSharedReviewCandidate("Use the approved project brief before drafting a launch page."))
	stage.TaskID = "session_git_memory_edit"
	staged, err := vault.StageGitTeamMemoryReview(stage, now)
	if err != nil {
		t.Fatal(err)
	}
	candidate := staged.Candidates[0]
	_, err = vault.PresentGitTeamMemoryCards(GitTeamMemoryPresentationRequest{
		Schema: GitTeamMemoryPresentationSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		BatchID: staged.BatchID, BatchGeneration: staged.Generation, Host: "codex", TaskID: stage.TaskID,
		SessionRef: "session:" + strings.Repeat("6", 64), TurnRef: "turn:" + strings.Repeat("7", 64),
		SourceEventDigest: strings.Repeat("8", 64), CardBlockDigest: strings.Repeat("9", 64),
		CandidateDigests: []string{candidate.ContentDigest},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	editedInput := safeSharedReviewCandidate("Use the approved project brief and current constraints before drafting.")
	editedInput.SourceReferences[0].SourceID = source.SourceID
	editedInput.SourceReferences[0].VersionDigest = source.VersionDigest
	if _, err := vault.EditGitTeamMemoryCandidate(GitTeamMemoryEditRequest{
		Schema: GitTeamMemoryEditSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		CandidateID: candidate.CandidateID, ExpectedVersion: 1, Candidate: editedInput,
	}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.ApproveExactGitTeamMemoryOK(GitTeamMemoryExactOKRequest{
		Schema: GitTeamMemoryExactOKSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		Host: "codex", SessionRef: "session:" + strings.Repeat("6", 64),
		PromptEventDigest: strings.Repeat("a", 64),
	}, now.Add(2*time.Second)); !errors.Is(err, ErrGitTeamMemoryApprovalUnavailable) {
		t.Fatalf("edited presentation approval error = %v", err)
	}
}

func TestGitTeamMemoryExactOKRejectsWrongTaskExpiredAndAmbiguousPresentations(t *testing.T) {
	vault, _ := openGitMemoryDeskStore(t)
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	source, err := vault.RegisterProjectSource(
		gitMemorySourceRegistration("notes/team.md", strings.Repeat("b", 64), 120), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	present := func(taskID, sessionRef, eventDigest, cardDigest, idempotency string, at time.Time) GitTeamMemoryBatchView {
		t.Helper()
		stage := stageSharedReviewRequest(source,
			safeSharedReviewCandidate("Use the approved project brief before drafting a launch page."))
		stage.TaskID = taskID
		stage.IdempotencyKey = idempotency
		staged, err := vault.StageGitTeamMemoryReview(stage, at)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := vault.PresentGitTeamMemoryCards(GitTeamMemoryPresentationRequest{
			Schema: GitTeamMemoryPresentationSchema, PortableProjectID: testGitMemoryProject,
			RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
			BatchID: staged.BatchID, BatchGeneration: staged.Generation, Host: "codex", TaskID: taskID,
			SessionRef: sessionRef, TurnRef: "turn:" + strings.Repeat("d", 64),
			SourceEventDigest: eventDigest, CardBlockDigest: cardDigest,
			CandidateDigests: []string{staged.Candidates[0].ContentDigest},
		}, at); err != nil {
			t.Fatal(err)
		}
		return staged
	}

	wrongStage := stageSharedReviewRequest(source,
		safeSharedReviewCandidate("Use the approved project brief before drafting a launch page."))
	wrongStage.TaskID = "task_expected"
	wrongStage.IdempotencyKey = "stage_wrong_task"
	wrong, err := vault.StageGitTeamMemoryReview(wrongStage, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.PresentGitTeamMemoryCards(GitTeamMemoryPresentationRequest{
		Schema: GitTeamMemoryPresentationSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		BatchID: wrong.BatchID, BatchGeneration: wrong.Generation, Host: "codex", TaskID: "task_wrong",
		SessionRef: "session:" + strings.Repeat("e", 64), TurnRef: "turn:" + strings.Repeat("f", 64),
		SourceEventDigest: strings.Repeat("1", 64), CardBlockDigest: strings.Repeat("2", 64),
		CandidateDigests: []string{wrong.Candidates[0].ContentDigest},
	}, now); !errors.Is(err, ErrGitTeamMemoryVersionConflict) {
		t.Fatalf("wrong task presentation error = %v", err)
	}

	expiredSession := "session:" + strings.Repeat("3", 64)
	expiredBatch := present("task_expired", expiredSession, strings.Repeat("4", 64), strings.Repeat("5", 64), "stage_expired", now)
	if _, err := vault.ApproveExactGitTeamMemoryOK(GitTeamMemoryExactOKRequest{
		Schema: GitTeamMemoryExactOKSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		Host: "codex", SessionRef: expiredSession, PromptEventDigest: strings.Repeat("6", 64),
	}, now.Add(11*time.Minute)); !errors.Is(err, ErrGitTeamMemoryApprovalUnavailable) {
		t.Fatalf("expired presentation approval error = %v", err)
	}
	represented, err := vault.PresentGitTeamMemoryCards(GitTeamMemoryPresentationRequest{
		Schema: GitTeamMemoryPresentationSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		BatchID: expiredBatch.BatchID, BatchGeneration: expiredBatch.Generation, Host: "codex", TaskID: "task_expired",
		SessionRef: expiredSession, TurnRef: "turn:" + strings.Repeat("e", 64),
		SourceEventDigest: strings.Repeat("f", 64), CardBlockDigest: strings.Repeat("5", 64),
		CandidateDigests: []string{expiredBatch.Candidates[0].ContentDigest},
	}, now.Add(11*time.Minute))
	if err != nil || represented.State != "presented" || represented.PresentationID == "" {
		t.Fatalf("re-present expired cards = %#v, err=%v", represented, err)
	}

	ambiguousSession := "session:" + strings.Repeat("7", 64)
	present("task_ambiguous_one", ambiguousSession, strings.Repeat("8", 64), strings.Repeat("9", 64), "stage_ambiguous_one", now)
	present("task_ambiguous_two", ambiguousSession, strings.Repeat("a", 64), strings.Repeat("c", 64), "stage_ambiguous_two", now)
	if _, err := vault.ApproveExactGitTeamMemoryOK(GitTeamMemoryExactOKRequest{
		Schema: GitTeamMemoryExactOKSchema, PortableProjectID: testGitMemoryProject,
		RepositoryID: testGitMemoryRepository, BindingDigest: testGitMemoryBinding,
		Host: "codex", SessionRef: ambiguousSession, PromptEventDigest: strings.Repeat("d", 64),
	}, now.Add(time.Second)); !errors.Is(err, ErrGitTeamMemoryApprovalAmbiguous) {
		t.Fatalf("ambiguous presentation approval error = %v", err)
	}
}
