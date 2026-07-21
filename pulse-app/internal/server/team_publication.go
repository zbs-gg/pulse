package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

const (
	// TeamPublicationAirlockRoutePath is served only by the dedicated Owner
	// browser listener. It must never be mounted into TeamServer or MCP.
	TeamPublicationAirlockRoutePath = "/airlock/team-publication"
	// TeamPublicationOwnerStepUpHeader is injected by the authenticated Airlock
	// gateway after it signs the exact method, path, and canonical envelope. It
	// must never be accepted from an HTML form or exposed to the browser.
	TeamPublicationOwnerStepUpHeader = "X-Pulse-Owner-Step-Up"

	teamPublicationDecisionFormField  = "decision"
	teamPublicationEnvelopeFormField  = "canonical_envelope"
	teamPublicationDigestFormField    = "envelope_digest"
	teamPublicationStoreFormField     = "store_id"
	teamPublicationTeamFormField      = "team_id"
	teamPublicationPublisherFormField = "publisher_principal_id"
	teamPublicationRequestIDFormField = "request_id"

	teamPublicationAirlockMaxEnvelopeBytes = 64 << 10
	teamPublicationApprovalTTL             = 4 * time.Minute
)

var (
	ErrTeamPublicationStepUpInvalid  = errors.New("invalid team publication step-up")
	ErrTeamPublicationStepUpExpired  = errors.New("expired team publication step-up")
	ErrTeamPublicationStepUpReplayed = errors.New("replayed team publication step-up")
)

type TeamPublicationAirlockStore interface {
	BuildTeamPublicationApprovalDraft(context.Context, store.TeamPublicationApprovalDraftRequest) (store.TeamPublicationApprovalIssueRequest, error)
	IssueTeamPublicationApproval(context.Context, store.TeamPublicationApprovalIssueRequest) (store.TeamPublicationApprovalChallenge, error)
	CommitApprovedTeamPublication(context.Context, store.ApprovedTeamPublicationRequest) (store.TeamPublicationReceipt, error)
}

// TeamPublicationStepUpVerificationRequest binds the OS-backed human
// assertion to the exact bytes visible in the Airlock preview. Implementations
// must verify freshness and replay protection; the handler independently
// checks the returned timestamps and IssueTeamPublicationApproval persists the
// assertion identifiers under a unique replay fence.
type TeamPublicationStepUpVerificationRequest struct {
	Assertion            string
	RequestID            string
	Method               string
	Path                 string
	CanonicalEnvelope    []byte
	EnvelopeDigest       string
	StoreID              string
	TeamID               string
	PublisherPrincipalID string
}

type TeamPublicationStepUpVerifier interface {
	VerifyTeamPublication(
		context.Context,
		TeamPublicationStepUpVerificationRequest,
	) (OwnerStepUpContext, error)
}

type TeamPublicationStepUpVerifierFunc func(
	context.Context,
	TeamPublicationStepUpVerificationRequest,
) (OwnerStepUpContext, error)

func (f TeamPublicationStepUpVerifierFunc) VerifyTeamPublication(
	ctx context.Context,
	request TeamPublicationStepUpVerificationRequest,
) (OwnerStepUpContext, error) {
	return f(ctx, request)
}

type TeamPublicationWriterLeaseProvider interface {
	CurrentTeamPublicationWriterLease(context.Context) (store.TeamWriterLeaseIdentity, error)
}

type TeamPublicationWriterLeaseProviderFunc func(context.Context) (store.TeamWriterLeaseIdentity, error)

func (f TeamPublicationWriterLeaseProviderFunc) CurrentTeamPublicationWriterLease(
	ctx context.Context,
) (store.TeamWriterLeaseIdentity, error) {
	return f(ctx)
}

