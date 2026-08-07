package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nkkmnk/pulse/internal/claude"
	"github.com/nkkmnk/pulse/internal/config"
	"github.com/nkkmnk/pulse/internal/contextquery"
	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/expand"
	"github.com/nkkmnk/pulse/internal/health"
	"github.com/nkkmnk/pulse/internal/model"
	"github.com/nkkmnk/pulse/internal/outbox"
	"github.com/nkkmnk/pulse/internal/platform"
	"github.com/nkkmnk/pulse/internal/prompt"
	"github.com/nkkmnk/pulse/internal/providers/anthropic"
	"github.com/nkkmnk/pulse/internal/providers/openaicompat"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/server"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/userpresence"
)

const (
	defaultAddr  = "127.0.0.1:18789"
	defaultModel = "claude-opus-4-6"
	defaultAlias = "anthropic/opus"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "local-migrate" {
		if err := runLocalMigrateCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
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

func runLocalMigrateCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("local-migrate requires preview, status, commit, or rollback")
	}
	switch args[0] {
	case "preview":
		flags := flag.NewFlagSet("local-migrate preview", flag.ContinueOnError)
		home := flags.String("home", "", "home directory")
		canonical := flags.String("canonical", "", "current Personal database")
		storeID := flags.String("store-id", "", "Personal store id")
		out := flags.String("out", "", "preview JSON output")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !filepath.IsAbs(*home) || !filepath.IsAbs(*canonical) || !filepath.IsAbs(*out) {
			return errors.New("local-migrate preview requires absolute --home, --canonical, and --out")
		}
		preview, err := store.BuildLocalMergePreview(*home, *canonical, *storeID, time.Now())
		if err != nil {
			return err
		}
		if err := store.WriteLocalMergePreview(*out, preview); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(preview)
	case "status":
		flags := flag.NewFlagSet("local-migrate status", flag.ContinueOnError)
		home := flags.String("home", "", "home directory")
		canonical := flags.String("canonical", "", "current Personal database")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !filepath.IsAbs(*home) || !filepath.IsAbs(*canonical) {
			return errors.New("local-migrate status requires absolute --home and --canonical")
		}
		status, err := store.InspectLocalMergeStatus(*home, *canonical)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	case "commit":
		flags := flag.NewFlagSet("local-migrate commit", flag.ContinueOnError)
		previewPath := flags.String("preview", "", "reviewed preview JSON")
		confirmation := flags.String("confirm", "", "exact human confirmation")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !filepath.IsAbs(*previewPath) {
			return errors.New("local-migrate commit requires absolute --preview")
		}
		archivePath, err := store.CommitLocalMergePreview(*previewPath, *confirmation, time.Now())
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"ok": true, "archive_path": archivePath,
		})
	case "rollback":
		flags := flag.NewFlagSet("local-migrate rollback", flag.ContinueOnError)
		previewPath := flags.String("preview", "", "reviewed preview JSON")
		archivePath := flags.String("archive", "", "rollback database")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !filepath.IsAbs(*previewPath) || !filepath.IsAbs(*archivePath) {
			return errors.New("local-migrate rollback requires absolute --preview and --archive")
		}
		if err := store.RestoreLocalMergeArchive(*previewPath, *archivePath); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true})
	default:
		return errors.New("local-migrate supports preview, status, commit, or rollback")
	}
}

func run(dataDir, addr string) error {
	switch os.Getenv("PULSE_RUNTIME_MODE") {
	case "", "local-stdio", "development-http":
		return runLocal(dataDir, addr)
	case "personal-local":
		return runProductLocal(dataDir, addr, config.VaultPersonal)
	default:
		return errors.New("Pulse Personal supports only local runtime modes")
	}
}

func runLocal(dataDir, addr string) error {
	return runLocalVault(dataDir, addr, "", "")
}

