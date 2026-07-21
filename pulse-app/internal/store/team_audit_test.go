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

func TestTeamObjectDomainAuditIsOrderedContentFreeAndReplayStable(t *testing.T) {
	ctx := context.Background()
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()

	rawKey := "idempotency-RAW-credential-like-value-9001"
	rawBody := []byte("private synthetic prompt /Users/example/secret transcript phrase")
	digest := sha256.Sum256(rawBody)
	f.request.IdempotencyKey = rawKey
	f.request.Body = rawBody
	f.request.BodyDigest = fmt.Sprintf("%x", digest)
	first, err := f.store.StoreTeamObject(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := cloneTeamObjectWriteRequest(f.request)
	secondRequest.RequestID = "request-object-spine-0002"
	secondRequest.IdempotencyKey = "idempotency-object-spine-0002"
	secondRequest.Body = []byte("second private body never written to spine tables")
	secondDigest := sha256.Sum256(secondRequest.Body)
	secondRequest.BodyDigest = fmt.Sprintf("%x", secondDigest)
	second, err := f.store.StoreTeamObject(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}

	var firstSequence, secondSequence int64
	if err := f.store.DB().QueryRow(`SELECT audit_sequence FROM team_audit_event_order WHERE event_id = ?`, first.AuditEventID).Scan(&firstSequence); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().QueryRow(`SELECT audit_sequence FROM team_audit_event_order WHERE event_id = ?`, second.AuditEventID).Scan(&secondSequence); err != nil {
		t.Fatal(err)
	}
	if firstSequence >= secondSequence {
		t.Fatalf("audit order = %d then %d", firstSequence, secondSequence)
	}

	var action, outcome, actor, clientKey, teamID, targetKind, targetID, requestID, mode, reason, metadata string
	var projectID *string
	var policyVersion int
	var authEpoch int64
	if err := f.store.DB().QueryRow(`
		SELECT action, outcome, actor_principal_id, client_key, team_id, project_id,
		       target_kind, target_id, request_id, policy_version, mode, auth_epoch,
		       reason_code, metadata_json
		  FROM team_audit_events WHERE event_id = ?`, first.AuditEventID).Scan(
		&action, &outcome, &actor, &clientKey, &teamID, &projectID, &targetKind,
		&targetID, &requestID, &policyVersion, &mode, &authEpoch, &reason, &metadata,
	); err != nil {
		t.Fatal(err)
	}
	if action != teamObjectWriteAction || outcome != "allowed" || actor != f.actor.binding.AgentPrincipalID ||
		clientKey != f.actor.clientKey || teamID != f.bootstrap.TeamID || projectID != nil ||
		targetKind != f.permit.ObjectKind() || targetID != first.ObjectID || requestID != f.request.RequestID ||
		policyVersion != 1 || mode != "team-remote" || authEpoch != f.permit.PolicyEpoch().Global ||
		reason != teamObjectStoredReason || metadata != "{}" {
		t.Fatalf("domain audit mismatch: action=%q outcome=%q actor=%q client=%q team=%q project=%v target=%q/%q request=%q policy=%d mode=%q epoch=%d reason=%q metadata=%q",
			action, outcome, actor, clientKey, teamID, projectID, targetKind, targetID,
			requestID, policyVersion, mode, authEpoch, reason, metadata)
	}

	var persistedText string
	if err := f.store.DB().QueryRow(`
		SELECT group_concat(value, '|') FROM (
			SELECT object_id || '|' || object_kind || '|' || scope_type || '|' || scope_id AS value
			  FROM team_object_registry WHERE object_id = ?
			UNION ALL
			SELECT idempotency_key_hash || '|' || body_digest || '|' || action || '|' || state
			  FROM team_idempotency_records WHERE object_id = ?
			UNION ALL
			SELECT event_id || '|' || action || '|' || target_kind || '|' || target_id || '|' || metadata_json
			  FROM team_audit_events WHERE event_id = ?
			UNION ALL
			SELECT job_id || '|' || projection_kind || '|' || state
			  FROM team_projection_jobs WHERE root_object_id = ?
		)`, first.ObjectID, first.ObjectID, first.AuditEventID, first.ObjectID).Scan(&persistedText); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{rawKey, string(rawBody), "private synthetic prompt", "/Users/example/secret", "transcript phrase"} {
		if strings.Contains(persistedText, forbidden) {
			t.Fatalf("content or raw key leaked into spine/audit rows: %q", forbidden)
		}
	}
	if strings.Contains(strings.ToLower(persistedText), "ready") {
		t.Fatalf("write path reported a successful ready state: %s", persistedText)
	}

	replay, err := f.store.StoreTeamObject(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.AuditEventID != first.AuditEventID {
		t.Fatalf("audit replay = %+v, want %s", replay, first.AuditEventID)
	}
	var auditCount, orderCount int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM team_audit_events WHERE action = ?`, teamObjectWriteAction).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB().QueryRow(`
		SELECT count(*) FROM team_audit_event_order ordered
		JOIN team_audit_events audit ON audit.event_id = ordered.event_id
		WHERE audit.action = ?`, teamObjectWriteAction).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 || orderCount != 2 {
		t.Fatalf("replay duplicated audit: events=%d order=%d", auditCount, orderCount)
	}
	if _, err := f.store.DB().Exec(`UPDATE team_audit_events SET reason_code = 'changed' WHERE event_id = ?`, first.AuditEventID); err == nil {
		t.Fatal("domain audit update succeeded")
	}
	if _, err := f.store.DB().Exec(`DELETE FROM team_audit_events WHERE event_id = ?`, first.AuditEventID); err == nil {
		t.Fatal("domain audit delete succeeded")
	}

	// Audit attribution uses opaque values rather than foreign keys to mutable
	// domain rows. Removing the synthetic object's operational rows therefore
	// cannot erase or orphan its durable audit evidence.
	if _, err := f.store.DB().Exec(`DELETE FROM team_projection_jobs WHERE root_object_id = ?`, first.ObjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().Exec(`DELETE FROM team_idempotency_records WHERE object_id = ?`, first.ObjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().Exec(`DELETE FROM team_object_registry WHERE object_id = ?`, first.ObjectID); err != nil {
		t.Fatal(err)
	}
	var survivingTarget string
	if err := f.store.DB().QueryRow(`SELECT target_id FROM team_audit_events WHERE event_id = ?`, first.AuditEventID).Scan(&survivingTarget); err != nil {
		t.Fatalf("audit did not survive object removal: %v", err)
	}
	if survivingTarget != first.ObjectID {
		t.Fatalf("surviving audit target = %q, want %q", survivingTarget, first.ObjectID)
	}
}

func TestAppendTeamDomainAuditRejectsFreeFormOrContentBearingFields(t *testing.T) {
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()
	base := teamDomainAuditEvent{
		StoreID: f.bootstrap.StoreID, TeamID: f.bootstrap.TeamID,
		ActorPrincipal: f.actor.binding.AgentPrincipalID, OAuthClientKey: f.actor.clientKey,
		RequestID: "request-audit-validation", Action: teamObjectWriteAction,
		TargetKind: "memory", TargetID: "object_synthetic",
		PolicyVersion: teamauth.PolicyVersion, AuthorizationAt: 1,
		ReasonCode: teamObjectStoredReason, OccurredAt: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name   string
		mutate func(*teamDomainAuditEvent)
	}{
		{name: "free form action", mutate: func(event *teamDomainAuditEvent) { event.Action = "team.object.write raw transcript" }},
		{name: "free form reason", mutate: func(event *teamDomainAuditEvent) { event.ReasonCode = "object_stored:/Users/private" }},
		{name: "content target", mutate: func(event *teamDomainAuditEvent) { event.TargetID = "private prompt contents" }},
		{name: "content request", mutate: func(event *teamDomainAuditEvent) { event.RequestID = "request-ok\nsecret" }},
		{name: "raw client", mutate: func(event *teamDomainAuditEvent) { event.OAuthClientKey = "client-secret" }},
		{name: "unsafe identity", mutate: func(event *teamDomainAuditEvent) { event.ActorPrincipal = "../../principal" }},
		{name: "wrong action reason pair", mutate: func(event *teamDomainAuditEvent) { event.Action = "team.object.delete" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := base
			test.mutate(&event)
			tx, err := f.store.DB().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := appendTeamDomainAudit(context.Background(), tx, event); !errors.Is(err, ErrTeamAuditInvalid) {
				t.Fatalf("invalid audit error = %v", err)
			}
		})
	}
	var events int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM team_audit_events WHERE action = ?`, teamObjectWriteAction).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("invalid audit attempts persisted %d events", events)
	}
}
