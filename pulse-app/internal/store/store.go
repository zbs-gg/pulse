package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"

	_ "modernc.org/sqlite"
)

type StoreKind string

const (
	StoreKindLocalPreview StoreKind = "local-preview"
	StoreKindPersonal     StoreKind = "personal"
	StoreKindDesk         StoreKind = "desk"
	StoreKindCommons      StoreKind = "commons"
)

var ErrStoreIdentityMismatch = errors.New("store identity does not match the requested vault")
var productStoreIDPattern = regexp.MustCompile(`^store_[a-z0-9][a-z0-9_]{2,127}$`)

type storeOpenProfile struct {
	Kind            StoreKind
	ExpectedStoreID string
}

// Store wraps a *sql.DB. Use DB() to access the underlying handle.
type Store struct {
	db                    *sql.DB
	path                  string
	storeKind             StoreKind
	storeID               string
	expectedBootstrapRoot *teamauth.BootstrapRoot
	clock                 func() time.Time
	expectedBindingDigest string
	expectedPolicyEpoch   int64
	expectedResolverEpoch int64
	continuityAuthorityMu sync.RWMutex
	continuityRepository  string
	publicationTargetMu   sync.RWMutex
	publicationTarget     *TeamPublicationTarget

	// Claim-resolution config (default off). See claim_resolver.go.
	claimMode      string                          // "off" | "shadow" | "on"
	claimThreshold float64                         // cosine threshold for same-key supersede
	claimEmbed     func(string) ([]float32, error) // injected by the server when an embedder is wired
	claimXKey      bool                            // enable cross-key resolution (subject phrased differently)
	claimXThresh   float64                         // higher cosine threshold for cross-key supersede
	claimPara      bool                            // enable paraphrase matching for brand-new claim_keys
	claimParaThr   float64                         // cosine threshold for paraphrase match (default 0.90)
}

// DB returns the underlying *sql.DB for use by other packages.
func (s *Store) DB() *sql.DB {
	return s.db
}

// DBPath returns the filesystem path used to open this store.
func (s *Store) DBPath() string {
	return s.path
}

func (s *Store) StoreKind() StoreKind { return s.storeKind }

func (s *Store) StoreID() string { return s.storeID }

func (s *Store) ConfigureProductRuntimeAuthority(bindingDigest string, policyEpoch, resolverEpoch int64) error {
	if s.storeKind != StoreKindPersonal && s.storeKind != StoreKindDesk {
		return errors.New("runtime authority applies only to Personal or Desk stores")
	}
	if !trayBindingDigestPattern.MatchString(bindingDigest) || policyEpoch < 0 || resolverEpoch < 0 {
		return errors.New("product runtime authority is invalid")
	}
	s.expectedBindingDigest = bindingDigest
	s.expectedPolicyEpoch = policyEpoch
	s.expectedResolverEpoch = resolverEpoch
	return nil
}

func (s *Store) productRuntimeAuthority() (string, int64, int64) {
	return s.expectedBindingDigest, s.expectedPolicyEpoch, s.expectedResolverEpoch
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// IncrementAccessCounts bumps access_count and stamps last_accessed_at for the
// given event ids (migration 029, Phase A instrumentation). It is keyed by id
// ONLY — no content, transcript, or text is read or written — so it is not the
// raw-content write path and leaves the daemon-never-sees-raw invariant intact.
// A nil/empty slice is a no-op. Callers treat this as best-effort: a failed
// counter write must never fail a retrieval.
func (s *Store) IncrementAccessCounts(ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, at.UTC().Format(time.RFC3339))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(
		"UPDATE events SET access_count = access_count + 1, last_accessed_at = ? WHERE id IN (%s)",
		strings.Join(placeholders, ","))
	_, err := s.db.Exec(q, args...)
	return err
}

// FeedSignal — proactive feed signal computed by pulse_consolidate.py.
type FeedSignal struct {
	ID              int64   `json:"id"`
	SignalKind      string  `json:"signal_kind"`
	SubjectEntityID *int64  `json:"subject_entity_id,omitempty"`
	EvidenceObsIDs  []any   `json:"evidence_obs_ids"`
	Salience        float64 `json:"salience"`
	ComputedAt      string  `json:"computed_at"`
	ConsumedAt      *string `json:"consumed_at,omitempty"`
}

