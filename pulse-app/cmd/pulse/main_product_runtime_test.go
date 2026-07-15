package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/config"
	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

type backfillTestEmbedder struct {
	calls  []int
	failAt int
}

func (e *backfillTestEmbedder) Embed(_ context.Context, texts []string, _ embed.InputType) ([][]float32, error) {
	e.calls = append(e.calls, len(texts))
	if len(e.calls) == e.failAt {
		return nil, errors.New("transient backfill failure")
	}
	vectors := make([][]float32, len(texts))
	for i := range texts {
		vectors[i] = []float32{1, 0, 0, 0}
	}
	return vectors, nil
}

func (*backfillTestEmbedder) Model() string { return "backfill-test-embedder" }

func TestBackfillCapsuleEventsPagesMoreThanFiveHundredAndRetriesPartialFailure(t *testing.T) {
	vault, err := store.OpenVault(
		filepath.Join(t.TempDir(), "backfill.db"), store.StoreKindPersonal, "store_personal_backfill_test",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	tx, err := vault.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 501; i++ {
		if _, err := tx.Exec(`
			INSERT INTO events (title, description, scorer_version, ts)
			VALUES (?, ?, 'host-extracted', '2026-07-15T00:00:00Z')`,
			"backfill event", "canonical restart text"); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	embedder := &backfillTestEmbedder{failAt: 2}
	engine := retrieve.New(retrieve.Config{Store: vault, Embedder: embedder})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	backfillCapsuleEvents(canceled, vault, engine)
	if len(embedder.calls) != 0 {
		t.Fatalf("canceled backfill called embedder: %v", embedder.calls)
	}

	backfillCapsuleEvents(context.Background(), vault, engine)
	var complete int
	if err := vault.DB().QueryRow(`SELECT count(*) FROM event_embeddings`).Scan(&complete); err != nil {
		t.Fatal(err)
	}
	if complete != 501 {
		t.Fatalf("complete embeddings = %d, want 501", complete)
	}
	wantCalls := []int{96, 96, 96, 96, 96, 96, 21}
	if len(embedder.calls) != len(wantCalls) {
		t.Fatalf("embed batches = %v, want %v", embedder.calls, wantCalls)
	}
	for i := range wantCalls {
		if embedder.calls[i] != wantCalls[i] {
			t.Fatalf("embed batches = %v, want %v", embedder.calls, wantCalls)
		}
	}
	result, err := engine.Retrieve(context.Background(), retrieve.RetrieveRequest{
		Query: "canonical restart text", Mode: retrieve.ModeEmpathic, TopK: 3,
	})
	if err != nil || len(result.EventIDs) == 0 {
		t.Fatalf("reloaded backfill retrieval = %#v, err=%v", result, err)
	}
}

func TestProductLocalRuntimeRequiresLoopbackAndExactStoreIdentity(t *testing.T) {
	t.Setenv("PULSE_VAULT_STORE_ID", "store_personal_nik")
	if err := runProductLocal(t.TempDir(), "0.0.0.0:18800", config.VaultPersonal); err == nil ||
		!strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback error = %v", err)
	}

	t.Setenv("PULSE_VAULT_STORE_ID", " store_personal_nik")
	if err := runProductLocal(t.TempDir(), "127.0.0.1:18800", config.VaultPersonal); err == nil ||
		!strings.Contains(err.Error(), "exact PULSE_VAULT_STORE_ID") {
		t.Fatalf("non-exact store ID error = %v", err)
	}
}

func managedEmbedderFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "artifacts", "embedder-runtime", "versions", strings.Repeat("a", 64))
	modelDir := filepath.Join(root, "artifacts", "model", "versions", strings.Repeat("b", 64))
	supportDir := filepath.Join(runtimeDir, "support")
	if err := os.MkdirAll(filepath.Join(root, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(supportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(runtimeDir, "runtime", "bin", "python3.12")
	helper := filepath.Join(runtimeDir, "helper.py")
	model := filepath.Join(modelDir, "model.safetensors")
	for path, mode := range map[string]os.FileMode{
		python:                                   0o755,
		helper:                                   0o644,
		model:                                    0o644,
		filepath.Join(supportDir, "config.json"): 0o644,
		filepath.Join(supportDir, "tokenizer.json"): 0o644,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root, map[string]any{
		"dimensions":                         1024,
		"embedder_runtime_activation_digest": strings.Repeat("c", 64),
		"embedder_runtime_tree_digest":       strings.Repeat("d", 64),
		"helper_path":                        helper,
		"model":                              "bge-m3",
		"model_activation_digest":            strings.Repeat("e", 64),
		"model_file":                         model,
		"model_tree_digest":                  strings.Repeat("f", 64),
		"normalized":                         true,
		"pooling":                            "cls",
		"protocol":                           1,
		"python_executable":                  python,
		"schema":                             "pulse.managed_embedder.config.v1",
		"support_directory":                  supportDir,
	}
}

func writeManagedEmbedderFixture(t *testing.T, root string, value map[string]any) string {
	t.Helper()
	path := filepath.Join(root, "runtime", "managed-embedder.json")
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManagedEmbedderConfigRequiresExactPrivateContract(t *testing.T) {
	root, value := managedEmbedderFixture(t)
	path := writeManagedEmbedderFixture(t, root, value)
	t.Setenv("PULSE_MANAGED_EMBEDDER_CONFIG", path)
	config, err := loadManagedEmbedderConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.ModelFile != value["model_file"] || config.PythonExecutable != value["python_executable"] {
		t.Fatalf("config = %+v", config)
	}

	value["unknown"] = true
	writeManagedEmbedderFixture(t, root, value)
	if _, err := loadManagedEmbedderConfig(); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("unknown-field error = %v", err)
	}
}

func TestManagedEmbedderConfigRejectsSymlinkAndUnsafeMode(t *testing.T) {
	root, value := managedEmbedderFixture(t)
	path := writeManagedEmbedderFixture(t, root, value)
	t.Setenv("PULSE_MANAGED_EMBEDDER_CONFIG", path)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadManagedEmbedderConfig(); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("mode error = %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "runtime", "target.json")
	data, _ := json.Marshal(value)
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadManagedEmbedderConfig(); err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestManagedEmbedderConfigNeverFallsBackToCohereOrSystemPaths(t *testing.T) {
	t.Setenv("PULSE_MANAGED_EMBEDDER_CONFIG", "")
	t.Setenv("COHERE_API_KEY", "remote-key-must-not-be-read")
	t.Setenv("PULSE_LOCAL_EMBED_PYTHON", "/usr/bin/python3")
	if _, err := loadManagedEmbedderConfig(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("managed config error = %v", err)
	}
}

func TestProductLocalRuntimeRequiresResolvedBindingAuthority(t *testing.T) {
	t.Setenv("PULSE_VAULT_STORE_ID", "store_personal_nik")
	t.Setenv("PULSE_BINDING_DIGEST", "")
	t.Setenv("PULSE_POLICY_EPOCH", "")
	t.Setenv("PULSE_RESOLVER_EPOCH", "")
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runProductLocal(dataDir, "127.0.0.1:18800", config.VaultPersonal); err == nil ||
		!strings.Contains(err.Error(), "runtime authority") {
		t.Fatalf("missing runtime authority error = %v", err)
	}
}
