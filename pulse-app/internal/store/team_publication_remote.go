package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

var (
	ErrTeamPublicationTargetRequired = errors.New("team_publication_target_required")
	ErrTeamPublicationTargetMismatch = errors.New("team_publication_target_mismatch")
	ErrTeamPublicationSyntheticOnly  = errors.New("team_publication_synthetic_only")
	ErrTeamPublicationReceiptMissing = errors.New("team_publication_receipt_missing")
)

// TeamPublicationTarget is immutable process configuration, supplied by the
// dedicated Team deployment rather than an agent request. It pins every
// publication to one deployment and one Commons project.
type TeamPublicationTarget struct {
	DeploymentID  string
	ProjectID     string
	SyntheticOnly bool
}

type TeamPublicationApprovalDraftRequest struct {
	CanonicalEnvelope         []byte
	EnvelopeDigest            string
	Writer                    TeamWriterLeaseIdentity
	ApprovingOwnerPrincipalID string
	ApprovingClientKey        string
}

type ApprovedTeamPublicationRequest struct {
	CanonicalEnvelope         []byte
	EnvelopeDigest            string
	ApprovalNonce             string
	RequestID                 string
	Writer                    TeamWriterLeaseIdentity
	ApprovingOwnerPrincipalID string
	ApprovingClientKey        string
}

type TeamPublicationReceiptLookup struct {
	Filter         AuthorizedCandidateFilter
	PublicationKey string
	EnvelopeDigest string
}

type TeamPublicationReceipt struct {
	PublicationID             string
	DeploymentID              string
	StoreID                   string
	TeamID                    string
	SharedProjectID           string
	EnvelopeDigest            string
	OperationDigest           string
	PublisherPrincipalID      string
	PublisherMembershipID     string
	PublisherClientKey        string
	PublisherBindingID        string
	ApprovingOwnerPrincipalID string
	ApprovalAuditEventID      string
	ObjectID                  string
	CapsuleID                 string
	ObjectAuditEventID        string
	EventProjectionJobID      string
	EmbeddingProjectionJobID  string
	ReceiptDigest             string
	CreatedAt                 time.Time
	Replayed                  bool
}

type normalizedTeamPublicationRemote struct {
	target      TeamPublicationTarget
	envelope    teamPublicationEnvelope
	canonical   []byte
	digest      string
	permit      TeamMutationPermit
	memory      normalizedTeamMemoryWrite
	object      TeamObjectWriteRequest
	objectWrite normalizedTeamObjectWrite
	issue       TeamPublicationApprovalIssueRequest
}

// ConfigureTeamPublicationTarget pins the sole remote publication destination
// before any Airlock route is exposed. Reconfiguration to a different target
// is refused for the lifetime of the process.
func (s *Store) ConfigureTeamPublicationTarget(target TeamPublicationTarget) error {
	if s == nil || s.storeKind != StoreKindCommons ||
		!validPublicationID(target.DeploymentID, "deployment_") ||
		!validPublicationID(target.ProjectID, "project_") {
		return ErrTeamPublicationTargetMismatch
	}
	info, err := readTeamStoreInfo(context.Background(), s.db)
	if err != nil {
		return err
	}
	var projects int
	if err := s.db.QueryRow(`
		SELECT count(*) FROM team_projects
		 WHERE project_id = ? AND team_id = ?`, target.ProjectID, info.TeamID,
	).Scan(&projects); err != nil {
		return err
	}
	if projects != 1 {
		return ErrTeamPublicationTargetMismatch
	}
	s.publicationTargetMu.Lock()
	defer s.publicationTargetMu.Unlock()
	if s.publicationTarget != nil {
		if *s.publicationTarget != target {
			return ErrTeamPublicationTargetMismatch
		}
		return nil
	}
	configured := target
	s.publicationTarget = &configured
	return nil
}

func (s *Store) configuredTeamPublicationTarget() (TeamPublicationTarget, error) {
	if s == nil || s.storeKind != StoreKindCommons {
		return TeamPublicationTarget{}, ErrTeamPublicationTargetRequired
	}
	s.publicationTargetMu.RLock()
	defer s.publicationTargetMu.RUnlock()
	if s.publicationTarget == nil {
		return TeamPublicationTarget{}, ErrTeamPublicationTargetRequired
	}
	return *s.publicationTarget, nil
}

