package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/unassigned"
	"github.com/nkkmnk/pulse/internal/userpresence"
)

const (
	homeSessionRequestMaxBytes          = int64(4 << 10)
	homeProtectedWipeBeginSchemaV1      = "pulse.home.protected_wipe_begin.v1"
	homeProtectedWipeMaximumPending     = 16
	homeProtectedWipeMaximumCeremonyTTL = 90 * time.Second
)

var (
	errHomeUnassignedRejected = errors.New("unassigned card was rejected before Tray creation")
	errHomeBindingStale       = errors.New("Home product binding is no longer current")
)

type homeSessionRequest struct {
	LiveReadiness personalLiveReadinessSnapshot `json:"live_readiness"`
}

type homeSessionResponse struct {
	CookieName    string `json:"cookie_name"`
	CookieValue   string `json:"cookie_value"`
	CookiePath    string `json:"cookie_path"`
	MaxAgeSeconds int    `json:"max_age_seconds"`
	TargetURL     string `json:"target_url"`
}

type homeProtectedWipeBeginResponse struct {
	Schema              string                            `json:"schema"`
	Action              userpresence.Action               `json:"action"`
	CeremonyID          string                            `json:"ceremony_id"`
	ProfileKind         userpresence.EnhancedPresenceKind `json:"profile_kind"`
	TargetDigest        string                            `json:"target_digest"`
	RepositoryID        string                            `json:"repository_id"`
	StoreID             string                            `json:"store_id"`
	BindingDigest       string                            `json:"binding_digest"`
	AffectedDataCount   uint64                            `json:"affected_data_count"`
	AffectedDataDigest  string                            `json:"affected_data_digest"`
	AffectedDataVersion uint64                            `json:"affected_data_version"`
	Display             string                            `json:"display"`
	ExpiresAt           time.Time                         `json:"expires_at"`
}

type homeProtectedWipePending struct {
	sessionDigest  [sha256.Size]byte
	routeScope     string
	trustedSurface string
	targetDigest   string
	profileKind    userpresence.EnhancedPresenceKind
	expiresAt      time.Time
	snapshot       store.ProductMemoryWipeSnapshotV1
}

func (s *Server) homeHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.homeHardenMiddleware)
	r.Post("/home/session", s.handleHomeSessionIssue)
	r.Route("/home/s/{route}", func(r chi.Router) {
		r.Get("/", s.handleHomePage)
		r.Get("/assets/home.js", s.handleHomeScript)
		r.Post("/present", s.handleHomePresent)
		r.Post("/logout", s.handleHomeLogout)
		r.Post("/tray/{id}/edit", s.handleHomeTrayEdit)
		r.Post("/tray/{id}/cancel", s.handleHomeTrayCancel)
		r.Post("/tray/{id}/commit", s.handleHomeTrayCommit)
		r.Post("/unassigned/{id}/assign", s.handleHomeUnassignedAssign)
		r.Post("/unassigned/{id}/delete", s.handleHomeUnassignedDelete)
		r.Post("/consolidation/start", s.handleHomeConsolidationStart)
		r.Post("/consolidation/{id}/cancel", s.handleHomeConsolidationCancel)
		r.Post("/consolidation/{id}/resume", s.handleHomeConsolidationResume)
		r.Post("/protected/wipe/begin", s.handleHomeProtectedWipeBegin)
		r.Post("/protected/wipe/complete", s.handleHomeProtectedWipeComplete)
	})
	return r
}

