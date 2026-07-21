package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/claude"
	"github.com/nkkmnk/pulse/internal/consolidation"
	"github.com/nkkmnk/pulse/internal/contextquery"
	"github.com/nkkmnk/pulse/internal/health"
	"github.com/nkkmnk/pulse/internal/ingest"
	"github.com/nkkmnk/pulse/internal/outbox"
	"github.com/nkkmnk/pulse/internal/prompt"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/userpresence"
)

// ClaudeAPI is the subset of claude.Client we need. Allows fakes in tests.
type ClaudeAPI interface {
	Complete(ctx context.Context, req claude.CompleteRequest) (*claude.CompleteResponse, error)
}

type ContextQueryAPI interface {
	Query(context.Context, contextquery.ContextQueryRequest) (*contextquery.ContextResult, error)
}

// HomePresence is retained as an optional compatibility dependency for
// protected native-confirmation flows. Opening ordinary Memory Home never
// consumes it and never upgrades a browser session into enhanced proof.
type HomePresence interface {
	Authorize(context.Context, userpresence.Challenge) (userpresence.Assertion, error)
}

// Config holds the dependencies a server needs.
type Config struct {
	IPCSecret string
	// StartupNonce is an optional supervisor-provided process-instance proof.
	// When set it is returned only from the authenticated loopback health route.
	StartupNonce string
	Outbox       *outbox.Outbox
	Builder      *prompt.Builder
	Claude       ClaudeAPI
	DefaultModel string
	Store        *store.Store
	// Retrieval is the Phase G hybrid engine. Optional — when nil the
	// /retrieve endpoint is not registered.
	Retrieval *retrieve.Engine
	// ContextQuery projects Pulse graph memory into a typed context contract.
	// Optional — when nil the route returns 503.
	ContextQuery ContextQueryAPI
	// Health provides Apple Health snapshots for GET /health/snapshot.
	// Optional — when nil the route returns 503. In M0 a fixture
	// provider is wired in cmd/pulse/main.go.
	Health health.Provider
	// Billing describes the current Pulse distribution mode for /memory/status.
	Billing BillingStatus
	// TrayGracePeriod controls the visible private-write preview window for
	// Personal/Desk product stores. Zero selects the product default (10s).
	TrayGracePeriod time.Duration
	// HomeOrigin is the exact loopback origin for the credential-free Personal
	// or Desk Memory Home. Empty keeps the Home surface disabled (legacy Local
	// Preview and tests that do not configure a product browser surface).
	HomeOrigin string
	// HomePresence is optional. It may support separately routed protected
	// actions, but it is neither a constructor nor ordinary Home prerequisite.
	HomePresence HomePresence
	// EnhancedPresenceAuthorizer is the exact protected-action capability
	// exposed by Memory Home. Nil means unavailable; it never blocks ordinary
	// install, read, recall, save, or Home use.
	EnhancedPresenceAuthorizer userpresence.EnhancedPresenceAuthorizer
	// UnassignedInboxPath is the owner-only, non-retrievable queue shared by
	// installed harnesses before a user chooses an exact project. Empty hides
	// the queue without changing canonical memory behavior.
	UnassignedInboxPath string
	// HomeBindingVerifier re-reads the signed workspace authority before every
	// Home render or mutation so a stale daemon/session cannot survive revoke.
	HomeBindingVerifier HomeBindingVerifier
	// ConsolidationReports overrides the daemon-owned report lifecycle manager.
	// When nil, Personal and Desk stores with an exact product boundary receive
	// a private manager next to their vault automatically.
	ConsolidationReports *consolidation.Manager
	// ConsolidationInventory overrides the recognized-source read-only engine.
	// It is primarily a test seam; production derives fixed roots from the
	// current user home and exact bound vault path.
	ConsolidationInventory *consolidation.Engine
}

type BillingStatus struct {
	Mode              string `json:"billing_mode"`
	Host              string `json:"host"`
	BackendLLMEnabled bool   `json:"backend_llm_enabled"`
	RawCaptureEnabled bool   `json:"raw_capture_enabled"`
	StoragePath       string `json:"storage_path"`
}

