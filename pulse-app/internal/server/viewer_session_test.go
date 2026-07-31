package server

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

const testViewerSessionOrigin = "http://127.0.0.1:43129"

func testViewerSessionReadiness() personalLiveReadinessSnapshot {
	return personalLiveReadinessForReason("personal_live_ready", "2026-07-16T08:00:00Z")
}

func TestNewViewerSessionManagerRequiresExactLoopbackOriginAndBounds(t *testing.T) {
	t.Parallel()

	valid := viewerSessionConfig{
		ExpectedOrigin: testViewerSessionOrigin,
		AbsoluteTTL:    2 * time.Hour,
		IdleTTL:        15 * time.Minute,
		MaxBodyBytes:   1024,
	}
	if _, err := newViewerSessionManager(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	invalidOrigins := []string{
		"",
		" http://127.0.0.1:43129",
		"http://127.0.0.1",
		"http://0.0.0.0:43129",
		"http://192.0.2.10:43129",
		"http://user@127.0.0.1:43129",
		"http://127.0.0.1:43129/home",
		"http://127.0.0.1:43129?next=/home",
		"http://127.0.0.1:43129#home",
		"ws://127.0.0.1:43129",
	}
	for _, origin := range invalidOrigins {
		origin := origin
		t.Run(origin, func(t *testing.T) {
			cfg := valid
			cfg.ExpectedOrigin = origin
			if _, err := newViewerSessionManager(cfg); err == nil {
				t.Fatalf("origin %q unexpectedly accepted", origin)
			}
		})
	}

	for name, mutate := range map[string]func(*viewerSessionConfig){
		"zero absolute ttl": func(cfg *viewerSessionConfig) { cfg.AbsoluteTTL = 0 },
		"zero idle ttl":     func(cfg *viewerSessionConfig) { cfg.IdleTTL = 0 },
		"idle over absolute": func(cfg *viewerSessionConfig) {
			cfg.IdleTTL = cfg.AbsoluteTTL + time.Second
		},
		"negative sessions": func(cfg *viewerSessionConfig) { cfg.MaxSessions = -1 },
		"over session cap":  func(cfg *viewerSessionConfig) { cfg.MaxSessions = 9 },
		"zero body limit":   func(cfg *viewerSessionConfig) { cfg.MaxBodyBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			mutate(&cfg)
			if _, err := newViewerSessionManager(cfg); err == nil {
				t.Fatal("invalid config unexpectedly accepted")
			}
		})
	}

	httpsConfig := valid
	httpsConfig.ExpectedOrigin = "https://localhost:43129"
	if _, err := newViewerSessionManager(httpsConfig); err != nil {
		t.Fatalf("exact HTTPS localhost origin rejected: %v", err)
	}
}

func TestViewerSessionCreateUsesOpaqueValuesAndStoresOnlySessionDigest(t *testing.T) {
	t.Parallel()

	manager, _ := newTestViewerSessionManager(t, 8)
	session, err := manager.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for name, value := range map[string]string{
		"session id":      session.ID,
		"csrf token":      session.CSRFToken,
		"trusted surface": session.TrustedSurfaceInstance,
	} {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(decoded) != viewerSessionRandomValueBytes || base64.RawURLEncoding.EncodeToString(decoded) != value {
			t.Fatalf("%s is not a canonical %d-byte URL-safe value: %q", name, viewerSessionRandomValueBytes, value)
		}
	}
	if session.ID == session.CSRFToken || session.ID == session.TrustedSurfaceInstance || session.CSRFToken == session.TrustedSurfaceInstance {
		t.Fatal("session, CSRF and trusted-surface values must be independent")
	}

	rawID, _ := base64.RawURLEncoding.DecodeString(session.ID)
	digest := sha256.Sum256(rawID)
	if _, ok := manager.sessions[digest]; !ok {
		t.Fatalf("session was not indexed by SHA-256 digest")
	}
	if strings.Contains(fmt.Sprintf("%#v", manager.sessions), session.ID) {
		t.Fatal("manager retained the raw session ID")
	}
}

