package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const (
	PrincipalCheckRoutePath         = "/team/v1/principal/check"
	PrincipalCheckMaxBodyBytes      = 8 << 10
	PrincipalAssertionMaxBytes      = 8 << 10
	PrincipalVerifyKeyringMaxBytes  = 16 << 10
	PrincipalVerifyKeyringEnv       = "PULSE_TEAM_PRINCIPAL_VERIFY_KEYRING_FILE"
	principalAssertionVersion       = "pulse.principal.v1"
	securityEventAssertionVersion   = "pulse.security_event.v1"
	principalContextVersion         = "pulse.team.principal_context.v1"
	principalAssertionIssuer        = "pulse-team-gateway"
	principalAssertionAudience      = "pulse-team-daemon"
	principalAssertionClockSkewSecs = int64(5)
	principalReplayPruneInterval    = 30 * time.Second
)

var (
	ErrPrincipalInvalid          = errors.New("invalid principal assertion")
	ErrPrincipalRequestMismatch  = errors.New("principal assertion request mismatch")
	ErrPrincipalReplay           = errors.New("principal assertion replay")
	ErrPrincipalRevoked          = errors.New("principal revoked")
	ErrPrincipalStoreUnavailable = errors.New("principal store unavailable")
)

type PrincipalVerifyKeyring struct {
	ActiveKid string
	Keys      map[string]ed25519.PublicKey
}

type PrincipalVerifierConfig struct {
	Store           *store.Store
	Keyring         PrincipalVerifyKeyring
	ExpectedStoreID string
	ExpectedTeamID  string
	Clock           func() time.Time
	WriterLease     *store.TeamWriterLease
}

type PrincipalVerifier struct {
	store           *store.Store
	keys            map[string]ed25519.PublicKey
	expectedStoreID string
	expectedTeamID  string
	clock           func() time.Time
	replayPruner    principalReplayPruner

	replayPruneMu         sync.Mutex
	replayPruneInFlight   bool
	nextReplayPrune       time.Time
	replayPruneInterval   time.Duration
	onReplayPruneDegraded func()
	writerLease           *store.TeamWriterLease
}

type principalReplayPruner interface {
	PruneExpiredAssertionIDs(context.Context) (int64, error)
}

type principalReplayPrunerFunc func(context.Context) (int64, error)

func (f principalReplayPrunerFunc) PruneExpiredAssertionIDs(ctx context.Context) (int64, error) {
	return f(ctx)
}

type PrincipalContext struct {
	Version             string   `json:"version"`
	RequestID           string   `json:"request_id"`
	StoreID             string   `json:"store_id"`
	TeamID              string   `json:"team_id"`
	PrincipalID         string   `json:"principal_id"`
	PrincipalKind       string   `json:"principal_kind"`
	OAuthClientKey      string   `json:"oauth_client_key"`
	HumanPrincipalID    *string  `json:"human_principal_id"`
	AgentBindingID      *string  `json:"agent_binding_id"`
	MembershipID        string   `json:"membership_id"`
	MembershipRole      string   `json:"membership_role"`
	TeamAuthEpoch       int64    `json:"team_auth_epoch"`
	PrincipalAuthEpoch  int64    `json:"principal_auth_epoch"`
	BindingAuthEpoch    *int64   `json:"binding_auth_epoch"`
	MembershipAuthEpoch int64    `json:"membership_auth_epoch"`
	Capabilities        []string `json:"capabilities"`
}

type principalAssertionHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type principalAssertionClaims struct {
	Version       string   `json:"version"`
	Issuer        string   `json:"iss"`
	Audience      string   `json:"aud"`
	IssuedAt      int64    `json:"iat"`
	NotBefore     int64    `json:"nbf"`
	ExpiresAt     int64    `json:"exp"`
	JTI           string   `json:"jti"`
	RequestID     string   `json:"request_id"`
	Method        string   `json:"method"`
	Path          string   `json:"path"`
	BodySHA256    string   `json:"body_sha256"`
	StoreID       string   `json:"store_id"`
	TeamID        string   `json:"team_id"`
	OAuthIssuer   string   `json:"oauth_issuer"`
	OAuthSubject  string   `json:"oauth_subject"`
	OAuthClientID string   `json:"oauth_client_id"`
	GrantKind     string   `json:"grant_kind"`
	Capabilities  []string `json:"capabilities"`
}

