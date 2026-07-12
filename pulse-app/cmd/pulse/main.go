package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
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
)

const (
	defaultAddr  = "127.0.0.1:18789"
	defaultModel = "claude-opus-4-6"
	defaultAlias = "anthropic/opus"

	teamWriterLeaseTTL        = 60 * time.Second
	teamWriterRenewalInterval = 20 * time.Second
	teamHandlerQuiesceTimeout = 5 * time.Second
	teamDeletionLeaseTTL      = 30 * time.Second
	teamDeletionPollInterval  = time.Second
	teamDeletionBaseBackoff   = time.Second
	teamDeletionMaxBackoff    = 5 * time.Minute
)

var (
	errTeamWriterLeaseChanged        = errors.New("team writer lease changed during renewal")
	errTeamDeletionWorkerStopTimeout = errors.New("team deletion worker did not stop before timeout")
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
	case "team-remote":
		return runTeam(dataDir, addr)
	default:
		return errors.New("unsupported PULSE_RUNTIME_MODE")
	}
}

func runLocal(dataDir, addr string) error {
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
			// Real life and fiction (e.g. a book being written) must not mix by
			// default: context queries return real-domain memory unless the caller
			// explicitly asks for fiction (book work).
			DefaultDomains: []string{"real"},
		})
	}

	// Capsule → event projection backfill (default ON; PULSE_CAPSULE_EVENTS=off
	// disables). Idempotent: only capsules never projected (event_id IS NULL,
	// privacy_tier='normal') gain a linked event. With an embedder wired the
	// new events are embed-indexed immediately; otherwise they stay dark until
	// an embedder is configured. Env wiring only — no billing / network-path
	// change (the optional embed call uses the already-configured embedder).
	if docs, err := s.BackfillCapsuleEvents(); err != nil {
		slog.Warn("capsule event backfill failed", "error", err)
	} else if len(docs) > 0 {
		slog.Info("capsule events backfilled", "count", len(docs))
		if retrievalEngine != nil && retrievalEngine.EmbedderReady() {
			indexDocs := make([]retrieve.IndexEventDoc, len(docs))
			for i, d := range docs {
				indexDocs[i] = retrieve.IndexEventDoc{EventID: d.EventID, Text: d.Text}
			}
			backfillCtx, cancelBackfill := context.WithTimeout(context.Background(), 60*time.Second)
			if err := retrievalEngine.EmbedAndIndexEvents(backfillCtx, indexDocs); err != nil {
				slog.Warn("capsule event embed-index failed", "error", err)
			}
			cancelBackfill()
		}
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

	teamServer, err := server.NewTeam(server.TeamServerConfig{
		IPCSecret:         cfg.IPCSecret,
		Store:             s,
		PrincipalVerifier: verifier,
		// Team retrieval is a separate no-cache engine. The foundation starts
		// lexical-only so team mode never discovers or reuses a personal Cohere
		// key from ~/.pulse; deployments may inject an explicit team embedder in
		// a later activation unit without touching the local retrieval stack.
		ReadService: teamread.New(
			s, retrieve.NewTeamRetrievalEngine(retrieve.TeamRetrievalConfig{}),
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
	listener, err := listenTeamDaemon(addr)
	if err != nil {
		return fmt.Errorf("team-remote listen: %w", err)
	}
	defer listener.Close()

	httpSrv := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           composeTeamRuntimeHandler(teamServer.Handler(), ownerAdminServer.Handler()),
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
		Signals:           sigCh,
		RenewLease:        func(ctx context.Context) error { return renewTeamWriterLease(ctx, s, lease) },
		RunDeletionWorker: deletionWorker.Run,
		ShutdownTimeout:   10 * time.Second,
		QuiesceTimeout:    teamHandlerQuiesceTimeout,
	})
	releaseLease = runtimeResult.ReleaseLease
	return runtimeResult.Err
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

func composeTeamRuntimeHandler(teamHandler, ownerHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/team/v1/owner/", ownerHandler)
	mux.Handle("/", teamHandler)
	return mux
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

type teamRuntimeOptions struct {
	Signals           <-chan os.Signal
	RenewLease        func(context.Context) error
	RunDeletionWorker func(context.Context) error
	ShutdownTimeout   time.Duration
	QuiesceTimeout    time.Duration
}

type teamRuntimeResult struct {
	Err          error
	ReleaseLease bool
}

type teamRequestTracker struct {
	mu        sync.Mutex
	accepting bool
	active    int
	drained   chan struct{}
}

func newTeamRequestTracker() *teamRequestTracker {
	return &teamRequestTracker{accepting: true, drained: make(chan struct{})}
}

func (tracker *teamRequestTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracker.mu.Lock()
		if !tracker.accepting {
			tracker.mu.Unlock()
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		tracker.active++
		tracker.mu.Unlock()
		defer func() {
			tracker.mu.Lock()
			tracker.active--
			if !tracker.accepting && tracker.active == 0 {
				select {
				case <-tracker.drained:
				default:
					close(tracker.drained)
				}
			}
			tracker.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

func (tracker *teamRequestTracker) stop() {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.accepting = false
	if tracker.active == 0 {
		select {
		case <-tracker.drained:
		default:
			close(tracker.drained)
		}
	}
}

func (tracker *teamRequestTracker) wait(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-tracker.drained:
		return true
	case <-timer.C:
		return false
	}
}

func serveTeamRuntime(httpSrv *http.Server, listener net.Listener, options teamRuntimeOptions) teamRuntimeResult {
	tracker := newTeamRequestTracker()
	handler := httpSrv.Handler
	if handler == nil {
		handler = http.DefaultServeMux
	}
	httpSrv.Handler = tracker.wrap(handler)
	requestsCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	httpSrv.BaseContext = func(net.Listener) context.Context { return requestsCtx }

	leaseCtx, cancelLease := context.WithCancel(context.Background())
	leaseResult := make(chan error, 1)
	go func() { leaseResult <- options.RenewLease(leaseCtx) }()
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	runDeletionWorker := options.RunDeletionWorker
	if runDeletionWorker == nil {
		runDeletionWorker = func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		}
	}
	workerResult := make(chan error, 1)
	go func() { workerResult <- runDeletionWorker(workerCtx) }()
	serveResult := make(chan error, 1)
	go func() {
		err := httpSrv.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveResult <- err
	}()

	select {
	case leaseErr := <-leaseResult:
		tracker.stop()
		cancelRequests()
		_ = httpSrv.Close()
		<-serveResult
		_, _ = stopTeamDeletionWorker(workerCtx, cancelWorker, workerResult, options.QuiesceTimeout)
		cancelLease()
		tracker.wait(options.QuiesceTimeout)
		if leaseErr == nil {
			leaseErr = errors.New("team writer lease renewal stopped")
		}
		return teamRuntimeResult{Err: fmt.Errorf("team writer lease renewal: %w", leaseErr)}
	case workerErr := <-workerResult:
		tracker.stop()
		cancelRequests()
		_ = httpSrv.Close()
		<-serveResult
		cancelWorker()
		cancelLease()
		_ = normalizeStoppedLeaseError(leaseCtx, <-leaseResult)
		tracker.wait(options.QuiesceTimeout)
		if workerErr == nil {
			workerErr = errors.New("team deletion worker stopped")
		}
		return teamRuntimeResult{Err: fmt.Errorf("team deletion worker: %w", workerErr)}
	case serveErr := <-serveResult:
		tracker.stop()
		cancelRequests()
		_ = httpSrv.Close()
		workerErr, _ := stopTeamDeletionWorker(
			workerCtx, cancelWorker, workerResult, options.QuiesceTimeout,
		)
		cancelLease()
		leaseErr := normalizeStoppedLeaseError(leaseCtx, <-leaseResult)
		quiesced := tracker.wait(options.QuiesceTimeout)
		if workerErr != nil {
			return teamRuntimeResult{Err: fmt.Errorf("team deletion worker: %w", workerErr)}
		}
		if leaseErr != nil {
			return teamRuntimeResult{Err: fmt.Errorf("team writer lease renewal: %w", leaseErr)}
		}
		return teamRuntimeResult{Err: serveErr, ReleaseLease: quiesced}
	case <-options.Signals:
		tracker.stop()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), options.ShutdownTimeout)
		shutdownResult := make(chan error, 1)
		go func() { shutdownResult <- httpSrv.Shutdown(shutdownCtx) }()
		select {
		case leaseErr := <-leaseResult:
			cancelRequests()
			_ = httpSrv.Close()
			<-shutdownResult
			cancelShutdown()
			<-serveResult
			_, _ = stopTeamDeletionWorker(
				workerCtx, cancelWorker, workerResult, options.QuiesceTimeout,
			)
			cancelLease()
			tracker.wait(options.QuiesceTimeout)
			if leaseErr == nil {
				leaseErr = errors.New("team writer lease renewal stopped")
			}
			return teamRuntimeResult{Err: fmt.Errorf("team writer lease renewal: %w", leaseErr)}
		case workerErr := <-workerResult:
			cancelRequests()
			_ = httpSrv.Close()
			<-shutdownResult
			cancelShutdown()
			<-serveResult
			cancelWorker()
			cancelLease()
			_ = normalizeStoppedLeaseError(leaseCtx, <-leaseResult)
			tracker.wait(options.QuiesceTimeout)
			if workerErr == nil {
				workerErr = errors.New("team deletion worker stopped")
			}
			return teamRuntimeResult{Err: fmt.Errorf("team deletion worker: %w", workerErr)}
		case shutdownErr := <-shutdownResult:
			cancelShutdown()
			if shutdownErr != nil {
				cancelRequests()
				_ = httpSrv.Close()
			}
			<-serveResult
			workerErr, _ := stopTeamDeletionWorker(
				workerCtx, cancelWorker, workerResult, options.QuiesceTimeout,
			)
			cancelLease()
			leaseErr := normalizeStoppedLeaseError(leaseCtx, <-leaseResult)
			quiesced := tracker.wait(options.QuiesceTimeout)
			if workerErr != nil {
				return teamRuntimeResult{Err: fmt.Errorf("team deletion worker: %w", workerErr)}
			}
			if leaseErr != nil {
				return teamRuntimeResult{Err: fmt.Errorf("team writer lease renewal: %w", leaseErr)}
			}
			return teamRuntimeResult{Err: shutdownErr, ReleaseLease: quiesced}
		}
	}
}

func normalizeStoppedLeaseError(ctx context.Context, err error) error {
	if err == nil || (ctx.Err() != nil && errors.Is(err, context.Canceled)) {
		return nil
	}
	return err
}

func stopTeamDeletionWorker(
	ctx context.Context,
	cancel context.CancelFunc,
	result <-chan error,
	timeout time.Duration,
) (error, bool) {
	cancel()
	if timeout <= 0 {
		return errTeamDeletionWorkerStopTimeout, false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-result:
		return normalizeStoppedLeaseError(ctx, err), true
	case <-timer.C:
		return errTeamDeletionWorkerStopTimeout, false
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
