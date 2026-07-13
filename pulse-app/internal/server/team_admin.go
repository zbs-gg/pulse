package server

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const (
	TeamStatusRoutePath  = "/team/v1/status"
	TeamInspectRoutePath = "/team/v1/inspect"
	TeamAuditRoutePath   = "/team/v1/audit"

	TeamStatusSchema        = "pulse.team.status.v1"
	TeamStatusResultSchema  = "pulse.team.status_result.v1"
	TeamInspectSchema       = "pulse.team.inspect.v1"
	TeamInspectResultSchema = "pulse.team.inspect_result.v1"
	TeamAuditSchema         = "pulse.team.audit.v1"
	TeamAuditResultSchema   = "pulse.team.audit_result.v1"

	teamAdminMaxBodyBytes = 16 << 10

	teamStatusErrorInvalid  = "invalid_team_status"
	teamInspectErrorInvalid = "invalid_team_inspect"
	teamAuditErrorInvalid   = "invalid_team_audit"
)

var errInvalidTeamAdminContract = errors.New("invalid team admin contract")

type teamStatusEnvelope struct {
	Schema        *string                `json:"schema"`
	ActiveContext *teamReadActiveContext `json:"active_context"`
}

type teamInspectEnvelope struct {
	Schema        *string                `json:"schema"`
	ObjectID      *string                `json:"object_id"`
	ActiveContext *teamReadActiveContext `json:"active_context"`
}

type teamAuditEnvelope struct {
	Schema        *string                `json:"schema"`
	ActiveContext *teamReadActiveContext `json:"active_context"`
	Cursor        *string                `json:"cursor,omitempty"`
	Limit         *int                   `json:"limit,omitempty"`
}

type teamStatusRequest struct {
	Schema        string
	ActiveContext teamReadActiveContext
}

type teamInspectRequest struct {
	Schema        string
	ObjectID      string
	ActiveContext teamReadActiveContext
}

type teamAuditRequest struct {
	Schema        string
	ActiveContext teamReadActiveContext
	Cursor        string
	Limit         int
}

type teamStatusResponse struct {
	Schema                string                `json:"schema"`
	Mode                  string                `json:"mode"`
	TeamID                string                `json:"team_id"`
	StoreID               string                `json:"store_id"`
	PrincipalID           string                `json:"principal_id"`
	PrincipalKind         string                `json:"principal_kind"`
	HumanPrincipalID      *string               `json:"human_principal_id"`
	AgentBindingID        *string               `json:"agent_binding_id"`
	MembershipID          string                `json:"membership_id"`
	MembershipRole        string                `json:"membership_role"`
	ActiveContext         teamReadActiveContext `json:"active_context"`
	EffectiveCapabilities []string              `json:"effective_capabilities"`
	PolicyVersion         int                   `json:"policy_version"`
	ProjectionState       string                `json:"projection_state"`
	Degraded              bool                  `json:"degraded"`
	DegradedReasons       []string              `json:"degraded_reasons"`
	Fallback              bool                  `json:"fallback"`
}

type teamInspectScopeResponse struct {
	Type             string  `json:"type"`
	ID               string  `json:"id"`
	OwnerPrincipalID *string `json:"owner_principal_id"`
}

type teamInspectResponse struct {
	Schema            string                   `json:"schema"`
	ObjectID          string                   `json:"object_id"`
	ObjectKind        string                   `json:"object_kind"`
	AuthorPrincipalID string                   `json:"author_principal_id"`
	CreatedAt         string                   `json:"created_at"`
	Scope             teamInspectScopeResponse `json:"scope"`
	PrivacyTier       string                   `json:"privacy_tier"`
	Retention         string                   `json:"retention"`
	ExpiresAt         string                   `json:"expires_at,omitempty"`
	LifecycleState    string                   `json:"lifecycle_state"`
	Generation        int64                    `json:"generation"`
	ProjectionState   string                   `json:"projection_state"`
	DeletionState     string                   `json:"deletion_state"`
	Fallback          bool                     `json:"fallback"`
}

