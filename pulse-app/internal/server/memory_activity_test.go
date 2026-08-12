package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

func TestMemoryRecallActivityStoresOnlyContentFreeProof(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "memory-activity.json")
	vault, err := store.OpenVault(filepath.Join(root, "pulse.db"), store.StoreKindPersonal, "store_personal_activity")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	server, err := New(Config{
		IPCSecret: "secret", Store: vault, ProductBindingVerifier: &productBindingVerifierStub{},
		MemoryActivityPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/memory/activity/recall", strings.NewReader(
		`{"schema":"pulse.memory_recall_activity.v1","result_count":1,"result_digest":"`+strings.Repeat("d", 64)+`"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Pulse-Key", "secret")
	request.Header.Set("X-Pulse-Product-Host", "codex")
	setProductBindingHeaders(request, filepath.Clean(root), strings.Repeat("b", 64), "repository_project_b", "3")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "query") || strings.Contains(text, "summary") || strings.Contains(text, "memory text") {
		t.Fatalf("activity leaked content fields: %s", text)
	}
	activity, err := readMemoryActivity(path)
	if err != nil {
		t.Fatal(err)
	}
	if activity.Hosts["codex"].RepositoryID != "repository_project_b" || activity.Hosts["codex"].ResultCount != 1 {
		t.Fatalf("activity=%#v", activity)
	}
}
