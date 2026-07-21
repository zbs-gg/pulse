package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/consolidation"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/userpresence"
)

type homeBindingVerifierStub struct {
	err   error
	calls int
}

func (value *homeBindingVerifierStub) Verify(_ context.Context, _, _ string) error {
	value.calls++
	return value.err
}

type warmingProductTestEmbedder struct{ productTestEmbedder }

func (warmingProductTestEmbedder) Ready() bool { return false }

func TestMemoryHomeConsolidationStartUsesExistingSessionAndCSRFBoundary(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	homeDir := t.TempDir()
	inventory, err := consolidation.NewEngine(consolidation.EngineConfig{
		Manager: srv.consolidationReports, HomeDir: homeDir,
		CanonicalPath: vault.DBPath(), CanonicalDB: vault.DB(),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.consolidationInventory = inventory
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}

	missingCSRF := httptest.NewRecorder()
	srv.Handler().ServeHTTP(missingCSRF, homeMutationRequest(srv, session, "consolidation/start", url.Values{}))
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("consolidation start without CSRF status=%d body=%s", missingCSRF.Code, missingCSRF.Body.String())
	}

	started := httptest.NewRecorder()
	srv.Handler().ServeHTTP(started, homeMutationRequest(srv, session, "consolidation/start", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
	}))
	if started.Code != http.StatusNoContent {
		t.Fatalf("consolidation start status=%d body=%s", started.Code, started.Body.String())
	}
	report, err := srv.consolidationReports.Latest(consolidation.Destination{
		StoreKind: string(vault.StoreKind()), StoreID: vault.StoreID(),
		BindingDigest: strings.Repeat("a", 64), RepositoryID: "repository_pulse",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Phase != consolidation.PhaseReportReady || report.Totals.Unique != 0 {
		t.Fatalf("unexpected content-free Home report: %#v", report)
	}
}

type homeEnhancedPresenceAuthorizerStub struct {
	profile userpresence.EnhancedPresenceProfile
}

type homeEnhancedPresenceProver struct {
	calls int
}

func (prover *homeEnhancedPresenceProver) Prove(context.Context, userpresence.Challenge) error {
	prover.calls++
	return nil
}

func (stub homeEnhancedPresenceAuthorizerStub) Profile() userpresence.EnhancedPresenceProfile {
	return stub.profile
}

func (homeEnhancedPresenceAuthorizerStub) Begin(context.Context, userpresence.ProtectedActionTargetV1) (userpresence.EnhancedPresenceCeremonyV1, error) {
	return userpresence.EnhancedPresenceCeremonyV1{}, userpresence.ErrEnhancedActionUnavailable
}

func (homeEnhancedPresenceAuthorizerStub) Complete(context.Context, userpresence.EnhancedPresenceCompletionV1) (userpresence.EnhancedPresenceAssertionV1, error) {
	return userpresence.EnhancedPresenceAssertionV1{}, userpresence.ErrEnhancedActionUnavailable
}

func TestMemoryHomeReportsExactEnhancedPresenceProfileWithoutGrantingAuthority(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}

	defaultPage := httptest.NewRecorder()
	srv.Handler().ServeHTTP(defaultPage, homePageRequest(srv, session))
	if defaultPage.Code != http.StatusOK {
		t.Fatalf("default Home status=%d body=%s", defaultPage.Code, defaultPage.Body.String())
	}
	for _, want := range []string{
		"pulse.enhanced_presence.profile.v1", "unavailable", "enhanced_presence_unavailable",
		"No protected actions can be authorized on this machine.",
	} {
		if !strings.Contains(defaultPage.Body.String(), want) {
			t.Fatalf("default Home profile missing %q: %s", want, defaultPage.Body.String())
		}
	}
	for _, forbidden := range []string{"<code>binding.change</code>", "<code>vault.wipe</code>"} {
		if strings.Contains(defaultPage.Body.String(), forbidden) {
			t.Fatalf("unavailable profile advertised %q", forbidden)
		}
	}
	if strings.Contains(defaultPage.Body.String(), `data-protected-wipe`) {
		t.Fatal("unavailable profile rendered an actionable protected wipe")
	}

	srv.cfg.EnhancedPresenceAuthorizer = homeEnhancedPresenceAuthorizerStub{profile: userpresence.EnhancedPresenceProfile{
		Schema: userpresence.EnhancedPresenceProfileSchemaV1, Version: 1,
		Kind: userpresence.EnhancedPresenceMacOSNative, Available: true,
		ProtectedActions: []userpresence.Action{userpresence.ActionBindingChange, userpresence.ActionVaultWipe},
	}}
	availablePage := httptest.NewRecorder()
	srv.Handler().ServeHTTP(availablePage, homePageRequest(srv, session))
	if availablePage.Code != http.StatusOK {
		t.Fatalf("available Home status=%d body=%s", availablePage.Code, availablePage.Body.String())
	}
	for _, want := range []string{
		"macos_native", "<code>binding.change</code>", "<code>vault.wipe</code>",
		`data-protected-wipe`, "Review exact stored records",
	} {
		if !strings.Contains(availablePage.Body.String(), want) {
			t.Fatalf("available Home profile missing %q: %s", want, availablePage.Body.String())
		}
	}
	if strings.Contains(availablePage.Body.String(), "No protected actions can be authorized on this machine.") {
		t.Fatal("available profile rendered the unavailable claim")
	}

	for _, path := range []string{"wipe", "binding/replace", "protected/authorize"} {
		response := httptest.NewRecorder()
		srv.Handler().ServeHTTP(response, homeMutationRequest(srv, session, path, url.Values{
			viewerSessionCSRFFormField: {session.CSRFToken},
		}))
		if response.Code != http.StatusNotFound {
			t.Fatalf("reported capability accidentally registered protected route %q: status=%d", path, response.Code)
		}
	}
}

