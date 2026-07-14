package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type TeamStatusMetadata struct {
	StoreID                string
	TeamID                 string
	PolicyVersion          int
	SchemaVersion          int
	AuthEpoch              int64
	PolicyEpoch            int64
	RealContentState       string
	ActivationState        string
	PublicEnabled          bool
	PrincipalID            string
	PrincipalKind          string
	HumanPrincipalID       string
	BindingID              string
	MembershipID           string
	MembershipRole         string
	PrincipalAuthEpoch     int64
	BindingAuthEpoch       int64
	MembershipAuthEpoch    int64
	ProjectionPending      bool
	ProjectionFailed       bool
	ProjectionWorkerState  string
	ProjectionWorkerReason string
	ProjectionHeartbeatAt  *time.Time
}

type TeamObjectMetadata struct {
	ObjectID          string
	ObjectKind        string
	TeamID            string
	ScopeType         string
	ScopeID           string
	OwnerPrincipalID  string
	AuthorPrincipalID string
	PrivacyTier       string
	Retention         string
	Lifecycle         string
	Generation        int64
	ProjectionState   string
	DeletionState     string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ExpiresAt         *time.Time
}

type TeamAuditMetadata struct {
	EventID          string
	OccurredAt       time.Time
	Action           string
	Outcome          string
	ActorPrincipalID string
	ClientKey        string
	TeamID           string
	ProjectID        string
	TargetKind       string
	TargetID         string
	RequestID        string
	PolicyVersion    int
	AuthEpoch        int64
	ReasonCode       string
}

type TeamAuditPage struct {
	Events     []TeamAuditMetadata
	NextCursor string
}

// ReadTeamStatusMetadata returns only the marked store and current opaque
// identity state. Effective capabilities and active request context are added
// by the server from its already-verified principal assertion.
func (s *Store) ReadTeamStatusMetadata(ctx context.Context, principalID string) (TeamStatusMetadata, error) {
	principal, err := s.ResolveTeamPrincipal(ctx, principalID)
	if err != nil {
		return TeamStatusMetadata{}, err
	}
	policy, err := readTeamPolicyState(ctx, s.db)
	if err != nil {
		return TeamStatusMetadata{}, err
	}
	activation, err := s.ReadTeamActivationState(ctx)
	if err != nil {
		return TeamStatusMetadata{}, err
	}
	if principal.StoreID != policy.StoreID || principal.TeamID != policy.TeamID ||
		activation.StoreID != policy.StoreID || activation.TeamID != policy.TeamID {
		return TeamStatusMetadata{}, ErrTeamStoreIdentityMismatch
	}
	result := TeamStatusMetadata{
		StoreID: policy.StoreID, TeamID: policy.TeamID,
		PolicyVersion: policy.PolicyVersion, SchemaVersion: policy.SchemaVersion,
		AuthEpoch: policy.GlobalEpoch, PolicyEpoch: policy.PolicyEpoch,
		RealContentState: policy.RealContentState,
		ActivationState:  activation.ActivationState, PublicEnabled: activation.PublicEnabled,
		PrincipalID: principal.PrincipalID, PrincipalKind: principal.Kind,
		HumanPrincipalID: principal.HumanPrincipalID, BindingID: principal.BindingID,
		MembershipID: principal.MembershipID, MembershipRole: principal.MembershipRole,
		PrincipalAuthEpoch: principal.PrincipalEpoch, BindingAuthEpoch: principal.BindingEpoch,
		MembershipAuthEpoch: principal.MembershipEpoch,
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
		           SELECT 1 FROM team_projection_jobs
		            WHERE store_id = ? AND team_id = ? AND state IN ('pending', 'leased')
		       ),
		       EXISTS(
		           SELECT 1 FROM team_projection_jobs
		            WHERE store_id = ? AND team_id = ? AND state = 'failed'
		       )`, policy.StoreID, policy.TeamID, policy.StoreID, policy.TeamID).
		Scan(&result.ProjectionPending, &result.ProjectionFailed); err != nil {
		return TeamStatusMetadata{}, err
	}
	workerHealth, err := s.ReadTeamProjectionWorkerHealth(ctx)
	if err != nil {
		return TeamStatusMetadata{}, err
	}
	result.ProjectionWorkerState = workerHealth.State
	result.ProjectionWorkerReason = workerHealth.Reason
	result.ProjectionHeartbeatAt = workerHealth.HeartbeatAt
	current, err := s.ResolveTeamPrincipal(ctx, principalID)
	if err != nil {
		return TeamStatusMetadata{}, err
	}
	if current.TeamEpoch != principal.TeamEpoch || current.PrincipalEpoch != principal.PrincipalEpoch ||
		current.MembershipEpoch != principal.MembershipEpoch || current.BindingEpoch != principal.BindingEpoch {
		return TeamStatusMetadata{}, ErrTeamPolicyEpochChanged
	}
	return result, nil
}

// CheckTeamProjectionQueueReadiness is the operational readiness gate used by
// the daemon's public /ready endpoint. Protected requests retain their bounded
// policy gate so status can still explain projection lag while workers catch up.
func (s *Store) CheckTeamProjectionQueueReadiness(ctx context.Context) error {
	info, err := readTeamStoreInfo(ctx, s.db)
	if err != nil {
		return err
	}
	var lagging bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM team_projection_jobs
		     WHERE store_id = ? AND team_id = ?
		       AND state IN ('pending', 'leased', 'failed')
		)`, info.StoreID, info.TeamID).Scan(&lagging); err != nil {
		return err
	}
	if lagging {
		return ErrTeamProjectionQueueLagging
	}
	health, err := s.ReadTeamProjectionWorkerHealth(ctx)
	if err != nil {
		return err
	}
	return projectionWorkerHealthError(health)
}

