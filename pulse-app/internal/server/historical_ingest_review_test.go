package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/historicalingest"
)

type historicalEvidenceStub struct {
	payload HistoricalIngestWorkPayload
	err     error
}

func (stub historicalEvidenceStub) LoadHistoricalIngestEvidence(context.Context, historicalingest.WorkUnit) (HistoricalIngestWorkPayload, error) {
	return stub.payload, stub.err
}

func seedHistoricalReview(t *testing.T, srv *Server, evidence string, items ...historicalingest.MaterialItem) historicalingest.ReviewSnapshot {
	t.Helper()
	evidenceDigest := digestHistoricalEvidence(evidence)
	snapshot := historicalingest.SourceSnapshot{
		Digest: strings.Repeat("a", 64), Cutoff: time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC), RootCount: 1,
		ParserVersion: historicalingest.CodexParserVersionV1,
		Files: []historicalingest.SourceFilePrefix{{
			Alias: "source_0123456789abcdef", CapturedBytes: 120, PrefixDigest: strings.Repeat("b", 64),
			RootID: "root_test", ParserVersion: historicalingest.CodexParserVersionV1, RecordCount: 1, IncludedCount: 1,
		}},
	}
	unit := historicalingest.WorkUnit{ID: "unit_test", RootID: "root_test", SnapshotDigest: snapshot.Digest, EvidenceDigest: evidenceDigest, EvidenceBytes: int64(len(evidence)), SourceAliases: []string{"source_0123456789abcdef"}}
	contract := historicalingest.RunnerContract{Digest: strings.Repeat("f", 64), SchemaDigest: historicalingest.SchemaDigest(), ModelID: "gpt-5.6-luna", ModelEffort: "low", ParserVersion: historicalingest.CodexParserVersionV1, PromptVersion: "historical_prompt_v1"}
	jobID := "job_0123456789abcdef"
	if _, err := srv.historicalIngest.StartJob(jobID, snapshot, []historicalingest.WorkUnit{unit}, contract); err != nil {
		t.Fatal(err)
	}
	lease, err := srv.historicalIngest.LeaseNext(jobID)
	if err != nil {
		t.Fatal(err)
	}
	for index := range items {
		items[index].SourceRefs = []historicalingest.SourceRef{{Alias: snapshot.Files[0].Alias, PrefixDigest: snapshot.Files[0].PrefixDigest, RecordLocator: "record:1"}}
	}
	result := historicalingest.WorkUnitResult{SchemaVersion: historicalingest.SchemaVersionV1, WorkUnitID: unit.ID, EvidenceDigest: unit.EvidenceDigest, Items: items, ZeroMaterial: len(items) == 0}
	if _, err := srv.historicalIngest.SubmitResult(jobID, unit.ID, lease.Token, result, historicalingest.TokenUsage{InputTokens: 20, OutputTokens: 5}, contract.Digest); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := srv.historicalIngest.BuildManifest(jobID); err != nil {
		t.Fatal(err)
	}
	review, err := srv.historicalIngest.ReviewSnapshot(jobID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return review
}

func historicalStateItem(summary string) historicalingest.MaterialItem {
	intensity := .8
	return historicalingest.MaterialItem{
		CandidateID: "candidate_0123456789abcdef", Kind: historicalingest.MaterialKindState, Confidence: .74,
		Privacy: historicalingest.PrivacyPrivate, EpistemicStatus: historicalingest.EpistemicHypothesis,
		Derivation: historicalingest.DerivationInferred, ValidTime: historicalingest.ValidTime{From: time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)},
		Scope:   historicalingest.Scope{Kind: historicalingest.ScopeGlobal},
		Payload: historicalingest.MaterialPayload{StateKind: "emotion", Summary: summary, Intensity: &intensity},
	}
}

func historicalDecisionItem(summary string) historicalingest.MaterialItem {
	return historicalingest.MaterialItem{
		CandidateID: "candidate_1123456789abcdef", Kind: historicalingest.MaterialKindDecision, Confidence: .9,
		Privacy: historicalingest.PrivacyPrivate, EpistemicStatus: historicalingest.EpistemicExplicit,
		Derivation: historicalingest.DerivationDirect, ValidTime: historicalingest.ValidTime{From: time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)},
		Scope: historicalingest.Scope{Kind: historicalingest.ScopeGlobal}, Payload: historicalingest.MaterialPayload{Summary: summary},
	}
}

