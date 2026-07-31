package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
		srv.Close()
		_ = vault.Close()
	})
	return ts, consolidation.Destination{
		StoreKind: string(store.StoreKindPersonal), StoreID: vault.StoreID(),
		BindingDigest: binding, RepositoryID: repository,
	}
}

type blockingConsolidationInventory struct {
	manager  *consolidation.Manager
	started  chan struct{}
	stopped  chan struct{}
	startOne sync.Once
	stopOne  sync.Once
}

func (b *blockingConsolidationInventory) Run(ctx context.Context, invocationID string, _ consolidation.Destination) (consolidation.Report, error) {
	if _, err := b.manager.Advance(
		invocationID, consolidation.PhaseInventory, consolidation.Totals{}, []consolidation.Source{}, nil,
		[]string{"adapter_pulse_v1"}, "Inspecting recognized local memory sources.", "",
	); err != nil {
		return consolidation.Report{}, err
	}
	b.startOne.Do(func() { close(b.started) })
	<-ctx.Done()
	b.stopOne.Do(func() { close(b.stopped) })
	return consolidation.Report{}, ctx.Err()
}

func (b *blockingConsolidationInventory) EnsureFresh(invocationID string) (consolidation.Report, error) {
	return b.manager.Get(invocationID)
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

func waitForConsolidationPhase(t *testing.T, phase consolidation.Phase, read func() consolidation.Report) consolidation.Report {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		report := read()
		if report.Phase == phase {
			return report
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("consolidation report did not reach %s; last=%#v", phase, report)
		}
	}
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

func TestConsolidationReportStartReturnsWhileDaemonJobRunsAndCancelStopsIt(t *testing.T) {
	binding := strings.Repeat("d", 64)
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "personal.db"), store.StoreKindPersonal, "store_personal_async_report")
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_async_report"); err != nil {
		t.Fatal(err)
	}
	manager, err := consolidation.NewManager(consolidation.ManagerConfig{
		RootDir: filepath.Join(filepath.Dir(vault.DBPath()), "report-test"),
		Key:     []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	inventory := &blockingConsolidationInventory{manager: manager, started: make(chan struct{}), stopped: make(chan struct{})}
	srv, err := New(Config{
		IPCSecret: "secret", Store: vault, ConsolidationReports: manager,
		ConsolidationInventory: inventory, ConsolidationJobTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close(); _ = vault.Close() })

	responseCh := make(chan *http.Response, 1)
	go func() {
		responseCh <- pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports", map[string]any{})
	}()
	var response *http.Response
	select {
	case response = <-responseCh:
	case <-time.After(time.Second):
		t.Fatal("start request waited for the inventory job")
	}
	started := decodeConsolidationReport(t, response)
	select {
	case <-inventory.started:
	case <-time.After(time.Second):
		t.Fatal("daemon inventory job did not start")
	}
	status := decodeConsolidationReport(t, pulseJSON(t, ts, http.MethodGet, "/memory/consolidation/reports/"+started.InvocationID, nil))
	if status.Phase != consolidation.PhaseInventory {
		t.Fatalf("running job was not observable: %#v", status)
	}
	canceled := decodeConsolidationReport(t, pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports/"+started.InvocationID+"/cancel", map[string]any{}))
	if canceled.Phase != consolidation.PhaseCanceled {
		t.Fatalf("cancel did not commit: %#v", canceled)
	}
	select {
	case <-inventory.stopped:
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop the daemon inventory job")
	}
}

func TestConsolidationResumeHTTPIsIdempotentAndRejectsKeyReuse(t *testing.T) {
	ts, _ := newConsolidationReportServer(t)
	started := decodeConsolidationReport(t, pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports", map[string]any{}))
	decodeConsolidationReport(t, pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports/"+started.InvocationID+"/cancel", map[string]any{}))

	const key = "mcp_0123456789abcdef0123456789abcdef"
	path := "/memory/consolidation/reports/" + started.InvocationID + "/resume"
	first := decodeConsolidationReport(t, pulseJSONWithIdempotency(t, ts, http.MethodPost, path, map[string]any{}, key))
	retry := decodeConsolidationReport(t, pulseJSONWithIdempotency(t, ts, http.MethodPost, path, map[string]any{}, key))
	if retry.InvocationID != first.InvocationID {
		t.Fatalf("response-loss retry created another report: first=%#v retry=%#v", first, retry)
	}

	decodeConsolidationReport(t, pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports/"+first.InvocationID+"/cancel", map[string]any{}))
	conflict := pulseJSONWithIdempotency(
		t, ts, http.MethodPost, "/memory/consolidation/reports/"+first.InvocationID+"/resume", map[string]any{}, key,
	)
	defer conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("idempotency key reuse status=%d", conflict.StatusCode)
	}
}

func TestCorruptConsolidationCheckpointDisablesReportsWithoutBlockingCoreServer(t *testing.T) {
	vault, err := store.OpenVault(filepath.Join(t.TempDir(), "personal.db"), store.StoreKindPersonal, "store_personal_corrupt_report")
	if err != nil {
		t.Fatal(err)
	}
	binding := strings.Repeat("f", 64)
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_corrupt_report"); err != nil {
		t.Fatal(err)
	}
	oldKey := sha256.Sum256([]byte("pulse:consolidation-report:v1:old-secret"))
	manager, err := consolidation.NewManager(consolidation.ManagerConfig{
		RootDir: filepath.Join(filepath.Dir(vault.DBPath()), "consolidation-reports"), Key: oldKey[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Start(consolidation.Destination{
		StoreKind: string(vault.StoreKind()), StoreID: vault.StoreID(),
		BindingDigest: binding, RepositoryID: "repository_corrupt_report",
	}); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{IPCSecret: "secret", Store: vault})
	if err != nil {
		t.Fatalf("report-only key rotation blocked core server startup: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close(); _ = vault.Close() })

	statusResponse := pulseJSON(t, ts, http.MethodGet, "/memory/status", nil)
	defer statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusOK {
		t.Fatalf("core status unavailable: %d", statusResponse.StatusCode)
	}
	var status struct {
		Available bool   `json:"consolidation_reports_available"`
		State     string `json:"consolidation_reports_state"`
	}
	if err := json.NewDecoder(statusResponse.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Available || status.State != "checkpoint_integrity_failure" {
		t.Fatalf("corrupt report bundle was not diagnosed fail-closed: %#v", status)
	}
	reportResponse := pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports", map[string]any{})
	defer reportResponse.Body.Close()
	if reportResponse.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("corrupt report route status=%d", reportResponse.StatusCode)
	}
}
