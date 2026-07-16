package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

const (
	testSharedBinding    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSharedRepository = "repository_server_git_memory"
	testSharedProject    = "project_0123456789abcdef0123456789abcdef"
)

func newGitMemoryReviewServer(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "desk.db"), store.StoreKindDesk, "store_desk_server_git_memory")
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureProductRuntimeAuthority(testSharedBinding, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(testSharedBinding, testSharedRepository); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	return vault, httptest.NewServer(srv.Handler())
}

func serverSourceBody() map[string]any {
	return map[string]any{
		"schema": "pulse.project_source.register.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding,
		"source_kind": "repository_text", "locator": "notes/team.md",
		"version_digest": strings.Repeat("b", 64), "byte_count": 120,
		"observed_at": "2026-07-16T08:00:00Z",
	}
}

func registerServerSource(t *testing.T, ts *httptest.Server) store.ProjectSourceRegistrationResult {
	t.Helper()
	resp := pulseJSON(t, ts, http.MethodPost, "/project/sources/register", serverSourceBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register status = %d", resp.StatusCode)
	}
	var result store.ProjectSourceRegistrationResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProjectSourceRoutesUseClosedHostAttestedMetadata(t *testing.T) {
	_, ts := newGitMemoryReviewServer(t)
	defer ts.Close()
	registered := registerServerSource(t, ts)
	status := pulseJSON(t, ts, http.MethodPost, "/project/sources/status", map[string]any{
		"schema": "pulse.project_source.status.v1", "portable_project_id": testSharedProject,
		"repository_id": testSharedRepository, "binding_digest": testSharedBinding, "source_id": registered.SourceID,
	})
	defer status.Body.Close()
	if status.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", status.StatusCode)
	}
	unknown := serverSourceBody()
	unknown["raw_source"] = "must never be accepted"
	response := pulseJSON(t, ts, http.MethodPost, "/project/sources/register", unknown)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown source field status = %d", response.StatusCode)
	}
}
