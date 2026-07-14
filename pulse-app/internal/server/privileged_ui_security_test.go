package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testPrivilegedUIOrigin = "https://airlock.pulse.example"

func TestNewPrivilegedUISecurityRequiresBareHTTPSOrigin(t *testing.T) {
	t.Parallel()

	invalid := []string{
		"",
		"http://airlock.pulse.example",
		"https://airlock.pulse.example/path",
		"https://airlock.pulse.example?query=1",
		"https://user@airlock.pulse.example",
		"https://airlock.pulse.example#fragment",
		"https://*.pulse.example",
	}
	for _, raw := range invalid {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := newPrivilegedUISecurity(raw, 1024); err == nil {
				t.Fatalf("newPrivilegedUISecurity(%q) unexpectedly succeeded", raw)
			}
		})
	}

	if _, err := newPrivilegedUISecurity(testPrivilegedUIOrigin, 1024); err != nil {
		t.Fatalf("valid origin rejected: %v", err)
	}
}

func TestPrivilegedUIPageRequiresConfiguredHostAndSetsDefensiveHeaders(t *testing.T) {
	t.Parallel()

	security := mustPrivilegedUISecurity(t, 1024)
	handler := security.page(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
	}))

	valid := httptest.NewRequest(http.MethodGet, testPrivilegedUIOrigin+"/airlock", nil)
	valid.Host = "airlock.pulse.example"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, valid)
	if response.Code != http.StatusOK {
		t.Fatalf("valid page status = %d, want %d", response.Code, http.StatusOK)
	}
	assertPrivilegedUIHeaders(t, response.Header())

	for name, host := range map[string]string{
		"absent":        "",
		"dns_rebinding": "127.0.0.1:8080",
		"port_mismatch": "airlock.pulse.example:443",
		"malformed":     "airlock.pulse.example,evil.example",
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, testPrivilegedUIOrigin+"/airlock", nil)
			request.Host = host
			got := httptest.NewRecorder()
			handler.ServeHTTP(got, request)
			if got.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", got.Code, http.StatusForbidden)
			}
			assertPrivilegedUIHeaders(t, got.Header())
		})
	}
}

func TestPrivilegedUIFormMutationRejectsUntrustedOriginAndCSRF(t *testing.T) {
	t.Parallel()

	security := mustPrivilegedUISecurity(t, 1024)
	token, cookie := issuePrivilegedCSRFCookie(t, security)
	handler := security.formMutation(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name        string
		origin      string
		host        string
		csrf        string
		withCookie  bool
		extraOrigin string
	}{
		{name: "malicious_local_page", origin: "http://127.0.0.1:5173", host: "airlock.pulse.example", csrf: token, withCookie: true},
		{name: "cross_origin", origin: "https://evil.example", host: "airlock.pulse.example", csrf: token, withCookie: true},
		{name: "missing_origin", host: "airlock.pulse.example", csrf: token, withCookie: true},
		{name: "malformed_origin", origin: "https://airlock.pulse.example/path", host: "airlock.pulse.example", csrf: token, withCookie: true},
		{name: "multiple_origins", origin: testPrivilegedUIOrigin, extraOrigin: "https://evil.example", host: "airlock.pulse.example", csrf: token, withCookie: true},
		{name: "dns_rebinding", origin: testPrivilegedUIOrigin, host: "127.0.0.1:8080", csrf: token, withCookie: true},
		{name: "missing_cookie", origin: testPrivilegedUIOrigin, host: "airlock.pulse.example", csrf: token},
		{name: "missing_form_token", origin: testPrivilegedUIOrigin, host: "airlock.pulse.example", withCookie: true},
		{name: "invalid_form_token", origin: testPrivilegedUIOrigin, host: "airlock.pulse.example", csrf: strings.Repeat("a", len(token)), withCookie: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{}
			if tc.csrf != "" {
				form.Set(privilegedUICSRFFormField, tc.csrf)
			}
			request := httptest.NewRequest(http.MethodPost, testPrivilegedUIOrigin+"/airlock/publish", strings.NewReader(form.Encode()))
			request.Host = tc.host
			request.Header.Set("Content-Type", privilegedUIFormMediaType)
			if tc.origin != "" {
				request.Header.Add("Origin", tc.origin)
			}
			if tc.extraOrigin != "" {
				request.Header.Add("Origin", tc.extraOrigin)
			}
			if tc.withCookie {
				request.AddCookie(cookie)
			}

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
			}
		})
	}
}

