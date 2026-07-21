package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/consolidation"
	"github.com/nkkmnk/pulse/internal/store"
)

func newConsolidationReportServer(t *testing.T) (*httptest.Server, consolidation.Destination) {
	t.Helper()
	binding := strings.Repeat("c", 64)
	repository := "repository_consolidation_report"
	vault, err := store.OpenVault(
		filepath.Join(t.TempDir(), "personal.db"),
		store.StoreKindPersonal,
		"store_personal_consolidation_report",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, repository); err != nil {
		t.Fatal(err)
	}
	manager, err := consolidation.NewManager(consolidation.ManagerConfig{
		RootDir: filepath.Join(filepath.Dir(vault.DBPath()), "report-test"),
		Key:     []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault, ConsolidationReports: manager})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = vault.Close()
	})
	return ts, consolidation.Destination{
		StoreKind: string(store.StoreKindPersonal), StoreID: vault.StoreID(),
		BindingDigest: binding, RepositoryID: repository,
	}
}

func decodeConsolidationReport(t *testing.T, response *http.Response) consolidation.Report {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("report status=%d", response.StatusCode)
	}
	var report consolidation.Report
	if err := json.NewDecoder(response.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestConsolidationReportRoutesDeriveAuthorityAndPreserveLifecycle(t *testing.T) {
	ts, destination := newConsolidationReportServer(t)

	unauthorized, err := http.Post(ts.URL+"/memory/consolidation/reports", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.StatusCode)
	}

	firstResponse := pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports", map[string]any{
		"destination": map[string]any{"store_id": "store_agent_selected"},
	})
	defer firstResponse.Body.Close()
	if firstResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("caller-selected destination status=%d", firstResponse.StatusCode)
	}

	first := decodeConsolidationReport(t, pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports", map[string]any{}))
	if first.Destination != destination || first.Schema != consolidation.ReportSchema || first.Phase != consolidation.PhasePlanned {
		t.Fatalf("server did not derive exact destination: %#v", first)
	}
	reused := decodeConsolidationReport(t, pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports", map[string]any{}))
	if reused.InvocationID != first.InvocationID || reused.Generation != first.Generation {
		t.Fatalf("active lease was not reused: first=%#v reused=%#v", first, reused)
	}
	latest := decodeConsolidationReport(t, pulseJSON(t, ts, http.MethodGet, "/memory/consolidation/reports/latest", nil))
	if latest.InvocationID != first.InvocationID {
		t.Fatalf("latest report mismatch: %#v", latest)
	}

	canceled := decodeConsolidationReport(t, pulseJSON(
		t, ts, http.MethodPost, "/memory/consolidation/reports/"+first.InvocationID+"/cancel", map[string]any{},
	))
	if canceled.Phase != consolidation.PhaseCanceled || canceled.Generation <= first.Generation {
		t.Fatalf("cancel did not commit: %#v", canceled)
	}
	resumed := decodeConsolidationReport(t, pulseJSON(
		t, ts, http.MethodPost, "/memory/consolidation/reports/"+first.InvocationID+"/resume", map[string]any{},
	))
	if resumed.InvocationID == first.InvocationID || resumed.Generation <= canceled.Generation {
		t.Fatalf("resume did not create replacement lease: %#v", resumed)
	}

	stale := pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports/"+first.InvocationID+"/cancel", map[string]any{})
	defer stale.Body.Close()
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale cancel status=%d", stale.StatusCode)
	}
}