func TestMemoryHomeOrdinarySessionNeedsNoNativePresenceAndGrantsNoProtectedRoute(t *testing.T) {
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "personal.db"), store.StoreKindPersonal, "store_personal_home_presence_required")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	binding := strings.Repeat("a", 64)
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_pulse"); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		IPCSecret: "home-test-daemon-secret", Store: vault, HomeOrigin: testViewerSessionOrigin,
		HomeBindingVerifier: &homeBindingVerifierStub{},
	})
	if err != nil {
		t.Fatalf("ordinary Memory Home must not require native presence: %v", err)
	}
	issued := issueHomeSession(t, srv, testHomeSessionRequestBody("personal_live_ready", "2026-07-16T08:00:00Z"))
	if issued.Code != http.StatusOK {
		t.Fatalf("ordinary Home session status=%d body=%s", issued.Code, issued.Body.String())
	}
	handoff := decodeHomeSessionResponse(t, issued)
	for _, path := range []string{"wipe", "binding/replace", "protected/authorize"} {
		request := httptest.NewRequest(http.MethodPost, handoff.TargetURL+path, nil)
		request.Host = srv.homeSessions.expectedHost
		request.RemoteAddr = "127.0.0.1:54321"
		request.AddCookie(&http.Cookie{Name: handoff.CookieName, Value: handoff.CookieValue, Path: handoff.CookiePath})
		response := httptest.NewRecorder()
		srv.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("ordinary Home session reached protected route %q: status=%d", path, response.Code)
		}
	}
}

func TestHomeProtectedWipeBindsTheExactSnapshotAndConsumesPresenceOnce(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	seedHomeProtectedWipeCapsule(t, vault, "capsule_protected", "Private content must never enter the protected receipt.")
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	prover := &homeEnhancedPresenceProver{}
	gate, err := userpresence.NewGate(prover, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := userpresence.NewSynchronousGateAuthorizer(
		gate, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x51}, 256)),
	)
	if err != nil {
		t.Fatal(err)
	}
	srv.cfg.EnhancedPresenceAuthorizer = authorizer
	srv.homeSessions.clock = func() time.Time { return now }
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}

	begin := httptest.NewRecorder()
	srv.Handler().ServeHTTP(begin, homeMutationRequest(srv, session, "protected/wipe/begin", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
	}))
	if begin.Code != http.StatusOK {
		t.Fatalf("protected wipe begin status=%d body=%s", begin.Code, begin.Body.String())
	}
	if strings.Contains(begin.Body.String(), "Private content") {
		t.Fatalf("protected wipe begin leaked memory content: %s", begin.Body.String())
	}
	var started homeProtectedWipeBeginResponse
	if err := json.Unmarshal(begin.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Schema != homeProtectedWipeBeginSchemaV1 || started.AffectedDataCount == 0 ||
		len(started.CeremonyID) != 64 || len(started.TargetDigest) != 64 {
		t.Fatalf("invalid protected wipe begin response: %#v", started)
	}

	completeForm := url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"ceremony_id":              {started.CeremonyID},
	}
	completed := httptest.NewRecorder()
	srv.Handler().ServeHTTP(completed, homeMutationRequest(srv, session, "protected/wipe/complete", completeForm))
	if completed.Code != http.StatusOK {
		t.Fatalf("protected wipe complete status=%d body=%s", completed.Code, completed.Body.String())
	}
	var receipt store.ProductMemoryWipeReceiptV1
	if err := json.Unmarshal(completed.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Schema != store.ProductMemoryWipeReceiptSchemaV1 || receipt.SnapshotDigest != started.AffectedDataDigest {
		t.Fatalf("invalid protected wipe receipt: %#v", receipt)
	}
	if prover.calls != 1 {
		t.Fatalf("presence proof calls=%d", prover.calls)
	}
	var remaining int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("protected wipe left %d capsules", remaining)
	}

	replay := httptest.NewRecorder()
	srv.Handler().ServeHTTP(replay, homeMutationRequest(srv, session, "protected/wipe/complete", completeForm))
	if replay.Code != http.StatusConflict || prover.calls != 1 {
		t.Fatalf("protected wipe replay status=%d presence_calls=%d", replay.Code, prover.calls)
	}
}

func TestHomeProtectedWipeRefusesDataAddedAfterTheHumanSnapshot(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	seedHomeProtectedWipeCapsule(t, vault, "capsule_before", "Approved set.")
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	prover := &homeEnhancedPresenceProver{}
	gate, _ := userpresence.NewGate(prover, func() time.Time { return now })
	authorizer, _ := userpresence.NewSynchronousGateAuthorizer(
		gate, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x52}, 256)),
	)
	srv.cfg.EnhancedPresenceAuthorizer = authorizer
	srv.homeSessions.clock = func() time.Time { return now }
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}

	begin := httptest.NewRecorder()
	srv.Handler().ServeHTTP(begin, homeMutationRequest(srv, session, "protected/wipe/begin", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
	}))
	var started homeProtectedWipeBeginResponse
	if begin.Code != http.StatusOK || json.Unmarshal(begin.Body.Bytes(), &started) != nil {
		t.Fatalf("protected wipe begin status=%d body=%s", begin.Code, begin.Body.String())
	}
	seedHomeProtectedWipeCapsule(t, vault, "capsule_after", "This was never approved.")

	completed := httptest.NewRecorder()
	srv.Handler().ServeHTTP(completed, homeMutationRequest(srv, session, "protected/wipe/complete", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "ceremony_id": {started.CeremonyID},
	}))
	if completed.Code != http.StatusConflict || !strings.Contains(completed.Body.String(), "changed") {
		t.Fatalf("stale protected wipe status=%d body=%s", completed.Code, completed.Body.String())
	}
	if prover.calls != 1 {
		t.Fatalf("presence proof calls=%d", prover.calls)
	}
	var remaining int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("stale protected wipe deleted data; capsules=%d", remaining)
	}
}

func TestHomeProtectedWipeStaysUnavailableWithoutAnEnhancedAdapter(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	seedHomeProtectedWipeCapsule(t, vault, "capsule_safe", "Must remain without an enhanced adapter.")
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, homeMutationRequest(srv, session, "protected/wipe/begin", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
	}))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "unavailable") {
		t.Fatalf("unavailable protected wipe status=%d body=%s", response.Code, response.Body.String())
	}
	var remaining int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("unavailable protected wipe changed data; capsules=%d", remaining)
	}
}

