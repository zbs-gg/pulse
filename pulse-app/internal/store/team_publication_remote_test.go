package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

func canonicalRemotePublicationEnvelope(t *testing.T, fixture teamPublicationApprovalFixture, content, key string) ([]byte, string) {
	t.Helper()
	var policyEpoch int64
	if err := fixture.store.DB().QueryRow(`
		SELECT policy_epoch FROM team_policy_metadata
		 WHERE store_id = ? AND team_id = ?`, fixture.bootstrap.StoreID, fixture.bootstrap.TeamID,
	).Scan(&policyEpoch); err != nil {
		t.Fatal(err)
	}
	envelope := []byte(fmt.Sprintf(
		`{"action":"team.commons.publish","client_key":%q,"content":%q,"deployment_id":%q,"metadata":{"kind":"decision","tags":["pilot","synthetic"]},"policy_epoch":%d,"publication_key":%q,"schema":"pulse.team.airlock_envelope.v1","source_timestamp":"2026-07-15T03:00:00.000Z","store_id":%q,"target_id":%q,"target_kind":"commons","team_id":%q,"writer_id":%q,"writer_principal_id":%q}`,
		fixture.publisher.clientKey, content, fixture.issue.DeploymentID, policyEpoch, key,
		fixture.bootstrap.StoreID, fixture.bootstrap.TeamID, fixture.bootstrap.TeamID,
		fixture.lease.WriterID, fixture.publisher.binding.AgentPrincipalID,
	))
	digest := sha256.Sum256(envelope)
	return envelope, fmt.Sprintf("%x", digest)
}

func prepareRemotePublicationFixture(t *testing.T) teamPublicationApprovalFixture {
	t.Helper()
	fixture := newTeamPublicationApprovalFixture(t)
	if _, err := fixture.store.GrantProjectAccess(context.Background(), GrantProjectAccessRequest{
		ActorPrincipalID:  fixture.bootstrap.OwnerPrincipalID,
		ProjectID:         fixture.project.ProjectID,
		TargetPrincipalID: fixture.publisher.binding.AgentPrincipalID,
		AccessLevel:       "write",
	}); err != nil {
		fixture.store.Close()
		t.Fatal(err)
	}
	if err := fixture.store.ConfigureTeamPublicationTarget(TeamPublicationTarget{
		DeploymentID:  fixture.issue.DeploymentID,
		ProjectID:     fixture.project.ProjectID,
		SyntheticOnly: true,
	}); err != nil {
		fixture.store.Close()
		t.Fatal(err)
	}
	return fixture
}