// TeamPublicationAirlockCandidate is the immutable disclosure received from a
// Desk. Private source identifiers are intentionally absent; only the exact
// outbound envelope and its public bindings may enter this browser surface.
type TeamPublicationAirlockCandidate struct {
	CanonicalEnvelope    []byte
	EnvelopeDigest       string
	StoreID              string
	TeamID               string
	PublisherPrincipalID string
}

type TeamPublicationAirlockServerConfig struct {
	Store                     TeamPublicationAirlockStore
	ExpectedOrigin            string
	MaxBodyBytes              int64
	Candidate                 TeamPublicationAirlockCandidate
	ApprovingOwnerPrincipalID string
	ApprovingClientKey        string
	StepUpVerifier            TeamPublicationStepUpVerifier
	WriterLeaseProvider       TeamPublicationWriterLeaseProvider
	Clock                     func() time.Time
}

type TeamPublicationAirlockServer struct {
	store                     TeamPublicationAirlockStore
	security                  *privilegedUISecurity
	candidate                 TeamPublicationAirlockCandidate
	preview                   teamPublicationEnvelopePreview
	approvingOwnerPrincipalID string
	approvingClientKey        string
	stepUpVerifier            TeamPublicationStepUpVerifier
	writerLeaseProvider       TeamPublicationWriterLeaseProvider
	clock                     func() time.Time
}

type teamPublicationEnvelopePreview struct {
	Schema            string                          `json:"schema"`
	Action            string                          `json:"action"`
	DeploymentID      string                          `json:"deployment_id"`
	StoreID           string                          `json:"store_id"`
	TeamID            string                          `json:"team_id"`
	TargetKind        string                          `json:"target_kind"`
	TargetID          string                          `json:"target_id"`
	PublicationKey    string                          `json:"publication_key"`
	PolicyEpoch       int64                           `json:"policy_epoch"`
	WriterPrincipalID string                          `json:"writer_principal_id"`
	ClientKey         string                          `json:"client_key"`
	WriterID          string                          `json:"writer_id"`
	SourceTimestamp   string                          `json:"source_timestamp"`
	Content           string                          `json:"content"`
	Metadata          *teamPublicationMetadataPreview `json:"metadata"`
}

type teamPublicationMetadataPreview struct {
	Kind string   `json:"kind"`
	Tags []string `json:"tags"`
}

