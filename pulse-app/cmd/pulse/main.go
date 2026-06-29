package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nkkmnk/pulse/internal/claude"
	"github.com/nkkmnk/pulse/internal/config"
	"github.com/nkkmnk/pulse/internal/contextquery"
	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/expand"
	"github.com/nkkmnk/pulse/internal/health"
	"github.com/nkkmnk/pulse/internal/model"
	"github.com/nkkmnk/pulse/internal/outbox"
	"github.com/nkkmnk/pulse/internal/prompt"
	"github.com/nkkmnk/pulse/internal/providers/anthropic"
	"github.com/nkkmnk/pulse/internal/providers/openaicompat"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/server"
	"github.com/nkkmnk/pulse/internal/store"
)

const (
	defaultAddr  = "127.0.0.1:18789"
	defaultModel = "claude-opus-4-6"
	defaultAlias = "anthropic/opus"
)

func main() {
	var (
		dataDir = flag.String("data-dir", filepath.Join(os.Getenv("HOME"), ".pulse"), "data directory")
		addr    = flag.String("addr", defaultAddr, "listen address")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(*dataDir, *addr); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run(dataDir, addr string) error {
	cfg, err := config.Load(dataDir)
	if err != nil {
		return err
	}
	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer s.Close()

	ob := outbox.New(s.DB(), 30*time.Second)

	var cc server.ClaudeAPI
	var builder *prompt.Builder
	backendLLMEnabled := cfg.AnthropicAPIKey != ""
	localAutoMode := cfg.Mode == "local-auto" || os.Getenv("PULSE_LOCAL_AUTO") == "1"
	rawCaptureEnabled := os.Getenv("PULSE_LOCAL_AUTO_RAW_REFS") == "1" || os.Getenv("PULSE_RAW_REFS") == "1"
	if backendLLMEnabled {
		reg, err := model.LoadRegistry(dataDir)
		if err != nil {
			return err
		}
		router := model.NewRouter(reg, map[string]model.Provider{
			model.ProviderAnthropic:    anthropic.New(),
			model.ProviderOpenAICompat: openaicompat.New(),
		})
		cc = claude.NewRouterAdapter(router, defaultAlias)
		slog.Info("model router wired",
			"default_alias", defaultAlias,
			"registry_default", reg.DefaultAlias())

		soulPath := filepath.Join(dataDir, "soul.md")
		builder, err = prompt.NewBuilder(soulPath)
		if err != nil {
			return err
		}
	} else {
		slog.Info("host-extracted memory mode: backend LLM disabled (ANTHROPIC_API_KEY not set)")
	}

	// Wire Phase G hybrid retrieval engine. Cohere key is optional — when
	// absent we leave Retrieval=nil so /retrieve returns 503 instead of 404.
	retrievalEngine, err := initRetrieval(s, dataDir)
	if err != nil {
		// Log but don't fail startup — retrieval is opt-in.
		slog.Warn("retrieval init failed; /retrieve will return 503", "error", err)
	}
	var contextQuery server.ContextQueryAPI
	if retrievalEngine != nil {
		contextQuery = contextquery.New(contextquery.ServiceConfig{
			DB:        s.DB(),
			Retrieval: retrievalEngine,
			// Temporal entity-graph retrieval on the LIVE recall path. "anchored"
			// is the validated control-safe win (entity-centric recall ↑, no
			// control regression). Override per-request via graph_mode; set ""
			// here to disable. "walk" stays opt-in (multi-hop unproven on real graph).
			GraphMode: "anchored",
		})
	}

	// Claim resolution (default OFF). Enabled only when an embedder is wired and
	// PULSE_CLAIM_RESOLUTION is shadow|on. Precision-first; never touches v3.
	if retrievalEngine != nil && retrievalEngine.EmbedderReady() {
		if mode := strings.TrimSpace(os.Getenv("PULSE_CLAIM_RESOLUTION")); mode == "shadow" || mode == "on" {
			thr := 0.83
			if v := strings.TrimSpace(os.Getenv("PULSE_CLAIM_COSINE_THRESHOLD")); v != "" {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					thr = f
				}
			}
			eng := retrievalEngine
			s.EnableClaimResolution(mode, thr, func(t string) ([]float32, error) {
				return eng.EmbedText(context.Background(), t)
			})
			xkey := os.Getenv("PULSE_CLAIM_XKEY") == "1"
			if xkey {
				xthr := 0.90
				if v := strings.TrimSpace(os.Getenv("PULSE_CLAIM_XKEY_THRESHOLD")); v != "" {
					if f, err := strconv.ParseFloat(v, 64); err == nil {
						xthr = f
					}
				}
				s.EnableCrossKey(xthr)
			}
			slog.Info("claim resolution enabled", "mode", mode, "threshold", thr, "cross_key", xkey)
		}
	}

	// Apple Health snapshot provider. M0 = static fixture so demos and
	// client integration can develop without a real health bridge. Anchor
	// at startup so the demo's "today" stays consistent during a single
	// process lifetime.
	healthProvider := health.NewFixtureProvider(time.Now())

	srv, err := server.New(server.Config{
		IPCSecret:    cfg.IPCSecret,
		Outbox:       ob,
		Builder:      builder,
		Claude:       cc,
		DefaultModel: defaultModel,
		Store:        s,
		Billing: server.BillingStatus{
			Mode:              pulseMode(localAutoMode),
			Host:              "claude-code",
			BackendLLMEnabled: backendLLMEnabled,
			RawCaptureEnabled: rawCaptureEnabled,
			StoragePath:       cfg.DBPath,
		},
		Retrieval:    retrievalEngine,
		ContextQuery: contextQuery,
		Health:       healthProvider,
	})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Background reaper for stale outbox leases.
	reaperCtx, cancelReaper := context.WithCancel(context.Background())

	var reaperWG sync.WaitGroup
	reaperWG.Add(1)
	go func() {
		defer reaperWG.Done()
		reaperLoop(reaperCtx, ob)
	}()

	slog.Info("pulse listening", "addr", addr, "data_dir", dataDir)

	// Graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var runErr error
	select {
	case <-sigCh:
		slog.Info("shutdown signal received")
	case err := <-errCh:
		slog.Error("server error", "error", err)
		runErr = err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if sErr := httpSrv.Shutdown(shutdownCtx); sErr != nil && runErr == nil {
		runErr = sErr
	}

	// Stop reaper BEFORE db.Close fires (via defer). Otherwise reaper may
	// error mid-Reap when the DB is closed under it.
	cancelReaper()
	reaperWG.Wait()

	return runErr
}

func pulseMode(localAuto bool) string {
	if localAuto {
		return "local-auto"
	}
	return "host-extracted"
}

func reaperLoop(ctx context.Context, ob *outbox.Outbox) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := ob.Reap(); err != nil {
				slog.Warn("reaper error", "error", err)
			} else if n > 0 {
				slog.Info("reaper requeued", "count", n)
			}
		}
	}
}

