package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const (
	OwnerAuditRoutePath          = "/team/v1/owner/audit"
	OwnerDeletionStatusRoutePath = "/team/v1/owner/deletion-status"

	OwnerAuditSchema                = "pulse.team.owner.audit.v1"
	OwnerAuditResultSchema          = "pulse.team.owner.audit_result.v1"
	OwnerDeletionStatusSchema       = "pulse.team.owner.deletion_status.v1"
	OwnerDeletionStatusResultSchema = "pulse.team.owner.deletion_status_result.v1"
)

type ownerAuditEnvelope struct {
	Schema        *string `json:"schema"`
	ApprovalNonce *string `json:"approval_nonce"`
	Cursor        *string `json:"cursor,omitempty"`
	Limit         *int    `json:"limit"`
}

type ownerAuditRequest struct {
	ApprovalNonce string
	Cursor        string
	Limit         int
}

type ownerDeletionStatusEnvelope struct {
	Schema        *string `json:"schema"`
	ApprovalNonce *string `json:"approval_nonce"`
	OperationID   *string `json:"operation_id"`
}

type ownerDeletionStatusRequest struct {
	ApprovalNonce string
	OperationID   string
}

func (s *OwnerAdminServer) handleOwnerAudit(w http.ResponseWriter, r *http.Request) {
	raw, ok := readOwnerAdminBody(w, r, "invalid_owner_audit")
	if !ok {
		return
	}
	request, err := decodeOwnerAuditRequest(raw)
	if err != nil {
		writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_audit")
		return
	}
	requestID, clientKey, ok := s.ownerQueryExecutionContext(w, r, "invalid_owner_audit")
	if !ok {
		return
	}
	page, err := s.cfg.Store.ReadApprovedOwnerAudit(r.Context(), store.ApprovedOwnerAuditRequest{
		ApprovalNonce: request.ApprovalNonce, RequestID: requestID, ClientKey: clientKey,
		Writer: s.writer, Cursor: request.Cursor, Limit: request.Limit,
	})
	if err != nil {
		writeOwnerQueryError(w, err, "invalid_owner_audit")
		return
	}
	response := buildTeamAuditResponse(page)
	response.Schema = OwnerAuditResultSchema
	response.OwnActionsOnly = false
	if !validOwnerAuditResponse(response) {
		writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func (s *OwnerAdminServer) handleOwnerDeletionStatus(w http.ResponseWriter, r *http.Request) {
	raw, ok := readOwnerAdminBody(w, r, "invalid_owner_deletion_status")
	if !ok {
		return
	}
	request, err := decodeOwnerDeletionStatusRequest(raw)
	if err != nil {
		writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_deletion_status")
		return
	}
	requestID, clientKey, ok := s.ownerQueryExecutionContext(w, r, "invalid_owner_deletion_status")
	if !ok {
		return
	}
	result, err := s.cfg.Store.ReadApprovedOwnerDeletionStatus(
		r.Context(), store.ApprovedOwnerDeletionStatusRequest{
			ApprovalNonce: request.ApprovalNonce, RequestID: requestID, ClientKey: clientKey,
			Writer: s.writer, OperationID: request.OperationID,
		},
	)
	if err != nil {
		writeOwnerQueryError(w, err, "invalid_owner_deletion_status")
		return
	}
	response := teamDeletionStatusResponse{
		Schema: OwnerDeletionStatusResultSchema, OperationID: result.OperationID,
		ObjectID: result.ObjectID, AuditEventID: result.AuditEventID,
		Status: result.Status, Attempts: result.AttemptCount, Fallback: false,
	}
	if result.NextAttemptAt != nil {
		response.NextAttemptAt = result.NextAttemptAt.UTC().Format(time.RFC3339Nano)
	}
	if result.CompletedAt != nil {
		response.CompletedAt = result.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if !validOwnerDeletionStatusResponse(response) {
		writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func decodeOwnerAuditRequest(raw []byte) (ownerAuditRequest, error) {
	var envelope ownerAuditEnvelope
	if !decodeOwnerAdminEnvelope(raw, &envelope) || envelope.Schema == nil ||
		envelope.ApprovalNonce == nil || envelope.Limit == nil ||
		*envelope.Schema != OwnerAuditSchema || !validTeamAdminDigest(*envelope.ApprovalNonce) ||
		*envelope.Limit < 1 || *envelope.Limit > 50 {
		return ownerAuditRequest{}, store.ErrOwnerApprovalInvalid
	}
	request := ownerAuditRequest{ApprovalNonce: *envelope.ApprovalNonce, Limit: *envelope.Limit}
	if envelope.Cursor != nil {
		if !validTeamAdminOpaque(*envelope.Cursor) {
			return ownerAuditRequest{}, store.ErrOwnerApprovalInvalid
		}
		request.Cursor = *envelope.Cursor
	}
	return request, nil
}

func decodeOwnerDeletionStatusRequest(raw []byte) (ownerDeletionStatusRequest, error) {
	var envelope ownerDeletionStatusEnvelope
	if !decodeOwnerAdminEnvelope(raw, &envelope) || envelope.Schema == nil ||
		envelope.ApprovalNonce == nil || envelope.OperationID == nil ||
		*envelope.Schema != OwnerDeletionStatusSchema ||
		!validTeamAdminDigest(*envelope.ApprovalNonce) ||
		!validTeamAdminOpaque(*envelope.OperationID) {
		return ownerDeletionStatusRequest{}, store.ErrOwnerApprovalInvalid
	}
	return ownerDeletionStatusRequest{
		ApprovalNonce: *envelope.ApprovalNonce, OperationID: *envelope.OperationID,
	}, nil
}

func (s *OwnerAdminServer) ownerQueryExecutionContext(
	w http.ResponseWriter,
	r *http.Request,
	invalidCode string,
) (string, string, bool) {
	if s.prebootstrap || s.writer.WriterID == "" || s.writer.Token == "" {
		writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return "", "", false
	}
	requestIDs := r.Header.Values("X-Pulse-Request-ID")
	if len(requestIDs) != 1 || !validOwnerAdminRequestID(requestIDs[0]) {
		writeOwnerAdminError(w, http.StatusBadRequest, invalidCode)
		return "", "", false
	}
	return requestIDs[0], teamauth.OAuthClientKey(
		s.cfg.StepUpVerifier.expectedRoot.Issuer,
		s.cfg.StepUpVerifier.expectedRoot.AdminClientID,
	), true
}

func validOwnerAuditResponse(response teamAuditResponse) bool {
	if response.Schema != OwnerAuditResultSchema || response.OwnActionsOnly || response.Fallback ||
		len(response.Events) > 50 ||
		(response.NextCursor != "" && !validTeamAdminOpaque(response.NextCursor)) {
		return false
	}
	for _, event := range response.Events {
		if !validTeamAdminOpaque(event.EventID) || !validTeamAdminTime(event.OccurredAt) ||
			!validTeamAdminClass(event.Action) || !validTeamAdminClass(event.Outcome) ||
			!validTeamAdminOpaque(event.ActorPrincipalID) ||
			!validTeamAdminClientKey(event.ClientKey) || !validTeamAdminOpaque(event.TeamID) ||
			!validTeamAdminClass(event.TargetKind) || event.PolicyVersion <= 0 ||
			event.Mode != "team-remote" || !validTeamAdminClass(event.ReasonCode) {
			return false
		}
		switch event.Outcome {
		case "allowed", "denied", "error":
		default:
			return false
		}
		for _, optional := range []*string{event.ProjectID, event.TargetID, event.RequestID} {
			if optional != nil && !validTeamAdminOpaque(*optional) {
				return false
			}
		}
	}
	return true
}

func validOwnerDeletionStatusResponse(response teamDeletionStatusResponse) bool {
	response.Schema = TeamDeleteStatusResultSchema
	return validTeamDeletionStatusResponse(response)
}

func writeOwnerQueryError(w http.ResponseWriter, err error, invalidCode string) {
	if errors.Is(err, store.ErrConcealedNotFound) {
		writeOwnerAdminError(w, http.StatusNotFound, "concealed_not_found")
		return
	}
	writeOwnerStoreError(w, err, invalidCode)
}
