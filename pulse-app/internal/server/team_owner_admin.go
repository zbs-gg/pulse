package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
	"golang.org/x/text/unicode/norm"
)

const (
	OwnerBootstrapRoutePath = "/team/v1/owner/bootstrap"
	OwnerActivateRoutePath  = "/team/v1/owner/activate"

	OwnerApprovalSchema        = "pulse.team.owner.approval.v1"
	OwnerApprovalResultSchema  = "pulse.team.owner.approval_result.v1"
	OwnerBootstrapSchema       = "pulse.team.owner.bootstrap.v1"
	OwnerBootstrapResultSchema = "pulse.team.owner.bootstrap_result.v1"
	OwnerActivateSchema        = "pulse.team.owner.activate.v1"
	OwnerActivateResultSchema  = "pulse.team.owner.activate_result.v1"

	ownerAdminMaxBodyBytes = 32 << 10
	ownerApprovalTTL       = 4 * time.Minute
)

var ownerUnsafeTextPattern = regexp.MustCompile(
	`(?i)(authorization:\s*bearer|token\s*=|api[_-]?key|password|private[_-]?key|begin private key|\bsk-[A-Za-z0-9_-]{12,}\b|\bghp_[A-Za-z0-9_]{12,}\b|/(?:users|home|etc|var|private|volumes)/|file://|(?:^|\s)~/|(?:^|\s)[a-z]:\\|\\\\[^\\\s]+\\)`,
)

type OwnerAdminServerConfig struct {
	IPCSecret      string
	Store          *store.Store
	StepUpVerifier *OwnerStepUpVerifier
	WriterLease    *store.TeamWriterLease
	Clock          func() time.Time
}

type OwnerAdminServer struct {
	cfg          OwnerAdminServerConfig
	clock        func() time.Time
	writer       store.TeamWriterLeaseIdentity
	prebootstrap bool
}

type ownerBootstrapIntentEnvelope struct {
	StoreID           *string `json:"store_id"`
	TeamID            *string `json:"team_id"`
	OwnerPrincipalID  *string `json:"owner_principal_id"`
	OwnerMembershipID *string `json:"owner_membership_id"`
}

type ownerBootstrapIntent struct {
	StoreID           string `json:"store_id"`
	TeamID            string `json:"team_id"`
	OwnerPrincipalID  string `json:"owner_principal_id"`
	OwnerMembershipID string `json:"owner_membership_id"`
}

type ownerApprovalEnvelope struct {
	Schema          *string                       `json:"schema"`
	Action          *string                       `json:"action"`
	StoreID         *string                       `json:"store_id"`
	TeamID          *string                       `json:"team_id"`
	TargetKind      *string                       `json:"target_kind"`
	TargetID        *string                       `json:"target_id"`
	TargetDigest    *string                       `json:"target_digest"`
	TeamName        *string                       `json:"team_name,omitempty"`
	BootstrapIntent *ownerBootstrapIntentEnvelope `json:"bootstrap_intent,omitempty"`
	GateDigest      *string                       `json:"gate_digest,omitempty"`
	Mutation        *ownerAdminMutationEnvelope   `json:"mutation,omitempty"`
}

type ownerApprovalRequest struct {
	Action          string
	StoreID         string
	TeamID          string
	TargetKind      string
	TargetID        string
	TargetDigest    string
	TeamName        string
	BootstrapIntent *ownerBootstrapIntent
	GateDigest      string
	Mutation        *store.OwnerAdminMutation
}

type ownerApprovalResponse struct {
	Schema        string `json:"schema"`
	ApprovalNonce string `json:"approval_nonce"`
	Action        string `json:"action"`
	StoreID       string `json:"store_id"`
	TeamID        string `json:"team_id"`
	TargetKind    string `json:"target_kind"`
	TargetID      string `json:"target_id"`
	ExpiresAt     string `json:"expires_at"`
	Fallback      bool   `json:"fallback"`
}

type ownerBootstrapEnvelope struct {
	Schema          *string                       `json:"schema"`
	Operation       *string                       `json:"operation"`
	TeamName        *string                       `json:"team_name,omitempty"`
	BootstrapIntent *ownerBootstrapIntentEnvelope `json:"bootstrap_intent,omitempty"`
	ApprovalNonce   *string                       `json:"approval_nonce,omitempty"`
}