func TestHomeProtectedWipeIsBoundToTheBeginningHomeSession(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	seedHomeProtectedWipeCapsule(t, vault, "capsule_session_bound", "Bound to the first Home session.")
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	prover := &homeEnhancedPresenceProver{}
	gate, _ := userpresence.NewGate(prover, func() time.Time { return now })
	authorizer, _ := userpresence.NewSynchronousGateAuthorizer(
		gate, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x53}, 256)),
	)
	srv.cfg.EnhancedPresenceAuthorizer = authorizer
	srv.homeSessions.clock = func() time.Time { return now }
	first, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	second, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	begin := httptest.NewRecorder()
	srv.Handler().ServeHTTP(begin, homeMutationRequest(srv, first, "protected/wipe/begin", url.Values{
		viewerSessionCSRFFormField: {first.CSRFToken},
	}))
	var started homeProtectedWipeBeginResponse
	if begin.Code != http.StatusOK || json.Unmarshal(begin.Body.Bytes(), &started) != nil {
		t.Fatalf("protected wipe begin status=%d body=%s", begin.Code, begin.Body.String())
	}

	wrongSession := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wrongSession, homeMutationRequest(srv, second, "protected/wipe/complete", url.Values{
		viewerSessionCSRFFormField: {second.CSRFToken}, "ceremony_id": {started.CeremonyID},
	}))
	if wrongSession.Code != http.StatusConflict || prover.calls != 0 {
		t.Fatalf("wrong-session completion status=%d presence_calls=%d", wrongSession.Code, prover.calls)
	}
	completed := httptest.NewRecorder()
	srv.Handler().ServeHTTP(completed, homeMutationRequest(srv, first, "protected/wipe/complete", url.Values{
		viewerSessionCSRFFormField: {first.CSRFToken}, "ceremony_id": {started.CeremonyID},
	}))
	if completed.Code != http.StatusOK || prover.calls != 1 {
		t.Fatalf("bound-session completion status=%d presence_calls=%d body=%s", completed.Code, prover.calls, completed.Body.String())
	}
}

