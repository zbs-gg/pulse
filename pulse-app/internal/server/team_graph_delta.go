package server

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const (
	TeamGraphDeltaRoutePath    = "/team/v1/graph/delta"
	TeamGraphDeltaResultSchema = "pulse.team.graph_delta_result.v1"
	teamGraphDeltaMaxBodyBytes = 256 << 10

	teamGraphDeltaErrorInvalid = "invalid_team_graph_delta"
)

type teamGraphDeltaEnvelope struct {
	Schema           *string                         `json:"schema"`
	Source           *store.CapsuleSource            `json:"source"`
	Nodes            *[]store.TeamGraphNode          `json:"nodes"`
	Edges            *[]store.TeamGraphEdge          `json:"edges"`
	Facts            *[]store.TeamGraphFact          `json:"facts"`
	Events           *[]store.TeamGraphEvent         `json:"events"`
	Continuity       *store.TeamGraphContinuity      `json:"continuity,omitempty"`
	RawInputIncluded *bool                           `json:"raw_input_included"`
	ActiveContext    *teamGraphActiveContextEnvelope `json:"active_context"`
	TargetScope      *teamGraphTargetEnvelope        `json:"target_scope,omitempty"`
	PrivacyTier      *string                         `json:"privacy_tier"`
	Retention        *string                         `json:"retention"`
	ExpiresAt        *string                         `json:"expires_at,omitempty"`
	IdempotencyKey   *string                         `json:"idempotency_key"`
}

type teamGraphActiveContextEnvelope struct {
	ProjectID *string `json:"project_id,omitempty"`
	RepoID    *string `json:"repo_id,omitempty"`
	AgentID   *string `json:"agent_id,omitempty"`
	SessionID *string `json:"session_id,omitempty"`
}

type teamGraphTargetEnvelope struct {
	Type *teamauth.ScopeType `json:"type"`
	ID   *string             `json:"id,omitempty"`
}

type teamGraphDeltaProjectionJobResponse struct {
	Kind  string `json:"kind"`
	JobID string `json:"job_id"`
	State string `json:"state"`
}

type teamGraphDeltaResponse struct {
	Schema          string                                `json:"schema"`
	ObjectID        string                                `json:"object_id"`
	AuditEventID    string                                `json:"audit_event_id"`
	Status          string                                `json:"status"`
	ProjectionState string                                `json:"projection_state"`
	ProjectionJobs  []teamGraphDeltaProjectionJobResponse `json:"projection_jobs"`
	FullyProjected  bool                                  `json:"fully_projected"`
	Replayed        bool                                  `json:"replayed"`
	Fallback        bool                                  `json:"fallback"`
}

