package embed

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalHelperProcess(t *testing.T) {
	if os.Getenv("PULSE_LOCAL_HELPER_PROCESS") != "1" {
		return
	}
	mode := os.Getenv("PULSE_LOCAL_HELPER_MODE")
	startup := map[string]any{
		"dimensions": 1024,
		"id":         "__startup__",
		"model":      "bge-m3",
		"normalized": true,
		"ok":         true,
		"pooling":    "cls",
		"protocol":   1,
		"schema":     "pulse.embedder.ready.v1",
	}
	if mode == "legacy" {
		startup = map[string]any{"id": "__startup__", "load_ms": 12.5, "ok": true}
	}
	if mode == "startup-extra" {
		startup["unexpected"] = true
	}
	if mode == "env" {
		switch {
		case os.Getenv("COHERE_API_KEY") != "":
			startup["unexpected"] = "secret environment leaked"
		case os.Getenv("PULSE_PRODUCT_AUTHORITY_NODE") != "/trusted/node":
			startup["unexpected"] = "trusted Node runtime was not forwarded"
		}
	}
	line, _ := json.Marshal(startup)
	fmt.Fprintln(os.Stdout, string(line))
	if mode == "startup-extra" {
		select {}
	}
	if mode == "no-read" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode == "exit-after-ready" {
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID    string   `json:"id"`
			Texts []string `json:"texts"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &request)
		count := len(request.Texts)
		if mode == "count" {
			count++
		}
		vectors := make([][]float32, count)
		for index := range vectors {
			vectors[index] = make([]float32, 1024)
			vectors[index][0] = 1
			if mode == "dimension" {
				vectors[index] = vectors[index][:1023]
			}
			if mode == "nan" {
				vectors[index][0] = float32(math.NaN())
			}
			if mode == "norm" {
				vectors[index][0] = 2
			}
		}
		response := map[string]any{
			"embeddings": vectors,
			"id":         request.ID,
			"schema":     "pulse.embedder.response.v1",
		}
		if mode == "legacy" {
			delete(response, "schema")
			response["dim"] = 1024
			response["latency_ms"] = 3.2
			response["n"] = len(vectors)
		}
		encoded, _ := json.Marshal(response)
		fmt.Fprintln(os.Stdout, string(encoded))
	}
}

func managedTestClient(t *testing.T, mode string) *LocalClient {
	t.Helper()
	t.Setenv("PULSE_LOCAL_HELPER_PROCESS", "1")
	t.Setenv("PULSE_LOCAL_HELPER_MODE", mode)
	return newLocalClient(localClientConfig{
		command:       os.Args[0],
		commandArgs:   []string{"-test.run=TestLocalHelperProcess"},
		dimensions:    1024,
		environment:   []string{"PULSE_LOCAL_HELPER_PROCESS=1", "PULSE_LOCAL_HELPER_MODE=" + mode},
		managed:       true,
		modelName:     "bge-m3",
		responseBytes: localMaximumResponseBytes,
	})
}

func TestManagedLocalAcceptsExactProtocolAndNormalizedVectors(t *testing.T) {
	client := managedTestClient(t, "ok")
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	vectors, err := client.Embed(ctx, []string{"alpha", "beta"}, TypeSearchDocument)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || len(vectors[0]) != 1024 || vectors[0][0] != 1 {
		t.Fatalf("unexpected vectors: count=%d dim=%d first=%v", len(vectors), len(vectors[0]), vectors[0][0])
	}
}

func TestManagedLocalV2UsesExactRunnerAndBoundedArgs(t *testing.T) {
	config := ManagedLocalConfig{
		RunnerPath: os.Args[0],
		RunnerArgs: []string{"-test.run=TestLocalHelperProcess", "--model-root", "/tmp/model root"},
		Engine:     "transformers-js-onnx",
		VectorContract: ManagedVectorContract{
			Model: "bge-m3", Source: "BAAI/bge-m3",
			Revision:   "5617a9f61b028005a4858fdac845db406aefb181",
			Dimensions: 1024, Pooling: "cls", Normalized: true, Opset: 17, Quantization: "dynamic-int8",
		},
	}
	client := NewManagedLocal(config)
	if client.command != config.RunnerPath || strings.Join(client.commandArgs, "\x00") != strings.Join(config.RunnerArgs, "\x00") {
		t.Fatalf("runner contract = %q %q", client.command, client.commandArgs)
	}
	if client.Model() != "bge-m3" || client.dimensions != 1024 {
		t.Fatalf("vector contract = %s/%d", client.Model(), client.dimensions)
	}
}

func TestManagedLocalDoesNotPassRemoteProviderEnvironment(t *testing.T) {
	t.Setenv("COHERE_API_KEY", "must-not-reach-managed-helper")
	t.Setenv("PULSE_PRODUCT_AUTHORITY_NODE", "/trusted/node")
	client := managedTestClient(t, "env")
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyLocalKeepsDeveloperAndTeamHelperExtrasCompatible(t *testing.T) {
	t.Setenv("PULSE_LOCAL_HELPER_PROCESS", "1")
	t.Setenv("PULSE_LOCAL_HELPER_MODE", "legacy")
	client := newLocalClient(localClientConfig{
		command: os.Args[0], commandArgs: []string{"-test.run=TestLocalHelperProcess"},
		dimensions: 1024, modelName: "bge-m3",
	})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	vectors, err := client.Embed(ctx, []string{"legacy team helper"}, TypeSearchDocument)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 1 || len(vectors[0]) != 1024 {
		t.Fatalf("legacy vectors = %d", len(vectors))
	}
}

func TestManagedLocalRejectsNonExactStartupAndReaps(t *testing.T) {
	client := managedTestClient(t, "startup-extra")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := client.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "startup contract") {
		t.Fatalf("error = %v", err)
	}
	if client.cmd != nil {
		t.Fatal("failed startup left helper process attached")
	}
}

func TestManagedLocalRejectsMalformedVectorsAndReaps(t *testing.T) {
	for _, mode := range []string{"count", "dimension", "nan", "norm"} {
		t.Run(mode, func(t *testing.T) {
			client := managedTestClient(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.Start(ctx); err != nil {
				t.Fatal(err)
			}
			_, err := client.Embed(ctx, []string{"alpha"}, TypeSearchQuery)
			if err == nil {
				t.Fatal("malformed vector accepted")
			}
			if client.cmd != nil {
				t.Fatal("protocol failure left helper process attached")
			}
		})
	}
}

func TestManagedLocalRejectsOversizedInputBeforeStarting(t *testing.T) {
	client := managedTestClient(t, "ok")
	ctx := context.Background()
	if _, err := client.Embed(ctx, make([]string, MaxBatchSize+1), TypeSearchQuery); err == nil ||
		!strings.Contains(err.Error(), "batch") {
		t.Fatalf("batch error = %v", err)
	}
	tooLong := strings.Repeat("x", localMaximumTextBytes+1)
	if _, err := client.Embed(ctx, []string{tooLong}, TypeSearchQuery); err == nil ||
		!strings.Contains(err.Error(), "text") {
		t.Fatalf("text error = %v", err)
	}
}

func TestManagedLocalWriteDeadlineKillsAndReapsHelperThatStopsReading(t *testing.T) {
	client := managedTestClient(t, "no-read")
	client.callTimeout = 75 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	texts := make([]string, MaxBatchSize)
	for index := range texts {
		texts[index] = strings.Repeat("x", localMaximumTextBytes)
	}
	started := time.Now()
	_, err := client.Embed(ctx, texts, TypeSearchDocument)
	if err == nil || !strings.Contains(err.Error(), "accept request within timeout") {
		t.Fatalf("write timeout error = %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("blocked write ignored deadline: %s", time.Since(started))
	}
	if client.Ready() || client.cmd != nil {
		t.Fatal("timed-out write left helper ready or attached")
	}
}

func TestManagedLocalIdleHelperExitClearsLiveReadiness(t *testing.T) {
	client := managedTestClient(t, "exit-after-ready")
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for client.Ready() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if client.Ready() {
		t.Fatal("exited helper remained live-ready")
	}
}

func TestManagedLocalRealBGEVectorWhenReleaseInputsAreAvailable(t *testing.T) {
	python := os.Getenv("PULSE_TEST_MANAGED_EMBED_PYTHON")
	helper := os.Getenv("PULSE_TEST_MANAGED_EMBED_HELPER")
	model := os.Getenv("PULSE_TEST_MANAGED_EMBED_MODEL")
	support := os.Getenv("PULSE_TEST_MANAGED_EMBED_SUPPORT")
	if python == "" || helper == "" || model == "" || support == "" {
		t.Skip("managed release inputs are not available")
	}
	client := NewManagedLocal(ManagedLocalConfig{
		RunnerPath: python,
		RunnerArgs: []string{helper, "--model-file", model, "--support-dir", support},
		Engine:     "mlx",
		VectorContract: ManagedVectorContract{
			Model: "bge-m3", Source: "BAAI/bge-m3",
			Revision:   "5617a9f61b028005a4858fdac845db406aefb181",
			Dimensions: 1024, Pooling: "cls", Normalized: true, Opset: 17, Quantization: "dynamic-int8",
		},
	})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	vectors, err := client.Embed(ctx, []string{"Пульс сохраняет контекст проекта."}, TypeSearchDocument)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 1 || len(vectors[0]) != localManagedDimensions {
		t.Fatalf("real vector shape = %d x %d", len(vectors), len(vectors[0]))
	}
}
