package server

import (
	"net/http"
	"strings"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const (
	OwnerMembersRoutePath       = "/team/v1/owner/members"
	OwnerBindingsRoutePath      = "/team/v1/owner/bindings"
	OwnerServicesRoutePath      = "/team/v1/owner/services"
	OwnerProjectsRoutePath      = "/team/v1/owner/projects"
	OwnerProjectGrantsRoutePath = "/team/v1/owner/project-grants"

	OwnerMembersSchema             = "pulse.team.owner.members.v1"
	OwnerMembersResultSchema       = "pulse.team.owner.members_result.v1"
	OwnerBindingsSchema            = "pulse.team.owner.bindings.v1"
	OwnerBindingsResultSchema      = "pulse.team.owner.bindings_result.v1"
	OwnerServicesSchema            = "pulse.team.owner.services.v1"
	OwnerServicesResultSchema      = "pulse.team.owner.services_result.v1"
	OwnerProjectsSchema            = "pulse.team.owner.projects.v1"
	OwnerProjectsResultSchema      = "pulse.team.owner.projects_result.v1"
	OwnerProjectGrantsSchema       = "pulse.team.owner.project_grants.v1"
	OwnerProjectGrantsResultSchema = "pulse.team.owner.project_grants_result.v1"
)

type ownerAdminMutationEnvelope struct {
	Issuer            *string `json:"issuer,omitempty"`
	Subject           *string `json:"subject,omitempty"`
	ClientID          *string `json:"client_id,omitempty"`
	Role              *string `json:"role,omitempty"`
	Name              *string `json:"name,omitempty"`
	TargetID          *string `json:"target_id,omitempty"`
	ProjectID         *string `json:"project_id,omitempty"`
	TargetPrincipalID *string `json:"target_principal_id,omitempty"`
	AccessLevel       *string `json:"access_level,omitempty"`
}

type ownerAdminMutationRequestEnvelope struct {
	Schema        *string `json:"schema"`
	Action        *string `json:"action"`
	ApprovalNonce *string `json:"approval_nonce"`
	ownerAdminMutationEnvelope
}

type ownerAdminMutationRequest struct {
	Mutation      store.OwnerAdminMutation
	ApprovalNonce string
}

type ownerMemberResponse struct {
	PrincipalID  string `json:"principal_id"`
	MembershipID string `json:"membership_id"`
	Role         string `json:"role"`
	AuthEpoch    int64  `json:"auth_epoch"`
}

type ownerBindingResponse struct {
	BindingID        string `json:"binding_id"`
	HumanPrincipalID string `json:"human_principal_id"`
	AgentPrincipalID string `json:"agent_principal_id"`
	AuthEpoch        int64  `json:"auth_epoch"`
}

type ownerServiceResponse struct {
	PrincipalID  string `json:"principal_id"`
	MembershipID string `json:"membership_id"`
	AuthEpoch    int64  `json:"auth_epoch"`
}

type ownerProjectResponse struct {
	ProjectID            string `json:"project_id"`
	TeamID               string `json:"team_id"`
	Name                 string `json:"name"`
	OwnerPrincipalID     string `json:"owner_principal_id"`
	CreatedByPrincipalID string `json:"created_by_principal_id"`
}

type ownerGrantResponse struct {
	GrantID     string `json:"grant_id"`
	ProjectID   string `json:"project_id"`
	PrincipalID string `json:"principal_id"`
	AccessLevel string `json:"access_level"`
	AuthEpoch   int64  `json:"auth_epoch"`
}

type ownerAdminMutationResponse struct {
	Schema       string                `json:"schema"`
	Action       string                `json:"action"`
	AuditEventID string                `json:"audit_event_id"`
	AuthEpoch    int64                 `json:"auth_epoch"`
	Status       string                `json:"status"`
	TargetID     string                `json:"target_id,omitempty"`
	Member       *ownerMemberResponse  `json:"member,omitempty"`
	Binding      *ownerBindingResponse `json:"binding,omitempty"`
	Service      *ownerServiceResponse `json:"service,omitempty"`
	Project      *ownerProjectResponse `json:"project,omitempty"`
	Grant        *ownerGrantResponse   `json:"grant,omitempty"`
	Fallback     bool                  `json:"fallback"`
}

func (s *OwnerAdminServer) registerMutationRoutes(router interface {
	Post(string, http.HandlerFunc)
}) {
	router.Post(OwnerMembersRoutePath, s.ownerMutationHandler(
		OwnerMembersSchema, OwnerMembersResultSchema,
		store.OwnerActionMembershipCreate, store.OwnerActionMembershipRevoke,
	))
	router.Post(OwnerBindingsRoutePath, s.ownerMutationHandler(
		OwnerBindingsSchema, OwnerBindingsResultSchema,
		store.OwnerActionAgentBindingCreate, store.OwnerActionAgentBindingRevoke,
	))
	router.Post(OwnerServicesRoutePath, s.ownerMutationHandler(
		OwnerServicesSchema, OwnerServicesResultSchema,
		store.OwnerActionServicePrincipalCreate, store.OwnerActionServicePrincipalRevoke,
	))
	router.Post(OwnerProjectsRoutePath, s.ownerMutationHandler(
		OwnerProjectsSchema, OwnerProjectsResultSchema, store.OwnerActionProjectCreate,
	))
	router.Post(OwnerProjectGrantsRoutePath, s.ownerMutationHandler(
		OwnerProjectGrantsSchema, OwnerProjectGrantsResultSchema,
		store.OwnerActionProjectGrantCreate, store.OwnerActionProjectGrantRevoke,
	))
}

func (s *OwnerAdminServer) ownerMutationHandler(
	requestSchema, resultSchema string,
	allowedActions ...string,
) http.HandlerFunc {
	allowed := make(map[string]bool, len(allowedActions))
	for _, action := range allowedActions {
		allowed[action] = true
	}
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := readOwnerAdminBody(w, r, "invalid_owner_action")
		if !ok {
			return
		}
		request, err := decodeOwnerAdminMutationRequest(raw, requestSchema, allowed)
		if err != nil {
			writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_action")
			return
		}
		if s.prebootstrap || s.writer.WriterID == "" || s.writer.Token == "" {
			writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
			return
		}
		requestIDs := r.Header.Values("X-Pulse-Request-ID")
		if len(requestIDs) != 1 || !validOwnerAdminRequestID(requestIDs[0]) {
			writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_action")
			return
		}
		clientKey := teamauth.OAuthClientKey(
			s.cfg.StepUpVerifier.expectedRoot.Issuer,
			s.cfg.StepUpVerifier.expectedRoot.AdminClientID,
		)
		result, err := s.cfg.Store.ExecuteApprovedOwnerAdminMutation(
			r.Context(), store.ApprovedOwnerAdminMutationRequest{
				Mutation: request.Mutation, ApprovalNonce: request.ApprovalNonce,
				RequestID: requestIDs[0], ClientKey: clientKey, Writer: s.writer,
			},
		)
		if err != nil {
			writeOwnerStoreError(w, err, "invalid_owner_action")
			return
		}
		response := buildOwnerAdminMutationResponse(resultSchema, request.Mutation, result)
		if !validOwnerAdminMutationResponse(response) {
			writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
			return
		}
		writeTeamReadJSON(w, response)
	}
}

