package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	viewerSessionCookieName       = "pulse_home_session"
	viewerSessionRoutePrefix      = "/home/s/"
	viewerSessionCSRFFormField    = "csrf_token"
	viewerSessionFormMediaType    = "application/x-www-form-urlencoded"
	viewerSessionRandomValueBytes = memoryPresentationBrowserValueBytes
	viewerSessionHardMaximum      = 8
	viewerSessionIDAttempts       = 8
)

var (
	errViewerSessionUnauthorized         = errors.New("viewer session unauthorized")
	errViewerSessionExpired              = errors.New("viewer session expired")
	errViewerSessionClockRollback        = errors.New("viewer session clock rollback")
	errViewerSessionMethodNotAllowed     = errors.New("viewer session method not allowed")
	errViewerSessionUnsupportedMediaType = errors.New("viewer session unsupported media type")
	errViewerSessionRequestTooLarge      = errors.New("viewer session request too large")
	errViewerSessionBadRequest           = errors.New("viewer session bad request")
)

// viewerSessionConfig defines the complete local browser-session boundary.
// ExpectedOrigin is one exact loopback origin with an explicit port; it is
// never inferred from Host or another browser-controlled request field.
type viewerSessionConfig struct {
	ExpectedOrigin string
	AbsoluteTTL    time.Duration
	IdleTTL        time.Duration
	MaxSessions    int
	MaxBodyBytes   int64
	Clock          func() time.Time
	Random         io.Reader
}

// viewerSessionView contains the ephemeral values needed by the Home renderer
// and presentation capability service. Only the SHA-256 digest of ID is held
// by the manager; the raw ID exists in this request-scoped view and its cookie.
type viewerSessionView struct {
	ID                     string
	RouteScope             string
	CSRFToken              string
	TrustedSurfaceInstance string
	AbsoluteExpiresAt      time.Time
	LiveReadiness          personalLiveReadinessSnapshot
}

type viewerSessionRecord struct {
	routeScope             string
	csrfToken              string
	trustedSurfaceInstance string
	createdAt              time.Time
	lastSeenAt             time.Time
	absoluteExpiresAt      time.Time
	liveReadiness          personalLiveReadinessSnapshot
	sequence               uint64
}

type viewerSessionManager struct {
	expectedOrigin string
	expectedHost   string
	secureCookie   bool
	absoluteTTL    time.Duration
	idleTTL        time.Duration
	maxSessions    int
	maxBodyBytes   int64
	clock          func() time.Time
	random         io.Reader

	mu           sync.Mutex
	sessions     map[[sha256.Size]byte]viewerSessionRecord
	lastObserved time.Time
	nextSequence uint64
}

func newViewerSessionManager(cfg viewerSessionConfig) (*viewerSessionManager, error) {
	origin, host, err := validateMemoryPresentationOrigin(cfg.ExpectedOrigin)
	if err != nil {
		return nil, errors.New("viewer session: exact loopback origin is required")
	}
	if cfg.AbsoluteTTL < time.Second || cfg.IdleTTL < time.Second || cfg.IdleTTL > cfg.AbsoluteTTL {
		return nil, errors.New("viewer session: bounded absolute and idle TTLs are required")
	}
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = viewerSessionHardMaximum
	}
	if cfg.MaxSessions < 1 || cfg.MaxSessions > viewerSessionHardMaximum {
		return nil, errors.New("viewer session: session limit must be between 1 and 8")
	}
	if cfg.MaxBodyBytes <= 0 {
		return nil, errors.New("viewer session: max body bytes must be positive")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	random := cfg.Random
	if random == nil {
		random = rand.Reader
	}
	return &viewerSessionManager{
		expectedOrigin: origin, expectedHost: host,
		secureCookie: strings.HasPrefix(origin, "https://"), absoluteTTL: cfg.AbsoluteTTL,
		idleTTL: cfg.IdleTTL, maxSessions: cfg.MaxSessions, maxBodyBytes: cfg.MaxBodyBytes,
		clock: clock, random: random, sessions: make(map[[sha256.Size]byte]viewerSessionRecord),
	}, nil
}

