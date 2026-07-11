package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const (
	TeamMemoryRememberRoutePath = "/team/v1/memory/remember"
	TeamMemoryResultSchema      = "pulse.team.memory_result.v1"
	teamMemoryMaxBodyBytes      = 256 << 10

	teamMemoryErrorInvalid                  = "invalid_team_memory"
	teamMemoryErrorInvalidPrincipal         = "invalid_principal"
	teamMemoryErrorPrincipalRequestMismatch = "principal_request_mismatch"
	teamMemoryErrorPrincipalReplay          = "principal_replay"
	teamMemoryErrorPrincipalRevoked         = "principal_revoked"
	teamMemoryErrorPolicyDenied             = "policy_denied"
	teamMemoryErrorNotFound                 = "not_found"
	teamMemoryErrorIdempotencyConflict      = "idempotency_conflict"
	teamMemoryErrorIdempotencyInProgress    = "idempotency_in_progress"
	teamMemoryErrorIdempotencyFailed        = "idempotency_failed"
	teamMemoryErrorAuthorizationStale       = "authorization_stale"
)

var teamMemoryTagPattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._:-]{0,63}$`)

type teamMemoryEnvelope struct {
	Schema           string                         `json:"schema"`
	Source           *store.CapsuleSource           `json:"source"`
	Items            []teamMemoryWireItem           `json:"items"`
	RawInputIncluded *bool                          `json:"raw_input_included"`
	ActiveContext    *store.TeamMemoryActiveContext `json:"active_context"`
	TargetScope      *store.TeamMemoryTarget        `json:"target_scope,omitempty"`
	PrivacyTier      string                         `json:"privacy_tier"`
	Retention        string                         `json:"retention"`
	ExpiresAt        string                         `json:"expires_at,omitempty"`
	IdempotencyKey   string                         `json:"idempotency_key"`
}

type teamMemoryWireItem struct {
	Kind            string   `json:"kind"`
	RedactedSummary string   `json:"redacted_summary"`
	Confidence      *float64 `json:"confidence"`
	EvidenceHint    string   `json:"evidence_hint"`
	Tags            []string `json:"tags,omitempty"`
}

type teamMemoryProjectionJobResponse struct {
	Kind  string `json:"kind"`
	JobID string `json:"job_id"`
	State string `json:"state"`
}

type teamMemoryRememberResponse struct {
	Schema          string                            `json:"schema"`
	ObjectID        string                            `json:"object_id"`
	AuditEventID    string                            `json:"audit_event_id"`
	CapsuleIDs      []string                          `json:"capsule_ids"`
	Status          string                            `json:"status"`
	ProjectionState string                            `json:"projection_state"`
	ProjectionJobs  []teamMemoryProjectionJobResponse `json:"projection_jobs"`
	FullyProjected  bool                              `json:"fully_projected"`
	Replayed        bool                              `json:"replayed"`
	Fallback        bool                              `json:"fallback"`
}

// VerifyDomainRequest verifies a fresh pulse.principal.v1 assertion without
// trusting identity fields in the domain body. The signed OAuth tuple is
// resolved against the current team registry into a request-local principal
// context, and the assertion ID is consumed before that context is returned.
func (v *PrincipalVerifier) VerifyDomainRequest(
	ctx context.Context,
	compact, requestID, method, requestPath string,
	body []byte,
) (PrincipalContext, error) {
	if len(compact) == 0 || len(compact) > PrincipalAssertionMaxBytes {
		return PrincipalContext{}, ErrPrincipalInvalid
	}
	header, claims, signingInput, signature, err := decodePrincipalAssertion(compact)
	if err != nil {
		return PrincipalContext{}, ErrPrincipalInvalid
	}
	key, ok := v.keys[header.Kid]
	if !ok || !ed25519.Verify(key, []byte(signingInput), signature) {
		return PrincipalContext{}, ErrPrincipalInvalid
	}
	if err := v.validateDomainClaims(claims); err != nil {
		return PrincipalContext{}, err
	}
	digest := sha256.Sum256(body)
	if claims.RequestID != requestID || claims.Method != method || claims.Path != requestPath ||
		claims.BodySHA256 != hex.EncodeToString(digest[:]) || claims.StoreID != v.expectedStoreID || claims.TeamID != v.expectedTeamID {
		return PrincipalContext{}, ErrPrincipalRequestMismatch
	}

	registration, err := v.store.ResolveOAuthClient(ctx, claims.OAuthIssuer, claims.OAuthClientID)
	if err != nil {
		return PrincipalContext{}, classifyPrincipalStoreError(err)
	}
	var resolved store.ResolvedTeamPrincipal
	switch registration.Kind {
	case string(teamauth.PrincipalAgent):
		binding, err := v.store.ResolveAgentBinding(ctx, claims.OAuthIssuer, claims.OAuthSubject, claims.OAuthClientID)
		if err != nil {
			return PrincipalContext{}, classifyPrincipalStoreError(err)
		}
		if binding.BindingID != registration.BindingID || binding.AgentPrincipalID != registration.PrincipalID {
			return PrincipalContext{}, ErrPrincipalRevoked
		}
		resolved, err = v.store.ResolveTeamPrincipal(ctx, registration.PrincipalID)
		if err != nil {
			return PrincipalContext{}, classifyPrincipalStoreError(err)
		}
		if resolved.BindingID != registration.BindingID || resolved.HumanPrincipalID != binding.HumanPrincipalID {
			return PrincipalContext{}, ErrPrincipalRevoked
		}
	case string(teamauth.PrincipalService):
		resolved, err = v.store.ResolveServiceIdentity(ctx, claims.OAuthIssuer, claims.OAuthClientID)
		if err != nil {
			return PrincipalContext{}, classifyPrincipalStoreError(err)
		}
		if resolved.PrincipalID != registration.PrincipalID || resolved.Kind != string(teamauth.PrincipalService) {
			return PrincipalContext{}, ErrPrincipalRevoked
		}
	default:
		return PrincipalContext{}, ErrPrincipalRevoked
	}
	if err := v.consumeAssertionID(ctx, header.Kid, claims.JTI, time.Unix(claims.ExpiresAt, 0)); err != nil {
		if errors.Is(err, store.ErrAssertionReplay) {
			return PrincipalContext{}, ErrPrincipalReplay
		}
		if errors.Is(err, store.ErrAssertionExpired) {
			return PrincipalContext{}, ErrPrincipalInvalid
		}
		return PrincipalContext{}, ErrPrincipalStoreUnavailable
	}
	v.maybePruneExpiredAssertions(ctx)
	return principalContextFromResolved(claims, registration.OAuthClientKey, resolved), nil
}

func (v *PrincipalVerifier) validateDomainClaims(claims principalAssertionClaims) error {
	now := v.clock().Unix()
	if claims.Version != principalAssertionVersion || claims.Issuer != principalAssertionIssuer || claims.Audience != principalAssertionAudience ||
		claims.GrantKind != "registered" || !safeOpaque(claims.JTI, 128) || len(claims.RequestID) < 8 || !safeOpaque(claims.RequestID, 64) ||
		!exactIdentity(claims.OAuthIssuer, 512) || !exactIdentity(claims.OAuthSubject, 512) || !exactIdentity(claims.OAuthClientID, 512) ||
		claims.Method != http.MethodPost || claims.Path != TeamMemoryRememberRoutePath ||
		len(claims.BodySHA256) != 64 || strings.ToLower(claims.BodySHA256) != claims.BodySHA256 ||
		claims.ExpiresAt < now-principalAssertionClockSkewSecs || claims.NotBefore > now+principalAssertionClockSkewSecs ||
		claims.IssuedAt <= 0 || claims.NotBefore <= 0 || claims.ExpiresAt <= 0 || claims.IssuedAt > now+principalAssertionClockSkewSecs ||
		claims.ExpiresAt <= claims.IssuedAt || claims.NotBefore >= claims.ExpiresAt || claims.ExpiresAt-claims.IssuedAt > 30 ||
		claims.NotBefore < claims.IssuedAt-principalAssertionClockSkewSecs || !validCapabilities(claims.Capabilities) {
		return ErrPrincipalInvalid
	}
	if _, err := hex.DecodeString(claims.BodySHA256); err != nil {
		return ErrPrincipalInvalid
	}
	return nil
}

func (s *TeamServer) handleTeamMemoryRemember(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Encoding") != "" {
		writeTeamMemoryError(w, http.StatusUnsupportedMediaType, teamMemoryErrorInvalid)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeTeamMemoryError(w, http.StatusUnsupportedMediaType, teamMemoryErrorInvalid)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, teamMemoryMaxBodyBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > teamMemoryMaxBodyBytes {
		status := http.StatusBadRequest
		if len(raw) > teamMemoryMaxBodyBytes {
			status = http.StatusRequestEntityTooLarge
		}
		writeTeamMemoryError(w, status, teamMemoryErrorInvalid)
		return
	}
	write, err := decodeTeamMemoryEnvelope(raw)
	if err != nil {
		writeTeamMemoryError(w, http.StatusBadRequest, teamMemoryErrorInvalid)
		return
	}
	assertions := r.Header.Values("X-Pulse-Principal")
	requestIDs := r.Header.Values("X-Pulse-Request-ID")
	if len(assertions) != 1 || len(requestIDs) != 1 || assertions[0] == "" || requestIDs[0] == "" {
		writeTeamMemoryError(w, http.StatusUnauthorized, teamMemoryErrorInvalidPrincipal)
		return
	}
	principal, err := s.cfg.PrincipalVerifier.VerifyDomainRequest(
		r.Context(), assertions[0], requestIDs[0], r.Method, r.URL.EscapedPath(), raw,
	)
	if err != nil {
		writeTeamMemoryPrincipalError(w, err)
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
		ObjectKind: "memory", RequestedScope: requestedScope,
	})
	if err != nil {
		writeTeamMemoryStoreError(w, err)
		return
	}
	result, err := s.cfg.Store.StoreTeamMemoryCapsule(
		r.Context(), permit,
		store.TeamWriterLeaseIdentity{WriterID: s.cfg.WriterLease.WriterID, Token: s.cfg.WriterLease.Token},
		principal.RequestID, principal.OAuthClientKey, write,
	)
	if err != nil {
		writeTeamMemoryStoreError(w, err)
		return
	}
	response := teamMemoryRememberResponse{
		Schema: TeamMemoryResultSchema, ObjectID: result.ObjectID, AuditEventID: result.AuditEventID,
		CapsuleIDs: append([]string(nil), result.CapsuleIDs...), Status: result.Status,
		ProjectionState: result.ProjectionState, FullyProjected: result.FullyProjected,
		Replayed: result.Replayed, Fallback: false,
	}
	response.ProjectionJobs = make([]teamMemoryProjectionJobResponse, len(result.ProjectionJobs))
	for index, job := range result.ProjectionJobs {
		response.ProjectionJobs[index] = teamMemoryProjectionJobResponse{Kind: job.Kind, JobID: job.JobID, State: job.State}
	}
	sort.Slice(response.ProjectionJobs, func(left, right int) bool {
		return response.ProjectionJobs[left].Kind < response.ProjectionJobs[right].Kind
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func decodeTeamMemoryEnvelope(raw []byte) (store.TeamMemoryWrite, error) {
	var envelope teamMemoryEnvelope
	if len(raw) == 0 || len(raw) > teamMemoryMaxBodyBytes || decodeStrictJSON(raw, &envelope) != nil ||
		!validTeamMemoryEnvelope(envelope) {
		return store.TeamMemoryWrite{}, store.ErrTeamMemoryInvalid
	}
	items := make([]store.TeamMemoryItem, len(envelope.Items))
	for index, item := range envelope.Items {
		items[index] = store.TeamMemoryItem{
			Kind: item.Kind, RedactedSummary: item.RedactedSummary, Confidence: *item.Confidence,
			EvidenceHint: item.EvidenceHint, Tags: append([]string{}, item.Tags...),
		}
	}
	return store.TeamMemoryWrite{
		Schema: envelope.Schema, Source: *envelope.Source, Items: items,
		RawInputIncluded: *envelope.RawInputIncluded, ActiveContext: *envelope.ActiveContext,
		TargetScope: cloneTeamMemoryTarget(envelope.TargetScope), PrivacyTier: envelope.PrivacyTier,
		Retention: envelope.Retention, ExpiresAt: envelope.ExpiresAt, IdempotencyKey: envelope.IdempotencyKey,
	}, nil
}

func validTeamMemoryEnvelope(envelope teamMemoryEnvelope) bool {
	if envelope.Schema != store.TeamMemorySchema || envelope.Source == nil || envelope.RawInputIncluded == nil ||
		*envelope.RawInputIncluded || envelope.ActiveContext == nil || len(envelope.Items) == 0 || len(envelope.Items) > 20 ||
		!validTeamMemorySource(*envelope.Source) || !validTeamMemoryActiveContext(*envelope.ActiveContext) ||
		!validTeamMemoryTarget(envelope.TargetScope) || !validTeamMemoryPolicy(envelope.PrivacyTier, envelope.Retention) ||
		len(envelope.IdempotencyKey) < 8 || !safeOpaque(envelope.IdempotencyKey, 255) {
		return false
	}
	if envelope.ExpiresAt != "" {
		if _, ok := parseTeamMemoryTime(envelope.ExpiresAt); !ok {
			return false
		}
	}
	for _, item := range envelope.Items {
		if !validTeamMemoryItem(item) {
			return false
		}
	}
	return true
}

func validTeamMemorySource(source store.CapsuleSource) bool {
	allowedHost := map[string]bool{
		"chatgpt": true, "claude": true, "codex": true, "claude-code": true,
		"gemini-cli": true, "cursor": true, "langchain": true, "crewai": true, "pulse-cli": true,
	}
	allowedScope := map[string]bool{
		"current_turn": true, "user_selected_excerpt": true, "project_context": true, "install_event": true,
	}
	_, timestampOK := parseTeamMemoryTime(source.Timestamp)
	return allowedHost[source.Host] && allowedScope[source.ConversationScope] && timestampOK
}

func validTeamMemoryActiveContext(active store.TeamMemoryActiveContext) bool {
	for _, value := range []string{active.ProjectID, active.RepoID, active.AgentID, active.SessionID} {
		if value != "" && !safeOpaque(value, 255) {
			return false
		}
	}
	return true
}

func validTeamMemoryTarget(target *store.TeamMemoryTarget) bool {
	if target == nil {
		return true
	}
	if target.Type == teamauth.ScopePersonal {
		return target.ID == ""
	}
	switch target.Type {
	case teamauth.ScopeProject, teamauth.ScopeRepo, teamauth.ScopeAgent, teamauth.ScopeSession:
		return safeOpaque(target.ID, 255)
	default:
		return false
	}
}

func validTeamMemoryPolicy(privacy, retention string) bool {
	return (privacy == "normal" || privacy == "sensitive" || privacy == "private") &&
		(retention == "session" || retention == "project" || retention == "long_term")
}

func validTeamMemoryItem(item teamMemoryWireItem) bool {
	allowedKind := map[string]bool{
		"fact": true, "decision": true, "preference": true, "project_state": true, "open_loop": true,
		"correction": true, "relationship_note": true, "do_not_repeat": true, "system_event": true, "state_signal": true,
	}
	allowedEvidence := map[string]bool{
		"user_selected": true, "current_turn": true, "assistant_inferred": true, "tool_result": true, "user_confirmed": true,
	}
	if !allowedKind[item.Kind] || item.RedactedSummary == "" || utf8.RuneCountInString(item.RedactedSummary) > 1200 ||
		strings.TrimSpace(item.RedactedSummary) != item.RedactedSummary || item.Confidence == nil ||
		math.IsNaN(*item.Confidence) || math.IsInf(*item.Confidence, 0) ||
		*item.Confidence < 0 || *item.Confidence > 1 ||
		!allowedEvidence[item.EvidenceHint] || len(item.Tags) > 20 {
		return false
	}
	seen := make(map[string]struct{}, len(item.Tags))
	for _, tag := range item.Tags {
		if !teamMemoryTagPattern.MatchString(tag) {
			return false
		}
		if _, duplicate := seen[tag]; duplicate {
			return false
		}
		seen[tag] = struct{}{}
	}
	return true
}

func parseTeamMemoryTime(value string) (time.Time, bool) {
	if value == "" || strings.TrimSpace(value) != value {
		return time.Time{}, false
	}
	normalized := value
	if len(normalized) > 10 && normalized[10] == 't' {
		normalized = normalized[:10] + "T" + normalized[11:]
	}
	if strings.HasSuffix(normalized, "z") {
		normalized = normalized[:len(normalized)-1] + "Z"
	}
	parsed, err := time.Parse(time.RFC3339Nano, normalized)
	return parsed, err == nil
}

func cloneTeamMemoryTarget(target *store.TeamMemoryTarget) *store.TeamMemoryTarget {
	if target == nil {
		return nil
	}
	clone := *target
	return &clone
}

func writeTeamMemoryPrincipalError(w http.ResponseWriter, err error) {
	status, code := http.StatusUnauthorized, teamMemoryErrorInvalidPrincipal
	switch {
	case errors.Is(err, ErrPrincipalRequestMismatch):
		code = teamMemoryErrorPrincipalRequestMismatch
	case errors.Is(err, ErrPrincipalReplay):
		code = teamMemoryErrorPrincipalReplay
	case errors.Is(err, ErrPrincipalRevoked):
		status, code = http.StatusForbidden, teamMemoryErrorPrincipalRevoked
	case errors.Is(err, ErrPrincipalStoreUnavailable):
		status, code = http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable
	}
	writeTeamMemoryError(w, status, code)
}

func writeTeamMemoryStoreError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable
	switch {
	case errors.Is(err, store.ErrTeamMemoryInvalid), errors.Is(err, store.ErrTeamObjectInvalid):
		status, code = http.StatusBadRequest, teamMemoryErrorInvalid
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
	writeTeamMemoryError(w, status, code)
}

func writeTeamMemoryError(w http.ResponseWriter, status int, code string) {
	writeTeamError(w, status, code, true)
}