// initRetrieval wires the Phase G hybrid retrieval engine. Returns (nil, nil)
// when no Cohere API key is configured — that's not an error, just a signal
// that /retrieve should respond with 503. Returns (nil, err) only when the
// key IS present but the engine fails to load its indexes.
//
// Key resolution order:
//  1. COHERE_API_KEY env var
//  2. ~/.pulse/cohere-key.txt
//
// embedderFromEnv returns the best available embedder.
// Priority:
//  1. COHERE_API_KEY (env or ~/.pulse/cohere-key.txt) → CohereClient
//  2. PULSE_LOCAL_EMBED_PYTHON + PULSE_LOCAL_EMBED_HELPER + PULSE_LOCAL_EMBED_MODEL
//     all set and pointing at existing files → LocalClient (optional MLX/
//     transformers embedding helper, see scripts in your deployment).
//  3. nil (retrieval disabled, /retrieve and /context/query return 503).
func embedderFromEnv() (retrieve.Embedder, string, error) {
	apiKey := strings.TrimSpace(os.Getenv("COHERE_API_KEY"))
	if apiKey == "" {
		home, _ := os.UserHomeDir()
		keyPath := filepath.Join(home, ".pulse", "cohere-key.txt")
		if data, err := os.ReadFile(keyPath); err == nil {
			apiKey = strings.TrimSpace(string(data))
		}
	}
	if apiKey != "" {
		c := embed.NewCohere(apiKey, "", "")
		return c, c.Model(), nil
	}

	// Optional local embedder. Disabled unless all three env vars are set and
	// point at existing files; otherwise we skip straight to retrieval-only.
	pythonExe := os.Getenv("PULSE_LOCAL_EMBED_PYTHON")
	helperPath := os.Getenv("PULSE_LOCAL_EMBED_HELPER")
	modelPath := os.Getenv("PULSE_LOCAL_EMBED_MODEL")
	if pythonExe == "" || helperPath == "" || modelPath == "" {
		return nil, "", nil
	}

	// Both helper script and python must exist
	if _, err := os.Stat(pythonExe); err == nil {
		if _, err := os.Stat(helperPath); err == nil {
			if _, err := os.Stat(modelPath); err == nil {
				lc := embed.NewLocal(pythonExe, helperPath, modelPath, "bge-m3-mlx-fp16")
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				if err := lc.Start(ctx); err != nil {
					return nil, "", fmt.Errorf("local embedder start: %w", err)
				}
				return lc, lc.Model(), nil
			}
		}
	}

	return nil, "", nil
}

