package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamjobs"
)

func teamDeleteBody(objectID, idempotencyKey string) []byte {
	return []byte(fmt.Sprintf(
		`{"schema":"pulse.team.delete.v1","object_id":%q,"active_context":{"session_id":"session-delete"},"idempotency_key":%q}`,
		objectID, idempotencyKey,
	))
}

func teamDeleteStatusBody(operationID string) []byte {
	return []byte(fmt.Sprintf(
		`{"schema":"pulse.team.delete_status.v1","operation_id":%q,"active_context":{"session_id":"session-delete"}}`,
		operationID,
	))
}

func serveSignedTeamDeletion(
	t *testing.T,
	srv *TeamServer,
	fixture *principalFixture,
	jti, path string,
	body []byte,
	capabilities []string,
) *httptest.ResponseRecorder {
	t.Helper()
	claims := fixture.claims(jti, "agent-client", body)
	claims["path"] = path
	claims["capabilities"] = capabilities
	assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
	return serveTeamGraphRequest(srv.Handler(), assertion, "req-"+jti, bodyPathRequest{
		path: path, body: body,
	})
}

func insertServerDeletionRoot(
	t *testing.T,
	fixture *principalFixture,
	rootID, ownerID string,
) {
	t.Helper()
	if _, err := fixture.store.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, created_at, updated_at)
		VALUES (?, ?, ?, 'memory', 'personal', ?, ?, ?, 'normal', 'project',
			'active', 1, '2026-07-11T05:00:00Z', '2026-07-11T05:00:00Z')`,
		rootID, fixture.storeID, fixture.teamID, ownerID, ownerID, ownerID,
	); err != nil {
		t.Fatal(err)
	}
}

func TestTeamDeletionRoutesStartReplayAndReadExactOpaqueStatus(t *testing.T) {
	srv, fixture, lease := newReadyTeamServer(t)
	insertServerDeletionRoot(t, fixture, "root-delete-1", fixture.ownerID)
	body := teamDeleteBody("root-delete-1", "delete-idempotency-0001")

	started := serveSignedTeamDeletion(
		t, srv, fixture, "delete-start", TeamDeleteRoutePath, body,
		[]string{"pulse:connect", "pulse:delete"},
	)
	if started.Code != http.StatusOK {
		t.Fatalf("delete start = %d %q", started.Code, started.Body.String())
	}
	var start map[string]any
	if err := json.Unmarshal(started.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	wantStartFields := []string{
		"audit_event_id", "fallback", "object_id", "operation_id", "replayed", "schema", "status",
	}
	if got := sortedMapKeys(start); !reflect.DeepEqual(got, wantStartFields) {
		t.Fatalf("delete fields = %v", got)
	}
	if start["schema"] != TeamDeleteResultSchema || start["object_id"] != "root-delete-1" ||
		start["status"] != store.TeamDeletionStatusInProgress || start["replayed"] != false ||
		start["fallback"] != false || !safeOpaque(start["operation_id"].(string), 255) ||
		!safeOpaque(start["audit_event_id"].(string), 255) {
		t.Fatalf("delete response = %v", start)
	}

	replayed := serveSignedTeamDeletion(
		t, srv, fixture, "delete-replay", TeamDeleteRoutePath, body,
		[]string{"pulse:connect", "pulse:delete"},
	)
	if replayed.Code != http.StatusOK {
		t.Fatalf("delete replay = %d %q", replayed.Code, replayed.Body.String())
	}
	var replay map[string]any
	if err := json.Unmarshal(replayed.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay["operation_id"] != start["operation_id"] || replay["audit_event_id"] != start["audit_event_id"] ||
		replay["replayed"] != true {
		t.Fatalf("replay = %v, start = %v", replay, start)
	}

	statusBody := teamDeleteStatusBody(start["operation_id"].(string))
	statusResponse := serveSignedTeamDeletion(
		t, srv, fixture, "delete-status", TeamDeleteStatusRoutePath, statusBody,
		[]string{"pulse:connect", "pulse:read"},
	)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("delete status = %d %q", statusResponse.Code, statusResponse.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	wantStatusFields := []string{
		"attempts", "audit_event_id", "fallback", "next_attempt_at", "object_id", "operation_id", "schema", "status",
	}
	if got := sortedMapKeys(status); !reflect.DeepEqual(got, wantStatusFields) {
		t.Fatalf("status fields = %v", got)
	}
	if status["schema"] != TeamDeleteStatusResultSchema || status["operation_id"] != start["operation_id"] ||
		status["object_id"] != start["object_id"] || status["audit_event_id"] != start["audit_event_id"] ||
		status["status"] != store.TeamDeletionStatusInProgress || status["attempts"] != float64(0) ||
		status["next_attempt_at"] != "2026-07-11T01:00:00Z" || status["fallback"] != false {
		t.Fatalf("status response = %v", status)
	}

	worker, err := teamjobs.NewDeletionWorker(teamjobs.DeletionWorkerConfig{
		Store: fixture.store,
		Writer: store.TeamWriterLeaseIdentity{
			WriterID: lease.WriterID, Token: lease.Token,
		},
		ClaimLimit: 4, ReapLimit: 8, LeaseTTL: 30 * time.Second,
		PollInterval: time.Millisecond, BaseBackoff: time.Second, MaxBackoff: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	cleaned, err := worker.RunOnce(context.Background())
	if err != nil || cleaned.Claimed != 1 || cleaned.Completed != 1 || cleaned.Failed != 0 {
		t.Fatalf("deletion worker result = %+v, err=%v", cleaned, err)
	}
	completedResponse := serveSignedTeamDeletion(
		t, srv, fixture, "delete-status-complete", TeamDeleteStatusRoutePath, statusBody,
		[]string{"pulse:connect", "pulse:read"},
	)
	if completedResponse.Code != http.StatusOK {
		t.Fatalf("completed delete status = %d %q", completedResponse.Code, completedResponse.Body.String())
	}
	var completed map[string]any
	if err := json.Unmarshal(completedResponse.Body.Bytes(), &completed); err != nil {
		t.Fatal(err)
	}
	wantCompletedFields := []string{
		"attempts", "audit_event_id", "completed_at", "fallback", "object_id", "operation_id", "schema", "status",
	}
	if got := sortedMapKeys(completed); !reflect.DeepEqual(got, wantCompletedFields) ||
		completed["status"] != store.TeamDeletionStatusComplete ||
		completed["attempts"] != float64(1) ||
		completed["completed_at"] != "2026-07-11T01:00:00Z" {
		t.Fatalf("completed status = %v", completed)
	}
}

func TestTeamDeletionRoutesConcealAbsentAndUnauthorizedObjectsWithIdenticalBytes(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	other, err := fixture.store.AddTeamMember(context.Background(), store.AddTeamMemberRequest{
		ActorPrincipalID: fixture.ownerID, Issuer: fixture.issuer,
		Subject: "other-delete-owner", Role: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	insertServerDeletionRoot(t, fixture, "root-delete-hidden", other.PrincipalID)

	absent := serveSignedTeamDeletion(
		t, srv, fixture, "delete-absent", TeamDeleteRoutePath,
		teamDeleteBody("root-delete-absent", "delete-absent-0001"),
		[]string{"pulse:connect", "pulse:delete"},
	)
	hidden := serveSignedTeamDeletion(
		t, srv, fixture, "delete-hidden", TeamDeleteRoutePath,
		teamDeleteBody("root-delete-hidden", "delete-hidden-0001"),
		[]string{"pulse:connect", "pulse:delete"},
	)
	if absent.Code != http.StatusNotFound || hidden.Code != http.StatusNotFound ||
		absent.Body.String() != hidden.Body.String() ||
		absent.Body.String() != "{\"error\":\"concealed_not_found\",\"fallback\":false}\n" {
		t.Fatalf("concealment absent=%d %q hidden=%d %q",
			absent.Code, absent.Body.String(), hidden.Code, hidden.Body.String())
	}

	missingStatus := serveSignedTeamDeletion(
		t, srv, fixture, "delete-status-absent", TeamDeleteStatusRoutePath,
		teamDeleteStatusBody("delete-operation-absent"),
		[]string{"pulse:connect", "pulse:read"},
	)
	if missingStatus.Code != http.StatusNotFound || missingStatus.Body.String() != absent.Body.String() {
		t.Fatalf("missing status = %d %q", missingStatus.Code, missingStatus.Body.String())
	}
}

func TestTeamDeletionRoutesRejectUnknownAuthorityNullsNearPathsAndCapabilityConfusion(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	insertServerDeletionRoot(t, fixture, "root-delete-strict", fixture.ownerID)

	invalidBodies := [][]byte{
		[]byte(`{"schema":"pulse.team.delete.v1","object_id":"root-delete-strict","active_context":{},"idempotency_key":"delete-strict-0001","principal_id":"spoofed"}`),
		[]byte(`{"schema":"pulse.team.delete.v1","object_id":"root-delete-strict","active_context":null,"idempotency_key":"delete-strict-0002"}`),
		[]byte(`{"schema":"pulse.team.delete.v1","object_id":"secret-root","active_context":{},"idempotency_key":"delete-strict-0003"}`),
		[]byte(`{"schema":"pulse.team.delete.v1","object_id":"root-delete-strict","active_context":{"team_id":"spoofed"},"idempotency_key":"delete-strict-0004"}`),
		[]byte(`{"schema":"pulse.team.delete.v1","object_id":"root-delete-strict","active_context":{},"idempotency_key":"short"}`),
		[]byte(`{"schema":"pulse.team.delete.v1","object_id":"-root-delete-strict","active_context":{},"idempotency_key":"delete-strict-0005"}`),
		[]byte(`{"schema":"pulse.team.delete.v1","object_id":".root-delete-strict","active_context":{},"idempotency_key":"delete-strict-0006"}`),
		[]byte(`{"schema":"pulse.team.delete.v1","object_id":"root.sk-abcdefghijkl","active_context":{},"idempotency_key":"delete-strict-0007"}`),
	}
	for index, body := range invalidBodies {
		response := serveSignedTeamDeletion(
			t, srv, fixture, fmt.Sprintf("delete-invalid-%d", index), TeamDeleteRoutePath,
			body, []string{"pulse:connect", "pulse:delete"},
		)
		if response.Code != http.StatusBadRequest ||
			response.Body.String() != "{\"error\":\"invalid_team_delete\",\"fallback\":false}\n" {
			t.Fatalf("invalid %d = %d %q", index, response.Code, response.Body.String())
		}
	}

	capabilityConfused := serveSignedTeamDeletion(
		t, srv, fixture, "delete-read-cap", TeamDeleteRoutePath,
		teamDeleteBody("root-delete-strict", "delete-read-cap-0001"),
		[]string{"pulse:connect", "pulse:read"},
	)
	if capabilityConfused.Code != http.StatusNotFound ||
		capabilityConfused.Body.String() != "{\"error\":\"concealed_not_found\",\"fallback\":false}\n" {
		t.Fatalf("delete with read capability = %d %q", capabilityConfused.Code, capabilityConfused.Body.String())
	}

	nearPath := serveSignedTeamDeletion(
		t, srv, fixture, "delete-near-path", TeamDeleteRoutePath+"/",
		teamDeleteBody("root-delete-strict", "delete-near-path-0001"),
		[]string{"pulse:connect", "pulse:delete"},
	)
	if nearPath.Code != http.StatusNotFound {
		t.Fatalf("near delete route = %d %q", nearPath.Code, nearPath.Body.String())
	}
}

func TestTeamDeletionRouteMapsIdempotencyConflictWithoutLocalFallback(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	insertServerDeletionRoot(t, fixture, "root-delete-conflict-a", fixture.ownerID)
	insertServerDeletionRoot(t, fixture, "root-delete-conflict-b", fixture.ownerID)
	key := "delete-conflict-0001"
	first := serveSignedTeamDeletion(
		t, srv, fixture, "delete-conflict-a", TeamDeleteRoutePath,
		teamDeleteBody("root-delete-conflict-a", key), []string{"pulse:connect", "pulse:delete"},
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first delete = %d %q", first.Code, first.Body.String())
	}
	conflict := serveSignedTeamDeletion(
		t, srv, fixture, "delete-conflict-b", TeamDeleteRoutePath,
		teamDeleteBody("root-delete-conflict-b", key), []string{"pulse:connect", "pulse:delete"},
	)
	if conflict.Code != http.StatusConflict ||
		conflict.Body.String() != "{\"error\":\"idempotency_conflict\",\"fallback\":false}\n" {
		t.Fatalf("conflict = %d %q", conflict.Code, conflict.Body.String())
	}
}

func sortedMapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