func TestViewerSessionRejectsStoreIncompatibleTrustedSurfaceSample(t *testing.T) {
	raw := make([]byte, viewerSessionRandomValueBytes*5)
	for chunk, value := range []byte{1, 2, 0xfb, 3, 4} {
		for index := 0; index < viewerSessionRandomValueBytes; index++ {
			raw[chunk*viewerSessionRandomValueBytes+index] = value
		}
	}
	manager, err := newViewerSessionManager(viewerSessionConfig{
		ExpectedOrigin: testViewerSessionOrigin, AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute,
		MaxBodyBytes: 1024, Clock: func() time.Time { return time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC) },
		Random: strings.NewReader(string(raw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(session.TrustedSurfaceInstance, "-") || strings.HasPrefix(session.TrustedSurfaceInstance, "_") {
		t.Fatalf("store-incompatible trusted surface escaped rejection sampling: %q", session.TrustedSurfaceInstance)
	}
}

func TestViewerSessionCookieUsesIndependentCanonicalRouteScope(t *testing.T) {
	manager, _ := newTestViewerSessionManager(t, 8)
	first, err := manager.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	routePattern := regexp.MustCompile(`^/home/s/([A-Za-z0-9_-]{43})/$`)
	firstMatch := routePattern.FindStringSubmatch(manager.Cookie(first).Path)
	secondMatch := routePattern.FindStringSubmatch(manager.Cookie(second).Path)
	if len(firstMatch) != 2 || len(secondMatch) != 2 {
		t.Fatalf("non-canonical route paths first=%q second=%q", manager.Cookie(first).Path, manager.Cookie(second).Path)
	}
	for _, route := range []string{firstMatch[1], secondMatch[1]} {
		decoded, err := base64.RawURLEncoding.DecodeString(route)
		if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != route {
			t.Fatalf("route scope is not canonical 32-byte base64url: %q", route)
		}
	}
	if firstMatch[1] == secondMatch[1] {
		t.Fatal("independent sessions reused one route scope")
	}
	if first.RouteScope == first.ID || first.RouteScope == first.CSRFToken ||
		first.RouteScope == first.TrustedSurfaceInstance {
		t.Fatal("route scope reused a browser authority value")
	}
}

func TestViewerSessionCookieIsNotEligibleForAmbientHomePath(t *testing.T) {
	manager, _ := newTestViewerSessionManager(t, 8)
	session, err := manager.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(manager.expectedOrigin + manager.Cookie(session).Path)
	jar.SetCookies(target, []*http.Cookie{manager.Cookie(session)})
	attacker, _ := url.Parse(manager.expectedOrigin + "/home")
	for _, cookie := range jar.Cookies(attacker) {
		if cookie.Name == viewerSessionCookieName {
			t.Fatalf("Home session cookie was eligible for ambient /home: %#v", cookie)
		}
	}
}

func TestViewerSessionRejectsStolenCookieOnAnotherSessionRoute(t *testing.T) {
	manager, _ := newTestViewerSessionManager(t, 8)
	first, _ := manager.Create(testViewerSessionReadiness())
	second, _ := manager.Create(testViewerSessionReadiness())
	firstPath := manager.Cookie(first).Path
	secondPath := manager.Cookie(second).Path
	if firstPath == secondPath {
		t.Fatalf("test requires independent session routes, both were %q", firstPath)
	}
	request := httptest.NewRequest(http.MethodGet, manager.expectedOrigin+secondPath, nil)
	request.Host = manager.expectedHost
	request.RemoteAddr = "127.0.0.1:54321"
	request.AddCookie(manager.Cookie(first))
	if _, err := manager.Authenticate(request); !errors.Is(err, errViewerSessionUnauthorized) {
		t.Fatalf("stolen cookie on another route error=%v, want unauthorized", err)
	}
}

func TestViewerSessionAuthenticateRequiresExactHostSingleCanonicalCookieAndNoAmbientAuthority(t *testing.T) {
	t.Parallel()

	manager, _ := newTestViewerSessionManager(t, 8)
	session, err := manager.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	valid := viewerSessionPageRequest(manager, session)
	authenticated, err := manager.Authenticate(valid)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if authenticated.ID != session.ID || authenticated.CSRFToken != session.CSRFToken ||
		authenticated.RouteScope != session.RouteScope || authenticated.TrustedSurfaceInstance != session.TrustedSurfaceInstance {
		t.Fatalf("authenticated session changed binding: %#v", authenticated)
	}

	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"missing cookie", func(r *http.Request) { r.Header.Del("Cookie") }},
		{"duplicate cookie", func(r *http.Request) { r.AddCookie(manager.Cookie(session)) }},
		{"malformed cookie header", func(r *http.Request) { r.Header.Set("Cookie", `bad="unterminated`) }},
		{"malformed session id", func(r *http.Request) { r.Header.Set("Cookie", viewerSessionCookieName+"=not-base64!") }},
		{"wrong host", func(r *http.Request) { r.Host = "localhost:43129" }},
		{"host case mismatch", func(r *http.Request) { r.Host = "127.0.0.1:43129 " }},
		{"dns rebinding", func(r *http.Request) { r.Host = "evil.example:43129" }},
		{"non-loopback peer", func(r *http.Request) { r.RemoteAddr = "192.0.2.44:5555" }},
		{"authorization", func(r *http.Request) { r.Header.Set("Authorization", "Bearer local") }},
		{"proxy authorization", func(r *http.Request) { r.Header.Set("Proxy-Authorization", "Basic local") }},
		{"ipc key", func(r *http.Request) { r.Header.Set("X-Pulse-Key", "daemon-secret") }},
		{"mcp session", func(r *http.Request) { r.Header.Set("Mcp-Session-Id", "mcp-session") }},
		{"mcp version", func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "2025-06-18") }},
		{"dpop", func(r *http.Request) { r.Header.Set("DPoP", "proof") }},
		{"owner step-up", func(r *http.Request) { r.Header.Set("X-Pulse-Owner-Step-Up", "assertion") }},
		{"gateway assertion", func(r *http.Request) { r.Header.Set("X-Pulse-Gateway-Assertion", "assertion") }},
		{"principal", func(r *http.Request) { r.Header.Set("X-Pulse-Principal", "principal") }},
		{"enrollment", func(r *http.Request) { r.Header.Set("X-Pulse-Enrollment", "enrollment") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := viewerSessionPageRequest(manager, session)
			tc.mutate(request)
			if _, err := manager.Authenticate(request); !errors.Is(err, errViewerSessionUnauthorized) {
				t.Fatalf("Authenticate error = %v, want unauthorized", err)
			}
		})
	}
}