func TestHomeHistoricalEgressConsentBindsExactFrozenSnapshot(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	snapshot := historicalingest.SourceSnapshot{
		Digest: strings.Repeat("a", 64), Cutoff: time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC), RootCount: 1,
		ParserVersion: historicalingest.CodexParserVersionV1,
		Files:         []historicalingest.SourceFilePrefix{{Alias: "source_0123456789abcdef", CapturedBytes: 20, PrefixDigest: strings.Repeat("b", 64), RootID: "root_test", ParserVersion: historicalingest.CodexParserVersionV1, RecordCount: 1, IncludedCount: 1}},
	}
	unit := historicalingest.WorkUnit{ID: "unit_test", RootID: "root_test", SnapshotDigest: snapshot.Digest, EvidenceDigest: strings.Repeat("c", 64), EvidenceBytes: 64, SourceAliases: []string{"source_0123456789abcdef"}}
	contract := historicalingest.RunnerContract{Digest: strings.Repeat("d", 64), SchemaDigest: historicalingest.SchemaDigest(), ModelID: "gpt-5.6-luna", ModelEffort: "low", ParserVersion: historicalingest.CodexParserVersionV1, PromptVersion: "historical_prompt_v1"}
	if _, err := srv.historicalIngest.StartJobAwaitingEgress("job_0123456789abcdef", snapshot, []historicalingest.WorkUnit{unit}, contract); err != nil {
		t.Fatal(err)
	}
	home := srv.memoryHomeHistoricalReview()
	if !home.CanAuthorizeEgress || home.HasManifest || home.EgressConfirmationDigest == "" {
		t.Fatalf("unexpected consent view: %+v", home)
	}
	html, err := renderMemoryHomeHTML(memoryHomePage{Historical: home, CSRFToken: "csrf-test"})
	if err != nil || !strings.Contains(html, "Authorize 1 GPT-5.4 turns") || !strings.Contains(html, "cannot write memory") || !strings.Contains(html, "64 B normalized evidence") {
		t.Fatalf("consent UI missing: err=%v html=%s", err, html)
	}
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	path := "history/job_0123456789abcdef/authorize-egress"
	stale := httptest.NewRecorder()
	srv.Handler().ServeHTTP(stale, homeMutationRequest(srv, session, path, url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "snapshot_digest": {snapshot.Digest}, "runner_contract_digest": {contract.Digest}, "confirmation_digest": {strings.Repeat("0", 64)},
	}))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale consent status=%d", stale.Code)
	}
	valid := httptest.NewRecorder()
	srv.Handler().ServeHTTP(valid, homeMutationRequest(srv, session, path, url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "snapshot_digest": {snapshot.Digest}, "runner_contract_digest": {contract.Digest}, "confirmation_digest": {home.EgressConfirmationDigest},
	}))
	if valid.Code != http.StatusNoContent {
		t.Fatalf("valid consent status=%d body=%s", valid.Code, valid.Body.String())
	}
	status, err := srv.historicalIngest.Status("job_0123456789abcdef")
	if err != nil || status.State != historicalingest.JobExtracting || !status.EgressAuthorized {
		t.Fatalf("authorized status=%+v err=%v", status, err)
	}
}