func initRetrieval(s *store.Store, dataDir string) (*retrieve.Engine, error) {
	embedder, name, err := embedderFromEnv()
	if err != nil {
		return nil, err
	}
	if embedder == nil {
		slog.Info("retrieval: no embedder configured (set COHERE_API_KEY for Cohere, or ensure local mlx_embed_helper.py + bge-m3 model are present); /retrieve and /context/query will respond 503")
		return nil, nil
	}

	expander := expanderFromEnv(dataDir)

	engine := retrieve.New(retrieve.Config{
		Store:            s,
		Embedder:         embedder,
		Expander:         expander,
		AssertionOverlay: os.Getenv("PULSE_ASSERTION_OVERLAY") == "1",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := engine.Init(ctx); err != nil {
		return nil, err
	}
	slog.Info("retrieval: engine initialized", "embedder", name, "expander", expander != nil)
	return engine, nil
}

// expanderFromEnv returns a query-expansion client when
// PULSE_QUERY_EXPAND=1 and the helper + Qwen3 model are on disk.
// Helper takes ~30-60s to load Qwen3-30B at startup; we accept that
// blocking cost once because expansion runs on every retrieval and
// query-time would be too long for a cold spawn.
func expanderFromEnv(dataDir string) retrieve.Expander {
	if os.Getenv("PULSE_QUERY_EXPAND") != "1" {
		return nil
	}
	// All three must be set and point at existing files; otherwise expansion
	// stays disabled. The helper is an optional local LLM (e.g. an MLX or
	// transformers model) that rewrites queries into grounded lexical terms.
	pythonExe := os.Getenv("PULSE_QUERY_EXPAND_PYTHON")
	helperPath := os.Getenv("PULSE_QUERY_EXPAND_HELPER")
	modelPath := os.Getenv("PULSE_QUERY_EXPAND_MODEL")
	if pythonExe == "" || helperPath == "" || modelPath == "" {
		return nil
	}
	dbPath := filepath.Join(dataDir, "pulse.db")
	for _, p := range []string{pythonExe, helperPath, modelPath, dbPath} {
		if _, err := os.Stat(p); err != nil {
			slog.Warn("query-expand: missing dependency, disabled", "path", p, "error", err)
			return nil
		}
	}
	client := expand.NewLocal(pythonExe, helperPath, dbPath, modelPath)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		slog.Warn("query-expand: helper failed to start", "error", err)
		return nil
	}
	slog.Info("query-expand: helper started", "model", filepath.Base(modelPath))
	return client
}