// BuildTeamPublicationApprovalDraft derives every publisher, membership,
// binding, policy and idempotency fact from the Commons store. The privileged
// UI adds only its fresh OS-backed assertion and TTL before issuing approval.
func (s *Store) BuildTeamPublicationApprovalDraft(
	ctx context.Context,
	request TeamPublicationApprovalDraftRequest,
) (TeamPublicationApprovalIssueRequest, error) {
	normalized, err := s.normalizeRemoteTeamPublication(ctx, request.CanonicalEnvelope,
		request.EnvelopeDigest, request.Writer, request.ApprovingOwnerPrincipalID,
		request.ApprovingClientKey, "request-publication-draft")
	if err != nil {
		return TeamPublicationApprovalIssueRequest{}, err
	}
	return normalized.issue, nil
}

// CommitApprovedTeamPublication creates the Commons root, single capsule,
// owner approval audit, object audit, two projection jobs and immutable receipt
// in one SQLite transaction. Response-loss retries return the original receipt.
func (s *Store) CommitApprovedTeamPublication(
	ctx context.Context,
	request ApprovedTeamPublicationRequest,
) (TeamPublicationReceipt, error) {
	if !validOwnerNonce(request.ApprovalNonce) || !validTeamOpaque(request.RequestID, 8, 64) {
		return TeamPublicationReceipt{}, ErrTeamPublicationInvalid
	}
	normalized, err := s.normalizeRemoteTeamPublication(ctx, request.CanonicalEnvelope,
		request.EnvelopeDigest, request.Writer, request.ApprovingOwnerPrincipalID,
		request.ApprovingClientKey, request.RequestID)
	if err != nil {
		return TeamPublicationReceipt{}, err
	}

	consume := TeamPublicationApprovalConsumeRequest{
		Nonce: request.ApprovalNonce, RequestID: request.RequestID,
		DeploymentID: normalized.issue.DeploymentID,
		StoreID:      normalized.issue.StoreID, TeamID: normalized.issue.TeamID,
		SharedProjectID:           normalized.issue.SharedProjectID,
		EnvelopeDigest:            normalized.issue.EnvelopeDigest,
		IdempotencyKeyHash:        normalized.issue.IdempotencyKeyHash,
		OperationDigest:           normalized.issue.OperationDigest,
		PublisherPrincipalID:      normalized.issue.PublisherPrincipalID,
		PublisherMembershipID:     normalized.issue.PublisherMembershipID,
		PublisherClientKey:        normalized.issue.PublisherClientKey,
		PublisherBindingID:        normalized.issue.PublisherBindingID,
		Writer:                    normalized.issue.Writer,
		ApprovingOwnerPrincipalID: normalized.issue.ApprovingOwnerPrincipalID,
		ApprovingClientKey:        normalized.issue.ApprovingClientKey,
		PolicyEpoch:               normalized.issue.PolicyEpoch, GlobalEpoch: normalized.issue.GlobalEpoch,
	}
	var consumption TeamPublicationApprovalConsumption
	var capsuleID string
	var created TeamPublicationReceipt
	root, err := s.storeTeamObjectWithHooks(ctx, normalized.object, teamObjectWriteHooks{
		RootOwnerPrincipalID: normalized.issue.ApprovingOwnerPrincipalID,
		BeforeCreate: func(ctx context.Context, tx *sql.Tx, _ normalizedTeamObjectWrite) error {
			info, err := readTeamStoreInfo(ctx, tx)
			if err != nil {
				return err
			}
			consumption, err = s.consumeTeamPublicationApprovalTx(ctx, tx, info, consume)
			return err
		},
		Content: func(ctx context.Context, transaction *teamObjectWriteTransaction) error {
			var err error
			capsuleID, err = newOpaqueID("team_capsule")
			if err != nil {
				return err
			}
			item := normalized.memory.write.Items[0]
			tags, err := json.Marshal(item.Tags)
			if err != nil {
				return err
			}
			if err := transaction.InsertTeamMemoryCapsuleItem(ctx, teamMemoryCapsuleStorageItem{
				CapsuleID: capsuleID, Ordinal: 0, Source: normalized.memory.write.Source,
				Item: item, TagsJSON: string(tags),
			}); err != nil {
				return err
			}
			return transaction.MapStorage(ctx, "memory_capsule", capsuleID)
		},
		AfterCreate: func(ctx context.Context, tx *sql.Tx, transaction *teamObjectWriteTransaction,
			objectAuditID string, jobs []TeamProjectionJobResult) error {
			var eventJobID, embeddingJobID string
			for _, job := range jobs {
				switch job.Kind {
				case "event":
					eventJobID = job.JobID
				case "embedding":
					embeddingJobID = job.JobID
				}
			}
			if eventJobID == "" || embeddingJobID == "" || capsuleID == "" || consumption.NonceHash == "" {
				return ErrTeamObjectCommitFailed
			}
			publicationID, err := newOpaqueID("publication")
			if err != nil {
				return err
			}
			createdAt := s.clock().UTC()
			created = TeamPublicationReceipt{
				PublicationID: publicationID, DeploymentID: normalized.issue.DeploymentID,
				StoreID: normalized.issue.StoreID, TeamID: normalized.issue.TeamID,
				SharedProjectID:           normalized.issue.SharedProjectID,
				EnvelopeDigest:            normalized.issue.EnvelopeDigest,
				OperationDigest:           normalized.issue.OperationDigest,
				PublisherPrincipalID:      normalized.issue.PublisherPrincipalID,
				PublisherMembershipID:     normalized.issue.PublisherMembershipID,
				PublisherClientKey:        normalized.issue.PublisherClientKey,
				PublisherBindingID:        normalized.issue.PublisherBindingID,
				ApprovingOwnerPrincipalID: normalized.issue.ApprovingOwnerPrincipalID,
				ApprovalAuditEventID:      consumption.AuditEventID,
				ObjectID:                  transaction.ObjectID, CapsuleID: capsuleID,
				ObjectAuditEventID:       objectAuditID,
				EventProjectionJobID:     eventJobID,
				EmbeddingProjectionJobID: embeddingJobID,
				CreatedAt:                createdAt,
			}
			created.ReceiptDigest = teamPublicationReceiptDigest(created)
			_, err = tx.ExecContext(ctx, `
				INSERT INTO team_publication_receipts(
					publication_id, store_id, team_id, deployment_id, shared_project_id,
					idempotency_key_hash, operation_digest, envelope_schema,
					envelope_digest, policy_epoch, global_epoch,
					publisher_principal_id, publisher_membership_id,
					publisher_client_key, publisher_binding_id, runtime_writer_id,
					approving_owner_principal_id, approval_nonce_hash,
					approval_audit_event_id, object_id, capsule_id,
					object_audit_event_id, event_projection_job_id,
					embedding_projection_job_id, receipt_digest, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				created.PublicationID, created.StoreID, created.TeamID, created.DeploymentID,
				created.SharedProjectID, normalized.issue.IdempotencyKeyHash,
				created.OperationDigest, TeamPublicationEnvelopeSchema, created.EnvelopeDigest,
				normalized.issue.PolicyEpoch, normalized.issue.GlobalEpoch,
				created.PublisherPrincipalID, created.PublisherMembershipID,
				created.PublisherClientKey, created.PublisherBindingID,
				normalized.issue.Writer.WriterID, created.ApprovingOwnerPrincipalID,
				consumption.NonceHash, created.ApprovalAuditEventID,
				created.ObjectID, created.CapsuleID, created.ObjectAuditEventID,
				created.EventProjectionJobID, created.EmbeddingProjectionJobID,
				created.ReceiptDigest, created.CreatedAt.Format(time.RFC3339Nano),
			)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO team_publication_receipt_payloads(publication_id, envelope_json)
				VALUES (?, ?)`, created.PublicationID, string(normalized.canonical))
			return err
		},
	})
	if err != nil {
		return TeamPublicationReceipt{}, err
	}
	receipt, err := s.loadTeamPublicationReceipt(ctx, normalized)
	if err != nil {
		return TeamPublicationReceipt{}, err
	}
	if receipt.ObjectID != root.ObjectID || (!root.Replayed && created.PublicationID != receipt.PublicationID) {
		return TeamPublicationReceipt{}, ErrTeamPublicationReceiptMissing
	}
	receipt.Replayed = root.Replayed
	return receipt, nil
}