func decodeOwnerAdminMutationRequest(
	raw []byte,
	expectedSchema string,
	allowed map[string]bool,
) (ownerAdminMutationRequest, error) {
	var envelope ownerAdminMutationRequestEnvelope
	if !decodeOwnerAdminEnvelope(raw, &envelope) || envelope.Schema == nil || envelope.Action == nil ||
		envelope.ApprovalNonce == nil || *envelope.Schema != expectedSchema ||
		!allowed[*envelope.Action] || !validTeamAdminDigest(*envelope.ApprovalNonce) {
		return ownerAdminMutationRequest{}, store.ErrOwnerApprovalInvalid
	}
	mutation, ok := decodeOwnerAdminMutation(*envelope.Action, envelope.ownerAdminMutationEnvelope)
	if !ok {
		return ownerAdminMutationRequest{}, store.ErrOwnerApprovalInvalid
	}
	return ownerAdminMutationRequest{Mutation: mutation, ApprovalNonce: *envelope.ApprovalNonce}, nil
}

func decodeOwnerAdminMutation(action string, envelope ownerAdminMutationEnvelope) (store.OwnerAdminMutation, bool) {
	mutation := store.OwnerAdminMutation{Action: action}
	set := func(target *string, source *string) {
		if source != nil {
			*target = *source
		}
	}
	set(&mutation.Issuer, envelope.Issuer)
	set(&mutation.Subject, envelope.Subject)
	set(&mutation.ClientID, envelope.ClientID)
	set(&mutation.Role, envelope.Role)
	set(&mutation.Name, envelope.Name)
	set(&mutation.TargetID, envelope.TargetID)
	set(&mutation.ProjectID, envelope.ProjectID)
	set(&mutation.TargetPrincipalID, envelope.TargetPrincipalID)
	set(&mutation.AccessLevel, envelope.AccessLevel)
	if !validOwnerAdminMutationShape(action, envelope) {
		return store.OwnerAdminMutation{}, false
	}
	if _, _, _, err := store.OwnerAdminMutationTarget(mutation); err != nil {
		return store.OwnerAdminMutation{}, false
	}
	return mutation, true
}

