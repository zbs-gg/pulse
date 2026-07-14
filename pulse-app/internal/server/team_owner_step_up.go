package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const (
	OwnerApprovalRoutePath       = "/team/v1/owner/approval"
	OwnerStepUpAssertionVersion  = "pulse.owner_step_up.v1"
	ownerStepUpAssertionIssuer   = "pulse-team-gateway"
	ownerStepUpAssertionAudience = "pulse-team-daemon"
	ownerStepUpMaxAge            = 5 * time.Minute
	ownerStepUpMaxAssertionTTL   = 30 * time.Second
)

var (
	ErrOwnerStepUpInvalid         = errors.New("invalid owner step-up assertion")
	ErrOwnerStepUpRequestMismatch = errors.New("owner step-up assertion request mismatch")
)

type OwnerStepUpVerifierConfig struct {
	Store        *store.Store
	Keyring      PrincipalVerifyKeyring
	ExpectedRoot teamauth.BootstrapRoot
	Clock        func() time.Time
}

type OwnerStepUpVerifier struct {
	store        *store.Store
	keys         map[string]ed25519.PublicKey
	expectedRoot teamauth.BootstrapRoot
	clock        func() time.Time
}

type OwnerStepUpContext struct {
	Identity           store.OwnerStepUpIdentity
	AuthenticatedAt    time.Time
	AssertionKID       string
	AssertionJTI       string
	AssertionExpiresAt time.Time
}

type OwnerStepUpBinding struct {
	Action  string
	StoreID string
	TeamID  string
}

