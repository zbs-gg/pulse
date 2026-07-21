package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"
)

const (
	SecurityEventRoutePath             = "/team/v1/security-events"
	SecurityEventMaxBodyBytes          = int64(4 * 1024)
	SecurityEventMaxCount              = uint32(1000)
	SecurityEventMaxRateWindow         = 10 * time.Minute
	SecurityEventMaxRateCountPerWindow = uint64(4096)
	SecurityEventMaxTrackedDedupeKeys  = 4096
	securityEventDefaultCountPerWindow = uint64(256)
	securityEventDefaultWindow         = time.Minute
)

type SecurityEventType string

const (
	SecurityEventTypeAuthenticationDenied     SecurityEventType = "authentication_denied"
	SecurityEventTypeAuthorizationDenied      SecurityEventType = "authorization_denied"
	SecurityEventTypePrincipalAssertionDenied SecurityEventType = "principal_assertion_denied"
	SecurityEventTypeOperationDenied          SecurityEventType = "operation_denied"
	SecurityEventTypeAuditDegraded            SecurityEventType = "audit_degraded"
)

type SecurityEventReason string

const (
	SecurityEventReasonMissingCredential        SecurityEventReason = "missing_credential"
	SecurityEventReasonMalformedCredential      SecurityEventReason = "malformed_credential"
	SecurityEventReasonInvalidCredential        SecurityEventReason = "invalid_credential"
	SecurityEventReasonExpiredCredential        SecurityEventReason = "expired_credential"
	SecurityEventReasonCredentialNotYetValid    SecurityEventReason = "credential_not_yet_valid"
	SecurityEventReasonIssuerMismatch           SecurityEventReason = "issuer_mismatch"
	SecurityEventReasonAudienceMismatch         SecurityEventReason = "audience_mismatch"
	SecurityEventReasonIncompleteClaims         SecurityEventReason = "incomplete_claims"
	SecurityEventReasonUnknownSigningKey        SecurityEventReason = "unknown_signing_key"
	SecurityEventReasonInsufficientScope        SecurityEventReason = "insufficient_scope"
	SecurityEventReasonPrincipalUnmapped        SecurityEventReason = "principal_unmapped"
	SecurityEventReasonPrincipalRevoked         SecurityEventReason = "principal_revoked"
	SecurityEventReasonPolicyDenied             SecurityEventReason = "policy_denied"
	SecurityEventReasonAssertionInvalid         SecurityEventReason = "assertion_invalid"
	SecurityEventReasonAssertionExpired         SecurityEventReason = "assertion_expired"
	SecurityEventReasonAssertionReplayed        SecurityEventReason = "assertion_replayed"
	SecurityEventReasonAssertionBindingMismatch SecurityEventReason = "assertion_binding_mismatch"
	SecurityEventReasonStaleGeneration          SecurityEventReason = "stale_generation"
	SecurityEventReasonInvalidContract          SecurityEventReason = "invalid_contract"
	SecurityEventReasonIdempotencyConflict      SecurityEventReason = "idempotency_conflict"
	SecurityEventReasonOperationInProgress      SecurityEventReason = "operation_in_progress"
	SecurityEventReasonStoreUnavailable         SecurityEventReason = "store_unavailable"
	SecurityEventReasonRateLimited              SecurityEventReason = "rate_limited"
	SecurityEventReasonInternalFailure          SecurityEventReason = "internal_failure"
)

type SecurityEventMethodClass string

const (
	SecurityEventMethodRead   SecurityEventMethodClass = "read"
	SecurityEventMethodWrite  SecurityEventMethodClass = "write"
	SecurityEventMethodDelete SecurityEventMethodClass = "delete"
	SecurityEventMethodOther  SecurityEventMethodClass = "other"
)

type SecurityEventPathClass string

