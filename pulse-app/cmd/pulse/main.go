package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/nkkmnk/pulse/internal/teamauth"
	"github.com/nkkmnk/pulse/internal/teamjobs"
	"github.com/nkkmnk/pulse/internal/teamread"
	"github.com/nkkmnk/pulse/internal/userpresence"
)

const (
	defaultAddr  = "127.0.0.1:18789"
	defaultModel = "claude-opus-4-6"
	defaultAlias = "anthropic/opus"

	teamWriterLeaseTTL              = 60 * time.Second
	teamWriterRenewalInterval       = 20 * time.Second
	teamHandlerQuiesceTimeout       = 5 * time.Second
	teamBackgroundPeerJoinTimeout   = 250 * time.Millisecond
	teamDeletionLeaseTTL            = 30 * time.Second
	teamDeletionPollInterval        = time.Second
	teamDeletionBaseBackoff         = time.Second
	teamDeletionMaxBackoff          = 5 * time.Minute
	teamProjectionLeaseTTL          = 30 * time.Second
	teamProjectionPollInterval      = time.Second
	teamProjectionHeartbeatInterval = 10 * time.Second
	teamProjectionBaseBackoff       = time.Second
	teamProjectionMaxBackoff        = 5 * time.Minute
)

var (
	errTeamWriterLeaseChanged          = errors.New("team writer lease changed during renewal")
	errTeamBackgroundWorkerStopTimeout = errors.New("team background workers did not stop before timeout")
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
	switch os.Getenv("PULSE_RUNTIME_MODE") {
	case "", "local-stdio", "development-http":
		return runLocal(dataDir, addr)
	case "personal-local":
		return runProductLocal(dataDir, addr, config.VaultPersonal)
	case "desk-local":
		return runProductLocal(dataDir, addr, config.VaultDesk)
	case "team-remote":
		return runTeam(dataDir, addr)
	default:
		return errors.New("unsupported PULSE_RUNTIME_MODE")
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
		homeBindingVerifier, err = server.NewCommandHomeBindingVerifier(
			os.Getenv("PULSE_PRODUCT_AUTHORITY_NODE"), os.Getenv("PULSE_PRODUCT_AUTHORITY_HELPER"),
			os.Getenv("PULSE_PRODUCT_WORKSPACE"), resolverEpoch,
		)
		if err != nil {
			return fmt.Errorf("configure live product binding verifier: %w", err)
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
			DB:        s.DB(),
			Retrieval: retrievalEngine,
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
	}
	srv, err := server.New(server.Config{
		IPCSecret:    cfg.IPCSecret,
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
		Retrieval:           retrievalEngine,
		ContextQuery:        contextQuery,
		Health:              healthProvider,
		HomeOrigin:          homeOrigin,
		HomePresence:        homePresence,
		UnassignedInboxPath: unassignedInboxPath,
		HomeBindingVerifier: homeBindingVerifier,
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
	reaperWG.Add(1)
	go func() {
		defer reaperWG.Done()
		backfillCapsuleEvents(reaperCtx, s, retrievalEngine)
	}()

	slog.Info("pulse listening", "addr", addr, "data_dir", dataDir,
		"vault_kind", s.StoreKind(), "store_id", s.StoreID())

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

func runTeam(dataDir, addr string) error {
	if !isLoopbackListenAddress(addr) {
		return errors.New("team-remote daemon must bind to a loopback address")
	}
	cfg, err := config.LoadTeam(dataDir)
	if err != nil {
		return err
	}
	ownerAdminOnly := os.Getenv("PULSE_TEAM_OWNER_ADMIN_ONLY")
	if ownerAdminOnly != "" && ownerAdminOnly != "1" {
		return errors.New("PULSE_TEAM_OWNER_ADMIN_ONLY must be unset or 1")
	}
	if ownerAdminOnly == "1" {
		return runTeamOwnerAdmin(cfg, addr)
	}
	if cfg.TeamBootstrapRoot == nil || cfg.ExpectedTeamStoreID == "" || cfg.ExpectedTeamID == "" {
		return errors.New("team-remote requires pinned bootstrap root and expected store identity")
	}

	s, err := store.OpenTeam(cfg.DBPath, store.TeamOpenOptions{ExpectedBootstrapRoot: *cfg.TeamBootstrapRoot})
	if err != nil {
		return err
	}
	defer s.Close()
	if _, err := s.CheckSyntheticTeamReadiness(context.Background(), store.TeamReadinessOptions{
		ExpectedStoreID: cfg.ExpectedTeamStoreID,
		ExpectedTeamID:  cfg.ExpectedTeamID,
		ReaderVersion:   teamauth.SchemaVersion,
		WriterVersion:   teamauth.SchemaVersion,
	}); err != nil {
		return fmt.Errorf("team-remote store readiness: %w", err)
	}

	keyring, err := server.LoadPrincipalVerifyKeyringFromEnv()
	if err != nil {
		return err
	}
	writerID, err := newTeamWriterID()
	if err != nil {
		return errors.New("team-remote writer identity unavailable")
	}
	lease, err := s.AcquireTeamWriterLease(context.Background(), store.TeamWriterLeaseRequest{
		WriterID: writerID, WriterVersion: teamauth.SchemaVersion, TTL: teamWriterLeaseTTL,
	})
	if err != nil {
		return fmt.Errorf("team-remote writer lease: %w", err)
	}
	releaseLease := true
	defer func() {
		if !releaseLease {
			slog.Warn("team writer lease left to expire because request quiescence or ownership was not proven")
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.ReleaseTeamWriterLease(releaseCtx, lease.WriterID, lease.Token); err != nil {
			slog.Warn("team writer lease release failed")
		}
	}()
	verifier, err := server.NewPrincipalVerifier(server.PrincipalVerifierConfig{
		Store:           s,
		Keyring:         keyring,
		ExpectedStoreID: cfg.ExpectedTeamStoreID,
		ExpectedTeamID:  cfg.ExpectedTeamID,
		WriterLease:     &lease,
	})
	if err != nil {
		return err
	}
	ownerStepUpVerifier, err := server.NewOwnerStepUpVerifier(server.OwnerStepUpVerifierConfig{
		Store: s, Keyring: keyring, ExpectedRoot: *cfg.TeamBootstrapRoot,
	})
	if err != nil {
		return err
	}
	ownerAdminServer, err := server.NewOwnerAdminServer(server.OwnerAdminServerConfig{
		IPCSecret: cfg.IPCSecret, Store: s, StepUpVerifier: ownerStepUpVerifier,
		WriterLease: &lease,
	})
	if err != nil {
		return err
	}
	publicationAirlock, err := newSyntheticTeamPublicationAirlock(
		s, cfg, lease, ownerStepUpVerifier,
	)
	if err != nil {
		return err
	}
	teamEmbedder, _, err := teamEmbedderFromEnv()
	if err != nil {
		return fmt.Errorf("team embedding dependency: %w", err)
	}
	if closer, ok := teamEmbedder.(io.Closer); ok {
		defer func() {
			if err := closer.Close(); err != nil {
				slog.Warn("team embedding dependency close failed")
			}
		}()
	}

	teamServer, err := server.NewTeam(server.TeamServerConfig{
		IPCSecret:         cfg.IPCSecret,
		Store:             s,
		PrincipalVerifier: verifier,
		ReadService: teamread.New(
			s, retrieve.NewTeamRetrievalEngine(retrieve.TeamRetrievalConfig{Embedder: teamEmbedder}),
		),
		ExpectedStoreID: cfg.ExpectedTeamStoreID,
		ExpectedTeamID:  cfg.ExpectedTeamID,
		WriterLease:     lease,
	})
	if err != nil {
		return err
	}
	deletionWorker, err := teamjobs.NewDeletionWorker(teamjobs.DeletionWorkerConfig{
		Store: s,
		Writer: store.TeamWriterLeaseIdentity{
			WriterID: lease.WriterID, Token: lease.Token,
		},
		ClaimLimit: 16, ReapLimit: 64, LeaseTTL: teamDeletionLeaseTTL,
		PollInterval: teamDeletionPollInterval,
		BaseBackoff:  teamDeletionBaseBackoff, MaxBackoff: teamDeletionMaxBackoff,
	})
	if err != nil {
		return err
	}
	embeddingProcessor, err := newTeamEmbeddingProjectionProcessor(s, teamEmbedder)
	if err != nil {
		return err
	}
	projectionProcessor, err := teamjobs.NewStoreProjectionProcessor(s, embeddingProcessor)
	if err != nil {
		return err
	}
	projectionWorker, err := teamjobs.NewProjectionWorker(teamjobs.ProjectionWorkerConfig{
		Store: s, Processor: projectionProcessor,
		Writer: store.TeamWriterLeaseIdentity{
			WriterID: lease.WriterID, Token: lease.Token,
		},
		ProjectionKind: "", ClaimLimit: 1, LeaseTTL: teamProjectionLeaseTTL,
		PollInterval:      teamProjectionPollInterval,
		HeartbeatInterval: teamProjectionHeartbeatInterval,
		WorkerInstanceID:  lease.WriterID + "-projection",
		BaseBackoff:       teamProjectionBaseBackoff, MaxBackoff: teamProjectionMaxBackoff,
	})
	if err != nil {
		return err
	}
	listener, err := listenTeamDaemon(addr)
	if err != nil {
		return fmt.Errorf("team-remote listen: %w", err)
	}
	defer listener.Close()

	httpSrv := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           composeTeamRuntimeHandler(teamServer.Handler(), ownerAdminServer.Handler(), publicationAirlock),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	slog.Info("pulse team daemon listening", "addr", listener.Addr().String())
	runtimeResult := serveTeamRuntime(httpSrv, listener, teamRuntimeOptions{
		Signals:    sigCh,
		RenewLease: func(ctx context.Context) error { return renewTeamWriterLease(ctx, s, lease) },
		RunBackgroundWorkers: func(ctx context.Context) error {
			return runTeamBackgroundWorkers(ctx, teamBackgroundPeerJoinTimeout,
				teamRuntimeWorker{Name: "deletion", Run: deletionWorker.Run},
				teamRuntimeWorker{Name: "projection", Run: projectionWorker.Run},
			)
		},
		ShutdownTimeout: 10 * time.Second,
		QuiesceTimeout:  teamHandlerQuiesceTimeout,
	})
	releaseLease = runtimeResult.ReleaseLease
	return runtimeResult.Err
}

func newTeamEmbeddingProjectionProcessor(
	projectionStore teamjobs.TeamEmbeddingProjectionStore,
	embedder retrieve.Embedder,
) (*teamjobs.TeamEmbeddingProjectionProcessor, error) {
	if embedder == nil {
		return nil, nil
	}
	return teamjobs.NewTeamEmbeddingProjectionProcessor(projectionStore, embedder)
}

func runTeamOwnerAdmin(cfg *config.Config, addr string) error {
	if cfg == nil || cfg.TeamBootstrapRoot == nil {
		return errors.New("team owner administration requires a pinned bootstrap root")
	}
	s, err := store.OpenTeam(cfg.DBPath, store.TeamOpenOptions{ExpectedBootstrapRoot: *cfg.TeamBootstrapRoot})
	if err != nil {
		return err
	}
	defer s.Close()
	keyring, err := server.LoadPrincipalVerifyKeyringFromEnv()
	if err != nil {
		return err
	}
	stepUpVerifier, err := server.NewOwnerStepUpVerifier(server.OwnerStepUpVerifierConfig{
		Store: s, Keyring: keyring, ExpectedRoot: *cfg.TeamBootstrapRoot,
	})
	if err != nil {
		return err
	}
	identity, err := s.ResolveOwnerStepUpIdentity(context.Background(), *cfg.TeamBootstrapRoot)
	if err != nil {
		return err
	}
	var lease *store.TeamWriterLease
	releaseLease := true
	if !identity.Bootstrap {
		if cfg.ExpectedTeamStoreID == "" || cfg.ExpectedTeamID == "" ||
			cfg.ExpectedTeamStoreID != identity.StoreID || cfg.ExpectedTeamID != identity.TeamID {
			return errors.New("post-bootstrap owner administration requires exact expected store identity")
		}
		if _, err := s.CheckTeamReadiness(context.Background(), store.TeamReadinessOptions{
			ExpectedStoreID: cfg.ExpectedTeamStoreID, ExpectedTeamID: cfg.ExpectedTeamID,
			ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion,
		}); err != nil {
			return fmt.Errorf("team owner store readiness: %w", err)
		}
		writerID, err := newTeamWriterID()
		if err != nil {
			return errors.New("team owner writer identity unavailable")
		}
		acquired, err := s.AcquireTeamWriterLease(context.Background(), store.TeamWriterLeaseRequest{
			WriterID: writerID, WriterVersion: teamauth.SchemaVersion, TTL: teamWriterLeaseTTL,
		})
		if err != nil {
			return fmt.Errorf("team owner writer lease: %w", err)
		}
		lease = &acquired
		defer func() {
			if !releaseLease {
				slog.Warn("team owner writer lease left to expire because request quiescence or ownership was not proven")
				return
			}
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.ReleaseTeamWriterLease(releaseCtx, acquired.WriterID, acquired.Token); err != nil {
				slog.Warn("team owner writer lease release failed")
			}
		}()
	}
	ownerServer, err := server.NewOwnerAdminServer(server.OwnerAdminServerConfig{
		IPCSecret: cfg.IPCSecret, Store: s, StepUpVerifier: stepUpVerifier, WriterLease: lease,
	})
	if err != nil {
		return err
	}
	listener, err := listenTeamDaemon(addr)
	if err != nil {
		return fmt.Errorf("team owner listen: %w", err)
	}
	defer listener.Close()
	httpSrv := &http.Server{
		Addr: listener.Addr().String(), Handler: ownerServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	renewLease := func(ctx context.Context) error {
		if lease == nil {
			<-ctx.Done()
			return nil
		}
		return renewTeamWriterLease(ctx, s, *lease)
	}
	runtimeResult := serveTeamRuntime(httpSrv, listener, teamRuntimeOptions{
		Signals: sigCh, RenewLease: renewLease,
		ShutdownTimeout: 10 * time.Second, QuiesceTimeout: teamHandlerQuiesceTimeout,
	})
	if lease != nil {
		releaseLease = runtimeResult.ReleaseLease
	}
	return runtimeResult.Err
}

func composeTeamRuntimeHandler(teamHandler, ownerHandler http.Handler, airlock ...http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/team/v1/owner/", ownerHandler)
	if len(airlock) == 1 && airlock[0] != nil {
		mux.Handle(server.TeamPublicationAirlockRoutePath, airlock[0])
	}
	mux.Handle("/", teamHandler)
	return mux
}

func newSyntheticTeamPublicationAirlock(
	teamStore *store.Store,
	cfg *config.Config,
	lease store.TeamWriterLease,
	stepUpVerifier *server.OwnerStepUpVerifier,
) (http.Handler, error) {
	deploymentID := os.Getenv("PULSE_TEAM_PUBLICATION_DEPLOYMENT_ID")
	projectID := os.Getenv("PULSE_TEAM_SHARED_PROJECT_ID")
	origin := os.Getenv("PULSE_TEAM_AIRLOCK_ORIGIN")
	candidatePath := os.Getenv("PULSE_TEAM_AIRLOCK_SYNTHETIC_CANDIDATE_FILE")
	syntheticOnly := os.Getenv("PULSE_TEAM_PUBLICATION_SYNTHETIC_ONLY")
	values := []string{deploymentID, projectID, origin, candidatePath, syntheticOnly}
	configured := 0
	for _, value := range values {
		if value != "" {
			configured++
		}
		if strings.TrimSpace(value) != value {
			return nil, errors.New("team publication Airlock configuration contains surrounding whitespace")
		}
	}
	if configured == 0 {
		return nil, nil
	}
	if configured != len(values) || syntheticOnly != "1" || teamStore == nil || cfg == nil ||
		stepUpVerifier == nil || cfg.ExpectedTeamStoreID == "" || cfg.ExpectedTeamID == "" {
		return nil, errors.New("team publication Airlock requires a complete synthetic-only configuration")
	}
	if err := teamStore.ConfigureTeamPublicationTarget(store.TeamPublicationTarget{
		DeploymentID: deploymentID, ProjectID: projectID, SyntheticOnly: true,
	}); err != nil {
		return nil, fmt.Errorf("team publication target: %w", err)
	}
	canonical, err := readTeamCredentialFile(candidatePath, 64<<10)
	if err != nil {
		return nil, errors.New("team publication Airlock candidate is unavailable or unsafe")
	}
	candidate, err := server.ParseTeamPublicationAirlockCandidate([]byte(canonical))
	if err != nil || candidate.StoreID != cfg.ExpectedTeamStoreID || candidate.TeamID != cfg.ExpectedTeamID {
		return nil, errors.New("team publication Airlock candidate does not match the pinned Team")
	}
	owner, err := teamStore.ResolveOwnerStepUpIdentity(context.Background(), *cfg.TeamBootstrapRoot)
	if err != nil || owner.Bootstrap || owner.StoreID != cfg.ExpectedTeamStoreID ||
		owner.TeamID != cfg.ExpectedTeamID || owner.OwnerPrincipalID == "" || owner.ClientKey == "" {
		return nil, errors.New("team publication Airlock Owner identity is unavailable")
	}
	airlockServer, err := server.NewTeamPublicationAirlockServer(server.TeamPublicationAirlockServerConfig{
		Store:                     teamStore,
		ExpectedOrigin:            origin,
		Candidate:                 candidate,
		ApprovingOwnerPrincipalID: owner.OwnerPrincipalID,
		ApprovingClientKey:        owner.ClientKey,
		StepUpVerifier:            stepUpVerifier,
		WriterLeaseProvider: server.TeamPublicationWriterLeaseProviderFunc(
			func(context.Context) (store.TeamWriterLeaseIdentity, error) {
				return store.TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}, nil
			},
		),
	})
	if err != nil {
		return nil, err
	}
	return airlockServer.Handler(), nil
}

type teamWriterLeaseRenewer interface {
	AcquireTeamWriterLease(context.Context, store.TeamWriterLeaseRequest) (store.TeamWriterLease, error)
}

func renewTeamWriterLease(ctx context.Context, renewer teamWriterLeaseRenewer, lease store.TeamWriterLease) error {
	return renewTeamWriterLeaseAtInterval(ctx, renewer, lease, teamWriterRenewalInterval)
}

func renewTeamWriterLeaseAtInterval(ctx context.Context, renewer teamWriterLeaseRenewer, lease store.TeamWriterLease, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			renewed, err := renewer.AcquireTeamWriterLease(ctx, store.TeamWriterLeaseRequest{
				WriterID: lease.WriterID, WriterVersion: teamauth.SchemaVersion,
				Token: lease.Token, TTL: teamWriterLeaseTTL,
			})
			if err != nil {
				return err
			}
			if renewed.StoreID != lease.StoreID || renewed.TeamID != lease.TeamID ||
				renewed.WriterID != lease.WriterID || renewed.WriterVersion != lease.WriterVersion ||
				renewed.Token != lease.Token {
				return errTeamWriterLeaseChanged
			}
		}
	}
}

func newTeamWriterID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "team-daemon-" + hex.EncodeToString(value[:]), nil
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

func listenTeamDaemon(addr string) (net.Listener, error) {
	if !isLoopbackListenAddress(addr) {
		return nil, errors.New("team-remote daemon must bind to a loopback address")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddr.IP == nil || !tcpAddr.IP.IsLoopback() {
		_ = listener.Close()
		return nil, errors.New("team-remote daemon resolved to a non-loopback address")
	}
	return listener, nil
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

// teamEmbedderFromEnv deliberately ignores the Personal/Local Preview
// COHERE_API_KEY and ~/.pulse/cohere-key.txt discovery paths. A Commons
// deployment must receive a separately provisioned credential file or a
// separately named local helper configuration; otherwise Team readiness stays
// degraded instead of silently sending shared content through a personal
// dependency.
func teamEmbedderFromEnv() (retrieve.Embedder, string, error) {
	keyPath := os.Getenv("PULSE_TEAM_COHERE_KEY_FILE")
	if strings.TrimSpace(keyPath) != keyPath {
		return nil, "", errors.New("PULSE_TEAM_COHERE_KEY_FILE must not contain surrounding whitespace")
	}
	if keyPath != "" {
		key, err := readTeamCredentialFile(keyPath, 4096)
		if err != nil {
			return nil, "", errors.New("team Cohere credential file is unavailable or unsafe")
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, "\r\n\x00") {
			return nil, "", errors.New("team Cohere credential file is invalid")
		}
		client := embed.NewCohere(key, "", "")
		return client, client.Model(), nil
	}

	pythonExe := os.Getenv("PULSE_TEAM_LOCAL_EMBED_PYTHON")
	helperPath := os.Getenv("PULSE_TEAM_LOCAL_EMBED_HELPER")
	modelPath := os.Getenv("PULSE_TEAM_LOCAL_EMBED_MODEL")
	configured := 0
	for _, value := range []string{pythonExe, helperPath, modelPath} {
		if value != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil, "", nil
	}
	if configured != 3 {
		return nil, "", errors.New("team local embedding dependency must set python, helper, and model together")
	}
	if err := validateTeamEmbeddingFile(pythonExe, true); err != nil {
		return nil, "", errors.New("team local embedding python is unavailable or unsafe")
	}
	if err := validateTeamEmbeddingFile(helperPath, false); err != nil {
		return nil, "", errors.New("team local embedding helper is unavailable or unsafe")
	}
	if strings.TrimSpace(modelPath) != modelPath || !filepath.IsAbs(modelPath) {
		return nil, "", errors.New("team local embedding model path is invalid")
	}
	modelInfo, err := os.Stat(modelPath)
	if err != nil || (!modelInfo.Mode().IsRegular() && !modelInfo.IsDir()) {
		return nil, "", errors.New("team local embedding model is unavailable")
	}
	client := embed.NewLocal(pythonExe, helperPath, modelPath, "bge-m3-mlx-fp16")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		return nil, "", errors.New("team local embedding dependency failed to start")
	}
	return client, client.Model(), nil
}

func validateTeamEmbeddingFile(path string, executable bool) error {
	if strings.TrimSpace(path) != path || !filepath.IsAbs(path) {
		return errors.New("invalid path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if executable && info.Mode().Perm()&0111 == 0 {
		return errors.New("not executable")
	}
	return nil
}

func readTeamCredentialFile(path string, maximum int64) (string, error) {
	if path == "" || !filepath.IsAbs(path) || maximum < 1 {
		return "", errors.New("invalid team credential path")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("unsafe team credential file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) {
		return "", errors.New("team credential file has an unexpected owner")
	}
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(value)) > maximum {
		return "", errors.New("team credential file is too large")
	}
	return string(value), nil
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

const managedEmbedderConfigSchema = "pulse.managed_embedder.config.v1"

type managedEmbedderDiskConfig struct {
	Dimensions                      int    `json:"dimensions"`
	EmbedderRuntimeActivationDigest string `json:"embedder_runtime_activation_digest"`
	EmbedderRuntimeTreeDigest       string `json:"embedder_runtime_tree_digest"`
	HelperPath                      string `json:"helper_path"`
	Model                           string `json:"model"`
	ModelActivationDigest           string `json:"model_activation_digest"`
	ModelFile                       string `json:"model_file"`
	ModelTreeDigest                 string `json:"model_tree_digest"`
	Normalized                      bool   `json:"normalized"`
	Pooling                         string `json:"pooling"`
	Protocol                        int    `json:"protocol"`
	PythonExecutable                string `json:"python_executable"`
	Schema                          string `json:"schema"`
	SupportDirectory                string `json:"support_directory"`
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
	if disk.Schema != managedEmbedderConfigSchema || disk.Model != "bge-m3" || disk.Dimensions != 1024 ||
		disk.Pooling != "cls" || !disk.Normalized || disk.Protocol != 1 {
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
		"python": disk.PythonExecutable, "helper": disk.HelperPath, "model": disk.ModelFile, "support": disk.SupportDirectory,
	} {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsRune(value, '\x00') {
			return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder %s path is not absolute and clean", label)
		}
	}
	if filepath.Ext(disk.ModelFile) != ".safetensors" {
		return embed.ManagedLocalConfig{}, errors.New("managed embedder model must be a safetensors file")
	}
	if err := validateManagedArtifactPath(disk.PythonExecutable, true, false); err != nil {
		return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder python: %w", err)
	}
	if err := validateManagedArtifactPath(disk.HelperPath, false, false); err != nil {
		return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder helper: %w", err)
	}
	if err := validateManagedArtifactPath(disk.ModelFile, false, false); err != nil {
		return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder model: %w", err)
	}
	if err := validateManagedArtifactPath(disk.SupportDirectory, false, true); err != nil {
		return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder support directory: %w", err)
	}
	for _, name := range []string{"config.json", "tokenizer.json"} {
		if err := validateManagedArtifactPath(filepath.Join(disk.SupportDirectory, name), false, false); err != nil {
			return embed.ManagedLocalConfig{}, fmt.Errorf("managed embedder support %s: %w", name, err)
		}
	}
	return embed.ManagedLocalConfig{
		PythonExecutable: disk.PythonExecutable,
		HelperPath:       disk.HelperPath,
		ModelFile:        disk.ModelFile,
		SupportDirectory: disk.SupportDirectory,
	}, nil
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
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("file has an unexpected owner")
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("file must be owner-only")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, errors.New("file is too large")
	}
	return data, nil
}

func validateManagedArtifactPath(path string, executable, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("symbolic links are forbidden")
	}
	if directory {
		if !info.IsDir() {
			return errors.New("not a directory")
		}
	} else if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("unexpected owner")
	}
	if info.Mode().Perm()&0022 != 0 {
		return errors.New("group/other writable path")
	}
	if executable && info.Mode().Perm()&0111 == 0 {
		return errors.New("not executable")
	}
	return nil
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
