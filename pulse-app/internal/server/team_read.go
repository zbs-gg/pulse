package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
	"github.com/nkkmnk/pulse/internal/teamread"
)

type teamReadApplication interface {
	Recall(context.Context, teamread.Authorization, teamread.RecallRequest) (retrieve.TeamRetrievalResponse, error)
	Context(context.Context, teamread.Authorization, teamread.ContextRequest) (retrieve.TeamContextResponse, error)
	Resume(context.Context, teamread.Authorization, teamread.ResumeRequest) (retrieve.TeamResumeResponse, error)
}

type teamRecallResponseItem struct {
	ObjectID        string   `json:"object_id"`
	Kind            string   `json:"kind"`
	RedactedSummary string   `json:"redacted_summary"`
	Confidence      float64  `json:"confidence"`
	PrivacyTier     string   `json:"privacy_tier"`
	Retention       string   `json:"retention"`
	Tags            []string `json:"tags"`
}

type teamRecallResponse struct {
	Schema        string                   `json:"schema"`
	Items         []teamRecallResponseItem `json:"items"`
	ReturnedCount int                      `json:"returned_count"`
	Fallback      bool                     `json:"fallback"`
}

type teamContextFactResponse struct {
	RootObjectID string  `json:"root_object_id"`
	ObjectID     string  `json:"object_id"`
	Text         string  `json:"text"`
	Score        float64 `json:"score"`
	Confidence   float64 `json:"confidence"`
	Domain       string  `json:"domain"`
}

type teamContextEventResponse struct {
	RootObjectID string  `json:"root_object_id"`
	ObjectID     string  `json:"object_id"`
	Title        string  `json:"title"`
	Summary      string  `json:"summary"`
	Score        float64 `json:"score"`
	Confidence   float64 `json:"confidence"`
	Domain       string  `json:"domain"`
}

type teamContextEntityResponse struct {
	RootObjectID  string  `json:"root_object_id"`
	ObjectID      string  `json:"object_id"`
	Kind          string  `json:"kind"`
	CanonicalName string  `json:"canonical_name"`
	Summary       string  `json:"summary"`
	Score         float64 `json:"score"`
	Confidence    float64 `json:"confidence"`
}

type teamContextRelationResponse struct {
	RootObjectID string  `json:"root_object_id"`
	ObjectID     string  `json:"object_id"`
	Kind         string  `json:"kind"`
	FromObjectID string  `json:"from_object_id"`
	ToObjectID   string  `json:"to_object_id"`
	Summary      string  `json:"summary"`
	Score        float64 `json:"score"`
	Confidence   float64 `json:"confidence"`
}

type teamContextAssertionResponse struct {
	RootObjectID    string  `json:"root_object_id"`
	ObjectID        string  `json:"object_id"`
	SubjectObjectID string  `json:"subject_object_id"`
	Predicate       string  `json:"predicate"`
	ObjectText      string  `json:"object_text"`
	Confidence      float64 `json:"confidence"`
}

type teamContextCountsResponse struct {
	Facts      int `json:"facts"`
	Events     int `json:"events"`
	Entities   int `json:"entities"`
	Relations  int `json:"relations"`
	Assertions int `json:"assertions"`
}

type teamContextTraceStageResponse struct {
	Kind              string   `json:"kind"`
	ReturnedObjectIDs []string `json:"returned_object_ids"`
}

type teamContextTraceResponse struct {
	Stages []teamContextTraceStageResponse `json:"stages"`
}

type teamContextResponse struct {
	Schema         string                         `json:"schema"`
	Facts          []teamContextFactResponse      `json:"facts"`
	Events         []teamContextEventResponse     `json:"events"`
	Entities       []teamContextEntityResponse    `json:"entities"`
	Relations      []teamContextRelationResponse  `json:"relations"`
	Assertions     []teamContextAssertionResponse `json:"assertions"`
	Trace          *teamContextTraceResponse      `json:"trace,omitempty"`
	ReturnedCounts teamContextCountsResponse      `json:"returned_counts"`
	Fallback       bool                           `json:"fallback"`
}

type teamResumeItemResponse struct {
	ObjectID string `json:"object_id"`
	Text     string `json:"text"`
}