func NewTeamPublicationAirlockServer(
	cfg TeamPublicationAirlockServerConfig,
) (*TeamPublicationAirlockServer, error) {
	if cfg.Store == nil || cfg.StepUpVerifier == nil || cfg.WriterLeaseProvider == nil ||
		!validTeamAdminOpaque(cfg.ApprovingOwnerPrincipalID) ||
		!strings.HasPrefix(cfg.ApprovingOwnerPrincipalID, "principal_") ||
		!validTeamAdminClientKey(cfg.ApprovingClientKey) {
		return nil, errors.New("team publication Airlock: incomplete configuration")
	}
	security, err := newPrivilegedUISecurity(cfg.ExpectedOrigin, cfg.MaxBodyBytes)
	if err != nil {
		return nil, err
	}
	preview, err := validateTeamPublicationAirlockCandidate(cfg.Candidate)
	if err != nil {
		return nil, err
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	candidate := cfg.Candidate
	candidate.CanonicalEnvelope = append([]byte(nil), cfg.Candidate.CanonicalEnvelope...)
	return &TeamPublicationAirlockServer{
		store: cfg.Store, security: security, candidate: candidate, preview: preview,
		approvingOwnerPrincipalID: cfg.ApprovingOwnerPrincipalID,
		approvingClientKey:        cfg.ApprovingClientKey, stepUpVerifier: cfg.StepUpVerifier,
		writerLeaseProvider: cfg.WriterLeaseProvider, clock: clock,
	}, nil
}

func validateTeamPublicationAirlockCandidate(
	candidate TeamPublicationAirlockCandidate,
) (teamPublicationEnvelopePreview, error) {
	invalid := func() (teamPublicationEnvelopePreview, error) {
		return teamPublicationEnvelopePreview{}, errors.New("team publication Airlock: invalid candidate")
	}
	if len(candidate.CanonicalEnvelope) < 2 || len(candidate.CanonicalEnvelope) > teamPublicationAirlockMaxEnvelopeBytes ||
		!validTeamAdminClientKey(candidate.EnvelopeDigest) ||
		!validTeamAdminOpaque(candidate.StoreID) || !strings.HasPrefix(candidate.StoreID, "store_") ||
		!validTeamAdminOpaque(candidate.TeamID) || !strings.HasPrefix(candidate.TeamID, "team_") ||
		!validTeamAdminOpaque(candidate.PublisherPrincipalID) || !strings.HasPrefix(candidate.PublisherPrincipalID, "principal_") {
		return invalid()
	}
	bindings, err := store.ParseCanonicalTeamPublicationEnvelope(candidate.CanonicalEnvelope)
	if err != nil || bindings.EnvelopeDigest != candidate.EnvelopeDigest ||
		bindings.StoreID != candidate.StoreID || bindings.TeamID != candidate.TeamID ||
		bindings.WriterPrincipalID != candidate.PublisherPrincipalID {
		return invalid()
	}
	digest := sha256.Sum256(candidate.CanonicalEnvelope)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(digest[:])), []byte(candidate.EnvelopeDigest)) != 1 {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(candidate.CanonicalEnvelope))
	decoder.DisallowUnknownFields()
	var preview teamPublicationEnvelopePreview
	if err := decoder.Decode(&preview); err != nil {
		return invalid()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return invalid()
	}
	if preview.Schema != store.TeamPublicationEnvelopeSchema || preview.Action != "team.commons.publish" ||
		preview.TargetKind != "commons" || preview.TargetID != preview.TeamID || preview.Metadata == nil ||
		preview.DeploymentID == "" || preview.PublicationKey == "" || preview.PolicyEpoch < 1 ||
		preview.WriterID == "" || preview.ClientKey == "" || preview.SourceTimestamp == "" ||
		preview.Metadata.Kind == "" || preview.StoreID != candidate.StoreID || preview.TeamID != candidate.TeamID ||
		preview.WriterPrincipalID != candidate.PublisherPrincipalID {
		return invalid()
	}
	return preview, nil
}

// ParseTeamPublicationAirlockCandidate derives the public bindings and digest
// from one exact canonical envelope. Callers cannot supply a second set of IDs
// that disagrees with the bytes rendered by the Airlock.
func ParseTeamPublicationAirlockCandidate(
	canonicalEnvelope []byte,
) (TeamPublicationAirlockCandidate, error) {
	if len(canonicalEnvelope) < 2 || len(canonicalEnvelope) > teamPublicationAirlockMaxEnvelopeBytes {
		return TeamPublicationAirlockCandidate{}, errors.New("team publication Airlock: invalid candidate")
	}
	bindings, err := store.ParseCanonicalTeamPublicationEnvelope(canonicalEnvelope)
	if err != nil {
		return TeamPublicationAirlockCandidate{}, errors.New("team publication Airlock: invalid candidate")
	}
	candidate := TeamPublicationAirlockCandidate{
		CanonicalEnvelope:    append([]byte(nil), canonicalEnvelope...),
		EnvelopeDigest:       bindings.EnvelopeDigest,
		StoreID:              bindings.StoreID,
		TeamID:               bindings.TeamID,
		PublisherPrincipalID: bindings.WriterPrincipalID,
	}
	if _, err := validateTeamPublicationAirlockCandidate(candidate); err != nil {
		return TeamPublicationAirlockCandidate{}, err
	}
	return candidate, nil
}

