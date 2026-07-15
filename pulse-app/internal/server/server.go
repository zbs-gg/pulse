package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/claude"
	"github.com/nkkmnk/pulse/internal/contextquery"
	"github.com/nkkmnk/pulse/internal/health"
	"github.com/nkkmnk/pulse/internal/ingest"
	"github.com/nkkmnk/pulse/internal/outbox"
	"github.com/nkkmnk/pulse/internal/prompt"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

// ClaudeAPI is the subset of claude.Client we need. Allows fakes in tests.
type ClaudeAPI interface {
	Complete(ctx context.Context, req claude.CompleteRequest) (*claude.CompleteResponse, error)
}

type ContextQueryAPI interface {
	Query(context.Context, contextquery.ContextQueryRequest) (*contextquery.ContextResult, error)
}

// Config holds the dependencies a server needs.
type Config struct {
	IPCSecret    string
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
	cfg            Config
	started        time.Time
	trayScheduleMu sync.Mutex
	traySchedules  map[memoryTrayScheduleKey]*memoryTrayScheduleState
}

func New(cfg Config) (*Server, error) {
	if cfg.IPCSecret == "" {
		return nil, errors.New("server: empty IPCSecret")
	}
	if cfg.Claude != nil && cfg.DefaultModel == "" {
		return nil, errors.New("server: Claude set but DefaultModel is empty")
	}
	if cfg.TrayGracePeriod == 0 {
		cfg.TrayGracePeriod = 10 * time.Second
	}
	if cfg.TrayGracePeriod < time.Second || cfg.TrayGracePeriod > 30*time.Second {
		return nil, errors.New("server: TrayGracePeriod must be between 1s and 30s")
	}
	server := &Server{
		cfg: cfg, started: time.Now(),
		traySchedules: make(map[memoryTrayScheduleKey]*memoryTrayScheduleState),
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
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{
		Status:        "ok",
		UptimeSeconds: time.Since(s.started).Seconds(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
