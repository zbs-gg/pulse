package server

import (
	"errors"
	"strings"
)

const (
	TeamRecallRoutePath       = "/team/v1/recall"
	TeamContextQueryRoutePath = "/team/v1/context/query"
	TeamResumeRoutePath       = "/team/v1/resume"

	TeamRecallSchema        = "pulse.team.recall.v1"
	TeamRecallResultSchema  = "pulse.team.recall_result.v1"
	TeamContextSchema       = "pulse.team.context.v1"
	TeamContextResultSchema = "pulse.team.context_result.v1"
	TeamResumeSchema        = "pulse.team.resume.v1"
	TeamResumeResultSchema  = "pulse.team.resume_result.v1"

	teamReadMaxBodyBytes = 64 << 10
)

var errInvalidTeamReadContract = errors.New("invalid team read contract")

type teamReadActiveContext struct {
	ProjectID string `json:"project_id,omitempty"`
	RepoID    string `json:"repo_id,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type teamRecallRequest struct {
	Schema         string
	Query          string
	ActiveContext  teamReadActiveContext
	PrivacyCeiling string
	Retention      string
	Limit          int
}

type teamContextRequest struct {
	Schema         string
	Query          string
	ActiveContext  teamReadActiveContext
	PrivacyCeiling string
	Retention      string
	Limit          int
	IncludeTrace   bool
	GraphMode      string
}

type teamResumeRequest struct {
	Schema        string
	ActiveContext teamReadActiveContext
	ThreadID      string
	Limit         int
}

type teamRecallEnvelope struct {
	Schema         *string                `json:"schema"`
	Query          *string                `json:"query"`
	ActiveContext  *teamReadActiveContext `json:"active_context"`
	PrivacyCeiling *string                `json:"privacy_ceiling"`
	Retention      *string                `json:"retention,omitempty"`
	Limit          *int                   `json:"limit,omitempty"`
}

type teamContextEnvelope struct {
	Schema         *string                `json:"schema"`
	Query          *string                `json:"query"`
	ActiveContext  *teamReadActiveContext `json:"active_context"`
	PrivacyCeiling *string                `json:"privacy_ceiling"`
	Retention      *string                `json:"retention,omitempty"`
	Limit          *int                   `json:"limit,omitempty"`
	IncludeTrace   *bool                  `json:"include_trace,omitempty"`
	GraphMode      *string                `json:"graph_mode,omitempty"`
}

type teamResumeEnvelope struct {
	Schema        *string                `json:"schema"`
	ActiveContext *teamReadActiveContext `json:"active_context"`
	ThreadID      *string                `json:"thread_id,omitempty"`
	Limit         *int                   `json:"limit,omitempty"`
}

func decodeTeamRecallRequest(raw []byte) (teamRecallRequest, error) {
	var envelope teamRecallEnvelope
	if !decodeTeamReadEnvelope(raw, &envelope) || envelope.Schema == nil ||
		envelope.Query == nil || envelope.ActiveContext == nil || envelope.PrivacyCeiling == nil {
		return teamRecallRequest{}, errInvalidTeamReadContract
	}
	limit, ok := teamReadLimit(envelope.Limit, 5)
	retention := optionalTeamReadString(envelope.Retention)
	query := strings.TrimSpace(*envelope.Query)
	if *envelope.Schema != TeamRecallSchema || !validTeamGraphTransportText(query, 1200) ||
		!validTeamReadContext(*envelope.ActiveContext) || !validTeamReadPrivacy(*envelope.PrivacyCeiling) ||
		!validTeamReadRetention(retention) || !ok {
		return teamRecallRequest{}, errInvalidTeamReadContract
	}
	return teamRecallRequest{
		Schema: *envelope.Schema, Query: query, ActiveContext: *envelope.ActiveContext,
		PrivacyCeiling: *envelope.PrivacyCeiling, Retention: retention, Limit: limit,
	}, nil
}

func decodeTeamContextRequest(raw []byte) (teamContextRequest, error) {
	var envelope teamContextEnvelope
	if !decodeTeamReadEnvelope(raw, &envelope) || envelope.Schema == nil ||
		envelope.Query == nil || envelope.ActiveContext == nil || envelope.PrivacyCeiling == nil {
		return teamContextRequest{}, errInvalidTeamReadContract
	}
	limit, ok := teamReadLimit(envelope.Limit, 10)
	retention := optionalTeamReadString(envelope.Retention)
	query := strings.TrimSpace(*envelope.Query)
	includeTrace := false
	if envelope.IncludeTrace != nil {
		includeTrace = *envelope.IncludeTrace
	}
	graphMode := "anchored"
	if envelope.GraphMode != nil {
		graphMode = *envelope.GraphMode
	}
	if *envelope.Schema != TeamContextSchema || !validTeamGraphTransportText(query, 1200) ||
		!validTeamReadContext(*envelope.ActiveContext) || !validTeamReadPrivacy(*envelope.PrivacyCeiling) ||
		!validTeamReadRetention(retention) || !validTeamReadGraphMode(graphMode) || !ok {
		return teamContextRequest{}, errInvalidTeamReadContract
	}
	return teamContextRequest{
		Schema: *envelope.Schema, Query: query, ActiveContext: *envelope.ActiveContext,
		PrivacyCeiling: *envelope.PrivacyCeiling, Retention: retention, Limit: limit,
		IncludeTrace: includeTrace, GraphMode: graphMode,
	}, nil
}

func decodeTeamResumeRequest(raw []byte) (teamResumeRequest, error) {
	var envelope teamResumeEnvelope
	if !decodeTeamReadEnvelope(raw, &envelope) || envelope.Schema == nil || envelope.ActiveContext == nil {
		return teamResumeRequest{}, errInvalidTeamReadContract
	}
	limit, ok := teamReadLimit(envelope.Limit, 20)
	threadID := optionalTeamReadString(envelope.ThreadID)
	if *envelope.Schema != TeamResumeSchema || !validTeamReadContext(*envelope.ActiveContext) || !ok ||
		(threadID != "" && !safeOpaque(threadID, 255)) ||
		(threadID == "" && envelope.ActiveContext.ProjectID == "" && envelope.ActiveContext.SessionID == "") {
		return teamResumeRequest{}, errInvalidTeamReadContract
	}
	return teamResumeRequest{
		Schema: *envelope.Schema, ActiveContext: *envelope.ActiveContext,
		ThreadID: threadID, Limit: limit,
	}, nil
}

func decodeTeamReadEnvelope(raw []byte, target any) bool {
	return len(raw) > 0 && len(raw) <= teamReadMaxBodyBytes &&
		decodeStrictJSON(raw, target) == nil && !teamGraphJSONContainsNull(raw)
}

func teamReadLimit(value *int, fallback int) (int, bool) {
	if value == nil {
		return fallback, true
	}
	return *value, *value >= 1 && *value <= 50
}

func optionalTeamReadString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validTeamReadContext(context teamReadActiveContext) bool {
	for _, value := range []string{context.ProjectID, context.RepoID, context.AgentID, context.SessionID} {
		if value != "" && !safeOpaque(value, 255) {
			return false
		}
	}
	return true
}

func validTeamReadPrivacy(value string) bool {
	switch value {
	case "normal", "sensitive", "private":
		return true
	default:
		return false
	}
}

func validTeamReadRetention(value string) bool {
	switch value {
	case "", "session", "project", "long_term":
		return true
	default:
		return false
	}
}

func validTeamReadGraphMode(value string) bool {
	switch value {
	case "off", "anchored", "walk":
		return true
	default:
		return false
	}
}