func (s *Server) handleHomeConsolidationStart(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	if !exactHomeFormFields(r, viewerSessionCSRFFormField) || s.consolidationReports == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := s.startConsolidationReport(r.Context()); err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHomeConsolidationCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	if !exactHomeFormFields(r, viewerSessionCSRFFormField) || s.consolidationReports == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.reportForCurrentDestination(id); err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	if _, err := s.consolidationReports.Cancel(id); err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHomeConsolidationResume(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	if !exactHomeFormFields(r, viewerSessionCSRFFormField) || s.consolidationReports == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := s.resumeConsolidationReport(r.Context(), chi.URLParam(r, "id"), r.Header.Get("Idempotency-Key")); err != nil {
		writeConsolidationReportError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHomeProtectedWipeBegin(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireHomeMutation(w, r)
	if !ok {
		return
	}
	if !exactHomeFormFields(r, viewerSessionCSRFFormField) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	profile := s.cfg.EnhancedPresenceAuthorizer.Profile()
	if !profile.Available || !profileAuthorizes(profile, userpresence.ActionVaultWipe) {
		http.Error(w, "Protected memory wipe is unavailable on this machine.", http.StatusConflict)
		return
	}
	snapshot, err := s.cfg.Store.PrepareProductMemoryWipe(r.Context())
	if err != nil {
		writeHomeProtectedWipeError(w, err)
		return
	}
	bindingDigest, repositoryID, boundaryOK := s.cfg.Store.ProductRuntimeBoundary()
	if !boundaryOK || bindingDigest != snapshot.BindingDigest {
		http.Error(w, "The project binding changed. Run pulse home again.", http.StatusConflict)
		return
	}
	target := userpresence.ProtectedActionTargetV1{
		Schema: userpresence.ProtectedActionTargetSchemaV1, Action: userpresence.ActionVaultWipe,
		AffectedDataCount: snapshot.AffectedDataCount, AffectedDataDigest: snapshot.AffectedDataDigest,
		AffectedDataVersion: snapshot.AffectedDataVersion, BindingDigest: snapshot.BindingDigest,
		PolicyEpoch: snapshot.PolicyEpoch, RepositoryID: repositoryID, StoreID: snapshot.StoreID,
	}
	targetDigest, err := target.Digest()
	if err != nil {
		writeHomeProtectedWipeError(w, err)
		return
	}
	ceremony, err := s.cfg.EnhancedPresenceAuthorizer.Begin(r.Context(), target)
	if err != nil {
		writeHomeProtectedWipeError(w, err)
		return
	}
	now := s.homeNow()
	if ceremony.Schema != userpresence.EnhancedPresenceCeremonySchemaV1 ||
		ceremony.ProfileKind != profile.Kind || ceremony.TargetDigest != targetDigest ||
		!validLowerHexDigest(ceremony.CeremonyID) || !validLowerHexDigest(ceremony.TargetDigest) ||
		!ceremony.ExpiresAt.UTC().After(now) || ceremony.ExpiresAt.UTC().Sub(now) > homeProtectedWipeMaximumCeremonyTTL {
		writeHomeProtectedWipeError(w, userpresence.ErrEnhancedCeremonyInvalid)
		return
	}
	sessionDigest, validSession := viewerSessionIDDigest(session.ID)
	if !validSession {
		writeHomeProtectedWipeError(w, errViewerSessionUnauthorized)
		return
	}
	pending := homeProtectedWipePending{
		sessionDigest: sessionDigest, routeScope: session.RouteScope,
		trustedSurface: session.TrustedSurfaceInstance, targetDigest: targetDigest,
		profileKind: ceremony.ProfileKind, expiresAt: ceremony.ExpiresAt.UTC(), snapshot: snapshot,
	}
	s.homeProtectedWipeMu.Lock()
	s.pruneHomeProtectedWipesLocked(now)
	_, collision := s.homeProtectedWipeItems[ceremony.CeremonyID]
	stored := !collision && len(s.homeProtectedWipeItems) < homeProtectedWipeMaximumPending
	if stored {
		s.homeProtectedWipeItems[ceremony.CeremonyID] = pending
	}
	s.homeProtectedWipeMu.Unlock()
	if !stored {
		http.Error(w, "Protected memory wipe is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, homeProtectedWipeBeginResponse{
		Schema: homeProtectedWipeBeginSchemaV1, Action: userpresence.ActionVaultWipe,
		CeremonyID: ceremony.CeremonyID, ProfileKind: ceremony.ProfileKind,
		TargetDigest: targetDigest, RepositoryID: repositoryID, StoreID: snapshot.StoreID,
		BindingDigest: snapshot.BindingDigest, AffectedDataCount: snapshot.AffectedDataCount,
		AffectedDataDigest: snapshot.AffectedDataDigest, AffectedDataVersion: snapshot.AffectedDataVersion,
		Display: fmt.Sprintf(
			"Authorize deletion of %d stored records across memory and continuity, including supporting local receipts and projections.",
			snapshot.AffectedDataCount,
		),
		ExpiresAt: ceremony.ExpiresAt.UTC(),
	})
}

func (s *Server) handleHomeProtectedWipeComplete(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireHomeMutation(w, r)
	if !ok {
		return
	}
	if !exactHomeFormFields(r, viewerSessionCSRFFormField, "ceremony_id") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ceremonyID := r.PostForm.Get("ceremony_id")
	if !validLowerHexDigest(ceremonyID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sessionDigest, validSession := viewerSessionIDDigest(session.ID)
	if !validSession {
		writeHomeProtectedWipeError(w, errViewerSessionUnauthorized)
		return
	}
	now := s.homeNow()
	s.homeProtectedWipeMu.Lock()
	pending, exists := s.homeProtectedWipeItems[ceremonyID]
	expired := exists && !now.Before(pending.expiresAt)
	if expired {
		delete(s.homeProtectedWipeItems, ceremonyID)
		exists = false
	} else if exists && pending.sessionDigest == sessionDigest &&
		memoryPresentationConstantEqual(pending.routeScope, session.RouteScope) &&
		memoryPresentationConstantEqual(pending.trustedSurface, session.TrustedSurfaceInstance) {
		delete(s.homeProtectedWipeItems, ceremonyID)
	} else if exists {
		exists = false
	}
	s.pruneHomeProtectedWipesLocked(now)
	s.homeProtectedWipeMu.Unlock()
	if expired {
		http.Error(w, "The protected memory wipe request expired.", http.StatusGone)
		return
	}
	if !exists {
		http.Error(w, "The protected memory wipe request changed or was already used.", http.StatusConflict)
		return
	}
	assertion, err := s.cfg.EnhancedPresenceAuthorizer.Complete(r.Context(), userpresence.EnhancedPresenceCompletionV1{
		Schema:     userpresence.EnhancedPresenceCompletionSchemaV1,
		CeremonyID: ceremonyID, TargetDigest: pending.targetDigest,
	})
	if err != nil {
		writeHomeProtectedWipeError(w, err)
		return
	}
	if assertion.Schema != userpresence.EnhancedPresenceAssertionSchemaV1 ||
		assertion.Action != userpresence.ActionVaultWipe || assertion.CeremonyID != ceremonyID ||
		assertion.ProfileKind != pending.profileKind || assertion.TargetDigest != pending.targetDigest ||
		assertion.PolicyEpoch != pending.snapshot.PolicyEpoch || !validLowerHexDigest(assertion.AssertionDigest) ||
		assertion.ApprovedAt.IsZero() || assertion.ExpiresAt.UTC() != pending.expiresAt ||
		!assertion.ApprovedAt.UTC().Before(assertion.ExpiresAt.UTC()) || !now.Before(assertion.ExpiresAt.UTC()) {
		writeHomeProtectedWipeError(w, userpresence.ErrEnhancedCeremonyInvalid)
		return
	}
	if err := s.verifyHomeBinding(r.Context()); err != nil {
		http.SetCookie(w, s.homeSessions.ClearCookie(session.RouteScope))
		http.Error(w, "The project binding changed. Run pulse home again.", http.StatusConflict)
		return
	}
	receipt, err := s.cfg.Store.WipeProductMemoryIfSnapshot(r.Context(), pending.snapshot)
	if err != nil {
		writeHomeProtectedWipeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, receipt)
}

func exactHomeFormFields(r *http.Request, names ...string) bool {
	if r == nil || len(r.PostForm) != len(names) {
		return false
	}
	for _, name := range names {
		if values := r.PostForm[name]; len(values) != 1 || values[0] == "" {
			return false
		}
	}
	return true
}

func profileAuthorizes(profile userpresence.EnhancedPresenceProfile, action userpresence.Action) bool {
	if profile.Schema != userpresence.EnhancedPresenceProfileSchemaV1 || profile.Version != 1 ||
		!profile.Available ||
		(profile.Kind != userpresence.EnhancedPresenceWebAuthn && profile.Kind != userpresence.EnhancedPresenceMacOSNative) ||
		!userpresence.IsEnhancedProtectedAction(action) {
		return false
	}
	seen := make(map[userpresence.Action]struct{}, len(profile.ProtectedActions))
	authorized := false
	for _, candidate := range profile.ProtectedActions {
		if !userpresence.IsEnhancedProtectedAction(candidate) {
			return false
		}
		if _, duplicate := seen[candidate]; duplicate {
			return false
		}
		seen[candidate] = struct{}{}
		if candidate == action {
			authorized = true
		}
	}
	return authorized
}

func validLowerHexDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) pruneHomeProtectedWipesLocked(now time.Time) {
	for ceremonyID, pending := range s.homeProtectedWipeItems {
		if !now.Before(pending.expiresAt) {
			delete(s.homeProtectedWipeItems, ceremonyID)
		}
	}
}

func writeHomeProtectedWipeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, userpresence.ErrEnhancedActionUnavailable):
		http.Error(w, "Protected memory wipe is unavailable on this machine.", http.StatusConflict)
	case errors.Is(err, store.ErrProductMemoryWipeEmpty):
		http.Error(w, "There is no bound product memory to wipe.", http.StatusConflict)
	case errors.Is(err, store.ErrProductMemoryWipeSnapshotInvalid),
		errors.Is(err, store.ErrProductMemoryWipeSnapshotStale):
		http.Error(w, "The protected memory set changed. Start the wipe again.", http.StatusConflict)
	case errors.Is(err, userpresence.ErrEnhancedCeremonyExpired), errors.Is(err, userpresence.ErrPresenceExpired):
		http.Error(w, "The protected memory wipe request expired.", http.StatusGone)
	case errors.Is(err, userpresence.ErrEnhancedCeremonyInvalid),
		errors.Is(err, userpresence.ErrPresenceInvalid), errors.Is(err, userpresence.ErrPresenceReplay),
		errors.Is(err, userpresence.ErrPresenceDenied), errors.Is(err, errViewerSessionUnauthorized):
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		http.Error(w, "Protected memory wipe failed.", http.StatusBadRequest)
	}
}