type ownerStepUpAssertionClaims struct {
	Version       string `json:"version"`
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	IssuedAt      int64  `json:"iat"`
	NotBefore     int64  `json:"nbf"`
	ExpiresAt     int64  `json:"exp"`
	JTI           string `json:"jti"`
	RequestID     string `json:"request_id"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	BodySHA256    string `json:"body_sha256"`
	Action        string `json:"action"`
	StoreID       string `json:"store_id"`
	TeamID        string `json:"team_id"`
	OAuthIssuer   string `json:"oauth_issuer"`
	OAuthSubject  string `json:"oauth_subject"`
	OAuthClientID string `json:"oauth_client_id"`
	AuthTime      int64  `json:"auth_time"`
}

func NewOwnerStepUpVerifier(cfg OwnerStepUpVerifierConfig) (*OwnerStepUpVerifier, error) {
	if cfg.Store == nil || cfg.ExpectedRoot.Validate() != nil || validatePrincipalKeyring(cfg.Keyring) != nil {
		return nil, ErrOwnerStepUpInvalid
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	keys := make(map[string]ed25519.PublicKey, len(cfg.Keyring.Keys))
	for kid, key := range cfg.Keyring.Keys {
		keys[kid] = append(ed25519.PublicKey(nil), key...)
	}
	return &OwnerStepUpVerifier{
		store: cfg.Store, keys: keys, expectedRoot: cfg.ExpectedRoot, clock: clock,
	}, nil
}

// VerifyApprovalRequest proves that a recent browser-authenticated Owner
// approved the exact action body reaching the loopback approval route. It
// deliberately does not consume assertion replay state: IssueOwnerApproval
// persists the (kid,jti) evidence atomically with the one-time nonce, including
// before bootstrap.
func (v *OwnerStepUpVerifier) VerifyApprovalRequest(
	ctx context.Context,
	compact, requestID, method, path string,
	body []byte,
	binding OwnerStepUpBinding,
) (OwnerStepUpContext, error) {
	if v == nil || len(compact) == 0 || len(compact) > PrincipalAssertionMaxBytes {
		return OwnerStepUpContext{}, ErrOwnerStepUpInvalid
	}
	var claims ownerStepUpAssertionClaims
	header, signingInput, signature, err := decodeCompactAssertion(
		compact, OwnerStepUpAssertionVersion, &claims,
	)
	if err != nil {
		return OwnerStepUpContext{}, ErrOwnerStepUpInvalid
	}
	key, ok := v.keys[header.Kid]
	if !ok || !ed25519.Verify(key, []byte(signingInput), signature) {
		return OwnerStepUpContext{}, ErrOwnerStepUpInvalid
	}
	if err := v.validateClaims(claims, OwnerApprovalRoutePath, nil); err != nil {
		return OwnerStepUpContext{}, err
	}
	digest := sha256.Sum256(body)
	if claims.RequestID != requestID || claims.Method != method || claims.Path != path ||
		claims.BodySHA256 != hex.EncodeToString(digest[:]) || claims.Action != binding.Action ||
		claims.StoreID != binding.StoreID || claims.TeamID != binding.TeamID {
		return OwnerStepUpContext{}, ErrOwnerStepUpRequestMismatch
	}
	presented := teamauth.BootstrapRoot{
		Issuer: claims.OAuthIssuer, Subject: claims.OAuthSubject, AdminClientID: claims.OAuthClientID,
	}
	if !v.expectedRoot.Matches(presented) {
		return OwnerStepUpContext{}, ErrOwnerStepUpInvalid
	}
	identity, err := v.store.ResolveOwnerStepUpIdentity(ctx, presented)
	if err != nil {
		return OwnerStepUpContext{}, err
	}
	if !identity.Bootstrap && (identity.StoreID != binding.StoreID || identity.TeamID != binding.TeamID) {
		return OwnerStepUpContext{}, ErrOwnerStepUpRequestMismatch
	}
	return OwnerStepUpContext{
		Identity: identity, AuthenticatedAt: time.Unix(claims.AuthTime, 0).UTC(),
		AssertionKID: header.Kid, AssertionJTI: claims.JTI,
		AssertionExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC(),
	}, nil
}

// VerifyTeamPublication implements TeamPublicationStepUpVerifier. The same
// gateway signing key and recent OAuth authentication used for Owner actions
// are reused, but the assertion is bound to the Airlock route, the exact
// canonical envelope bytes, and the one publication action. This keeps the
// browser handler from accepting an assertion minted for any other Owner
// operation.
func (v *OwnerStepUpVerifier) VerifyTeamPublication(
	ctx context.Context,
	request TeamPublicationStepUpVerificationRequest,
) (OwnerStepUpContext, error) {
	if v == nil || request.Path != TeamPublicationAirlockRoutePath ||
		request.Method != http.MethodPost || request.StoreID == "" || request.TeamID == "" ||
		request.PublisherPrincipalID == "" {
		return OwnerStepUpContext{}, ErrOwnerStepUpInvalid
	}
	return v.verifyBoundStepUpAssertion(
		ctx, request.Assertion, request.RequestID, request.Method, request.Path,
		request.CanonicalEnvelope,
		OwnerStepUpBinding{
			Action: "team.commons.publish", StoreID: request.StoreID, TeamID: request.TeamID,
		},
		map[string]bool{"team.commons.publish": true},
	)
}

func (v *OwnerStepUpVerifier) verifyBoundStepUpAssertion(
	ctx context.Context,
	compact, requestID, method, path string,
	body []byte,
	binding OwnerStepUpBinding,
	allowedActions map[string]bool,
) (OwnerStepUpContext, error) {
	if v == nil || len(compact) == 0 || len(compact) > PrincipalAssertionMaxBytes {
		return OwnerStepUpContext{}, ErrOwnerStepUpInvalid
	}
	var claims ownerStepUpAssertionClaims
	header, signingInput, signature, err := decodeCompactAssertion(
		compact, OwnerStepUpAssertionVersion, &claims,
	)
	if err != nil {
		return OwnerStepUpContext{}, ErrOwnerStepUpInvalid
	}
	key, ok := v.keys[header.Kid]
	if !ok || !ed25519.Verify(key, []byte(signingInput), signature) {
		return OwnerStepUpContext{}, ErrOwnerStepUpInvalid
	}
	if err := v.validateClaims(claims, path, allowedActions); err != nil {
		return OwnerStepUpContext{}, err
	}
	digest := sha256.Sum256(body)
	if claims.RequestID != requestID || claims.Method != method || claims.Path != path ||
		claims.BodySHA256 != hex.EncodeToString(digest[:]) || claims.Action != binding.Action ||
		claims.StoreID != binding.StoreID || claims.TeamID != binding.TeamID {
		return OwnerStepUpContext{}, ErrOwnerStepUpRequestMismatch
	}
	presented := teamauth.BootstrapRoot{
		Issuer: claims.OAuthIssuer, Subject: claims.OAuthSubject, AdminClientID: claims.OAuthClientID,
	}
	if !v.expectedRoot.Matches(presented) {
		return OwnerStepUpContext{}, ErrOwnerStepUpInvalid
	}
	identity, err := v.store.ResolveOwnerStepUpIdentity(ctx, presented)
	if err != nil {
		return OwnerStepUpContext{}, err
	}
	if !identity.Bootstrap && (identity.StoreID != binding.StoreID || identity.TeamID != binding.TeamID) {
		return OwnerStepUpContext{}, ErrOwnerStepUpRequestMismatch
	}
	return OwnerStepUpContext{
		Identity: identity, AuthenticatedAt: time.Unix(claims.AuthTime, 0).UTC(),
		AssertionKID: header.Kid, AssertionJTI: claims.JTI,
		AssertionExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC(),
	}, nil
}

func (v *OwnerStepUpVerifier) validateClaims(
	claims ownerStepUpAssertionClaims,
	expectedPath string,
	allowedActions map[string]bool,
) error {
	now := v.clock().UTC()
	nowUnix := now.Unix()
	if claims.Version != OwnerStepUpAssertionVersion || claims.Issuer != ownerStepUpAssertionIssuer ||
		claims.Audience != ownerStepUpAssertionAudience || !safeOpaque(claims.JTI, 128) ||
		len(claims.RequestID) < 8 || !safeOpaque(claims.RequestID, 64) ||
		claims.Method != http.MethodPost || claims.Path != expectedPath ||
		(allowedActions != nil && !allowedActions[claims.Action]) ||
		len(claims.BodySHA256) != 64 || !validTeamAdminClientKey(claims.BodySHA256) ||
		!validTeamAdminClass(claims.Action) || !validTeamAdminOpaque(claims.StoreID) ||
		!validTeamAdminOpaque(claims.TeamID) ||
		!exactIdentity(claims.OAuthIssuer, 512) || !exactIdentity(claims.OAuthSubject, 512) ||
		!exactIdentity(claims.OAuthClientID, 512) || claims.IssuedAt <= 0 || claims.NotBefore <= 0 ||
		claims.ExpiresAt <= 0 || claims.AuthTime <= 0 ||
		claims.IssuedAt > nowUnix+principalAssertionClockSkewSecs ||
		claims.NotBefore > nowUnix+principalAssertionClockSkewSecs ||
		claims.ExpiresAt < nowUnix-principalAssertionClockSkewSecs ||
		claims.ExpiresAt <= claims.IssuedAt || claims.NotBefore >= claims.ExpiresAt ||
		time.Duration(claims.ExpiresAt-claims.IssuedAt)*time.Second > ownerStepUpMaxAssertionTTL ||
		claims.NotBefore < claims.IssuedAt-principalAssertionClockSkewSecs {
		return ErrOwnerStepUpInvalid
	}
	authenticatedAt := time.Unix(claims.AuthTime, 0).UTC()
	if authenticatedAt.After(now.Add(time.Duration(principalAssertionClockSkewSecs)*time.Second)) ||
		now.Sub(authenticatedAt) > ownerStepUpMaxAge {
		return ErrOwnerStepUpInvalid
	}
	return nil
}
