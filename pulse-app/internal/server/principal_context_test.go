package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

type principalFixture struct {
	store     *store.Store
	verifier  *PrincipalVerifier
	private   ed25519.PrivateKey
	now       time.Time
	storeID   string
	teamID    string
	issuer    string
	subject   string
	agentID   string
	bindingID string
	serviceID string
	ownerID   string
	root      teamauth.BootstrapRoot
}

func newPrincipalFixture(t *testing.T) *principalFixture {
	t.Helper()
	now := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	root := teamauth.BootstrapRoot{Issuer: "https://issuer.example", Subject: "owner-sub", AdminClientID: "admin-client"}
	s, err := store.OpenTeam(filepath.Join(t.TempDir(), "team.db"), store.TeamOpenOptions{
		ExpectedBootstrapRoot: root, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	boot, err := s.BootstrapTeam(context.Background(), store.BootstrapTeamRequest{TeamName: "U3", PresentedRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.RegisterAgentBinding(context.Background(), store.RegisterAgentBindingRequest{
		ActorPrincipalID: boot.OwnerPrincipalID, Issuer: root.Issuer, Subject: root.Subject, ClientID: "agent-client",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := s.RegisterServicePrincipal(context.Background(), store.RegisterServicePrincipalRequest{
		ActorPrincipalID: boot.OwnerPrincipalID, Issuer: root.Issuer, ClientID: "service-client",
	})
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewPrincipalVerifier(PrincipalVerifierConfig{
		Store: s, Keyring: PrincipalVerifyKeyring{ActiveKid: "active", Keys: map[string]ed25519.PublicKey{"active": pub}},
		ExpectedStoreID: boot.StoreID, ExpectedTeamID: boot.TeamID, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &principalFixture{store: s, verifier: verifier, private: priv, now: now, storeID: boot.StoreID, teamID: boot.TeamID,
		issuer: root.Issuer, subject: root.Subject, agentID: agent.AgentPrincipalID, bindingID: agent.BindingID, serviceID: service.PrincipalID,
		ownerID: boot.OwnerPrincipalID, root: root}
}

func TestPrincipalCapabilitiesAcceptSortedAllowlistWhenAuditSortsBeforeConnect(t *testing.T) {
	f := newPrincipalFixture(t)
	body := f.requestBody("agent-client")
	var request principalCheckBody
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	request.Capabilities = []string{"pulse:audit", "pulse:connect"}
	body, _ = json.Marshal(request)
	claims := f.claims("audit-capability", "agent-client", body)
	claims["capabilities"] = request.Capabilities
	assertion := signPrincipalAssertion(t, f.private, "active", nil, claims)
	if _, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-audit-capability", "POST", PrincipalCheckRoutePath, body); err != nil {
		t.Fatalf("sorted allowlisted capabilities rejected: %v", err)
	}
}

func (f *principalFixture) claims(jti, clientID string, body []byte) map[string]any {
	digest := sha256.Sum256(body)
	return map[string]any{
		"version": "pulse.principal.v1", "iss": "pulse-team-gateway", "aud": "pulse-team-daemon",
		"iat": f.now.Unix(), "nbf": f.now.Unix(), "exp": f.now.Add(25 * time.Second).Unix(), "jti": jti,
		"request_id": "req-" + jti, "method": "POST", "path": PrincipalCheckRoutePath,
		"body_sha256": fmt.Sprintf("%x", digest), "store_id": f.storeID, "team_id": f.teamID,
		"oauth_issuer": f.issuer, "oauth_subject": f.subject, "oauth_client_id": clientID,
		"grant_kind": "registered", "capabilities": []string{"pulse:connect", "pulse:read"},
	}
}

func (f *principalFixture) requestBody(clientID string) []byte {
	body, _ := json.Marshal(struct {
		OAuthIssuer  string   `json:"oauth_issuer"`
		OAuthSubject string   `json:"oauth_subject"`
		OAuthClient  string   `json:"oauth_client_id"`
		Capabilities []string `json:"capabilities"`
	}{f.issuer, f.subject, clientID, []string{"pulse:connect", "pulse:read"}})
	return body
}

func (f *principalFixture) gatewayEventClaims(jti string, body []byte) map[string]any {
	digest := sha256.Sum256(body)
	return map[string]any{
		"version": "pulse.security_event.v1", "iss": "pulse-team-gateway", "aud": "pulse-team-daemon",
		"iat": f.now.Unix(), "nbf": f.now.Add(-time.Second).Unix(), "exp": f.now.Add(25 * time.Second).Unix(), "jti": jti,
		"request_id": "req-" + jti, "method": http.MethodPost, "path": SecurityEventRoutePath,
		"body_sha256": fmt.Sprintf("%x", digest), "store_id": f.storeID, "team_id": f.teamID,
	}
}

func TestPrincipalVerifierResolvesAgentAndServiceFromRegistry(t *testing.T) {
	f := newPrincipalFixture(t)
	for _, tc := range []struct{ name, client, jti, kind, principal, binding string }{
		{"agent", "agent-client", "agent-ok", "agent", f.agentID, f.bindingID},
		{"service", "service-client", "service-ok", "service", f.serviceID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := f.requestBody(tc.client)
			assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims(tc.jti, tc.client, body))
			principal, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-"+tc.jti, "POST", PrincipalCheckRoutePath, body)
			if err != nil {
				t.Fatal(err)
			}
			binding := ""
			if principal.AgentBindingID != nil {
				binding = *principal.AgentBindingID
			}
			if principal.PrincipalKind != tc.kind || principal.PrincipalID != tc.principal || binding != tc.binding {
				t.Fatalf("principal = %+v", principal)
			}
			if principal.OAuthClientKey != teamauth.OAuthClientKey(f.issuer, tc.client) {
				t.Fatalf("opaque OAuth client key = %q", principal.OAuthClientKey)
			}
			encoded, _ := json.Marshal(principal)
			if bytes.Contains(encoded, []byte(f.subject)) || bytes.Contains(encoded, []byte(tc.client)) {
				t.Fatalf("external identity leaked: %s", encoded)
			}
		})
	}
}

func TestPrincipalVerifierRejectsAlteredExpiredReplayAndRequestMismatch(t *testing.T) {
	f := newPrincipalFixture(t)
	body := f.requestBody("agent-client")
	tests := []struct {
		name                    string
		mutate                  func(map[string]any)
		requestID, method, path string
		requestBody             []byte
		kid                     string
		want                    error
	}{
		{name: "expired", mutate: func(c map[string]any) {
			c["iat"] = f.now.Add(-40 * time.Second).Unix()
			c["nbf"] = f.now.Add(-40 * time.Second).Unix()
			c["exp"] = f.now.Add(-10 * time.Second).Unix()
		}, want: ErrPrincipalInvalid},
		{name: "wrong body", requestBody: f.requestBody("service-client"), want: ErrPrincipalRequestMismatch},
		{name: "wrong path", path: "/team/v1/wrong", want: ErrPrincipalRequestMismatch},
		{name: "wrong request", requestID: "req-other", want: ErrPrincipalRequestMismatch},
		{name: "wrong store", mutate: func(c map[string]any) { c["store_id"] = "store_wrong" }, want: ErrPrincipalRequestMismatch},
		{name: "wrong team", mutate: func(c map[string]any) { c["team_id"] = "team_wrong" }, want: ErrPrincipalRequestMismatch},
		{name: "unknown kid", kid: "missing", want: ErrPrincipalInvalid},
		{name: "wrong version", mutate: func(c map[string]any) { c["version"] = "pulse.principal.v2" }, want: ErrPrincipalInvalid},
		{name: "wrong issuer", mutate: func(c map[string]any) { c["iss"] = "pulse-gateway" }, want: ErrPrincipalInvalid},
		{name: "wrong audience", mutate: func(c map[string]any) { c["aud"] = "pulse-daemon" }, want: ErrPrincipalInvalid},
		{name: "not before", mutate: func(c map[string]any) { c["nbf"] = f.now.Add(6 * time.Second).Unix() }, want: ErrPrincipalInvalid},
		{name: "lifetime", mutate: func(c map[string]any) { c["exp"] = f.now.Add(31 * time.Second).Unix() }, want: ErrPrincipalInvalid},
		{name: "time ordering", mutate: func(c map[string]any) {
			c["exp"] = f.now.Add(time.Second).Unix()
			c["nbf"] = f.now.Add(5 * time.Second).Unix()
		}, want: ErrPrincipalInvalid},
		{name: "lowercase method", mutate: func(c map[string]any) { c["method"] = "post" }, want: ErrPrincipalInvalid},
		{name: "wrong grant kind", mutate: func(c map[string]any) { c["grant_kind"] = "inferred" }, want: ErrPrincipalInvalid},
		{name: "unsorted capabilities", mutate: func(c map[string]any) { c["capabilities"] = []string{"pulse:read", "pulse:connect"} }, want: ErrPrincipalInvalid},
		{name: "duplicate capabilities", mutate: func(c map[string]any) { c["capabilities"] = []string{"pulse:connect", "pulse:connect"} }, want: ErrPrincipalInvalid},
		{name: "caller role forbidden", mutate: func(c map[string]any) { c["role"] = "owner" }, want: ErrPrincipalInvalid},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			jti := fmt.Sprintf("negative-%d", i)
			claims := f.claims(jti, "agent-client", body)
			if tc.mutate != nil {
				tc.mutate(claims)
			}
			kid := tc.kid
			if kid == "" {
				kid = "active"
			}
			assertion := signPrincipalAssertion(t, f.private, kid, nil, claims)
			requestID := tc.requestID
			if requestID == "" {
				requestID = "req-" + jti
			}
			method := tc.method
			if method == "" {
				method = "POST"
			}
			path := tc.path
			if path == "" {
				path = PrincipalCheckRoutePath
			}
			requestBody := tc.requestBody
			if requestBody == nil {
				requestBody = body
			}
			_, err := f.verifier.VerifyRequest(context.Background(), assertion, requestID, method, path, requestBody)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	claims := f.claims("altered", "agent-client", body)
	assertion := signPrincipalAssertion(t, f.private, "active", nil, claims)
	assertion = assertion[:len(assertion)-1] + map[bool]string{true: "A", false: "B"}[assertion[len(assertion)-1] != 'A']
	if _, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-altered", "POST", PrincipalCheckRoutePath, body); !errors.Is(err, ErrPrincipalInvalid) {
		t.Fatalf("altered assertion error = %v", err)
	}

	valid := signPrincipalAssertion(t, f.private, "active", nil, f.claims("replay", "agent-client", body))
	if _, err := f.verifier.VerifyRequest(context.Background(), valid, "req-replay", "POST", PrincipalCheckRoutePath, body); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verifier.VerifyRequest(context.Background(), valid, "req-replay", "POST", PrincipalCheckRoutePath, body); !errors.Is(err, ErrPrincipalReplay) {
		t.Fatalf("replay error = %v", err)
	}
}

func TestPrincipalVerifierRejectsStrictJWSHeaderAndFraming(t *testing.T) {
	f := newPrincipalFixture(t)
	body := f.requestBody("agent-client")
	claims := f.claims("header", "agent-client", body)
	for _, tc := range []struct {
		name   string
		header map[string]any
	}{
		{"algorithm", map[string]any{"alg": "RS256"}},
		{"type", map[string]any{"typ": "JWT"}},
		{"unknown header", map[string]any{"crit": "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertion := signPrincipalAssertion(t, f.private, "active", tc.header, claims)
			if _, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-header", "POST", PrincipalCheckRoutePath, body); !errors.Is(err, ErrPrincipalInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for _, compact := range []string{"a.b", "a.b.c.d", strings.Repeat("x", PrincipalAssertionMaxBytes+1)} {
		if _, err := f.verifier.VerifyRequest(context.Background(), compact, "req-header", "POST", PrincipalCheckRoutePath, body); !errors.Is(err, ErrPrincipalInvalid) {
			t.Fatalf("framing %q error=%v", compact[:min(len(compact), 16)], err)
		}
	}
}

func TestPrincipalVerifierAcceptsStillValidAssertionIssuedTwentyFiveSecondsAgo(t *testing.T) {
	f := newPrincipalFixture(t)
	body := f.requestBody("agent-client")
	claims := f.claims("old-valid", "agent-client", body)
	claims["iat"] = f.now.Add(-25 * time.Second).Unix()
	claims["nbf"] = f.now.Add(-25 * time.Second).Unix()
	claims["exp"] = f.now.Add(5 * time.Second).Unix()
	assertion := signPrincipalAssertion(t, f.private, "active", nil, claims)
	if _, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-old-valid", "POST", PrincipalCheckRoutePath, body); err != nil {
		t.Fatalf("still-valid assertion rejected: %v", err)
	}
}

func TestPrincipalVerifierRejectsRequestIDBoundsAndIdentityControls(t *testing.T) {
	f := newPrincipalFixture(t)
	body := f.requestBody("agent-client")
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any, *principalCheckBody)
	}{
		{"request id", func(c map[string]any, _ *principalCheckBody) { c["request_id"] = strings.Repeat("r", 65) }},
		{"identity control", func(c map[string]any, b *principalCheckBody) {
			c["oauth_subject"] = f.subject + "\u0001"
			b.OAuthSubject = f.subject + "\u0001"
		}},
		{"signed path", func(c map[string]any, _ *principalCheckBody) { c["path"] = "/team/v1/other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request principalCheckBody
			_ = json.Unmarshal(body, &request)
			claims := f.claims("bounds-"+strings.ReplaceAll(tc.name, " ", "-"), "agent-client", body)
			tc.mutate(claims, &request)
			requestBody, _ := json.Marshal(request)
			if tc.name == "identity control" {
				digest := sha256.Sum256(requestBody)
				claims["body_sha256"] = fmt.Sprintf("%x", digest)
			}
			assertion := signPrincipalAssertion(t, f.private, "active", nil, claims)
			requestID, _ := claims["request_id"].(string)
			if _, err := f.verifier.VerifyRequest(context.Background(), assertion, requestID, "POST", PrincipalCheckRoutePath, requestBody); !errors.Is(err, ErrPrincipalInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestPrincipalVerifierConcurrentContextsAndRevocation(t *testing.T) {
	f := newPrincipalFixture(t)
	type result struct {
		principal PrincipalContext
		err       error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, tc := range []struct{ jti, client string }{{"parallel-agent", "agent-client"}, {"parallel-service", "service-client"}} {
		wg.Add(1)
		go func(jti, client string) {
			defer wg.Done()
			body := f.requestBody(client)
			assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims(jti, client, body))
			principal, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-"+jti, "POST", PrincipalCheckRoutePath, body)
			results <- result{principal, err}
		}(tc.jti, tc.client)
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		seen[result.principal.PrincipalID] = true
	}
	if !seen[f.agentID] || !seen[f.serviceID] || len(seen) != 2 {
		t.Fatalf("principal crossover: %v", seen)
	}

	body := f.requestBody("agent-client")
	assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims("revoked", "agent-client", body))
	if err := f.store.RevokeAgentBinding(context.Background(), f.ownerID, f.bindingID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-revoked", "POST", PrincipalCheckRoutePath, body); !errors.Is(err, ErrPrincipalRevoked) {
		t.Fatalf("revoked error=%v", err)
	}
}

func TestPrincipalReplayPersistsAcrossStoreRestart(t *testing.T) {
	f := newPrincipalFixture(t)
	body := f.requestBody("service-client")
	assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims("restart-replay", "service-client", body))
	if _, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-restart-replay", "POST", PrincipalCheckRoutePath, body); err != nil {
		t.Fatal(err)
	}
	path := f.store.DBPath()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenTeam(path, store.TeamOpenOptions{ExpectedBootstrapRoot: f.root, Clock: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	public := f.private.Public().(ed25519.PublicKey)
	verifier, err := NewPrincipalVerifier(PrincipalVerifierConfig{Store: reopened, Keyring: PrincipalVerifyKeyring{ActiveKid: "active", Keys: map[string]ed25519.PublicKey{"active": public}}, ExpectedStoreID: f.storeID, ExpectedTeamID: f.teamID, Clock: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyRequest(context.Background(), assertion, "req-restart-replay", "POST", PrincipalCheckRoutePath, body); !errors.Is(err, ErrPrincipalReplay) {
		t.Fatalf("restart replay error=%v", err)
	}
}

func TestPrincipalVerifierPrunesExpiredReplayRowsOnBoundedCadence(t *testing.T) {
	f := newPrincipalFixture(t)
	if _, err := f.store.DB().Exec(`
		INSERT INTO team_assertion_replay(store_id, kid, jti, expires_at, consumed_at)
		VALUES (?, ?, ?, ?, ?)`, f.storeID, strings.Repeat("a", 64), strings.Repeat("b", 64),
		f.now.Add(-time.Second).Format(time.RFC3339Nano), f.now.Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	body := f.requestBody("agent-client")
	assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims("prune-real", "agent-client", body))
	if _, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-prune-real", http.MethodPost, PrincipalCheckRoutePath, body); err != nil {
		t.Fatal(err)
	}
	var expired int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM team_assertion_replay WHERE expires_at <= ?`,
		f.now.Format(time.RFC3339Nano)).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 0 {
		t.Fatalf("expired replay rows = %d, want 0", expired)
	}
}

func TestPrincipalVerifierCoalescesReplayPruningAndReportsDegraded(t *testing.T) {
	f := newPrincipalFixture(t)
	var mu sync.Mutex
	pruneCalls := 0
	degradedCalls := 0
	f.verifier.replayPruner = principalReplayPrunerFunc(func(context.Context) (int64, error) {
		mu.Lock()
		pruneCalls++
		mu.Unlock()
		return 0, errors.New("synthetic prune outage")
	})
	f.verifier.onReplayPruneDegraded = func() {
		mu.Lock()
		degradedCalls++
		mu.Unlock()
	}
	f.verifier.replayPruneInterval = time.Minute

	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jti := fmt.Sprintf("prune-%02d", i)
			body := f.requestBody("agent-client")
			assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims(jti, "agent-client", body))
			_, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-"+jti, http.MethodPost, PrincipalCheckRoutePath, body)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("valid request failed because pruning degraded: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if pruneCalls != 1 || degradedCalls != 1 {
		t.Fatalf("prune calls=%d degraded signals=%d, want 1 each", pruneCalls, degradedCalls)
	}
}

func TestPrincipalVerifierAuthenticatesGatewaySecurityEventEnvelope(t *testing.T) {
	f := newPrincipalFixture(t)
	body := []byte(`{"event_type":"authentication_denied","reason_code":"missing_credential","method_class":"other","path_class":"mcp","request_id":"req-gateway-event","count":1}`)
	claims := f.gatewayEventClaims("gateway-event", body)
	assertion := signPrincipalAssertion(t, f.private, "active", map[string]any{"typ": "pulse.security_event.v1"}, claims)
	if err := f.verifier.VerifyGatewayEvent(context.Background(), assertion, "req-gateway-event", http.MethodPost, SecurityEventRoutePath, body); err != nil {
		t.Fatal(err)
	}
	if err := f.verifier.VerifyGatewayEvent(context.Background(), assertion, "req-gateway-event", http.MethodPost, SecurityEventRoutePath, body); !errors.Is(err, ErrPrincipalReplay) {
		t.Fatalf("gateway replay error = %v, want replay", err)
	}

	for _, tc := range []struct {
		name        string
		mutate      func(map[string]any)
		requestID   string
		requestBody []byte
		want        error
	}{
		{name: "body", requestBody: append(append([]byte(nil), body...), ' '), want: ErrPrincipalRequestMismatch},
		{name: "request", requestID: "req-other", want: ErrPrincipalRequestMismatch},
		{name: "principal fields forbidden", mutate: func(value map[string]any) { value["oauth_subject"] = "forbidden" }, want: ErrPrincipalInvalid},
		{name: "wrong version", mutate: func(value map[string]any) { value["version"] = "pulse.principal.v1" }, want: ErrPrincipalInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jti := "gateway-" + strings.ReplaceAll(tc.name, " ", "-")
			candidate := f.gatewayEventClaims(jti, body)
			if tc.mutate != nil {
				tc.mutate(candidate)
			}
			token := signPrincipalAssertion(t, f.private, "active", map[string]any{"typ": "pulse.security_event.v1"}, candidate)
			requestID := tc.requestID
			if requestID == "" {
				requestID = "req-" + jti
			}
			requestBody := tc.requestBody
			if requestBody == nil {
				requestBody = body
			}
			err := f.verifier.VerifyGatewayEvent(context.Background(), token, requestID, http.MethodPost, SecurityEventRoutePath, requestBody)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPrincipalVerifierReportsStoreUnavailableSeparatelyFromRevocation(t *testing.T) {
	f := newPrincipalFixture(t)
	body := f.requestBody("agent-client")
	assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims("store-down", "agent-client", body))
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-store-down", "POST", PrincipalCheckRoutePath, body); !errors.Is(err, ErrPrincipalStoreUnavailable) {
		t.Fatalf("error=%v", err)
	}
	h := NewPrincipalCheckHandler(f.verifier)
	r := httptest.NewRequest("POST", PrincipalCheckRoutePath, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Pulse-Principal", assertion)
	r.Header.Set("X-Pulse-Request-ID", "req-store-down")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"error":"principal_store_unavailable"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPrincipalVerifyKeyringFilePermissionsAndRotation(t *testing.T) {
	activePublic, _, _ := ed25519.GenerateKey(rand.Reader)
	previousPublic, previousPrivate, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	path := filepath.Join(dir, "keyring.json")
	writePrincipalKeyring(t, path, activePublic, previousPublic, 0o600)
	keyring, err := LoadPrincipalVerifyKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	if keyring.ActiveKid != "active" || len(keyring.Keys) != 2 {
		t.Fatalf("keyring=%+v", keyring)
	}
	t.Setenv(PrincipalVerifyKeyringEnv, path)
	if _, err := LoadPrincipalVerifyKeyringFromEnv(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPrincipalVerifyKeyring(path, uint32(os.Geteuid()+1)); err == nil {
		t.Fatal("wrong owner accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrincipalVerifyKeyring(path); err == nil {
		t.Fatal("group/world permissions accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "keyring-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrincipalVerifyKeyring(symlink); err == nil {
		t.Fatal("symlink accepted")
	}
	oversized := filepath.Join(dir, "oversized.json")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("x"), PrincipalVerifyKeyringMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrincipalVerifyKeyring(oversized); err == nil {
		t.Fatal("oversized keyring accepted")
	}

	f := newPrincipalFixture(t)
	f.verifier.keys = keyring.Keys
	body := f.requestBody("agent-client")
	assertion := signPrincipalAssertion(t, previousPrivate, "previous", nil, f.claims("previous-key", "agent-client", body))
	if _, err := f.verifier.VerifyRequest(context.Background(), assertion, "req-previous-key", "POST", PrincipalCheckRoutePath, body); err != nil {
		t.Fatalf("previous key rejected: %v", err)
	}
}

func TestPrincipalKeyringAllowsLongKidAndFourPreviousKeys(t *testing.T) {
	entries := make([]map[string]any, 0, 4)
	active, _, _ := ed25519.GenerateKey(rand.Reader)
	for i := 0; i < 4; i++ {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		entries = append(entries, map[string]any{"kid": fmt.Sprintf("previous-%d", i), "public_key": base64.RawURLEncoding.EncodeToString(pub)})
	}
	kid := strings.Repeat("k", 100)
	raw, _ := json.Marshal(map[string]any{"active": map[string]any{"kid": kid, "public_key": base64.RawURLEncoding.EncodeToString(active)}, "previous": entries})
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	keyring, err := LoadPrincipalVerifyKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	if keyring.ActiveKid != kid || len(keyring.Keys) != 5 {
		t.Fatalf("keyring=%+v", keyring)
	}
}

func TestPrincipalContextStorageCopiesMutableFields(t *testing.T) {
	human, binding, bindingEpoch := "human", "binding", int64(2)
	principal := PrincipalContext{HumanPrincipalID: &human, AgentBindingID: &binding, BindingAuthEpoch: &bindingEpoch, Capabilities: []string{"pulse:connect"}}
	ctx := WithPrincipalContext(context.Background(), principal)
	human = "changed"
	binding = "changed"
	bindingEpoch = 99
	principal.Capabilities[0] = "changed"
	got, ok := PrincipalContextFromContext(ctx)
	if !ok || *got.HumanPrincipalID != "human" || *got.AgentBindingID != "binding" || *got.BindingAuthEpoch != 2 || got.Capabilities[0] != "pulse:connect" {
		t.Fatalf("mutable context=%+v", got)
	}
}

func writePrincipalKeyring(t *testing.T, path string, active, previous ed25519.PublicKey, mode os.FileMode) {
	t.Helper()
	data := map[string]any{"active": map[string]any{"kid": "active", "public_key": base64.RawURLEncoding.EncodeToString(active)}, "previous": []map[string]any{{"kid": "previous", "public_key": base64.RawURLEncoding.EncodeToString(previous)}}}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
}

func TestPrincipalCheckHandlerRequiresPostJSONAndExactBody(t *testing.T) {
	f := newPrincipalFixture(t)
	body := f.requestBody("agent-client")
	assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims("handler", "agent-client", body))
	h := NewPrincipalCheckHandler(f.verifier)
	req := httptest.NewRequest(http.MethodPost, PrincipalCheckRoutePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pulse-Principal", assertion)
	req.Header.Set("X-Pulse-Request-ID", "req-handler")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got PrincipalContext
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.PrincipalID != f.agentID {
		t.Fatalf("context=%+v", got)
	}

	for _, tc := range []struct {
		name, method, contentType, body string
		want                            int
	}{
		{"method", http.MethodGet, "application/json", `{}`, http.StatusMethodNotAllowed},
		{"content type", http.MethodPost, "text/plain", `{}`, http.StatusUnsupportedMediaType},
		{"trailing json", http.MethodPost, "application/json", `{} {}`, http.StatusBadRequest},
		{"too large", http.MethodPost, "application/json", `"` + strings.Repeat("x", PrincipalCheckMaxBodyBytes) + `"`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, PrincipalCheckRoutePath, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			ww := httptest.NewRecorder()
			h.ServeHTTP(ww, r)
			if ww.Code != tc.want {
				t.Fatalf("status=%d", ww.Code)
			}
		})
	}

	revokedAssertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims("handler-revoked", "agent-client", body))
	if err := f.store.RevokeAgentBinding(context.Background(), f.ownerID, f.bindingID); err != nil {
		t.Fatal(err)
	}
	revokedRequest := httptest.NewRequest(http.MethodPost, PrincipalCheckRoutePath, bytes.NewReader(body))
	revokedRequest.Header.Set("Content-Type", "application/json")
	revokedRequest.Header.Set("X-Pulse-Principal", revokedAssertion)
	revokedRequest.Header.Set("X-Pulse-Request-ID", "req-handler-revoked")
	revokedResponse := httptest.NewRecorder()
	h.ServeHTTP(revokedResponse, revokedRequest)
	if revokedResponse.Code != http.StatusForbidden || !strings.Contains(revokedResponse.Body.String(), `"error":"principal_revoked"`) {
		t.Fatalf("revoked response status=%d body=%s", revokedResponse.Code, revokedResponse.Body.String())
	}
}

type testPrincipalRegistrar struct {
	method, path string
	handler      http.Handler
}

func (r *testPrincipalRegistrar) Method(method, path string, handler http.Handler) {
	r.method, r.path, r.handler = method, path, handler
}

func TestPrincipalRouteRegistrarAndEnvelopeRejectDuplicateHeadersEncodingAndWrongPath(t *testing.T) {
	f := newPrincipalFixture(t)
	registrar := &testPrincipalRegistrar{}
	RegisterPrincipalCheckRoute(registrar, f.verifier)
	if registrar.method != "POST" || registrar.path != PrincipalCheckRoutePath || registrar.handler == nil {
		t.Fatalf("registrar=%+v", registrar)
	}
	body := f.requestBody("agent-client")
	for i, tc := range []struct {
		name, path, encoding string
		duplicate            bool
		want                 int
	}{
		{"duplicate", PrincipalCheckRoutePath, "", true, http.StatusUnauthorized},
		{"encoding", PrincipalCheckRoutePath, "gzip", false, http.StatusUnsupportedMediaType},
		{"path", "/team/v1/other", "", false, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jti := fmt.Sprintf("envelope-%d", i)
			assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims(jti, "agent-client", body))
			r := httptest.NewRequest("POST", tc.path, bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Content-Encoding", tc.encoding)
			r.Header.Set("X-Pulse-Principal", assertion)
			r.Header.Set("X-Pulse-Request-ID", "req-"+jti)
			if tc.duplicate {
				r.Header.Add("X-Pulse-Principal", assertion)
			}
			w := httptest.NewRecorder()
			registrar.handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPrincipalStrictJSONRejectsDuplicateObjectKeys(t *testing.T) {
	f := newPrincipalFixture(t)
	body := fmt.Sprintf(`{"oauth_issuer":%q,"oauth_subject":%q,"oauth_subject":%q,"oauth_client_id":"agent-client","capabilities":["pulse:connect"]}`, f.issuer, f.subject, f.subject)
	r := httptest.NewRequest("POST", PrincipalCheckRoutePath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	NewPrincipalCheckHandler(f.verifier).ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate-key status=%d body=%s", w.Code, w.Body.String())
	}
}

func signPrincipalAssertion(t *testing.T, private ed25519.PrivateKey, kid string, headerExtra map[string]any, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "EdDSA", "typ": "pulse.principal.v1", "kid": kid}
	for key, value := range headerExtra {
		header[key] = value
	}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	return input + "." + enc.EncodeToString(ed25519.Sign(private, []byte(input)))
}