func (s *viewerSessionManager) Create(liveReadiness personalLiveReadinessSnapshot) (viewerSessionView, error) {
	if s == nil {
		return viewerSessionView{}, errViewerSessionUnauthorized
	}
	if err := validatePersonalLiveReadiness(liveReadiness); err != nil {
		return viewerSessionView{}, errors.New("viewer session: invalid readiness snapshot")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now, err := s.nowLocked()
	if err != nil {
		return viewerSessionView{}, err
	}
	s.evictExpiredLocked(now)
	for len(s.sessions) >= s.maxSessions {
		s.evictOldestLocked()
	}

	var id string
	var digest [sha256.Size]byte
	for attempt := 0; attempt < viewerSessionIDAttempts; attempt++ {
		id, err = s.randomValueLocked()
		if err != nil {
			return viewerSessionView{}, err
		}
		digest, _ = viewerSessionIDDigest(id)
		if _, exists := s.sessions[digest]; !exists {
			break
		}
		id = ""
	}
	if id == "" {
		return viewerSessionView{}, errors.New("viewer session: generate unique session ID")
	}
	csrfToken, err := s.randomValueLocked()
	if err != nil {
		return viewerSessionView{}, err
	}
	trustedSurface, err := s.randomStoreIdentifierLocked()
	if err != nil {
		return viewerSessionView{}, err
	}
	routeScope, err := s.randomValueLocked()
	if err != nil {
		return viewerSessionView{}, err
	}

	s.nextSequence++
	record := viewerSessionRecord{
		routeScope: routeScope, csrfToken: csrfToken, trustedSurfaceInstance: trustedSurface,
		createdAt: now, lastSeenAt: now, absoluteExpiresAt: now.Add(s.absoluteTTL),
		liveReadiness: liveReadiness, sequence: s.nextSequence,
	}
	s.sessions[digest] = record
	return record.view(id), nil
}

// Authenticate validates an ordinary Home page request and rotates its idle
// deadline. Page navigation intentionally does not require Origin, which is
// unavailable on some direct browser navigations; mutations use the stricter
// ValidateMutation boundary below.
func (s *viewerSessionManager) Authenticate(r *http.Request) (viewerSessionView, error) {
	return s.authenticate(r, "")
}

// ValidateMutation accepts only an exact same-origin browser POST carrying a
// bounded form body and the server-issued CSRF token. It authenticates and
// touches the session only after all request-shape and CSRF checks succeed.
func (s *viewerSessionManager) ValidateMutation(w http.ResponseWriter, r *http.Request) (viewerSessionView, error) {
	if s == nil || r == nil || r.URL == nil {
		return viewerSessionView{}, errViewerSessionUnauthorized
	}
	if r.Method != http.MethodPost {
		return viewerSessionView{}, errViewerSessionMethodNotAllowed
	}
	if r.URL.RawQuery != "" || r.URL.Fragment != "" || !s.validRequestBoundary(r) ||
		!exactMemoryPresentationHeader(r.Header, "Origin", s.expectedOrigin) ||
		!exactMemoryPresentationHeader(r.Header, "Sec-Fetch-Site", "same-origin") ||
		!exactMemoryPresentationHeader(r.Header, "Sec-Fetch-Mode", "cors") ||
		!exactMemoryPresentationHeader(r.Header, "Sec-Fetch-Dest", "empty") {
		return viewerSessionView{}, errViewerSessionUnauthorized
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return viewerSessionView{}, errViewerSessionUnsupportedMediaType
	}
	mediaType, params, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != viewerSessionFormMediaType || len(params) != 0 {
		return viewerSessionView{}, errViewerSessionUnsupportedMediaType
	}
	if r.ContentLength > s.maxBodyBytes {
		return viewerSessionView{}, errViewerSessionRequestTooLarge
	}
	if r.Body == nil {
		return viewerSessionView{}, errViewerSessionBadRequest
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return viewerSessionView{}, errViewerSessionRequestTooLarge
		}
		return viewerSessionView{}, errViewerSessionBadRequest
	}
	csrfValues := r.PostForm[viewerSessionCSRFFormField]
	if len(csrfValues) != 1 || !validViewerSessionRandomValue(csrfValues[0]) {
		return viewerSessionView{}, errViewerSessionUnauthorized
	}
	return s.authenticate(r, csrfValues[0])
}