const (
	SecurityEventPathMCP           SecurityEventPathClass = "mcp"
	SecurityEventPathOAuthMetadata SecurityEventPathClass = "oauth_metadata"
	SecurityEventPathPrincipal     SecurityEventPathClass = "principal"
	SecurityEventPathTeamAPI       SecurityEventPathClass = "team_api"
	SecurityEventPathReadiness     SecurityEventPathClass = "readiness"
	SecurityEventPathUnknown       SecurityEventPathClass = "unknown"
)

// SecurityEvent is deliberately content-free. Each field is either a fixed
// classification, an opaque request correlation ID, or an aggregate count.
type SecurityEvent struct {
	EventType   SecurityEventType        `json:"event_type"`
	ReasonCode  SecurityEventReason      `json:"reason_code"`
	MethodClass SecurityEventMethodClass `json:"method_class"`
	PathClass   SecurityEventPathClass   `json:"path_class"`
	RequestID   string                   `json:"request_id"`
	Count       uint32                   `json:"count"`
}

type SecurityEventStorage interface {
	AppendSecurityEvent(context.Context, SecurityEvent) error
}

type SecurityEventStorageFunc func(context.Context, SecurityEvent) error

func (f SecurityEventStorageFunc) AppendSecurityEvent(ctx context.Context, event SecurityEvent) error {
	return f(ctx, event)
}

type SecurityEventGatewayVerifier interface {
	VerifyGatewayEvent(context.Context, string, string, string, string, []byte) error
}

type SecurityEventGatewayVerifierFunc func(context.Context, string, string, string, string, []byte) error

func (f SecurityEventGatewayVerifierFunc) VerifyGatewayEvent(
	ctx context.Context,
	assertion, requestID, method, path string,
	body []byte,
) error {
	return f(ctx, assertion, requestID, method, path, body)
}

type SecurityEventHandlerOptions struct {
	Now               func() time.Time
	Window            time.Duration
	MaxCountPerWindow uint64
	MaxDedupeEntries  int
	GatewayVerifier   SecurityEventGatewayVerifier
}

type securityEventHandler struct {
	storage         SecurityEventStorage
	gatewayVerifier SecurityEventGatewayVerifier
	gate            *securityEventGate
}

func NewSecurityEventHandler(storage SecurityEventStorage, options SecurityEventHandlerOptions) (http.Handler, error) {
	if storage == nil {
		return nil, errors.New("security event storage is required")
	}
	if options.GatewayVerifier == nil {
		return nil, errors.New("security event gateway verifier is required")
	}
	if options.Window < 0 {
		return nil, errors.New("security event window must not be negative")
	}
	if options.Window == 0 {
		options.Window = securityEventDefaultWindow
	}
	if options.Window > SecurityEventMaxRateWindow {
		return nil, errors.New("security event rate window exceeds hard bound")
	}
	if options.MaxCountPerWindow == 0 {
		options.MaxCountPerWindow = securityEventDefaultCountPerWindow
	}
	if options.MaxCountPerWindow > SecurityEventMaxRateCountPerWindow {
		return nil, errors.New("security event rate limit exceeds hard bound")
	}
	if options.MaxDedupeEntries == 0 {
		options.MaxDedupeEntries = int(options.MaxCountPerWindow)
	}
	if options.MaxDedupeEntries < 0 || options.MaxDedupeEntries > SecurityEventMaxTrackedDedupeKeys {
		return nil, errors.New("security event dedupe limit exceeds hard bound")
	}
	if uint64(options.MaxDedupeEntries) < options.MaxCountPerWindow {
		return nil, errors.New("security event dedupe limit must cover rate window")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &securityEventHandler{
		storage: storage, gatewayVerifier: options.GatewayVerifier,
		gate: &securityEventGate{
			now:      options.Now,
			window:   options.Window,
			maxCount: options.MaxCountPerWindow,
			seen:     make(map[[sha256.Size]byte]*securityEventEntry, options.MaxDedupeEntries),
		},
	}, nil
}

func (h *securityEventHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, SecurityEventMaxBodyBytes+1))
	if err != nil || len(raw) > int(SecurityEventMaxBodyBytes) {
		http.Error(w, "security event too large", http.StatusRequestEntityTooLarge)
		return
	}
	var event SecurityEvent
	if err := decodeStrictJSON(raw, &event); err != nil {
		http.Error(w, "invalid security event", http.StatusBadRequest)
		return
	}
	if !validSecurityEvent(event) {
		http.Error(w, "invalid security event", http.StatusBadRequest)
		return
	}
	assertions := r.Header.Values("X-Pulse-Gateway-Assertion")
	requestIDs := r.Header.Values("X-Pulse-Request-ID")
	if len(assertions) != 1 || len(requestIDs) != 1 || assertions[0] == "" || requestIDs[0] != event.RequestID {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := h.gatewayVerifier.VerifyGatewayEvent(
		r.Context(), assertions[0], requestIDs[0], r.Method, r.URL.EscapedPath(), raw,
	); err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrPrincipalStoreUnavailable) {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, "security event denied", status)
		return
	}
	for {
		admission, reservation := h.gate.reserve(event)
		switch admission {
		case securityEventDuplicate:
			select {
			case <-reservation.entry.done:
				if reservation.entry.persisted {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				// The in-flight writer failed and released its reservation.
				// Try to become the writer instead of acknowledging a lost event.
				continue
			case <-r.Context().Done():
				http.Error(w, "security event unavailable", http.StatusServiceUnavailable)
				return
			}
		case securityEventLimited:
			http.Error(w, "security event rate limited", http.StatusTooManyRequests)
			return
		}
		if err := h.storage.AppendSecurityEvent(r.Context(), event); err != nil {
			h.gate.finish(reservation, false)
			http.Error(w, "security event unavailable", http.StatusServiceUnavailable)
			return
		}
		h.gate.finish(reservation, true)
		w.WriteHeader(http.StatusNoContent)
		return
	}
}