// RecentFeedSignals returns up to `limit` most-salient unconsumed signals.
// If markConsumed is true, marks them consumed in the same transaction.
func (s *Store) RecentFeedSignals(limit int, markConsumed bool) ([]FeedSignal, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
        SELECT id, signal_kind, subject_entity_id, evidence_obs_ids, salience, computed_at, consumed_at
          FROM feed_signals
         WHERE consumed_at IS NULL
         ORDER BY salience DESC, computed_at DESC
         LIMIT ?
    `, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FeedSignal
	var ids []int64
	for rows.Next() {
		var fs FeedSignal
		var evidenceJSON string
		var subj sql.NullInt64
		var consumed sql.NullString
		if err := rows.Scan(&fs.ID, &fs.SignalKind, &subj, &evidenceJSON, &fs.Salience, &fs.ComputedAt, &consumed); err != nil {
			return nil, err
		}
		if subj.Valid {
			v := subj.Int64
			fs.SubjectEntityID = &v
		}
		if consumed.Valid {
			v := consumed.String
			fs.ConsumedAt = &v
		}
		if err := json.Unmarshal([]byte(evidenceJSON), &fs.EvidenceObsIDs); err != nil {
			fs.EvidenceObsIDs = []any{}
		}
		out = append(out, fs)
		ids = append(ids, fs.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if markConsumed && len(ids) > 0 {
		now := time.Now().UTC().Format(time.RFC3339)
		for _, id := range ids {
			if _, err := tx.Exec("UPDATE feed_signals SET consumed_at=? WHERE id=?", now, id); err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// Open opens a local SQLite database. A marked team database must be opened
// explicitly with OpenTeam so an old/local writer cannot silently proceed.
func Open(path string) (*Store, error) {
	return openStore(path, false, TeamOpenOptions{}, storeOpenProfile{Kind: StoreKindLocalPreview})
}

func OpenVault(path string, kind StoreKind, storeID string) (*Store, error) {
	if (kind != StoreKindPersonal && kind != StoreKindDesk) || !productStoreIDPattern.MatchString(storeID) {
		return nil, errors.New("product local store requires personal or desk kind and exact store ID")
	}
	return openStore(path, false, TeamOpenOptions{}, storeOpenProfile{Kind: kind, ExpectedStoreID: storeID})
}

type TeamOpenOptions struct {
	ExpectedBootstrapRoot teamauth.BootstrapRoot
	Clock                 func() time.Time
}

// OpenTeam opens a team-store candidate with the required FULL durability
// profile. An empty database remains unmarked until BootstrapTeam succeeds.
func OpenTeam(path string, options TeamOpenOptions) (*Store, error) {
	if err := options.ExpectedBootstrapRoot.Validate(); err != nil {
		return nil, fmt.Errorf("team expected bootstrap root: %w", err)
	}
	return openStore(path, true, options, storeOpenProfile{Kind: StoreKindCommons})
}

func openStore(path string, team bool, options TeamOpenOptions, profile storeOpenProfile) (*Store, error) {
	synchronous := "NORMAL"
	if team {
		synchronous = "FULL"
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(%s)", path, synchronous)
	if team || profile.Kind == StoreKindPersonal || profile.Kind == StoreKindDesk {
		dsn += "&_txlock=immediate"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if !team {
		marked, err := hasTeamMarker(db)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("inspect team marker: %w", err)
		}
		if marked {
			db.Close()
			return nil, ErrTeamStoreRequiresTeamOpen
		}
	}
	initialVersion, err := existingSchemaVersion(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("read initial schema version: %w", err)
	}
	initialCatalogBlank := false
	if team && initialVersion == 0 {
		initialCatalogBlank, err = bootstrapCatalogIsBlank(db)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("inspect initial SQLite catalog: %w", err)
		}
	}
	if err := migrateForProfile(db, profile); err != nil {
		db.Close()
		return nil, err
	}
	identity, err := validateStoreIdentity(db, profile)
	if err != nil {
		db.Close()
		return nil, err
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	if team && initialVersion == 0 && initialCatalogBlank {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO team_bootstrap_candidates(singleton, created_by_version, created_at)
			VALUES (1, 33, ?)`, clock().UTC().Format(time.RFC3339Nano)); err != nil {
			db.Close()
			return nil, fmt.Errorf("record team bootstrap candidate: %w", err)
		}
	}
	if !team {
		marked, err := hasTeamMarker(db)
		if err != nil {
			db.Close()
			return nil, err
		}
		if marked {
			db.Close()
			return nil, ErrTeamStoreRequiresTeamOpen
		}
	}
	store := &Store{db: db, path: path, storeKind: identity.Kind, storeID: identity.StoreID, clock: clock}
	if identity.Kind == StoreKindPersonal || identity.Kind == StoreKindDesk {
		store.expectedBindingDigest = localStoreBindingDigest(identity.StoreID)
	}
	if team {
		expected := options.ExpectedBootstrapRoot
		store.expectedBootstrapRoot = &expected
		var storedFingerprint string
		err := db.QueryRow(`SELECT bootstrap_root_fingerprint FROM team_stores WHERE singleton = 1`).Scan(&storedFingerprint)
		if err == nil {
			expectedFingerprint, _ := expected.Fingerprint()
			if storedFingerprint != expectedFingerprint {
				db.Close()
				return nil, ErrBootstrapRootMismatch
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			db.Close()
			return nil, fmt.Errorf("verify team bootstrap root: %w", err)
		}
	}
	return store, nil
}