// Handler exposes exactly one browser page. It does not register a Team API,
// owner-admin API, CLI action, or MCP tool.
func (s *TeamPublicationAirlockServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != TeamPublicationAirlockRoutePath {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.security.page(http.HandlerFunc(s.handlePreview)).ServeHTTP(w, r)
		case http.MethodPost:
			s.security.formMutation(http.HandlerFunc(s.handleApproval)).ServeHTTP(w, r)
		default:
			s.security.page(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			})).ServeHTTP(w, r)
		}
	})
}

func (s *TeamPublicationAirlockServer) handlePreview(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	csrfToken, err := s.security.issueCSRFCookie(w)
	if err != nil {
		writeTeamPublicationAirlockError(w, http.StatusServiceUnavailable, "airlock unavailable")
		return
	}
	data := teamPublicationPreviewPage{
		Path: TeamPublicationAirlockRoutePath, CSRFToken: csrfToken,
		CanonicalEnvelope:        string(s.candidate.CanonicalEnvelope),
		CanonicalEnvelopeEncoded: base64.RawURLEncoding.EncodeToString(s.candidate.CanonicalEnvelope),
		EnvelopeDigest:           s.candidate.EnvelopeDigest, StoreID: s.candidate.StoreID,
		TeamID: s.candidate.TeamID, PublisherPrincipalID: s.candidate.PublisherPrincipalID,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := teamPublicationPreviewTemplate.Execute(w, data); err != nil {
		return
	}
}

func (s *TeamPublicationAirlockServer) handleApproval(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || !validTeamPublicationAirlockForm(r.PostForm) {
		writeTeamPublicationAirlockError(w, http.StatusBadRequest, "invalid approval request")
		return
	}
	assertion, ok := exactTeamPublicationOwnerStepUpHeader(r.Header)
	if !ok {
		writeTeamPublicationStepUpError(w, ErrTeamPublicationStepUpInvalid)
		return
	}
	encodedEnvelope := r.PostForm.Get(teamPublicationEnvelopeFormField)
	envelope, err := base64.RawURLEncoding.DecodeString(encodedEnvelope)
	if err != nil || base64.RawURLEncoding.EncodeToString(envelope) != encodedEnvelope {
		writeTeamPublicationAirlockError(w, http.StatusBadRequest, "invalid approval request")
		return
	}
	digest := r.PostForm.Get(teamPublicationDigestFormField)
	if !bytes.Equal(envelope, s.candidate.CanonicalEnvelope) ||
		subtle.ConstantTimeCompare([]byte(digest), []byte(s.candidate.EnvelopeDigest)) != 1 ||
		r.PostForm.Get(teamPublicationStoreFormField) != s.candidate.StoreID ||
		r.PostForm.Get(teamPublicationTeamFormField) != s.candidate.TeamID ||
		r.PostForm.Get(teamPublicationPublisherFormField) != s.candidate.PublisherPrincipalID {
		writeTeamPublicationAirlockError(w, http.StatusConflict, "publication changed")
		return
	}
	writer, err := s.writerLeaseProvider.CurrentTeamPublicationWriterLease(r.Context())
	if err != nil || writer.WriterID == "" || writer.Token == "" || writer.WriterID != s.preview.WriterID {
		writeTeamPublicationAirlockError(w, http.StatusServiceUnavailable, "airlock unavailable")
		return
	}
	requestID := r.PostForm.Get(teamPublicationRequestIDFormField)
	stepUp, err := s.stepUpVerifier.VerifyTeamPublication(r.Context(), TeamPublicationStepUpVerificationRequest{
		Assertion: assertion, RequestID: requestID,
		Method: r.Method, Path: r.URL.EscapedPath(),
		CanonicalEnvelope: append([]byte(nil), envelope...), EnvelopeDigest: digest,
		StoreID: s.candidate.StoreID, TeamID: s.candidate.TeamID,
		PublisherPrincipalID: s.candidate.PublisherPrincipalID,
	})
	if err != nil {
		writeTeamPublicationStepUpError(w, err)
		return
	}
	now := s.clock().UTC()
	if !validTeamPublicationStepUp(stepUp, now, s.candidate, s.approvingOwnerPrincipalID, s.approvingClientKey) {
		writeTeamPublicationStepUpError(w, ErrTeamPublicationStepUpExpired)
		return
	}
	draft, err := s.store.BuildTeamPublicationApprovalDraft(r.Context(), store.TeamPublicationApprovalDraftRequest{
		CanonicalEnvelope: append([]byte(nil), envelope...), EnvelopeDigest: digest,
		Writer: writer, ApprovingOwnerPrincipalID: s.approvingOwnerPrincipalID,
		ApprovingClientKey: s.approvingClientKey,
	})
	if err != nil {
		writeTeamPublicationStoreError(w, err)
		return
	}
	if !s.validApprovalDraft(draft, writer) {
		writeTeamPublicationAirlockError(w, http.StatusConflict, "publication changed")
		return
	}
	draft.AssertionKID = stepUp.AssertionKID
	draft.AssertionJTI = stepUp.AssertionJTI
	draft.AssertionExpiresAt = stepUp.AssertionExpiresAt.UTC()
	draft.StepUpAt = stepUp.AuthenticatedAt.UTC()
	draft.ExpiresAt = now.Add(teamPublicationApprovalTTL)
	if stepUpExpiry := draft.StepUpAt.Add(ownerStepUpMaxAge); stepUpExpiry.Before(draft.ExpiresAt) {
		draft.ExpiresAt = stepUpExpiry
	}
	challenge, err := s.store.IssueTeamPublicationApproval(r.Context(), draft)
	if err != nil {
		writeTeamPublicationStoreError(w, err)
		return
	}
	receipt, err := s.store.CommitApprovedTeamPublication(r.Context(), store.ApprovedTeamPublicationRequest{
		CanonicalEnvelope: append([]byte(nil), envelope...), EnvelopeDigest: digest,
		ApprovalNonce: challenge.Nonce, RequestID: requestID, Writer: writer,
		ApprovingOwnerPrincipalID: s.approvingOwnerPrincipalID,
		ApprovingClientKey:        s.approvingClientKey,
	})
	if err != nil {
		writeTeamPublicationStoreError(w, err)
		return
	}
	if !s.validReceipt(receipt, draft) {
		writeTeamPublicationAirlockError(w, http.StatusServiceUnavailable, "airlock unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = teamPublicationSuccessTemplate.Execute(w, receipt)
}

func validTeamPublicationAirlockForm(values url.Values) bool {
	if len(values) != 8 {
		return false
	}
	for _, name := range []string{
		privilegedUICSRFFormField, teamPublicationDecisionFormField,
		teamPublicationEnvelopeFormField, teamPublicationDigestFormField,
		teamPublicationStoreFormField, teamPublicationTeamFormField,
		teamPublicationPublisherFormField, teamPublicationRequestIDFormField,
	} {
		entries := values[name]
		if len(entries) != 1 || entries[0] == "" {
			return false
		}
	}
	return values.Get(teamPublicationDecisionFormField) == "approve" &&
		validOwnerAdminRequestID(values.Get(teamPublicationRequestIDFormField))
}

func exactTeamPublicationOwnerStepUpHeader(header http.Header) (string, bool) {
	values := header.Values(TeamPublicationOwnerStepUpHeader)
	if len(values) != 1 {
		return "", false
	}
	assertion := values[0]
	return assertion, assertion != "" && strings.TrimSpace(assertion) == assertion &&
		len(assertion) <= PrincipalAssertionMaxBytes && !strings.Contains(assertion, ",")
}

func validTeamPublicationStepUp(
	stepUp OwnerStepUpContext,
	now time.Time,
	candidate TeamPublicationAirlockCandidate,
	ownerPrincipalID, ownerClientKey string,
) bool {
	authenticatedAt := stepUp.AuthenticatedAt.UTC()
	assertionExpiresAt := stepUp.AssertionExpiresAt.UTC()
	return !stepUp.Identity.Bootstrap && stepUp.Identity.StoreID == candidate.StoreID &&
		stepUp.Identity.TeamID == candidate.TeamID && stepUp.Identity.OwnerPrincipalID == ownerPrincipalID &&
		subtle.ConstantTimeCompare([]byte(stepUp.Identity.ClientKey), []byte(ownerClientKey)) == 1 &&
		stepUp.AssertionKID != "" && len(stepUp.AssertionKID) <= 1024 &&
		strings.TrimSpace(stepUp.AssertionKID) == stepUp.AssertionKID &&
		stepUp.AssertionJTI != "" && len(stepUp.AssertionJTI) <= 1024 &&
		strings.TrimSpace(stepUp.AssertionJTI) == stepUp.AssertionJTI &&
		!authenticatedAt.IsZero() && !assertionExpiresAt.IsZero() &&
		!authenticatedAt.After(now.Add(30*time.Second)) && now.Sub(authenticatedAt) <= ownerStepUpMaxAge &&
		assertionExpiresAt.After(now) && assertionExpiresAt.Sub(now) <= ownerStepUpMaxAssertionTTL
}

func (s *TeamPublicationAirlockServer) validApprovalDraft(
	draft store.TeamPublicationApprovalIssueRequest,
	writer store.TeamWriterLeaseIdentity,
) bool {
	return draft.DeploymentID == s.preview.DeploymentID && draft.StoreID == s.candidate.StoreID &&
		draft.TeamID == s.candidate.TeamID && draft.EnvelopeDigest == s.candidate.EnvelopeDigest &&
		draft.PublisherPrincipalID == s.candidate.PublisherPrincipalID &&
		subtle.ConstantTimeCompare([]byte(draft.PublisherClientKey), []byte(s.preview.ClientKey)) == 1 &&
		draft.Writer.WriterID == writer.WriterID &&
		subtle.ConstantTimeCompare([]byte(draft.Writer.Token), []byte(writer.Token)) == 1 &&
		draft.ApprovingOwnerPrincipalID == s.approvingOwnerPrincipalID &&
		subtle.ConstantTimeCompare([]byte(draft.ApprovingClientKey), []byte(s.approvingClientKey)) == 1 &&
		draft.PolicyEpoch == s.preview.PolicyEpoch && draft.GlobalEpoch > 0 &&
		draft.SharedProjectID != "" && draft.PublisherMembershipID != "" && draft.PublisherBindingID != "" &&
		validTeamAdminDigest(draft.IdempotencyKeyHash) && validTeamAdminDigest(draft.OperationDigest)
}

func (s *TeamPublicationAirlockServer) validReceipt(
	receipt store.TeamPublicationReceipt,
	draft store.TeamPublicationApprovalIssueRequest,
) bool {
	return receipt.PublicationID != "" && receipt.DeploymentID == draft.DeploymentID &&
		receipt.StoreID == draft.StoreID && receipt.TeamID == draft.TeamID &&
		receipt.SharedProjectID == draft.SharedProjectID && receipt.EnvelopeDigest == draft.EnvelopeDigest &&
		receipt.OperationDigest == draft.OperationDigest &&
		receipt.PublisherPrincipalID == draft.PublisherPrincipalID &&
		receipt.PublisherMembershipID == draft.PublisherMembershipID &&
		subtle.ConstantTimeCompare([]byte(receipt.PublisherClientKey), []byte(draft.PublisherClientKey)) == 1 &&
		receipt.PublisherBindingID == draft.PublisherBindingID &&
		receipt.ApprovingOwnerPrincipalID == draft.ApprovingOwnerPrincipalID &&
		receipt.ApprovalAuditEventID != "" && receipt.ObjectID != "" && receipt.CapsuleID != "" &&
		receipt.ObjectAuditEventID != "" && receipt.EventProjectionJobID != "" &&
		receipt.EmbeddingProjectionJobID != "" && validTeamAdminDigest(receipt.ReceiptDigest)
}

func writeTeamPublicationStepUpError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrTeamPublicationStepUpReplayed) || errors.Is(err, store.ErrTeamPublicationApprovalReplay) {
		writeTeamPublicationAirlockError(w, http.StatusConflict, "step-up replayed")
		return
	}
	writeTeamPublicationAirlockError(w, http.StatusUnauthorized, "step-up rejected")
}

func writeTeamPublicationStoreError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "airlock unavailable"
	switch {
	case errors.Is(err, store.ErrTeamPublicationApprovalReplay):
		status, code = http.StatusConflict, "step-up replayed"
	case errors.Is(err, store.ErrTeamPublicationApprovalExpired), errors.Is(err, store.ErrOwnerStepUpStale):
		status, code = http.StatusForbidden, "approval expired"
	case errors.Is(err, store.ErrTeamPublicationApprovalBindingMismatch),
		errors.Is(err, store.ErrTeamPublicationTargetMismatch),
		errors.Is(err, store.ErrTeamPublicationIdempotencyConflict):
		status, code = http.StatusConflict, "publication changed"
	case errors.Is(err, store.ErrTeamPublicationInvalid),
		errors.Is(err, store.ErrTeamPublicationApprovalInvalid):
		status, code = http.StatusBadRequest, "invalid approval request"
	case errors.Is(err, store.ErrHumanOwnerRequired):
		status, code = http.StatusForbidden, "owner required"
	}
	writeTeamPublicationAirlockError(w, status, code)
}