// LookupTeamPublicationReceipt reconciles an ambiguous remote result without
// publishing again. It returns content-free IDs/digests only and requires the
// same active publisher or a current Team Owner who can read the stored root.
func (s *Store) LookupTeamPublicationReceipt(
	ctx context.Context,
	request TeamPublicationReceiptLookup,
) (TeamPublicationReceipt, error) {
	target, err := s.configuredTeamPublicationTarget()
	if err != nil {
		return TeamPublicationReceipt{}, err
	}
	if !validTrayIdentifier(request.PublicationKey) ||
		teamPublicationPrivateReference.MatchString(request.PublicationKey) ||
		!validDigest(request.EnvelopeDigest) {
		return TeamPublicationReceipt{}, ErrTeamPublicationInvalid
	}
	keyDigest := sha256.Sum256([]byte("pulse-team-idempotency-key-v1\x00" + request.PublicationKey))
	receipt, err := s.readTeamPublicationReceiptByKey(ctx, target, hex.EncodeToString(keyDigest[:]))
	if err != nil {
		return TeamPublicationReceipt{}, err
	}
	if subtle.ConstantTimeCompare([]byte(receipt.EnvelopeDigest), []byte(request.EnvelopeDigest)) != 1 ||
		teamPublicationReceiptDigest(receipt) != receipt.ReceiptDigest {
		return TeamPublicationReceipt{}, ErrTeamPublicationIdempotencyConflict
	}
	principal, err := s.ResolveTeamPrincipal(ctx, request.Filter.principalID)
	if err != nil {
		return TeamPublicationReceipt{}, err
	}
	if request.Filter.principalID != receipt.PublisherPrincipalID &&
		!(principal.Kind == string(teamauth.PrincipalHuman) && principal.MembershipRole == "owner") {
		return TeamPublicationReceipt{}, ErrConcealedNotFound
	}
	if err := s.recheckAuthorizedTeamPublicationReceipt(ctx, request.Filter, receipt.PublicationID); err != nil {
		return TeamPublicationReceipt{}, err
	}
	return receipt, nil
}

