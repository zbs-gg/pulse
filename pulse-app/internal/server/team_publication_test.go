package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

type publicationStoreStub struct {
	draft       store.TeamPublicationApprovalIssueRequest
	draftErr    error
	issueErr    error
	commitErr   error
	draftCalls  []store.TeamPublicationApprovalDraftRequest
	issueCalls  []store.TeamPublicationApprovalIssueRequest
	commitCalls []store.ApprovedTeamPublicationRequest
}

func (s *publicationStoreStub) BuildTeamPublicationApprovalDraft(
	_ context.Context,
	request store.TeamPublicationApprovalDraftRequest,
) (store.TeamPublicationApprovalIssueRequest, error) {
	s.draftCalls = append(s.draftCalls, clonePublicationDraftRequest(request))
	if s.draftErr != nil {
		return store.TeamPublicationApprovalIssueRequest{}, s.draftErr
	}
	draft := s.draft
	draft.Writer = request.Writer
	return draft, nil
}

func (s *publicationStoreStub) IssueTeamPublicationApproval(
	_ context.Context,
	request store.TeamPublicationApprovalIssueRequest,
) (store.TeamPublicationApprovalChallenge, error) {
	s.issueCalls = append(s.issueCalls, request)
	if s.issueErr != nil {
		return store.TeamPublicationApprovalChallenge{}, s.issueErr
	}
	return store.TeamPublicationApprovalChallenge{
		Nonce: strings.Repeat("a", 64), ExpiresAt: request.ExpiresAt,
	}, nil
}

func (s *publicationStoreStub) CommitApprovedTeamPublication(
	_ context.Context,
	request store.ApprovedTeamPublicationRequest,
) (store.TeamPublicationReceipt, error) {
	request.CanonicalEnvelope = append([]byte(nil), request.CanonicalEnvelope...)
	s.commitCalls = append(s.commitCalls, request)
	if s.commitErr != nil {
		return store.TeamPublicationReceipt{}, s.commitErr
	}
	return store.TeamPublicationReceipt{
		PublicationID:             "publication_test",
		DeploymentID:              "deployment_test",
		StoreID:                   "store_test",
		TeamID:                    "team_test",
		SharedProjectID:           "project_test",
		EnvelopeDigest:            testPublicationDigest(request.CanonicalEnvelope),
		OperationDigest:           strings.Repeat("b", 64),
		PublisherPrincipalID:      "principal_publisher",
		PublisherMembershipID:     "membership_publisher",
		PublisherClientKey:        strings.Repeat("c", 64),
		PublisherBindingID:        "binding_publisher",
		ApprovingOwnerPrincipalID: "principal_owner",
		ApprovalAuditEventID:      "audit_approval",
		ObjectID:                  "object_publication",
		CapsuleID:                 "team_capsule_publication",
		ObjectAuditEventID:        "audit_object",
		EventProjectionJobID:      "job_event",
		EmbeddingProjectionJobID:  "job_embedding",
		ReceiptDigest:             strings.Repeat("d", 64),
		CreatedAt:                 testPublicationNow,
	}, nil
}

func clonePublicationDraftRequest(request store.TeamPublicationApprovalDraftRequest) store.TeamPublicationApprovalDraftRequest {
	request.CanonicalEnvelope = append([]byte(nil), request.CanonicalEnvelope...)
	return request
}

var testPublicationNow = time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)

func testPublicationEnvelope(content string) []byte {
	encodedContent, _ := json.Marshal(content)
	return []byte(`{"action":"team.commons.publish","client_key":"` + strings.Repeat("c", 64) + `","content":` + string(encodedContent) + `,"deployment_id":"deployment_test","metadata":{"kind":"fact","tags":["synthetic","team"]},"policy_epoch":7,"publication_key":"publish_test_12345678","schema":"pulse.team.airlock_envelope.v1","source_timestamp":"2026-07-15T08:00:00.000Z","store_id":"store_test","target_id":"team_test","target_kind":"commons","team_id":"team_test","writer_id":"writer_test","writer_principal_id":"principal_publisher"}`)
}

func testPublicationDigest(envelope []byte) string {
	digest := sha256.Sum256(envelope)
	return hex.EncodeToString(digest[:])
}

