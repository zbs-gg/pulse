package server

import (
	"encoding/json"
	"net/http"
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