type SecurityEventRouteRegistrar interface {
	Method(method, pattern string, handler http.Handler)
}

// RegisterSecurityEventRoute registers only the route. The caller supplies the
// already composed handler, so IPC authentication and loopback restrictions
// remain owned by the server wiring rather than being duplicated here.
func RegisterSecurityEventRoute(registrar SecurityEventRouteRegistrar, handler http.Handler) {
	registrar.Method(http.MethodPost, SecurityEventRoutePath, handler)
}

type securityEventAdmission uint8

const (
	securityEventAccepted securityEventAdmission = iota
	securityEventDuplicate
	securityEventLimited
)

type securityEventReservation struct {
	key        [sha256.Size]byte
	generation uint64
	count      uint64
	entry      *securityEventEntry
}

type securityEventEntry struct {
	done      chan struct{}
	persisted bool
}

type securityEventGate struct {
	mu          sync.Mutex
	now         func() time.Time
	window      time.Duration
	maxCount    uint64
	initialized bool
	windowStart time.Time
	generation  uint64
	used        uint64
	seen        map[[sha256.Size]byte]*securityEventEntry
}

func (g *securityEventGate) reserve(event SecurityEvent) (securityEventAdmission, securityEventReservation) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()
	if !g.initialized || now.Before(g.windowStart) || now.Sub(g.windowStart) >= g.window {
		g.initialized = true
		g.windowStart = now
		g.generation++
		g.used = 0
		clear(g.seen)
	}
	key := securityEventFingerprint(event)
	if entry, duplicate := g.seen[key]; duplicate {
		return securityEventDuplicate, securityEventReservation{entry: entry}
	}
	count := uint64(event.Count)
	if count > g.maxCount-g.used {
		return securityEventLimited, securityEventReservation{}
	}
	entry := &securityEventEntry{done: make(chan struct{})}
	g.seen[key] = entry
	g.used += count
	return securityEventAccepted, securityEventReservation{
		key: key, generation: g.generation, count: count, entry: entry,
	}
}