func TestViewerSessionEnforcesIdleAbsoluteExpiryAndClockRollback(t *testing.T) {
	t.Run("idle expiry", func(t *testing.T) {
		manager, clock := newTestViewerSessionManager(t, 8)
		session, _ := manager.Create(testViewerSessionReadiness())
		clock.Advance(15 * time.Minute)
		if _, err := manager.Authenticate(viewerSessionPageRequest(manager, session)); !errors.Is(err, errViewerSessionExpired) {
			t.Fatalf("Authenticate error = %v, want idle expiry", err)
		}
	})

	t.Run("absolute expiry despite activity", func(t *testing.T) {
		manager, clock := newTestViewerSessionManager(t, 8)
		session, _ := manager.Create(testViewerSessionReadiness())
		for _, advance := range []time.Duration{10 * time.Minute, 10 * time.Minute, 10 * time.Minute, 10 * time.Minute, 10 * time.Minute} {
			clock.Advance(advance)
			if _, err := manager.Authenticate(viewerSessionPageRequest(manager, session)); err != nil {
				t.Fatalf("active session rejected early: %v", err)
			}
		}
		clock.Advance(10 * time.Minute)
		if _, err := manager.Authenticate(viewerSessionPageRequest(manager, session)); !errors.Is(err, errViewerSessionExpired) {
			t.Fatalf("Authenticate error = %v, want absolute expiry", err)
		}
	})

	t.Run("clock rollback", func(t *testing.T) {
		manager, clock := newTestViewerSessionManager(t, 8)
		session, _ := manager.Create(testViewerSessionReadiness())
		clock.Advance(time.Minute)
		if _, err := manager.Authenticate(viewerSessionPageRequest(manager, session)); err != nil {
			t.Fatalf("Authenticate before rollback: %v", err)
		}
		clock.Advance(-time.Second)
		if _, err := manager.Authenticate(viewerSessionPageRequest(manager, session)); !errors.Is(err, errViewerSessionClockRollback) {
			t.Fatalf("Authenticate error = %v, want clock rollback", err)
		}
	})
}

