package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

func serverStageBody(source store.ProjectSourceRegistrationResult, statement string) map[string]any {
	return map[string]any{
		"schema": "pulse.git_team_memory.stage.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding,
		"host": "codex", "task_id": "task_server_git_memory", "idempotency_key": "stage_server_git_memory",
		"source_id": source.SourceID, "source_version_digest": source.VersionDigest,
		"raw_input_included": false,
		"candidates": []map[string]any{{
			"kind": "decision", "statement": statement, "audience": "project", "confidence": 0.91,
			"source_references": []map[string]any{{"source_id": source.SourceID, "version_digest": source.VersionDigest}},
			"advisory_warnings": []map[string]any{{"code": "weak_evidence", "summary": "Confirm this conclusion with the project owner."}},
		}},
	}
}

func TestGitTeamMemoryTrustedPresentationAndExactOKRoutes(t *testing.T) {
	_, ts := newGitMemoryReviewServer(t)
	defer ts.Close()
	source := registerServerSource(t, ts)
	stage := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/review/stage",
		serverStageBody(source, "Use the approved brief before drafting the launch page."))
	defer stage.Body.Close()
	if stage.StatusCode != http.StatusOK {
		t.Fatalf("stage status = %d", stage.StatusCode)
	}
	var batch store.GitTeamMemoryBatchView
	if err := json.NewDecoder(stage.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	digests := []string{batch.Candidates[0].ContentDigest}
	approverLabel := "Nikita"
	approverDigest := sha256.Sum256([]byte("pulse-git-memory-approver-label-v1\x00" + approverLabel))
	present := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/review/present", map[string]any{
		"schema": "pulse.git_team_memory.presentation.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding,
		"batch_id": batch.BatchID, "batch_generation": batch.Generation,
		"host": "codex", "task_id": "task_server_git_memory",
		"session_ref": "session:" + strings.Repeat("c", 64), "turn_ref": "turn:" + strings.Repeat("d", 64),
		"source_event_digest": strings.Repeat("e", 64), "card_block_digest": strings.Repeat("f", 64),
		"candidate_digests": digests, "approver_label_digest": hex.EncodeToString(approverDigest[:]),
	})
	defer present.Body.Close()
	if present.StatusCode != http.StatusOK {
		t.Fatalf("present status = %d", present.StatusCode)
	}
	var receipt store.GitTeamMemoryPresentationReceipt
	if err := json.NewDecoder(present.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.State != "presented" || receipt.BatchID != batch.BatchID {
		t.Fatalf("presentation receipt = %#v", receipt)
	}
	approve := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/review/exact-ok", map[string]any{
		"schema": "pulse.git_team_memory.exact_ok.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding,
		"host": "codex", "session_ref": "session:" + strings.Repeat("c", 64),
		"prompt_event_digest": strings.Repeat("1", 64),
	})
	defer approve.Body.Close()
	if approve.StatusCode != http.StatusOK {
		t.Fatalf("exact ok status = %d", approve.StatusCode)
	}
	var lease store.GitTeamMemoryApprovalLease
	if err := json.NewDecoder(approve.Body).Decode(&lease); err != nil {
		t.Fatal(err)
	}
	if lease.State != "issued" || lease.BatchID != batch.BatchID || lease.Validate() != nil {
		t.Fatalf("approval lease = %#v", lease)
	}
	start := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/publications/start", map[string]any{
		"schema": "pulse.git_team_memory.publication_start.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding,
		"approval_lease_id": lease.LeaseID, "approver_label": approverLabel,
		"expected_parent": strings.Repeat("a", 40),
	})
	defer start.Body.Close()
	if start.StatusCode != http.StatusOK {
		t.Fatalf("publication start status = %d", start.StatusCode)
	}
	var publication store.GitTeamMemoryPublicationReceipt
	if err := json.NewDecoder(start.Body).Decode(&publication); err != nil {
		t.Fatal(err)
	}
	finalize := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/publications/finalize", map[string]any{
		"schema": "pulse.git_team_memory.publication_finalize.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding,
		"publication_id": publication.PublicationID, "files_digest": publication.FilesDigest,
		"outcome": "committed", "commit_hash": strings.Repeat("c", 40),
	})
	defer finalize.Body.Close()
	if finalize.StatusCode != http.StatusOK {
		t.Fatalf("publication finalize status = %d", finalize.StatusCode)
	}

	unknown := map[string]any{
		"schema": "pulse.git_team_memory.exact_ok.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding,
		"host": "codex", "session_ref": "session:" + strings.Repeat("c", 64),
		"prompt_event_digest": strings.Repeat("2", 64), "raw_prompt": "ok",
	}
	bad := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/review/exact-ok", unknown)
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("raw prompt field status = %d", bad.StatusCode)
	}
}