func (s *viewerSessionManager) Revoke(session viewerSessionView) bool {
	if s == nil || !validViewerSessionRandomValue(session.RouteScope) || !validViewerSessionRandomValue(session.CSRFToken) ||
		!validViewerSessionRandomValue(session.TrustedSurfaceInstance) {
		return false
	}
	digest, ok := viewerSessionIDDigest(session.ID)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.sessions[digest]
	if !exists || !memoryPresentationConstantEqual(record.routeScope, session.RouteScope) ||
		!memoryPresentationConstantEqual(record.csrfToken, session.CSRFToken) ||
		!memoryPresentationConstantEqual(record.trustedSurfaceInstance, session.TrustedSurfaceInstance) {
		return false
	}
	delete(s.sessions, digest)
	return true
}

func (s *viewerSessionManager) Cookie(session viewerSessionView) *http.Cookie {
	maxAge := int((s.absoluteTTL + time.Second - 1) / time.Second)
	return &http.Cookie{
		Name: viewerSessionCookieName, Value: session.ID, Path: viewerSessionRoutePath(session.RouteScope),
		Expires: session.AbsoluteExpiresAt, MaxAge: maxAge, HttpOnly: true,
		Secure: s.secureCookie, SameSite: http.SameSiteStrictMode,
	}
}

func (s *viewerSessionManager) ClearCookie(routeScope string) *http.Cookie {
	return &http.Cookie{
		Name: viewerSessionCookieName, Value: "", Path: viewerSessionRoutePath(routeScope),
		Expires: time.Unix(1, 0).UTC(), MaxAge: -1, HttpOnly: true,
		Secure: s.secureCookie, SameSite: http.SameSiteStrictMode,
	}
}

func (s *viewerSessionManager) HardenHeaders(header http.Header) {
	setPrivilegedUIHeaders(header)
}