// InspectAuthorizedTeamObject applies the same pre-candidate policy predicate
// as retrieval and returns no content-bearing representation or local path.
func (s *Store) InspectAuthorizedTeamObject(ctx context.Context, request CandidateFilterRequest, objectID string) (TeamObjectMetadata, error) {
	if !validOwnerOpaque(objectID, 255) {
		return TeamObjectMetadata{}, ErrConcealedNotFound
	}
	filter, err := s.BuildAuthorizedCandidateFilter(ctx, request)
	if err != nil {
		return TeamObjectMetadata{}, err
	}
	predicate, args, err := filter.SQLPredicate("object")
	if err != nil {
		return TeamObjectMetadata{}, err
	}
	args = append([]any{objectID}, args...)
	var result TeamObjectMetadata
	var ownerID string
	var expiresText sql.NullString
	var createdText, updatedText string
	err = s.db.QueryRowContext(ctx, `
		SELECT object.object_id, object.object_kind, object.team_id,
		       object.scope_type, object.scope_id, COALESCE(object.owner_principal_id, ''),
		       object.author_principal_id, object.privacy_tier, object.retention,
		       object.lifecycle, object.generation, object.created_at, object.updated_at,
		       object.expires_at,
		       CASE
		         WHEN NOT EXISTS (
		              SELECT 1 FROM team_projection_jobs job
		               WHERE job.root_object_id = object.object_id
		                 AND job.root_generation = object.generation
		         ) THEN 'none'
		         WHEN EXISTS (
		              SELECT 1 FROM team_projection_jobs job
		               WHERE job.root_object_id = object.object_id
		                 AND job.root_generation = object.generation AND job.state = 'failed'
		         ) THEN 'failed'
		         WHEN EXISTS (
		              SELECT 1 FROM team_projection_jobs job
		               WHERE job.root_object_id = object.object_id
		                 AND job.root_generation = object.generation AND job.state = 'leased'
		         ) THEN 'leased'
		         WHEN EXISTS (
		              SELECT 1 FROM team_projection_jobs job
		               WHERE job.root_object_id = object.object_id
		                 AND job.root_generation = object.generation AND job.state = 'pending'
		         ) THEN 'pending'
		         WHEN NOT EXISTS (
		              SELECT 1 FROM team_projection_jobs job
		               WHERE job.root_object_id = object.object_id
		                 AND job.root_generation = object.generation AND job.state <> 'ready'
		         ) THEN 'ready'
		         ELSE 'cancelled'
		       END,
		       COALESCE((
		           SELECT operation.state FROM team_deletion_operations operation
		            WHERE operation.root_object_id = object.object_id
		            ORDER BY operation.started_at DESC LIMIT 1
		       ), '')
		  FROM team_object_registry object
		 WHERE object.object_id = ? AND `+predicate, args...).Scan(
		&result.ObjectID, &result.ObjectKind, &result.TeamID, &result.ScopeType,
		&result.ScopeID, &ownerID, &result.AuthorPrincipalID, &result.PrivacyTier,
		&result.Retention, &result.Lifecycle, &result.Generation, &createdText,
		&updatedText, &expiresText, &result.ProjectionState, &result.DeletionState,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamObjectMetadata{}, ErrConcealedNotFound
	}
	if err != nil {
		return TeamObjectMetadata{}, err
	}
	result.OwnerPrincipalID = ownerID
	if result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdText); err != nil {
		return TeamObjectMetadata{}, ErrTeamPolicyNotReady
	}
	if result.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedText); err != nil {
		return TeamObjectMetadata{}, ErrTeamPolicyNotReady
	}
	if expiresText.Valid {
		expires, parseErr := time.Parse(time.RFC3339Nano, expiresText.String)
		if parseErr != nil {
			return TeamObjectMetadata{}, ErrTeamPolicyNotReady
		}
		result.ExpiresAt = &expires
	}
	if err := s.RecheckAuthorizedTeamObjectAccess(ctx, filter, objectID); err != nil {
		return TeamObjectMetadata{}, err
	}
	return result, nil
}