type securityEventAssertionClaims struct {
	Version    string `json:"version"`
	Issuer     string `json:"iss"`
	Audience   string `json:"aud"`
	IssuedAt   int64  `json:"iat"`
	NotBefore  int64  `json:"nbf"`
	ExpiresAt  int64  `json:"exp"`
	JTI        string `json:"jti"`
	RequestID  string `json:"request_id"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	BodySHA256 string `json:"body_sha256"`
	StoreID    string `json:"store_id"`
	TeamID     string `json:"team_id"`
}

type principalCheckBody struct {
	OAuthIssuer   string   `json:"oauth_issuer"`
	OAuthSubject  string   `json:"oauth_subject"`
	OAuthClientID string   `json:"oauth_client_id"`
	Capabilities  []string `json:"capabilities"`
}

func NewPrincipalVerifier(cfg PrincipalVerifierConfig) (*PrincipalVerifier, error) {
	if cfg.Store == nil || cfg.ExpectedStoreID == "" || cfg.ExpectedTeamID == "" {
		return nil, errors.New("principal verifier requires team store identity")
	}
	if err := validatePrincipalKeyring(cfg.Keyring); err != nil {
		return nil, err
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	keys := make(map[string]ed25519.PublicKey, len(cfg.Keyring.Keys))
	for kid, key := range cfg.Keyring.Keys {
		keys[kid] = append(ed25519.PublicKey(nil), key...)
	}
	var writerLease *store.TeamWriterLease
	if cfg.WriterLease != nil {
		lease := *cfg.WriterLease
		if lease.StoreID != cfg.ExpectedStoreID || lease.TeamID != cfg.ExpectedTeamID ||
			lease.WriterID == "" || lease.Token == "" || lease.WriterVersion < teamauth.SchemaVersion {
			return nil, errors.New("principal verifier writer lease identity mismatch")
		}
		writerLease = &lease
	}
	return &PrincipalVerifier{
		store: cfg.Store, keys: keys, expectedStoreID: cfg.ExpectedStoreID, expectedTeamID: cfg.ExpectedTeamID, clock: clock,
		replayPruner: cfg.Store, replayPruneInterval: principalReplayPruneInterval,
		onReplayPruneDegraded: func() { slog.Warn("principal assertion replay pruning degraded") },
		writerLease:           writerLease,
	}, nil
}

func (v *PrincipalVerifier) VerifyRequest(ctx context.Context, compact, requestID, method, requestPath string, body []byte) (PrincipalContext, error) {
	if len(compact) == 0 || len(compact) > PrincipalAssertionMaxBytes {
		return PrincipalContext{}, ErrPrincipalInvalid
	}
	requestBody, err := decodePrincipalCheckBody(body)
	if err != nil {
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
	if err := v.validateClaims(claims); err != nil {
		return PrincipalContext{}, err
	}
	digest := sha256.Sum256(body)
	if claims.RequestID != requestID || claims.Method != method || claims.Path != requestPath ||
		claims.BodySHA256 != hex.EncodeToString(digest[:]) || claims.StoreID != v.expectedStoreID || claims.TeamID != v.expectedTeamID ||
		claims.OAuthIssuer != requestBody.OAuthIssuer || claims.OAuthSubject != requestBody.OAuthSubject ||
		claims.OAuthClientID != requestBody.OAuthClientID || !equalStrings(claims.Capabilities, requestBody.Capabilities) {
		return PrincipalContext{}, ErrPrincipalRequestMismatch
	}

	registration, err := v.store.ResolveOAuthClient(ctx, claims.OAuthIssuer, claims.OAuthClientID)
	if err != nil {
		return PrincipalContext{}, classifyPrincipalStoreError(err)
	}
	var resolved store.ResolvedTeamPrincipal
	switch registration.Kind {
	case "agent":
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
	case "service":
		resolved, err = v.store.ResolveServiceIdentity(ctx, claims.OAuthIssuer, claims.OAuthClientID)
		if err != nil {
			return PrincipalContext{}, classifyPrincipalStoreError(err)
		}
		if resolved.PrincipalID != registration.PrincipalID || resolved.Kind != "service" {
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

func (v *PrincipalVerifier) consumeAssertionID(ctx context.Context, kid, jti string, expiresAt time.Time) error {
	if v.writerLease == nil {
		return v.store.ConsumeAssertionID(ctx, kid, jti, expiresAt)
	}
	return v.store.ConsumeAssertionIDWithWriterLease(
		ctx, kid, jti, expiresAt, v.writerLease.WriterID, v.writerLease.Token,
	)
}

// VerifyGatewayEvent authenticates a content-free pre-principal security
// event as coming from the configured gateway. It deliberately carries no
// OAuth subject, client, role, or capability fields.
func (v *PrincipalVerifier) VerifyGatewayEvent(ctx context.Context, compact, requestID, method, requestPath string, body []byte) error {
	if len(compact) == 0 || len(compact) > PrincipalAssertionMaxBytes {
		return ErrPrincipalInvalid
	}
	header, claims, signingInput, signature, err := decodeSecurityEventAssertion(compact)
	if err != nil {
		return ErrPrincipalInvalid
	}
	key, ok := v.keys[header.Kid]
	if !ok || !ed25519.Verify(key, []byte(signingInput), signature) {
		return ErrPrincipalInvalid
	}
	if err := v.validateSecurityEventClaims(claims); err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	if claims.RequestID != requestID || claims.Method != method || claims.Path != requestPath ||
		claims.BodySHA256 != hex.EncodeToString(digest[:]) || claims.StoreID != v.expectedStoreID || claims.TeamID != v.expectedTeamID {
		return ErrPrincipalRequestMismatch
	}
	if err := v.consumeAssertionID(ctx, header.Kid, claims.JTI, time.Unix(claims.ExpiresAt, 0)); err != nil {
		if errors.Is(err, store.ErrAssertionReplay) {
			return ErrPrincipalReplay
		}
		if errors.Is(err, store.ErrAssertionExpired) {
			return ErrPrincipalInvalid
		}
		return ErrPrincipalStoreUnavailable
	}
	v.maybePruneExpiredAssertions(ctx)
	return nil
}

func (v *PrincipalVerifier) maybePruneExpiredAssertions(ctx context.Context) {
	now := v.clock()
	v.replayPruneMu.Lock()
	if v.replayPruneInFlight || (!v.nextReplayPrune.IsZero() && now.Before(v.nextReplayPrune)) {
		v.replayPruneMu.Unlock()
		return
	}
	v.replayPruneInFlight = true
	v.replayPruneMu.Unlock()

	var err error
	if v.writerLease == nil {
		_, err = v.replayPruner.PruneExpiredAssertionIDs(ctx)
	} else {
		_, err = v.store.PruneExpiredAssertionIDsWithWriterLease(ctx, v.writerLease.WriterID, v.writerLease.Token)
	}
	v.replayPruneMu.Lock()
	v.replayPruneInFlight = false
	v.nextReplayPrune = v.clock().Add(v.replayPruneInterval)
	v.replayPruneMu.Unlock()
	if err != nil && v.onReplayPruneDegraded != nil {
		v.onReplayPruneDegraded()
	}
}

func (v *PrincipalVerifier) validateClaims(claims principalAssertionClaims) error {
	now := v.clock().Unix()
	if claims.Version != principalAssertionVersion || claims.Issuer != principalAssertionIssuer || claims.Audience != principalAssertionAudience ||
		claims.GrantKind != "registered" || !safeOpaque(claims.JTI, 128) || !safeOpaque(claims.RequestID, 64) ||
		!exactIdentity(claims.OAuthIssuer, 512) || !exactIdentity(claims.OAuthSubject, 512) || !exactIdentity(claims.OAuthClientID, 512) ||
		claims.Method != http.MethodPost || claims.Path != PrincipalCheckRoutePath ||
		len(claims.BodySHA256) != 64 || strings.ToLower(claims.BodySHA256) != claims.BodySHA256 ||
		claims.ExpiresAt < now-principalAssertionClockSkewSecs || claims.NotBefore > now+principalAssertionClockSkewSecs ||
		claims.IssuedAt <= 0 || claims.NotBefore <= 0 || claims.ExpiresAt <= 0 || claims.IssuedAt > now+principalAssertionClockSkewSecs ||
		claims.ExpiresAt <= claims.IssuedAt || claims.NotBefore >= claims.ExpiresAt || claims.ExpiresAt-claims.IssuedAt > 30 || claims.NotBefore < claims.IssuedAt-principalAssertionClockSkewSecs ||
		!validCapabilities(claims.Capabilities) {
		return ErrPrincipalInvalid
	}
	if _, err := hex.DecodeString(claims.BodySHA256); err != nil {
		return ErrPrincipalInvalid
	}
	return nil
}

func (v *PrincipalVerifier) validateSecurityEventClaims(claims securityEventAssertionClaims) error {
	now := v.clock().Unix()
	if claims.Version != securityEventAssertionVersion || claims.Issuer != principalAssertionIssuer || claims.Audience != principalAssertionAudience ||
		!safeOpaque(claims.JTI, 128) || !safeOpaque(claims.RequestID, 64) ||
		claims.Method != http.MethodPost || claims.Path != SecurityEventRoutePath ||
		len(claims.BodySHA256) != 64 || strings.ToLower(claims.BodySHA256) != claims.BodySHA256 ||
		claims.ExpiresAt < now-principalAssertionClockSkewSecs || claims.NotBefore > now+principalAssertionClockSkewSecs ||
		claims.IssuedAt <= 0 || claims.NotBefore <= 0 || claims.ExpiresAt <= 0 || claims.IssuedAt > now+principalAssertionClockSkewSecs ||
		claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt > 30 || claims.NotBefore >= claims.ExpiresAt ||
		claims.NotBefore < claims.IssuedAt-principalAssertionClockSkewSecs {
		return ErrPrincipalInvalid
	}
	if _, err := hex.DecodeString(claims.BodySHA256); err != nil {
		return ErrPrincipalInvalid
	}
	return nil
}

func decodePrincipalAssertion(compact string) (principalAssertionHeader, principalAssertionClaims, string, []byte, error) {
	var claims principalAssertionClaims
	header, signingInput, signature, err := decodeCompactAssertion(compact, principalAssertionVersion, &claims)
	return header, claims, signingInput, signature, err
}

func decodeSecurityEventAssertion(compact string) (principalAssertionHeader, securityEventAssertionClaims, string, []byte, error) {
	var claims securityEventAssertionClaims
	header, signingInput, signature, err := decodeCompactAssertion(compact, securityEventAssertionVersion, &claims)
	return header, claims, signingInput, signature, err
}

func decodeCompactAssertion(compact, expectedType string, claims any) (principalAssertionHeader, string, []byte, error) {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return principalAssertionHeader{}, "", nil, ErrPrincipalInvalid
	}
	headerBytes, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return principalAssertionHeader{}, "", nil, err
	}
	payloadBytes, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return principalAssertionHeader{}, "", nil, err
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return principalAssertionHeader{}, "", nil, ErrPrincipalInvalid
	}
	var header principalAssertionHeader
	if err := decodeStrictJSON(headerBytes, &header); err != nil || header.Alg != "EdDSA" || header.Typ != expectedType || !safeOpaque(header.Kid, 128) {
		return principalAssertionHeader{}, "", nil, ErrPrincipalInvalid
	}
	if err := decodeStrictJSON(payloadBytes, claims); err != nil {
		return principalAssertionHeader{}, "", nil, ErrPrincipalInvalid
	}
	return header, parts[0] + "." + parts[1], signature, nil
}

func NewPrincipalCheckHandler(verifier *PrincipalVerifier) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != PrincipalCheckRoutePath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Content-Encoding") != "" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, PrincipalCheckMaxBodyBytes+1))
		if err != nil || len(raw) > PrincipalCheckMaxBodyBytes {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		if _, err := decodePrincipalCheckBody(raw); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		assertionValues := r.Header.Values("X-Pulse-Principal")
		requestIDValues := r.Header.Values("X-Pulse-Request-ID")
		if len(assertionValues) != 1 || len(requestIDValues) != 1 || assertionValues[0] == "" || requestIDValues[0] == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		assertion, requestID := assertionValues[0], requestIDValues[0]
		principal, err := verifier.VerifyRequest(r.Context(), assertion, requestID, r.Method, r.URL.EscapedPath(), raw)
		if err != nil {
			status := http.StatusUnauthorized
			code := "invalid_principal"
			if errors.Is(err, ErrPrincipalRevoked) {
				status = http.StatusForbidden
				code = "principal_revoked"
			} else if errors.Is(err, ErrPrincipalReplay) {
				code = "principal_replay"
			} else if errors.Is(err, ErrPrincipalRequestMismatch) {
				code = "principal_request_mismatch"
			} else if errors.Is(err, ErrPrincipalStoreUnavailable) {
				status = http.StatusServiceUnavailable
				code = "principal_store_unavailable"
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(principal)
	})
}

func classifyPrincipalStoreError(err error) error {
	if errors.Is(err, store.ErrPrincipalRevoked) || errors.Is(err, store.ErrMembershipRequired) {
		return ErrPrincipalRevoked
	}
	return ErrPrincipalStoreUnavailable
}

type PrincipalRouteRegistrar interface {
	Method(method, path string, handler http.Handler)
}

func RegisterPrincipalCheckRoute(registrar PrincipalRouteRegistrar, verifier *PrincipalVerifier) {
	registrar.Method(http.MethodPost, PrincipalCheckRoutePath, NewPrincipalCheckHandler(verifier))
}

type principalContextKey struct{}

func WithPrincipalContext(ctx context.Context, principal PrincipalContext) context.Context {
	return context.WithValue(ctx, principalContextKey{}, clonePrincipalContext(principal))
}

func PrincipalContextFromContext(ctx context.Context) (PrincipalContext, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(PrincipalContext)
	return clonePrincipalContext(principal), ok
}

func clonePrincipalContext(principal PrincipalContext) PrincipalContext {
	principal.Capabilities = append([]string(nil), principal.Capabilities...)
	if principal.HumanPrincipalID != nil {
		value := *principal.HumanPrincipalID
		principal.HumanPrincipalID = &value
	}
	if principal.AgentBindingID != nil {
		value := *principal.AgentBindingID
		principal.AgentBindingID = &value
	}
	if principal.BindingAuthEpoch != nil {
		value := *principal.BindingAuthEpoch
		principal.BindingAuthEpoch = &value
	}
	return principal
}

func principalContextFromResolved(claims principalAssertionClaims, oauthClientKey string, resolved store.ResolvedTeamPrincipal) PrincipalContext {
	principal := PrincipalContext{
		Version: principalContextVersion, RequestID: claims.RequestID, StoreID: resolved.StoreID, TeamID: resolved.TeamID,
		PrincipalID: resolved.PrincipalID, PrincipalKind: resolved.Kind, OAuthClientKey: oauthClientKey, MembershipID: resolved.MembershipID,
		MembershipRole: resolved.MembershipRole, TeamAuthEpoch: resolved.TeamEpoch, PrincipalAuthEpoch: resolved.PrincipalEpoch,
		MembershipAuthEpoch: resolved.MembershipEpoch, Capabilities: append([]string(nil), claims.Capabilities...),
	}
	if resolved.Kind == "agent" {
		human, binding, epoch := resolved.HumanPrincipalID, resolved.BindingID, resolved.BindingEpoch
		principal.HumanPrincipalID, principal.AgentBindingID, principal.BindingAuthEpoch = &human, &binding, &epoch
	}
	return principal
}

func decodePrincipalCheckBody(raw []byte) (principalCheckBody, error) {
	var body principalCheckBody
	if len(raw) == 0 || len(raw) > PrincipalCheckMaxBodyBytes {
		return body, ErrPrincipalInvalid
	}
	if err := decodeStrictJSON(raw, &body); err != nil || !exactIdentity(body.OAuthIssuer, 512) || !exactIdentity(body.OAuthSubject, 512) ||
		!exactIdentity(body.OAuthClientID, 512) || !validCapabilities(body.Capabilities) {
		return principalCheckBody{}, ErrPrincipalInvalid
	}
	return body, nil
}

func decodeStrictJSON(raw []byte, target any) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var readValue func() error
	readValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON object key")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate JSON object key")
				}
				seen[key] = struct{}{}
				if err := readValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
		case '[':
			for decoder.More() {
				if err := readValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := readValue(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func validCapabilities(capabilities []string) bool {
	if len(capabilities) == 0 || len(capabilities) > 6 {
		return false
	}
	allowed := map[string]struct{}{"pulse:connect": {}, "pulse:status": {}, "pulse:read": {}, "pulse:write": {}, "pulse:audit": {}, "pulse:delete": {}}
	if !sort.StringsAreSorted(capabilities) {
		return false
	}
	hasConnect := false
	for i, capability := range capabilities {
		if _, ok := allowed[capability]; !ok || (i > 0 && capabilities[i-1] == capability) {
			return false
		}
		hasConnect = hasConnect || capability == "pulse:connect"
	}
	return hasConnect
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func exactIdentity(value string, max int) bool {
	if value == "" || len(value) > max || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func safeOpaque(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

type principalKeyFileEntry struct {
	Kid       string `json:"kid"`
	PublicKey string `json:"public_key"`
}

type principalKeyringFile struct {
	Active   principalKeyFileEntry   `json:"active"`
	Previous []principalKeyFileEntry `json:"previous"`
}

func LoadPrincipalVerifyKeyringFromEnv() (PrincipalVerifyKeyring, error) {
	path := os.Getenv(PrincipalVerifyKeyringEnv)
	if path == "" {
		return PrincipalVerifyKeyring{}, fmt.Errorf("%s is required", PrincipalVerifyKeyringEnv)
	}
	return loadPrincipalVerifyKeyring(path, uint32(os.Geteuid()))
}

func LoadPrincipalVerifyKeyring(path string) (PrincipalVerifyKeyring, error) {
	return loadPrincipalVerifyKeyring(path, uint32(os.Geteuid()))
}

func loadPrincipalVerifyKeyring(path string, expectedUID uint32) (PrincipalVerifyKeyring, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return PrincipalVerifyKeyring{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return PrincipalVerifyKeyring{}, errors.New("open principal verify keyring")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return PrincipalVerifyKeyring{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > PrincipalVerifyKeyringMaxBytes ||
		info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o400 == 0 || info.Mode().Perm()&0o111 != 0 {
		return PrincipalVerifyKeyring{}, errors.New("unsafe principal verify keyring file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != expectedUID {
		return PrincipalVerifyKeyring{}, errors.New("principal verify keyring owner mismatch")
	}
	raw, err := io.ReadAll(io.LimitReader(file, PrincipalVerifyKeyringMaxBytes+1))
	if err != nil {
		return PrincipalVerifyKeyring{}, err
	}
	if len(raw) == 0 || len(raw) > PrincipalVerifyKeyringMaxBytes {
		return PrincipalVerifyKeyring{}, errors.New("invalid principal verify keyring size")
	}
	var parsed principalKeyringFile
	if err := decodeStrictJSON(raw, &parsed); err != nil || len(parsed.Previous) > 4 {
		return PrincipalVerifyKeyring{}, errors.New("invalid principal verify keyring")
	}
	keyring := PrincipalVerifyKeyring{ActiveKid: parsed.Active.Kid, Keys: make(map[string]ed25519.PublicKey, 1+len(parsed.Previous))}
	for _, entry := range append([]principalKeyFileEntry{parsed.Active}, parsed.Previous...) {
		if !safeOpaque(entry.Kid, 128) {
			return PrincipalVerifyKeyring{}, errors.New("invalid principal key id")
		}
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(entry.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return PrincipalVerifyKeyring{}, errors.New("invalid Ed25519 public key")
		}
		if _, exists := keyring.Keys[entry.Kid]; exists {
			return PrincipalVerifyKeyring{}, errors.New("duplicate principal key id")
		}
		keyring.Keys[entry.Kid] = ed25519.PublicKey(decoded)
	}
	if err := validatePrincipalKeyring(keyring); err != nil {
		return PrincipalVerifyKeyring{}, err
	}
	return keyring, nil
}

func validatePrincipalKeyring(keyring PrincipalVerifyKeyring) error {
	if !safeOpaque(keyring.ActiveKid, 128) || len(keyring.Keys) == 0 || len(keyring.Keys) > 5 {
		return errors.New("invalid principal verify keyring")
	}
	if _, ok := keyring.Keys[keyring.ActiveKid]; !ok {
		return errors.New("active principal key missing")
	}
	for kid, key := range keyring.Keys {
		if !safeOpaque(kid, 128) || len(key) != ed25519.PublicKeySize {
			return errors.New("invalid principal verify key")
		}
	}
	return nil
}