func TestHomeProtectedWipeExpiresWithoutInvokingPresence(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	seedHomeProtectedWipeCapsule(t, vault, "capsule_expiry", "Must survive an expired ceremony.")
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	prover := &homeEnhancedPresenceProver{}
	gate, _ := userpresence.NewGate(prover, func() time.Time { return now })
	authorizer, _ := userpresence.NewSynchronousGateAuthorizer(
		gate, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x54}, 256)),
	)
	srv.cfg.EnhancedPresenceAuthorizer = authorizer
	srv.homeSessions.clock = func() time.Time { return now }
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	begin := httptest.NewRecorder()
	srv.Handler().ServeHTTP(begin, homeMutationRequest(srv, session, "protected/wipe/begin", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
	}))
	var started homeProtectedWipeBeginResponse
	if begin.Code != http.StatusOK || json.Unmarshal(begin.Body.Bytes(), &started) != nil {
		t.Fatalf("protected wipe begin status=%d body=%s", begin.Code, begin.Body.String())
	}
	now = now.Add(91 * time.Second)
	expired := httptest.NewRecorder()
	srv.Handler().ServeHTTP(expired, homeMutationRequest(srv, session, "protected/wipe/complete", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "ceremony_id": {started.CeremonyID},
	}))
	if expired.Code != http.StatusGone || prover.calls != 0 {
		t.Fatalf("expired completion status=%d presence_calls=%d body=%s", expired.Code, prover.calls, expired.Body.String())
	}
	var remaining int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM memory_capsules`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("expired protected wipe changed data; capsules=%d", remaining)
	}
}

func TestHomeRouterIsolatedFromIPCAndCORSAndRendersRealReadModel(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}

	request := homePageRequest(srv, session)
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Home status=%d body=%s", response.Code, response.Body.String())
	}
	for _, want := range []string{"Pulse is connected", "Personal memory", "No estimate yet", "What the next task receives"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("Home body missing %q", want)
		}
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" || response.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("Home inherited CORS authority: %#v", response.Header())
	}
	assertHomeHeaders(t, response.Header())
	csp := response.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"script-src 'self'", "style-src 'unsafe-inline'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("Home CSP missing %q: %q", directive, csp)
		}
	}
	for _, forbidden := range []string{"home-test-daemon-secret", "key=", "X-Pulse-Key", "Authorization"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("Home HTML exposed %q", forbidden)
		}
	}
	asset := httptest.NewRequest(http.MethodGet, testViewerSessionOrigin+viewerSessionRoutePath(session.RouteScope)+"assets/home.js", nil)
	asset.Host = srv.homeSessions.expectedHost
	asset.RemoteAddr = "127.0.0.1:54321"
	asset.AddCookie(srv.homeSessions.Cookie(session))
	assetResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(assetResponse, asset)
	if assetResponse.Code != http.StatusOK || assetResponse.Body.String() != memoryHomeBrowserScript {
		t.Fatalf("scoped Home asset status=%d", assetResponse.Code)
	}

	withIPC := homePageRequest(srv, session)
	withIPC.Header.Set("X-Pulse-Key", "home-test-daemon-secret")
	blocked := httptest.NewRecorder()
	srv.Handler().ServeHTTP(blocked, withIPC)
	if blocked.Code != http.StatusUnauthorized {
		t.Fatalf("IPC header crossed Home boundary: status=%d", blocked.Code)
	}
	assertHomeHeaders(t, blocked.Header())
}

func TestHomeUnassignedCardHasZeroInfluenceUntilExactProjectAssignment(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	inboxPath, itemID, contentDigest := writeHomeUnassignedInbox(t)
	srv.cfg.UnassignedInboxPath = inboxPath
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}

	page := httptest.NewRecorder()
	srv.Handler().ServeHTTP(page, homePageRequest(srv, session))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Unassigned Inbox") ||
		!strings.Contains(page.Body.String(), "Assign this only to an exact project.") ||
		!strings.Contains(page.Body.String(), "Not counted as memory") {
		t.Fatalf("unassigned card not rendered truthfully: status=%d body=%s", page.Code, page.Body.String())
	}
	var active, pending int
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM private_memory_objects WHERE lifecycle='active'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates WHERE state='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if active != 0 || pending != 0 {
		t.Fatalf("unassigned card influenced Vault before assignment: active=%d pending=%d", active, pending)
	}

	form := url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"content_digest":           {contentDigest},
		"expected_binding_digest":  {strings.Repeat("a", 64)},
	}
	if err := vault.ConfigureProductRuntimeAuthority(strings.Repeat("b", 64), 1, 2); err != nil {
		t.Fatal(err)
	}
	stale := httptest.NewRecorder()
	srv.Handler().ServeHTTP(stale, homeMutationRequest(srv, session, "unassigned/"+itemID+"/assign", form))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "binding changed") {
		t.Fatalf("stale binding status=%d body=%s", stale.Code, stale.Body.String())
	}
	if err := vault.ConfigureProductRuntimeAuthority(strings.Repeat("a", 64), 1, 1); err != nil {
		t.Fatal(err)
	}
	assigned := httptest.NewRecorder()
	srv.Handler().ServeHTTP(assigned, homeMutationRequest(srv, session, "unassigned/"+itemID+"/assign", form))
	if assigned.Code != http.StatusNoContent {
		t.Fatalf("assign status=%d body=%s", assigned.Code, assigned.Body.String())
	}
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM private_memory_objects WHERE lifecycle='active'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates WHERE state='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if active != 0 || pending != 1 {
		t.Fatalf("assignment bypassed ordinary Tray: active=%d pending=%d", active, pending)
	}
	var provenanceHost string
	if err := vault.DB().QueryRow(`SELECT provenance_host FROM memory_write_receipts ORDER BY created_at DESC, receipt_id DESC LIMIT 1`).Scan(&provenanceHost); err != nil {
		t.Fatal(err)
	}
	if provenanceHost != "codex" {
		t.Fatalf("assignment lost harness provenance: %q", provenanceHost)
	}

	retry := httptest.NewRecorder()
	srv.Handler().ServeHTTP(retry, homeMutationRequest(srv, session, "unassigned/"+itemID+"/assign", form))
	if retry.Code != http.StatusNoContent {
		t.Fatalf("idempotent assign retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates WHERE state='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("assign retry duplicated Tray candidates: %d", pending)
	}
	completedPage := httptest.NewRecorder()
	srv.Handler().ServeHTTP(completedPage, homePageRequest(srv, session))
	if completedPage.Code != http.StatusOK ||
		!strings.Contains(completedPage.Body.String(), "Moved to this project’s Tray") ||
		!strings.Contains(completedPage.Body.String(), "No unassigned memories") {
		t.Fatalf("assignment receipt not visible: status=%d body=%s", completedPage.Code, completedPage.Body.String())
	}
}

func TestHomeUnassignedDeleteKeepsAVisibleTerminalReceipt(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	inboxPath, itemID, contentDigest := writeHomeUnassignedInbox(t)
	srv.cfg.UnassignedInboxPath = inboxPath
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"content_digest":           {contentDigest},
		"expected_binding_digest":  {strings.Repeat("a", 64)},
	}
	deleted := httptest.NewRecorder()
	srv.Handler().ServeHTTP(deleted, homeMutationRequest(srv, session, "unassigned/"+itemID+"/delete", form))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	page := httptest.NewRecorder()
	srv.Handler().ServeHTTP(page, homePageRequest(srv, session))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Deleted from Inbox") ||
		!strings.Contains(page.Body.String(), "No unassigned memories") {
		t.Fatalf("delete receipt not visible: status=%d body=%s", page.Code, page.Body.String())
	}
}

func TestHomeRejectedUnassignedAssignmentLeavesTheCardInInbox(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	inboxPath, itemID, contentDigest := writeHomeUnassignedInboxWithSummary(t, strings.Repeat("я", 601))
	srv.cfg.UnassignedInboxPath = inboxPath
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"content_digest":           {contentDigest},
		"expected_binding_digest":  {strings.Repeat("a", 64)},
	}
	rejected := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rejected, homeMutationRequest(srv, session, "unassigned/"+itemID+"/assign", form))
	if rejected.Code != http.StatusUnprocessableEntity || !strings.Contains(rejected.Body.String(), "remains in Inbox") {
		t.Fatalf("rejected assignment status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	var pending int
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM memory_tray_candidates WHERE state='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("rejected assignment created pending Tray rows: %d", pending)
	}
	page := httptest.NewRecorder()
	srv.Handler().ServeHTTP(page, homePageRequest(srv, session))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), strings.Repeat("я", 40)) {
		t.Fatalf("rejected card disappeared: status=%d body=%s", page.Code, page.Body.String())
	}
}

func TestHomeFailsClosedWhenTheSignedWorkspaceBindingIsRevoked(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	verifier := srv.cfg.HomeBindingVerifier.(*homeBindingVerifierStub)
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	verifier.err = errors.New("binding revoked")
	page := httptest.NewRecorder()
	srv.Handler().ServeHTTP(page, homePageRequest(srv, session))
	if page.Code != http.StatusConflict || !strings.Contains(page.Body.String(), "binding changed") {
		t.Fatalf("revoked page status=%d body=%s", page.Code, page.Body.String())
	}
	mutation := httptest.NewRecorder()
	srv.Handler().ServeHTTP(mutation, homeMutationRequest(srv, session, "tray/candidate_deadbeef/commit", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken}, "expected_version": {"1"},
	}))
	if mutation.Code != http.StatusConflict || !strings.Contains(mutation.Body.String(), "binding changed") {
		t.Fatalf("revoked mutation status=%d body=%s", mutation.Code, mutation.Body.String())
	}
}

func TestHomeRejectsAmbientAndWrongSessionRoutesEvenWithStolenCookie(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	first, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	second, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}

	ambient := httptest.NewRequest(http.MethodGet, testViewerSessionOrigin+"/home", nil)
	ambient.Host = srv.homeSessions.expectedHost
	ambient.RemoteAddr = "127.0.0.1:54321"
	ambient.AddCookie(srv.homeSessions.Cookie(first))
	ambientResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ambientResponse, ambient)
	if ambientResponse.Code == http.StatusOK {
		t.Fatal("ambient /home accepted browser authority")
	}

	wrongRoute := httptest.NewRequest(http.MethodGet, testViewerSessionOrigin+viewerSessionRoutePath(second.RouteScope), nil)
	wrongRoute.Host = srv.homeSessions.expectedHost
	wrongRoute.RemoteAddr = "127.0.0.1:54321"
	wrongRoute.AddCookie(srv.homeSessions.Cookie(first))
	wrongResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wrongResponse, wrongRoute)
	if wrongResponse.Code != http.StatusUnauthorized {
		t.Fatalf("stolen cookie on wrong route status=%d", wrongResponse.Code)
	}

	routeOnly := httptest.NewRequest(http.MethodGet, testViewerSessionOrigin+viewerSessionRoutePath(first.RouteScope), nil)
	routeOnly.Host = srv.homeSessions.expectedHost
	routeOnly.RemoteAddr = "127.0.0.1:54321"
	routeOnlyResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(routeOnlyResponse, routeOnly)
	if routeOnlyResponse.Code != http.StatusUnauthorized {
		t.Fatalf("route alone status=%d", routeOnlyResponse.Code)
	}
}

func TestHomeSessionIssueReturnsBoundedCookieHandoffWithoutSettingBrowserAuthority(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	request := httptest.NewRequest(
		http.MethodPost,
		testViewerSessionOrigin+"/home/session",
		strings.NewReader(testHomeSessionRequestBody("personal_live_ready", "2026-07-16T08:00:00Z")),
	)
	request.Host = srv.homeSessions.expectedHost
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Pulse-Key", "home-test-daemon-secret")
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("session issue status=%d body=%s", response.Code, response.Body.String())
	}
	var handoff homeSessionResponse
	if err := json.NewDecoder(response.Body).Decode(&handoff); err != nil {
		t.Fatal(err)
	}
	routeScope, routeOK := viewerSessionRouteFromPath(handoff.CookiePath)
	if handoff.CookieName != viewerSessionCookieName || !routeOK || handoff.CookiePath != viewerSessionRoutePath(routeScope) ||
		!validViewerSessionRandomValue(handoff.CookieValue) || handoff.MaxAgeSeconds <= 0 ||
		handoff.TargetURL != testViewerSessionOrigin+handoff.CookiePath || strings.ContainsAny(handoff.TargetURL, "?#") ||
		strings.Contains(handoff.TargetURL, handoff.CookieValue) {
		t.Fatalf("unsafe Home handoff: %#v", handoff)
	}
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("daemon session endpoint set browser cookie directly")
	}
	if strings.Contains(response.Body.String(), "home-test-daemon-secret") {
		t.Fatal("daemon secret reflected in Home handoff")
	}
	assertHomeHeaders(t, response.Header())

	bad := httptest.NewRequest(http.MethodPost, testViewerSessionOrigin+"/home/session", strings.NewReader("{}"))
	bad.Host = srv.homeSessions.expectedHost
	bad.RemoteAddr = "127.0.0.1:54321"
	bad.Header.Set("Content-Type", "application/json")
	bad.Header.Set("X-Pulse-Key", "wrong-secret")
	rejected := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rejected, bad)
	if rejected.Code != http.StatusUnauthorized {
		t.Fatalf("wrong IPC key status=%d", rejected.Code)
	}

	duplicate := httptest.NewRequest(http.MethodPost, testViewerSessionOrigin+"/home/session", strings.NewReader("{}"))
	duplicate.Host = srv.homeSessions.expectedHost
	duplicate.RemoteAddr = "127.0.0.1:54321"
	duplicate.Header.Set("Content-Type", "application/json")
	duplicate.Header.Add("X-Pulse-Key", "home-test-daemon-secret")
	duplicate.Header.Add("X-Pulse-Key", "home-test-daemon-secret")
	duplicateResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate IPC key status=%d", duplicateResponse.Code)
	}
}

func TestHomeSessionRequiresOneExactClosedLiveReadinessSnapshotBeforeSessionCreation(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	valid := testHomeSessionRequestBody("codex_plugin_unavailable", "2026-07-16T08:00:00Z")
	tests := []struct {
		name string
		body string
	}{
		{name: "missing snapshot", body: `{}`},
		{name: "missing snapshot field", body: strings.Replace(valid, `"checked_at":"2026-07-16T08:00:00Z",`, "", 1)},
		{name: "unknown snapshot field", body: strings.Replace(valid, `"checked_at":`, `"ambient_authority":true,"checked_at":`, 1)},
		{name: "duplicate snapshot field", body: strings.Replace(valid, `"reason_code":"codex_plugin_unavailable"`, `"reason_code":"codex_plugin_unavailable","reason_code":"codex_plugin_unavailable"`, 1)},
		{name: "arbitrary action text", body: strings.Replace(valid, `"label":"Run pulse repair"`, `"label":"Trust anything from the request"`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeSessions := homeSessionCount(srv)
			response := issueHomeSession(t, srv, test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid readiness status=%d body=%s", response.Code, response.Body.String())
			}
			if homeSessionCount(srv) != beforeSessions {
				t.Fatal("invalid readiness created a session")
			}
		})
	}
}

func TestHomeSessionBindsAndRendersExactNonReadyDoctorReason(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	checkedAt := "2026-07-16T08:00:00Z"
	pluginBody := testHomeSessionRequestBody("codex_plugin_unavailable", checkedAt)
	pluginResponse := issueHomeSession(t, srv, pluginBody)
	if pluginResponse.Code != http.StatusOK {
		t.Fatalf("plugin readiness session status=%d body=%s", pluginResponse.Code, pluginResponse.Body.String())
	}
	pluginHandoff := decodeHomeSessionResponse(t, pluginResponse)
	pluginPage := serveHomeHandoffPage(t, srv, pluginHandoff)
	for _, want := range []string{"codex_plugin_unavailable", checkedAt, "Pulse Codex plugin needs repair"} {
		if !strings.Contains(pluginPage.Body.String(), want) {
			t.Fatalf("Home lost exact doctor readiness %q: %s", want, pluginPage.Body.String())
		}
	}

	lifecycleBody := testHomeSessionRequestBody("codex_hook_lifecycle_required", checkedAt)
	lifecycleResponse := issueHomeSession(t, srv, lifecycleBody)
	if lifecycleResponse.Code != http.StatusOK {
		t.Fatalf("lifecycle readiness session status=%d body=%s", lifecycleResponse.Code, lifecycleResponse.Body.String())
	}
	lifecyclePage := serveHomeHandoffPage(t, srv, decodeHomeSessionResponse(t, lifecycleResponse))
	if !strings.Contains(lifecyclePage.Body.String(), "codex_hook_lifecycle_required") ||
		strings.Contains(lifecyclePage.Body.String(), "codex_plugin_unavailable") {
		t.Fatalf("Home lifecycle parity failed: %s", lifecyclePage.Body.String())
	}
}

func TestHomeNeverPromotesNonReadySnapshotAndDowngradesReadyWhenRetrievalDisappears(t *testing.T) {
	srv, _ := newHomeRouteFixture(t)
	checkedAt := "2026-07-16T08:00:00Z"
	nonReady := issueHomeSession(t, srv, testHomeSessionRequestBody("codex_hook_lifecycle_required", checkedAt))
	if nonReady.Code != http.StatusOK {
		t.Fatalf("non-ready session status=%d body=%s", nonReady.Code, nonReady.Body.String())
	}
	nonReadyPage := serveHomeHandoffPage(t, srv, decodeHomeSessionResponse(t, nonReady))
	if !strings.Contains(nonReadyPage.Body.String(), "codex_hook_lifecycle_required") ||
		strings.Contains(nonReadyPage.Body.String(), "personal_live_ready") {
		t.Fatalf("healthy retrieval promoted supplied non-ready snapshot: %s", nonReadyPage.Body.String())
	}

	srv.cfg.Retrieval = nil
	ready := issueHomeSession(t, srv, testHomeSessionRequestBody("personal_live_ready", checkedAt))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready session status=%d body=%s", ready.Code, ready.Body.String())
	}
	readyPage := serveHomeHandoffPage(t, srv, decodeHomeSessionResponse(t, ready))
	if !strings.Contains(readyPage.Body.String(), "full_retrieval_unavailable") ||
		strings.Contains(readyPage.Body.String(), "personal_live_ready") || strings.Contains(readyPage.Body.String(), checkedAt) {
		t.Fatalf("server failed to downgrade disappeared retrieval: %s", readyPage.Body.String())
	}

	srv.cfg.Retrieval = retrieve.New(retrieve.Config{Store: srv.cfg.Store, Embedder: warmingProductTestEmbedder{}})
	warming := issueHomeSession(t, srv, testHomeSessionRequestBody("personal_live_ready", checkedAt))
	if warming.Code != http.StatusOK {
		t.Fatalf("warming session status=%d body=%s", warming.Code, warming.Body.String())
	}
	warmingPage := serveHomeHandoffPage(t, srv, decodeHomeSessionResponse(t, warming))
	if !strings.Contains(warmingPage.Body.String(), "local_embedder_warming") ||
		strings.Contains(warmingPage.Body.String(), "personal_live_ready") || strings.Contains(warmingPage.Body.String(), checkedAt) {
		t.Fatalf("server failed to downgrade warming retrieval: %s", warmingPage.Body.String())
	}
}
func TestHomePresentationCreatesExactReceiptAndGETCannotMutate(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	pending := newHomeRoutePending(t, vault, "presentation", "Show the exact memory before saving it.")
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}

	page := httptest.NewRecorder()
	srv.Handler().ServeHTTP(page, homePageRequest(srv, session))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Show the exact memory before saving it.") ||
		!strings.Contains(page.Body.String(), `src="assets/home.js"`) {
		t.Fatalf("pending card was not rendered: status=%d body=%s", page.Code, page.Body.String())
	}
	for _, forbidden := range []string{`action="/home/`, `src="/home/`, session.ID, session.RouteScope} {
		if strings.Contains(page.Body.String(), forbidden) {
			t.Fatalf("Home page exposed ambient authority %q", forbidden)
		}
	}

	form := url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"candidate_id":             {pending.CandidateID},
		"expected_version":         {strconv.Itoa(pending.Version)},
	}
	present := homeMutationRequest(srv, session, "present", form)
	presented := httptest.NewRecorder()
	srv.Handler().ServeHTTP(presented, present)
	if presented.Code != http.StatusNoContent {
		t.Fatalf("present status=%d body=%s", presented.Code, presented.Body.String())
	}
	var presentationCount int
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM memory_presentation_receipts WHERE candidate_id=?`, pending.CandidateID).Scan(&presentationCount); err != nil {
		t.Fatal(err)
	}
	if presentationCount != 1 {
		t.Fatalf("presentation receipts=%d, want 1", presentationCount)
	}

	get := httptest.NewRequest(http.MethodGet, testViewerSessionOrigin+viewerSessionRoutePath(session.RouteScope)+"present", nil)
	get.Host = srv.homeSessions.expectedHost
	get.RemoteAddr = "127.0.0.1:54321"
	get.AddCookie(srv.homeSessions.Cookie(session))
	getResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET presentation status=%d", getResponse.Code)
	}
	if err := vault.DB().QueryRow(`SELECT COUNT(*) FROM memory_presentation_receipts WHERE candidate_id=?`, pending.CandidateID).Scan(&presentationCount); err != nil {
		t.Fatal(err)
	}
	if presentationCount != 1 {
		t.Fatal("GET mutated presentation receipts")
	}
	assertHomeHeaders(t, getResponse.Header())
}