func TestGitTeamMemoryReviewRoutesStageInspectEditAndRejectWithoutApproval(t *testing.T) {
	vault, ts := newGitMemoryReviewServer(t)
	defer ts.Close()
	source := registerServerSource(t, ts)
	stage := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/review/stage",
		serverStageBody(source, "Use the approved brief before drafting the launch page."))
	defer stage.Body.Close()
	if stage.StatusCode != http.StatusOK {
		t.Fatalf("stage status = %d", stage.StatusCode)
	}
	var batch store.GitTeamMemoryBatchView
	if err := json.NewDecoder(stage.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Candidates) != 1 {
		t.Fatalf("batch = %#v", batch)
	}
	candidate := batch.Candidates[0]
	inspect := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/review/inspect", map[string]any{
		"schema": "pulse.git_team_memory.inspect.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding, "batch_id": batch.BatchID,
	})
	defer inspect.Body.Close()
	if inspect.StatusCode != http.StatusOK {
		t.Fatalf("inspect status = %d", inspect.StatusCode)
	}
	edit := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/review/candidates/"+candidate.CandidateID+"/edit", map[string]any{
		"schema": "pulse.git_team_memory.edit.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding,
		"candidate_id": candidate.CandidateID, "expected_version": 1,
		"candidate": map[string]any{
			"kind": "decision", "statement": "Use the approved brief and current audience constraints before drafting the launch page.",
			"audience": "project", "confidence": 0.93,
			"source_references": []map[string]any{{"source_id": source.SourceID, "version_digest": source.VersionDigest}},
			"advisory_warnings": []map[string]any{},
		},
	})
	defer edit.Body.Close()
	if edit.StatusCode != http.StatusOK {
		t.Fatalf("edit status = %d", edit.StatusCode)
	}
	reject := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/review/candidates/"+candidate.CandidateID+"/reject", map[string]any{
		"schema": "pulse.git_team_memory.reject.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding,
		"candidate_id": candidate.CandidateID, "expected_version": 2, "reason_code": "user_rejected",
	})
	defer reject.Body.Close()
	if reject.StatusCode != http.StatusOK {
		t.Fatalf("reject status = %d", reject.StatusCode)
	}
	var publicationRows int
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM git_memory_publications`).Scan(&publicationRows); err != nil {
		t.Fatal(err)
	}
	if publicationRows != 0 {
		t.Fatalf("U1 created %d publication rows", publicationRows)
	}
}

func TestGitTeamMemoryReviewRouteRejectsRawFlagUnknownFieldsAndUnsafeMixedBatch(t *testing.T) {
	vault, ts := newGitMemoryReviewServer(t)
	defer ts.Close()
	source := registerServerSource(t, ts)
	for name, mutate := range map[string]func(map[string]any){
		"raw flag":      func(body map[string]any) { body["raw_input_included"] = true },
		"unknown field": func(body map[string]any) { body["approval"] = "ok" },
	} {
		t.Run(name, func(t *testing.T) {
			body := serverStageBody(source, "Use the approved brief before drafting the launch page.")
			mutate(body)
			resp := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/review/stage", body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d", resp.StatusCode)
			}
		})
	}
	mixed := serverStageBody(source, "Use the approved brief before drafting the launch page.")
	mixed["idempotency_key"] = "stage_server_git_memory_mixed"
	candidates := mixed["candidates"].([]map[string]any)
	unsafe := map[string]any{}
	for key, value := range candidates[0] {
		unsafe[key] = value
	}
	unsafe["statement"] = "User: copy this transcript and use sk-abcdefghijklmnopqrstuvwxyz123456."
	mixed["candidates"] = append(candidates, unsafe)
	resp := pulseJSON(t, ts, http.MethodPost, "/project/shared-memory/review/stage", mixed)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsafe mixed status = %d", resp.StatusCode)
	}
	var count int
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM git_memory_review_candidates`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unsafe route persisted %d candidates", count)
	}
}
