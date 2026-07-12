package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

func insertOwnerSharedDeleteRoot(t *testing.T, fixture *principalFixture, objectID string) {
	t.Helper()
	if _, err := fixture.store.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, created_at, updated_at)
		VALUES (?, ?, ?, 'memory', 'team', ?, NULL, ?, 'normal', 'project',
			'active', 1, '2026-07-12T06:00:00Z', '2026-07-12T06:00:00Z')`,
		objectID, fixture.storeID, fixture.teamID, fixture.teamID, fixture.ownerID,
	); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerSharedDeleteIsOutOfBandApprovalBoundAndGenerationFenced(t *testing.T) {
	fixture := newPrincipalFixture(t)
	server := newOwnerAdminServerFixture(t, fixture)
	objectID := "root-shared-delete-owner"
	insertOwnerSharedDeleteRoot(t, fixture, objectID)

	directBody, _ := json.Marshal(map[string]any{
		"schema": OwnerSharedDeleteSchema, "object_id": objectID,
		"idempotency_key": "owner-shared-delete-direct", "approval_nonce": strings.Repeat("a", 64),
	})
	direct := serveOwnerAdminRequest(
		server.Handler(), OwnerSharedDeleteRoutePath, directBody, "req-owner-shared-direct", "",
	)
	if direct.Code != http.StatusForbidden ||
		direct.Body.String() != "{\"error\":\"owner_approval_required\",\"fallback\":false}\n" {
		t.Fatalf("direct shared delete = %d %q", direct.Code, direct.Body.String())
	}

	targetDigest := store.SharedDeletionApprovalTargetDigest(objectID)
	approvalBody, _ := json.Marshal(map[string]any{
		"schema": OwnerApprovalSchema, "action": store.OwnerActionSharedDelete,
		"store_id": fixture.storeID, "team_id": fixture.teamID,
		"target_kind": "team_object", "target_id": objectID, "target_digest": targetDigest,
	})
	assertion := signOwnerApprovalAssertion(
		t, fixture, "owner-shared-delete-approval", approvalBody,
		store.OwnerActionSharedDelete, fixture.storeID, fixture.teamID,
	)
	approved := serveOwnerAdminRequest(
		server.Handler(), OwnerApprovalRoutePath, approvalBody,
		"req-owner-shared-delete-approval", assertion,
	)
	if approved.Code != http.StatusOK {
		t.Fatalf("shared delete approval = %d %q", approved.Code, approved.Body.String())
	}
	var approval map[string]any
	if err := json.Unmarshal(approved.Body.Bytes(), &approval); err != nil {
		t.Fatal(err)
	}
	nonce, _ := approval["approval_nonce"].(string)
	executeBody := []byte(fmt.Sprintf(
		`{"schema":"pulse.team.owner.shared_delete.v1","object_id":%q,"idempotency_key":"owner-shared-delete-0001","approval_nonce":%q}`,
		objectID, nonce,
	))
	deleted := serveOwnerAdminRequest(
		server.Handler(), OwnerSharedDeleteRoutePath, executeBody, "req-owner-shared-delete", "",
	)
	if deleted.Code != http.StatusOK {
		t.Fatalf("shared delete = %d %q", deleted.Code, deleted.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(deleted.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["schema"] != OwnerSharedDeleteResultSchema || result["object_id"] != objectID ||
		result["status"] != store.TeamDeletionStatusInProgress || result["replayed"] != false ||
		result["fallback"] != false || !validTeamAdminOpaque(result["operation_id"].(string)) ||
		!validTeamAdminOpaque(result["audit_event_id"].(string)) {
		t.Fatalf("shared delete result = %v", result)
	}
	var lifecycle string
	if err := fixture.store.DB().QueryRow(`
		SELECT lifecycle FROM team_object_registry WHERE object_id = ?`, objectID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "tombstoned" {
		t.Fatalf("shared root lifecycle = %q", lifecycle)
	}
}