// recheckAuthorizedTeamPublicationReceipt authorizes the immutable,
// content-free receipt after its published root has been deleted. The receipt
// retains only audit identity; current membership, binding, policy epoch,
// active project context, project ownership/grant, privacy, and retention are
// still evaluated through the same fixed candidate predicate used for a live
// project-scoped memory root.
func (s *Store) recheckAuthorizedTeamPublicationReceipt(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
	publicationID string,
) error {
	if err := s.RecheckAuthorizedCandidateFilter(ctx, filter); err != nil {
		return err
	}
	predicate, args, err := filter.SQLPredicate("receipt_scope")
	if err != nil {
		return err
	}
	args = append([]any{publicationID}, args...)
	var present int
	err = s.db.QueryRowContext(ctx, `
		SELECT 1
		  FROM (
			SELECT receipt.team_id AS team_id,
			       'active' AS lifecycle,
			       NULL AS expires_at,
			       'project' AS scope_type,
			       receipt.shared_project_id AS scope_id,
			       project.owner_principal_id AS owner_principal_id,
			       'memory' AS object_kind,
			       'normal' AS privacy_tier,
			       'long_term' AS retention
			  FROM team_publication_receipts receipt
			  JOIN team_projects project
			    ON project.project_id = receipt.shared_project_id
			   AND project.team_id = receipt.team_id
			 WHERE receipt.publication_id = ?
		  ) receipt_scope
		 WHERE `+predicate, args...).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		if recheckErr := s.RecheckAuthorizedCandidateFilter(ctx, filter); recheckErr != nil {
			return recheckErr
		}
		return ErrConcealedNotFound
	}
	if err != nil {
		return err
	}
	return s.RecheckAuthorizedCandidateFilter(ctx, filter)
}

