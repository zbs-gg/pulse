package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

const (
	MemoryPresentationSurfaceHome = "memory_home"

	MemoryPresentationAuthorityHomeBrowser MemoryPresentationAuthority = "home_browser_session"

	memoryPresentationCapabilitySchema  = "pulse.memory_presentation_capability.v1"
	memoryPresentationKeyBytes          = 32
	memoryPresentationNonceBytes        = 32
	memoryPresentationBrowserValueBytes = 32
	memoryPresentationMaxCapabilityTTL  = 2 * time.Minute
	memoryPresentationMaxTokenBytes     = 4096
	memoryPresentationScopedHomePath    = "/home/s/{route}/present"
)

var (
	ErrMemoryPresentationUnauthorized  = errors.New("memory presentation authority rejected")
	ErrMemoryPresentationExpired       = errors.New("memory presentation capability expired")
	ErrMemoryPresentationReplay        = errors.New("memory presentation capability replayed")
	ErrMemoryPresentationStoreMismatch = errors.New("memory presentation store receipt mismatch")

	memoryPresentationCandidatePattern = regexp.MustCompile(`^candidate_[a-f0-9]{32}$`)
	memoryPresentationDigestPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// MemoryPresentationAuthority is supplied by the route group, never decoded
// from a browser body. Only the authenticated Home browser route may use the
// sole accepted value; daemon, MCP, tool, hook, model and ambient loopback
// callers therefore cannot promote themselves with ordinary local authority.
type MemoryPresentationAuthority string

type MemoryPresentationStore interface {
	PresentMemoryTrayCandidate(
		store.MemoryPresentationRequest,
		time.Time,
		time.Duration,
	) (store.MemoryPresentationReceipt, error)
}

type MemoryPresentationServiceConfig struct {
	Store          MemoryPresentationStore
	Schedule       func(store.MemoryPresentationReceipt, time.Duration)
	ExpectedOrigin string
	ExpectedPath   string
	GracePeriod    time.Duration
	CapabilityTTL  time.Duration
	Clock          func() time.Time
	Random         io.Reader
}

// MemoryPresentationBinding is the exact card and authenticated Home surface
// for which a short-lived capability is minted. Browser credentials are only
// hashed into the capability payload; the persistent daemon key is forbidden.
type MemoryPresentationBinding struct {
	BrowserSessionID       string
	CSRFToken              string
	WorkspaceBindingDigest string
	CandidateID            string
	CandidateVersion       int
	ContentDigest          string
	TrustedSurfaceInstance string
}

type MemoryPresentationAttempt struct {
	Authority  MemoryPresentationAuthority
	Capability string
	Binding    MemoryPresentationBinding
}

type memoryPresentationCapabilityClaims struct {
	Schema                     string `json:"schema"`
	Nonce                      string `json:"nonce"`
	BrowserSessionDigest       string `json:"browser_session_digest"`
	CSRFDigest                 string `json:"csrf_digest"`
	WorkspaceBindingDigest     string `json:"workspace_binding_digest"`
	CandidateID                string `json:"candidate_id"`
	CandidateVersion           int    `json:"candidate_version"`
	ContentDigest              string `json:"content_digest"`
	TrustedSurfaceKind         string `json:"trusted_surface_kind"`
	TrustedSurfaceInstanceHash string `json:"trusted_surface_instance_digest"`
	IssuedAtUnixNano           int64  `json:"issued_at_unix_nano"`
	ExpiresAtUnixNano          int64  `json:"expires_at_unix_nano"`
}

type MemoryPresentationService struct {
	store          MemoryPresentationStore
	expectedOrigin string
	expectedHost   string
	expectedPath   string
	gracePeriod    time.Duration
	capabilityTTL  time.Duration
	clock          func() time.Time
	random         io.Reader
	signingKey     []byte
	schedule       func(store.MemoryPresentationReceipt, time.Duration)

	randomMu   sync.Mutex
	mu         sync.Mutex
	usedNonces map[string]time.Time
}

func NewMemoryPresentationService(cfg MemoryPresentationServiceConfig) (*MemoryPresentationService, error) {
	if cfg.Store == nil {
		return nil, errors.New("memory presentation: store is required")
	}
	if cfg.Schedule == nil {
		return nil, errors.New("memory presentation: commit scheduler is required")
	}
	origin, host, err := validateMemoryPresentationOrigin(cfg.ExpectedOrigin)
	if err != nil {
		return nil, err
	}
	if !validMemoryPresentationPath(cfg.ExpectedPath) {
		return nil, errors.New("memory presentation: exact Home path is required")
	}
	if cfg.GracePeriod < 0 || cfg.GracePeriod > 30*time.Second {
		return nil, errors.New("memory presentation: write delay must be between 0s and 30s")
	}
	if cfg.CapabilityTTL < time.Second || cfg.CapabilityTTL > memoryPresentationMaxCapabilityTTL {
		return nil, errors.New("memory presentation: capability TTL must be between 1s and 2m")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	random := cfg.Random
	if random == nil {
		random = rand.Reader
	}
	key := make([]byte, memoryPresentationKeyBytes)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, errors.New("memory presentation: generate ephemeral signing key")
	}
	return &MemoryPresentationService{
		store: cfg.Store, expectedOrigin: origin, expectedHost: host, expectedPath: cfg.ExpectedPath,
		gracePeriod: cfg.GracePeriod, capabilityTTL: cfg.CapabilityTTL,
		clock: clock, random: random, signingKey: key, schedule: cfg.Schedule,
		usedNonces: make(map[string]time.Time),
	}, nil
}

func (s *MemoryPresentationService) IssueCapability(binding MemoryPresentationBinding) (string, error) {
	if s == nil || !validMemoryPresentationBinding(binding) {
		return "", ErrMemoryPresentationUnauthorized
	}
	nonceBytes := make([]byte, memoryPresentationNonceBytes)
	s.randomMu.Lock()
	_, randomErr := io.ReadFull(s.random, nonceBytes)
	s.randomMu.Unlock()
	if randomErr != nil {
		return "", errors.New("memory presentation: generate capability nonce")
	}
	now := s.clock().UTC()
	claims := memoryPresentationCapabilityClaims{
		Schema:                     memoryPresentationCapabilitySchema,
		Nonce:                      base64.RawURLEncoding.EncodeToString(nonceBytes),
		BrowserSessionDigest:       memoryPresentationBoundDigest("browser_session", binding.BrowserSessionID),
		CSRFDigest:                 memoryPresentationBoundDigest("csrf", binding.CSRFToken),
		WorkspaceBindingDigest:     binding.WorkspaceBindingDigest,
		CandidateID:                binding.CandidateID,
		CandidateVersion:           binding.CandidateVersion,
		ContentDigest:              binding.ContentDigest,
		TrustedSurfaceKind:         MemoryPresentationSurfaceHome,
		TrustedSurfaceInstanceHash: memoryPresentationBoundDigest("surface_instance", binding.TrustedSurfaceInstance),
		IssuedAtUnixNano:           now.UnixNano(),
		ExpiresAtUnixNano:          now.Add(s.capabilityTTL).UnixNano(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", errors.New("memory presentation: encode capability")
	}
	signature := memoryPresentationMAC(s.signingKey, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *MemoryPresentationService) Present(
	ctx context.Context,
	r *http.Request,
	attempt MemoryPresentationAttempt,
) (store.MemoryPresentationReceipt, error) {
	if s == nil || ctx == nil || attempt.Authority != MemoryPresentationAuthorityHomeBrowser ||
		!validMemoryPresentationBinding(attempt.Binding) || !s.validBrowserRequest(r) {
		return store.MemoryPresentationReceipt{}, ErrMemoryPresentationUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return store.MemoryPresentationReceipt{}, err
	}
	now := s.clock().UTC()
	claims, err := s.verifyCapability(attempt.Capability, now)
	if err != nil {
		return store.MemoryPresentationReceipt{}, err
	}
	if !claims.match(attempt.Binding) {
		return store.MemoryPresentationReceipt{}, ErrMemoryPresentationUnauthorized
	}
	if !s.consumeNonce(claims.Nonce, time.Unix(0, claims.ExpiresAtUnixNano).UTC(), now) {
		return store.MemoryPresentationReceipt{}, ErrMemoryPresentationReplay
	}
	req := store.MemoryPresentationRequest{
		CandidateID:            attempt.Binding.CandidateID,
		CandidateVersion:       attempt.Binding.CandidateVersion,
		ContentDigest:          attempt.Binding.ContentDigest,
		BindingDigest:          attempt.Binding.WorkspaceBindingDigest,
		TrustedSurfaceKind:     MemoryPresentationSurfaceHome,
		TrustedSurfaceInstance: attempt.Binding.TrustedSurfaceInstance,
	}
	receipt, err := s.store.PresentMemoryTrayCandidate(req, now, s.gracePeriod)
	if err != nil {
		return store.MemoryPresentationReceipt{}, err
	}
	if !validMemoryPresentationStoreReceipt(receipt, req) {
		return store.MemoryPresentationReceipt{}, ErrMemoryPresentationStoreMismatch
	}
	graceExpiresAt, _ := time.Parse(time.RFC3339Nano, receipt.GraceExpiresAt)
	delay := graceExpiresAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	s.schedule(receipt, delay)
	return receipt, nil
}

func (s *MemoryPresentationService) verifyCapability(
	token string,
	now time.Time,
) (memoryPresentationCapabilityClaims, error) {
	unauthorized := func() (memoryPresentationCapabilityClaims, error) {
		return memoryPresentationCapabilityClaims{}, ErrMemoryPresentationUnauthorized
	}
	if token == "" || len(token) > memoryPresentationMaxTokenBytes || token != strings.TrimSpace(token) {
		return unauthorized()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return unauthorized()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return unauthorized()
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(signature) != parts[1] ||
		subtle.ConstantTimeCompare(signature, memoryPresentationMAC(s.signingKey, payload)) != 1 {
		return unauthorized()
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var claims memoryPresentationCapabilityClaims
	if err := decoder.Decode(&claims); err != nil {
		return unauthorized()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return unauthorized()
	}
	canonical, err := json.Marshal(claims)
	if err != nil || !bytes.Equal(canonical, payload) || !validMemoryPresentationClaims(claims, s.capabilityTTL) {
		return unauthorized()
	}
	issuedAt := time.Unix(0, claims.IssuedAtUnixNano).UTC()
	expiresAt := time.Unix(0, claims.ExpiresAtUnixNano).UTC()
	if now.Before(issuedAt) {
		return unauthorized()
	}
	if !now.Before(expiresAt) {
		return memoryPresentationCapabilityClaims{}, ErrMemoryPresentationExpired
	}
	return claims, nil
}

func (claims memoryPresentationCapabilityClaims) match(binding MemoryPresentationBinding) bool {
	return claims.Schema == memoryPresentationCapabilitySchema &&
		memoryPresentationConstantEqual(claims.BrowserSessionDigest, memoryPresentationBoundDigest("browser_session", binding.BrowserSessionID)) &&
		memoryPresentationConstantEqual(claims.CSRFDigest, memoryPresentationBoundDigest("csrf", binding.CSRFToken)) &&
		memoryPresentationConstantEqual(claims.WorkspaceBindingDigest, binding.WorkspaceBindingDigest) &&
		claims.CandidateID == binding.CandidateID && claims.CandidateVersion == binding.CandidateVersion &&
		memoryPresentationConstantEqual(claims.ContentDigest, binding.ContentDigest) &&
		claims.TrustedSurfaceKind == MemoryPresentationSurfaceHome &&
		memoryPresentationConstantEqual(claims.TrustedSurfaceInstanceHash,
			memoryPresentationBoundDigest("surface_instance", binding.TrustedSurfaceInstance))
}

func (s *MemoryPresentationService) consumeNonce(nonce string, expiresAt, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for usedNonce, expiry := range s.usedNonces {
		if !now.Before(expiry) {
			delete(s.usedNonces, usedNonce)
		}
	}
	if _, used := s.usedNonces[nonce]; used {
		return false
	}
	s.usedNonces[nonce] = expiresAt
	return true
}

func (s *MemoryPresentationService) validBrowserRequest(r *http.Request) bool {
	pathMatches := s != nil && r != nil && r.URL != nil && r.URL.EscapedPath() == s.expectedPath
	if s != nil && s.expectedPath == memoryPresentationScopedHomePath && r != nil && r.URL != nil {
		pathMatches = viewerSessionExactActionPath(r.URL.EscapedPath(), "present")
	}
	if r == nil || r.URL == nil || r.Method != http.MethodPost || r.Host != s.expectedHost ||
		!pathMatches || r.URL.RawQuery != "" || r.URL.Fragment != "" {
		return false
	}
	for _, name := range []string{
		"Authorization", "Proxy-Authorization", "X-Pulse-Key", "X-Pulse-Principal",
		"X-Pulse-Gateway-Assertion", "X-Pulse-Owner-Step-Up", "DPoP", "X-Pulse-Enrollment",
		"Mcp-Session-Id", "MCP-Protocol-Version", "Access-Control-Request-Method",
		"Access-Control-Request-Headers", "Purpose", "Sec-Purpose",
	} {
		if len(r.Header.Values(name)) != 0 {
			return false
		}
	}
	return exactMemoryPresentationHeader(r.Header, "Origin", s.expectedOrigin) &&
		exactMemoryPresentationHeader(r.Header, "Sec-Fetch-Site", "same-origin") &&
		exactMemoryPresentationHeader(r.Header, "Sec-Fetch-Mode", "cors") &&
		exactMemoryPresentationHeader(r.Header, "Sec-Fetch-Dest", "empty")
}

func validateMemoryPresentationOrigin(raw string) (string, string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\r\n\t") {
		return "", "", errors.New("memory presentation: exact loopback origin is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Port() == "" ||
		parsed.Scheme+"://"+parsed.Host != raw || !memoryPresentationLoopbackHost(parsed.Hostname()) {
		return "", "", errors.New("memory presentation: exact loopback origin is required")
	}
	return raw, parsed.Host, nil
}

func memoryPresentationLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validMemoryPresentationPath(value string) bool {
	if value == memoryPresentationScopedHomePath {
		return true
	}
	if value == "" || value != strings.TrimSpace(value) || !strings.HasPrefix(value, "/") ||
		strings.ContainsAny(value, "?#\\\r\n\t") || strings.Contains(value, "//") || strings.Contains(value, "/../") ||
		strings.HasSuffix(value, "/..") || strings.Contains(value, "/./") || strings.HasSuffix(value, "/.") {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Path == value && parsed.RawPath == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validMemoryPresentationBinding(binding MemoryPresentationBinding) bool {
	return validMemoryPresentationBrowserValue(binding.BrowserSessionID) &&
		validMemoryPresentationBrowserValue(binding.CSRFToken) &&
		validMemoryPresentationBrowserValue(binding.TrustedSurfaceInstance) &&
		memoryPresentationDigestPattern.MatchString(binding.WorkspaceBindingDigest) &&
		memoryPresentationCandidatePattern.MatchString(binding.CandidateID) &&
		binding.CandidateVersion > 0 &&
		memoryPresentationDigestPattern.MatchString(binding.ContentDigest)
}

func validMemoryPresentationBrowserValue(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == memoryPresentationBrowserValueBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == value
}

func validMemoryPresentationClaims(claims memoryPresentationCapabilityClaims, ttl time.Duration) bool {
	nonce, err := base64.RawURLEncoding.DecodeString(claims.Nonce)
	return claims.Schema == memoryPresentationCapabilitySchema && err == nil &&
		len(nonce) == memoryPresentationNonceBytes && base64.RawURLEncoding.EncodeToString(nonce) == claims.Nonce &&
		memoryPresentationDigestPattern.MatchString(claims.BrowserSessionDigest) &&
		memoryPresentationDigestPattern.MatchString(claims.CSRFDigest) &&
		memoryPresentationDigestPattern.MatchString(claims.WorkspaceBindingDigest) &&
		memoryPresentationCandidatePattern.MatchString(claims.CandidateID) && claims.CandidateVersion > 0 &&
		memoryPresentationDigestPattern.MatchString(claims.ContentDigest) &&
		claims.TrustedSurfaceKind == MemoryPresentationSurfaceHome &&
		memoryPresentationDigestPattern.MatchString(claims.TrustedSurfaceInstanceHash) &&
		claims.IssuedAtUnixNano > 0 && claims.ExpiresAtUnixNano > claims.IssuedAtUnixNano &&
		time.Duration(claims.ExpiresAtUnixNano-claims.IssuedAtUnixNano) == ttl
}

func validMemoryPresentationStoreReceipt(
	receipt store.MemoryPresentationReceipt,
	req store.MemoryPresentationRequest,
) bool {
	if receipt.Schema != store.MemoryPresentationReceiptSchema || receipt.ReceiptID == "" ||
		receipt.CandidateID != req.CandidateID || receipt.CandidateVersion != req.CandidateVersion ||
		!memoryPresentationConstantEqual(receipt.ContentDigest, req.ContentDigest) ||
		!memoryPresentationConstantEqual(receipt.BindingDigest, req.BindingDigest) ||
		receipt.TrustedSurfaceKind != req.TrustedSurfaceKind ||
		receipt.TrustedSurfaceInstance != req.TrustedSurfaceInstance {
		return false
	}
	_, presentedErr := time.Parse(time.RFC3339Nano, receipt.PresentedAt)
	_, graceErr := time.Parse(time.RFC3339Nano, receipt.GraceExpiresAt)
	return presentedErr == nil && graceErr == nil
}

func exactMemoryPresentationHeader(header http.Header, name, expected string) bool {
	values := header.Values(name)
	return len(values) == 1 && values[0] == expected
}

func memoryPresentationBoundDigest(kind, value string) string {
	digest := sha256.Sum256([]byte(memoryPresentationCapabilitySchema + "\x00" + kind + "\x00" + value))
	return hex.EncodeToString(digest[:])
}

func memoryPresentationMAC(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(memoryPresentationCapabilitySchema))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func memoryPresentationConstantEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