func (g *securityEventGate) finish(reservation securityEventReservation, persisted bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	reservation.entry.persisted = persisted
	close(reservation.entry.done)
	if reservation.generation != g.generation {
		return
	}
	entry, reserved := g.seen[reservation.key]
	if !reserved || entry != reservation.entry {
		return
	}
	if !persisted {
		delete(g.seen, reservation.key)
		g.used -= reservation.count
	}
}

func securityEventFingerprint(event SecurityEvent) [sha256.Size]byte {
	// Count is intentionally absent: changing an aggregate count cannot turn a
	// replay of the same correlated classification into a new event.
	value := string(event.EventType) + "\x00" + string(event.ReasonCode) + "\x00" +
		string(event.MethodClass) + "\x00" + string(event.PathClass) + "\x00" + event.RequestID
	return sha256.Sum256([]byte(value))
}

func validSecurityEvent(event SecurityEvent) bool {
	if !validSecurityEventReason(event.EventType, event.ReasonCode) ||
		!validSecurityEventMethod(event.MethodClass) ||
		!validSecurityEventPath(event.PathClass) ||
		!validSecurityEventRequestID(event.RequestID) {
		return false
	}
	return event.Count > 0 && event.Count <= SecurityEventMaxCount
}

func validSecurityEventReason(eventType SecurityEventType, reason SecurityEventReason) bool {
	switch eventType {
	case SecurityEventTypeAuthenticationDenied:
		switch reason {
		case SecurityEventReasonMissingCredential,
			SecurityEventReasonMalformedCredential,
			SecurityEventReasonInvalidCredential,
			SecurityEventReasonExpiredCredential,
			SecurityEventReasonCredentialNotYetValid,
			SecurityEventReasonIssuerMismatch,
			SecurityEventReasonAudienceMismatch,
			SecurityEventReasonIncompleteClaims,
			SecurityEventReasonUnknownSigningKey,
			SecurityEventReasonPrincipalUnmapped:
			return true
		}
	case SecurityEventTypeAuthorizationDenied:
		switch reason {
		case SecurityEventReasonInsufficientScope,
			SecurityEventReasonPrincipalUnmapped,
			SecurityEventReasonPrincipalRevoked,
			SecurityEventReasonPolicyDenied,
			SecurityEventReasonStaleGeneration:
			return true
		}
	case SecurityEventTypePrincipalAssertionDenied:
		switch reason {
		case SecurityEventReasonAssertionInvalid,
			SecurityEventReasonAssertionExpired,
			SecurityEventReasonAssertionReplayed,
			SecurityEventReasonAssertionBindingMismatch,
			SecurityEventReasonUnknownSigningKey,
			SecurityEventReasonStaleGeneration:
			return true
		}
	case SecurityEventTypeOperationDenied:
		switch reason {
		case SecurityEventReasonInvalidContract,
			SecurityEventReasonIdempotencyConflict,
			SecurityEventReasonOperationInProgress:
			return true
		}
	case SecurityEventTypeAuditDegraded:
		switch reason {
		case SecurityEventReasonStoreUnavailable,
			SecurityEventReasonRateLimited,
			SecurityEventReasonInternalFailure:
			return true
		}
	}
	return false
}

func validSecurityEventMethod(method SecurityEventMethodClass) bool {
	switch method {
	case SecurityEventMethodRead, SecurityEventMethodWrite,
		SecurityEventMethodDelete, SecurityEventMethodOther:
		return true
	default:
		return false
	}
}

func validSecurityEventPath(path SecurityEventPathClass) bool {
	switch path {
	case SecurityEventPathMCP, SecurityEventPathOAuthMetadata,
		SecurityEventPathPrincipal, SecurityEventPathTeamAPI,
		SecurityEventPathReadiness, SecurityEventPathUnknown:
		return true
	default:
		return false
	}
}

func validSecurityEventRequestID(requestID string) bool {
	if len(requestID) < 8 || len(requestID) > 64 {
		return false
	}
	for i := range len(requestID) {
		char := requestID[i]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
