package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
	"github.com/nkkmnk/pulse/internal/teamjobs"
	"github.com/nkkmnk/pulse/internal/teamread"
)

func TestApprovedPublicationBecomesAuthorizedNewSessionContinuityAfterProjection(t *testing.T) {
	srv, fixture, lease := newReadyTeamServer(t)
	project, err := fixture.store.CreateTeamProject(
		context.Background(), fixture.ownerID, "Published continuity",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GrantProjectAccess(context.Background(), store.GrantProjectAccessRequest{
		ActorPrincipalID: fixture.ownerID, ProjectID: project.ProjectID,
		TargetPrincipalID: fixture.agentID, AccessLevel: "write",
	}); err != nil {
		t.Fatal(err)
	}
	const deploymentID = "deployment_publication_continuity_e2e"
	if err := fixture.store.ConfigureTeamPublicationTarget(store.TeamPublicationTarget{
		DeploymentID: deploymentID, ProjectID: project.ProjectID, SyntheticOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	var policyEpoch int64
	if err := fixture.store.DB().QueryRow(`
		SELECT policy_epoch FROM team_policy_metadata
		 WHERE store_id = ? AND team_id = ?`, fixture.storeID, fixture.teamID,
	).Scan(&policyEpoch); err != nil {
		t.Fatal(err)
	}

	const content = "Keep the approved Airlock decision in project continuity."
	clientKey := teamauth.OAuthClientKey(fixture.issuer, "agent-client")
	envelope := []byte(fmt.Sprintf(
		`{"action":"team.commons.publish","client_key":%q,"content":%q,"deployment_id":%q,"metadata":{"kind":"decision","tags":["pilot","synthetic"]},"policy_epoch":%d,"publication_key":"publication_continuity_e2e","schema":"pulse.team.airlock_envelope.v1","source_timestamp":%q,"store_id":%q,"target_id":%q,"target_kind":"commons","team_id":%q,"writer_id":%q,"writer_principal_id":%q}`,
		clientKey, content, deploymentID, policyEpoch,
		fixture.now.Format("2006-01-02T15:04:05.000Z"), fixture.storeID,
		fixture.teamID, fixture.teamID, lease.WriterID, fixture.agentID,
	))
	envelopeDigest := fmt.Sprintf("%x", sha256.Sum256(envelope))
	ownerClientKey := teamauth.OAuthClientKey(fixture.root.Issuer, fixture.root.AdminClientID)
	draft, err := fixture.store.BuildTeamPublicationApprovalDraft(
		context.Background(), store.TeamPublicationApprovalDraftRequest{
			CanonicalEnvelope: envelope, EnvelopeDigest: envelopeDigest,
			Writer:                    store.TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token},
			ApprovingOwnerPrincipalID: fixture.ownerID, ApprovingClientKey: ownerClientKey,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	draft.AssertionKID = "publication-continuity-e2e-kid"
	draft.AssertionJTI = "publication-continuity-e2e-jti"
	draft.AssertionExpiresAt = fixture.now.Add(20 * time.Second)
	draft.StepUpAt = fixture.now.Add(-time.Minute)
	draft.ExpiresAt = fixture.now.Add(45 * time.Second)
	challenge, err := fixture.store.IssueTeamPublicationApproval(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := fixture.store.CommitApprovedTeamPublication(
		context.Background(), store.ApprovedTeamPublicationRequest{
			CanonicalEnvelope: envelope, EnvelopeDigest: envelopeDigest,
			ApprovalNonce: challenge.Nonce, RequestID: "request-publication-continuity-e2e",
			Writer:                    store.TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token},
			ApprovingOwnerPrincipalID: fixture.ownerID, ApprovingClientKey: ownerClientKey,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.CheckReadiness(context.Background()); err != nil {
		t.Fatalf("readiness after publication: %T %v", err, err)
	}

	readService := teamread.New(
		fixture.store,
		retrieve.NewTeamRetrievalEngine(retrieve.TeamRetrievalConfig{CandidateLimit: 32}),
	)
	srv.cfg.ReadService = readService
	resume := func(jti string) teamResumeResponse {
		t.Helper()
		body := []byte(fmt.Sprintf(
			`{"schema":"pulse.team.resume.v1","active_context":{"project_id":%q,"session_id":"session-after-publication"},"thread_id":"repository-compositor-bound","limit":20}`,
			project.ProjectID,
		))
		claims := fixture.claims(jti, "agent-client", body)
		claims["path"] = TeamResumeRoutePath
		assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
		response := serveTeamGraphRequest(
			srv.Handler(), assertion, "req-"+jti,
			bodyPathRequest{path: TeamResumeRoutePath, body: body},
		)
		if response.Code != http.StatusOK {
			t.Fatalf("resume = %d %q", response.Code, response.Body.String())
		}
		var result teamResumeResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if before := resume("publication-continuity-before-projection"); before.ReturnedCount != 0 {
		t.Fatalf("unmaterialized publication entered continuity: %+v", before)
	}

	processor, err := teamjobs.NewStoreProjectionProcessor(fixture.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := teamjobs.NewProjectionWorker(teamjobs.ProjectionWorkerConfig{
		Store: fixture.store, Processor: processor,
		Writer:         store.TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token},
		ProjectionKind: "event", ClaimLimit: 1, LeaseTTL: 30 * time.Second,
		PollInterval: time.Millisecond, HeartbeatInterval: time.Millisecond,
		WorkerInstanceID: "publication-continuity-e2e-worker",
		BaseBackoff:      time.Second, MaxBackoff: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if projection.Claimed != 1 || projection.Completed != 1 {
		t.Fatalf("event projection = %+v", projection)
	}

	after := resume("publication-continuity-after-projection")
	if after.ReturnedCount != 2 || len(after.Sections.WhereWeLeftOff) != 1 ||
		len(after.Sections.ActiveDecisions) != 1 ||
		after.Sections.WhereWeLeftOff[0].ObjectID != receipt.ObjectID ||
		after.Sections.WhereWeLeftOff[0].Text != content ||
		after.Sections.ActiveDecisions[0].ObjectID != receipt.ObjectID ||
		after.Sections.ActiveDecisions[0].Text != content {
		t.Fatalf("published continuity lost content or root provenance: %+v", after)
	}
}