func validOwnerAdminMutationShape(action string, value ownerAdminMutationEnvelope) bool {
	none := func(fields ...*string) bool {
		for _, field := range fields {
			if field != nil {
				return false
			}
		}
		return true
	}
	switch action {
	case store.OwnerActionMembershipCreate:
		return value.Issuer != nil && value.Subject != nil && value.Role != nil &&
			none(value.ClientID, value.Name, value.TargetID, value.ProjectID, value.TargetPrincipalID, value.AccessLevel)
	case store.OwnerActionMembershipRevoke, store.OwnerActionAgentBindingRevoke,
		store.OwnerActionServicePrincipalRevoke, store.OwnerActionProjectGrantRevoke:
		return value.TargetID != nil && none(value.Issuer, value.Subject, value.ClientID, value.Role,
			value.Name, value.ProjectID, value.TargetPrincipalID, value.AccessLevel)
	case store.OwnerActionAgentBindingCreate:
		return value.Issuer != nil && value.Subject != nil && value.ClientID != nil &&
			none(value.Role, value.Name, value.TargetID, value.ProjectID, value.TargetPrincipalID, value.AccessLevel)
	case store.OwnerActionServicePrincipalCreate:
		return value.Issuer != nil && value.ClientID != nil &&
			none(value.Subject, value.Role, value.Name, value.TargetID, value.ProjectID, value.TargetPrincipalID, value.AccessLevel)
	case store.OwnerActionProjectCreate:
		return value.Name != nil && validOwnerDisplayName(*value.Name) && none(value.Issuer, value.Subject, value.ClientID, value.Role,
			value.TargetID, value.ProjectID, value.TargetPrincipalID, value.AccessLevel)
	case store.OwnerActionProjectGrantCreate:
		return value.ProjectID != nil && value.TargetPrincipalID != nil && value.AccessLevel != nil &&
			none(value.Issuer, value.Subject, value.ClientID, value.Role, value.Name, value.TargetID)
	default:
		return false
	}
}