func newPublicationAirlockFixture(t *testing.T, content string) (*TeamPublicationAirlockServer, *publicationStoreStub, []byte) {
	t.Helper()
	envelope := testPublicationEnvelope(content)
	digest := testPublicationDigest(envelope)
	backend := &publicationStoreStub{draft: store.TeamPublicationApprovalIssueRequest{
		DeploymentID:              "deployment_test",
		StoreID:                   "store_test",
		TeamID:                    "team_test",
		SharedProjectID:           "project_test",
		EnvelopeDigest:            digest,
		IdempotencyKeyHash:        strings.Repeat("e", 64),
		OperationDigest:           strings.Repeat("b", 64),
		PublisherPrincipalID:      "principal_publisher",
		PublisherMembershipID:     "membership_publisher",
		PublisherClientKey:        strings.Repeat("c", 64),
		PublisherBindingID:        "binding_publisher",
		ApprovingOwnerPrincipalID: "principal_owner",
		ApprovingClientKey:        strings.Repeat("f", 64),
		PolicyEpoch:               7,
		GlobalEpoch:               11,
	}}
	verifier := TeamPublicationStepUpVerifierFunc(func(
		_ context.Context,
		request TeamPublicationStepUpVerificationRequest,
	) (OwnerStepUpContext, error) {
		if request.Assertion != "os-backed-assertion" || request.RequestID != "request-publication-123" ||
			request.Method != http.MethodPost || request.Path != TeamPublicationAirlockRoutePath ||
			!bytes.Equal(request.CanonicalEnvelope, envelope) || request.EnvelopeDigest != digest ||
			request.StoreID != "store_test" || request.TeamID != "team_test" ||
			request.PublisherPrincipalID != "principal_publisher" {
			return OwnerStepUpContext{}, ErrTeamPublicationStepUpInvalid
		}
		return OwnerStepUpContext{
			Identity: store.OwnerStepUpIdentity{
				StoreID: "store_test", TeamID: "team_test",
				OwnerPrincipalID: "principal_owner", ClientKey: strings.Repeat("f", 64),
			},
			AuthenticatedAt: testPublicationNow.Add(-time.Minute),
			AssertionKID:    "os-key", AssertionJTI: "assertion-publication-123",
			AssertionExpiresAt: testPublicationNow.Add(20 * time.Second),
		}, nil
	})
	writer := TeamPublicationWriterLeaseProviderFunc(func(context.Context) (store.TeamWriterLeaseIdentity, error) {
		return store.TeamWriterLeaseIdentity{WriterID: "writer_test", Token: "writer-lease-token"}, nil
	})
	server, err := NewTeamPublicationAirlockServer(TeamPublicationAirlockServerConfig{
		Store: backend, ExpectedOrigin: testPrivilegedUIOrigin, MaxBodyBytes: 96 << 10,
		Candidate: TeamPublicationAirlockCandidate{
			CanonicalEnvelope: envelope, EnvelopeDigest: digest,
			StoreID: "store_test", TeamID: "team_test",
			PublisherPrincipalID: "principal_publisher",
		},
		ApprovingOwnerPrincipalID: "principal_owner",
		ApprovingClientKey:        strings.Repeat("f", 64),
		StepUpVerifier:            verifier,
		WriterLeaseProvider:       writer,
		Clock:                     func() time.Time { return testPublicationNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, backend, envelope
}

func TestTeamPublicationAirlockPreviewShowsExactFullEnvelopeAndEscapesStoredText(t *testing.T) {
	server, _, envelope := newPublicationAirlockFixture(t, `A < B & C`)
	request := httptest.NewRequest(http.MethodGet, testPrivilegedUIOrigin+TeamPublicationAirlockRoutePath, nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("preview = %d %q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "A < B") || strings.Contains(response.Body.String(), "A & B") {
		t.Fatalf("stored markup was executable: %q", response.Body.String())
	}
	visible := html.UnescapeString(response.Body.String())
	if !strings.Contains(visible, string(envelope)) ||
		!strings.Contains(visible, testPublicationDigest(envelope)) ||
		!strings.Contains(visible, "Every byte below is the outbound Team Commons envelope") {
		t.Fatalf("preview does not show exact envelope and digest: %q", visible)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != privilegedUICSRFCookieName {
		t.Fatalf("preview cookies = %#v", cookies)
	}
	assertPrivilegedUIHeaders(t, response.Header())
}

func TestParseTeamPublicationAirlockCandidateDerivesBindingsFromExactBytes(t *testing.T) {
	envelope := testPublicationEnvelope("A shared synthetic fact")
	candidate, err := ParseTeamPublicationAirlockCandidate(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(candidate.CanonicalEnvelope, envelope) ||
		candidate.EnvelopeDigest != testPublicationDigest(envelope) ||
		candidate.StoreID != "store_test" || candidate.TeamID != "team_test" ||
		candidate.PublisherPrincipalID != "principal_publisher" {
		t.Fatalf("candidate = %+v", candidate)
	}
	tampered := append(append([]byte(nil), envelope...), ' ')
	if _, err := ParseTeamPublicationAirlockCandidate(tampered); err == nil {
		t.Fatal("non-canonical trailing bytes were accepted")
	}
}

func TestTeamPublicationAirlockApprovesOnceAndCommitsExactPreviewedBytes(t *testing.T) {
	server, backend, envelope := newPublicationAirlockFixture(t, "A shared synthetic fact")
	response := servePublicationAirlockForm(t, server, validPublicationAirlockForm(server, envelope), nil)

	if response.Code != http.StatusCreated {
		t.Fatalf("publish = %d %q", response.Code, response.Body.String())
	}
	if len(backend.draftCalls) != 1 || len(backend.issueCalls) != 1 || len(backend.commitCalls) != 1 {
		t.Fatalf("calls draft=%d issue=%d commit=%d", len(backend.draftCalls), len(backend.issueCalls), len(backend.commitCalls))
	}
	if !bytes.Equal(backend.draftCalls[0].CanonicalEnvelope, envelope) ||
		!bytes.Equal(backend.commitCalls[0].CanonicalEnvelope, envelope) ||
		backend.draftCalls[0].EnvelopeDigest != testPublicationDigest(envelope) ||
		backend.commitCalls[0].EnvelopeDigest != testPublicationDigest(envelope) ||
		backend.issueCalls[0].AssertionKID != "os-key" ||
		backend.issueCalls[0].AssertionJTI != "assertion-publication-123" ||
		backend.commitCalls[0].ApprovalNonce != strings.Repeat("a", 64) {
		t.Fatalf("approval did not bind exact preview: draft=%+v issue=%+v commit=%+v", backend.draftCalls[0], backend.issueCalls[0], backend.commitCalls[0])
	}
	if strings.Contains(response.Body.String(), "A shared synthetic fact") ||
		!strings.Contains(response.Body.String(), "publication_test") ||
		!strings.Contains(response.Body.String(), testPublicationDigest(envelope)) {
		t.Fatalf("success page leaked content or omitted receipt: %q", response.Body.String())
	}
}

func TestTeamPublicationAirlockRequiresOneGatewayInjectedStepUpHeader(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{name: "missing", mutate: func(request *http.Request) {
			request.Header.Del(TeamPublicationOwnerStepUpHeader)
		}},
		{name: "duplicate", mutate: func(request *http.Request) {
			request.Header.Add(TeamPublicationOwnerStepUpHeader, "second-assertion")
		}},
		{name: "comma joined", mutate: func(request *http.Request) {
			request.Header.Set(TeamPublicationOwnerStepUpHeader, "first,second")
		}},
		{name: "whitespace padded", mutate: func(request *http.Request) {
			request.Header.Set(TeamPublicationOwnerStepUpHeader, " os-backed-assertion")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, backend, envelope := newPublicationAirlockFixture(t, "A shared synthetic fact")
			response := servePublicationAirlockForm(t, server, validPublicationAirlockForm(server, envelope), test.mutate)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d body=%q", response.Code, response.Body.String())
			}
			if len(backend.draftCalls)+len(backend.issueCalls)+len(backend.commitCalls) != 0 {
				t.Fatal("invalid gateway assertion reached publication store")
			}
		})
	}
}

func TestTeamPublicationAirlockRejectsUnsafeBrowserRequestShapes(t *testing.T) {
	server, backend, envelope := newPublicationAirlockFixture(t, "A shared synthetic fact")
	valid := validPublicationAirlockForm(server, envelope)
	token, cookie := publicationAirlockCSRF(t, server)
	valid.Set(privilegedUICSRFFormField, token)

	tests := []struct {
		name        string
		method      string
		host        string
		origin      string
		contentType string
		cookie      *http.Cookie
		form        url.Values
		want        int
	}{
		{name: "wrong host", method: http.MethodPost, host: "evil.example", origin: testPrivilegedUIOrigin, contentType: privilegedUIFormMediaType, cookie: cookie, form: valid, want: http.StatusForbidden},
		{name: "wrong origin", method: http.MethodPost, host: "airlock.pulse.example", origin: "https://evil.example", contentType: privilegedUIFormMediaType, cookie: cookie, form: valid, want: http.StatusForbidden},
		{name: "missing csrf", method: http.MethodPost, host: "airlock.pulse.example", origin: testPrivilegedUIOrigin, contentType: privilegedUIFormMediaType, form: valid, want: http.StatusForbidden},
		{name: "wrong method", method: http.MethodPut, host: "airlock.pulse.example", origin: testPrivilegedUIOrigin, contentType: privilegedUIFormMediaType, cookie: cookie, form: valid, want: http.StatusMethodNotAllowed},
		{name: "wrong content type", method: http.MethodPost, host: "airlock.pulse.example", origin: testPrivilegedUIOrigin, contentType: "application/json", cookie: cookie, form: valid, want: http.StatusUnsupportedMediaType},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, testPrivilegedUIOrigin+TeamPublicationAirlockRoutePath, strings.NewReader(test.form.Encode()))
			request.Host = test.host
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Content-Type", test.contentType)
			if test.cookie != nil {
				request.AddCookie(test.cookie)
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d body=%q", response.Code, response.Body.String())
			}
		})
	}
	if len(backend.draftCalls)+len(backend.issueCalls)+len(backend.commitCalls) != 0 {
		t.Fatal("unsafe browser request reached publication store")
	}
}

func TestTeamPublicationAirlockRejectsAnyChangedDisclosureBinding(t *testing.T) {
	changes := []struct {
		name  string
		field string
		value func([]byte) string
	}{
		{name: "bytes", field: teamPublicationEnvelopeFormField, value: func(envelope []byte) string {
			changed := append([]byte(nil), envelope...)
			changed[len(changed)-2] = 'x'
			return base64.RawURLEncoding.EncodeToString(changed)
		}},
		{name: "digest", field: teamPublicationDigestFormField, value: func([]byte) string { return strings.Repeat("0", 64) }},
		{name: "team", field: teamPublicationTeamFormField, value: func([]byte) string { return "team_other" }},
		{name: "principal", field: teamPublicationPublisherFormField, value: func([]byte) string { return "principal_other" }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			server, backend, envelope := newPublicationAirlockFixture(t, "A shared synthetic fact")
			form := validPublicationAirlockForm(server, envelope)
			form.Set(change.field, change.value(envelope))
			response := servePublicationAirlockForm(t, server, form, nil)
			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d body=%q", response.Code, response.Body.String())
			}
			if len(backend.draftCalls)+len(backend.issueCalls)+len(backend.commitCalls) != 0 {
				t.Fatal("changed disclosure reached publication store")
			}
		})
	}
}

