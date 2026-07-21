package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

func newOwnerAdminServerFixture(t *testing.T, fixture *principalFixture) *OwnerAdminServer {
	t.Helper()
	stepUp := newOwnerStepUpVerifierFixture(t, fixture)
	lease, err := fixture.store.AcquireTeamWriterLease(context.Background(), store.TeamWriterLeaseRequest{
		WriterID: "owner-admin-server-test", WriterVersion: teamauth.SchemaVersion, TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewOwnerAdminServer(OwnerAdminServerConfig{
		IPCSecret: testTeamIPCSecret, Store: fixture.store, StepUpVerifier: stepUp,
		WriterLease: &lease, Clock: func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func serveOwnerAdminRequest(
	handler http.Handler,
	path string,
	body []byte,
	requestID, assertion string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:57000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Pulse-Key", testTeamIPCSecret)
	if requestID != "" {
		request.Header.Set("X-Pulse-Request-ID", requestID)
	}
	if assertion != "" {
		request.Header.Set("X-Pulse-Owner-Step-Up", assertion)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func activationOwnerApprovalBody(fixture *principalFixture, gateDigest string) []byte {
	targetDigest := store.SyntheticActivationTargetDigest(fixture.storeID, fixture.teamID, gateDigest)
	return []byte(fmt.Sprintf(
		`{"schema":"pulse.team.owner.approval.v1","action":"team.activation.synthetic","store_id":%q,"team_id":%q,"target_kind":"team_activation","target_id":%q,"target_digest":%q,"gate_digest":%q}`,
		fixture.storeID, fixture.teamID, fixture.teamID, targetDigest, gateDigest,
	))
}

func signOwnerApprovalAssertion(
	t *testing.T,
	fixture *principalFixture,
	jti string,
	body []byte,
	action, storeID, teamID string,
) string {
	t.Helper()
	claims := ownerStepUpClaims(fixture, jti, body)
	claims["action"] = action
	claims["store_id"] = storeID
	claims["team_id"] = teamID
	return signPrincipalAssertion(
		t, fixture.private, "active", map[string]any{"typ": OwnerStepUpAssertionVersion}, claims,
	)
}

func TestOwnerAdminApprovalAndActivationRequireBrowserStepUpThenConsumeOnce(t *testing.T) {
	fixture := newPrincipalFixture(t)
	server := newOwnerAdminServerFixture(t, fixture)
	gate := fmt.Sprintf("%x", sha256.Sum256([]byte("synthetic-ae1-ae12-green")))
	approvalBody := activationOwnerApprovalBody(fixture, gate)

	missingStepUp := serveOwnerAdminRequest(
		server.Handler(), OwnerApprovalRoutePath, approvalBody, "req-owner-missing-step-up", "",
	)
	if missingStepUp.Code != http.StatusUnauthorized ||
		missingStepUp.Body.String() != "{\"error\":\"invalid_owner_step_up\",\"fallback\":false}\n" {
		t.Fatalf("missing step-up = %d %q", missingStepUp.Code, missingStepUp.Body.String())
	}

	assertion := signOwnerApprovalAssertion(
		t, fixture, "owner-activation-approval", approvalBody,
		store.OwnerActionSyntheticActivate, fixture.storeID, fixture.teamID,
	)
	approved := serveOwnerAdminRequest(
		server.Handler(), OwnerApprovalRoutePath, approvalBody,
		"req-owner-activation-approval", assertion,
	)
	if approved.Code != http.StatusOK {
		t.Fatalf("approval = %d %q", approved.Code, approved.Body.String())
	}
	var approval map[string]any
	if err := json.Unmarshal(approved.Body.Bytes(), &approval); err != nil {
		t.Fatal(err)
	}
	if got := sortedAdminMapKeys(approval); !reflect.DeepEqual(got, []string{
		"action", "approval_nonce", "expires_at", "fallback", "schema", "store_id",
		"target_id", "target_kind", "team_id",
	}) {
		t.Fatalf("approval fields = %v", got)
	}
	nonce, _ := approval["approval_nonce"].(string)
	if approval["schema"] != OwnerApprovalResultSchema || approval["action"] != store.OwnerActionSyntheticActivate ||
		approval["store_id"] != fixture.storeID || approval["team_id"] != fixture.teamID ||
		!validTeamAdminClientKey(nonce) || approval["fallback"] != false {
		t.Fatalf("approval result = %v", approval)
	}

	activateBody := []byte(fmt.Sprintf(
		`{"schema":"pulse.team.owner.activate.v1","approval_nonce":%q,"gate_digest":%q}`,
		nonce, gate,
	))
	activated := serveOwnerAdminRequest(
		server.Handler(), OwnerActivateRoutePath, activateBody, "req-owner-activate", "",
	)
	if activated.Code != http.StatusOK {
		t.Fatalf("activate = %d %q", activated.Code, activated.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(activated.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["schema"] != OwnerActivateResultSchema || result["store_id"] != fixture.storeID ||
		result["team_id"] != fixture.teamID || result["activation_state"] != "active" ||
		result["content_boundary"] != "synthetic" || result["public_enabled"] != true ||
		result["gate_digest"] != gate || result["activated_by_principal_id"] != fixture.ownerID ||
		result["fallback"] != false {
		t.Fatalf("activation result = %v", result)
	}

	replayed := serveOwnerAdminRequest(
		server.Handler(), OwnerActivateRoutePath, activateBody, "req-owner-activate-replay", "",
	)
	if replayed.Code != http.StatusConflict ||
		replayed.Body.String() != "{\"error\":\"owner_approval_replayed\",\"fallback\":false}\n" {
		t.Fatalf("activation replay = %d %q", replayed.Code, replayed.Body.String())
	}
}

func TestOwnerAdminBootstrapPrepareReturnsOpaqueInactiveIntentWithoutMutation(t *testing.T) {
	now := time.Date(2026, 7, 12, 7, 0, 0, 0, time.UTC)
	root := teamauth.BootstrapRoot{
		Issuer: "https://issuer.example", Subject: "bootstrap-owner", AdminClientID: "bootstrap-admin",
	}
	teamStore, err := store.OpenTeam(filepath.Join(t.TempDir(), "team.db"), store.TeamOpenOptions{
		ExpectedBootstrapRoot: root, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer teamStore.Close()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	stepUp, err := NewOwnerStepUpVerifier(OwnerStepUpVerifierConfig{
		Store: teamStore,
		Keyring: PrincipalVerifyKeyring{ActiveKid: "active", Keys: map[string]ed25519.PublicKey{
			"active": public,
		}},
		ExpectedRoot: root, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewOwnerAdminServer(OwnerAdminServerConfig{
		IPCSecret: testTeamIPCSecret, Store: teamStore, StepUpVerifier: stepUp,
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schema":"pulse.team.owner.bootstrap.v1","operation":"prepare"}`)
	prepared := serveOwnerAdminRequest(server.Handler(), OwnerBootstrapRoutePath, body, "", "")
	if prepared.Code != http.StatusOK {
		t.Fatalf("prepare = %d %q", prepared.Code, prepared.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(prepared.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	intent, ok := result["bootstrap_intent"].(map[string]any)
	if !ok || result["schema"] != OwnerBootstrapResultSchema || result["operation"] != "prepared" ||
		result["fallback"] != false || len(intent) != 4 {
		t.Fatalf("prepare result = %v", result)
	}
	for _, key := range []string{"store_id", "team_id", "owner_principal_id", "owner_membership_id"} {
		value, _ := intent[key].(string)
		if !validTeamAdminOpaque(value) {
			t.Fatalf("invalid intent %s=%q", key, value)
		}
	}
	var markers int
	if err := teamStore.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM team_stores`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 0 {
		t.Fatal("prepare mutated or bootstrapped the team store")
	}

	teamName := "Synthetic Browser Pilot"
	bootstrapIntent := store.TeamBootstrapIntent{
		StoreID: intent["store_id"].(string), TeamID: intent["team_id"].(string),
		OwnerPrincipalID:  intent["owner_principal_id"].(string),
		OwnerMembershipID: intent["owner_membership_id"].(string),
	}
	targetDigest := store.TeamBootstrapApprovalTargetDigest(bootstrapIntent, teamName)
	approvalMap := map[string]any{
		"schema": OwnerApprovalSchema, "action": store.OwnerActionTeamBootstrap,
		"store_id": bootstrapIntent.StoreID, "team_id": bootstrapIntent.TeamID,
		"target_kind": "team", "target_id": bootstrapIntent.TeamID,
		"target_digest": targetDigest, "team_name": teamName,
		"bootstrap_intent": map[string]any{
			"store_id": bootstrapIntent.StoreID, "team_id": bootstrapIntent.TeamID,
			"owner_principal_id":  bootstrapIntent.OwnerPrincipalID,
			"owner_membership_id": bootstrapIntent.OwnerMembershipID,
		},
	}
	approvalBody, _ := json.Marshal(approvalMap)
	bodyDigest := sha256.Sum256(approvalBody)
	claims := map[string]any{
		"version": OwnerStepUpAssertionVersion, "iss": ownerStepUpAssertionIssuer,
		"aud": ownerStepUpAssertionAudience, "iat": now.Unix(), "nbf": now.Unix(),
		"exp": now.Add(25 * time.Second).Unix(), "jti": "bootstrap-browser-approval",
		"request_id": "req-bootstrap-browser-approval", "method": http.MethodPost,
		"path": OwnerApprovalRoutePath, "body_sha256": fmt.Sprintf("%x", bodyDigest),
		"action": store.OwnerActionTeamBootstrap, "store_id": bootstrapIntent.StoreID,
		"team_id": bootstrapIntent.TeamID, "oauth_issuer": root.Issuer,
		"oauth_subject": root.Subject, "oauth_client_id": root.AdminClientID,
		"auth_time": now.Add(-time.Minute).Unix(),
	}
	assertion := signPrincipalAssertion(
		t, private, "active", map[string]any{"typ": OwnerStepUpAssertionVersion}, claims,
	)
	approved := serveOwnerAdminRequest(
		server.Handler(), OwnerApprovalRoutePath, approvalBody,
		"req-bootstrap-browser-approval", assertion,
	)
	if approved.Code != http.StatusOK {
		t.Fatalf("bootstrap approval = %d %q", approved.Code, approved.Body.String())
	}
	var approvalResult map[string]any
	if err := json.Unmarshal(approved.Body.Bytes(), &approvalResult); err != nil {
		t.Fatal(err)
	}
	nonce, _ := approvalResult["approval_nonce"].(string)
	executeBody, _ := json.Marshal(map[string]any{
		"schema": OwnerBootstrapSchema, "operation": "execute", "team_name": teamName,
		"bootstrap_intent": approvalMap["bootstrap_intent"], "approval_nonce": nonce,
	})
	completed := serveOwnerAdminRequest(
		server.Handler(), OwnerBootstrapRoutePath, executeBody, "req-bootstrap-execute", "",
	)
	if completed.Code != http.StatusOK {
		t.Fatalf("bootstrap execute = %d %q", completed.Code, completed.Body.String())
	}
	var complete map[string]any
	if err := json.Unmarshal(completed.Body.Bytes(), &complete); err != nil {
		t.Fatal(err)
	}
	if complete["schema"] != OwnerBootstrapResultSchema || complete["operation"] != "complete" ||
		complete["store_id"] != bootstrapIntent.StoreID || complete["team_id"] != bootstrapIntent.TeamID ||
		complete["owner_principal_id"] != bootstrapIntent.OwnerPrincipalID ||
		complete["owner_membership_id"] != bootstrapIntent.OwnerMembershipID ||
		complete["activation_state"] != "inactive" || complete["content_boundary"] != "synthetic" ||
		complete["public_enabled"] != false || complete["fallback"] != false {
		t.Fatalf("bootstrap complete = %v", complete)
	}
	if err := teamStore.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM team_stores`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("bootstrap markers = %d", markers)
	}
}

func TestOwnerApprovalTargetDigestsMatchGatewayCrossRuntimeGoldens(t *testing.T) {
	bootstrap := store.TeamBootstrapApprovalTargetDigest(store.TeamBootstrapIntent{
		StoreID: "store_test", TeamID: "team_test", OwnerPrincipalID: "principal_owner",
		OwnerMembershipID: "membership_owner",
	}, "Pulse synthetic pilot")
	if bootstrap != "c015b6f66d46bb78311c542a94a54c395f206fc3d9828844a88d28c166596a86" {
		t.Fatalf("bootstrap digest = %q", bootstrap)
	}
	activation := store.SyntheticActivationTargetDigest(
		"store_test", "team_test", strings.Repeat("c", 64),
	)
	if activation != "e47e9c046d7d263703e80f6b0d6625f813d8f503ab00b7177094af0d0304f849" {
		t.Fatalf("activation digest = %q", activation)
	}
}

func TestOwnerAdminGoBoundaryRejectsUnsafeNamesAndEmptyDigests(t *testing.T) {
	intent := store.TeamBootstrapIntent{
		StoreID: "store_test", TeamID: "team_test", OwnerPrincipalID: "principal_owner",
		OwnerMembershipID: "membership_owner",
	}
	for _, name := range []string{
		"/Users/example/private", "token=supersecret", "Cafe\u0301", strings.Repeat("я", 129),
	} {
		body, _ := json.Marshal(map[string]any{
			"schema": OwnerApprovalSchema, "action": store.OwnerActionTeamBootstrap,
			"store_id": intent.StoreID, "team_id": intent.TeamID,
			"target_kind": "team", "target_id": intent.TeamID,
			"target_digest": store.TeamBootstrapApprovalTargetDigest(intent, name),
			"team_name":     name,
			"bootstrap_intent": map[string]any{
				"store_id": intent.StoreID, "team_id": intent.TeamID,
				"owner_principal_id":  intent.OwnerPrincipalID,
				"owner_membership_id": intent.OwnerMembershipID,
			},
		})
		if _, err := decodeOwnerApprovalRequest(body); err == nil {
			t.Errorf("unsafe team name accepted: %q", name)
		}
	}
	if _, err := decodeOwnerActivateRequest([]byte(
		`{"schema":"pulse.team.owner.activate.v1","approval_nonce":"","gate_digest":""}`,
	)); err == nil {
		t.Fatal("empty owner nonce/digest was accepted")
	}
	project := ownerAdminMutationEnvelope{Name: stringPointer("/private/team/project")}
	if _, ok := decodeOwnerAdminMutation(store.OwnerActionProjectCreate, project); ok {
		t.Fatal("path-like project name was accepted")
	}
}

func stringPointer(value string) *string { return &value }