func TestViewerSessionEvictsExpiredThenLeastRecentlyUsedDeterministically(t *testing.T) {
	t.Parallel()

	manager, clock := newTestViewerSessionManager(t, 2)
	first, _ := manager.Create(testViewerSessionReadiness())
	clock.Advance(time.Minute)
	second, _ := manager.Create(testViewerSessionReadiness())
	clock.Advance(time.Minute)
	if _, err := manager.Authenticate(viewerSessionPageRequest(manager, first)); err != nil {
		t.Fatalf("refresh first session: %v", err)
	}
	clock.Advance(time.Minute)
	third, _ := manager.Create(testViewerSessionReadiness())

	if _, err := manager.Authenticate(viewerSessionPageRequest(manager, second)); !errors.Is(err, errViewerSessionUnauthorized) {
		t.Fatalf("least-recent session error = %v, want eviction", err)
	}
	for name, session := range map[string]viewerSessionView{"refreshed": first, "newest": third} {
		if _, err := manager.Authenticate(viewerSessionPageRequest(manager, session)); err != nil {
			t.Fatalf("%s session was evicted: %v", name, err)
		}
	}

	expiring, expiryClock := newTestViewerSessionManager(t, 2)
	expired, _ := expiring.Create(testViewerSessionReadiness())
	expiryClock.Advance(15 * time.Minute)
	replacement, _ := expiring.Create(testViewerSessionReadiness())
	if len(expiring.sessions) != 1 {
		t.Fatalf("expired sessions retained: got %d active records", len(expiring.sessions))
	}
	if _, err := expiring.Authenticate(viewerSessionPageRequest(expiring, expired)); !errors.Is(err, errViewerSessionUnauthorized) {
		t.Fatalf("expired evicted session error = %v, want unauthorized", err)
	}
	if _, err := expiring.Authenticate(viewerSessionPageRequest(expiring, replacement)); err != nil {
		t.Fatalf("replacement session rejected: %v", err)
	}
}