type teamResumeSectionsResponse struct {
	WhereWeLeftOff                []teamResumeItemResponse `json:"where_we_left_off"`
	ActiveDecisions               []teamResumeItemResponse `json:"active_decisions"`
	OpenLoops                     []teamResumeItemResponse `json:"open_loops"`
	DoNotRepeat                   []teamResumeItemResponse `json:"do_not_repeat"`
	RelevantEmotionalStateContext []teamResumeItemResponse `json:"relevant_emotional_state_context"`
	SuggestedNextStep             []teamResumeItemResponse `json:"suggested_next_step"`
}

type teamResumeResponse struct {
	Schema        string                     `json:"schema"`
	ThreadID      string                     `json:"thread_id,omitempty"`
	Sections      teamResumeSectionsResponse `json:"sections"`
	ReturnedCount int                        `json:"returned_count"`
	Fallback      bool                       `json:"fallback"`
}

var teamContextTagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

func (s *TeamServer) handleTeamRecall(w http.ResponseWriter, r *http.Request) {
	raw, ok := readTeamDomainBody(w, r, "invalid_team_recall")
	if !ok {
		return
	}
	request, err := decodeTeamRecallRequest(raw)
	if err != nil {
		writeTeamReadError(w, http.StatusBadRequest, "invalid_team_recall")
		return
	}
	principal, ok := s.verifyTeamReadPrincipal(w, r, raw)
	if !ok {
		return
	}
	if s.cfg.ReadService == nil {
		writeTeamReadError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	result, err := s.cfg.ReadService.Recall(r.Context(), teamReadAuthorization(principal), teamread.RecallRequest{
		Query: request.Query, ActiveContext: toTeamReadActiveContext(request.ActiveContext),
		PrivacyCeiling: request.PrivacyCeiling, Retention: request.Retention, Limit: request.Limit,
	})
	if err != nil {
		writeTeamReadServiceError(w, err, "invalid_team_recall")
		return
	}
	response := teamRecallResponse{
		Schema: TeamRecallResultSchema, Items: make([]teamRecallResponseItem, 0, len(result.Items)),
	}
	for _, item := range result.Items {
		if !validTeamReadOpaque(item.RootObjectID) || !validTeamReadOutputText(item.Text, 1200) ||
			!validTeamReadScore(item.Confidence) || !validTeamReadPrivacy(item.PrivacyTier) ||
			!validTeamReadRetention(item.Retention) {
			writeTeamReadError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
			return
		}
		response.Items = append(response.Items, teamRecallResponseItem{
			ObjectID: item.RootObjectID, Kind: item.Kind, RedactedSummary: item.Text,
			Confidence: item.Confidence, PrivacyTier: item.PrivacyTier,
			Retention: item.Retention, Tags: append([]string(nil), item.Tags...),
		})
	}
	response.ReturnedCount = len(response.Items)
	writeTeamReadJSON(w, response)
}

func (s *TeamServer) handleTeamContextQuery(w http.ResponseWriter, r *http.Request) {
	raw, ok := readTeamDomainBody(w, r, "invalid_team_context")
	if !ok {
		return
	}
	request, err := decodeTeamContextRequest(raw)
	if err != nil {
		writeTeamReadError(w, http.StatusBadRequest, "invalid_team_context")
		return
	}
	principal, ok := s.verifyTeamReadPrincipal(w, r, raw)
	if !ok {
		return
	}
	if s.cfg.ReadService == nil {
		writeTeamReadError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	result, err := s.cfg.ReadService.Context(r.Context(), teamReadAuthorization(principal), teamread.ContextRequest{
		Query: request.Query, ActiveContext: toTeamReadActiveContext(request.ActiveContext),
		PrivacyCeiling: request.PrivacyCeiling, Retention: request.Retention, Limit: request.Limit,
		IncludeTrace: request.IncludeTrace, GraphMode: request.GraphMode,
	})
	if err != nil {
		writeTeamReadServiceError(w, err, "invalid_team_context")
		return
	}
	response, ok := buildTeamContextResponse(result, request)
	if !ok {
		writeTeamReadError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func (s *TeamServer) handleTeamResume(w http.ResponseWriter, r *http.Request) {
	raw, ok := readTeamDomainBody(w, r, "invalid_team_resume")
	if !ok {
		return
	}
	request, err := decodeTeamResumeRequest(raw)
	if err != nil {
		writeTeamReadError(w, http.StatusBadRequest, "invalid_team_resume")
		return
	}
	principal, ok := s.verifyTeamReadPrincipal(w, r, raw)
	if !ok {
		return
	}
	if s.cfg.ReadService == nil {
		writeTeamReadError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	result, err := s.cfg.ReadService.Resume(r.Context(), teamReadAuthorization(principal), teamread.ResumeRequest{
		ActiveContext: toTeamReadActiveContext(request.ActiveContext),
		ThreadID:      request.ThreadID, Limit: request.Limit,
	})
	if err != nil {
		writeTeamReadServiceError(w, err, "invalid_team_resume")
		return
	}
	response, ok := buildTeamResumeResponse(result, request)
	if !ok {
		writeTeamReadError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func readTeamDomainBody(w http.ResponseWriter, r *http.Request, invalidCode string) ([]byte, bool) {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Encoding") != "" {
		writeTeamReadError(w, http.StatusBadRequest, invalidCode)
		return nil, false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeTeamReadError(w, http.StatusBadRequest, invalidCode)
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, teamReadMaxBodyBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > teamReadMaxBodyBytes {
		writeTeamReadError(w, http.StatusBadRequest, invalidCode)
		return nil, false
	}
	return raw, true
}

func (s *TeamServer) verifyTeamReadPrincipal(
	w http.ResponseWriter,
	r *http.Request,
	raw []byte,
) (PrincipalContext, bool) {
	assertions := r.Header.Values("X-Pulse-Principal")
	requestIDs := r.Header.Values("X-Pulse-Request-ID")
	if len(assertions) != 1 || len(requestIDs) != 1 || assertions[0] == "" || requestIDs[0] == "" {
		writeTeamReadError(w, http.StatusUnauthorized, teamMemoryErrorInvalidPrincipal)
		return PrincipalContext{}, false
	}
	principal, err := s.cfg.PrincipalVerifier.VerifyDomainRequest(
		r.Context(), assertions[0], requestIDs[0], r.Method, r.URL.EscapedPath(), raw,
	)
	if err != nil {
		writeTeamDomainPrincipalError(w, err)
		return PrincipalContext{}, false
	}
	return principal, true
}

func teamReadAuthorization(principal PrincipalContext) teamread.Authorization {
	capabilities := make([]teamauth.Capability, len(principal.Capabilities))
	for index, capability := range principal.Capabilities {
		capabilities[index] = teamauth.Capability(capability)
	}
	return teamread.Authorization{
		PrincipalID: principal.PrincipalID, TeamID: principal.TeamID, Capabilities: capabilities,
	}
}

func toTeamReadActiveContext(active teamReadActiveContext) teamread.ActiveContext {
	return teamread.ActiveContext{
		ProjectID: active.ProjectID, RepoID: active.RepoID,
		AgentID: active.AgentID, SessionID: active.SessionID,
	}
}

func buildTeamContextResponse(
	result retrieve.TeamContextResponse,
	request teamContextRequest,
) (teamContextResponse, bool) {
	response := teamContextResponse{
		Schema:     TeamContextResultSchema,
		Facts:      make([]teamContextFactResponse, 0, len(result.Facts)),
		Events:     make([]teamContextEventResponse, 0, len(result.Events)),
		Entities:   make([]teamContextEntityResponse, 0, len(result.Entities)),
		Relations:  make([]teamContextRelationResponse, 0, len(result.Relations)),
		Assertions: make([]teamContextAssertionResponse, 0, len(result.Assertions)),
	}
	returned := make(map[string]bool)
	entityIDs := make(map[string]bool)
	addID := func(rootID, objectID string) bool {
		if !validTeamReadOpaque(rootID) || !validTeamReadOpaque(objectID) || returned[objectID] {
			return false
		}
		returned[objectID] = true
		return true
	}
	for _, item := range result.Entities {
		if !addID(item.RootObjectID, item.ObjectID) || !teamContextTagPattern.MatchString(item.EntityKind) ||
			!validTeamReadOutputText(item.Name, 160) || !validTeamReadOutputText(item.Summary, 1200) ||
			!validTeamReadScore(item.Score) || !validTeamReadScore(item.Confidence) {
			return teamContextResponse{}, false
		}
		entityIDs[item.ObjectID] = true
		response.Entities = append(response.Entities, teamContextEntityResponse{
			RootObjectID: item.RootObjectID, ObjectID: item.ObjectID, Kind: item.EntityKind,
			CanonicalName: item.Name, Summary: item.Summary,
			Score: item.Score, Confidence: item.Confidence,
		})
	}
	for _, item := range result.Facts {
		if !addID(item.RootObjectID, item.ObjectID) || !validTeamReadOutputText(item.Text, 1200) ||
			!validTeamReadScore(item.Score) || !validTeamReadScore(item.Confidence) ||
			!validTeamGraphDomain(item.Domain) {
			return teamContextResponse{}, false
		}
		response.Facts = append(response.Facts, teamContextFactResponse{
			RootObjectID: item.RootObjectID, ObjectID: item.ObjectID, Text: item.Text,
			Score: item.Score, Confidence: item.Confidence, Domain: item.Domain,
		})
	}
	for _, item := range result.Events {
		if !addID(item.RootObjectID, item.ObjectID) || !validTeamReadOutputText(item.Title, 180) ||
			!validTeamReadOutputText(item.Summary, 1200) || !validTeamReadScore(item.Score) ||
			!validTeamReadScore(item.Confidence) || !validTeamGraphDomain(item.Domain) {
			return teamContextResponse{}, false
		}
		response.Events = append(response.Events, teamContextEventResponse{
			RootObjectID: item.RootObjectID, ObjectID: item.ObjectID,
			Title: item.Title, Summary: item.Summary, Score: item.Score,
			Confidence: item.Confidence, Domain: item.Domain,
		})
	}
	for _, item := range result.Relations {
		if !addID(item.RootObjectID, item.ObjectID) || !teamContextTagPattern.MatchString(item.RelationKind) ||
			!entityIDs[item.FromObjectID] || !entityIDs[item.ToObjectID] ||
			!validTeamReadOutputText(item.Summary, 1200) || !validTeamReadScore(item.Score) ||
			!validTeamReadScore(item.Confidence) {
			return teamContextResponse{}, false
		}
		response.Relations = append(response.Relations, teamContextRelationResponse{
			RootObjectID: item.RootObjectID, ObjectID: item.ObjectID, Kind: item.RelationKind,
			FromObjectID: item.FromObjectID, ToObjectID: item.ToObjectID,
			Summary: item.Summary, Score: item.Score, Confidence: item.Confidence,
		})
	}
	for _, item := range result.Assertions {
		if !addID(item.RootObjectID, item.ObjectID) || !entityIDs[item.SubjectObjectID] ||
			!teamContextTagPattern.MatchString(item.Predicate) ||
			!validTeamReadOutputText(item.ObjectText, 400) || !validTeamReadScore(item.Confidence) {
			return teamContextResponse{}, false
		}
		response.Assertions = append(response.Assertions, teamContextAssertionResponse{
			RootObjectID: item.RootObjectID, ObjectID: item.ObjectID,
			SubjectObjectID: item.SubjectObjectID, Predicate: item.Predicate,
			ObjectText: item.ObjectText, Confidence: item.Confidence,
		})
	}
	response.ReturnedCounts = teamContextCountsResponse{
		Facts: len(response.Facts), Events: len(response.Events), Entities: len(response.Entities),
		Relations: len(response.Relations), Assertions: len(response.Assertions),
	}
	if len(returned) > request.Limit {
		return teamContextResponse{}, false
	}
	if request.IncludeTrace {
		trace := teamContextTraceResponse{Stages: make([]teamContextTraceStageResponse, 0, 4)}
		stages := []struct {
			kind string
			use  func(retrieve.TeamContextTrace) bool
		}{
			{kind: "lexical", use: func(item retrieve.TeamContextTrace) bool { return item.Lexical > 0 }},
			{kind: "vector", use: func(item retrieve.TeamContextTrace) bool { return item.Cosine > 0 }},
			{kind: "graph", use: func(item retrieve.TeamContextTrace) bool { return item.Graph > 0 }},
			{kind: "assertion", use: func(item retrieve.TeamContextTrace) bool { return item.AssertionState != "" }},
		}
		for _, stage := range stages {
			ids := make([]string, 0)
			seen := make(map[string]bool)
			for _, item := range result.Trace {
				if returned[item.ObjectID] && stage.use(item) && !seen[item.ObjectID] {
					seen[item.ObjectID] = true
					ids = append(ids, item.ObjectID)
				}
			}
			if len(ids) > 0 {
				sort.Strings(ids)
				trace.Stages = append(trace.Stages, teamContextTraceStageResponse{
					Kind: stage.kind, ReturnedObjectIDs: ids,
				})
			}
		}
		response.Trace = &trace
	}
	return response, true
}

func buildTeamResumeResponse(
	result retrieve.TeamResumeResponse,
	request teamResumeRequest,
) (teamResumeResponse, bool) {
	response := teamResumeResponse{
		Schema: TeamResumeResultSchema, ThreadID: request.ThreadID,
		Sections: teamResumeSectionsResponse{
			WhereWeLeftOff:                make([]teamResumeItemResponse, 0),
			ActiveDecisions:               make([]teamResumeItemResponse, 0),
			OpenLoops:                     make([]teamResumeItemResponse, 0),
			DoNotRepeat:                   make([]teamResumeItemResponse, 0),
			RelevantEmotionalStateContext: make([]teamResumeItemResponse, 0),
			SuggestedNextStep:             make([]teamResumeItemResponse, 0),
		},
	}
	appendItems := func(destination *[]teamResumeItemResponse, items []retrieve.TeamResumeItem) bool {
		for _, item := range items {
			if !validTeamReadOpaque(item.RootObjectID) || !validTeamReadOutputText(item.Text, 1200) {
				return false
			}
			*destination = append(*destination, teamResumeItemResponse{
				ObjectID: item.RootObjectID, Text: item.Text,
			})
			response.ReturnedCount++
			if response.ReturnedCount > request.Limit {
				return false
			}
		}
		return true
	}
	if !appendItems(&response.Sections.WhereWeLeftOff, result.WhereWeLeftOff) ||
		!appendItems(&response.Sections.ActiveDecisions, result.ActiveDecisions) ||
		!appendItems(&response.Sections.OpenLoops, result.OpenLoops) ||
		!appendItems(&response.Sections.DoNotRepeat, result.DoNotRepeat) ||
		!appendItems(&response.Sections.RelevantEmotionalStateContext, result.RelevantEmotionalStateContext) ||
		!appendItems(&response.Sections.SuggestedNextStep, result.SuggestedNextStep) {
		return teamResumeResponse{}, false
	}
	if result.ReturnedCount != response.ReturnedCount {
		return teamResumeResponse{}, false
	}
	return response, true
}

func validTeamReadOpaque(value string) bool {
	return safeOpaque(value, 255)
}

func validTeamReadOutputText(value string, maximum int) bool {
	return strings.TrimSpace(value) == value && validTeamGraphTransportText(value, maximum)
}

func validTeamReadScore(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func writeTeamReadServiceError(w http.ResponseWriter, err error, invalidCode string) {
	status, code := http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable
	switch {
	case errors.Is(err, teamread.ErrInvalidRequest), errors.Is(err, retrieve.ErrInvalidTeamRetrievalRequest),
		errors.Is(err, store.ErrInvalidTeamReadQuery):
		status, code = http.StatusBadRequest, invalidCode
	case errors.Is(err, store.ErrConcealedNotFound):
		status, code = http.StatusNotFound, teamMemoryErrorNotFound
	case errors.Is(err, store.ErrPrincipalRevoked):
		status, code = http.StatusForbidden, teamMemoryErrorPrincipalRevoked
	case errors.Is(err, store.ErrTeamPolicyDenied):
		status, code = http.StatusForbidden, teamMemoryErrorPolicyDenied
	case errors.Is(err, store.ErrTeamPolicyEpochChanged):
		status, code = http.StatusConflict, teamMemoryErrorAuthorizationStale
	}
	writeTeamReadError(w, status, code)
}

func writeTeamReadError(w http.ResponseWriter, status int, code string) {
	writeTeamError(w, status, code, true)
}

func writeTeamReadJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