func writeTeamPublicationAirlockError(w http.ResponseWriter, status int, code string) {
	http.Error(w, code, status)
}

type teamPublicationPreviewPage struct {
	Path                     string
	CSRFToken                string
	CanonicalEnvelope        string
	CanonicalEnvelopeEncoded string
	EnvelopeDigest           string
	StoreID                  string
	TeamID                   string
	PublisherPrincipalID     string
}

var teamPublicationPreviewTemplate = template.Must(template.New("team-publication-preview").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Pulse Airlock</title></head>
<body><main><h1>Publish to Team Commons</h1>
<p>Every byte below is the outbound Team Commons envelope. Review it before approving.</p>
<p><strong>Envelope SHA-256:</strong> <code>{{.EnvelopeDigest}}</code></p>
<pre id="canonical-envelope">{{.CanonicalEnvelope}}</pre>
<form method="post" action="{{.Path}}" autocomplete="off">
<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
<input type="hidden" name="decision" value="approve">
<input type="hidden" name="canonical_envelope" value="{{.CanonicalEnvelopeEncoded}}">
<input type="hidden" name="envelope_digest" value="{{.EnvelopeDigest}}">
<input type="hidden" name="store_id" value="{{.StoreID}}">
<input type="hidden" name="team_id" value="{{.TeamID}}">
<input type="hidden" name="publisher_principal_id" value="{{.PublisherPrincipalID}}">
<label>Approval request ID <input name="request_id" required></label>
<button type="submit">Approve and publish these exact bytes</button>
</form></main></body></html>`))

var teamPublicationSuccessTemplate = template.Must(template.New("team-publication-success").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Pulse Airlock receipt</title></head>
<body><main><h1>Published to Team Commons</h1>
<dl><dt>Publication</dt><dd><code>{{.PublicationID}}</code></dd>
<dt>Envelope SHA-256</dt><dd><code>{{.EnvelopeDigest}}</code></dd>
<dt>Receipt SHA-256</dt><dd><code>{{.ReceiptDigest}}</code></dd>
<dt>Object</dt><dd><code>{{.ObjectID}}</code></dd>
<dt>Capsule</dt><dd><code>{{.CapsuleID}}</code></dd></dl>
</main></body></html>`))