type persistedStoreIdentity struct {
	StoreID string
	Kind    StoreKind
}

func validateStoreIdentity(db *sql.DB, profile storeOpenProfile) (persistedStoreIdentity, error) {
	return validateStoreIdentityForVersion(db, profile, 46)
}

func validateStoreIdentityForVersion(db *sql.DB, profile storeOpenProfile, schemaVersion int) (persistedStoreIdentity, error) {
	var identity persistedStoreIdentity
	var readerFloor, writerFloor int
	err := db.QueryRow(`
		SELECT store_id, store_kind, min_reader_version, min_writer_version
		  FROM store_identity WHERE singleton = 1`,
	).Scan(&identity.StoreID, &identity.Kind, &readerFloor, &writerFloor)
	if err != nil {
		return persistedStoreIdentity{}, fmt.Errorf("read store identity: %w", err)
	}
	if identity.Kind != profile.Kind || (profile.ExpectedStoreID != "" && identity.StoreID != profile.ExpectedStoreID) {
		return persistedStoreIdentity{}, ErrStoreIdentityMismatch
	}
	expectedFloor := 40
	for version, policy := range postFoundationMigrationPolicies {
		if version <= schemaVersion && policy.StoreKinds[profile.Kind] && policy.MinWriterVersion > expectedFloor {
			expectedFloor = policy.MinWriterVersion
		}
	}
	if readerFloor != expectedFloor || writerFloor != expectedFloor {
		return persistedStoreIdentity{}, errors.New("store schema floor is incompatible with this binary")
	}
	return identity, nil
}

func bootstrapCatalogIsBlank(db *sql.DB) (bool, error) {
	rows, err := db.Query(`
		SELECT type, name
		  FROM sqlite_master
		 WHERE type IN ('table', 'view', 'trigger', 'index')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var objectType, name string
		if err := rows.Scan(&objectType, &name); err != nil {
			return false, err
		}
		if !strings.HasPrefix(name, "sqlite_") {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func existingSchemaVersion(db *sql.DB) (int, error) {
	var exists int
	if err := db.QueryRow(`
		SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_meta'`).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_meta`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func hasTeamMarker(db *sql.DB) (bool, error) {
	var tableExists int
	if err := db.QueryRow(`
		SELECT count(*) FROM sqlite_master
		 WHERE type = 'table' AND name = 'team_stores'`).Scan(&tableExists); err != nil {
		return false, err
	}
	if tableExists == 0 {
		return false, nil
	}
	var markers int
	if err := db.QueryRow(`SELECT count(*) FROM team_stores`).Scan(&markers); err != nil {
		return false, err
	}
	return markers != 0, nil
}