type ownerBootstrapRequest struct {
	Operation       string
	TeamName        string
	BootstrapIntent *ownerBootstrapIntent
	ApprovalNonce   string
}

type ownerBootstrapResponse struct {
	Schema            string                `json:"schema"`
	Operation         string                `json:"operation"`
	BootstrapIntent   *ownerBootstrapIntent `json:"bootstrap_intent,omitempty"`
	StoreID           string                `json:"store_id,omitempty"`
	TeamID            string                `json:"team_id,omitempty"`
	OwnerPrincipalID  string                `json:"owner_principal_id,omitempty"`
	OwnerMembershipID string                `json:"owner_membership_id,omitempty"`
	ActivationState   string                `json:"activation_state,omitempty"`
	ContentBoundary   string                `json:"content_boundary,omitempty"`
	PublicEnabled     *bool                 `json:"public_enabled,omitempty"`
	Fallback          bool                  `json:"fallback"`
}

type ownerActivateEnvelope struct {
	Schema        *string `json:"schema"`
	ApprovalNonce *string `json:"approval_nonce"`
	GateDigest    *string `json:"gate_digest"`
}

type ownerActivateRequest struct {
	ApprovalNonce string
	GateDigest    string
}

type ownerActivateResponse struct {
	Schema                 string `json:"schema"`
	StoreID                string `json:"store_id"`
	TeamID                 string `json:"team_id"`
	ActivationState        string `json:"activation_state"`
	ContentBoundary        string `json:"content_boundary"`
	PublicEnabled          bool   `json:"public_enabled"`
	GateDigest             string `json:"gate_digest"`
	ActivatedByPrincipalID string `json:"activated_by_principal_id"`
	AuditEventID           string `json:"audit_event_id"`
	ActivatedAt            string `json:"activated_at"`
	Fallback               bool   `json:"fallback"`
}