func TestTeamPublicationAirlockRejectsExpiredAndReplayedStepUp(t *testing.T) {
	tests := []struct {
		name   string
		verify func(context.Context, TeamPublicationStepUpVerificationRequest) (OwnerStepUpContext, error)
		want   int
	}{
		{name: "expired", want: http.StatusUnauthorized, verify: func(context.Context, TeamPublicationStepUpVerificationRequest) (OwnerStepUpContext, error) {
			return OwnerStepUpContext{Identity: store.OwnerStepUpIdentity{
				StoreID: "store_test", TeamID: "team_test", OwnerPrincipalID: "principal_owner", ClientKey: strings.Repeat("f", 64),
			}, AuthenticatedAt: testPublicationNow.Add(-10 * time.Minute), AssertionKID: "os-key", AssertionJTI: "expired", AssertionExpiresAt: testPublicationNow.Add(-time.Second)}, nil
		}},
		{name: "replayed", want: http.StatusConflict, verify: func(context.Context, TeamPublicationStepUpVerificationRequest) (OwnerStepUpContext, error) {
			return OwnerStepUpContext{}, ErrTeamPublicationStepUpReplayed
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, backend, envelope := newPublicationAirlockFixture(t, "A shared synthetic fact")
			server.stepUpVerifier = TeamPublicationStepUpVerifierFunc(test.verify)
			response := servePublicationAirlockForm(t, server, validPublicationAirlockForm(server, envelope), nil)
			if response.Code != test.want {
				t.Fatalf("status = %d body=%q", response.Code, response.Body.String())
			}
			if len(backend.issueCalls)+len(backend.commitCalls) != 0 {
				t.Fatal("invalid step-up created publication authority")
			}
		})
	}
}

