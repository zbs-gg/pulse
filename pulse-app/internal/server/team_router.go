package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const teamReadinessRoutePath = "/ready"

// TeamServerConfig is deliberately separate from Config. A team runtime
// cannot accidentally inherit the local route registry or any local writer.
type TeamServerConfig struct {
	IPCSecret         string
	Store             *store.Store
	PrincipalVerifier *PrincipalVerifier
	ExpectedStoreID   string
	ExpectedTeamID    string
	WriterLease       store.TeamWriterLease
}

// TeamServer owns the fail-closed internal daemon surface for team-remote.
// Its Handler never composes or delegates to the local Handler.
type TeamServer struct {
	cfg                  TeamServerConfig
	securityEventHandler http.Handler
}

func NewTeam(cfg TeamServerConfig) (*TeamServer, error) {
	if !validTeamIPCSecret(cfg.IPCSecret) {
		return nil, errors.New("team server: invalid IPC secret")
	}
	if cfg.Store == nil || cfg.PrincipalVerifier == nil || cfg.ExpectedStoreID == "" || cfg.ExpectedTeamID == "" {
		return nil, errors.New("team server: incomplete store identity")
	}
	if cfg.PrincipalVerifier.store != cfg.Store ||
		cfg.PrincipalVerifier.expectedStoreID != cfg.ExpectedStoreID ||
		cfg.PrincipalVerifier.expectedTeamID != cfg.ExpectedTeamID {
		return nil, errors.New("team server: principal verifier identity mismatch")
	}
	if cfg.WriterLease.StoreID != cfg.ExpectedStoreID || cfg.WriterLease.TeamID != cfg.ExpectedTeamID ||
		cfg.WriterLease.WriterID == "" || cfg.WriterLease.Token == "" ||
		cfg.WriterLease.WriterVersion < teamauth.SchemaVersion {
		return nil, errors.New("team server: invalid writer lease")
	}
	boundLease := cfg.PrincipalVerifier.writerLease
	if boundLease == nil || boundLease.StoreID != cfg.WriterLease.StoreID || boundLease.TeamID != cfg.WriterLease.TeamID ||
		boundLease.WriterID != cfg.WriterLease.WriterID ||
		subtle.ConstantTimeCompare([]byte(boundLease.Token), []byte(cfg.WriterLease.Token)) != 1 {
		return nil, errors.New("team server: principal verifier is not bound to writer lease")
	}

	securityHandler, err := NewSecurityEventHandler(
		teamSecurityEventStore{
			store: cfg.Store, storeID: cfg.ExpectedStoreID, teamID: cfg.ExpectedTeamID,
			readiness: teamReadinessOptions(cfg),
		},
		SecurityEventHandlerOptions{GatewayVerifier: cfg.PrincipalVerifier},
	)
	if err != nil {
		return nil, fmt.Errorf("team server: security event handler: %w", err)
	}
	s := &TeamServer{cfg: cfg, securityEventHandler: securityHandler}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.CheckReadiness(ctx); err != nil {
		return nil, fmt.Errorf("team server: readiness: %w", err)
	}
	return s, nil
}

func validTeamIPCSecret(secret string) bool {
	if len(secret) != 64 {
		return false
	}
	for _, char := range []byte(secret) {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// CheckReadiness revalidates marker, identity, schema, durability, policy,
// unscoped-row, and active-writer invariants against the authoritative store.
func (s *TeamServer) CheckReadiness(ctx context.Context) (store.TeamPolicyReadiness, error) {
	return s.cfg.Store.CheckTeamPolicyReadiness(ctx, teamReadinessOptions(s.cfg))
}

func teamReadinessOptions(cfg TeamServerConfig) store.TeamPolicyReadinessOptions {
	return store.TeamPolicyReadinessOptions{
		TeamReadinessOptions: store.TeamReadinessOptions{
			ExpectedStoreID: cfg.ExpectedStoreID,
			ExpectedTeamID:  cfg.ExpectedTeamID,
			ReaderVersion:   teamauth.SchemaVersion,
			WriterVersion:   teamauth.SchemaVersion,
		},
		WriterID:    cfg.WriterLease.WriterID,
		WriterToken: cfg.WriterLease.Token,
	}
}

// Handler registers an exact allowlist. It never calls Server.Handler and so
// cannot expose local outbox, ingest, memory, graph, Viewer, or backfill paths.
func (s *TeamServer) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.loopbackIPCMiddleware)
	r.Get("/health", s.handleTeamHealth)
	r.Get(teamReadinessRoutePath, s.handleTeamReadiness)
	r.Group(func(protected chi.Router) {
		protected.Use(s.readinessMiddleware)
		RegisterPrincipalCheckRoute(protected, s.cfg.PrincipalVerifier)
		RegisterSecurityEventRoute(protected, s.securityEventHandler)
		protected.Post(TeamMemoryRememberRoutePath, s.handleTeamMemoryRemember)
		protected.Post(TeamGraphDeltaRoutePath, s.handleTeamGraphDelta)
	})
	return r
}

func (s *TeamServer) loopbackIPCMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("X-Pulse-Key")
		if !isLoopbackRequest(r) || len(values) != 1 || len(values[0]) > 512 ||
			subtle.ConstantTimeCompare([]byte(values[0]), []byte(s.cfg.IPCSecret)) != 1 {
			writeTeamError(w, http.StatusUnauthorized, teamErrorUnauthorized, false)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *TeamServer) readinessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.CheckReadiness(r.Context()); err != nil {
			if r.URL.EscapedPath() == PrincipalCheckRoutePath {
				writeTeamError(w, http.StatusServiceUnavailable, teamErrorPrincipalUnavailable, false)
			} else {
				writeTeamError(w, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable, true)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

type teamSecurityEventStore struct {
	store     *store.Store
	storeID   string
	teamID    string
	readiness store.TeamPolicyReadinessOptions
}

func (s teamSecurityEventStore) AppendSecurityEvent(ctx context.Context, event SecurityEvent) error {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// OpenTeam uses BEGIN IMMEDIATE. Once this transaction-scoped fence passes,
	// another writer cannot replace the lease before the append commits.
	if err := s.store.RecheckTeamWriterLeaseTx(
		ctx, tx, s.readiness.WriterID, s.readiness.WriterToken,
	); err != nil {
		return err
	}
	eventID, err := newTeamSecurityEventID()
	if err != nil {
		return err
	}
	outcome := "denied"
	if event.EventType == SecurityEventTypeAuditDegraded {
		outcome = "error"
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_security_events(
			event_id, store_id, occurred_at, event_type, outcome,
			principal_id, client_key, team_id, project_id, request_id,
			policy_version, mode, reason_code, metadata_json,
			method_class, path_class, aggregate_count)
		VALUES (?, ?, ?, ?, ?, NULL, NULL, ?, NULL, ?, ?, 'team-remote', ?, '{}', ?, ?, ?)`,
		eventID, s.storeID, time.Now().UTC().Format(time.RFC3339Nano), event.EventType, outcome,
		s.teamID, event.RequestID, teamauth.PolicyVersion, event.ReasonCode,
		event.MethodClass, event.PathClass, event.Count,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func newTeamSecurityEventID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "security_" + hex.EncodeToString(random[:]), nil
}