func NewOwnerAdminServer(cfg OwnerAdminServerConfig) (*OwnerAdminServer, error) {
	if !validTeamIPCSecret(cfg.IPCSecret) || cfg.Store == nil || cfg.StepUpVerifier == nil ||
		cfg.StepUpVerifier.store != cfg.Store {
		return nil, errors.New("owner admin server: incomplete configuration")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	identity, err := cfg.Store.ResolveOwnerStepUpIdentity(context.Background(), cfg.StepUpVerifier.expectedRoot)
	if err != nil {
		return nil, errors.New("owner admin server: owner identity unavailable")
	}
	server := &OwnerAdminServer{cfg: cfg, clock: clock, prebootstrap: identity.Bootstrap}
	if identity.Bootstrap {
		if cfg.WriterLease != nil {
			return nil, errors.New("owner admin server: prebootstrap writer lease is invalid")
		}
		return server, nil
	}
	if cfg.WriterLease == nil || cfg.WriterLease.StoreID != identity.StoreID ||
		cfg.WriterLease.TeamID != identity.TeamID || cfg.WriterLease.WriterID == "" ||
		cfg.WriterLease.Token == "" || cfg.WriterLease.WriterVersion < teamauth.SchemaVersion {
		return nil, errors.New("owner admin server: active writer lease is required")
	}
	server.writer = store.TeamWriterLeaseIdentity{
		WriterID: cfg.WriterLease.WriterID, Token: cfg.WriterLease.Token,
	}
	return server, nil
}

// Handler is a separate loopback-only administration surface. It is never
// mounted into TeamServer.Handler and none of these routes are MCP tools.
func (s *OwnerAdminServer) Handler() http.Handler {
	router := chi.NewRouter()
	router.Use(s.loopbackIPCMiddleware)
	router.Post(OwnerApprovalRoutePath, s.handleOwnerApproval)
	router.Post(OwnerBootstrapRoutePath, s.handleOwnerBootstrap)
	router.Post(OwnerActivateRoutePath, s.handleOwnerActivate)
	router.Post(OwnerSharedDeleteRoutePath, s.handleOwnerSharedDelete)
	s.registerMutationRoutes(router)
	return router
}

func (s *OwnerAdminServer) loopbackIPCMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("X-Pulse-Key")
		if !isLoopbackRequest(r) || len(values) != 1 || len(values[0]) > 512 ||
			subtle.ConstantTimeCompare([]byte(values[0]), []byte(s.cfg.IPCSecret)) != 1 {
			writeOwnerAdminError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *OwnerAdminServer) handleOwnerApproval(w http.ResponseWriter, r *http.Request) {
	raw, ok := readOwnerAdminBody(w, r, "invalid_owner_approval")
	if !ok {
		return
	}
	request, err := decodeOwnerApprovalRequest(raw)
	if err != nil {
		writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_approval")
		return
	}
	assertions := r.Header.Values("X-Pulse-Owner-Step-Up")
	requestIDs := r.Header.Values("X-Pulse-Request-ID")
	if len(assertions) != 1 || len(requestIDs) != 1 || assertions[0] == "" ||
		!validOwnerAdminRequestID(requestIDs[0]) {
		writeOwnerAdminError(w, http.StatusUnauthorized, "invalid_owner_step_up")
		return
	}
	stepUp, err := s.cfg.StepUpVerifier.VerifyApprovalRequest(
		r.Context(), assertions[0], requestIDs[0], r.Method, r.URL.EscapedPath(), raw,
		OwnerStepUpBinding{Action: request.Action, StoreID: request.StoreID, TeamID: request.TeamID},
	)
	if err != nil {
		writeOwnerStepUpError(w, err)
		return
	}
	ownerID := stepUp.Identity.OwnerPrincipalID
	if request.Action == store.OwnerActionTeamBootstrap {
		if !s.prebootstrap || !stepUp.Identity.Bootstrap || request.BootstrapIntent == nil {
			writeOwnerAdminError(w, http.StatusForbidden, "owner_required")
			return
		}
		ownerID = request.BootstrapIntent.OwnerPrincipalID
	} else if s.prebootstrap || stepUp.Identity.Bootstrap || stepUp.Identity.StoreID != request.StoreID ||
		stepUp.Identity.TeamID != request.TeamID || ownerID == "" {
		writeOwnerAdminError(w, http.StatusForbidden, "owner_required")
		return
	}
	now := s.clock().UTC()
	expiresAt := now.Add(ownerApprovalTTL)
	stepUpExpiry := stepUp.AuthenticatedAt.Add(ownerStepUpMaxAge)
	if stepUpExpiry.Before(expiresAt) {
		expiresAt = stepUpExpiry
	}
	challenge, err := s.cfg.Store.IssueOwnerApproval(r.Context(), store.OwnerApprovalIssueRequest{
		OwnerPrincipalID: ownerID, StoreID: request.StoreID, TeamID: request.TeamID,
		ClientKey: stepUp.Identity.ClientKey,
		Action:    request.Action, TargetKind: request.TargetKind, TargetID: request.TargetID,
		TargetDigest: request.TargetDigest, StepUpAt: stepUp.AuthenticatedAt,
		ExpiresAt: expiresAt, AssertionKID: stepUp.AssertionKID,
		AssertionJTI: stepUp.AssertionJTI, AssertionExpiresAt: stepUp.AssertionExpiresAt,
		Writer: s.writer,
	})
	if err != nil {
		writeOwnerStoreError(w, err, "invalid_owner_approval")
		return
	}
	response := ownerApprovalResponse{
		Schema: OwnerApprovalResultSchema, ApprovalNonce: challenge.Nonce,
		Action: request.Action, StoreID: request.StoreID, TeamID: request.TeamID,
		TargetKind: request.TargetKind, TargetID: request.TargetID,
		ExpiresAt: challenge.ExpiresAt.UTC().Format(time.RFC3339Nano), Fallback: false,
	}
	if !validOwnerApprovalResponse(response) {
		writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func (s *OwnerAdminServer) handleOwnerBootstrap(w http.ResponseWriter, r *http.Request) {
	raw, ok := readOwnerAdminBody(w, r, "invalid_owner_bootstrap")
	if !ok {
		return
	}
	request, err := decodeOwnerBootstrapRequest(raw)
	if err != nil {
		writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_bootstrap")
		return
	}
	response := ownerBootstrapResponse{Schema: OwnerBootstrapResultSchema, Fallback: false}
	if request.Operation == "prepare" {
		intent, err := s.cfg.Store.PrepareTeamBootstrap(r.Context())
		if err != nil {
			writeOwnerStoreError(w, err, "invalid_owner_bootstrap")
			return
		}
		response.Operation = "prepared"
		response.BootstrapIntent = &ownerBootstrapIntent{
			StoreID: intent.StoreID, TeamID: intent.TeamID,
			OwnerPrincipalID: intent.OwnerPrincipalID, OwnerMembershipID: intent.OwnerMembershipID,
		}
	} else {
		requestIDs := r.Header.Values("X-Pulse-Request-ID")
		if len(requestIDs) != 1 || !validOwnerAdminRequestID(requestIDs[0]) || request.BootstrapIntent == nil {
			writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_bootstrap")
			return
		}
		clientKey := teamauth.OAuthClientKey(
			s.cfg.StepUpVerifier.expectedRoot.Issuer,
			s.cfg.StepUpVerifier.expectedRoot.AdminClientID,
		)
		result, err := s.cfg.Store.BootstrapTeamWithApproval(r.Context(), store.ApprovedBootstrapTeamRequest{
			TeamName: request.TeamName, PresentedRoot: s.cfg.StepUpVerifier.expectedRoot,
			Intent:        toStoreBootstrapIntent(*request.BootstrapIntent),
			ApprovalNonce: request.ApprovalNonce, RequestID: requestIDs[0], ClientKey: clientKey,
		})
		if err != nil {
			writeOwnerStoreError(w, err, "invalid_owner_bootstrap")
			return
		}
		publicEnabled := false
		response.Operation = "complete"
		response.StoreID, response.TeamID = result.StoreID, result.TeamID
		response.OwnerPrincipalID, response.OwnerMembershipID = result.OwnerPrincipalID, result.OwnerMembershipID
		response.ActivationState, response.ContentBoundary = store.TeamActivationInactive, store.TeamContentSynthetic
		response.PublicEnabled = &publicEnabled
	}
	if !validOwnerBootstrapResponse(response) {
		writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func (s *OwnerAdminServer) handleOwnerActivate(w http.ResponseWriter, r *http.Request) {
	raw, ok := readOwnerAdminBody(w, r, "invalid_owner_activate")
	if !ok {
		return
	}
	request, err := decodeOwnerActivateRequest(raw)
	if err != nil {
		writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_activate")
		return
	}
	if s.prebootstrap || s.writer.WriterID == "" || s.writer.Token == "" {
		writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	requestIDs := r.Header.Values("X-Pulse-Request-ID")
	if len(requestIDs) != 1 || !validOwnerAdminRequestID(requestIDs[0]) {
		writeOwnerAdminError(w, http.StatusBadRequest, "invalid_owner_activate")
		return
	}
	clientKey := teamauth.OAuthClientKey(
		s.cfg.StepUpVerifier.expectedRoot.Issuer,
		s.cfg.StepUpVerifier.expectedRoot.AdminClientID,
	)
	state, err := s.cfg.Store.ActivateSyntheticTeamRemote(r.Context(), store.ActivateSyntheticTeamRequest{
		ApprovalNonce: request.ApprovalNonce, GateDigest: request.GateDigest,
		RequestID: requestIDs[0], ClientKey: clientKey, Writer: s.writer,
	})
	if err != nil {
		writeOwnerStoreError(w, err, "invalid_owner_activate")
		return
	}
	response := ownerActivateResponse{
		Schema: OwnerActivateResultSchema, StoreID: state.StoreID, TeamID: state.TeamID,
		ActivationState: state.ActivationState, ContentBoundary: state.ContentBoundary,
		PublicEnabled: state.PublicEnabled, GateDigest: state.GateDigest,
		ActivatedByPrincipalID: state.ActivatedBy, AuditEventID: state.AuditEventID,
		Fallback: false,
	}
	if state.ActivatedAt != nil {
		response.ActivatedAt = state.ActivatedAt.UTC().Format(time.RFC3339Nano)
	}
	if !validOwnerActivateResponse(response) {
		writeOwnerAdminError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
		return
	}
	writeTeamReadJSON(w, response)
}

func decodeOwnerApprovalRequest(raw []byte) (ownerApprovalRequest, error) {
	var envelope ownerApprovalEnvelope
	if !decodeOwnerAdminEnvelope(raw, &envelope) || envelope.Schema == nil || envelope.Action == nil ||
		envelope.StoreID == nil || envelope.TeamID == nil || envelope.TargetKind == nil ||
		envelope.TargetID == nil || envelope.TargetDigest == nil || *envelope.Schema != OwnerApprovalSchema ||
		!validTeamAdminClass(*envelope.Action) || !validTeamAdminOpaque(*envelope.StoreID) ||
		!validTeamAdminOpaque(*envelope.TeamID) || !validTeamAdminClass(*envelope.TargetKind) ||
		!validTeamAdminOpaque(*envelope.TargetID) || !validTeamAdminDigest(*envelope.TargetDigest) {
		return ownerApprovalRequest{}, store.ErrOwnerApprovalInvalid
	}
	request := ownerApprovalRequest{
		Action: *envelope.Action, StoreID: *envelope.StoreID, TeamID: *envelope.TeamID,
		TargetKind: *envelope.TargetKind, TargetID: *envelope.TargetID, TargetDigest: *envelope.TargetDigest,
	}
	switch request.Action {
	case store.OwnerActionTeamBootstrap:
		if envelope.TeamName == nil || envelope.BootstrapIntent == nil || envelope.GateDigest != nil || envelope.Mutation != nil ||
			!validOwnerDisplayName(*envelope.TeamName) {
			return ownerApprovalRequest{}, store.ErrOwnerApprovalInvalid
		}
		intent, ok := decodeOwnerBootstrapIntent(*envelope.BootstrapIntent)
		if !ok || intent.StoreID != request.StoreID || intent.TeamID != request.TeamID ||
			request.TargetKind != "team" || request.TargetID != request.TeamID ||
			store.TeamBootstrapApprovalTargetDigest(toStoreBootstrapIntent(intent), *envelope.TeamName) != request.TargetDigest {
			return ownerApprovalRequest{}, store.ErrOwnerApprovalInvalid
		}
		request.TeamName, request.BootstrapIntent = *envelope.TeamName, &intent
	case store.OwnerActionSyntheticActivate:
		if envelope.GateDigest == nil || envelope.TeamName != nil || envelope.BootstrapIntent != nil || envelope.Mutation != nil ||
			!validTeamAdminDigest(*envelope.GateDigest) || request.TargetKind != "team_activation" ||
			request.TargetID != request.TeamID ||
			store.SyntheticActivationTargetDigest(request.StoreID, request.TeamID, *envelope.GateDigest) != request.TargetDigest {
			return ownerApprovalRequest{}, store.ErrOwnerApprovalInvalid
		}
		request.GateDigest = *envelope.GateDigest
	case store.OwnerActionSharedDelete:
		if envelope.TeamName != nil || envelope.BootstrapIntent != nil || envelope.GateDigest != nil ||
			envelope.Mutation != nil || request.TargetKind != "team_object" ||
			store.SharedDeletionApprovalTargetDigest(request.TargetID) != request.TargetDigest {
			return ownerApprovalRequest{}, store.ErrOwnerApprovalInvalid
		}
	default:
		if envelope.TeamName != nil || envelope.BootstrapIntent != nil || envelope.GateDigest != nil ||
			envelope.Mutation == nil {
			return ownerApprovalRequest{}, store.ErrOwnerApprovalInvalid
		}
		mutation, ok := decodeOwnerAdminMutation(request.Action, *envelope.Mutation)
		if !ok {
			return ownerApprovalRequest{}, store.ErrOwnerApprovalInvalid
		}
		targetKind, targetID, targetDigest, err := store.OwnerAdminMutationTarget(mutation)
		if err != nil || targetKind != request.TargetKind || targetID != request.TargetID ||
			targetDigest != request.TargetDigest {
			return ownerApprovalRequest{}, store.ErrOwnerApprovalInvalid
		}
		request.Mutation = &mutation
	}
	return request, nil
}

func decodeOwnerBootstrapRequest(raw []byte) (ownerBootstrapRequest, error) {
	var envelope ownerBootstrapEnvelope
	if !decodeOwnerAdminEnvelope(raw, &envelope) || envelope.Schema == nil || envelope.Operation == nil ||
		*envelope.Schema != OwnerBootstrapSchema {
		return ownerBootstrapRequest{}, store.ErrOwnerApprovalInvalid
	}
	switch *envelope.Operation {
	case "prepare":
		if envelope.TeamName != nil || envelope.BootstrapIntent != nil || envelope.ApprovalNonce != nil {
			return ownerBootstrapRequest{}, store.ErrOwnerApprovalInvalid
		}
		return ownerBootstrapRequest{Operation: "prepare"}, nil
	case "execute":
		if envelope.TeamName == nil || envelope.BootstrapIntent == nil || envelope.ApprovalNonce == nil ||
			!validOwnerDisplayName(*envelope.TeamName) ||
			!validTeamAdminDigest(*envelope.ApprovalNonce) {
			return ownerBootstrapRequest{}, store.ErrOwnerApprovalInvalid
		}
		intent, ok := decodeOwnerBootstrapIntent(*envelope.BootstrapIntent)
		if !ok {
			return ownerBootstrapRequest{}, store.ErrOwnerApprovalInvalid
		}
		return ownerBootstrapRequest{
			Operation: "execute", TeamName: *envelope.TeamName, BootstrapIntent: &intent,
			ApprovalNonce: *envelope.ApprovalNonce,
		}, nil
	default:
		return ownerBootstrapRequest{}, store.ErrOwnerApprovalInvalid
	}
}

func decodeOwnerActivateRequest(raw []byte) (ownerActivateRequest, error) {
	var envelope ownerActivateEnvelope
	if !decodeOwnerAdminEnvelope(raw, &envelope) || envelope.Schema == nil ||
		envelope.ApprovalNonce == nil || envelope.GateDigest == nil ||
		*envelope.Schema != OwnerActivateSchema || !validTeamAdminDigest(*envelope.ApprovalNonce) ||
		!validTeamAdminDigest(*envelope.GateDigest) {
		return ownerActivateRequest{}, store.ErrOwnerApprovalInvalid
	}
	return ownerActivateRequest{ApprovalNonce: *envelope.ApprovalNonce, GateDigest: *envelope.GateDigest}, nil
}

func decodeOwnerAdminEnvelope(raw []byte, target any) bool {
	return len(raw) > 0 && len(raw) <= ownerAdminMaxBodyBytes &&
		decodeStrictJSON(raw, target) == nil && !teamGraphJSONContainsNull(raw)
}

func decodeOwnerBootstrapIntent(envelope ownerBootstrapIntentEnvelope) (ownerBootstrapIntent, bool) {
	if envelope.StoreID == nil || envelope.TeamID == nil || envelope.OwnerPrincipalID == nil ||
		envelope.OwnerMembershipID == nil {
		return ownerBootstrapIntent{}, false
	}
	result := ownerBootstrapIntent{
		StoreID: *envelope.StoreID, TeamID: *envelope.TeamID,
		OwnerPrincipalID: *envelope.OwnerPrincipalID, OwnerMembershipID: *envelope.OwnerMembershipID,
	}
	return result, validTeamAdminOpaque(result.StoreID) && strings.HasPrefix(result.StoreID, "store_") &&
		validTeamAdminOpaque(result.TeamID) && strings.HasPrefix(result.TeamID, "team_") &&
		validTeamAdminOpaque(result.OwnerPrincipalID) && strings.HasPrefix(result.OwnerPrincipalID, "principal_") &&
		validTeamAdminOpaque(result.OwnerMembershipID) && strings.HasPrefix(result.OwnerMembershipID, "membership_")
}

func toStoreBootstrapIntent(intent ownerBootstrapIntent) store.TeamBootstrapIntent {
	return store.TeamBootstrapIntent{
		StoreID: intent.StoreID, TeamID: intent.TeamID,
		OwnerPrincipalID: intent.OwnerPrincipalID, OwnerMembershipID: intent.OwnerMembershipID,
	}
}

func readOwnerAdminBody(w http.ResponseWriter, r *http.Request, invalidCode string) ([]byte, bool) {
	return readTeamJSONBody(w, r, ownerAdminMaxBodyBytes, invalidCode)
}

func validOwnerApprovalResponse(response ownerApprovalResponse) bool {
	return response.Schema == OwnerApprovalResultSchema && !response.Fallback &&
		validTeamAdminDigest(response.ApprovalNonce) && validTeamAdminClass(response.Action) &&
		validTeamAdminOpaque(response.StoreID) && validTeamAdminOpaque(response.TeamID) &&
		validTeamAdminClass(response.TargetKind) && validTeamAdminOpaque(response.TargetID) &&
		validTeamAdminTime(response.ExpiresAt)
}

func validOwnerBootstrapResponse(response ownerBootstrapResponse) bool {
	if response.Schema != OwnerBootstrapResultSchema || response.Fallback {
		return false
	}
	if response.Operation == "prepared" {
		return response.BootstrapIntent != nil && response.StoreID == "" && response.TeamID == "" &&
			response.OwnerPrincipalID == "" && response.OwnerMembershipID == "" &&
			response.ActivationState == "" && response.ContentBoundary == "" && response.PublicEnabled == nil &&
			validTeamAdminOpaque(response.BootstrapIntent.StoreID) &&
			strings.HasPrefix(response.BootstrapIntent.StoreID, "store_") &&
			validTeamAdminOpaque(response.BootstrapIntent.TeamID) &&
			strings.HasPrefix(response.BootstrapIntent.TeamID, "team_") &&
			validTeamAdminOpaque(response.BootstrapIntent.OwnerPrincipalID) &&
			strings.HasPrefix(response.BootstrapIntent.OwnerPrincipalID, "principal_") &&
			validTeamAdminOpaque(response.BootstrapIntent.OwnerMembershipID) &&
			strings.HasPrefix(response.BootstrapIntent.OwnerMembershipID, "membership_")
	}
	return response.Operation == "complete" && response.BootstrapIntent == nil &&
		validTeamAdminOpaque(response.StoreID) && validTeamAdminOpaque(response.TeamID) &&
		validTeamAdminOpaque(response.OwnerPrincipalID) && validTeamAdminOpaque(response.OwnerMembershipID) &&
		response.ActivationState == store.TeamActivationInactive &&
		response.ContentBoundary == store.TeamContentSynthetic && response.PublicEnabled != nil && !*response.PublicEnabled
}

func validOwnerActivateResponse(response ownerActivateResponse) bool {
	return response.Schema == OwnerActivateResultSchema && !response.Fallback &&
		validTeamAdminOpaque(response.StoreID) && validTeamAdminOpaque(response.TeamID) &&
		response.ActivationState == store.TeamActivationActive &&
		response.ContentBoundary == store.TeamContentSynthetic && response.PublicEnabled &&
		validTeamAdminDigest(response.GateDigest) && validTeamAdminOpaque(response.ActivatedByPrincipalID) &&
		validTeamAdminOpaque(response.AuditEventID) && validTeamAdminTime(response.ActivatedAt)
}

func validOwnerAdminRequestID(value string) bool {
	return len(value) >= 8 && validTeamAdminOpaque(value)
}

func validOwnerDisplayName(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > 128 || !norm.NFC.IsNormalString(value) ||
		ownerUnsafeTextPattern.MatchString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func writeOwnerStepUpError(w http.ResponseWriter, err error) {
	status, code := http.StatusUnauthorized, "invalid_owner_step_up"
	if errors.Is(err, store.ErrHumanOwnerRequired) {
		status, code = http.StatusForbidden, "owner_required"
	} else if !errors.Is(err, ErrOwnerStepUpInvalid) && !errors.Is(err, ErrOwnerStepUpRequestMismatch) {
		status, code = http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable
	}
	writeOwnerAdminError(w, status, code)
}

func writeOwnerStoreError(w http.ResponseWriter, err error, invalidCode string) {
	status, code := http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable
	switch {
	case errors.Is(err, store.ErrOwnerApprovalInvalid):
		status, code = http.StatusBadRequest, invalidCode
	case errors.Is(err, store.ErrOwnerApprovalRequired):
		status, code = http.StatusForbidden, "owner_approval_required"
	case errors.Is(err, store.ErrOwnerApprovalExpired), errors.Is(err, store.ErrOwnerStepUpStale):
		status, code = http.StatusForbidden, "owner_approval_expired"
	case errors.Is(err, store.ErrOwnerApprovalReplay):
		status, code = http.StatusConflict, "owner_approval_replayed"
	case errors.Is(err, store.ErrOwnerApprovalBindingMismatch):
		status, code = http.StatusConflict, "owner_approval_binding_mismatch"
	case errors.Is(err, store.ErrHumanOwnerRequired):
		status, code = http.StatusForbidden, "owner_required"
	case errors.Is(err, store.ErrTeamAlreadyActivated), errors.Is(err, store.ErrBootstrapConsumed):
		status, code = http.StatusConflict, "owner_action_conflict"
	}
	writeOwnerAdminError(w, status, code)
}

func writeOwnerAdminError(w http.ResponseWriter, status int, code string) {
	writeTeamError(w, status, code, true)
}