type teamAuditEventResponse struct {
	EventID          string  `json:"event_id"`
	OccurredAt       string  `json:"occurred_at"`
	Action           string  `json:"action"`
	Outcome          string  `json:"outcome"`
	ActorPrincipalID string  `json:"actor_principal_id"`
	ClientKey        string  `json:"client_key"`
	TeamID           string  `json:"team_id"`
	ProjectID        *string `json:"project_id"`
	TargetKind       string  `json:"target_kind"`
	TargetID         *string `json:"target_id"`
	RequestID        *string `json:"request_id"`
	PolicyVersion    int     `json:"policy_version"`
	Mode             string  `json:"mode"`
	ReasonCode       string  `json:"reason_code"`
}

type teamAuditResponse struct {
	Schema         string                   `json:"schema"`
	Events         []teamAuditEventResponse `json:"events"`
	NextCursor     string                   `json:"next_cursor,omitempty"`
	OwnActionsOnly bool                     `json:"own_actions_only"`
	Fallback       bool                     `json:"fallback"`
}

func decodeTeamStatusRequest(raw []byte) (teamStatusRequest, error) {
	var envelope teamStatusEnvelope
	if !decodeTeamAdminEnvelope(raw, &envelope) || envelope.Schema == nil ||
		envelope.ActiveContext == nil || *envelope.Schema != TeamStatusSchema ||
		!validTeamAdminContext(*envelope.ActiveContext) {
		return teamStatusRequest{}, errInvalidTeamAdminContract
	}
	return teamStatusRequest{Schema: *envelope.Schema, ActiveContext: *envelope.ActiveContext}, nil
}

func decodeTeamInspectRequest(raw []byte) (teamInspectRequest, error) {
	var envelope teamInspectEnvelope
	if !decodeTeamAdminEnvelope(raw, &envelope) || envelope.Schema == nil ||
		envelope.ObjectID == nil || envelope.ActiveContext == nil ||
		*envelope.Schema != TeamInspectSchema || !validTeamAdminOpaque(*envelope.ObjectID) ||
		!validTeamAdminContext(*envelope.ActiveContext) {
		return teamInspectRequest{}, errInvalidTeamAdminContract
	}
	return teamInspectRequest{
		Schema: *envelope.Schema, ObjectID: *envelope.ObjectID,
		ActiveContext: *envelope.ActiveContext,
	}, nil
}

func decodeTeamAuditRequest(raw []byte) (teamAuditRequest, error) {
	var envelope teamAuditEnvelope
	if !decodeTeamAdminEnvelope(raw, &envelope) || envelope.Schema == nil ||
		envelope.ActiveContext == nil || *envelope.Schema != TeamAuditSchema ||
		!validTeamAdminContext(*envelope.ActiveContext) {
		return teamAuditRequest{}, errInvalidTeamAdminContract
	}
	limit := 50
	if envelope.Limit != nil {
		limit = *envelope.Limit
	}
	if limit < 1 || limit > 50 {
		return teamAuditRequest{}, errInvalidTeamAdminContract
	}
	cursor := ""
	if envelope.Cursor != nil {
		cursor = *envelope.Cursor
		if !validTeamAdminOpaque(cursor) {
			return teamAuditRequest{}, errInvalidTeamAdminContract
		}
	}
	return teamAuditRequest{
		Schema: *envelope.Schema, ActiveContext: *envelope.ActiveContext,
		Cursor: cursor, Limit: limit,
	}, nil
}

func decodeTeamAdminEnvelope(raw []byte, target any) bool {
	return len(raw) > 0 && len(raw) <= teamAdminMaxBodyBytes &&
		decodeStrictJSON(raw, target) == nil && !teamGraphJSONContainsNull(raw)
}

func validTeamAdminContext(active teamReadActiveContext) bool {
	return validTeamDeletionContext(active)
}

func validTeamAdminOpaque(value string) bool {
	return validTeamDeletionOpaque(value, 1)
}