func TestTeamPublicationAirlockStoreFailureNeverRendersSuccessOrContent(t *testing.T) {
	server, backend, envelope := newPublicationAirlockFixture(t, "failure content must not render")
	backend.commitErr = errors.New("database failed with failure content must not render")
	response := servePublicationAirlockForm(t, server, validPublicationAirlockForm(server, envelope), nil)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "failure content must not render") ||
		strings.Contains(response.Body.String(), "published") {
		t.Fatalf("store failure response = %d %q", response.Code, response.Body.String())
	}
	if len(backend.issueCalls) != 1 || len(backend.commitCalls) != 1 {
		t.Fatalf("unexpected store call sequence: issue=%d commit=%d", len(backend.issueCalls), len(backend.commitCalls))
	}
}

func TestTeamPublicationAirlockHandlerIsOnlyTheDedicatedBrowserPath(t *testing.T) {
	server, _, _ := newPublicationAirlockFixture(t, "A shared synthetic fact")
	for _, path := range []string{"/", "/team/v1/memory", OwnerApprovalRoutePath, "/mcp", TeamPublicationAirlockRoutePath + "/extra"} {
		request := httptest.NewRequest(http.MethodGet, testPrivilegedUIOrigin+path, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("path %q exposed status %d", path, response.Code)
		}
	}
}