func TestHomeRoutePresentationGraceAndCommitCreatesCanonicalReceipt(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	pending := newHomeRoutePending(t, vault, "route_commit", "Commit only after the exact visible delay.")
	clock := &viewerSessionTestClock{now: time.Now().UTC()}
	srv.homePresentation.clock = clock.Now
	srv.homeSessions.clock = clock.Now
	srv.homePresentation.schedule = func(store.MemoryPresentationReceipt, time.Duration) {}
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"candidate_id":             {pending.CandidateID},
		"expected_version":         {strconv.Itoa(pending.Version)},
	}
	presented := httptest.NewRecorder()
	srv.Handler().ServeHTTP(presented, homeMutationRequest(srv, session, "present", form))
	if presented.Code != http.StatusNoContent {
		t.Fatalf("present status=%d body=%s", presented.Code, presented.Body.String())
	}

	commitForm := url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"expected_version":         {strconv.Itoa(pending.Version)},
	}
	immediate := httptest.NewRecorder()
	srv.Handler().ServeHTTP(immediate, homeMutationRequest(srv, session, "tray/"+pending.CandidateID+"/commit", commitForm))
	if immediate.Code != http.StatusTooEarly {
		t.Fatalf("immediate commit status=%d body=%s", immediate.Code, immediate.Body.String())
	}

	clock.Advance(31 * time.Second)
	committed := httptest.NewRecorder()
	srv.Handler().ServeHTTP(committed, homeMutationRequest(srv, session, "tray/"+pending.CandidateID+"/commit", commitForm))
	if committed.Code != http.StatusNoContent {
		t.Fatalf("post-grace commit status=%d body=%s", committed.Code, committed.Body.String())
	}
	canonical := homeRouteCandidate(t, vault, pending.CandidateID)
	if canonical.State != "committed" || canonical.CanonicalObjectID == "" ||
		canonical.LatestReceipt.ReceiptID == "" || canonical.LatestReceipt.ObjectID != canonical.CanonicalObjectID ||
		canonical.LatestReceipt.Status != store.MemoryWriteCreated {
		t.Fatalf("route commit lacks canonical terminal proof: %#v", canonical)
	}
}