func (s *viewerSessionManager) authenticate(r *http.Request, requiredCSRF string) (viewerSessionView, error) {
	if s == nil || !s.validRequestBoundary(r) {
		return viewerSessionView{}, errViewerSessionUnauthorized
	}
	rawID, err := readViewerSessionCookie(r)
	if err != nil {
		return viewerSessionView{}, errViewerSessionUnauthorized
	}
	digest, ok := viewerSessionIDDigest(rawID)
	if !ok {
		return viewerSessionView{}, errViewerSessionUnauthorized
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now, err := s.nowLocked()
	if err != nil {
		return viewerSessionView{}, err
	}
	record, exists := s.sessions[digest]
	if !exists {
		return viewerSessionView{}, errViewerSessionUnauthorized
	}
	routeScope, ok := viewerSessionRouteFromPath(r.URL.EscapedPath())
	if !ok || !memoryPresentationConstantEqual(record.routeScope, routeScope) {
		return viewerSessionView{}, errViewerSessionUnauthorized
	}
	if !now.Before(record.absoluteExpiresAt) || !now.Before(record.lastSeenAt.Add(s.idleTTL)) {
		delete(s.sessions, digest)
		return viewerSessionView{}, errViewerSessionExpired
	}
	if requiredCSRF != "" && !memoryPresentationConstantEqual(record.csrfToken, requiredCSRF) {
		return viewerSessionView{}, errViewerSessionUnauthorized
	}
	record.lastSeenAt = now
	s.sessions[digest] = record
	return record.view(rawID), nil
}

func (s *viewerSessionManager) validRequestBoundary(r *http.Request) bool {
	if r == nil || r.URL == nil || r.Host != s.expectedHost || !isLoopbackRequest(r) || len(r.Header.Values("Host")) != 0 {
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
	return true
}

func (s *viewerSessionManager) nowLocked() (time.Time, error) {
	now := s.clock().UTC()
	if now.IsZero() {
		return time.Time{}, errViewerSessionClockRollback
	}
	if !s.lastObserved.IsZero() && now.Before(s.lastObserved) {
		return time.Time{}, errViewerSessionClockRollback
	}
	s.lastObserved = now
	return now, nil
}

func (s *viewerSessionManager) randomValueLocked() (string, error) {
	raw := make([]byte, viewerSessionRandomValueBytes)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return "", errors.New("viewer session: generate random value")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *viewerSessionManager) randomStoreIdentifierLocked() (string, error) {
	for attempt := 0; attempt < viewerSessionIDAttempts; attempt++ {
		value, err := s.randomValueLocked()
		if err != nil {
			return "", err
		}
		first := value[0]
		if !((first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
			continue
		}
		lower := strings.ToLower(value)
		compatible := true
		for _, prefix := range []string{"sk-", "ghp_", "gho_", "ghu_", "ghs_", "github_pat_", "xoxb-", "xoxp-", "xapp-", "akia", "ya29."} {
			if strings.HasPrefix(lower, prefix) {
				compatible = false
				break
			}
		}
		if compatible {
			return value, nil
		}
	}
	return "", errors.New("viewer session: generate store-compatible trusted surface")
}

func (s *viewerSessionManager) evictExpiredLocked(now time.Time) {
	for digest, record := range s.sessions {
		if !now.Before(record.absoluteExpiresAt) || !now.Before(record.lastSeenAt.Add(s.idleTTL)) {
			delete(s.sessions, digest)
		}
	}
}

func (s *viewerSessionManager) evictOldestLocked() {
	var oldestDigest [sha256.Size]byte
	var oldest viewerSessionRecord
	found := false
	for digest, record := range s.sessions {
		if !found || record.lastSeenAt.Before(oldest.lastSeenAt) ||
			(record.lastSeenAt.Equal(oldest.lastSeenAt) && record.createdAt.Before(oldest.createdAt)) ||
			(record.lastSeenAt.Equal(oldest.lastSeenAt) && record.createdAt.Equal(oldest.createdAt) && record.sequence < oldest.sequence) {
			oldestDigest, oldest, found = digest, record, true
		}
	}
	if found {
		delete(s.sessions, oldestDigest)
	}
}

func (record viewerSessionRecord) view(id string) viewerSessionView {
	return viewerSessionView{
		ID: id, RouteScope: record.routeScope, CSRFToken: record.csrfToken, TrustedSurfaceInstance: record.trustedSurfaceInstance,
		AbsoluteExpiresAt: record.absoluteExpiresAt, LiveReadiness: record.liveReadiness,
	}
}

func viewerSessionRoutePath(routeScope string) string {
	if !validViewerSessionRandomValue(routeScope) {
		return viewerSessionRoutePrefix
	}
	return viewerSessionRoutePrefix + routeScope + "/"
}

func viewerSessionRouteFromPath(path string) (string, bool) {
	if !strings.HasPrefix(path, viewerSessionRoutePrefix) {
		return "", false
	}
	remainder := strings.TrimPrefix(path, viewerSessionRoutePrefix)
	separator := strings.IndexByte(remainder, '/')
	if separator < 0 {
		return "", false
	}
	routeScope := remainder[:separator]
	return routeScope, validViewerSessionRandomValue(routeScope)
}

func viewerSessionExactActionPath(path, action string) bool {
	if action == "" || strings.ContainsAny(action, "/?#\\\r\n\t") {
		return false
	}
	routeScope, ok := viewerSessionRouteFromPath(path)
	return ok && path == viewerSessionRoutePath(routeScope)+action
}

func readViewerSessionCookie(r *http.Request) (string, error) {
	if r == nil {
		return "", errViewerSessionUnauthorized
	}
	headers := r.Header.Values("Cookie")
	if len(headers) == 0 {
		return "", errViewerSessionUnauthorized
	}
	value := ""
	count := 0
	for _, header := range headers {
		cookies, err := http.ParseCookie(header)
		if err != nil {
			return "", errViewerSessionUnauthorized
		}
		for _, cookie := range cookies {
			if cookie.Name == viewerSessionCookieName {
				count++
				value = cookie.Value
			}
		}
	}
	if count != 1 {
		return "", errViewerSessionUnauthorized
	}
	return value, nil
}

func viewerSessionIDDigest(id string) ([sha256.Size]byte, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(decoded) != viewerSessionRandomValueBytes || base64.RawURLEncoding.EncodeToString(decoded) != id {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256(decoded), true
}

func validViewerSessionRandomValue(value string) bool {
	return validMemoryPresentationBrowserValue(value)
}
