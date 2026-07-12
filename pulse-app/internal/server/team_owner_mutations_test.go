package server

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

func issueOwnerMutationApproval(
	t *testing.T,
	server *OwnerAdminServer,
	fixture *principalFixture,
	jti string,
	mutation store.OwnerAdminMutation,
	mutationJSON map[string]any,
) string {
	t.Helper()
	targetKind, targetID, targetDigest, err := store.OwnerAdminMutationTarget(mutation)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"schema": OwnerApprovalSchema, "action": mutation.Action,
		"store_id": fixture.storeID, "team_id": fixture.teamID,
		"target_kind": targetKind, "target_id": targetID, "target_digest": targetDigest,
		"mutation": mutationJSON,
	})
	assertion := signOwnerApprovalAssertion(
		t, fixture, jti, body, mutation.Action, fixture.storeID, fixture.teamID,
	)
	response := serveOwnerAdminRequest(
		server.Handler(), OwnerApprovalRoutePath, body, "req-"+jti, assertion,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("mutation approval = %d %q", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	nonce, _ := result["approval_nonce"].(string)
	if !validTeamAdminClientKey(nonce) {
		t.Fatalf("approval nonce = %q", nonce)
	}
	return nonce
}

func TestOwnerMemberRouteConsumesActionBoundApprovalAndReturnsOpaqueMetadata(t *testing.T) {
	fixture := newPrincipalFixture(t)
	server := newOwnerAdminServerFixture(t, fixture)
	mutation := store.OwnerAdminMutation{
		Action: store.OwnerActionMembershipCreate, Issuer: fixture.issuer,
		Subject: "new-member-subject", Role: "member",
	}
	mutationJSON := map[string]any{
		"issuer": mutation.Issuer, "subject": mutation.Subject, "role": mutation.Role,
	}
	nonce := issueOwnerMutationApproval(t, server, fixture, "owner-member-create", mutation, mutationJSON)
	body, _ := json.Marshal(map[string]any{
		"schema": OwnerMembersSchema, "action": mutation.Action, "approval_nonce": nonce,
		"issuer": mutation.Issuer, "subject": mutation.Subject, "role": mutation.Role,
	})
	created := serveOwnerAdminRequest(
		server.Handler(), OwnerMembersRoutePath, body, "req-owner-member-execute", "",
	)
	if created.Code != http.StatusOK {
		t.Fatalf("member create = %d %q", created.Code, created.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if got := sortedAdminMapKeys(result); !reflect.DeepEqual(got, []string{
		"action", "audit_event_id", "auth_epoch", "fallback", "member", "schema", "status",
	}) {
		t.Fatalf("member result fields = %v", got)
	}
	member, ok := result["member"].(map[string]any)
	if !ok || result["schema"] != OwnerMembersResultSchema || result["action"] != mutation.Action ||
		result["status"] != "complete" || result["fallback"] != false ||
		member["role"] != "member" || !validTeamAdminOpaque(member["principal_id"].(string)) ||
		!validTeamAdminOpaque(member["membership_id"].(string)) {
		t.Fatalf("member result = %v", result)
	}
	if strings.Contains(created.Body.String(), mutation.Issuer) ||
		strings.Contains(created.Body.String(), mutation.Subject) {
		t.Fatalf("member response leaked external identity: %q", created.Body.String())
	}

	replay := serveOwnerAdminRequest(
		server.Handler(), OwnerMembersRoutePath, body, "req-owner-member-replay", "",
	)
	if replay.Code != http.StatusConflict ||
		replay.Body.String() != "{\"error\":\"owner_approval_replayed\",\"fallback\":false}\n" {
		t.Fatalf("member replay = %d %q", replay.Code, replay.Body.String())
	}
}

func TestOwnerMutationContractsAreActionSpecificAndRejectKnownFieldSmuggling(t *testing.T) {
	valid := []struct {
		schema string
		action string
		body   map[string]any
	}{
		{OwnerMembersSchema, store.OwnerActionMembershipCreate, map[string]any{"issuer": "https://issuer.example", "subject": "member", "role": "member"}},
		{OwnerMembersSchema, store.OwnerActionMembershipRevoke, map[string]any{"target_id": "principal-member"}},
		{OwnerBindingsSchema, store.OwnerActionAgentBindingCreate, map[string]any{"issuer": "https://issuer.example", "subject": "member", "client_id": "agent-client"}},
		{OwnerBindingsSchema, store.OwnerActionAgentBindingRevoke, map[string]any{"target_id": "binding-1"}},
		{OwnerServicesSchema, store.OwnerActionServicePrincipalCreate, map[string]any{"issuer": "https://issuer.example", "client_id": "service-client"}},
		{OwnerServicesSchema, store.OwnerActionServicePrincipalRevoke, map[string]any{"target_id": "principal-service"}},
		{OwnerProjectsSchema, store.OwnerActionProjectCreate, map[string]any{"name": "Synthetic Project"}},
		{OwnerProjectGrantsSchema, store.OwnerActionProjectGrantCreate, map[string]any{"project_id": "project-1", "target_principal_id": "principal-1", "access_level": "write"}},
		{OwnerProjectGrantsSchema, store.OwnerActionProjectGrantRevoke, map[string]any{"target_id": "grant-1"}},
	}
	for _, test := range valid {
		body := map[string]any{
			"schema": test.schema, "action": test.action,
			"approval_nonce": strings.Repeat("a", 64),
		}
		for key, value := range test.body {
			body[key] = value
		}
		raw, _ := json.Marshal(body)
		allowed := map[string]bool{test.action: true}
		if _, err := decodeOwnerAdminMutationRequest(raw, test.schema, allowed); err != nil {
			t.Errorf("valid %s rejected: %v", test.action, err)
		}
		body["name"] = "smuggled-known-field"
		if test.action == store.OwnerActionProjectCreate {
			body["issuer"] = "smuggled-known-field"
		}
		raw, _ = json.Marshal(body)
		if _, err := decodeOwnerAdminMutationRequest(raw, test.schema, allowed); err == nil {
			t.Errorf("known-field smuggling accepted for %s", test.action)
		}
	}
}