func TestMemoryHomeBrowserScriptPresentsOnlyVisibleCardsAfterPaint(t *testing.T) {
	for _, want := range []string{
		"const maxConcurrentPresentations = 4;",
		"new IntersectionObserver((entries) => {",
		"entry.isIntersecting && entry.intersectionRatio > 0",
		"document.visibilityState !== \"visible\"",
		"requestAnimationFrame(() => requestAnimationFrame(() => {",
		"observer.observe(card)",
	} {
		if !strings.Contains(memoryHomeBrowserScript, want) {
			t.Errorf("Home browser script missing visible-card contract %q", want)
		}
	}
	for _, forbidden := range []string{
		"for (let index = 0; index < cards.length; index += 4)",
		"Promise.all(",
	} {
		if strings.Contains(memoryHomeBrowserScript, forbidden) {
			t.Errorf("Home browser script still acknowledges rendered cards indiscriminately via %q", forbidden)
		}
	}
}

func TestMemoryHomeBrowserScriptIsolatesPresentationAndMutationFailures(t *testing.T) {
	for _, want := range []string{
		"Review delay did not start. Refresh Home to retry.",
		"Review delay could not be confirmed. Refresh Home to retry.",
		"present(card).finally(() => {",
		"activePresentations -= 1;",
		"if (!response.ok) {",
		"showMutationFailure(form, message);",
		"Action failed. Refresh Home and try again.",
		"window.confirm(form.dataset.homeConfirm)",
		"form.dataset.homePendingLabel",
		"mutationScope.dataset.homeMutationBusy",
		"buttons.forEach((button) => { button.disabled = true; })",
		"releaseMutation();",
	} {
		if !strings.Contains(memoryHomeBrowserScript, want) {
			t.Errorf("Home browser script missing failure-isolation contract %q", want)
		}
	}
}