func TestViewerSessionValidateMutationRequiresExactBrowserFormAndCSRF(t *testing.T) {
	t.Parallel()

	manager, _ := newTestViewerSessionManager(t, 8)
	session, _ := manager.Create(testViewerSessionReadiness())
	valid := viewerSessionMutationRequest(manager, session, nil)
	authenticated, err := manager.ValidateMutation(httptest.NewRecorder(), valid)
	if err != nil {
		t.Fatalf("ValidateMutation: %v", err)
	}
	if authenticated.ID != session.ID || valid.PostForm.Get("action") != "present" {
		t.Fatalf("valid mutation did not return session and parsed form: %#v %#v", authenticated, valid.PostForm)
	}

	tests := []struct {
		name   string
		mutate func(*http.Request)
		want   error
	}{
		{"GET", func(r *http.Request) { r.Method = http.MethodGet }, errViewerSessionMethodNotAllowed},
		{"query", func(r *http.Request) { r.URL.RawQuery = "from=ambient" }, errViewerSessionUnauthorized},
		{"missing origin", func(r *http.Request) { r.Header.Del("Origin") }, errViewerSessionUnauthorized},
		{"cross origin", func(r *http.Request) { r.Header.Set("Origin", "http://localhost:43129") }, errViewerSessionUnauthorized},
		{"duplicate origin", func(r *http.Request) { r.Header.Add("Origin", testViewerSessionOrigin) }, errViewerSessionUnauthorized},
		{"cross site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, errViewerSessionUnauthorized},
		{"navigate", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }, errViewerSessionUnauthorized},
		{"iframe", func(r *http.Request) { r.Header.Set("Sec-Fetch-Dest", "iframe") }, errViewerSessionUnauthorized},
		{"missing fetch metadata", func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") }, errViewerSessionUnauthorized},
		{"preflight", func(r *http.Request) { r.Header.Set("Access-Control-Request-Method", "POST") }, errViewerSessionUnauthorized},
		{"prefetch", func(r *http.Request) { r.Header.Set("Sec-Purpose", "prefetch") }, errViewerSessionUnauthorized},
		{"ipc authority", func(r *http.Request) { r.Header.Set("X-Pulse-Key", "daemon") }, errViewerSessionUnauthorized},
		{"wrong content type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, errViewerSessionUnsupportedMediaType},
		{"content type parameters", func(r *http.Request) { r.Header.Set("Content-Type", viewerSessionFormMediaType+"; charset=utf-8") }, errViewerSessionUnsupportedMediaType},
		{"duplicate content type", func(r *http.Request) { r.Header.Add("Content-Type", viewerSessionFormMediaType) }, errViewerSessionUnsupportedMediaType},
		{"missing csrf", func(r *http.Request) { replaceViewerSessionForm(r, url.Values{"action": {"present"}}) }, errViewerSessionUnauthorized},
		{"duplicate csrf", func(r *http.Request) {
			replaceViewerSessionForm(r, url.Values{viewerSessionCSRFFormField: {session.CSRFToken, session.CSRFToken}, "action": {"present"}})
		}, errViewerSessionUnauthorized},
		{"wrong csrf", func(r *http.Request) {
			replaceViewerSessionForm(r, url.Values{viewerSessionCSRFFormField: {strings.Repeat("A", len(session.CSRFToken))}, "action": {"present"}})
		}, errViewerSessionUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := viewerSessionMutationRequest(manager, session, nil)
			tc.mutate(request)
			if _, err := manager.ValidateMutation(httptest.NewRecorder(), request); !errors.Is(err, tc.want) {
				t.Fatalf("ValidateMutation error = %v, want %v", err, tc.want)
			}
		})
	}

	oversized := viewerSessionMutationRequest(manager, session, url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"padding":                  {strings.Repeat("x", 2048)},
	})
	if _, err := manager.ValidateMutation(httptest.NewRecorder(), oversized); !errors.Is(err, errViewerSessionRequestTooLarge) {
		t.Fatalf("oversized mutation error = %v, want request too large", err)
	}
}