func TestMemoryHomeHistoricalReviewRendersEscapedBlockingCardsAndNoApply(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	evidence := "bounded normalized evidence"
	srv.historicalEvidence = historicalEvidenceStub{payload: HistoricalIngestWorkPayload{TrustedPrompt: "prompt", Evidence: evidence}}
	review := seedHistoricalReview(t, srv, evidence, historicalStateItem("Owner may be frustrated. <script>alert(1)</script>"))
	home := srv.memoryHomeHistoricalReview()
	if !home.Available || home.RemainingRequired != 1 || len(home.Cards) != 1 || !home.Cards[0].RequiresReview {
		t.Fatalf("home=%+v review=%+v", home, review)
	}
	html, err := renderMemoryHomeHTML(memoryHomePage{Historical: home, CSRFToken: "csrf-test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"What Pulse read", "What Pulse will write", "Blocking decisions left", "Hypothesis", "Inferred", "Load bounded source evidence", "Finish blocking decisions first"} {
		if !strings.Contains(html, want) {
			t.Fatalf("Home missing %q", want)
		}
	}
	if strings.Contains(html, "<script>alert(1)</script>") || strings.Contains(html, "Apply memory") || strings.Contains(html, ">Apply<") {
		t.Fatal("historical review rendered executable content or apply authority")
	}
}

func TestHomeHistoricalReviewMutationRequiresSessionCSRFExactRevisionAndDigest(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	evidence := "bounded normalized evidence"
	srv.historicalEvidence = historicalEvidenceStub{payload: HistoricalIngestWorkPayload{TrustedPrompt: "prompt", Evidence: evidence}}
	review := seedHistoricalReview(t, srv, evidence, historicalStateItem("Owner may be frustrated."))
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	candidateID := review.Items[0].Item.CandidateID
	path := "history/" + review.JobID + "/items/" + candidateID + "/review"

	withoutCSRF := httptest.NewRecorder()
	srv.Handler().ServeHTTP(withoutCSRF, homeMutationRequest(srv, session, path, url.Values{
		"expected_revision": {"1"}, "expected_digest": {review.ManifestDigest}, "action": {"kept"},
	}))
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("without CSRF status=%d", withoutCSRF.Code)
	}

	kept := httptest.NewRecorder()
	srv.Handler().ServeHTTP(kept, homeMutationRequest(srv, session, path, url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_revision": {"1"},
		"expected_digest": {review.ManifestDigest}, "action": {"kept"},
	}))
	if kept.Code != http.StatusNoContent {
		t.Fatalf("keep status=%d body=%s", kept.Code, kept.Body.String())
	}
	current, err := srv.historicalIngest.ReviewSnapshot(review.JobID, nil)
	if err != nil || current.RemainingRequired != 0 || current.Revision != 2 {
		t.Fatalf("current=%+v err=%v", current, err)
	}

	stale := httptest.NewRecorder()
	srv.Handler().ServeHTTP(stale, homeMutationRequest(srv, session, path, url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_revision": {"1"},
		"expected_digest": {review.ManifestDigest}, "action": {"excluded"},
	}))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}

	bindingDigest, repositoryID, _ := srv.cfg.Store.ProductRuntimeBoundary()
	destinationStoreID := srv.cfg.Store.StoreID()
	wrongConfirmation := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wrongConfirmation, homeMutationRequest(srv, session, "history/"+review.JobID+"/complete", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_revision": {"2"},
		"manifest_digest": {current.ManifestDigest}, "destination_store_id": {destinationStoreID},
		"repository_id": {repositoryID}, "confirmation_digest": {strings.Repeat("0", 64)},
	}))
	if wrongConfirmation.Code != http.StatusConflict {
		t.Fatalf("wrong confirmation status=%d body=%s", wrongConfirmation.Code, wrongConfirmation.Body.String())
	}

	completed := httptest.NewRecorder()
	srv.Handler().ServeHTTP(completed, homeMutationRequest(srv, session, "history/"+review.JobID+"/complete", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_revision": {"2"},
		"manifest_digest": {current.ManifestDigest}, "destination_store_id": {destinationStoreID},
		"repository_id":       {repositoryID},
		"confirmation_digest": {historicalReviewDestinationConfirmationDigest(review.JobID, current.Revision, current.ManifestDigest, destinationStoreID, repositoryID, bindingDigest)},
	}))
	if completed.Code != http.StatusNoContent {
		t.Fatalf("complete status=%d body=%s", completed.Code, completed.Body.String())
	}
	final, err := srv.historicalIngest.ReviewSnapshot(review.JobID, nil)
	if err != nil || final.State != historicalingest.JobApprovalReady || !final.ReviewComplete {
		t.Fatalf("final=%+v err=%v", final, err)
	}
}