func buildOwnerAdminMutationResponse(
	schema string,
	mutation store.OwnerAdminMutation,
	result store.OwnerAdminMutationResult,
) ownerAdminMutationResponse {
	response := ownerAdminMutationResponse{
		Schema: schema, Action: result.Action, AuditEventID: result.AuditEventID,
		AuthEpoch: result.AuthEpoch, Status: "complete", Fallback: false,
	}
	if result.Member != nil {
		response.Member = &ownerMemberResponse{
			PrincipalID: result.Member.PrincipalID, MembershipID: result.Member.MembershipID,
			Role: result.Member.Role, AuthEpoch: result.Member.AuthEpoch,
		}
	}
	if result.Binding != nil {
		response.Binding = &ownerBindingResponse{
			BindingID: result.Binding.BindingID, HumanPrincipalID: result.Binding.HumanPrincipalID,
			AgentPrincipalID: result.Binding.AgentPrincipalID, AuthEpoch: result.Binding.AuthEpoch,
		}
	}
	if result.Service != nil {
		response.Service = &ownerServiceResponse{
			PrincipalID: result.Service.PrincipalID, MembershipID: result.Service.MembershipID,
			AuthEpoch: result.Service.AuthEpoch,
		}
	}
	if result.Project != nil {
		response.Project = &ownerProjectResponse{
			ProjectID: result.Project.ProjectID, TeamID: result.Project.TeamID, Name: result.Project.Name,
			OwnerPrincipalID:     result.Project.OwnerPrincipalID,
			CreatedByPrincipalID: result.Project.CreatedByPrincipalID,
		}
	}
	if result.Grant != nil {
		response.Grant = &ownerGrantResponse{
			GrantID: result.Grant.GrantID, ProjectID: result.Grant.ProjectID,
			PrincipalID: result.Grant.PrincipalID, AccessLevel: result.Grant.AccessLevel,
			AuthEpoch: result.Grant.AuthEpoch,
		}
	}
	if response.Member == nil && response.Binding == nil && response.Service == nil &&
		response.Project == nil && response.Grant == nil {
		response.TargetID = mutation.TargetID
	}
	return response
}

func validOwnerAdminMutationResponse(response ownerAdminMutationResponse) bool {
	if response.Schema == "" || response.Fallback || response.Status != "complete" ||
		!validTeamAdminClass(response.Action) || !validTeamAdminOpaque(response.AuditEventID) ||
		response.AuthEpoch < 1 {
		return false
	}
	count := 0
	for _, present := range []bool{
		response.TargetID != "", response.Member != nil, response.Binding != nil,
		response.Service != nil, response.Project != nil, response.Grant != nil,
	} {
		if present {
			count++
		}
	}
	if count != 1 {
		return false
	}
	if response.TargetID != "" {
		return validTeamAdminOpaque(response.TargetID)
	}
	if response.Member != nil {
		return validTeamAdminOpaque(response.Member.PrincipalID) &&
			validTeamAdminOpaque(response.Member.MembershipID) &&
			(response.Member.Role == "owner" || response.Member.Role == "member" || response.Member.Role == "reviewer") &&
			response.Member.AuthEpoch >= 1
	}
	if response.Binding != nil {
		return validTeamAdminOpaque(response.Binding.BindingID) &&
			validTeamAdminOpaque(response.Binding.HumanPrincipalID) &&
			validTeamAdminOpaque(response.Binding.AgentPrincipalID) && response.Binding.AuthEpoch >= 1
	}
	if response.Service != nil {
		return validTeamAdminOpaque(response.Service.PrincipalID) &&
			validTeamAdminOpaque(response.Service.MembershipID) && response.Service.AuthEpoch >= 1
	}
	if response.Project != nil {
		return validTeamAdminOpaque(response.Project.ProjectID) && validTeamAdminOpaque(response.Project.TeamID) &&
			strings.TrimSpace(response.Project.Name) != "" && len(response.Project.Name) <= 255 &&
			validTeamAdminOpaque(response.Project.OwnerPrincipalID) &&
			validTeamAdminOpaque(response.Project.CreatedByPrincipalID)
	}
	return response.Grant != nil && validTeamAdminOpaque(response.Grant.GrantID) &&
		validTeamAdminOpaque(response.Grant.ProjectID) && validTeamAdminOpaque(response.Grant.PrincipalID) &&
		(response.Grant.AccessLevel == "read" || response.Grant.AccessLevel == "write" || response.Grant.AccessLevel == "admin") &&
		response.Grant.AuthEpoch >= 1
}
