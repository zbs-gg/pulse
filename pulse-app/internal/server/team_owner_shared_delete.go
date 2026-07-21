package server

import (
	"errors"
	"net/http"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const (
	OwnerSharedDeleteRoutePath    = "/team/v1/owner/shared-delete"
	OwnerSharedDeleteSchema       = "pulse.team.owner.shared_delete.v1"
	OwnerSharedDeleteResultSchema = "pulse.team.owner.shared_delete_result.v1"
)

type ownerSharedDeleteEnvelope struct {
	Schema         *string `json:"schema"`
	ObjectID       *string `json:"object_id"`
	IdempotencyKey *string `json:"idempotency_key"`
	ApprovalNonce  *string `json:"approval_nonce"`
}

type ownerSharedDeleteRequest struct {
	ObjectID       string
	IdempotencyKey string
	ApprovalNonce  string
}

type ownerSharedDeleteResponse struct {
	Schema       string `json:"schema"`
	OperationID  string `json:"operation_id"`
	ObjectID     string `json:"object_id"`
	AuditEventID string `json:"audit_event_id"`
	Status       string `json:"status"`
	Replayed     bool   `json:"replayed"`
	Fallback     bool   `json:"fallback"`
}

func (s *OwnerAdminServer) handleOwnerSharedDelete(w http.ResponseWriter, r *http.Request) {
	raw, ok := readOwnerAdminBody(w, r, "invalid_owner_shared_delete")
	if !ok {
		return
	}
	request, err := decodeOwnerSharedDeleteRequest(raw)
	if err != nil {
		writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_shared_delete")
		return
	}
	if s.prebootstrap || s.writer.WriterID == "" || s.writer.Token == "" {
		writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	requestIDs := r.Header.Values("X-Pulse-Request-ID")
	if len(requestIDs) != 1 || !validOwnerAdminRequestID(requestIDs[0]) {
		writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_shared_delete")
		return
	}
	clientKey := teamauth.OAuthClientKey(
		s.cfg.StepUpVerifier.expectedRoot.Issuer,
		s.cfg.StepUpVerifier.expectedRoot.AdminClientID,
	)
	result, err := s.cfg.Store.StartTeamDeletionWithApproval(r.Context(), store.TeamSharedDeletionStartRequest{
		ApprovalNonce: request.ApprovalNonce, ClientKey: clientKey, Writer: s.writer,
		RequestID: requestIDs[0], IdempotencyKey: request.IdempotencyKey, ObjectID: request.ObjectID,
	})
	if err != nil {
		writeOwnerSharedDeleteError(w, err)
		return
	}
	response := ownerSharedDeleteResponse{
		Schema: OwnerSharedDeleteResultSchema, OperationID: result.OperationID,
		ObjectID: result.ObjectID, AuditEventID: result.AuditEventID,
		Status: result.Status, Replayed: result.Replayed, Fallback: false,
	}
	if !validOwnerSharedDeleteResponse(response) {
		writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func decodeOwnerSharedDeleteRequest(raw []byte) (ownerSharedDeleteRequest, error) {
	var envelope ownerSharedDeleteEnvelope
	if !decodeOwnerAdminEnvelope(raw, &envelope) || envelope.Schema == nil ||
		envelope.ObjectID == nil || envelope.IdempotencyKey == nil || envelope.ApprovalNonce == nil ||
		*envelope.Schema != OwnerSharedDeleteSchema || !validTeamAdminOpaque(*envelope.ObjectID) ||
		!validTeamDeletionOpaque(*envelope.IdempotencyKey, 8) ||
		!validTeamAdminDigest(*envelope.ApprovalNonce) {
		return ownerSharedDeleteRequest{}, store.ErrOwnerApprovalInvalid
	}
	return ownerSharedDeleteRequest{
		ObjectID: *envelope.ObjectID, IdempotencyKey: *envelope.IdempotencyKey,
		ApprovalNonce: *envelope.ApprovalNonce,
	}, nil
}

func validOwnerSharedDeleteResponse(response ownerSharedDeleteResponse) bool {
	return response.Schema == OwnerSharedDeleteResultSchema && !response.Fallback &&
		validTeamAdminOpaque(response.OperationID) && validTeamAdminOpaque(response.ObjectID) &&
		validTeamAdminOpaque(response.AuditEventID) &&
		(response.Status == store.TeamDeletionStatusInProgress || response.Status == store.TeamDeletionStatusComplete)
}

func writeOwnerSharedDeleteError(w http.ResponseWriter, err error) {
	if err == nil {
		writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	// Owner approval errors preserve the common administration mapping. Domain
	// concealment remains byte-identical with the ordinary deletion route.
	switch {
	case errors.Is(err, store.ErrConcealedNotFound), errors.Is(err, store.ErrTeamPolicyDenied):
		writeOwnerAdminError(w, http.StatusNotFound, "concealed_not_found")
	case errors.Is(err, store.ErrTeamIdempotencyConflict):
		writeOwnerAdminError(w, http.StatusConflict, "idempotency_conflict")
	case errors.Is(err, store.ErrTeamPolicyEpochChanged):
		writeOwnerAdminError(w, http.StatusConflict, "authorization_stale")
	case errors.Is(err, store.ErrTeamDeletionInvalid), errors.Is(err, store.ErrTeamObjectInvalid):
		writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_shared_delete")
	default:
		writeOwnerStoreError(w, err, "invalid_owner_shared_delete")
	}
}