// Server wraps the chi router.
type Server struct {
	cfg                    Config
	started                time.Time
	homeSessions           *viewerSessionManager
	homePresentation       *MemoryPresentationService
	homeProtectedWipeMu    sync.Mutex
	homeProtectedWipeItems map[string]homeProtectedWipePending
	trayScheduleMu         sync.Mutex
	traySchedules          map[memoryTrayScheduleKey]*memoryTrayScheduleState
	consolidationReports   *consolidation.Manager
	consolidationInventory *consolidation.Engine
}

func New(cfg Config) (*Server, error) {
	if cfg.IPCSecret == "" {
		return nil, errors.New("server: empty IPCSecret")
	}
	if cfg.Claude != nil && cfg.DefaultModel == "" {
		return nil, errors.New("server: Claude set but DefaultModel is empty")
	}
	if cfg.StartupNonce != "" && !validStartupNonce(cfg.StartupNonce) {
		return nil, errors.New("server: startup nonce must be 32 lowercase-hex bytes")
	}
	if cfg.TrayGracePeriod == 0 {
		cfg.TrayGracePeriod = 10 * time.Second
	}
	if cfg.TrayGracePeriod < time.Second || cfg.TrayGracePeriod > 30*time.Second {
		return nil, errors.New("server: TrayGracePeriod must be between 1s and 30s")
	}
	if cfg.EnhancedPresenceAuthorizer == nil {
		cfg.EnhancedPresenceAuthorizer = userpresence.NewUnavailableAuthorizer("enhanced_presence_unavailable")
	}
	server := &Server{
		cfg: cfg, started: time.Now(),
		homeProtectedWipeItems: make(map[string]homeProtectedWipePending),
		traySchedules:          make(map[memoryTrayScheduleKey]*memoryTrayScheduleState),
		consolidationReports:   cfg.ConsolidationReports,
		consolidationInventory: cfg.ConsolidationInventory,
	}
	if server.consolidationReports == nil && cfg.Store != nil &&
		(cfg.Store.StoreKind() == store.StoreKindPersonal || cfg.Store.StoreKind() == store.StoreKindDesk) {
		if _, _, ok := cfg.Store.ProductRuntimeBoundary(); ok {
			if !filepath.IsAbs(cfg.Store.DBPath()) {
				return nil, errors.New("server: product consolidation reports require an absolute vault path")
			}
			key := sha256.Sum256([]byte("pulse:consolidation-report:v1:" + cfg.IPCSecret))
			manager, err := consolidation.NewManager(consolidation.ManagerConfig{
				RootDir: filepath.Join(filepath.Dir(cfg.Store.DBPath()), "consolidation-reports"),
				Key:     key[:],
			})
			if err != nil {
				return nil, fmt.Errorf("server: open consolidation reports: %w", err)
			}
			server.consolidationReports = manager
			if server.consolidationInventory == nil {
				homeDir, homeErr := os.UserHomeDir()
				if homeErr != nil || !filepath.IsAbs(homeDir) {
					return nil, errors.New("server: consolidation inventory requires an absolute user home")
				}
				inventory, inventoryErr := consolidation.NewEngine(consolidation.EngineConfig{
					Manager: manager, HomeDir: homeDir, CanonicalPath: cfg.Store.DBPath(), CanonicalDB: cfg.Store.DB(),
				})
				if inventoryErr != nil {
					return nil, fmt.Errorf("server: open consolidation inventory: %w", inventoryErr)
				}
				server.consolidationInventory = inventory
			}
		}
	}
	if cfg.HomeOrigin != "" {
		parsedHomeOrigin, err := url.Parse(cfg.HomeOrigin)
		if err != nil || parsedHomeOrigin.Scheme != "http" || parsedHomeOrigin.Hostname() != "127.0.0.1" || parsedHomeOrigin.Port() == "" {
			return nil, errors.New("server: Memory Home requires an exact http://127.0.0.1 origin")
		}
		if cfg.Store == nil || (cfg.Store.StoreKind() != store.StoreKindPersonal && cfg.Store.StoreKind() != store.StoreKindDesk) {
			return nil, errors.New("server: Memory Home requires a Personal or Desk store")
		}
		if cfg.HomeBindingVerifier == nil {
			return nil, errors.New("server: Memory Home requires live product binding verification")
		}
		if _, _, ok := cfg.Store.ProductRuntimeBoundary(); !ok {
			return nil, errors.New("server: Memory Home requires an exact product runtime boundary")
		}
		if cfg.UnassignedInboxPath != "" && !filepath.IsAbs(cfg.UnassignedInboxPath) {
			return nil, errors.New("server: unassigned inbox path must be absolute")
		}
		homeSessions, err := newViewerSessionManager(viewerSessionConfig{
			ExpectedOrigin: cfg.HomeOrigin, AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute,
			MaxSessions: 8, MaxBodyBytes: 64 << 10,
		})
		if err != nil {
			return nil, err
		}
		homePresentation, err := NewMemoryPresentationService(MemoryPresentationServiceConfig{
			Store: cfg.Store, Schedule: server.schedulePresentedMemory,
			ExpectedOrigin: cfg.HomeOrigin, ExpectedPath: memoryPresentationScopedHomePath,
			GracePeriod: cfg.TrayGracePeriod, CapabilityTTL: 45 * time.Second,
		})
		if err != nil {
			return nil, err
		}
		server.homeSessions = homeSessions
		server.homePresentation = homePresentation
	}
	if err := server.recoverMemoryTray(); err != nil {
		return nil, err
	}
	return server, nil
}