func sortedUniqueTeamCapabilities(values []string) ([]string, bool) {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		switch teamauth.Capability(value) {
		case teamauth.CapabilityConnect, teamauth.CapabilityStatus, teamauth.CapabilityRead,
			teamauth.CapabilityWrite, teamauth.CapabilityAudit, teamauth.CapabilityDelete,
			teamauth.CapabilityOwner:
		default:
			return nil, false
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result, true
}

func (s *TeamServer) handleTeamStatus(w http.ResponseWriter, r *http.Request) {
	raw, ok := readTeamAdminBody(w, r, teamStatusErrorInvalid)
	if !ok {
		return
	}
	request, err := decodeTeamStatusRequest(raw)
	if err != nil {
		writeTeamAdminError(w, http.StatusBadRequest, teamStatusErrorInvalid)
		return
	}
	principal, ok := s.verifyTeamReadPrincipal(w, r, raw)
	if !ok {
		return
	}
	if !teamPrincipalHasCapability(principal, teamauth.CapabilityStatus) {
		writeTeamAdminError(w, http.StatusForbidden, "policy_denied")
		return
	}
	metadata, err := s.cfg.Store.ReadTeamStatusMetadata(r.Context(), principal.PrincipalID)
	if err != nil {
		writeTeamAdminStoreError(w, err, teamStatusErrorInvalid, false)
		return
	}
	if !teamStatusMetadataMatchesPrincipal(metadata, principal) {
		writeTeamAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	capabilities, ok := sortedUniqueTeamCapabilities(principal.Capabilities)
	if !ok {
		writeTeamAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	response := teamStatusResponse{
		Schema: TeamStatusResultSchema, Mode: "team-remote",
		TeamID: principal.TeamID, StoreID: principal.StoreID,
		PrincipalID: principal.PrincipalID, PrincipalKind: principal.PrincipalKind,
		HumanPrincipalID: principal.HumanPrincipalID, AgentBindingID: principal.AgentBindingID,
		MembershipID: principal.MembershipID, MembershipRole: principal.MembershipRole,
		ActiveContext: request.ActiveContext, EffectiveCapabilities: capabilities,
		PolicyVersion: metadata.PolicyVersion, ProjectionState: "ready",
		DegradedReasons: []string{}, Fallback: false,
	}
	if metadata.ActivationState != store.TeamActivationActive || !metadata.PublicEnabled ||
		metadata.RealContentState != store.TeamContentSynthetic {
		response.ProjectionState = "pending"
		response.Degraded = true
		response.DegradedReasons = []string{"team_remote_inactive"}
	}
	if !validTeamStatusResponse(response) {
		writeTeamAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func (s *TeamServer) handleTeamInspect(w http.ResponseWriter, r *http.Request) {
	raw, ok := readTeamAdminBody(w, r, teamInspectErrorInvalid)
	if !ok {
		return
	}
	request, err := decodeTeamInspectRequest(raw)
	if err != nil {
		writeTeamAdminError(w, http.StatusBadRequest, teamInspectErrorInvalid)
		return
	}
	principal, ok := s.verifyTeamReadPrincipal(w, r, raw)
	if !ok {
		return
	}
	if !teamPrincipalHasCapability(principal, teamauth.CapabilityRead) {
		writeTeamAdminError(w, http.StatusForbidden, "policy_denied")
		return
	}
	metadata, err := s.cfg.Store.InspectAuthorizedTeamObject(r.Context(), store.CandidateFilterRequest{
		PrincipalID: principal.PrincipalID, Capabilities: teamPrincipalCapabilities(principal),
		Context: teamDeletionActiveContext(principal, request.ActiveContext), PrivacyCeiling: "private",
	}, request.ObjectID)
	if err != nil {
		writeTeamAdminStoreError(w, err, teamInspectErrorInvalid, true)
		return
	}
	response := buildTeamInspectResponse(metadata)
	if !validTeamInspectResponse(response) {
		writeTeamAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func (s *TeamServer) handleTeamAudit(w http.ResponseWriter, r *http.Request) {
	raw, ok := readTeamAdminBody(w, r, teamAuditErrorInvalid)
	if !ok {
		return
	}
	request, err := decodeTeamAuditRequest(raw)
	if err != nil {
		writeTeamAdminError(w, http.StatusBadRequest, teamAuditErrorInvalid)
		return
	}
	principal, ok := s.verifyTeamReadPrincipal(w, r, raw)
	if !ok {
		return
	}
	if !teamPrincipalHasCapability(principal, teamauth.CapabilityAudit) {
		writeTeamAdminError(w, http.StatusForbidden, "policy_denied")
		return
	}
	page, err := s.cfg.Store.ReadOwnTeamAudit(r.Context(), principal.PrincipalID, request.Cursor, request.Limit)
	if err != nil {
		writeTeamAdminStoreError(w, err, teamAuditErrorInvalid, true)
		return
	}
	response := buildTeamAuditResponse(page)
	if !validTeamAuditResponse(response, principal.PrincipalID, principal.TeamID) {
		writeTeamAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func teamStatusMetadataMatchesPrincipal(metadata store.TeamStatusMetadata, principal PrincipalContext) bool {
	if metadata.StoreID != principal.StoreID || metadata.TeamID != principal.TeamID ||
		metadata.PrincipalID != principal.PrincipalID || metadata.PrincipalKind != principal.PrincipalKind ||
		metadata.MembershipID != principal.MembershipID || metadata.MembershipRole != principal.MembershipRole ||
		metadata.AuthEpoch != principal.TeamAuthEpoch || metadata.PrincipalAuthEpoch != principal.PrincipalAuthEpoch ||
		metadata.MembershipAuthEpoch != principal.MembershipAuthEpoch || metadata.PolicyVersion != teamauth.PolicyVersion ||
		metadata.SchemaVersion != teamauth.SchemaVersion {
		return false
	}
	if principal.HumanPrincipalID == nil {
		if metadata.HumanPrincipalID != "" {
			return false
		}
	} else if metadata.HumanPrincipalID != *principal.HumanPrincipalID {
		return false
	}
	if principal.AgentBindingID == nil || principal.BindingAuthEpoch == nil {
		return metadata.BindingID == "" && metadata.BindingAuthEpoch == 0
	}
	return metadata.BindingID == *principal.AgentBindingID &&
		metadata.BindingAuthEpoch == *principal.BindingAuthEpoch
}

func buildTeamInspectResponse(metadata store.TeamObjectMetadata) teamInspectResponse {
	var owner *string
	if metadata.OwnerPrincipalID != "" {
		value := metadata.OwnerPrincipalID
		owner = &value
	}
	response := teamInspectResponse{
		Schema: TeamInspectResultSchema, ObjectID: metadata.ObjectID,
		ObjectKind: metadata.ObjectKind, AuthorPrincipalID: metadata.AuthorPrincipalID,
		CreatedAt: metadata.CreatedAt.UTC().Format(time.RFC3339Nano),
		Scope: teamInspectScopeResponse{
			Type: metadata.ScopeType, ID: metadata.ScopeID, OwnerPrincipalID: owner,
		},
		PrivacyTier: metadata.PrivacyTier, Retention: metadata.Retention,
		LifecycleState: metadata.Lifecycle, Generation: metadata.Generation,
		ProjectionState: canonicalTeamInspectProjectionState(metadata.ProjectionState),
		DeletionState:   canonicalTeamInspectDeletionState(metadata.DeletionState),
		Fallback:        false,
	}
	if metadata.ExpiresAt != nil {
		response.ExpiresAt = metadata.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return response
}

func canonicalTeamInspectProjectionState(value string) string {
	switch value {
	case "pending", "leased":
		return "pending"
	case "ready":
		return "ready"
	case "failed", "cancelled":
		return "failed"
	case "none":
		return "not_applicable"
	default:
		return ""
	}
}

func canonicalTeamInspectDeletionState(value string) string {
	switch value {
	case "":
		return "none"
	case "pending", "leased":
		return "pending"
	case "cleaning":
		return "cleaning"
	case "complete":
		return "complete"
	case "cleanup_failed":
		return "cleanup_failed"
	default:
		return ""
	}
}

func buildTeamAuditResponse(page store.TeamAuditPage) teamAuditResponse {
	response := teamAuditResponse{
		Schema: TeamAuditResultSchema, Events: make([]teamAuditEventResponse, 0, len(page.Events)),
		NextCursor: page.NextCursor, OwnActionsOnly: true, Fallback: false,
	}
	for _, event := range page.Events {
		response.Events = append(response.Events, teamAuditEventResponse{
			EventID: event.EventID, OccurredAt: event.OccurredAt.UTC().Format(time.RFC3339Nano),
			Action: event.Action, Outcome: event.Outcome, ActorPrincipalID: event.ActorPrincipalID,
			ClientKey: event.ClientKey, TeamID: event.TeamID,
			ProjectID: optionalTeamAdminOpaque(event.ProjectID), TargetKind: event.TargetKind,
			TargetID: optionalTeamAdminOpaque(event.TargetID), RequestID: optionalTeamAdminOpaque(event.RequestID),
			PolicyVersion: event.PolicyVersion, Mode: "team-remote", ReasonCode: event.ReasonCode,
		})
	}
	return response
}

func optionalTeamAdminOpaque(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func writeTeamAdminStoreError(w http.ResponseWriter, err error, invalidCode string, conceal bool) {
	status, code := http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable
	switch {
	case errors.Is(err, store.ErrOwnerApprovalInvalid):
		status, code = http.StatusBadRequest, invalidCode
	case errors.Is(err, store.ErrConcealedNotFound) && conceal:
		status, code = http.StatusNotFound, "concealed_not_found"
	case errors.Is(err, store.ErrPrincipalRevoked):
		status, code = http.StatusForbidden, "principal_revoked"
	case errors.Is(err, store.ErrTeamPolicyDenied):
		status, code = http.StatusForbidden, "policy_denied"
	case errors.Is(err, store.ErrTeamPolicyEpochChanged):
		status, code = http.StatusConflict, "authorization_stale"
	}
	writeTeamAdminError(w, status, code)
}

func readTeamAdminBody(w http.ResponseWriter, r *http.Request, invalidCode string) ([]byte, bool) {
	return readTeamJSONBody(w, r, teamAdminMaxBodyBytes, invalidCode)
}

func teamPrincipalHasCapability(principal PrincipalContext, capability teamauth.Capability) bool {
	for _, value := range principal.Capabilities {
		if value == string(capability) {
			return true
		}
	}
	return false
}

func validTeamStatusResponse(response teamStatusResponse) bool {
	if response.Schema != TeamStatusResultSchema || response.Mode != "team-remote" || response.Fallback ||
		!validTeamAdminOpaque(response.TeamID) || !validTeamAdminOpaque(response.StoreID) ||
		!validTeamAdminOpaque(response.PrincipalID) || !validTeamAdminOpaque(response.MembershipID) ||
		!validTeamAdminContext(response.ActiveContext) || response.PolicyVersion < 1 {
		return false
	}
	if response.Degraded {
		if response.ProjectionState != "pending" ||
			!equalStrings(response.DegradedReasons, []string{"team_remote_inactive"}) {
			return false
		}
	} else if response.ProjectionState != "ready" || len(response.DegradedReasons) != 0 {
		return false
	}
	switch response.PrincipalKind {
	case string(teamauth.PrincipalAgent):
		if response.HumanPrincipalID == nil || response.AgentBindingID == nil ||
			!validTeamAdminOpaque(*response.HumanPrincipalID) || !validTeamAdminOpaque(*response.AgentBindingID) {
			return false
		}
	case string(teamauth.PrincipalService):
		if response.HumanPrincipalID != nil || response.AgentBindingID != nil {
			return false
		}
	default:
		return false
	}
	switch response.MembershipRole {
	case string(teamauth.RoleOwner), string(teamauth.RoleMember), string(teamauth.RoleReviewer):
	default:
		return false
	}
	capabilities, ok := sortedUniqueTeamCapabilities(response.EffectiveCapabilities)
	if !ok || len(capabilities) != len(response.EffectiveCapabilities) {
		return false
	}
	for index := range capabilities {
		if capabilities[index] != response.EffectiveCapabilities[index] {
			return false
		}
	}
	return true
}

func validTeamInspectResponse(response teamInspectResponse) bool {
	if response.Schema != TeamInspectResultSchema || response.Fallback ||
		!validTeamAdminOpaque(response.ObjectID) || !validTeamAdminClass(response.ObjectKind) ||
		!validTeamAdminOpaque(response.AuthorPrincipalID) || !validTeamAdminTime(response.CreatedAt) ||
		!validTeamAdminScope(response.Scope) || !validTeamReadPrivacy(response.PrivacyTier) ||
		!validTeamReadRetention(response.Retention) || response.Retention == "" ||
		(response.ExpiresAt != "" && !validTeamAdminTime(response.ExpiresAt)) ||
		response.Generation < 1 {
		return false
	}
	switch response.ProjectionState {
	case "pending", "ready", "failed", "not_applicable":
	default:
		return false
	}
	switch response.LifecycleState {
	case string(teamauth.LifecycleActive):
		return response.DeletionState == "none"
	case string(teamauth.LifecycleTombstoned):
		return response.DeletionState == "pending"
	case string(teamauth.LifecycleCleaning):
		return response.DeletionState == "cleaning"
	case string(teamauth.LifecycleComplete):
		return response.DeletionState == "complete"
	case string(teamauth.LifecycleCleanupFailed):
		return response.DeletionState == "cleanup_failed"
	default:
		return false
	}
}

func validTeamAdminScope(scope teamInspectScopeResponse) bool {
	if !validTeamAdminOpaque(scope.ID) {
		return false
	}
	switch teamauth.ScopeType(scope.Type) {
	case teamauth.ScopeTeam:
		return scope.OwnerPrincipalID == nil
	case teamauth.ScopePersonal:
		return scope.OwnerPrincipalID != nil && *scope.OwnerPrincipalID == scope.ID &&
			validTeamAdminOpaque(*scope.OwnerPrincipalID)
	case teamauth.ScopeProject, teamauth.ScopeRepo, teamauth.ScopeAgent, teamauth.ScopeSession:
		return scope.OwnerPrincipalID != nil && validTeamAdminOpaque(*scope.OwnerPrincipalID)
	default:
		return false
	}
}

func validTeamAuditResponse(response teamAuditResponse, principalID, teamID string) bool {
	if response.Schema != TeamAuditResultSchema || response.Fallback || !response.OwnActionsOnly ||
		!validTeamAdminOpaque(principalID) || !validTeamAdminOpaque(teamID) ||
		(response.NextCursor != "" && !validTeamAdminOpaque(response.NextCursor)) {
		return false
	}
	for _, event := range response.Events {
		if !validTeamAuditEventResponse(event, principalID, teamID) {
			return false
		}
	}
	return true
}

func validTeamAuditEventResponse(event teamAuditEventResponse, principalID, teamID string) bool {
	if !validTeamAdminOpaque(event.EventID) || !validTeamAdminTime(event.OccurredAt) ||
		!validTeamAdminClass(event.Action) || event.ActorPrincipalID != principalID ||
		event.TeamID != teamID || !validTeamAdminClientKey(event.ClientKey) ||
		!validTeamAdminClass(event.TargetKind) || !validTeamAdminClass(event.ReasonCode) ||
		event.PolicyVersion < 1 || event.Mode != "team-remote" {
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
	return true
}

func validTeamAdminClass(value string) bool {
	return value != "" && len(value) <= 64 && teamContextTagPattern.MatchString(value)
}

func validTeamAdminClientKey(value string) bool {
	if value == "" {
		return true
	}
	return validTeamAdminDigest(value)
}

func validTeamAdminDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range []byte(value) {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validTeamAdminTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !parsed.IsZero()
}

func writeTeamAdminError(w http.ResponseWriter, status int, code string) {
	writeTeamError(w, status, code, true)
}
