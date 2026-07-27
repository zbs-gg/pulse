package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

func TestPersonalTurnFinalizeUsesVerifiedRequestNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personal-write.db")
	vault, err := store.OpenVault(path, store.StoreKindPersonal, "store_personal_request_write")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	if err := vault.ConfigureProductRuntimeAuthority(strings.Repeat("a", 64), 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(strings.Repeat("a", 64), "repository_project_a"); err != nil {
		t.Fatal(err)
	}
	verifier := &productBindingVerifierStub{}
	server, err := New(Config{
		IPCSecret: "secret", Store: vault, ProductBindingVerifier: verifier,
		HomeOrigin: testViewerSessionOrigin, HomeBindingVerifier: &homeBindingVerifierStub{},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	secondBinding := strings.Repeat("b", 64)
	body, err := json.Marshal(store.TurnFinalizeRequest{
		Schema: store.TurnFinalizeRequestSchema,
		Host:   "codex", SessionID: "session_project_b", TurnID: "turn_project_b",
		SourceEventKey: "codex:session_project_b:turn_project_b:stop",
		IdempotencyKey: "finalize_project_b", BindingDigest: secondBinding,
		PolicyEpoch: 0, ResolverEpoch: 3,
		Candidates: []store.PrivateMemoryCandidate{{
			Kind: store.PrivateMemoryCandidateCapsule,
			Capsule: &store.MemoryCapsule{
				Schema: store.MemoryCapsuleSchema,
				Source: store.CapsuleSource{
					Host: "codex", ConversationScope: "current_turn",
					Timestamp: time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC).Format(time.RFC3339),
				},
				Items: []store.MemoryCapsuleItem{{
					Kind: "decision", RedactedSummary: "Project B keeps its own verified namespace.",
					Confidence: 0.95, EvidenceHint: "current_turn", PrivacyTier: "normal",
					Retention: "project", Tags: []string{"project-b"},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/turn/finalize", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Pulse-Key", "secret")
	workspace := filepath.Clean(t.TempDir())
	setProductBindingHeaders(request, workspace, secondBinding, "repository_project_b", "3")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("request-scoped finalize status=%d", response.StatusCode)
	}
	var result store.TurnFinalizeResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Receipts) != 1 || result.Receipts[0].Status != store.MemoryWriteCreated {
		t.Fatalf("request-scoped receipt=%#v", result.Receipts)
	}
	var repositoryID, scope string
	if err := vault.DB().QueryRow(`
		SELECT original_repository_id, memory_scope
		  FROM private_memory_objects
		 WHERE object_id=?`, result.Receipts[0].ObjectID).Scan(&repositoryID, &scope); err != nil {
		t.Fatal(err)
	}
	if repositoryID != "repository_project_b" || scope != store.MemoryScopeProject {
		t.Fatalf("request-scoped object repository=%q scope=%q", repositoryID, scope)
	}

	homeRequest := httptest.NewRequest(
		http.MethodPost,
		server.homeSessions.expectedOrigin+"/home/session",
		strings.NewReader(testHomeSessionRequestBody("personal_live_ready", "2026-07-27T03:01:00Z")),
	)
	homeRequest.Host = server.homeSessions.expectedHost
	homeRequest.RemoteAddr = "127.0.0.1:54321"
	homeRequest.Header.Set("Content-Type", "application/json")
	homeRequest.Header.Set("X-Pulse-Key", "secret")
	setProductBindingHeaders(homeRequest, workspace, secondBinding, "repository_project_b", "3")
	homeResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(homeResponse, homeRequest)
	if homeResponse.Code != http.StatusOK {
		t.Fatalf("request-scoped Home session status=%d body=%s", homeResponse.Code, homeResponse.Body.String())
	}
	handoff := decodeHomeSessionResponse(t, homeResponse)
	page := serveHomeHandoffPage(t, server, handoff)
	if !strings.Contains(page.Body.String(), "Project B keeps its own verified namespace.") ||
		!strings.Contains(page.Body.String(), "repository_project_b") {
		t.Fatalf("request-scoped Home omitted Project B memory: %s", page.Body.String())
	}
	authRequest := httptest.NewRequest(http.MethodGet, handoff.TargetURL, nil)
	authRequest.Host = server.homeSessions.expectedHost
	authRequest.RemoteAddr = "127.0.0.1:54321"
	authRequest.AddCookie(&http.Cookie{
		Name: handoff.CookieName, Value: handoff.CookieValue, Path: handoff.CookiePath,
	})
	homeSession, err := server.homeSessions.authenticate(authRequest, "")
	if err != nil {
		t.Fatal(err)
	}
	objectID := result.Receipts[0].ObjectID
	edit := homeMutationRequest(server, homeSession, "memory/"+objectID+"/edit", url.Values{
		viewerSessionCSRFFormField: {homeSession.CSRFToken},
		"summary":                  {"Project B edit stays inside its signed Home session."},
		"expected_generation":      {"1"},
	})
	editResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(editResponse, edit)
	if editResponse.Code != http.StatusNoContent {
		t.Fatalf("request-scoped Home edit status=%d body=%s", editResponse.Code, editResponse.Body.String())
	}
	var summary, memoryScope string
	var generation int
	if err := vault.DB().QueryRow(`
		SELECT capsule.redacted_summary, object.memory_scope, object.logical_generation
		  FROM private_memory_objects object
		  JOIN memory_capsules capsule ON capsule.id=object.object_id
		 WHERE object.object_id=?`, objectID,
	).Scan(&summary, &memoryScope, &generation); err != nil {
		t.Fatal(err)
	}
	if summary != "Project B edit stays inside its signed Home session." || generation != 2 {
		t.Fatalf("request-scoped Home edit summary=%q generation=%d", summary, generation)
	}
	move := homeMutationRequest(server, homeSession, "memory/"+objectID+"/move", url.Values{
		viewerSessionCSRFFormField: {homeSession.CSRFToken},
		"expected_generation":      {strconv.Itoa(generation)},
		"target_scope":             {store.MemoryScopePersonalGlobal},
	})
	moveResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(moveResponse, move)
	if moveResponse.Code != http.StatusNoContent {
		t.Fatalf("request-scoped Home move status=%d body=%s", moveResponse.Code, moveResponse.Body.String())
	}
	if err := vault.DB().QueryRow(`
		SELECT memory_scope, logical_generation
		  FROM private_memory_objects WHERE object_id=?`, objectID,
	).Scan(&memoryScope, &generation); err != nil {
		t.Fatal(err)
	}
	if memoryScope != store.MemoryScopePersonalGlobal || generation != 3 {
		t.Fatalf("request-scoped Home move scope=%q generation=%d", memoryScope, generation)
	}
	remove := homeMutationRequest(server, homeSession, "memory/"+objectID+"/delete", url.Values{
		viewerSessionCSRFFormField: {homeSession.CSRFToken},
		"expected_generation":      {strconv.Itoa(generation)},
	})
	removeResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(removeResponse, remove)
	if removeResponse.Code != http.StatusNoContent {
		t.Fatalf("request-scoped Home delete status=%d body=%s", removeResponse.Code, removeResponse.Body.String())
	}
	var lifecycle string
	if err := vault.DB().QueryRow(`
		SELECT lifecycle FROM private_memory_objects WHERE object_id=?`, objectID,
	).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "deleted" {
		t.Fatalf("request-scoped Home delete lifecycle=%q", lifecycle)
	}
}

type productBindingVerifierStub struct {
	workspace, binding, repository string
	epoch                          int64
	calls                          int
}

func (value *productBindingVerifierStub) VerifyBinding(
	_ context.Context,
	workspace, binding, repository string,
	epoch int64,
) error {
	value.calls++
	value.workspace, value.binding, value.repository, value.epoch = workspace, binding, repository, epoch
	return nil
}

func setProductBindingHeaders(
	request *http.Request,
	workspace, binding, repository string,
	epoch string,
) {
	request.Header.Set(
		productWorkspaceHeader,
		base64.RawURLEncoding.EncodeToString([]byte(workspace)),
	)
	request.Header.Set(productBindingHeader, binding)
	request.Header.Set(productRepositoryHeader, repository)
	request.Header.Set(productResolverEpochHeader, epoch)
}

func TestPersonalContinuityResumeUsesVerifiedRequestBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personal.db")
	vault, err := store.OpenVault(path, store.StoreKindPersonal, "store_personal_request_scope")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	firstBinding := strings.Repeat("a", 64)
	if err := vault.ConfigureProductRuntimeAuthority(firstBinding, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(firstBinding, "repository_project_a"); err != nil {
		t.Fatal(err)
	}
	verifier := &productBindingVerifierStub{}
	server, err := New(Config{
		IPCSecret: "secret", Store: vault, ProductBindingVerifier: verifier,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	request, err := http.NewRequest(
		http.MethodPost, httpServer.URL+"/continuity/resume",
		strings.NewReader(`{"thread_id":"repository_project_b","project_id":"workspace_b","session_id":"session_b","host":"codex","token_budget":800}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Pulse-Key", "secret")
	workspace := filepath.Clean(t.TempDir())
	secondBinding := strings.Repeat("b", 64)
	setProductBindingHeaders(request, workspace, secondBinding, "repository_project_b", "3")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("request-scoped resume status=%d", response.StatusCode)
	}
	if verifier.calls != 1 || verifier.workspace != workspace || verifier.binding != secondBinding ||
		verifier.repository != "repository_project_b" || verifier.epoch != 3 {
		t.Fatalf("verified authority=%#v", verifier)
	}
}

func TestProductBindingRequestRejectsMissingAndDuplicateAuthority(t *testing.T) {
	verifier := &productBindingVerifierStub{}
	server := &Server{cfg: Config{ProductBindingVerifier: verifier}}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/continuity/resume", nil)
	recorder := httptest.NewRecorder()
	if _, ok := server.requireProductBindingAuthority(recorder, request); ok ||
		recorder.Code != http.StatusForbidden || verifier.calls != 0 {
		t.Fatalf("missing authority status=%d ok=%v calls=%d", recorder.Code, ok, verifier.calls)
	}

	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/continuity/resume", nil)
	setProductBindingHeaders(
		request, filepath.Clean(t.TempDir()), strings.Repeat("c", 64), "repository_project_c", "4",
	)
	request.Header.Add(productBindingHeader, strings.Repeat("d", 64))
	recorder = httptest.NewRecorder()
	if _, ok := server.requireProductBindingAuthority(recorder, request); ok ||
		recorder.Code != http.StatusForbidden || verifier.calls != 0 {
		t.Fatalf("duplicate authority status=%d ok=%v calls=%d", recorder.Code, ok, verifier.calls)
	}
}