func (s *Store) normalizeRemoteTeamPublication(
	ctx context.Context,
	canonical []byte,
	digest string,
	writer TeamWriterLeaseIdentity,
	approvingOwnerPrincipalID, approvingClientKey, requestID string,
) (normalizedTeamPublicationRemote, error) {
	target, err := s.configuredTeamPublicationTarget()
	if err != nil {
		return normalizedTeamPublicationRemote{}, err
	}
	if len(canonical) < 2 || len(canonical) > teamPublicationMaxBytes || !validDigest(digest) {
		return normalizedTeamPublicationRemote{}, ErrTeamPublicationInvalid
	}
	var preliminary teamPublicationEnvelope
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&preliminary); err != nil {
		return normalizedTeamPublicationRemote{}, ErrTeamPublicationInvalid
	}
	envelope, exact, exactDigest, err := normalizeTeamPublicationEnvelope(TeamPublicationPrepareRequest{
		DeploymentID: preliminary.DeploymentID, RemoteStoreID: preliminary.StoreID,
		TeamID: preliminary.TeamID, PolicyEpoch: preliminary.PolicyEpoch,
		WriterPrincipalID: preliminary.WriterPrincipalID, ClientKey: preliminary.ClientKey,
		WriterID: preliminary.WriterID, CanonicalEnvelope: canonical,
		EnvelopeDigest: digest, IdempotencyKey: preliminary.PublicationKey,
	})
	if err != nil || subtle.ConstantTimeCompare([]byte(exactDigest), []byte(digest)) != 1 {
		return normalizedTeamPublicationRemote{}, ErrTeamPublicationInvalid
	}
	if envelope.DeploymentID != target.DeploymentID || envelope.WriterID != writer.WriterID {
		return normalizedTeamPublicationRemote{}, ErrTeamPublicationTargetMismatch
	}
	info, err := readTeamStoreInfo(ctx, s.db)
	if err != nil {
		return normalizedTeamPublicationRemote{}, err
	}
	if envelope.StoreID != info.StoreID || envelope.TeamID != info.TeamID || envelope.TargetID != info.TeamID {
		return normalizedTeamPublicationRemote{}, ErrTeamPublicationTargetMismatch
	}
	if target.SyntheticOnly && !containsExactString(*envelope.Metadata.Tags, "synthetic") {
		return normalizedTeamPublicationRemote{}, ErrTeamPublicationSyntheticOnly
	}
	var projectOwner string
	var policyEpoch, globalEpoch int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT project.owner_principal_id, policy.policy_epoch, policy.global_epoch
		  FROM team_projects project
		  JOIN team_policy_metadata policy ON policy.team_id = project.team_id
		 WHERE project.project_id = ? AND project.team_id = ?
		   AND policy.store_id = ?`, target.ProjectID, info.TeamID, info.StoreID,
	).Scan(&projectOwner, &policyEpoch, &globalEpoch); err != nil {
		return normalizedTeamPublicationRemote{}, ErrTeamPublicationTargetMismatch
	}
	if projectOwner != approvingOwnerPrincipalID || envelope.PolicyEpoch != policyEpoch {
		return normalizedTeamPublicationRemote{}, ErrTeamPublicationTargetMismatch
	}
	permit, err := s.AuthorizeTeamMutation(ctx, TeamMutationAuthorizationRequest{
		PrincipalID: envelope.WriterPrincipalID, OAuthClientKey: envelope.ClientKey,
		Action: teamauth.ActionWrite, Capabilities: []teamauth.Capability{teamauth.CapabilityWrite},
		Context:        teamauth.ActiveContext{TeamID: info.TeamID, ProjectID: target.ProjectID},
		ObjectKind:     "memory",
		RequestedScope: &teamauth.CanonicalScope{Type: teamauth.ScopeProject, ID: target.ProjectID},
	})
	if err != nil {
		return normalizedTeamPublicationRemote{}, err
	}
	memory, err := normalizeTeamMemoryWrite(permit, TeamMemoryWrite{
		Schema: TeamMemorySchema,
		Source: CapsuleSource{Host: "pulse-cli", ConversationScope: "user_selected_excerpt", Timestamp: envelope.SourceTimestamp},
		Items: []TeamMemoryItem{{Kind: envelope.Metadata.Kind, RedactedSummary: envelope.Content,
			Confidence: 1, EvidenceHint: "user_confirmed", Tags: append([]string(nil), (*envelope.Metadata.Tags)...)}},
		RawInputIncluded: false,
		ActiveContext:    TeamMemoryActiveContext{ProjectID: target.ProjectID},
		TargetScope:      &TeamMemoryTarget{Type: teamauth.ScopeProject, ID: target.ProjectID},
		PrivacyTier:      "normal", Retention: "long_term", IdempotencyKey: envelope.PublicationKey,
	})
	if err != nil {
		return normalizedTeamPublicationRemote{}, err
	}
	object := TeamObjectWriteRequest{
		Permit: permit, Writer: writer, RequestID: requestID,
		OAuthClientKey: envelope.ClientKey, IdempotencyKey: envelope.PublicationKey,
		Body: memory.body, BodyDigest: memory.bodyDigest,
		Policy:          TeamObjectPolicy{PrivacyTier: memory.write.PrivacyTier, Retention: memory.write.Retention},
		ProjectionKinds: []string{"event", "embedding"},
	}
	objectWrite, err := s.normalizeTeamObjectWrite(object)
	if err != nil {
		return normalizedTeamPublicationRemote{}, err
	}
	if err := overrideTeamObjectRootOwner(&objectWrite, approvingOwnerPrincipalID); err != nil {
		return normalizedTeamPublicationRemote{}, err
	}
	issue := TeamPublicationApprovalIssueRequest{
		DeploymentID: target.DeploymentID, StoreID: info.StoreID, TeamID: info.TeamID,
		SharedProjectID: target.ProjectID, EnvelopeDigest: exactDigest,
		IdempotencyKeyHash:    objectWrite.idempotencyHash,
		PublisherPrincipalID:  permit.attribution.ActorPrincipalID,
		PublisherMembershipID: permit.membershipID,
		PublisherClientKey:    permit.attribution.OAuthClientKey,
		PublisherBindingID:    permit.bindingID, Writer: writer,
		ApprovingOwnerPrincipalID: approvingOwnerPrincipalID,
		ApprovingClientKey:        approvingClientKey,
		PolicyEpoch:               policyEpoch, GlobalEpoch: globalEpoch,
	}
	issue.OperationDigest = teamPublicationOperationDigest(issue, objectWrite.operationDigest)
	return normalizedTeamPublicationRemote{
		target: target, envelope: envelope, canonical: exact, digest: exactDigest,
		permit: permit, memory: memory, object: object, objectWrite: objectWrite, issue: issue,
	}, nil
}

func (s *Store) loadTeamPublicationReceipt(
	ctx context.Context,
	normalized normalizedTeamPublicationRemote,
) (TeamPublicationReceipt, error) {
	receipt, err := s.readTeamPublicationReceiptByKey(ctx, normalized.target, normalized.issue.IdempotencyKeyHash)
	if err != nil {
		return TeamPublicationReceipt{}, err
	}
	if receipt.OperationDigest != normalized.issue.OperationDigest ||
		receipt.EnvelopeDigest != normalized.issue.EnvelopeDigest ||
		receipt.SharedProjectID != normalized.issue.SharedProjectID ||
		receipt.PublisherPrincipalID != normalized.issue.PublisherPrincipalID ||
		receipt.PublisherMembershipID != normalized.issue.PublisherMembershipID ||
		receipt.PublisherClientKey != normalized.issue.PublisherClientKey ||
		receipt.PublisherBindingID != normalized.issue.PublisherBindingID ||
		receipt.ApprovingOwnerPrincipalID != normalized.issue.ApprovingOwnerPrincipalID ||
		teamPublicationReceiptDigest(receipt) != receipt.ReceiptDigest {
		return TeamPublicationReceipt{}, ErrTeamPublicationIdempotencyConflict
	}
	return receipt, nil
}

func (s *Store) readTeamPublicationReceiptByKey(
	ctx context.Context,
	target TeamPublicationTarget,
	idempotencyHash string,
) (TeamPublicationReceipt, error) {
	info, err := readTeamStoreInfo(ctx, s.db)
	if err != nil {
		return TeamPublicationReceipt{}, err
	}
	var receipt TeamPublicationReceipt
	var storedIdempotencyHash, createdAt string
	err = s.db.QueryRowContext(ctx, `
		SELECT publication_id, deployment_id, store_id, team_id, shared_project_id,
		       idempotency_key_hash, operation_digest, envelope_digest,
		       publisher_principal_id, publisher_membership_id,
		       publisher_client_key, publisher_binding_id,
		       approving_owner_principal_id, approval_audit_event_id,
		       object_id, capsule_id, object_audit_event_id,
		       event_projection_job_id, embedding_projection_job_id,
		       receipt_digest, created_at
		  FROM team_publication_receipts
		 WHERE store_id = ? AND team_id = ? AND deployment_id = ?
		   AND idempotency_key_hash = ?`,
		info.StoreID, info.TeamID, target.DeploymentID, idempotencyHash,
	).Scan(
		&receipt.PublicationID, &receipt.DeploymentID, &receipt.StoreID, &receipt.TeamID,
		&receipt.SharedProjectID, &storedIdempotencyHash, &receipt.OperationDigest,
		&receipt.EnvelopeDigest, &receipt.PublisherPrincipalID,
		&receipt.PublisherMembershipID, &receipt.PublisherClientKey,
		&receipt.PublisherBindingID, &receipt.ApprovingOwnerPrincipalID,
		&receipt.ApprovalAuditEventID, &receipt.ObjectID, &receipt.CapsuleID,
		&receipt.ObjectAuditEventID, &receipt.EventProjectionJobID,
		&receipt.EmbeddingProjectionJobID, &receipt.ReceiptDigest, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamPublicationReceipt{}, ErrTeamPublicationReceiptMissing
	}
	if err != nil {
		return TeamPublicationReceipt{}, err
	}
	receipt.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || storedIdempotencyHash != idempotencyHash ||
		receipt.DeploymentID != target.DeploymentID ||
		receipt.SharedProjectID != target.ProjectID ||
		teamPublicationReceiptDigest(receipt) != receipt.ReceiptDigest {
		return TeamPublicationReceipt{}, ErrTeamPublicationIdempotencyConflict
	}
	return receipt, nil
}

func teamPublicationOperationDigest(issue TeamPublicationApprovalIssueRequest, objectOperationDigest string) string {
	return ownerApprovalDigest(
		"pulse-team-publication-operation-v1", teamPublicationAction,
		issue.DeploymentID, issue.StoreID, issue.TeamID, issue.SharedProjectID,
		issue.EnvelopeDigest, issue.IdempotencyKeyHash,
		issue.PublisherPrincipalID, issue.PublisherMembershipID,
		issue.PublisherClientKey, issue.PublisherBindingID,
		issue.Writer.WriterID, issue.ApprovingOwnerPrincipalID,
		fmt.Sprintf("%d", issue.PolicyEpoch), fmt.Sprintf("%d", issue.GlobalEpoch),
		objectOperationDigest,
	)
}

func teamPublicationReceiptDigest(receipt TeamPublicationReceipt) string {
	return ownerApprovalDigest(
		"pulse-team-publication-receipt-v1", receipt.PublicationID,
		receipt.DeploymentID, receipt.StoreID, receipt.TeamID, receipt.SharedProjectID,
		receipt.EnvelopeDigest, receipt.OperationDigest,
		receipt.PublisherPrincipalID, receipt.PublisherMembershipID,
		receipt.PublisherClientKey, receipt.PublisherBindingID,
		receipt.ApprovingOwnerPrincipalID, receipt.ApprovalAuditEventID,
		receipt.ObjectID, receipt.CapsuleID, receipt.ObjectAuditEventID,
		receipt.EventProjectionJobID, receipt.EmbeddingProjectionJobID,
		receipt.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
}

func containsExactString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