// Handler returns the local-only root http.Handler with auth middleware.
// Team remote must use NewTeam(...).Handler(); the two route registries never
// compose so a local route cannot become reachable through team configuration.
func (s *Server) Handler() http.Handler {
	local := s.localHandler()
	if s.homeSessions == nil {
		return local
	}
	mux := http.NewServeMux()
	home := s.homeHandler()
	mux.Handle("/home", home)
	mux.Handle("/home/", home)
	mux.Handle("/", local)
	return mux
}

func (s *Server) localHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(corsMiddleware)
	r.Use(s.authMiddleware)
	r.Get("/assets/anime.min.js", s.handleAnimeAsset)
	r.Get("/health", s.handleHealth)
	// /health/snapshot is a sibling under /health — chi's tree routing
	// disambiguates against /health (exact match wins).
	r.Get("/health/snapshot", s.handleHealthSnapshot)
	r.Get("/outbox", s.handleOutboxList)
	r.Post("/outbox/ack", s.handleOutboxAck)
	r.Post("/msg", s.handleMsg)
	if s.cfg.Store != nil {
		if s.cfg.Store.StoreKind() == store.StoreKindLocalPreview {
			r.Method(http.MethodPost, "/ingest", ingest.NewHandler(s.cfg.Store))
		}
		r.Post("/memory/remember", s.handleMemoryRemember)
		r.Post("/memory/recall", s.handleMemoryRecall)
		r.Get("/memory/status", s.handleMemoryStatus)
		r.Get("/memory/export", s.handleMemoryExport)
		r.Post("/memory/import", s.handleMemoryImport)
		r.Post("/memory/delete", s.handleMemoryDelete)
		r.Delete("/memory/{id}", s.handleMemoryDelete)
		r.Post("/memory/wipe", s.handleMemoryWipe)
		r.Post("/memory/consolidate", s.handleMemoryConsolidate)
		r.Post("/graph/delta", s.handleGraphDelta)
		r.Post("/turn/finalize", s.handleTurnFinalize)
		r.Post("/turn/no-change", s.handleTurnNoChange)
		r.Get("/memory/tray", s.handleMemoryTrayList)
		r.Get("/memory/receipts/{id}", s.handleMemoryReceiptGet)
		r.Post("/memory/tray/{id}/edit", s.handleMemoryTrayEdit)
		r.Post("/memory/tray/{id}/cancel", s.handleMemoryTrayCancel)
		r.Post("/memory/tray/{id}/commit", s.handleMemoryTrayCommit)
		r.Post("/memory/{id}/correct", s.handleMemoryCorrect)
		r.Post("/graph/entity/hide", s.handleGraphEntityHide)
		r.Post("/graph/entity/restore", s.handleGraphEntityRestore)
		r.Get("/graph/export", s.handleGraphExport)
		r.Post("/continuity/resume", s.handleContinuityResume)
		r.Post("/continuity/checkpoint", s.handleContinuityCheckpoint)
		r.Post("/continuity/observe", s.handleContinuityObserve)
		if s.cfg.Store.StoreKind() == store.StoreKindPersonal || s.cfg.Store.StoreKind() == store.StoreKindDesk {
			if s.consolidationReports != nil {
				r.Post("/memory/consolidation/reports", s.handleConsolidationReportStart)
				r.Get("/memory/consolidation/reports/latest", s.handleConsolidationReportLatest)
				r.Get("/memory/consolidation/reports/{id}", s.handleConsolidationReportGet)
				r.Get("/memory/consolidation/reports/{id}/explain", s.handleConsolidationReportExplain)
				r.Post("/memory/consolidation/reports/{id}/cancel", s.handleConsolidationReportCancel)
				r.Post("/memory/consolidation/reports/{id}/resume", s.handleConsolidationReportResume)
			}
			r.Get("/memory/lifecycle-readiness", s.handleSupportedHostLifecycleReadiness)
			r.Post("/continuity/delivery/offers", s.handleContinuityDeliveryOffer)
			r.Post("/continuity/delivery/observations", s.handleContinuityDeliveryObservation)
			r.Post("/project/sources/register", s.handleProjectSourceRegister)
			r.Post("/project/sources/status", s.handleProjectSourceStatus)
			r.Post("/project/shared-memory/review/stage", s.handleGitTeamMemoryStage)
			r.Post("/project/shared-memory/review/inspect", s.handleGitTeamMemoryInspect)
			r.Post("/project/shared-memory/review/candidates/{id}/edit", s.handleGitTeamMemoryEdit)
			r.Post("/project/shared-memory/review/candidates/{id}/reject", s.handleGitTeamMemoryReject)
			r.Post("/project/shared-memory/review/present", s.handleGitTeamMemoryPresent)
			r.Post("/project/shared-memory/review/exact-ok", s.handleGitTeamMemoryExactOK)
			r.Post("/project/shared-memory/publications/start", s.handleGitTeamMemoryPublicationStart)
			r.Post("/project/shared-memory/publications/finalize", s.handleGitTeamMemoryPublicationFinalize)
			r.Post("/project/shared-memory/index", s.handleGitTeamMemoryIndex)
		}
		r.Get("/viewer", s.handleViewer)
		r.Get("/viewer/data", s.handleViewerData)
	}
	// /retrieve is always registered. handleRetrieve responds with 503 when
	// the engine is not configured (e.g. no Cohere API key) — better UX than
	// 404 since callers can tell intent vs absence.
	r.Post("/retrieve", s.handleRetrieve)
	r.Post("/context/query", s.handleContextQuery)
	if s.cfg.Store != nil {
		r.Get("/feed_signals", s.handleFeedSignals)
	}
	return r
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS preflight bypasses auth — corsMiddleware already answered.
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/assets/anime.min.js" {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("X-Pulse-Key")
		if got == "" && (r.URL.Path == "/viewer" || r.URL.Path == "/viewer/data") && isLoopbackRequest(r) {
			got = r.URL.Query().Get("key")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.IPCSecret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// corsMiddleware allows browser clients (Heart dev on :5173, MCP UIs, etc.)
// to call Pulse directly. Origins come from PULSE_CORS_ORIGINS (comma-
// separated) and default to localhost dev ports. Preflights are handled
// before auth so OPTIONS requests don't 401.
func corsMiddleware(next http.Handler) http.Handler {
	allowed := corsAllowedOrigins()
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		allowedSet[o] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := allowedSet[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Pulse-Key, Authorization")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Max-Age", "600")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func corsAllowedOrigins() []string {
	if env := strings.TrimSpace(os.Getenv("PULSE_CORS_ORIGINS")); env != "" {
		parts := strings.Split(env, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
		"http://localhost:5174",
		"http://127.0.0.1:5174",
	}
}

type healthResponse struct {
	Status        string  `json:"status"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	StartupNonce  string  `json:"startup_nonce,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{
		Status:        "ok",
		UptimeSeconds: time.Since(s.started).Seconds(),
		StartupNonce:  s.cfg.StartupNonce,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func validStartupNonce(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
