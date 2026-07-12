package server

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const (
	TeamDeleteRoutePath       = "/team/v1/delete"
	TeamDeleteStatusRoutePath = "/team/v1/delete/status"

	TeamDeleteSchema             = "pulse.team.delete.v1"
	TeamDeleteResultSchema       = "pulse.team.delete_result.v1"
	TeamDeleteStatusSchema       = "pulse.team.delete_status.v1"
	TeamDeleteStatusResultSchema = "pulse.team.delete_status_result.v1"

	teamDeletionMaxBodyBytes = 16 << 10

	teamDeletionErrorInvalid       = "invalid_team_delete"
	teamDeletionStatusErrorInvalid = "invalid_team_delete_status"
	teamDeletionErrorConcealed     = "concealed_not_found"
)

type teamDeletionEnvelope struct {
	Schema         *string                `json:"schema"`
	ObjectID       *string                `json:"object_id"`
	ActiveContext  *teamReadActiveContext `json:"active_context"`
	IdempotencyKey *string                `json:"idempotency_key"`
}

type teamDeletionStatusEnvelope struct {
	Schema        *string                `json:"schema"`
	OperationID   *string                `json:"operation_id"`
	ActiveContext *teamReadActiveContext `json:"active_context"`
}

type teamDeletionRequest struct {
	ObjectID       string
	ActiveContext  teamReadActiveContext
	IdempotencyKey string
}

type teamDeletionStatusRequest struct {
	OperationID   string
	ActiveContext teamReadActiveContext
}

type teamDeletionResponse struct {
	Schema       string `json:"schema"`
	OperationID  string `json:"operation_id"`
	ObjectID     string `json:"object_id"`
	AuditEventID string `json:"audit_event_id"`
	Status       string `json:"status"`
	Replayed     bool   `json:"replayed"`
	Fallback     bool   `json:"fallback"`
}

type teamDeletionStatusResponse struct {
	Schema        string `json:"schema"`
	OperationID   string `json:"operation_id"`
	ObjectID      string `json:"object_id"`
	AuditEventID  string `json:"audit_event_id"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
	CompletedAt   string `json:"completed_at,omitempty"`
	Fallback      bool   `json:"fallback"`
}

var teamDeletionSecretPattern = regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9_-]{12,}\b`)

func decodeTeamDeletionRequest(raw []byte) (teamDeletionRequest, error) {
	var envelope teamDeletionEnvelope
	if len(raw) == 0 || len(raw) > teamDeletionMaxBodyBytes ||
		decodeStrictJSON(raw, &envelope) != nil || teamGraphJSONContainsNull(raw) ||
		envelope.Schema == nil || envelope.ObjectID == nil || envelope.ActiveContext == nil ||
		envelope.IdempotencyKey == nil || *envelope.Schema != TeamDeleteSchema ||
		!validTeamDeletionOpaque(*envelope.ObjectID, 1) ||
		!validTeamDeletionContext(*envelope.ActiveContext) ||
		!validTeamDeletionOpaque(*envelope.IdempotencyKey, 8) {
		return teamDeletionRequest{}, store.ErrTeamDeletionInvalid
	}
	return teamDeletionRequest{
		ObjectID: *envelope.ObjectID, ActiveContext: *envelope.ActiveContext,
		IdempotencyKey: *envelope.IdempotencyKey,
	}, nil
}

func decodeTeamDeletionStatusRequest(raw []byte) (teamDeletionStatusRequest, error) {
	var envelope teamDeletionStatusEnvelope
	if len(raw) == 0 || len(raw) > teamDeletionMaxBodyBytes ||
		decodeStrictJSON(raw, &envelope) != nil || teamGraphJSONContainsNull(raw) ||
		envelope.Schema == nil || envelope.OperationID == nil || envelope.ActiveContext == nil ||
		*envelope.Schema != TeamDeleteStatusSchema ||
		!validTeamDeletionOpaque(*envelope.OperationID, 1) ||
		!validTeamDeletionContext(*envelope.ActiveContext) {
		return teamDeletionStatusRequest{}, store.ErrTeamDeletionInvalid
	}
	return teamDeletionStatusRequest{
		OperationID: *envelope.OperationID, ActiveContext: *envelope.ActiveContext,
	}, nil
}