func TestMemoryHomeBrowserScriptRequiresExplicitProtectedWipeCompletion(t *testing.T) {
	for _, want := range []string{
		`async function beginProtectedWipe(card)`,
		`async function completeProtectedWipe(card)`,
		`function cancelProtectedWipe(card)`,
		`post("protected/wipe/begin"`,
		`post("protected/wipe/complete"`,
		`beginButton.addEventListener("click", () => beginProtectedWipe(card))`,
		`completeButton.addEventListener("click", () => completeProtectedWipe(card))`,
		`cancelButton.addEventListener("click", () => cancelProtectedWipe(card))`,
		`response.status === 410`,
		`response.status === 409`,
		`countdownLive.textContent`,
		`review.focus()`,
		`beginButton.focus()`,
		`receipt.focus()`,
	} {
		if !strings.Contains(memoryHomeBrowserScript, want) {
			t.Errorf("Home browser script missing protected wipe contract %q", want)
		}
	}
	if strings.Count(memoryHomeBrowserScript, `completeProtectedWipe(card)`) != 2 {
		t.Fatal("protected wipe completion must have one definition and one explicit click binding")
	}
}

func TestHomeTrayEditThenCancelKeepsMemoryOutOfRecall(t *testing.T) {
	srv, vault := newHomeRouteFixture(t)
	pending := newHomeRoutePending(t, vault, "edit_cancel", "Keep the original private summary.")
	session, err := srv.homeSessions.Create(testViewerSessionReadiness())
	if err != nil {
		t.Fatal(err)
	}

	replacement := pending.Candidate
	replacement.Capsule.Items[0].RedactedSummary = "Keep only the edited private summary."
	replacementJSON, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	edit := homeMutationRequest(srv, session, "tray/"+pending.CandidateID+"/edit", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"expected_version":         {strconv.Itoa(pending.Version)},
		"candidate_json":           {string(replacementJSON)},
	})
	editResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(editResponse, edit)
	if editResponse.Code != http.StatusNoContent {
		t.Fatalf("edit status=%d body=%s", editResponse.Code, editResponse.Body.String())
	}

	edited := homeRouteCandidate(t, vault, pending.CandidateID)
	if edited.Version != pending.Version+1 || edited.Candidate.Capsule.Items[0].RedactedSummary != "Keep only the edited private summary." || edited.GraceExpiresAt != "" {
		t.Fatalf("edited candidate=%#v", edited)
	}
	cancel := homeMutationRequest(srv, session, "tray/"+pending.CandidateID+"/cancel", url.Values{
		viewerSessionCSRFFormField: {session.CSRFToken},
		"expected_version":         {strconv.Itoa(edited.Version)},
	})
	cancelResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(cancelResponse, cancel)
	if cancelResponse.Code != http.StatusNoContent {
		t.Fatalf("cancel status=%d body=%s", cancelResponse.Code, cancelResponse.Body.String())
	}
	canceled := homeRouteCandidate(t, vault, pending.CandidateID)
	if canceled.State != "canceled" || canceled.Candidate.Capsule != nil || canceled.Candidate.SemanticDelta != nil {
		t.Fatalf("canceled candidate retained private content: %#v", canceled)
	}
	home, err := vault.BuildMemoryHomeData(store.MemoryHomeQuery{
		RepositoryID: "repository_pulse", BindingDigest: strings.Repeat("a", 64), GeneratedAt: time.Now().UTC(),
		LiveReadiness: store.MemoryHomeLiveReadiness{Outcome: store.MemoryHomeReadinessReady},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if home.Memories.ActiveCount != 0 || len(home.Memories.LatestActive) != 0 {
		t.Fatalf("canceled memory entered canonical recall: %#v", home.Memories)
	}
}

func newHomeRoutePending(t *testing.T, vault *store.Store, suffix, summary string) store.MemoryTrayCandidateView {
	t.Helper()
	now := time.Now().UTC()
	finalized, err := vault.FinalizeTurn(store.TurnFinalizeRequest{
		Schema: store.TurnFinalizeRequestSchema, Host: "codex",
		SessionID: "session_home_" + suffix, TurnID: "turn_home_" + suffix,
		SourceEventKey: "event_home_" + suffix, IdempotencyKey: "idempotency_home_" + suffix,
		BindingDigest: strings.Repeat("a", 64), PolicyEpoch: 1, ResolverEpoch: 1,
		Candidates: []store.PrivateMemoryCandidate{{
			Kind: store.PrivateMemoryCandidateCapsule,
			Capsule: &store.MemoryCapsule{
				Schema: store.MemoryCapsuleSchema,
				Source: store.CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: now.Format(time.RFC3339Nano)},
				Items: []store.MemoryCapsuleItem{{
					Kind: "decision", RedactedSummary: summary,
					Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
				}},
			},
		}},
	}, now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalized.Receipts) != 1 {
		t.Fatalf("finalize receipts=%d, want 1", len(finalized.Receipts))
	}
	candidateID := finalized.Receipts[0].CandidateID
	candidates, err := vault.ListMemoryTray(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.CandidateID == candidateID {
			return candidate
		}
	}
	t.Fatal("pending Home candidate not found")
	return store.MemoryTrayCandidateView{}
}

func writeHomeUnassignedInbox(t *testing.T) (path, itemID, contentDigest string) {
	return writeHomeUnassignedInboxWithSummary(t, "Assign this only to an exact project.")
}