func TestHomeHistoricalPrepareAndApplyUsesExactWriteSetWithoutCLIAuthority(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	evidence := "bounded normalized evidence"
	srv.historicalEvidence = historicalEvidenceStub{payload: HistoricalIngestWorkPayload{TrustedPrompt: "prompt", Evidence: evidence}}
	review := seedHistoricalReview(t, srv, evidence, historicalDecisionItem("Keep the reviewed import exact and replayable."))
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, repositoryID, _ := vault.ProductRuntimeBoundary()
	destinationStoreID := vault.StoreID()
	completed := httptest.NewRecorder()
	srv.Handler().ServeHTTP(completed, homeMutationRequest(srv, session, "history/"+review.JobID+"/complete", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_revision": {"1"},
		"manifest_digest": {review.ManifestDigest}, "destination_store_id": {destinationStoreID},
		"repository_id":       {repositoryID},
		"confirmation_digest": {historicalReviewDestinationConfirmationDigest(review.JobID, review.Revision, review.ManifestDigest, destinationStoreID, repositoryID, bindingDigest)},
	}))
	if completed.Code != http.StatusNoContent {
		t.Fatalf("complete status=%d body=%s", completed.Code, completed.Body.String())
	}
	frozen, err := srv.historicalIngest.ReviewSnapshot(review.JobID, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared := httptest.NewRecorder()
	srv.Handler().ServeHTTP(prepared, homeMutationRequest(srv, session, "history/"+review.JobID+"/prepare-apply", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_revision": {strconv.FormatInt(frozen.Revision, 10)},
		"manifest_digest": {frozen.ManifestDigest}, "destination_store_id": {destinationStoreID}, "repository_id": {repositoryID},
	}))
	if prepared.Code != http.StatusNoContent {
		t.Fatalf("prepare status=%d body=%s", prepared.Code, prepared.Body.String())
	}
	home := srv.memoryHomeHistoricalReview()
	if !home.WriteSetReady || !home.CanApply || home.WriteSetDigest == "" || home.ApplyConfirmationDigest == "" || home.PlannedCreatedCount != 1 {
		t.Fatalf("prepared home=%+v", home)
	}
	form := url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_revision": {strconv.FormatInt(home.Revision, 10)},
		"manifest_digest": {home.ManifestDigest}, "write_set_digest": {home.WriteSetDigest},
		"destination_store_id": {home.DestinationStoreID}, "destination_generation": {strconv.FormatInt(home.DestinationGeneration, 10)},
		"confirmation_digest": {strings.Repeat("0", 64)},
	}
	wrong := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wrong, homeMutationRequest(srv, session, "history/"+review.JobID+"/apply", form))
	if wrong.Code != http.StatusConflict {
		t.Fatalf("wrong apply status=%d body=%s", wrong.Code, wrong.Body.String())
	}
	form.Set("confirmation_digest", home.ApplyConfirmationDigest)
	applied := httptest.NewRecorder()
	srv.Handler().ServeHTTP(applied, homeMutationRequest(srv, session, "history/"+review.JobID+"/apply", form))
	if applied.Code != http.StatusNoContent {
		t.Fatalf("apply status=%d body=%s", applied.Code, applied.Body.String())
	}
	status, err := srv.historicalIngest.Status(review.JobID)
	if err != nil || status.State != historicalingest.JobCommittedIndexing || status.BatchReceiptID == "" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	var capsules, receipts int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&capsules); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT count(*) FROM historical_ingest_batch_receipts WHERE job_id=?`, review.JobID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if capsules != 1 || receipts != 1 {
		t.Fatalf("capsules=%d receipts=%d", capsules, receipts)
	}
}

func TestHistoricalApplyRecoveryJoinsCommittedStoreReceiptAfterCheckpointGap(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	evidence := "bounded recovery evidence"
	srv.historicalEvidence = historicalEvidenceStub{payload: HistoricalIngestWorkPayload{TrustedPrompt: "prompt", Evidence: evidence}}
	review := seedHistoricalReview(t, srv, evidence, historicalDecisionItem("Recover the exact committed import after a checkpoint interruption."))
	if _, err := srv.historicalIngest.CompleteReview(
		review.JobID, review.Revision, review.ManifestDigest,
		historicalingest.ReviewConfirmationDigest(review.JobID, review.Revision, review.ManifestDigest), nil,
	); err != nil {
		t.Fatal(err)
	}
	frozen, err := srv.historicalIngest.ReviewSnapshot(review.JobID, nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := srv.historicalIngest.ApplySource(review.JobID, frozen.Revision, frozen.ManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, repositoryID, _ := vault.ProductRuntimeBoundary()
	set, digest, err := vault.CompileHistoricalWriteSet(source, bindingDigest, repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.historicalIngest.RecordWriteSet(review.JobID, frozen.ManifestDigest, digest, set.DestinationStoreID, set.DestinationGeneration); err != nil {
		t.Fatal(err)
	}
	now := srv.homeNow()
	capability, err := vault.AuthorizeHistoricalApply(review.JobID, digest, set.DestinationGeneration, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.CreateHistoricalBackup(review.JobID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.historicalIngest.MarkApplying(review.JobID, frozen.ManifestDigest, digest); err != nil {
		t.Fatal(err)
	}
	receipt, err := vault.ApplyHistoricalWriteSet(capability, now)
	if err != nil {
		t.Fatal(err)
	}
	before, err := srv.historicalIngest.Status(review.JobID)
	if err != nil || before.State != historicalingest.JobApplying || before.BatchReceiptID != "" {
		t.Fatalf("pre-recovery status=%+v err=%v", before, err)
	}
	if err := srv.recoverHistoricalIngest(); err != nil {
		t.Fatal(err)
	}
	after, err := srv.historicalIngest.Status(review.JobID)
	if err != nil || after.State != historicalingest.JobCommittedIndexing || after.BatchReceiptID != receipt.ReceiptID {
		t.Fatalf("recovered status=%+v receipt=%+v err=%v", after, receipt, err)
	}
}

func TestHomeHistoricalEvidenceIsBoundedPlainTextAndFailsOnDrift(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	evidence := strings.Repeat("e", historicalEvidenceViewLimit+100) + "<script>alert(1)</script>"
	srv.historicalEvidence = historicalEvidenceStub{payload: HistoricalIngestWorkPayload{TrustedPrompt: "prompt", Evidence: evidence}}
	review := seedHistoricalReview(t, srv, evidence, historicalStateItem("Owner may be frustrated."))
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	request := homePageRequest(srv, session)
	request.URL.Path = strings.TrimSuffix(request.URL.Path, "/") + "/history/" + review.JobID + "/items/" + review.Items[0].Item.CandidateID + "/evidence"
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/plain; charset=utf-8" || response.Body.Len() > historicalEvidenceViewLimit+64 || !strings.Contains(response.Body.String(), "bounded by Pulse") {
		t.Fatalf("evidence status=%d type=%q bytes=%d body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.Len(), response.Body.String())
	}

	srv.historicalEvidence = historicalEvidenceStub{err: errors.New("prefix changed")}
	drift := httptest.NewRecorder()
	srv.Handler().ServeHTTP(drift, request)
	if drift.Code != http.StatusConflict {
		t.Fatalf("drift status=%d body=%s", drift.Code, drift.Body.String())
	}
	status, err := srv.historicalIngest.Status(review.JobID)
	if err != nil || status.State != historicalingest.JobStale {
		t.Fatalf("drift status checkpoint=%+v err=%v", status, err)
	}
}

func TestHomeHistoricalEditCreatesNewScopedRevision(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	evidence := "state evidence"
	srv.historicalEvidence = historicalEvidenceStub{payload: HistoricalIngestWorkPayload{TrustedPrompt: "prompt", Evidence: evidence}}
	review := seedHistoricalReview(t, srv, evidence, historicalStateItem("Owner may be frustrated."))
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	candidateID := review.Items[0].Item.CandidateID
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, homeMutationRequest(srv, session, "history/"+review.JobID+"/items/"+candidateID+"/review", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_revision": {"1"}, "expected_digest": {review.ManifestDigest},
		"action": {"edit"}, "primary_text": {"The owner explicitly reported frustration."}, "scope_kind": {"project"},
		"project_id": {"project_pulse"}, "valid_from": {"2026-07-22T05:00:00Z"}, "valid_to": {""},
		"epistemic_status": {"hypothesis"}, "continuity_status": {""},
	}))
	if response.Code != http.StatusNoContent {
		t.Fatalf("edit status=%d body=%s", response.Code, response.Body.String())
	}
	current, err := srv.historicalIngest.ReviewSnapshot(review.JobID, nil)
	if err != nil || current.Revision != 2 || current.ManifestDigest == review.ManifestDigest || current.Items[0].Item.Scope.ProjectID != "project_pulse" || current.Items[0].Item.Payload.Summary != "The owner explicitly reported frustration." {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestHomeHistoricalReviewBlocksUnavailableEvidenceUntilExplicitDisposition(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	review := seedHistoricalReview(t, srv, "evidence without a provider", historicalDecisionItem("Use reviewed local memory."))
	home := srv.memoryHomeHistoricalReview()
	if home.RemainingRequired != 1 || home.Cards[0].EvidenceAvailable || !home.Cards[0].RequiresReview {
		t.Fatalf("home=%+v", home)
	}
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, repositoryID, _ := srv.cfg.Store.ProductRuntimeBoundary()
	destinationStoreID := srv.cfg.Store.StoreID()
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, homeMutationRequest(srv, session, "history/"+review.JobID+"/complete", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_revision": {"1"},
		"manifest_digest": {review.ManifestDigest}, "destination_store_id": {destinationStoreID},
		"repository_id":       {repositoryID},
		"confirmation_digest": {historicalReviewDestinationConfirmationDigest(review.JobID, review.Revision, review.ManifestDigest, destinationStoreID, repositoryID, bindingDigest)},
	}))
	if response.Code != http.StatusConflict {
		t.Fatalf("unavailable evidence completion status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHomeHistoricalEntityRewriteRequiresPreviewDigest(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	evidence := "relation evidence"
	srv.historicalEvidence = historicalEvidenceStub{payload: HistoricalIngestWorkPayload{TrustedPrompt: "prompt", Evidence: evidence}}
	relation := historicalingest.MaterialItem{
		CandidateID: "candidate_2123456789abcdef", Kind: historicalingest.MaterialKindRelation, Confidence: .9,
		Privacy: historicalingest.PrivacyPrivate, EpistemicStatus: historicalingest.EpistemicExplicit,
		Derivation: historicalingest.DerivationDirect, ValidTime: historicalingest.ValidTime{From: time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)},
		Scope:   historicalingest.Scope{Kind: historicalingest.ScopeGlobal},
		Payload: historicalingest.MaterialPayload{SubjectID: "person_alex", Predicate: "works_on", ObjectID: "project_pulse"},
	}
	review := seedHistoricalReview(t, srv, evidence, relation)
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	base := url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_revision": {"1"},
		"expected_digest": {review.ManifestDigest}, "mode": {"merge"}, "from_entity_id": {"person_alex"},
		"to_entity_id": {"person_alexander"}, "selected_candidates": {""},
	}
	previewResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(previewResponse, homeMutationRequest(srv, session, "history/"+review.JobID+"/entities/preview", base))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview historicalingest.EntityRewritePreview
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil || len(preview.Affected) != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	base.Set("preview_digest", strings.Repeat("0", 64))
	wrong := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wrong, homeMutationRequest(srv, session, "history/"+review.JobID+"/entities/apply", base))
	if wrong.Code != http.StatusConflict {
		t.Fatalf("wrong preview status=%d body=%s", wrong.Code, wrong.Body.String())
	}
	base.Set("preview_digest", preview.PreviewDigest)
	applied := httptest.NewRecorder()
	srv.Handler().ServeHTTP(applied, homeMutationRequest(srv, session, "history/"+review.JobID+"/entities/apply", base))
	if applied.Code != http.StatusNoContent {
		t.Fatalf("apply preview status=%d body=%s", applied.Code, applied.Body.String())
	}
}

func TestHistoricalReviewStateCopyCoversEveryLifecycleWithoutSupersededState(t *testing.T) {
	states := []historicalingest.JobState{
		historicalingest.JobPreflight, historicalingest.JobAwaitingEgress, historicalingest.JobSnapshotting,
		historicalingest.JobExtracting, historicalingest.JobPausedQuota, historicalingest.JobExtractionFailed,
		historicalingest.JobManifestReady, historicalingest.JobApprovalReady, historicalingest.JobStale,
		historicalingest.JobApproved, historicalingest.JobApplying, historicalingest.JobCommittedIndexing,
		historicalingest.JobIndexingFailed, historicalingest.JobRetrievalReady, historicalingest.JobCanceled,
	}
	for _, state := range states {
		title, detail := historicalReviewStateCopy(state, 1)
		if strings.TrimSpace(title) == "" || strings.TrimSpace(detail) == "" || strings.Contains(title, "superseded") {
			t.Fatalf("state %s copy title=%q detail=%q", state, title, detail)
		}
	}
}
