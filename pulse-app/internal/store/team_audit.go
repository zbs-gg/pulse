package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

var ErrTeamAuditInvalid = errors.New("team_audit_invalid")

const (
	teamObjectWriteAction  = "team.object.write"
	teamObjectStoredReason = "object_stored"
)

// teamDomainAuditEvent is intentionally a closed, metadata-free shape. Domain
// writers can attribute an opaque state transition, but they cannot pass raw
// request content, prompts, transcripts, paths, credentials, or arbitrary
// metadata into the durable audit log.
type teamDomainAuditEvent struct {
	StoreID         string
	TeamID          string
	ProjectID       string
	ActorPrincipal  string
	OAuthClientKey  string
	RequestID       string
	Action          string
	TargetKind      string
	TargetID        string
	PolicyVersion   int
	AuthorizationAt int64
	ReasonCode      string
	OccurredAt      time.Time
}

// appendTeamDomainAudit appends one ordered, content-free event and returns
// the exact durable ID. The migration's AFTER INSERT trigger assigns the
// strictly increasing audit sequence in this same transaction.
func appendTeamDomainAudit(ctx context.Context, tx *sql.Tx, event teamDomainAuditEvent) (string, error) {
	if tx == nil || !validTeamDomainAuditEvent(event) {
		return "", ErrTeamAuditInvalid
	}
	eventID, err := newOpaqueID("audit")
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_audit_events(
			event_id, store_id, occurred_at, action, outcome, actor_principal_id,
			client_key, team_id, project_id, target_kind, target_id, request_id,
			policy_version, mode, auth_epoch, reason_code, metadata_json)
		VALUES (?, ?, ?, ?, 'allowed', ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?,
		        'team-remote', ?, ?, '{}')`,
		eventID, event.StoreID, event.OccurredAt.UTC().Format(time.RFC3339Nano),
		event.Action, event.ActorPrincipal, event.OAuthClientKey, event.TeamID,
		event.ProjectID, event.TargetKind, event.TargetID, event.RequestID,
		event.PolicyVersion, event.AuthorizationAt, event.ReasonCode,
	)
	if err != nil {
		return "", err
	}
	return eventID, nil
}

func validTeamDomainAuditEvent(event teamDomainAuditEvent) bool {
	if event.Action != teamObjectWriteAction || event.ReasonCode != teamObjectStoredReason {
		return false
	}
	return validTeamOpaque(event.StoreID, 1, 255) && validTeamOpaque(event.TeamID, 1, 255) &&
		validTeamOpaque(event.ActorPrincipal, 1, 255) && lowerHexDigest(event.OAuthClientKey) &&
		validTeamOpaque(event.RequestID, 8, 64) && validTeamClass(event.TargetKind, 64) &&
		validTeamOpaque(event.TargetID, 1, 255) &&
		(event.ProjectID == "" || validTeamOpaque(event.ProjectID, 1, 255)) &&
		event.PolicyVersion == teamauth.PolicyVersion && event.AuthorizationAt > 0 && !event.OccurredAt.IsZero()
}