func TestPrivilegedUIFormMutationAcceptsExactOriginAndDoubleSubmitToken(t *testing.T) {
	t.Parallel()

	security := mustPrivilegedUISecurity(t, 1024)
	token, cookie := issuePrivilegedCSRFCookie(t, security)
	called := false
	handler := security.formMutation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got := r.PostForm.Get("decision"); got != "approve" {
			t.Fatalf("parsed form decision = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	form := url.Values{privilegedUICSRFFormField: {token}, "decision": {"approve"}}
	request := httptest.NewRequest(http.MethodPost, testPrivilegedUIOrigin+"/airlock/publish", strings.NewReader(form.Encode()))
	request.Host = "AIRLOCK.PULSE.EXAMPLE"
	request.Header.Set("Origin", testPrivilegedUIOrigin)
	request.Header.Set("Content-Type", privilegedUIFormMediaType)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("status = %d called = %t, want 204/true", response.Code, called)
	}
}

func TestPrivilegedUIFormMutationRejectsUnsafeRequestShapes(t *testing.T) {
	t.Parallel()

	security := mustPrivilegedUISecurity(t, 128)
	token, cookie := issuePrivilegedCSRFCookie(t, security)
	handler := security.formMutation(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := privilegedFormRequest(token, cookie)
	request.Method = http.MethodGet
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("unsafe method response = %d Allow=%q", response.Code, response.Header().Get("Allow"))
	}

	request = privilegedFormRequest(token, cookie)
	request.Header.Set("Content-Type", "text/plain")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("unsafe content type status = %d", response.Code)
	}

	oversized := url.Values{privilegedUICSRFFormField: {token}, "padding": {strings.Repeat("x", 256)}}
	request = httptest.NewRequest(http.MethodPost, testPrivilegedUIOrigin+"/airlock/publish", strings.NewReader(oversized.Encode()))
	request.Host = "airlock.pulse.example"
	request.Header.Set("Origin", testPrivilegedUIOrigin)
	request.Header.Set("Content-Type", privilegedUIFormMediaType)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestPrivilegedUICSRFCookieAndStoredTextEscaping(t *testing.T) {
	t.Parallel()

	security := mustPrivilegedUISecurity(t, 1024)
	token, cookie := issuePrivilegedCSRFCookie(t, security)
	if !validPrivilegedUICSRFToken(token) {
		t.Fatalf("issued token is not canonical")
	}
	if cookie.Name != privilegedUICSRFCookieName || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie attributes: %#v", cookie)
	}
	if cookie.Domain != "" || cookie.MaxAge <= 0 {
		t.Fatalf("cookie must be host-only and short-lived: %#v", cookie)
	}

	stored := `<img src=x onerror="alert(1)"><script>alert(2)</script>&"'`
	escaped := escapePrivilegedUIText(stored)
	if strings.Contains(escaped, "<script") || strings.Contains(escaped, "<img") || strings.Contains(escaped, "onerror=\"") {
		t.Fatalf("stored XSS remained executable: %q", escaped)
	}
	for _, want := range []string{"&lt;script&gt;", "&lt;img", "&amp;", "&#34;", "&#39;"} {
		if !strings.Contains(escaped, want) {
			t.Fatalf("escaped text %q missing %q", escaped, want)
		}
	}
}

func mustPrivilegedUISecurity(t *testing.T, maxBodyBytes int64) *privilegedUISecurity {
	t.Helper()
	security, err := newPrivilegedUISecurity(testPrivilegedUIOrigin, maxBodyBytes)
	if err != nil {
		t.Fatal(err)
	}
	return security
}

func issuePrivilegedCSRFCookie(t *testing.T, security *privilegedUISecurity) (string, *http.Cookie) {
	t.Helper()
	recorder := httptest.NewRecorder()
	token, err := security.issueCSRFCookie(recorder)
	if err != nil {
		t.Fatal(err)
	}
	response := recorder.Result()
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	return token, cookies[0]
}

func privilegedFormRequest(token string, cookie *http.Cookie) *http.Request {
	form := url.Values{privilegedUICSRFFormField: {token}}
	request := httptest.NewRequest(http.MethodPost, testPrivilegedUIOrigin+"/airlock/publish", strings.NewReader(form.Encode()))
	request.Host = "airlock.pulse.example"
	request.Header.Set("Origin", testPrivilegedUIOrigin)
	request.Header.Set("Content-Type", privilegedUIFormMediaType)
	request.AddCookie(cookie)
	return request
}

func assertPrivilegedUIHeaders(t *testing.T, header http.Header) {
	t.Helper()
	for name, want := range map[string]string{
		"Cache-Control":           "no-store, private, max-age=0",
		"Content-Security-Policy": "default-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
	} {
		if got := header.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}