func validPublicationAirlockForm(server *TeamPublicationAirlockServer, envelope []byte) url.Values {
	return url.Values{
		teamPublicationDecisionFormField:  {"approve"},
		teamPublicationEnvelopeFormField:  {base64.RawURLEncoding.EncodeToString(envelope)},
		teamPublicationDigestFormField:    {testPublicationDigest(envelope)},
		teamPublicationStoreFormField:     {"store_test"},
		teamPublicationTeamFormField:      {"team_test"},
		teamPublicationPublisherFormField: {"principal_publisher"},
		teamPublicationRequestIDFormField: {"request-publication-123"},
	}
}

func publicationAirlockCSRF(t *testing.T, server *TeamPublicationAirlockServer) (string, *http.Cookie) {
	t.Helper()
	response := httptest.NewRecorder()
	token, err := server.security.issueCSRFCookie(response)
	if err != nil {
		t.Fatal(err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("csrf cookies = %d", len(cookies))
	}
	return token, cookies[0]
}

func servePublicationAirlockForm(
	t *testing.T,
	server *TeamPublicationAirlockServer,
	form url.Values,
	mutate func(*http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	token, cookie := publicationAirlockCSRF(t, server)
	form = cloneURLValues(form)
	form.Set(privilegedUICSRFFormField, token)
	request := httptest.NewRequest(http.MethodPost, testPrivilegedUIOrigin+TeamPublicationAirlockRoutePath, strings.NewReader(form.Encode()))
	request.Header.Set("Origin", testPrivilegedUIOrigin)
	request.Header.Set("Content-Type", privilegedUIFormMediaType)
	request.Header.Set(TeamPublicationOwnerStepUpHeader, "os-backed-assertion")
	request.AddCookie(cookie)
	if mutate != nil {
		mutate(request)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func cloneURLValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, entries := range values {
		cloned[key] = append([]string(nil), entries...)
	}
	return cloned
}