func writeHomeUnassignedInboxWithSummary(t *testing.T, summary string) (path, itemID, contentDigest string) {
	t.Helper()
	canonicalCandidate := fmt.Sprintf(
		`{"capsule":{"items":[{"confidence":1,"evidence_hint":"user_confirmed","kind":"decision","privacy_tier":"normal","redacted_summary":%q,"retention":"project","tags":["pulse"]}],"raw_input_included":false,"schema":"pulse.memory_capsule.v1","source":{"conversation_scope":"current_turn","host":"codex","timestamp":"2026-07-17T10:00:00Z"}},"kind":"memory_capsule"}`,
		summary,
	)
	digest := sha256.Sum256(append([]byte("pulse-unassigned-candidate-v1\x00"), []byte(canonicalCandidate)...))
	contentDigest = fmt.Sprintf("%x", digest[:])
	itemID = "unassigned_" + contentDigest[:32]
	directory := filepath.Join(t.TempDir(), ".pulse", "supervisor")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(directory, "unassigned-inbox.json")
	body := fmt.Sprintf(
		`{"schema":"pulse.unassigned_inbox.v1","items":[{"schema":"pulse.unassigned_candidate.v1","item_id":%q,"content_digest":%q,"created_at":"2026-07-17T10:00:00Z","host":"codex","idempotency_key":"request_home_01","candidate":%s}],"receipts":[{"receipt_id":"unassigned_receipt_%s","item_id":%q,"content_digest":%q,"action":"stage","status":"staged","created_at":"2026-07-17T10:00:00Z"}]}`,
		itemID, contentDigest, canonicalCandidate, strings.Repeat("a", 32), itemID, contentDigest,
	) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, itemID, contentDigest
}

func homeRouteCandidate(t *testing.T, vault *store.Store, candidateID string) store.MemoryTrayCandidateView {
	t.Helper()
	candidates, err := vault.ListMemoryTray(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.CandidateID == candidateID {
			return candidate
		}
	}
	t.Fatal("Home candidate not found")
	return store.MemoryTrayCandidateView{}
}

func newHomeRouteFixture(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "personal.db"), store.StoreKindPersonal, "store_personal_home_routes")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	binding := strings.Repeat("a", 64)
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_pulse"); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		IPCSecret: "home-test-daemon-secret", Store: vault, HomeOrigin: testViewerSessionOrigin,
		HomeBindingVerifier: &homeBindingVerifierStub{},
		TrayGracePeriod:     30 * time.Second, Billing: BillingStatus{Host: "codex", Mode: "host-extracted"},
		Retrieval: retrieve.New(retrieve.Config{Store: vault, Embedder: productTestEmbedder{}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, vault
}

func seedHomeProtectedWipeCapsule(t *testing.T, vault *store.Store, id, summary string) {
	t.Helper()
	_, err := vault.DB().Exec(`
		INSERT INTO memory_capsules(
			id, schema_version, source_host, conversation_scope, source_timestamp,
			kind, redacted_summary, confidence, evidence_hint, privacy_tier,
			retention, tags, created_at
		) VALUES (?, 'pulse.memory_capsule.v1', 'codex', 'project',
		          '2026-07-19T00:00:00Z', 'decision', ?, 1, 'fixture',
		          'private', 'durable', '[]', '2026-07-19T00:00:00Z')`,
		id, summary,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func homePageRequest(srv *Server, session viewerSessionView) *http.Request {
	request := httptest.NewRequest(http.MethodGet, testViewerSessionOrigin+viewerSessionRoutePath(session.RouteScope), nil)
	request.Host = srv.homeSessions.expectedHost
	request.RemoteAddr = "127.0.0.1:54321"
	request.AddCookie(srv.homeSessions.Cookie(session))
	return request
}

func homeMutationRequest(srv *Server, session viewerSessionView, path string, form url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, testViewerSessionOrigin+viewerSessionRoutePath(session.RouteScope)+path, strings.NewReader(form.Encode()))
	request.Host = srv.homeSessions.expectedHost
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Origin", testViewerSessionOrigin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Sec-Fetch-Mode", "cors")
	request.Header.Set("Sec-Fetch-Dest", "empty")
	request.Header.Set("Content-Type", viewerSessionFormMediaType)
	request.AddCookie(srv.homeSessions.Cookie(session))
	return request
}

func issueHomeSession(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, testViewerSessionOrigin+"/home/session", strings.NewReader(body))
	request.Host = srv.homeSessions.expectedHost
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Pulse-Key", "home-test-daemon-secret")
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	return response
}

func testHomeSessionRequestBody(reasonCode, checkedAt string) string {
	contracts := map[string]struct {
		outcome string
		code    string
		label   string
	}{
		"personal_live_ready":           {outcome: "ready", code: "continue_working", label: "Continue working"},
		"codex_plugin_unavailable":      {outcome: "action_required", code: "repair_codex_plugin", label: "Run pulse repair"},
		"codex_hook_lifecycle_required": {outcome: "action_required", code: "complete_codex_lifecycle", label: "Complete one normal Codex turn"},
	}
	contract, ok := contracts[reasonCode]
	if !ok {
		panic("unknown test Home readiness")
	}
	body, err := json.Marshal(map[string]any{"live_readiness": map[string]any{
		"schema": "pulse.personal_live_readiness.v1", "outcome": contract.outcome,
		"reason_code": reasonCode, "next_action": map[string]string{"code": contract.code, "label": contract.label},
		"checked_at": checkedAt,
	}})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func decodeHomeSessionResponse(t *testing.T, response *httptest.ResponseRecorder) homeSessionResponse {
	t.Helper()
	var handoff homeSessionResponse
	if err := json.NewDecoder(response.Body).Decode(&handoff); err != nil {
		t.Fatal(err)
	}
	return handoff
}

func serveHomeHandoffPage(t *testing.T, srv *Server, handoff homeSessionResponse) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, handoff.TargetURL, nil)
	request.Host = srv.homeSessions.expectedHost
	request.RemoteAddr = "127.0.0.1:54321"
	request.AddCookie(&http.Cookie{Name: handoff.CookieName, Value: handoff.CookieValue, Path: handoff.CookiePath})
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Home page status=%d body=%s", response.Code, response.Body.String())
	}
	return response
}

func homeSessionCount(srv *Server) int {
	srv.homeSessions.mu.Lock()
	defer srv.homeSessions.mu.Unlock()
	return len(srv.homeSessions.sessions)
}

func assertHomeHeaders(t *testing.T, header http.Header) {
	t.Helper()
	for name, want := range map[string]string{
		"Cache-Control":          "no-store, private, max-age=0",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := header.Get(name); got != want {
			t.Fatalf("%s=%q want %q", name, got, want)
		}
	}
}