func (s *Server) homeHardenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.homeSessions.HardenHeaders(w.Header())
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHomeSessionIssue(w http.ResponseWriter, r *http.Request) {
	ipcKeys := r.Header.Values("X-Pulse-Key")
	if r.URL.RawQuery != "" || r.URL.Fragment != "" || r.Host != s.homeSessions.expectedHost || !isLoopbackRequest(r) ||
		len(r.Header.Values("Authorization")) != 0 || len(r.Header.Values("Proxy-Authorization")) != 0 ||
		len(r.Header.Values("Origin")) != 0 || len(r.Header.Values("DPoP")) != 0 ||
		len(r.Header.Values("Mcp-Session-Id")) != 0 || len(r.Header.Values("MCP-Protocol-Version")) != 0 ||
		len(ipcKeys) != 1 || !memoryPresentationConstantEqual(ipcKeys[0], s.cfg.IPCSecret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if values := r.Header.Values("Content-Type"); len(values) != 1 || values[0] != "application/json" {
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	request, err := parseHomeSessionRequest(w, r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.verifyHomeBinding(r.Context()); err != nil {
		http.Error(w, "The project binding changed. Run pulse home again.", http.StatusConflict)
		return
	}
	session, err := s.homeSessions.Create(request.LiveReadiness)
	if err != nil {
		http.Error(w, "Home session unavailable", http.StatusServiceUnavailable)
		return
	}
	cookie := s.homeSessions.Cookie(session)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	writeJSON(w, homeSessionResponse{
		CookieName: cookie.Name, CookieValue: cookie.Value, CookiePath: cookie.Path,
		MaxAgeSeconds: cookie.MaxAge, TargetURL: s.homeSessions.expectedOrigin + cookie.Path,
	})
}

func parseHomeSessionRequest(w http.ResponseWriter, r *http.Request) (homeSessionRequest, error) {
	var request homeSessionRequest
	if r == nil || r.Body == nil || r.ContentLength > homeSessionRequestMaxBytes {
		return request, errors.New("invalid Home session request")
	}
	r.Body = http.MaxBytesReader(w, r.Body, homeSessionRequestMaxBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return request, err
	}
	if err := decodeStrictJSON(raw, &request); err != nil {
		return request, err
	}
	if err := validatePersonalLiveReadiness(request.LiveReadiness); err != nil {
		return request, err
	}
	return request, nil
}

func (s *Server) handleHomePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.Fragment != "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	session, err := s.homeSessions.Authenticate(r)
	if err != nil {
		routeScope, _ := viewerSessionRouteFromPath(r.URL.EscapedPath())
		http.SetCookie(w, s.homeSessions.ClearCookie(routeScope))
		http.Error(w, "Memory Home is locked. Run pulse home again.", http.StatusUnauthorized)
		return
	}
	if err := s.verifyHomeBinding(r.Context()); err != nil {
		http.SetCookie(w, s.homeSessions.ClearCookie(session.RouteScope))
		http.Error(w, "The project binding changed. Run pulse home again.", http.StatusConflict)
		return
	}
	data, err := s.buildMemoryHome(s.homeNow(), session.LiveReadiness)
	if err != nil {
		http.Error(w, "Memory Home data is unavailable", http.StatusServiceUnavailable)
		return
	}
	candidates, err := s.cfg.Store.ListPendingMemoryTrayCandidates(50)
	if err != nil {
		http.Error(w, "Memory Tray is unavailable", http.StatusServiceUnavailable)
		return
	}
	cards, err := memoryHomePendingCards(candidates)
	if err != nil {
		http.Error(w, "Memory Tray data is invalid", http.StatusInternalServerError)
		return
	}
	unassignedSnapshot := unassigned.Snapshot{}
	unassignedUnavailable := false
	if s.cfg.UnassignedInboxPath != "" {
		unassignedSnapshot, err = unassigned.ReadSnapshot(s.cfg.UnassignedInboxPath)
		if err != nil {
			unassignedUnavailable = true
		}
	}
	page, err := renderMemoryHomeHTML(memoryHomePage{
		Data: data, Pending: cards,
		EnhancedPresenceProfile: s.cfg.EnhancedPresenceAuthorizer.Profile(),
		UnassignedEnabled:       s.cfg.UnassignedInboxPath != "", UnassignedUnavailable: unassignedUnavailable,
		Unassigned: memoryHomeUnassignedCards(unassignedSnapshot.Cards),
		UnassignedActivity: memoryHomeUnassignedActivities(
			unassignedSnapshot.Activity, data.Boundary.BindingDigest,
		),
		CSRFToken: session.CSRFToken,
	})
	if err != nil {
		http.Error(w, "Memory Home render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; script-src 'self'; style-src 'unsafe-inline'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, page)
}

func (s *Server) handleHomeScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.Fragment != "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if _, err := s.homeSessions.Authenticate(r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = io.WriteString(w, memoryHomeBrowserScript)
}

func (s *Server) handleHomePresent(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireHomeMutation(w, r)
	if !ok {
		return
	}
	candidateID, candidateVersion, ok := exactHomeCandidateForm(r)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	candidate, ok := s.currentHomeCandidate(candidateID, candidateVersion)
	if !ok {
		http.Error(w, "candidate changed", http.StatusConflict)
		return
	}
	bindingDigest, _, boundaryOK := s.cfg.Store.ProductRuntimeBoundary()
	if !boundaryOK {
		http.Error(w, "Home boundary unavailable", http.StatusServiceUnavailable)
		return
	}
	binding := MemoryPresentationBinding{
		BrowserSessionID: session.ID, CSRFToken: session.CSRFToken,
		WorkspaceBindingDigest: bindingDigest, CandidateID: candidate.CandidateID,
		CandidateVersion: candidate.Version, ContentDigest: candidate.ContentDigest,
		TrustedSurfaceInstance: session.TrustedSurfaceInstance,
	}
	capability, err := s.homePresentation.IssueCapability(binding)
	if err != nil {
		writeHomeMutationError(w, err)
		return
	}
	if _, err := s.homePresentation.Present(r.Context(), r, MemoryPresentationAttempt{
		Authority: MemoryPresentationAuthorityHomeBrowser, Capability: capability, Binding: binding,
	}); err != nil {
		writeHomeMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHomeLogout(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireHomeMutation(w, r)
	if !ok {
		return
	}
	s.homeSessions.Revoke(session)
	http.SetCookie(w, s.homeSessions.ClearCookie(session.RouteScope))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHomeTrayEdit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	version, ok := exactHomeVersion(r)
	values := r.PostForm["candidate_json"]
	if !ok || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	decoder := json.NewDecoder(strings.NewReader(values[0]))
	decoder.DisallowUnknownFields()
	var candidate store.PrivateMemoryCandidate
	if err := decoder.Decode(&candidate); err != nil {
		http.Error(w, "invalid structured memory", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid structured memory", http.StatusBadRequest)
		return
	}
	if _, err := s.cfg.Store.EditMemoryTrayCandidate(
		chi.URLParam(r, "id"), version, candidate, s.homeNow(), s.cfg.TrayGracePeriod,
	); err != nil {
		writeHomeMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHomeTrayCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	version, ok := exactHomeVersion(r)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := s.cfg.Store.CancelMemoryTrayCandidate(chi.URLParam(r, "id"), version, s.homeNow()); err != nil {
		writeHomeMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHomeTrayCommit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	version, ok := exactHomeVersion(r)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	receipt, err := s.cfg.Store.CommitMemoryTrayCandidate(chi.URLParam(r, "id"), version, s.homeNow())
	if err != nil {
		writeHomeMutationError(w, err)
		return
	}
	s.refreshProductRetrieval(receipt)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHomeUnassignedAssign(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	contentDigest, ok := exactHomeUnassignedDigest(r)
	expectedBinding, bindingOK := exactHomeUnassignedBinding(r)
	currentBinding, currentRepository, currentOK := s.cfg.Store.ProductRuntimeBoundary()
	if !ok || !bindingOK || !currentOK || expectedBinding != currentBinding || s.cfg.UnassignedInboxPath == "" {
		if ok && bindingOK && currentOK && expectedBinding != currentBinding {
			http.Error(w, "The project binding changed. Refresh Home.", http.StatusConflict)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	now := s.homeNow()
	err := unassigned.Assign(
		s.cfg.UnassignedInboxPath, chi.URLParam(r, "id"), contentDigest,
		unassigned.Destination{
			BindingDigest: currentBinding, RepositoryID: currentRepository, StoreID: s.cfg.Store.StoreID(),
		},
		now,
		func(candidate store.PrivateMemoryCandidate) error {
			if candidate.Capsule == nil {
				return errors.New("unassigned capsule is missing")
			}
			result, err := s.cfg.Store.PrepareUnassignedMemoryCapsuleWithInvocation(
				*candidate.Capsule, contentDigest, now, s.cfg.TrayGracePeriod,
			)
			if err != nil {
				return err
			}
			if result.Status != store.TurnFinalizedCandidates || len(result.Receipts) < 1 {
				return errHomeUnassignedRejected
			}
			for _, receipt := range result.Receipts {
				if receipt.Status != store.MemoryWritePending {
					return errHomeUnassignedRejected
				}
			}
			return nil
		},
	)
	if err != nil {
		if errors.Is(err, errHomeUnassignedRejected) {
			http.Error(w, "Pulse rejected this card before Tray creation. It remains in Inbox.", http.StatusUnprocessableEntity)
			return
		}
		if errors.Is(err, unassigned.ErrDestinationConflict) {
			http.Error(w, "This Inbox card is already assigned to another project. Refresh Home.", http.StatusConflict)
			return
		}
		http.Error(w, "The Inbox card changed. Refresh Home.", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHomeUnassignedDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireHomeMutation(w, r); !ok {
		return
	}
	contentDigest, ok := exactHomeUnassignedDigest(r)
	expectedBinding, bindingOK := exactHomeUnassignedBinding(r)
	currentBinding, _, currentOK := s.cfg.Store.ProductRuntimeBoundary()
	if !ok || !bindingOK || !currentOK || expectedBinding != currentBinding || s.cfg.UnassignedInboxPath == "" {
		if ok && bindingOK && currentOK && expectedBinding != currentBinding {
			http.Error(w, "The project binding changed. Refresh Home.", http.StatusConflict)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := unassigned.Delete(
		s.cfg.UnassignedInboxPath, chi.URLParam(r, "id"), contentDigest, s.homeNow(),
	); err != nil {
		http.Error(w, "The Inbox card changed. Refresh Home.", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) homeNow() time.Time {
	if s != nil && s.homeSessions != nil && s.homeSessions.clock != nil {
		return s.homeSessions.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *Server) requireHomeMutation(w http.ResponseWriter, r *http.Request) (viewerSessionView, bool) {
	session, err := s.homeSessions.ValidateMutation(w, r)
	if err == nil {
		if verifyErr := s.verifyHomeBinding(r.Context()); verifyErr != nil {
			http.SetCookie(w, s.homeSessions.ClearCookie(session.RouteScope))
			http.Error(w, "The project binding changed. Run pulse home again.", http.StatusConflict)
			return viewerSessionView{}, false
		}
		return session, true
	}
	writeHomeMutationError(w, err)
	return viewerSessionView{}, false
}

func (s *Server) verifyHomeBinding(ctx context.Context) error {
	if s == nil || s.cfg.Store == nil || s.cfg.HomeBindingVerifier == nil {
		return errHomeBindingStale
	}
	bindingDigest, repositoryID, ok := s.cfg.Store.ProductRuntimeBoundary()
	if !ok || s.cfg.HomeBindingVerifier.Verify(ctx, bindingDigest, repositoryID) != nil {
		return errHomeBindingStale
	}
	return nil
}

func exactHomeCandidateForm(r *http.Request) (string, int, bool) {
	ids := r.PostForm["candidate_id"]
	if len(ids) != 1 || ids[0] == "" {
		return "", 0, false
	}
	version, ok := exactHomeVersion(r)
	return ids[0], version, ok
}

func exactHomeVersion(r *http.Request) (int, bool) {
	values := r.PostForm["expected_version"]
	if len(values) != 1 {
		return 0, false
	}
	version, err := strconv.Atoi(values[0])
	return version, err == nil && version > 0
}

func exactHomeUnassignedDigest(r *http.Request) (string, bool) {
	values := r.PostForm["content_digest"]
	if len(values) != 1 || len(values[0]) != 64 {
		return "", false
	}
	for _, char := range values[0] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", false
		}
	}
	return values[0], true
}

func exactHomeUnassignedBinding(r *http.Request) (string, bool) {
	values := r.PostForm["expected_binding_digest"]
	if len(values) != 1 || len(values[0]) != 64 {
		return "", false
	}
	for _, char := range values[0] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", false
		}
	}
	return values[0], true
}

func (s *Server) currentHomeCandidate(candidateID string, version int) (store.MemoryTrayPendingCandidate, bool) {
	candidate, err := s.cfg.Store.GetPendingMemoryTrayCandidate(candidateID, version)
	return candidate, err == nil
}

func writeHomeMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errViewerSessionMethodNotAllowed):
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	case errors.Is(err, errViewerSessionUnsupportedMediaType):
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
	case errors.Is(err, errViewerSessionRequestTooLarge):
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
	case errors.Is(err, errViewerSessionBadRequest):
		http.Error(w, "bad request", http.StatusBadRequest)
	case errors.Is(err, errViewerSessionUnauthorized), errors.Is(err, errViewerSessionExpired),
		errors.Is(err, errViewerSessionClockRollback), errors.Is(err, ErrMemoryPresentationUnauthorized),
		errors.Is(err, ErrMemoryPresentationExpired), errors.Is(err, ErrMemoryPresentationReplay):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, store.ErrMemoryTrayGraceActive):
		http.Error(w, "The visible review delay is still active.", http.StatusTooEarly)
	case errors.Is(err, store.ErrMemoryTrayVersionConflict), errors.Is(err, store.ErrMemoryTrayTerminal),
		errors.Is(err, store.ErrMemoryTrayNotPresented), errors.Is(err, store.ErrProductRuntimeMismatch),
		errors.Is(err, store.ErrMemoryPresentationConflict):
		http.Error(w, "The memory card changed. Refresh Home.", http.StatusConflict)
	default:
		http.Error(w, "Memory Home action failed", http.StatusBadRequest)
	}
}

const memoryHomeBrowserScript = `(() => {
  "use strict";
  const maxConcurrentPresentations = 4;
  const encode = (values) => new URLSearchParams(values).toString();
  const csrf = document.querySelector('input[name="csrf_token"]')?.value || "";
  const post = async (url, values) => fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: {"Content-Type": "application/x-www-form-urlencoded"},
    body: encode(values),
  });
  const show = (card, message) => {
    const status = card?.querySelector("[data-home-status]");
    if (status) status.textContent = message;
  };
  const showMutationFailure = (form, message) => {
    const card = form.closest("[data-candidate-id]");
    if (card) {
      show(card, message);
      return;
    }
    let status = form.querySelector("[data-home-form-status]");
    if (!status) {
      status = document.createElement("span");
      status.dataset.homeFormStatus = "";
      status.setAttribute("role", "status");
      form.append(status);
    }
    status.textContent = message;
  };

  const protectedWipeTimers = new WeakMap();
  const protectedWipePart = (card, name) => card.querySelector("[data-protected-wipe-" + name + "]");
  const setProtectedWipeStatus = (card, message) => {
    const status = protectedWipePart(card, "status");
    if (status) status.textContent = message;
  };
  const setProtectedWipeBusy = (card, busy) => {
    if (busy) {
      card.setAttribute("aria-busy", "true");
    } else {
      card.removeAttribute("aria-busy");
    }
    card.querySelectorAll("button").forEach((button) => { button.disabled = busy; });
  };
  const clearProtectedWipeTimer = (card) => {
    const timer = protectedWipeTimers.get(card);
    if (timer) window.clearInterval(timer);
    protectedWipeTimers.delete(card);
  };
  const clearProtectedWipeRequest = (card) => {
    clearProtectedWipeTimer(card);
    delete card.dataset.protectedWipeCeremonyId;
    delete card.dataset.protectedWipeExpiresAt;
    delete card.dataset.protectedWipeAnnouncement;
  };
  const resetProtectedWipe = (card, message, restoreFocus) => {
    const beginButton = protectedWipePart(card, "begin");
    clearProtectedWipeRequest(card);
    protectedWipePart(card, "review").hidden = true;
    protectedWipePart(card, "receipt").hidden = true;
    protectedWipePart(card, "idle").hidden = false;
    setProtectedWipeBusy(card, false);
    setProtectedWipeStatus(card, message);
    if (restoreFocus) beginButton.focus();
  };
  const protectedWipeRemainingSeconds = (card) => {
    const expiresAt = Date.parse(card.dataset.protectedWipeExpiresAt || "");
    if (!Number.isFinite(expiresAt)) return 0;
    return Math.max(0, Math.ceil((expiresAt - Date.now()) / 1000));
  };
  const announceProtectedWipeCountdown = (card, remaining) => {
    let bucket = "";
    if (remaining <= 5) bucket = String(remaining);
    else if (remaining <= 10) bucket = "10";
    else if (remaining <= 30) bucket = "30";
    else if (remaining <= 60) bucket = "60";
    if (!bucket || card.dataset.protectedWipeAnnouncement === bucket) return;
    card.dataset.protectedWipeAnnouncement = bucket;
    const countdownLive = protectedWipePart(card, "countdown-live");
    countdownLive.textContent = remaining === 0
      ? "The verification request expired. Nothing was deleted."
      : "Verification request expires in " + remaining + " seconds.";
  };
  const expireProtectedWipe = (card) => {
    const review = protectedWipePart(card, "review");
    const shouldRestoreFocus = review.contains(document.activeElement);
    const countdownLive = protectedWipePart(card, "countdown-live");
    countdownLive.textContent = "The verification request expired. Nothing was deleted.";
    resetProtectedWipe(card, "The verification request expired. Nothing was deleted. Review a fresh count to try again.", shouldRestoreFocus);
  };
  const updateProtectedWipeCountdown = (card) => {
    const remaining = protectedWipeRemainingSeconds(card);
    protectedWipePart(card, "countdown").textContent = remaining + (remaining === 1 ? " second remaining" : " seconds remaining");
    announceProtectedWipeCountdown(card, remaining);
    if (remaining === 0) expireProtectedWipe(card);
  };
  const startProtectedWipeCountdown = (card) => {
    clearProtectedWipeTimer(card);
    card.dataset.protectedWipeAnnouncement = "initial";
    protectedWipePart(card, "countdown-live").textContent =
      "Verification request expires in " + protectedWipeRemainingSeconds(card) + " seconds.";
    updateProtectedWipeCountdown(card);
    if (!card.dataset.protectedWipeCeremonyId) return;
    protectedWipeTimers.set(card, window.setInterval(() => updateProtectedWipeCountdown(card), 1000));
  };
  const validProtectedWipeBegin = (value) => value &&
    value.schema === "pulse.home.protected_wipe_begin.v1" && value.action === "vault.wipe" &&
    /^[0-9a-f]{64}$/.test(value.ceremony_id) && Number.isSafeInteger(value.affected_data_count) &&
    value.affected_data_count > 0 && Number.isFinite(Date.parse(value.expires_at)) &&
    Date.parse(value.expires_at) > Date.now();
  const validProtectedWipeReceipt = (value) => value &&
    value.schema === "pulse.product_memory_wipe_receipt.v1" &&
    Number.isSafeInteger(value.affected_data_count) && value.affected_data_count > 0 &&
    /^[0-9a-f]{64}$/.test(value.snapshot_digest) && Number.isFinite(Date.parse(value.wiped_at));

  async function beginProtectedWipe(card) {
    if (card.getAttribute("aria-busy") === "true") return;
    setProtectedWipeBusy(card, true);
    setProtectedWipeStatus(card, "Preparing an exact content-free record count. Nothing is being deleted.");
    try {
      const response = await post("protected/wipe/begin", {csrf_token: csrf});
      if (!response.ok) {
        const message = response.status === 409
          ? "No protected wipe can be prepared right now. Nothing was deleted."
          : "The exact record count could not be prepared. Nothing was deleted.";
        resetProtectedWipe(card, message, true);
        return;
      }
      const started = await response.json();
      if (!validProtectedWipeBegin(started)) {
        resetProtectedWipe(card, "Pulse returned an invalid protected-wipe preview. Nothing was deleted.", true);
        return;
      }
      card.dataset.protectedWipeCeremonyId = started.ceremony_id;
      card.dataset.protectedWipeExpiresAt = started.expires_at;
      protectedWipePart(card, "count").textContent = String(started.affected_data_count);
      const expiry = protectedWipePart(card, "expiry");
      expiry.dateTime = started.expires_at;
      expiry.textContent = new Date(started.expires_at).toLocaleTimeString([], {hour: "2-digit", minute: "2-digit", second: "2-digit"});
      protectedWipePart(card, "idle").hidden = true;
      protectedWipePart(card, "receipt").hidden = true;
      const review = protectedWipePart(card, "review");
      review.hidden = false;
      setProtectedWipeBusy(card, false);
      setProtectedWipeStatus(card, "Review the exact stored-record count, then explicitly choose device verification or cancel.");
      startProtectedWipeCountdown(card);
      review.focus();
    } catch (_) {
      resetProtectedWipe(card, "The exact record count could not be prepared. Nothing was deleted.", true);
    }
  }

  async function completeProtectedWipe(card) {
    if (card.getAttribute("aria-busy") === "true") return;
    if (!card.dataset.protectedWipeCeremonyId || protectedWipeRemainingSeconds(card) === 0) {
      expireProtectedWipe(card);
      return;
    }
    setProtectedWipeBusy(card, true);
    setProtectedWipeStatus(card, "Waiting for enhanced device verification. Nothing is deleted unless verification succeeds.");
    try {
      const response = await post("protected/wipe/complete", {
        csrf_token: csrf,
        ceremony_id: card.dataset.protectedWipeCeremonyId,
      });
      if (!response.ok) {
        if (response.status === 410) {
          resetProtectedWipe(card, "The verification request expired. Nothing was deleted. Review a fresh count to try again.", true);
        } else if (response.status === 409) {
          resetProtectedWipe(card, "The stored record set changed or this request is no longer valid. Nothing was deleted. Review a fresh count.", true);
        } else if (response.status === 403) {
          resetProtectedWipe(card, "Enhanced device verification did not succeed. Nothing was deleted. Review a fresh count to try again.", true);
        } else {
          resetProtectedWipe(card, "Protected deletion failed. Nothing was deleted. Review a fresh count to try again.", true);
        }
        return;
      }
      const result = await response.json();
      if (!validProtectedWipeReceipt(result)) {
        resetProtectedWipe(card, "Pulse did not return a valid deletion receipt. Refresh Home before taking another action.", true);
        return;
      }
      clearProtectedWipeRequest(card);
      protectedWipePart(card, "idle").hidden = true;
      protectedWipePart(card, "review").hidden = true;
      const receipt = protectedWipePart(card, "receipt");
      protectedWipePart(card, "receipt-count").textContent = String(result.affected_data_count);
      const receiptTime = protectedWipePart(card, "receipt-time");
      receiptTime.dateTime = result.wiped_at;
      receiptTime.textContent = new Date(result.wiped_at).toLocaleString();
      protectedWipePart(card, "receipt-schema").textContent = result.schema;
      protectedWipePart(card, "receipt-digest").textContent = result.snapshot_digest;
      receipt.hidden = false;
      setProtectedWipeBusy(card, false);
      setProtectedWipeStatus(card, "Protected deletion completed. The content-free receipt is shown below.");
      receipt.focus();
    } catch (_) {
      resetProtectedWipe(card, "Protected deletion could not be confirmed. Review a fresh count before trying again.", true);
    }
  }

  function cancelProtectedWipe(card) {
    if (card.getAttribute("aria-busy") === "true") return;
    resetProtectedWipe(card, "Canceled. Nothing was deleted.", true);
  }

  [...document.querySelectorAll("[data-protected-wipe]")].forEach((card) => {
    const beginButton = protectedWipePart(card, "begin");
    const completeButton = protectedWipePart(card, "complete");
    const cancelButton = protectedWipePart(card, "cancel");
    beginButton.addEventListener("click", () => beginProtectedWipe(card));
    completeButton.addEventListener("click", () => completeProtectedWipe(card));
    cancelButton.addEventListener("click", () => cancelProtectedWipe(card));
  });

  const cards = [...document.querySelectorAll("[data-candidate-id]")];
  const intersectingCards = new Set();
  const scheduledCards = new Set();
  const queuedCards = new Set();
  const attemptedCards = new Set();
  const presentationQueue = [];
  let activePresentations = 0;

  const present = async (card) => {
    try {
      const response = await post("present", {
        csrf_token: csrf,
        candidate_id: card.dataset.candidateId,
        expected_version: card.dataset.candidateVersion,
      });
      if (!response.ok) {
        show(card, "Review delay did not start. Refresh Home to retry.");
        return;
      }
      show(card, "Shown to you. The review delay is running.");
    } catch (_) {
      show(card, "Review delay could not be confirmed. Refresh Home to retry.");
    }
  };

  const pumpPresentations = () => {
    while (activePresentations < maxConcurrentPresentations && presentationQueue.length > 0) {
      const card = presentationQueue.shift();
      queuedCards.delete(card);
      if (attemptedCards.has(card) || !intersectingCards.has(card) || document.visibilityState !== "visible") {
        continue;
      }
      attemptedCards.add(card);
      activePresentations += 1;
      present(card).finally(() => {
        activePresentations -= 1;
        pumpPresentations();
      });
    }
  };

  const schedulePresentation = (card) => {
    if (attemptedCards.has(card) || scheduledCards.has(card) || queuedCards.has(card) ||
        !intersectingCards.has(card) || document.visibilityState !== "visible") {
      return;
    }
    scheduledCards.add(card);
    requestAnimationFrame(() => requestAnimationFrame(() => {
      scheduledCards.delete(card);
      if (attemptedCards.has(card) || queuedCards.has(card) ||
          !intersectingCards.has(card) || document.visibilityState !== "visible") {
        return;
      }
      queuedCards.add(card);
      presentationQueue.push(card);
      pumpPresentations();
    }));
  };

  if (typeof IntersectionObserver !== "function") {
    cards.forEach((card) => show(card, "Visible review is unavailable. Refresh Home in a supported browser."));
  } else {
    const observer = new IntersectionObserver((entries) => {
      entries.forEach((entry) => {
        const card = entry.target;
        if (entry.isIntersecting && entry.intersectionRatio > 0) {
          intersectingCards.add(card);
          schedulePresentation(card);
          return;
        }
        intersectingCards.delete(card);
      });
    });
    cards.forEach((card) => observer.observe(card));
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") {
        intersectingCards.forEach(schedulePresentation);
      }
    });
  }

  document.addEventListener("submit", async (event) => {
    const form = event.target.closest("form[data-home-mutation], form.logout");
    if (!form) return;
    event.preventDefault();
    if (form.dataset.homeConfirm && !window.confirm(form.dataset.homeConfirm)) return;
    const mutationScope = form.closest(".tray-card") || form;
    if (mutationScope.dataset.homeMutationBusy === "true") return;
    const body = new URLSearchParams(new FormData(form)).toString();
    const buttons = mutationScope.querySelectorAll("button");
    mutationScope.dataset.homeMutationBusy = "true";
    mutationScope.setAttribute("aria-busy", "true");
    buttons.forEach((button) => { button.disabled = true; });
    const releaseMutation = () => {
      delete mutationScope.dataset.homeMutationBusy;
      mutationScope.removeAttribute("aria-busy");
      buttons.forEach((button) => { button.disabled = false; });
    };
    if (form.dataset.homePendingLabel) showMutationFailure(form, form.dataset.homePendingLabel);
    try {
      const response = await fetch(form.action, {
        method: "POST", credentials: "same-origin",
        headers: {"Content-Type": "application/x-www-form-urlencoded"},
        body,
      });
      if (!response.ok) {
        releaseMutation();
        const detail = (await response.text()).trim();
        const message = detail.includes("Refresh Home")
          ? detail
          : (detail ? detail + " Refresh Home and try again." : "Action failed. Refresh Home and try again.");
        showMutationFailure(form, message);
        return;
      }
      window.location.reload();
    } catch (_) {
      releaseMutation();
      showMutationFailure(form, "Action failed. Refresh Home and try again.");
    }
  });
})();`