func TestViewerSessionRevokeCookiesAndHardenedHeaders(t *testing.T) {
	t.Parallel()

	manager, _ := newTestViewerSessionManager(t, 8)
	session, _ := manager.Create(testViewerSessionReadiness())
	cookie := manager.Cookie(session)
	if cookie.Name != viewerSessionCookieName || cookie.Value != session.ID || cookie.Path != viewerSessionRoutePath(session.RouteScope) ||
		cookie.Domain != "" || cookie.HttpOnly != true || cookie.Secure || cookie.SameSite != http.SameSiteStrictMode ||
		cookie.MaxAge <= 0 || !cookie.Expires.Equal(session.AbsoluteExpiresAt) {
		t.Fatalf("unsafe HTTP session cookie: %#v", cookie)
	}
	clear := manager.ClearCookie(session.RouteScope)
	if clear.Name != viewerSessionCookieName || clear.Value != "" || clear.Path != viewerSessionRoutePath(session.RouteScope) ||
		clear.Domain != "" || !clear.HttpOnly || clear.Secure || clear.SameSite != http.SameSiteStrictMode ||
		clear.MaxAge >= 0 || !clear.Expires.Before(time.Now()) {
		t.Fatalf("unsafe clear cookie: %#v", clear)
	}

	httpsConfig := viewerSessionConfig{
		ExpectedOrigin: "https://localhost:43129", AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute,
		MaxSessions: 8, MaxBodyBytes: 1024,
	}
	httpsManager, err := newViewerSessionManager(httpsConfig)
	if err != nil {
		t.Fatalf("new HTTPS manager: %v", err)
	}
	httpsSession, _ := httpsManager.Create(testViewerSessionReadiness())
	if !httpsManager.Cookie(httpsSession).Secure || !httpsManager.ClearCookie(httpsSession.RouteScope).Secure {
		t.Fatal("HTTPS Home cookies must be Secure")
	}

	if !manager.Revoke(session) || manager.Revoke(session) {
		t.Fatal("Revoke must remove one exact live session once")
	}
	if _, err := manager.Authenticate(viewerSessionPageRequest(manager, session)); !errors.Is(err, errViewerSessionUnauthorized) {
		t.Fatalf("revoked session error = %v, want unauthorized", err)
	}

	header := make(http.Header)
	manager.HardenHeaders(header)
	wants := map[string]string{
		"Cache-Control":                "no-store, private, max-age=0",
		"Pragma":                       "no-cache",
		"Referrer-Policy":              "no-referrer",
		"X-Frame-Options":              "DENY",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"X-Content-Type-Options":       "nosniff",
	}
	for name, want := range wants {
		if got := header.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	csp := header.Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'none'", "form-action 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("CSP %q missing %q", csp, directive)
		}
	}
}

type viewerSessionTestClock struct {
	now time.Time
}

func (c *viewerSessionTestClock) Now() time.Time {
	return c.now
}

func (c *viewerSessionTestClock) Advance(delta time.Duration) {
	c.now = c.now.Add(delta)
}

func newTestViewerSessionManager(t *testing.T, maxSessions int) (*viewerSessionManager, *viewerSessionTestClock) {
	t.Helper()
	clock := &viewerSessionTestClock{now: time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)}
	manager, err := newViewerSessionManager(viewerSessionConfig{
		ExpectedOrigin: testViewerSessionOrigin,
		AbsoluteTTL:    time.Hour,
		IdleTTL:        15 * time.Minute,
		MaxSessions:    maxSessions,
		MaxBodyBytes:   1024,
		Clock:          clock.Now,
	})
	if err != nil {
		t.Fatalf("newViewerSessionManager: %v", err)
	}
	return manager, clock
}

func viewerSessionPageRequest(manager *viewerSessionManager, session viewerSessionView) *http.Request {
	request := httptest.NewRequest(http.MethodGet, manager.expectedOrigin+viewerSessionRoutePath(session.RouteScope), nil)
	request.Host = manager.expectedHost
	request.RemoteAddr = "127.0.0.1:54321"
	request.AddCookie(manager.Cookie(session))
	return request
}

func viewerSessionMutationRequest(manager *viewerSessionManager, session viewerSessionView, override url.Values) *http.Request {
	form := url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"action":                   {"present"},
	}
	if override != nil {
		form = override
	}
	request := httptest.NewRequest(http.MethodPost, manager.expectedOrigin+viewerSessionRoutePath(session.RouteScope)+"present", strings.NewReader(form.Encode()))
	request.Host = manager.expectedHost
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Origin", manager.expectedOrigin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Sec-Fetch-Mode", "cors")
	request.Header.Set("Sec-Fetch-Dest", "empty")
	request.Header.Set("Content-Type", viewerSessionFormMediaType)
	request.AddCookie(manager.Cookie(session))
	return request
}

func replaceViewerSessionForm(request *http.Request, form url.Values) {
	encoded := form.Encode()
	request.Body = io.NopCloser(strings.NewReader(encoded))
	request.ContentLength = int64(len(encoded))
}
