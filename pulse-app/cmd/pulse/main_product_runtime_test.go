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
	supportDir := filepath.Join(modelDir, "support")
	if err := os.MkdirAll(filepath.Join(root, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(supportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(runtimeDir, "bin", "pulse-embedder")
	model := filepath.Join(modelDir, "model_int8.onnx")
	for path, mode := range map[string]os.FileMode{
		runner:                                   0o755,
		model:                                    0o644,
		filepath.Join(supportDir, "config.json"): 0o644,
		filepath.Join(supportDir, "tokenizer.json"):          0o644,
		filepath.Join(supportDir, "tokenizer_config.json"):   0o644,
		filepath.Join(supportDir, "special_tokens_map.json"): 0o644,
		filepath.Join(modelDir, "pulse-model-contract.json"): 0o644,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root, map[string]any{
		"engine":                             "transformers-js-onnx",
		"embedder_runtime_activation_digest": strings.Repeat("c", 64),
		"embedder_runtime_tree_digest":       strings.Repeat("d", 64),
		"model_activation_digest":            strings.Repeat("e", 64),
		"model_root":                         modelDir,
		"model_tree_digest":                  strings.Repeat("f", 64),
		"protocol":                           1,
		"runner_args":                        []string{"--model-root", modelDir, "--support-root", supportDir},
		"runner_path":                        runner,
		"schema":                             "pulse.managed_embedder.config.v2",
		"support_root":                       supportDir,
		"vector_contract": map[string]any{
			"model": "bge-m3", "source": "BAAI/bge-m3",
			"revision":   "5617a9f61b028005a4858fdac845db406aefb181",
			"dimensions": 1024, "pooling": "cls", "normalized": true,
			"opset": 17, "quantization": "dynamic-int8",
		},
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
	if config.RunnerPath != value["runner_path"] || config.ModelRoot != value["model_root"] ||
		len(config.RunnerArgs) != 4 {
		t.Fatalf("config = %+v", config)
	}

	value["unknown"] = true
	writeManagedEmbedderFixture(t, root, value)
	if _, err := loadManagedEmbedderConfig(); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("unknown-field error = %v", err)
	}
}

func TestManagedEmbedderConfigV1IsHistoricalNotReady(t *testing.T) {
	root, value := managedEmbedderFixture(t)
	value["schema"] = "pulse.managed_embedder.config.v1"
	delete(value, "runner_path")
	delete(value, "runner_args")
	delete(value, "engine")
	delete(value, "model_root")
	delete(value, "support_root")
	delete(value, "vector_contract")
	value["python_executable"] = filepath.Join(root, "legacy", "python")
	value["helper_path"] = filepath.Join(root, "legacy", "helper.py")
	value["model_file"] = filepath.Join(root, "legacy", "model.safetensors")
	value["support_directory"] = filepath.Join(root, "legacy", "support")
	value["model"] = "bge-m3"
	value["dimensions"] = 1024
	value["pooling"] = "cls"
	value["normalized"] = true
	path := writeManagedEmbedderFixture(t, root, value)
	t.Setenv("PULSE_MANAGED_EMBEDDER_CONFIG", path)
	if _, err := loadManagedEmbedderConfig(); err == nil || !strings.Contains(err.Error(), "historical") {
		t.Fatalf("v1 compatibility error = %v", err)
	}
}

func TestManagedEmbedderConfigRejectsRunnerAndVectorDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(root string, value map[string]any)
		want   string
	}{
		{
			name:   "wrong engine",
			mutate: func(_ string, value map[string]any) { value["engine"] = "python-system" },
			want:   "contract mismatch",
		},
		{
			name: "vector dimension drift",
			mutate: func(_ string, value map[string]any) {
				value["vector_contract"].(map[string]any)["dimensions"] = 384
			},
			want: "contract mismatch",
		},
		{
			name: "too many args",
			mutate: func(_ string, value map[string]any) {
				value["runner_args"] = make([]string, managedEmbedderMaximumArgs+1)
				for index := range value["runner_args"].([]string) {
					value["runner_args"].([]string)[index] = "--safe"
				}
			},
			want: "bounded",
		},
		{
			name: "remote arg",
			mutate: func(_ string, value map[string]any) {
				value["runner_args"] = []string{"--model", "https://example.test/model.onnx"}
			},
			want: "arg 1 is invalid",
		},
		{
			name:   "support escapes model tree",
			mutate: func(root string, value map[string]any) { value["support_root"] = filepath.Join(root, "outside") },
			want:   "inside",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, original := managedEmbedderFixture(t)
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(encoded, &value); err != nil {
				t.Fatal(err)
			}
			test.mutate(root, value)
			path := writeManagedEmbedderFixture(t, root, value)
			t.Setenv("PULSE_MANAGED_EMBEDDER_CONFIG", path)
			if _, err := loadManagedEmbedderConfig(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
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
