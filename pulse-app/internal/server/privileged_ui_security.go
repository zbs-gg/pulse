package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	privilegedUICSRFCookieName = "__Host-pulse-airlock-csrf"
	privilegedUICSRFFormField  = "csrf_token"
	privilegedUIFormMediaType  = "application/x-www-form-urlencoded"
	privilegedUICSRFTokenBytes = 32
	privilegedUICSRFMaxAge     = 10 * time.Minute
)

// privilegedUISecurity is the fail-closed browser boundary for the owner-only
// Airlock UI. It deliberately does not authorize a publication: callers must
// perform owner step-up and store authorization after this HTTP boundary.
type privilegedUISecurity struct {
	expectedOrigin string
	expectedHost   string
	maxBodyBytes   int64
	random         io.Reader
	now            func() time.Time
}

// newPrivilegedUISecurity accepts a single, bare HTTPS origin. Wildcards,
// paths and alternate schemes are rejected so Host and Origin checks cannot
// silently grow broader than the configured owner surface.
func newPrivilegedUISecurity(expectedOrigin string, maxBodyBytes int64) (*privilegedUISecurity, error) {
	if expectedOrigin == "" || expectedOrigin != strings.TrimSpace(expectedOrigin) {
		return nil, errors.New("privileged UI: expected origin is required")
	}
	parsed, err := url.Parse(expectedOrigin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" ||
		!validPrivilegedUIAuthority(parsed.Host) || strings.Contains(parsed.Host, "*") {
		return nil, errors.New("privileged UI: expected origin must be a bare HTTPS origin")
	}
	if maxBodyBytes <= 0 {
		return nil, errors.New("privileged UI: max body bytes must be positive")
	}
	return &privilegedUISecurity{
		expectedOrigin: "https://" + parsed.Host,
		expectedHost:   parsed.Host,
		maxBodyBytes:   maxBodyBytes,
		random:         rand.Reader,
		now:            time.Now,
	}, nil
}

// page applies response hardening and the configured Host boundary. Browser
// navigation does not reliably carry Origin, so exact Origin is required by
// formMutation, where ambient credentials could otherwise trigger a write.
func (s *privilegedUISecurity) page(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPrivilegedUIHeaders(w.Header())
		if !s.validHost(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// formMutation enforces the complete browser-request shape for an owner
// action. It accepts only a same-origin POSTed HTML form whose host-only CSRF
// cookie exactly matches the hidden, body-carried token.
func (s *privilegedUISecurity) formMutation(next http.Handler) http.Handler {
	return s.page(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.validOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != privilegedUIFormMediaType || len(params) != 0 {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			return
		}
		if r.ContentLength > s.maxBodyBytes {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
		if err := r.ParseForm(); err != nil {
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		presented := r.PostForm[privilegedUICSRFFormField]
		if len(presented) != 1 || !s.validCSRF(r, presented[0]) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// issueCSRFCookie creates a cryptographically random, host-only double-submit
// token. HttpOnly is intentional: the server renders the returned value into a
// hidden form field, while browser script cannot read the ambient cookie.
func (s *privilegedUISecurity) issueCSRFCookie(w http.ResponseWriter) (string, error) {
	raw := make([]byte, privilegedUICSRFTokenBytes)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return "", errors.New("privileged UI: generate CSRF token")
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now().UTC()
	http.SetCookie(w, &http.Cookie{
		Name:     privilegedUICSRFCookieName,
		Value:    token,
		Path:     "/",
		Expires:  now.Add(privilegedUICSRFMaxAge),
		MaxAge:   int(privilegedUICSRFMaxAge / time.Second),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	return token, nil
}

func (s *privilegedUISecurity) validHost(rawHost string) bool {
	return validPrivilegedUIAuthority(rawHost) && strings.EqualFold(rawHost, s.expectedHost)
}

func (s *privilegedUISecurity) validOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) != 1 {
		return false
	}
	raw := origins[0]
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, ",\r\n\t") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.Opaque != "" || !validPrivilegedUIAuthority(parsed.Host) {
		return false
	}
	if parsed.Scheme+"://"+parsed.Host != raw || raw != s.expectedOrigin ||
		!strings.EqualFold(parsed.Host, s.expectedHost) {
		return false
	}
	if fetchSite := r.Header.Get("Sec-Fetch-Site"); fetchSite != "" && fetchSite != "same-origin" {
		return false
	}
	return true
}

func (s *privilegedUISecurity) validCSRF(r *http.Request, presented string) bool {
	if !validPrivilegedUICSRFToken(presented) {
		return false
	}
	var cookieValue string
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == privilegedUICSRFCookieName {
			count++
			cookieValue = cookie.Value
		}
	}
	if count != 1 || !validPrivilegedUICSRFToken(cookieValue) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookieValue), []byte(presented)) == 1
}

func validPrivilegedUICSRFToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == privilegedUICSRFTokenBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == token
}

func validPrivilegedUIAuthority(authority string) bool {
	if authority == "" || authority != strings.TrimSpace(authority) ||
		strings.ContainsAny(authority, "@/,\\?#\r\n\t ") {
		return false
	}
	parsed, err := url.Parse("https://" + authority)
	return err == nil && parsed.Host == authority && parsed.Hostname() != "" && parsed.User == nil &&
		parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func setPrivilegedUIHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store, private, max-age=0")
	header.Set("Pragma", "no-cache")
	header.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}

// escapePrivilegedUIText is for stored scalar values interpolated into owner
// HTML. Full pages should still use html/template, which auto-escapes by
// context; this helper keeps even small status fragments out of raw markup.
func escapePrivilegedUIText(value string) string {
	return html.EscapeString(value)
}