func validTeamDeletionContext(context teamReadActiveContext) bool {
	for _, value := range []string{context.ProjectID, context.RepoID, context.AgentID, context.SessionID} {
		if value != "" && !validTeamDeletionOpaque(value, 1) {
			return false
		}
	}
	return true
}

func validTeamDeletionOpaque(value string, minimum int) bool {
	if len(value) < minimum || !safeOpaque(value, 255) {
		return false
	}
	first := value[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') ||
		(first >= '0' && first <= '9')) {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"token=", "api_key", "apikey", "password", "secret", "private_key",
		"begin private key", "authorization: bearer", "akia", "xoxb-", "ghp_",
	} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return !teamDeletionSecretPattern.MatchString(value)
}

func (s *TeamServer) handleTeamDelete(w http.ResponseWriter, r *http.Request) {
	raw, ok := readTeamDeletionBody(w, r, teamDeletionErrorInvalid)
	if !ok {
		return
	}
	request, err := decodeTeamDeletionRequest(raw)
	if err != nil {
		writeTeamDeletionError(w, http.StatusBadRequest, teamDeletionErrorInvalid)
		return
	}
	principal, ok := s.verifyTeamReadPrincipal(w, r, raw)
	if !ok {
		return
	}
	result, err := s.cfg.Store.StartTeamDeletion(r.Context(), store.TeamDeletionStartRequest{
		Authorization: store.TeamMutationAuthorizationRequest{
			PrincipalID: principal.PrincipalID, OAuthClientKey: principal.OAuthClientKey,
			Action: teamauth.ActionDelete, Capabilities: principalCapabilities(principal),
			Context:          teamDeletionActiveContext(principal, request.ActiveContext),
			ExistingObjectID: request.ObjectID,
		},
		Writer: store.TeamWriterLeaseIdentity{
			WriterID: s.cfg.WriterLease.WriterID, Token: s.cfg.WriterLease.Token,
		},
		RequestID: principal.RequestID, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		writeTeamDeletionStoreError(w, err, teamDeletionErrorInvalid)
		return
	}
	response := teamDeletionResponse{
		Schema: TeamDeleteResultSchema, OperationID: result.OperationID,
		ObjectID: result.ObjectID, AuditEventID: result.AuditEventID,
		Status: result.Status, Replayed: result.Replayed, Fallback: false,
	}
	if !validTeamDeletionResponse(response) {
		writeTeamDeletionError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func (s *TeamServer) handleTeamDeleteStatus(w http.ResponseWriter, r *http.Request) {
	raw, ok := readTeamDeletionBody(w, r, teamDeletionStatusErrorInvalid)
	if !ok {
		return
	}
	request, err := decodeTeamDeletionStatusRequest(raw)
	if err != nil {
		writeTeamDeletionError(w, http.StatusBadRequest, teamDeletionStatusErrorInvalid)
		return
	}
	principal, ok := s.verifyTeamReadPrincipal(w, r, raw)
	if !ok {
		return
	}
	result, err := s.cfg.Store.ReadTeamDeletionStatus(r.Context(), store.TeamDeletionStatusRequest{
		PrincipalID: principal.PrincipalID, OAuthClientKey: principal.OAuthClientKey,
		Capabilities: principalCapabilities(principal),
		Context:      teamDeletionActiveContext(principal, request.ActiveContext),
		OperationID:  request.OperationID,
	})
	if err != nil {
		writeTeamDeletionStoreError(w, err, teamDeletionStatusErrorInvalid)
		return
	}
	response := teamDeletionStatusResponse{
		Schema: TeamDeleteStatusResultSchema, OperationID: result.OperationID,
		ObjectID: result.ObjectID, AuditEventID: result.AuditEventID,
		Status: result.Status, Attempts: result.AttemptCount, Fallback: false,
	}
	if result.NextAttemptAt != nil {
		response.NextAttemptAt = result.NextAttemptAt.UTC().Format(time.RFC3339Nano)
	}
	if result.CompletedAt != nil {
		response.CompletedAt = result.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if !validTeamDeletionStatusResponse(response) {
		writeTeamDeletionError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func readTeamDeletionBody(w http.ResponseWriter, r *http.Request, invalidCode string) ([]byte, bool) {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Encoding") != "" {
		writeTeamDeletionError(w, http.StatusBadRequest, invalidCode)
		return nil, false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeTeamDeletionError(w, http.StatusBadRequest, invalidCode)
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, teamDeletionMaxBodyBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > teamDeletionMaxBodyBytes {
		writeTeamDeletionError(w, http.StatusBadRequest, invalidCode)
		return nil, false
	}
	return raw, true
}

func principalCapabilities(principal PrincipalContext) []teamauth.Capability {
	capabilities := make([]teamauth.Capability, len(principal.Capabilities))
	for index, capability := range principal.Capabilities {
		capabilities[index] = teamauth.Capability(capability)
	}
	return capabilities
}

func teamDeletionActiveContext(principal PrincipalContext, active teamReadActiveContext) teamauth.ActiveContext {
	return teamauth.ActiveContext{
		TeamID: principal.TeamID, ProjectID: active.ProjectID, RepoID: active.RepoID,
		AgentID: active.AgentID, SessionID: active.SessionID,
	}
}

func validTeamDeletionResponse(response teamDeletionResponse) bool {
	return response.Schema == TeamDeleteResultSchema && response.Fallback == false &&
		validTeamDeletionOpaque(response.OperationID, 1) &&
		validTeamDeletionOpaque(response.ObjectID, 1) &&
		validTeamDeletionOpaque(response.AuditEventID, 1) &&
		(response.Status == store.TeamDeletionStatusInProgress ||
			response.Status == store.TeamDeletionStatusComplete)
}

func validTeamDeletionStatusResponse(response teamDeletionStatusResponse) bool {
	if response.Schema != TeamDeleteStatusResultSchema || response.Fallback ||
		!validTeamDeletionOpaque(response.OperationID, 1) ||
		!validTeamDeletionOpaque(response.ObjectID, 1) ||
		!validTeamDeletionOpaque(response.AuditEventID, 1) ||
		response.Attempts < 0 || response.Attempts > 1_000_000 {
		return false
	}
	switch response.Status {
	case store.TeamDeletionStatusInProgress:
		return response.CompletedAt == "" && validOptionalTeamDeletionTime(response.NextAttemptAt)
	case store.TeamDeletionStatusCleanupFailed:
		return response.CompletedAt == "" && validTeamDeletionTime(response.NextAttemptAt)
	case store.TeamDeletionStatusComplete:
		return response.NextAttemptAt == "" && validTeamDeletionTime(response.CompletedAt)
	default:
		return false
	}
}

func validOptionalTeamDeletionTime(value string) bool {
	return value == "" || validTeamDeletionTime(value)
}

func validTeamDeletionTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func writeTeamDeletionStoreError(w http.ResponseWriter, err error, invalidCode string) {
	status, code := http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable
	switch {
	case errors.Is(err, store.ErrTeamDeletionInvalid), errors.Is(err, store.ErrTeamObjectInvalid):
		status, code = http.StatusBadRequest, invalidCode
	case errors.Is(err, store.ErrConcealedNotFound), errors.Is(err, store.ErrTeamPolicyDenied):
		status, code = http.StatusNotFound, teamDeletionErrorConcealed
	case errors.Is(err, store.ErrPrincipalRevoked):
		status, code = http.StatusForbidden, teamMemoryErrorPrincipalRevoked
	case errors.Is(err, store.ErrTeamIdempotencyConflict):
		status, code = http.StatusConflict, teamMemoryErrorIdempotencyConflict
	case errors.Is(err, store.ErrTeamPolicyEpochChanged):
		status, code = http.StatusConflict, teamMemoryErrorAuthorizationStale
	}
	writeTeamDeletionError(w, status, code)
}

func writeTeamDeletionError(w http.ResponseWriter, status int, code string) {
	writeTeamError(w, status, code, true)
}