func approveRemotePublication(t *testing.T, fixture teamPublicationApprovalFixture, envelope []byte, digest string) TeamPublicationApprovalChallenge {
	t.Helper()
	draft, err := fixture.store.BuildTeamPublicationApprovalDraft(context.Background(), TeamPublicationApprovalDraftRequest{
		CanonicalEnvelope:         envelope,
		EnvelopeDigest:            digest,
		Writer:                    TeamWriterLeaseIdentity{WriterID: fixture.lease.WriterID, Token: fixture.lease.Token},
		ApprovingOwnerPrincipalID: fixture.bootstrap.OwnerPrincipalID,
		ApprovingClientKey:        fixture.issue.ApprovingClientKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := *fixture.now
	draft.AssertionKID = "publication-remote-kid"
	draft.AssertionJTI = "publication-remote-jti"
	draft.AssertionExpiresAt = now.Add(20 * time.Second)
	draft.StepUpAt = now.Add(-time.Minute)
	draft.ExpiresAt = now.Add(45 * time.Second)
	challenge, err := fixture.store.IssueTeamPublicationApproval(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	return challenge
}

func TestCommitApprovedTeamPublicationIsAtomicAndReplaysExactReceipt(t *testing.T) {
	fixture := prepareRemotePublicationFixture(t)
	defer fixture.store.Close()
	envelope, digest := canonicalRemotePublicationEnvelope(t, fixture, "Use the synthetic reviewed team rule.", "publication_remote_01")
	challenge := approveRemotePublication(t, fixture, envelope, digest)
	request := ApprovedTeamPublicationRequest{
		CanonicalEnvelope:         envelope,
		EnvelopeDigest:            digest,
		ApprovalNonce:             challenge.Nonce,
		RequestID:                 "request-publication-remote-0001",
		Writer:                    TeamWriterLeaseIdentity{WriterID: fixture.lease.WriterID, Token: fixture.lease.Token},
		ApprovingOwnerPrincipalID: fixture.bootstrap.OwnerPrincipalID,
		ApprovingClientKey:        fixture.issue.ApprovingClientKey,
	}
	first, err := fixture.store.CommitApprovedTeamPublication(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.PublicationID == "" || first.ObjectID == "" || first.CapsuleID == "" ||
		first.ApprovalAuditEventID == "" || first.ObjectAuditEventID == "" ||
		first.EventProjectionJobID == "" || first.EmbeddingProjectionJobID == "" ||
		first.ReceiptDigest == "" || first.Replayed {
		t.Fatalf("incomplete publication receipt: %+v", first)
	}

	wantCounts := map[string]int{
		"team_publication_receipts":         1,
		"team_publication_receipt_payloads": 1,
		"team_object_registry":              1,
		"team_memory_capsules":              1,
		"team_projection_jobs":              2,
	}
	for table, want := range wantCounts {
		var got int
		query := "SELECT count(*) FROM " + table
		if err := fixture.store.DB().QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, got, want, err)
		}
	}
	var publicationAudits int
	if err := fixture.store.DB().QueryRow(`
		SELECT count(*) FROM team_audit_events
		 WHERE event_id IN (?, ?)`, first.ApprovalAuditEventID, first.ObjectAuditEventID,
	).Scan(&publicationAudits); err != nil || publicationAudits != 2 {
		t.Fatalf("publication audits=%d err=%v", publicationAudits, err)
	}
	var owner, author, scopeType, scopeID string
	if err := fixture.store.DB().QueryRow(`
		SELECT owner_principal_id, author_principal_id, scope_type, scope_id
		  FROM team_object_registry WHERE object_id = ?`, first.ObjectID,
	).Scan(&owner, &author, &scopeType, &scopeID); err != nil {
		t.Fatal(err)
	}
	if owner != fixture.bootstrap.OwnerPrincipalID || author != fixture.publisher.binding.AgentPrincipalID ||
		scopeType != string(teamauth.ScopeProject) || scopeID != fixture.project.ProjectID {
		t.Fatalf("wrong Commons root authority owner=%q author=%q scope=%s/%s", owner, author, scopeType, scopeID)
	}

	replayed, err := fixture.store.CommitApprovedTeamPublication(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.PublicationID != first.PublicationID ||
		replayed.ObjectID != first.ObjectID || replayed.CapsuleID != first.CapsuleID ||
		replayed.ReceiptDigest != first.ReceiptDigest {
		t.Fatalf("publication replay changed receipt: first=%+v replay=%+v", first, replayed)
	}
	filter, err := fixture.store.BuildAuthorizedCandidateFilter(context.Background(), CandidateFilterRequest{
		PrincipalID:  fixture.publisher.binding.AgentPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context:      teamauth.ActiveContext{TeamID: fixture.bootstrap.TeamID, ProjectID: fixture.project.ProjectID},
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := fixture.store.LookupTeamPublicationReceipt(context.Background(), TeamPublicationReceiptLookup{
		Filter: filter, PublicationKey: "publication_remote_01", EnvelopeDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.PublicationID != first.PublicationID || reconciled.ObjectID != first.ObjectID ||
		reconciled.ReceiptDigest != first.ReceiptDigest || reconciled.Replayed {
		t.Fatalf("read-only reconciliation changed receipt: %+v", reconciled)
	}
}

func TestCommitApprovedTeamPublicationRejectsChangedEnvelopeAndRollsBackApproval(t *testing.T) {
	fixture := prepareRemotePublicationFixture(t)
	defer fixture.store.Close()
	envelope, digest := canonicalRemotePublicationEnvelope(t, fixture, "Use the synthetic reviewed team rule.", "publication_remote_02")
	challenge := approveRemotePublication(t, fixture, envelope, digest)
	changed := []byte(strings.Replace(string(envelope), "reviewed team rule", "changed team rule", 1))
	changedDigest := sha256.Sum256(changed)
	request := ApprovedTeamPublicationRequest{
		CanonicalEnvelope:         changed,
		EnvelopeDigest:            fmt.Sprintf("%x", changedDigest),
		ApprovalNonce:             challenge.Nonce,
		RequestID:                 "request-publication-remote-0002",
		Writer:                    TeamWriterLeaseIdentity{WriterID: fixture.lease.WriterID, Token: fixture.lease.Token},
		ApprovingOwnerPrincipalID: fixture.bootstrap.OwnerPrincipalID,
		ApprovingClientKey:        fixture.issue.ApprovingClientKey,
	}
	if _, err := fixture.store.CommitApprovedTeamPublication(context.Background(), request); !errors.Is(err, ErrTeamPublicationApprovalBindingMismatch) {
		t.Fatalf("changed approved envelope error=%v", err)
	}
	for _, table := range []string{"team_publication_receipts", "team_publication_receipt_payloads", "team_object_registry", "team_memory_capsules", "team_projection_jobs"} {
		var count int
		if err := fixture.store.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("changed envelope mutated %s count=%d err=%v", table, count, err)
		}
	}
	var consumed any
	if err := fixture.store.DB().QueryRow(`
		SELECT consumed_at FROM team_publication_approvals
		 WHERE nonce_hash = ?`, teamPublicationApprovalNonceHash(challenge.Nonce),
	).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != nil {
		t.Fatal("failed remote commit burned publication approval")
	}
}

func TestApprovedTeamPublicationCanBeDeletedWithoutLosingContentFreeReceipt(t *testing.T) {
	fixture := prepareRemotePublicationFixture(t)
	defer fixture.store.Close()
	ownerClientID := "publication-owner-delete-agent"
	ownerBinding, err := fixture.store.RegisterAgentBinding(context.Background(), RegisterAgentBindingRequest{
		ActorPrincipalID: fixture.bootstrap.OwnerPrincipalID,
		Issuer:           testBootstrapRoot().Issuer, Subject: testBootstrapRoot().Subject,
		ClientID: ownerClientID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GrantProjectAccess(context.Background(), GrantProjectAccessRequest{
		ActorPrincipalID: fixture.bootstrap.OwnerPrincipalID, ProjectID: fixture.project.ProjectID,
		TargetPrincipalID: ownerBinding.AgentPrincipalID, AccessLevel: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	ownerClientKey := teamauth.OAuthClientKey(testBootstrapRoot().Issuer, ownerClientID)
	const publishedContent = "Use the synthetic publication deletion rule."
	envelope, digest := canonicalRemotePublicationEnvelope(
		t, fixture, publishedContent, "publication_remote_delete_01",
	)
	challenge := approveRemotePublication(t, fixture, envelope, digest)
	receipt, err := fixture.store.CommitApprovedTeamPublication(context.Background(), ApprovedTeamPublicationRequest{
		CanonicalEnvelope: envelope, EnvelopeDigest: digest, ApprovalNonce: challenge.Nonce,
		RequestID:                 "request-publication-delete-0001",
		Writer:                    TeamWriterLeaseIdentity{WriterID: fixture.lease.WriterID, Token: fixture.lease.Token},
		ApprovingOwnerPrincipalID: fixture.bootstrap.OwnerPrincipalID,
		ApprovingClientKey:        fixture.issue.ApprovingClientKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := fixture.store.StartTeamDeletion(context.Background(), TeamDeletionStartRequest{
		Authorization: TeamMutationAuthorizationRequest{
			PrincipalID: ownerBinding.AgentPrincipalID, OAuthClientKey: ownerClientKey,
			Action: teamauth.ActionDelete, Capabilities: []teamauth.Capability{teamauth.CapabilityDelete},
			Context:          teamauth.ActiveContext{TeamID: fixture.bootstrap.TeamID, ProjectID: fixture.project.ProjectID},
			ExistingObjectID: receipt.ObjectID,
		},
		Writer:    TeamWriterLeaseIdentity{WriterID: fixture.lease.WriterID, Token: fixture.lease.Token},
		RequestID: "request-publication-delete-start-0001", IdempotencyKey: "publication-delete-0001",
	})
	if err != nil {
		t.Fatalf("start published root deletion: %v", err)
	}
	claims, err := fixture.store.ClaimTeamDeletionJobs(context.Background(), TeamDeletionClaimRequest{
		WriterID: fixture.lease.WriterID, WriterToken: fixture.lease.Token,
		Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim published root deletion=%+v err=%v", claims, err)
	}
	if _, err := fixture.store.CompleteTeamDeletion(context.Background(), TeamDeletionCompletionRequest{
		WriterID: fixture.lease.WriterID, WriterToken: fixture.lease.Token,
		OperationID: started.OperationID, LeaseToken: claims[0].LeaseToken,
	}); err != nil {
		t.Fatalf("complete published root deletion: %v", err)
	}

	var publications, payloads, capsules, jobs int
	var storedEnvelopeDigest, storedReceiptDigest, storedObjectID string
	if err := fixture.store.DB().QueryRow(`
		SELECT envelope_digest, receipt_digest, object_id
		  FROM team_publication_receipts WHERE publication_id = ?`, receipt.PublicationID,
	).Scan(&storedEnvelopeDigest, &storedReceiptDigest, &storedObjectID); err != nil {
		t.Fatal(err)
	}
	for table, destination := range map[string]*int{
		"team_publication_receipts":         &publications,
		"team_publication_receipt_payloads": &payloads,
		"team_memory_capsules":              &capsules,
		"team_projection_jobs":              &jobs,
	} {
		if err := fixture.store.DB().QueryRow("SELECT count(*) FROM " + table).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if publications != 1 || payloads != 0 || capsules != 0 || jobs != 0 ||
		storedEnvelopeDigest != receipt.EnvelopeDigest || storedReceiptDigest != receipt.ReceiptDigest ||
		storedObjectID != receipt.ObjectID {
		t.Fatalf("post-delete publication evidence publications=%d payloads=%d capsules=%d jobs=%d envelope=%q receipt=%q object=%q",
			publications, payloads, capsules, jobs, storedEnvelopeDigest, storedReceiptDigest, storedObjectID)
	}
	var leaked int
	if err := fixture.store.DB().QueryRow(`
		SELECT count(*) FROM team_publication_receipt_payloads
		 WHERE instr(envelope_json, ?) > 0`, publishedContent,
	).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("published envelope content remains after deletion=%d err=%v", leaked, err)
	}

	filter, err := fixture.store.BuildAuthorizedCandidateFilter(context.Background(), CandidateFilterRequest{
		PrincipalID:  fixture.publisher.binding.AgentPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context:      teamauth.ActiveContext{TeamID: fixture.bootstrap.TeamID, ProjectID: fixture.project.ProjectID},
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := fixture.store.LookupTeamPublicationReceipt(context.Background(), TeamPublicationReceiptLookup{
		Filter: filter, PublicationKey: "publication_remote_delete_01", EnvelopeDigest: digest,
	})
	if err != nil {
		t.Fatalf("reconcile content-free receipt after completed root deletion: %v", err)
	}
	if reconciled.PublicationID != receipt.PublicationID || reconciled.ObjectID != receipt.ObjectID ||
		reconciled.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("reconciled deleted publication receipt = %+v, want %+v", reconciled, receipt)
	}
	grant, err := fixture.store.ResolveProjectGrant(
		context.Background(), fixture.project.ProjectID, fixture.publisher.binding.AgentPrincipalID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.RevokeProjectGrant(
		context.Background(), fixture.bootstrap.OwnerPrincipalID, grant.GrantID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.LookupTeamPublicationReceipt(context.Background(), TeamPublicationReceiptLookup{
		Filter: filter, PublicationKey: "publication_remote_delete_01", EnvelopeDigest: digest,
	}); !errors.Is(err, ErrTeamPolicyEpochChanged) && !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("stale authorized filter read receipt after project revoke: %v", err)
	}
	freshFilter, err := fixture.store.BuildAuthorizedCandidateFilter(context.Background(), CandidateFilterRequest{
		PrincipalID:  fixture.publisher.binding.AgentPrincipalID,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context:      teamauth.ActiveContext{TeamID: fixture.bootstrap.TeamID, ProjectID: fixture.project.ProjectID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.LookupTeamPublicationReceipt(context.Background(), TeamPublicationReceiptLookup{
		Filter: freshFilter, PublicationKey: "publication_remote_delete_01", EnvelopeDigest: digest,
	}); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("fresh publisher filter read receipt after project revoke: %v", err)
	}
}

func TestTeamPublicationTargetIsPinnedAndSyntheticGateRejectsRealEnvelope(t *testing.T) {
	fixture := prepareRemotePublicationFixture(t)
	defer fixture.store.Close()
	if err := fixture.store.ConfigureTeamPublicationTarget(TeamPublicationTarget{
		DeploymentID: "deployment_other", ProjectID: fixture.project.ProjectID, SyntheticOnly: true,
	}); !errors.Is(err, ErrTeamPublicationTargetMismatch) {
		t.Fatalf("retarget error=%v", err)
	}
	envelope, _ := canonicalRemotePublicationEnvelope(t, fixture, "Use the reviewed team rule.", "publication_remote_03")
	realEnvelope := []byte(strings.Replace(string(envelope), `"tags":["pilot","synthetic"]`, `"tags":["pilot"]`, 1))
	realDigest := sha256.Sum256(realEnvelope)
	_, err := fixture.store.BuildTeamPublicationApprovalDraft(context.Background(), TeamPublicationApprovalDraftRequest{
		CanonicalEnvelope:         realEnvelope,
		EnvelopeDigest:            fmt.Sprintf("%x", realDigest),
		Writer:                    TeamWriterLeaseIdentity{WriterID: fixture.lease.WriterID, Token: fixture.lease.Token},
		ApprovingOwnerPrincipalID: fixture.bootstrap.OwnerPrincipalID,
		ApprovingClientKey:        fixture.issue.ApprovingClientKey,
	})
	if !errors.Is(err, ErrTeamPublicationSyntheticOnly) {
		t.Fatalf("real publication under synthetic-only gate error=%v", err)
	}
}