// ReadOwnTeamAudit exposes only events attributed to the exact current
// principal. The cursor is the last random event ID, not the global audit
// sequence, so gaps cannot reveal the number of other principals' events.
func (s *Store) ReadOwnTeamAudit(ctx context.Context, principalID, cursor string, limit int) (TeamAuditPage, error) {
	if !validOwnerOpaque(principalID, 255) || (cursor != "" && !validOwnerOpaque(cursor, 255)) {
		return TeamAuditPage{}, ErrConcealedNotFound
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return TeamAuditPage{}, ErrTeamPolicyDenied
	}
	principal, err := s.ResolveTeamPrincipal(ctx, principalID)
	if err != nil {
		return TeamAuditPage{}, err
	}
	beforeSequence := int64(0)
	if cursor != "" {
		err := s.db.QueryRowContext(ctx, `
			SELECT ordered.audit_sequence
			  FROM team_audit_event_order ordered
			  JOIN team_audit_events audit ON audit.event_id = ordered.event_id
			 WHERE audit.event_id = ? AND audit.team_id = ?
			   AND audit.actor_principal_id = ?`, cursor, principal.TeamID, principalID).
			Scan(&beforeSequence)
		if errors.Is(err, sql.ErrNoRows) {
			return TeamAuditPage{}, ErrConcealedNotFound
		}
		if err != nil {
			return TeamAuditPage{}, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT ordered.audit_sequence, audit.event_id, audit.occurred_at,
		       audit.action, audit.outcome, COALESCE(audit.actor_principal_id, ''),
		       COALESCE(audit.client_key, ''), audit.team_id,
		       COALESCE(audit.project_id, ''), audit.target_kind,
		       COALESCE(audit.target_id, ''), COALESCE(audit.request_id, ''),
		       audit.policy_version, audit.auth_epoch, audit.reason_code
		  FROM team_audit_event_order ordered
		  JOIN team_audit_events audit ON audit.event_id = ordered.event_id
		 WHERE audit.team_id = ? AND audit.actor_principal_id = ?
		   AND (? = 0 OR ordered.audit_sequence < ?)
		 ORDER BY ordered.audit_sequence DESC
		 LIMIT ?`, principal.TeamID, principalID, beforeSequence, beforeSequence, limit+1)
	if err != nil {
		return TeamAuditPage{}, err
	}
	defer rows.Close()
	events := make([]TeamAuditMetadata, 0, limit+1)
	for rows.Next() {
		var sequence int64
		var occurredText string
		var event TeamAuditMetadata
		if err := rows.Scan(
			&sequence, &event.EventID, &occurredText, &event.Action, &event.Outcome,
			&event.ActorPrincipalID, &event.ClientKey, &event.TeamID, &event.ProjectID,
			&event.TargetKind, &event.TargetID, &event.RequestID, &event.PolicyVersion,
			&event.AuthEpoch, &event.ReasonCode,
		); err != nil {
			return TeamAuditPage{}, err
		}
		event.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredText)
		if err != nil {
			return TeamAuditPage{}, ErrTeamPolicyNotReady
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return TeamAuditPage{}, err
	}
	page := TeamAuditPage{Events: events}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		page.NextCursor = page.Events[len(page.Events)-1].EventID
	}
	current, err := s.ResolveTeamPrincipal(ctx, principalID)
	if err != nil {
		return TeamAuditPage{}, err
	}
	if current.TeamEpoch != principal.TeamEpoch || current.PrincipalEpoch != principal.PrincipalEpoch ||
		current.MembershipEpoch != principal.MembershipEpoch || current.BindingEpoch != principal.BindingEpoch {
		return TeamAuditPage{}, ErrTeamPolicyEpochChanged
	}
	return page, nil
}
