package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

func issueOwnerQueryApproval(
	t *testing.T,
	server *OwnerAdminServer,
	fixture *principalFixture,
	jti string,
	action string,
	targetKind string,
	targetID string,
	targetDigest string,
	extra map[string]any,
) string {
	t.Helper()
	body := map[string]any{
		"schema": OwnerApprovalSchema, "action": action,
		"store_id": fixture.storeID, "team_id": fixture.teamID,
		"target_kind": targetKind, "target_id": targetID, "target_digest": targetDigest,
	}
	for key, value := range extra {
		body[key] = value
	}
	raw, _ := json.Marshal(body)
	assertion := signOwnerApprovalAssertion(
		t, fixture, jti, raw, action, fixture.storeID, fixture.teamID,
	)
	approved := serveOwnerAdminRequest(
		server.Handler(), OwnerApprovalRoutePath, raw, "req-"+jti, assertion,
	)
	if approved.Code != http.StatusOK {
		t.Fatalf("%s approval = %d %q", action, approved.Code, approved.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(approved.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	nonce, _ := result["approval_nonce"].(string)
	if !validTeamAdminDigest(nonce) {
		t.Fatalf("%s approval nonce = %q", action, nonce)
	}
	return nonce
}

func TestOwnerAuditRouteConsumesApprovalAndReturnsMetadataOnlyTeamPage(t *testing.T) {
	fixture := newPrincipalFixture(t)
	server := newOwnerAdminServerFixture(t, fixture)
	mutation := store.OwnerAdminMutation{
		Action: store.OwnerActionMembershipCreate, Issuer: fixture.issuer,
		Subject: "owner-audit-route-member", Role: "member",
	}
	issueOwnerMutationApproval(
		t, server, fixture, "owner-audit-seed", mutation,
		map[string]any{"issuer": mutation.Issuer, "subject": mutation.Subject, "role": mutation.Role},
	)

	limit := 10
	nonce := issueOwnerQueryApproval(
		t, server, fixture, "owner-audit-route", store.OwnerActionTeamAuditInspect,
		"team_audit", fixture.teamID, store.OwnerAuditApprovalTargetDigest("", limit),
		map[string]any{"limit": limit},
	)
	body := []byte(fmt.Sprintf(
		`{"schema":%q,"approval_nonce":%q,"limit":%d}`,
		OwnerAuditSchema, nonce, limit,
	))
	response := serveOwnerAdminRequest(
		server.Handler(), OwnerAuditRoutePath, body, "req-owner-audit-route-execute", "",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("Owner audit = %d %q", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["schema"] != OwnerAuditResultSchema || result["own_actions_only"] != false ||
		result["fallback"] != false {
		t.Fatalf("Owner audit result = %v", result)
	}
	events, ok := result["events"].([]any)
	if !ok || len(events) == 0 {
		t.Fatalf("Owner audit events = %v", result["events"])
	}
	if containsUnsafeOwnerText(response.Body.String()) {
		t.Fatalf("Owner audit response leaked unsafe content: %q", response.Body.String())
	}
}

func TestOwnerDeletionStatusRouteConsumesApprovalBoundToExactOperation(t *testing.T) {
	fixture := newPrincipalFixture(t)
	server := newOwnerAdminServerFixture(t, fixture)
	objectID := "root-owner-deletion-status"
	insertOwnerSharedDeleteRoot(t, fixture, objectID)

	deleteNonce := issueOwnerQueryApproval(
		t, server, fixture, "owner-status-delete", store.OwnerActionSharedDelete,
		"team_object", objectID, store.SharedDeletionApprovalTargetDigest(objectID), nil,
	)
	deleteBody := []byte(fmt.Sprintf(
		`{"schema":%q,"object_id":%q,"idempotency_key":"owner-status-delete-0001","approval_nonce":%q}`,
		OwnerSharedDeleteSchema, objectID, deleteNonce,
	))
	deleted := serveOwnerAdminRequest(
		server.Handler(), OwnerSharedDeleteRoutePath, deleteBody, "req-owner-status-delete", "",
	)
	if deleted.Code != http.StatusOK {
		t.Fatalf("Owner shared delete = %d %q", deleted.Code, deleted.Body.String())
	}
	var deletion map[string]any
	if err := json.Unmarshal(deleted.Body.Bytes(), &deletion); err != nil {
		t.Fatal(err)
	}
	operationID, _ := deletion["operation_id"].(string)

	statusNonce := issueOwnerQueryApproval(
		t, server, fixture, "owner-deletion-status", store.OwnerActionDeletionStatus,
		"deletion_operation", operationID,
		store.OwnerDeletionStatusApprovalTargetDigest(operationID),
		map[string]any{"operation_id": operationID},
	)
	statusBody := []byte(fmt.Sprintf(
		`{"schema":%q,"approval_nonce":%q,"operation_id":%q}`,
		OwnerDeletionStatusSchema, statusNonce, operationID,
	))
	status := serveOwnerAdminRequest(
		server.Handler(), OwnerDeletionStatusRoutePath, statusBody,
		"req-owner-deletion-status-execute", "",
	)
	if status.Code != http.StatusOK {
		t.Fatalf("Owner deletion status = %d %q", status.Code, status.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(status.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["schema"] != OwnerDeletionStatusResultSchema ||
		result["operation_id"] != operationID || result["object_id"] != objectID ||
		result["status"] != store.TeamDeletionStatusInProgress || result["fallback"] != false {
		t.Fatalf("Owner deletion status result = %v", result)
	}
}

func TestOwnerDeletionStatusRoutePreservesCleanupFailedTimestampContract(t *testing.T) {
	fixture := newPrincipalFixture(t)
	server := newOwnerAdminServerFixture(t, fixture)
	objectID := "root-owner-deletion-cleanup-failed"
	operationID := startOwnerSharedDeletionForStatus(
		t, server, fixture, objectID, "owner-status-cleanup-failed",
	)
	claims, err := fixture.store.ClaimTeamDeletionJobs(context.Background(), store.TeamDeletionClaimRequest{
		WriterID: server.writer.WriterID, WriterToken: server.writer.Token,
		Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 || claims[0].OperationID != operationID {
		t.Fatalf("claim Owner deletion = %+v, %v", claims, err)
	}
	if err := fixture.store.FailTeamDeletion(context.Background(), store.TeamDeletionFailureRequest{
		WriterID: server.writer.WriterID, WriterToken: server.writer.Token,
		OperationID: operationID, LeaseToken: claims[0].LeaseToken,
		ErrorCode: store.TeamDeletionFailureTemporary, Backoff: 2 * time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	result := readOwnerDeletionStatusRoute(
		t, server, fixture, operationID, "owner-status-cleanup-failed-read",
	)
	wantFields := []string{
		"attempts", "audit_event_id", "fallback", "next_attempt_at", "object_id",
		"operation_id", "schema", "status",
	}
	if got := sortedMapKeys(result); !reflect.DeepEqual(got, wantFields) ||
		result["schema"] != OwnerDeletionStatusResultSchema ||
		result["operation_id"] != operationID || result["object_id"] != objectID ||
		result["status"] != store.TeamDeletionStatusCleanupFailed ||
		result["attempts"] != float64(1) ||
		result["next_attempt_at"] != "2026-07-11T01:02:00Z" ||
		result["fallback"] != false {
		t.Fatalf("cleanup-failed Owner deletion status = %v", result)
	}
}

func TestOwnerDeletionStatusRoutePreservesCompleteTimestampContract(t *testing.T) {
	fixture := newPrincipalFixture(t)
	server := newOwnerAdminServerFixture(t, fixture)
	objectID := "root-owner-deletion-complete"
	operationID := startOwnerSharedDeletionForStatus(
		t, server, fixture, objectID, "owner-status-complete",
	)
	claims, err := fixture.store.ClaimTeamDeletionJobs(context.Background(), store.TeamDeletionClaimRequest{
		WriterID: server.writer.WriterID, WriterToken: server.writer.Token,
		Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 || claims[0].OperationID != operationID {
		t.Fatalf("claim Owner deletion = %+v, %v", claims, err)
	}
	completed, err := fixture.store.CompleteTeamDeletion(context.Background(), store.TeamDeletionCompletionRequest{
		WriterID: server.writer.WriterID, WriterToken: server.writer.Token,
		OperationID: operationID, LeaseToken: claims[0].LeaseToken,
	})
	if err != nil || completed.Status != store.TeamDeletionStatusComplete {
		t.Fatalf("complete Owner deletion = %+v, %v", completed, err)
	}

	result := readOwnerDeletionStatusRoute(
		t, server, fixture, operationID, "owner-status-complete-read",
	)
	wantFields := []string{
		"attempts", "audit_event_id", "completed_at", "fallback", "object_id",
		"operation_id", "schema", "status",
	}
	if got := sortedMapKeys(result); !reflect.DeepEqual(got, wantFields) ||
		result["schema"] != OwnerDeletionStatusResultSchema ||
		result["operation_id"] != operationID || result["object_id"] != objectID ||
		result["status"] != store.TeamDeletionStatusComplete ||
		result["attempts"] != float64(1) ||
		result["completed_at"] != "2026-07-11T01:00:00Z" ||
		result["fallback"] != false {
		t.Fatalf("complete Owner deletion status = %v", result)
	}
}

func startOwnerSharedDeletionForStatus(
	t *testing.T,
	server *OwnerAdminServer,
	fixture *principalFixture,
	objectID string,
	suffix string,
) string {
	t.Helper()
	insertOwnerSharedDeleteRoot(t, fixture, objectID)
	nonce := issueOwnerQueryApproval(
		t, server, fixture, suffix+"-delete", store.OwnerActionSharedDelete,
		"team_object", objectID, store.SharedDeletionApprovalTargetDigest(objectID), nil,
	)
	body := []byte(fmt.Sprintf(
		`{"schema":%q,"object_id":%q,"idempotency_key":%q,"approval_nonce":%q}`,
		OwnerSharedDeleteSchema, objectID, suffix+"-delete-0001", nonce,
	))
	response := serveOwnerAdminRequest(
		server.Handler(), OwnerSharedDeleteRoutePath, body, "req-"+suffix+"-delete", "",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("Owner shared delete = %d %q", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	operationID, _ := result["operation_id"].(string)
	if operationID == "" {
		t.Fatalf("Owner shared deletion operation = %v", result)
	}
	return operationID
}

func readOwnerDeletionStatusRoute(
	t *testing.T,
	server *OwnerAdminServer,
	fixture *principalFixture,
	operationID string,
	suffix string,
) map[string]any {
	t.Helper()
	nonce := issueOwnerQueryApproval(
		t, server, fixture, suffix, store.OwnerActionDeletionStatus,
		"deletion_operation", operationID,
		store.OwnerDeletionStatusApprovalTargetDigest(operationID),
		map[string]any{"operation_id": operationID},
	)
	body := []byte(fmt.Sprintf(
		`{"schema":%q,"approval_nonce":%q,"operation_id":%q}`,
		OwnerDeletionStatusSchema, nonce, operationID,
	))
	response := serveOwnerAdminRequest(
		server.Handler(), OwnerDeletionStatusRoutePath, body, "req-"+suffix+"-execute", "",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("Owner deletion status = %d %q", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func containsUnsafeOwnerText(value string) bool {
	return ownerUnsafeTextPattern.MatchString(value)
}