func runProductLocal(dataDir, addr string, kind config.VaultKind) error {
	if !isLoopbackListenAddress(addr) {
		return errors.New("product local vault must bind to a loopback address")
	}
	storeID := strings.TrimSpace(os.Getenv("PULSE_VAULT_STORE_ID"))
	if storeID == "" || storeID != os.Getenv("PULSE_VAULT_STORE_ID") {
		return errors.New("product local vault requires exact PULSE_VAULT_STORE_ID")
	}
	return runLocalVault(dataDir, addr, kind, storeID)
}

func runLocalVault(dataDir, addr string, kind config.VaultKind, storeID string) error {
	var cfg *config.Config
	var err error
	if kind == "" {
		cfg, err = config.Load(dataDir)
	} else {
		cfg, err = config.LoadVault(dataDir, kind, storeID)
	}
	if err != nil {
		return err
	}
	var s *store.Store
	if kind == "" {
		s, err = store.Open(cfg.DBPath)
	} else {
		s, err = store.OpenVault(cfg.DBPath, store.StoreKind(kind), storeID)
	}
	if err != nil {
		return err
	}
	defer s.Close()
	var homeBindingVerifier server.HomeBindingVerifier
	var productBindingVerifier server.ProductBindingVerifier
	if kind != "" {
		bindingDigest := os.Getenv("PULSE_BINDING_DIGEST")
		repositoryID := os.Getenv("PULSE_REPOSITORY_ID")
		policyEpoch, policyErr := strconv.ParseInt(os.Getenv("PULSE_POLICY_EPOCH"), 10, 64)
		resolverEpoch, resolverErr := strconv.ParseInt(os.Getenv("PULSE_RESOLVER_EPOCH"), 10, 64)
		if policyErr != nil || resolverErr != nil || resolverEpoch < 1 {
			return errors.New("product local vault requires valid runtime authority epochs")
		}
		if err := s.ConfigureProductRuntimeAuthority(bindingDigest, policyEpoch, resolverEpoch); err != nil {
			return fmt.Errorf("configure product runtime authority: %w", err)
		}
		if err := s.ConfigureContinuityDeliveryAuthority(bindingDigest, repositoryID); err != nil {
			return fmt.Errorf("configure continuity delivery authority: %w", err)
		}
		workspace := os.Getenv("PULSE_PRODUCT_WORKSPACE")
		if err := s.RegisterPersonalProjectLabel(
			repositoryID, filepath.Base(filepath.Clean(workspace)),
		); err != nil {
			return fmt.Errorf("configure Personal project label: %w", err)
		}
		homeBindingVerifier, err = server.NewCommandHomeBindingVerifier(
			os.Getenv("PULSE_PRODUCT_AUTHORITY_NODE"), os.Getenv("PULSE_PRODUCT_AUTHORITY_HELPER"),
			workspace, resolverEpoch,
		)
		if err != nil {
			return fmt.Errorf("configure live product binding verifier: %w", err)
		}
		productBindingVerifier, err = server.NewCommandProductBindingVerifier(
			os.Getenv("PULSE_PRODUCT_AUTHORITY_NODE"), os.Getenv("PULSE_PRODUCT_AUTHORITY_HELPER"),
		)
		if err != nil {
			return fmt.Errorf("configure request product binding verifier: %w", err)
		}
	}

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
	retrievalEngine, err := initRetrieval(s, dataDir, kind != "")
	if err != nil {
		if kind != "" {
			return fmt.Errorf("managed retrieval init: %w", err)
		}
		// Development retrieval is opt-in, so a broken optional dependency
		// does not stop the rest of the developer daemon.
		slog.Warn("retrieval init failed; /retrieve will return 503", "error", err)
	}
	var contextQuery server.ContextQueryAPI
	if retrievalEngine != nil {
		contextQuery = contextquery.New(contextquery.ServiceConfig{
			DB:            s.DB(),
			Retrieval:     retrievalEngine,
			EmotionMemory: s,
			// Temporal entity-graph retrieval on the LIVE recall path. "anchored"
			// is the validated control-safe win (entity-centric recall ↑, no
			// control regression). Override per-request via graph_mode; set ""
			// here to disable. "walk" stays opt-in (multi-hop unproven on real graph).
			GraphMode: "anchored",
			// Real life and fiction (e.g. a book being written) must not mix by
			// default: context queries return real-domain memory unless the caller
			// explicitly asks for fiction (book work).
			DefaultDomains: []string{"real"},
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
			// Paraphrase claim matching (default OFF). A reworded restatement whose
			// claim_key has no exact match corroborates/supersedes the embedding-
			// nearest same-scope claim. Reuses the embedder wired above — no new
			// network path, and a no-op whenever the embedder is unavailable.
			paraphrase := os.Getenv("PULSE_PARAPHRASE_CLAIMS") == "1"
			if paraphrase {
				pthr := 0.90
				if v := strings.TrimSpace(os.Getenv("PULSE_PARAPHRASE_THRESHOLD")); v != "" {
					if f, err := strconv.ParseFloat(v, 64); err == nil {
						pthr = f
					}
				}
				s.EnableParaphraseClaims(pthr)
			}
			slog.Info("claim resolution enabled", "mode", mode, "threshold", thr, "cross_key", xkey, "paraphrase", paraphrase)
		}
	}

	// Apple Health snapshot provider. M0 = static fixture so demos and
	// client integration can develop without a real health bridge. Anchor
	// at startup so the demo's "today" stays consistent during a single
	// process lifetime.
	healthProvider := health.NewFixtureProvider(time.Now())

	billingHost := strings.TrimSpace(os.Getenv("PULSE_HOST"))
	if billingHost != "codex" && billingHost != "claude-code" && billingHost != "pulse-product" {
		billingHost = "pulse-product"
	}
	homeOrigin := ""
	unassignedInboxPath := ""
	var homePresence server.HomePresence
	var enhancedPresenceAuthorizer userpresence.EnhancedPresenceAuthorizer = userpresence.NewUnavailableAuthorizer("enhanced_presence_unavailable")
	if kind != "" {
		homeOrigin = "http://" + addr
		userHome, homeErr := os.UserHomeDir()
		if homeErr != nil || !filepath.IsAbs(userHome) {
			return fmt.Errorf("configure unassigned inbox: user home unavailable")
		}
		unassignedInboxPath = filepath.Join(userHome, ".pulse", "supervisor", "unassigned-inbox.json")
		presenceGate, err := userpresence.NewGate(userpresence.NewPlatformProver(), time.Now)
		if err != nil {
			return fmt.Errorf("configure Memory Home OS presence: %w", err)
		}
		homePresence = presenceGate
		inspectionCtx, cancelInspection := context.WithTimeout(context.Background(), 5*time.Second)
		enhancedPresenceAuthorizer = userpresence.NewPlatformEnhancedAuthorizer(
			inspectionCtx, time.Now, rand.Reader,
		)
		cancelInspection()
	}
	srv, err := server.New(server.Config{
		IPCSecret:    cfg.IPCSecret,
		StartupNonce: os.Getenv("PULSE_STARTUP_NONCE"),
		Outbox:       ob,
		Builder:      builder,
		Claude:       cc,
		DefaultModel: defaultModel,
		Store:        s,
		Billing: server.BillingStatus{
			Mode:              pulseMode(localAutoMode),
			Host:              billingHost,
			BackendLLMEnabled: backendLLMEnabled,
			RawCaptureEnabled: rawCaptureEnabled,
			StoragePath:       cfg.DBPath,
		},
		Retrieval:                   retrievalEngine,
		ContextQuery:                contextQuery,
		Health:                      healthProvider,
		HomeOrigin:                  homeOrigin,
		HomePresence:                homePresence,
		EnhancedPresenceAuthorizer:  enhancedPresenceAuthorizer,
		UnassignedInboxPath:         unassignedInboxPath,
		HomeBindingVerifier:         homeBindingVerifier,
		ProductBindingVerifier:      productBindingVerifier,
		EnableHistoricalCodexSource: kind != "",
	})
	if err != nil {
		return err
	}
	defer srv.Close()

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
	reaperWG.Add(1)
	go func() {
		defer reaperWG.Done()
		backfillCapsuleEvents(reaperCtx, s, retrievalEngine)
	}()

	slog.Info("pulse listening", "addr", addr, "data_dir", dataDir,
		"vault_kind", s.StoreKind(), "store_id", s.StoreID())

	// Graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, platform.ShutdownSignals()...)

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

func backfillCapsuleEvents(ctx context.Context, s *store.Store, engine *retrieve.Engine) {
	projected := 0
	for ctx.Err() == nil {
		docs, err := s.BackfillCapsuleEventsBatch(500)
		if err != nil {
			slog.Warn("capsule event backfill failed", "error", err)
			return
		}
		projected += len(docs)
		if len(docs) < 500 {
			break
		}
	}
	if projected > 0 {
		slog.Info("capsule events backfilled", "count", projected)
	}
	if ctx.Err() != nil || engine == nil || !engine.EmbedderReady() {
		return
	}
	persisted := 0
	consecutiveFailures := 0
	for ctx.Err() == nil {
		docs, err := s.UnindexedHostEventDocs(500)
		if err != nil {
			slog.Warn("capsule event embed-index list failed", "error", err)
			return
		}
		if len(docs) == 0 {
			break
		}
		indexDocs := make([]retrieve.IndexEventDoc, len(docs))
		for i, doc := range docs {
			indexDocs[i] = retrieve.IndexEventDoc{EventID: doc.EventID, Text: doc.Text}
		}
		batchCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		committed, embedErr := engine.EmbedAndPersistEvents(batchCtx, indexDocs)
		cancel()
		persisted += committed
		if embedErr != nil {
			consecutiveFailures++
			slog.Warn("capsule event embed-index failed", "error", embedErr, "committed", committed,
				"attempt", consecutiveFailures)
			if consecutiveFailures >= 3 {
				break
			}
			delay := time.Duration(consecutiveFailures) * 100 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		consecutiveFailures = 0
		if len(docs) < 500 {
			break
		}
	}
	if persisted > 0 {
		reloadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := engine.Reload(reloadCtx)
		cancel()
		if err != nil {
			slog.Warn("capsule event embed-index reload failed", "error", err)
		}
	}
}

func isLoopbackListenAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

func initRetrieval(s *store.Store, dataDir string, productLocal bool) (*retrieve.Engine, error) {
	var (
		embedder retrieve.Embedder
		name     string
		err      error
	)
	if productLocal {
		embedder, name, err = managedEmbedderFromConfig()
	} else {
		embedder, name, err = embedderFromEnv()
	}
	if err != nil {
		return nil, err
	}
	if embedder == nil {
		if productLocal {
			return nil, errors.New("managed embedder is required in product-local mode")
		}
		slog.Info("retrieval: no embedder configured (set COHERE_API_KEY for Cohere, or ensure local mlx_embed_helper.py + bge-m3 model are present); /retrieve and /context/query will respond 503")
		return nil, nil
	}

	var expander retrieve.Expander
	if !productLocal {
		expander = expanderFromEnv(dataDir)
	}

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

const (
	managedEmbedderConfigSchemaV1   = "pulse.managed_embedder.config.v1"
	managedEmbedderConfigSchemaV2   = "pulse.managed_embedder.config.v2"
	managedEmbedderProtocol         = 1
	managedEmbedderMaximumArgs      = 8
	managedEmbedderMaximumArgBytes  = 4096
	managedEmbedderMaximumArgsBytes = 16 * 1024
)

type managedVectorContract struct {
	Dimensions   int    `json:"dimensions"`
	Model        string `json:"model"`
	Normalized   bool   `json:"normalized"`
	Opset        int    `json:"opset"`
	Pooling      string `json:"pooling"`
	Quantization string `json:"quantization"`
	Revision     string `json:"revision"`
	Source       string `json:"source"`
}

type managedEmbedderDiskConfig struct {
	EmbedderRuntimeActivationDigest string                `json:"embedder_runtime_activation_digest"`
	EmbedderRuntimeTreeDigest       string                `json:"embedder_runtime_tree_digest"`
	Engine                          string                `json:"engine"`
	ModelActivationDigest           string                `json:"model_activation_digest"`
	ModelRoot                       string                `json:"model_root"`
	ModelTreeDigest                 string                `json:"model_tree_digest"`
	Protocol                        int                   `json:"protocol"`
	RunnerArgs                      []string              `json:"runner_args"`
	RunnerPath                      string                `json:"runner_path"`
	Schema                          string                `json:"schema"`
	SupportRoot                     string                `json:"support_root"`
	VectorContract                  managedVectorContract `json:"vector_contract"`
}

func managedEmbedderFromConfig() (retrieve.Embedder, string, error) {
	config, err := loadManagedEmbedderConfig()
	if err != nil {
		return nil, "", err
	}
	client := embed.NewManagedLocal(config)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		_ = client.Close()
		return nil, "", fmt.Errorf("start managed embedder: %w", err)
	}
	return client, client.Model(), nil
}

func loadManagedEmbedderConfig() (embed.ManagedLocalConfig, error) {
	path := os.Getenv("PULSE_MANAGED_EMBEDDER_CONFIG")
	if path == "" || strings.TrimSpace(path) != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return embed.ManagedLocalConfig{}, errors.New("managed embedder config path is missing or invalid")
	}
	data, err := readOwnerOnlyRegularFile(path, 16*1024)
	if err != nil {
		return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder config open: %w", err)
	}
	var header struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder config decode: %w", err)
	}
	if header.Schema == managedEmbedderConfigSchemaV1 {
		return embed.ManagedLocalConfig{}, errors.New("managed embedder config v1 is historical and not ready for universal retrieval")
	}
	if header.Schema != managedEmbedderConfigSchemaV2 {
		return embed.ManagedLocalConfig{}, errors.New("managed embedder config contract mismatch")
	}
	var disk managedEmbedderDiskConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&disk); err != nil {
		return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder config decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return embed.ManagedLocalConfig{}, errors.New("managed embedder config decode: trailing JSON")
	}
	if disk.Schema != managedEmbedderConfigSchemaV2 || disk.Protocol != managedEmbedderProtocol ||
		disk.Engine != "transformers-js-onnx" ||
		disk.VectorContract.Model != "bge-m3" || disk.VectorContract.Source != "BAAI/bge-m3" ||
		disk.VectorContract.Revision != "5617a9f61b028005a4858fdac845db406aefb181" ||
		disk.VectorContract.Dimensions != 1024 || disk.VectorContract.Pooling != "cls" ||
		!disk.VectorContract.Normalized || disk.VectorContract.Opset != 17 ||
		disk.VectorContract.Quantization != "dynamic-int8" {
		return embed.ManagedLocalConfig{}, errors.New("managed embedder config contract mismatch")
	}
	for label, digest := range map[string]string{
		"embedder runtime activation": disk.EmbedderRuntimeActivationDigest,
		"embedder runtime tree":       disk.EmbedderRuntimeTreeDigest,
		"model activation":            disk.ModelActivationDigest,
		"model tree":                  disk.ModelTreeDigest,
	} {
		if !validManagedDigest(digest) {
			return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder %s digest is invalid", label)
		}
	}
	for label, value := range map[string]string{
		"runner": disk.RunnerPath, "model root": disk.ModelRoot, "support root": disk.SupportRoot,
	} {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsRune(value, '\x00') {
			return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder %s path is not absolute and clean", label)
		}
	}
	if len(disk.RunnerArgs) == 0 || len(disk.RunnerArgs) > managedEmbedderMaximumArgs {
		return embed.ManagedLocalConfig{}, errors.New("managed embedder runner args are outside the bounded contract")
	}
	totalArgBytes := 0
	for index, argument := range disk.RunnerArgs {
		totalArgBytes += len(argument)
		if argument == "" || len(argument) > managedEmbedderMaximumArgBytes || !utf8.ValidString(argument) ||
			strings.ContainsRune(argument, '\x00') || strings.ContainsAny(argument, "\r\n") || strings.Contains(argument, "://") {
			return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder runner arg %d is invalid", index)
		}
		if filepath.IsAbs(argument) && !managedPathInside(disk.ModelRoot, argument) &&
			!managedPathInside(disk.SupportRoot, argument) {
			return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder runner arg %d escapes verified roots", index)
		}
	}
	if totalArgBytes > managedEmbedderMaximumArgsBytes {
		return embed.ManagedLocalConfig{}, errors.New("managed embedder runner args exceed the bounded contract")
	}
	if !managedPathInside(disk.ModelRoot, disk.SupportRoot) || disk.ModelRoot == disk.SupportRoot {
		return embed.ManagedLocalConfig{}, errors.New("managed embedder support root must be inside the verified model root")
	}
	if err := validateManagedArtifactPath(disk.RunnerPath, true, false); err != nil {
		return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder runner: %w", err)
	}
	if err := validateManagedArtifactPath(disk.ModelRoot, false, true); err != nil {
		return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder model root: %w", err)
	}
	if err := validateManagedArtifactPath(disk.SupportRoot, false, true); err != nil {
		return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder support root: %w", err)
	}
	for _, name := range []string{"model_int8.onnx", "pulse-model-contract.json"} {
		if err := validateManagedArtifactPath(filepath.Join(disk.ModelRoot, name), false, false); err != nil {
			return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder model %s: %w", name, err)
		}
	}
	for _, name := range []string{"config.json", "tokenizer.json", "tokenizer_config.json", "special_tokens_map.json"} {
		if err := validateManagedArtifactPath(filepath.Join(disk.SupportRoot, name), false, false); err != nil {
			return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder support %s: %w", name, err)
		}
	}
	return embed.ManagedLocalConfig{
		Schema: disk.Schema, Protocol: disk.Protocol, Engine: disk.Engine,
		RunnerPath: disk.RunnerPath, RunnerArgs: append([]string(nil), disk.RunnerArgs...),
		ModelRoot: disk.ModelRoot, SupportRoot: disk.SupportRoot,
		VectorContract: embed.ManagedVectorContract{
			Model: disk.VectorContract.Model, Source: disk.VectorContract.Source,
			Revision: disk.VectorContract.Revision, Dimensions: disk.VectorContract.Dimensions,
			Pooling: disk.VectorContract.Pooling, Normalized: disk.VectorContract.Normalized,
			Opset: disk.VectorContract.Opset, Quantization: disk.VectorContract.Quantization,
		},
		EmbedderRuntimeActivationDigest: disk.EmbedderRuntimeActivationDigest,
		EmbedderRuntimeTreeDigest:       disk.EmbedderRuntimeTreeDigest,
		ModelActivationDigest:           disk.ModelActivationDigest, ModelTreeDigest: disk.ModelTreeDigest,
	}, nil
}

func managedPathInside(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func validManagedDigest(value string) bool {
	if len(value) != 64 || value == strings.Repeat("0", 64) {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func readOwnerOnlyRegularFile(path string, maximum int64) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || maximum < 1 {
		return nil, errors.New("invalid path")
	}
	data, err := platform.ReadPrivateFile(path, platform.FilePolicy{
		MaximumBytes: maximum, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	})
	if err != nil {
		return nil, fmt.Errorf("file must be owner-only: %w", err)
	}
	return data, nil
}

func validateManagedArtifactPath(path string, executable, directory bool) error {
	return platform.ValidatePrivatePath(path, platform.FilePolicy{
		RequireCurrentOwner: true, NoUntrustedWrite: true, Directory: directory, Executable: executable,
	})
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