func (s *TeamServer) handleTeamGraphDelta(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeTeamGraphDeltaError(w, http.StatusBadRequest, teamGraphDeltaErrorInvalid)
		return
	}
	if r.Header.Get("Content-Encoding") != "" {
		writeTeamGraphDeltaError(w, http.StatusUnsupportedMediaType, teamGraphDeltaErrorInvalid)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeTeamGraphDeltaError(w, http.StatusUnsupportedMediaType, teamGraphDeltaErrorInvalid)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, teamGraphDeltaMaxBodyBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > teamGraphDeltaMaxBodyBytes {
		status := http.StatusBadRequest
		if len(raw) > teamGraphDeltaMaxBodyBytes {
			status = http.StatusRequestEntityTooLarge
		}
		writeTeamGraphDeltaError(w, status, teamGraphDeltaErrorInvalid)
		return
	}
	write, err := decodeTeamGraphDeltaEnvelope(raw)
	if err != nil {
		writeTeamGraphDeltaError(w, http.StatusBadRequest, teamGraphDeltaErrorInvalid)
		return
	}
	assertions := r.Header.Values("X-Pulse-Principal")
	requestIDs := r.Header.Values("X-Pulse-Request-ID")
	if len(assertions) != 1 || len(requestIDs) != 1 || assertions[0] == "" || requestIDs[0] == "" {
		writeTeamGraphDeltaError(w, http.StatusUnauthorized, teamMemoryErrorInvalidPrincipal)
		return
	}
	principal, err := s.cfg.PrincipalVerifier.VerifyDomainRequest(
		r.Context(), assertions[0], requestIDs[0], r.Method, r.URL.EscapedPath(), raw,
	)
	if err != nil {
		writeTeamDomainPrincipalError(w, err)
		return
	}

	capabilities := make([]teamauth.Capability, len(principal.Capabilities))
	for index, capability := range principal.Capabilities {
		capabilities[index] = teamauth.Capability(capability)
	}
	active := teamauth.ActiveContext{
		TeamID: principal.TeamID, ProjectID: write.ActiveContext.ProjectID,
		RepoID: write.ActiveContext.RepoID, AgentID: write.ActiveContext.AgentID,
		SessionID: write.ActiveContext.SessionID,
	}
	var requestedScope *teamauth.CanonicalScope
	if write.TargetScope != nil {
		requestedScope = &teamauth.CanonicalScope{Type: write.TargetScope.Type, ID: write.TargetScope.ID}
	}
	permit, err := s.cfg.Store.AuthorizeTeamMutation(r.Context(), store.TeamMutationAuthorizationRequest{
		PrincipalID: principal.PrincipalID, OAuthClientKey: principal.OAuthClientKey,
		Action: teamauth.ActionWrite, Capabilities: capabilities, Context: active,
		ObjectKind: "graph_delta", RequestedScope: requestedScope,
	})
	if err != nil {
		writeTeamGraphDeltaStoreError(w, err)
		return
	}
	result, err := s.cfg.Store.StoreTeamGraphDelta(
		r.Context(), permit,
		store.TeamWriterLeaseIdentity{WriterID: s.cfg.WriterLease.WriterID, Token: s.cfg.WriterLease.Token},
		principal.RequestID, principal.OAuthClientKey, write,
	)
	if err != nil {
		writeTeamGraphDeltaStoreError(w, err)
		return
	}
	response := teamGraphDeltaResponse{
		Schema: TeamGraphDeltaResultSchema, ObjectID: result.ObjectID, AuditEventID: result.AuditEventID,
		Status: result.Status, ProjectionState: result.ProjectionState,
		FullyProjected: result.FullyProjected, Replayed: result.Replayed, Fallback: false,
		ProjectionJobs: make([]teamGraphDeltaProjectionJobResponse, len(result.ProjectionJobs)),
	}
	for index, job := range result.ProjectionJobs {
		response.ProjectionJobs[index] = teamGraphDeltaProjectionJobResponse{
			Kind: job.Kind, JobID: job.JobID, State: job.State,
		}
	}
	sort.Slice(response.ProjectionJobs, func(left, right int) bool {
		return response.ProjectionJobs[left].Kind < response.ProjectionJobs[right].Kind
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func decodeTeamGraphDeltaEnvelope(raw []byte) (store.TeamGraphDeltaWrite, error) {
	var envelope teamGraphDeltaEnvelope
	if len(raw) == 0 || len(raw) > teamGraphDeltaMaxBodyBytes ||
		decodeStrictJSON(raw, &envelope) != nil || teamGraphJSONContainsNull(raw) ||
		envelope.Schema == nil || envelope.Source == nil || envelope.Nodes == nil ||
		envelope.Edges == nil || envelope.Facts == nil || envelope.Events == nil ||
		envelope.RawInputIncluded == nil || envelope.ActiveContext == nil ||
		envelope.PrivacyTier == nil || envelope.Retention == nil || envelope.IdempotencyKey == nil {
		return store.TeamGraphDeltaWrite{}, store.ErrTeamGraphDeltaInvalid
	}
	active, activeOK := teamGraphActiveContextFromEnvelope(*envelope.ActiveContext)
	target, targetOK := teamGraphTargetFromEnvelope(envelope.TargetScope)
	if !activeOK || !targetOK {
		return store.TeamGraphDeltaWrite{}, store.ErrTeamGraphDeltaInvalid
	}
	write := store.TeamGraphDeltaWrite{
		Schema: *envelope.Schema, Source: *envelope.Source,
		Nodes: *envelope.Nodes, Edges: *envelope.Edges, Facts: *envelope.Facts, Events: *envelope.Events,
		Continuity: envelope.Continuity, RawInputIncluded: *envelope.RawInputIncluded,
		ActiveContext: active, TargetScope: target,
		PrivacyTier: *envelope.PrivacyTier, Retention: *envelope.Retention,
		ExpiresAt: envelope.ExpiresAt, IdempotencyKey: *envelope.IdempotencyKey,
	}
	if !validTeamGraphDeltaTransport(write) {
		return store.TeamGraphDeltaWrite{}, store.ErrTeamGraphDeltaInvalid
	}
	return write, nil
}

func teamGraphActiveContextFromEnvelope(
	envelope teamGraphActiveContextEnvelope,
) (store.TeamGraphActiveContext, bool) {
	active := store.TeamGraphActiveContext{}
	fields := []struct {
		source *string
		target *string
	}{
		{source: envelope.ProjectID, target: &active.ProjectID},
		{source: envelope.RepoID, target: &active.RepoID},
		{source: envelope.AgentID, target: &active.AgentID},
		{source: envelope.SessionID, target: &active.SessionID},
	}
	for _, field := range fields {
		if field.source == nil {
			continue
		}
		canonical, ok := teamGraphTransportOpaque(*field.source, 1, 255)
		if !ok {
			return store.TeamGraphActiveContext{}, false
		}
		*field.target = canonical
	}
	return active, true
}

func teamGraphTargetFromEnvelope(envelope *teamGraphTargetEnvelope) (*store.TeamGraphTarget, bool) {
	if envelope == nil || envelope.Type == nil {
		return nil, envelope == nil
	}
	targetType := teamauth.ScopeType(strings.TrimSpace(string(*envelope.Type)))
	if targetType == teamauth.ScopePersonal {
		if envelope.ID != nil {
			return nil, false
		}
		return &store.TeamGraphTarget{Type: targetType}, true
	}
	if envelope.ID == nil {
		return nil, false
	}
	canonicalID, ok := teamGraphTransportOpaque(*envelope.ID, 1, 255)
	if !ok {
		return nil, false
	}
	switch targetType {
	case teamauth.ScopeProject, teamauth.ScopeRepo, teamauth.ScopeAgent, teamauth.ScopeSession:
		return &store.TeamGraphTarget{Type: targetType, ID: canonicalID}, true
	default:
		return nil, false
	}
}

func teamGraphJSONContainsNull(raw []byte) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	var containsNull func(any) bool
	containsNull = func(current any) bool {
		switch typed := current.(type) {
		case nil:
			return true
		case []any:
			for _, item := range typed {
				if containsNull(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range typed {
				if containsNull(item) {
					return true
				}
			}
		}
		return false
	}
	return containsNull(value)
}

func validTeamGraphDeltaTransport(write store.TeamGraphDeltaWrite) bool {
	if write.Schema != store.TeamGraphDeltaSchema || write.RawInputIncluded ||
		write.Nodes == nil || write.Edges == nil || write.Facts == nil || write.Events == nil ||
		len(write.Nodes) > 30 || len(write.Edges) > 50 || len(write.Facts) > 50 || len(write.Events) > 20 ||
		(len(write.Nodes) == 0 && len(write.Edges) == 0 && len(write.Facts) == 0 &&
			len(write.Events) == 0 && write.Continuity == nil) ||
		!validTeamGraphSource(write.Source) ||
		!validTeamMemoryPolicy(strings.TrimSpace(write.PrivacyTier), strings.TrimSpace(write.Retention)) ||
		!validTeamGraphOpaque(write.IdempotencyKey, 8, 255) {
		return false
	}
	if write.ExpiresAt != nil {
		if _, ok := parseTeamMemoryTime(strings.TrimSpace(*write.ExpiresAt)); !ok {
			return false
		}
	}

	nodeRefs := make(map[string]struct{}, len(write.Nodes))
	for _, node := range write.Nodes {
		clientID, ok := teamGraphTransportRef(node.ClientID)
		if !ok || !validTeamGraphNodeKind(strings.TrimSpace(node.Kind)) ||
			!validTeamGraphTransportText(node.CanonicalName, 160) ||
			!validTeamGraphDomain(strings.TrimSpace(node.Domain)) || len(node.Aliases) > 20 ||
			!validOptionalTeamGraphTransportText(node.Summary, 1200) ||
			!validOptionalTeamGraphScore(node.Salience) || !validOptionalTeamGraphScore(node.EmotionalWeight) {
			return false
		}
		if _, duplicate := nodeRefs[clientID]; duplicate {
			return false
		}
		nodeRefs[clientID] = struct{}{}
		for _, alias := range node.Aliases {
			if !validTeamGraphTransportText(alias, 160) {
				return false
			}
		}
	}

	for _, edge := range write.Edges {
		from, fromOK := teamGraphTransportRef(edge.From)
		to, toOK := teamGraphTransportRef(edge.To)
		_, fromExists := nodeRefs[from]
		_, toExists := nodeRefs[to]
		kind := strings.TrimSpace(edge.Kind)
		if !fromOK || !toOK || !fromExists || !toExists ||
			!teamMemoryTagPattern.MatchString(kind) || utf8.RuneCountInString(kind) > 64 ||
			!validOptionalTeamGraphTransportText(edge.Summary, 1200) ||
			!validOptionalTeamGraphScore(edge.Strength) {
			return false
		}
	}

	eventRefs := make(map[string]struct{}, len(write.Events))
	for _, event := range write.Events {
		clientID, ok := teamGraphTransportRef(event.ClientID)
		if !ok || !validTeamGraphTransportText(event.Title, 180) ||
			!validTeamGraphTransportText(event.Summary, 1200) ||
			!validRequiredTeamGraphScore(event.Confidence) ||
			!validOptionalTeamGraphScore(event.EmotionalWeight) ||
			!validTeamGraphDomain(strings.TrimSpace(event.Domain)) || len(event.EntityRefs) > 20 ||
			!validOptionalTeamGraphTransportText(event.Sentiment, 240) ||
			!validTeamGraphBiometrics(event.Biometrics) || !validTeamGraphEmotions(event.Emotions) {
			return false
		}
		if event.OccurredAt != nil {
			if _, valid := parseTeamMemoryTime(strings.TrimSpace(*event.OccurredAt)); !valid {
				return false
			}
		}
		if _, duplicate := eventRefs[clientID]; duplicate {
			return false
		}
		eventRefs[clientID] = struct{}{}
		for _, ref := range event.EntityRefs {
			canonical, valid := teamGraphTransportRef(ref)
			if _, exists := nodeRefs[canonical]; !valid || !exists {
				return false
			}
		}
	}

	for _, fact := range write.Facts {
		node, ok := teamGraphTransportRef(fact.Node)
		_, nodeExists := nodeRefs[node]
		hasPredicate := fact.Predicate != nil
		hasObject := fact.ObjectText != nil
		hasClaimMetadata := fact.ValidFrom != nil || fact.ChangeCue != nil || fact.SourceEventRefs != nil
		if !ok || !nodeExists || !validTeamGraphTransportText(fact.Text, 1200) ||
			!validRequiredTeamGraphScore(fact.Confidence) ||
			!validTeamGraphDomain(strings.TrimSpace(fact.Domain)) || hasPredicate != hasObject ||
			(!hasPredicate && hasClaimMetadata) || len(fact.SourceEventRefs) > 20 {
			return false
		}
		if hasPredicate && (!validTeamGraphTransportText(*fact.Predicate, 120) ||
			!validTeamGraphTransportText(*fact.ObjectText, 400)) {
			return false
		}
		if fact.ValidFrom != nil {
			if _, valid := parseTeamMemoryTime(strings.TrimSpace(*fact.ValidFrom)); !valid {
				return false
			}
		}
		for _, ref := range fact.SourceEventRefs {
			canonical, valid := teamGraphTransportRef(ref)
			if _, exists := eventRefs[canonical]; !valid || !exists {
				return false
			}
		}
	}
	return validTeamGraphContinuity(write.Continuity, write.ActiveContext, write.TargetScope)
}

func validTeamGraphSource(source store.CapsuleSource) bool {
	source.Host = strings.TrimSpace(source.Host)
	source.ConversationScope = strings.TrimSpace(source.ConversationScope)
	source.Timestamp = strings.TrimSpace(source.Timestamp)
	return validTeamMemorySource(source)
}

func validTeamGraphContinuity(
	continuity *store.TeamGraphContinuity,
	active store.TeamGraphActiveContext,
	target *store.TeamGraphTarget,
) bool {
	if continuity == nil {
		return true
	}
	threadID, threadOK := teamGraphTransportOpaque(continuity.ThreadID, 1, 96)
	sessionID, sessionOK := teamGraphTransportOpaque(continuity.SessionID, 1, 96)
	activeSession, activeOK := teamGraphTransportOpaque(active.SessionID, 1, 255)
	if !threadOK || !sessionOK || !activeOK || threadID == "" || activeSession != sessionID ||
		!validTeamGraphTransportText(continuity.Summary, 1200) {
		return false
	}
	if target != nil && teamauth.ScopeType(strings.TrimSpace(string(target.Type))) == teamauth.ScopeSession {
		targetID, ok := teamGraphTransportOpaque(target.ID, 1, 255)
		if !ok || targetID != sessionID {
			return false
		}
	}
	for _, values := range [][]string{
		continuity.Decisions, continuity.OpenLoops, continuity.DoNotRepeat,
		continuity.EmotionalAnchors, continuity.StateSignals, continuity.ActiveThreads,
		continuity.ReviewInsights,
	} {
		if len(values) > 20 {
			return false
		}
		for _, value := range values {
			if !validTeamGraphTransportText(value, 1200) {
				return false
			}
		}
	}
	return true
}

func validTeamGraphBiometrics(biometrics *store.TeamGraphBiometrics) bool {
	if biometrics == nil {
		return true
	}
	if !validOptionalTeamGraphNumber(biometrics.HRV, 0, 300) ||
		!validOptionalTeamGraphScore(biometrics.SleepQuality) ||
		!validOptionalTeamGraphScore(biometrics.StressProxy) {
		return false
	}
	for _, trend := range []*string{biometrics.HRTrend, biometrics.HRVTrend} {
		if trend != nil {
			value := strings.TrimSpace(*trend)
			if value != "rising" && value != "stable" && value != "falling" {
				return false
			}
		}
	}
	return true
}

func validTeamGraphEmotions(emotions map[string]*float64) bool {
	for name, value := range emotions {
		if !validTeamGraphEmotion(name) || !validRequiredTeamGraphScore(value) {
			return false
		}
	}
	return true
}

func validTeamGraphEmotion(value string) bool {
	switch value {
	case "joy", "sadness", "anger", "fear", "trust", "disgust",
		"anticipation", "surprise", "shame", "guilt":
		return true
	default:
		return false
	}
}

func validTeamGraphNodeKind(value string) bool {
	switch value {
	case "person", "place", "project", "org", "product", "community",
		"skill", "concept", "thing", "event_series":
		return true
	default:
		return false
	}
}

func validTeamGraphDomain(value string) bool {
	switch value {
	case "real", "fiction_content", "fiction_meta", "meta_authorial":
		return true
	default:
		return false
	}
}

func validTeamGraphTransportText(value string, maximum int) bool {
	clean := strings.TrimSpace(value)
	return clean != "" && utf8.ValidString(clean) && utf8.RuneCountInString(clean) <= maximum &&
		!strings.ContainsRune(clean, '\u2028') && !strings.ContainsRune(clean, '\u2029')
}

func validOptionalTeamGraphTransportText(value *string, maximum int) bool {
	return value == nil || validTeamGraphTransportText(*value, maximum)
}

func teamGraphTransportOpaque(value string, minimum, maximum int) (string, bool) {
	clean := strings.TrimSpace(value)
	return clean, len(clean) >= minimum && safeOpaque(clean, maximum)
}

func validTeamGraphOpaque(value string, minimum, maximum int) bool {
	_, ok := teamGraphTransportOpaque(value, minimum, maximum)
	return ok
}

func teamGraphTransportRef(value string) (string, bool) {
	return teamGraphTransportOpaque(value, 2, 96)
}

func validOptionalTeamGraphScore(value *float64) bool {
	return value == nil || validTeamGraphNumber(*value, 0, 1)
}

func validRequiredTeamGraphScore(value *float64) bool {
	return value != nil && validTeamGraphNumber(*value, 0, 1)
}

func validOptionalTeamGraphNumber(value *float64, minimum, maximum float64) bool {
	return value == nil || validTeamGraphNumber(*value, minimum, maximum)
}

func validTeamGraphNumber(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func writeTeamGraphDeltaStoreError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable
	switch {
	case errors.Is(err, store.ErrTeamGraphDeltaInvalid), errors.Is(err, store.ErrTeamObjectInvalid):
		status, code = http.StatusBadRequest, teamGraphDeltaErrorInvalid
	case errors.Is(err, store.ErrConcealedNotFound):
		status, code = http.StatusNotFound, teamMemoryErrorNotFound
	case errors.Is(err, store.ErrPrincipalRevoked):
		status, code = http.StatusForbidden, teamMemoryErrorPrincipalRevoked
	case errors.Is(err, store.ErrTeamPolicyDenied):
		status, code = http.StatusForbidden, teamMemoryErrorPolicyDenied
	case errors.Is(err, store.ErrTeamIdempotencyConflict):
		status, code = http.StatusConflict, teamMemoryErrorIdempotencyConflict
	case errors.Is(err, store.ErrTeamIdempotencyInProgress):
		status, code = http.StatusConflict, teamMemoryErrorIdempotencyInProgress
	case errors.Is(err, store.ErrTeamIdempotencyFailed):
		status, code = http.StatusConflict, teamMemoryErrorIdempotencyFailed
	case errors.Is(err, store.ErrTeamPolicyEpochChanged):
		status, code = http.StatusConflict, teamMemoryErrorAuthorizationStale
	}
	writeTeamGraphDeltaError(w, status, code)
}

func writeTeamGraphDeltaError(w http.ResponseWriter, status int, code string) {
	writeTeamError(w, status, code, true)
}
