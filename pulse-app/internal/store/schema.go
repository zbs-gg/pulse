package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migrationDescriptor struct {
	Version int
	Name    string
	SQL     string
	SHA256  string
	Policy  migrationPolicy
}

type migrationPolicy struct {
	StoreKinds       map[StoreKind]bool
	MinReaderVersion int
	MinWriterVersion int
}

const (
	firstPersonalMigrationVersion = 33
	latestSchemaVersion           = 45
)

var postFoundationMigrationPolicies = map[int]migrationPolicy{
	33: {
		StoreKinds: map[StoreKind]bool{
			StoreKindLocalPreview: true,
			StoreKindPersonal:     true,
		},
		MinReaderVersion: 33,
		MinWriterVersion: 33,
	},
	34: {StoreKinds: map[StoreKind]bool{StoreKindPersonal: true}, MinReaderVersion: 34, MinWriterVersion: 34},
	35: {StoreKinds: map[StoreKind]bool{StoreKindPersonal: true}, MinReaderVersion: 35, MinWriterVersion: 35},
	36: {StoreKinds: map[StoreKind]bool{StoreKindPersonal: true}, MinReaderVersion: 36, MinWriterVersion: 36},
	37: {StoreKinds: map[StoreKind]bool{StoreKindPersonal: true}, MinReaderVersion: 37, MinWriterVersion: 37},
	38: {StoreKinds: map[StoreKind]bool{StoreKindPersonal: true}, MinReaderVersion: 38, MinWriterVersion: 38},
	39: {StoreKinds: map[StoreKind]bool{StoreKindPersonal: true}, MinReaderVersion: 39, MinWriterVersion: 39},
	40: {StoreKinds: map[StoreKind]bool{StoreKindPersonal: true}, MinReaderVersion: 40, MinWriterVersion: 40},
	41: {StoreKinds: map[StoreKind]bool{StoreKindPersonal: true}, MinReaderVersion: 41, MinWriterVersion: 41},
	42: {
		StoreKinds: map[StoreKind]bool{
			StoreKindLocalPreview: true,
			StoreKindPersonal:     true,
		},
		MinReaderVersion: 42,
		MinWriterVersion: 42,
	},
	43: {
		StoreKinds: map[StoreKind]bool{
			StoreKindLocalPreview: true,
			StoreKindPersonal:     true,
		},
		MinReaderVersion: 43,
		MinWriterVersion: 43,
	},
	44: {StoreKinds: map[StoreKind]bool{StoreKindPersonal: true}, MinReaderVersion: 44, MinWriterVersion: 44},
	45: {StoreKinds: map[StoreKind]bool{StoreKindPersonal: true}, MinReaderVersion: 45, MinWriterVersion: 45},
}

func loadMigrationSet(fsys fs.FS) ([]migrationDescriptor, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	migrations := make([]migrationDescriptor, 0, len(names))
	for _, name := range names {
		var version int
		if _, err := fmt.Sscanf(name, "%03d_", &version); err != nil {
			return nil, fmt.Errorf("parse migration name %q: %w", name, err)
		}
		body, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		digest := sha256.Sum256(body)
		migrations = append(migrations, migrationDescriptor{
			Version: version,
			Name:    name,
			SQL:     string(body),
			SHA256:  fmt.Sprintf("%x", digest),
			Policy:  postFoundationMigrationPolicies[version],
		})
	}
	if err := validateMigrationSequence(migrations); err != nil {
		return nil, err
	}
	return migrations, nil
}

func validateMigrationSequence(migrations []migrationDescriptor) error {
	if len(migrations) == 0 {
		return fmt.Errorf("migration sequence is empty")
	}
	for i, migration := range migrations {
		want := i + 1
		if migration.Version != want {
			return fmt.Errorf("migration sequence at position %d has version %d, want %d", i, migration.Version, want)
		}
		if migration.Version >= firstPersonalMigrationVersion {
			if len(migration.Policy.StoreKinds) == 0 || migration.Policy.MinReaderVersion < migration.Version ||
				migration.Policy.MinWriterVersion < migration.Policy.MinReaderVersion {
				return fmt.Errorf("migration %03d has no valid store applicability policy", migration.Version)
			}
		}
	}
	return nil
}

// migrate applies pending migrations in order and verifies the immutable
// fingerprint manifest introduced by migration 033.
func migrate(db *sql.DB) error {
	return migrateForProfile(db, storeOpenProfile{Kind: StoreKindLocalPreview})
}

func migrateForProfile(db *sql.DB, profile storeOpenProfile) error {
	if _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS schema_meta (
            version INTEGER PRIMARY KEY,
            applied TEXT NOT NULL
        )
    `); err != nil {
		return fmt.Errorf("create schema_meta: %w", err)
	}

	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		return err
	}
	current, err := validateAppliedSequence(db, len(migrations))
	if err != nil {
		return err
	}
	if err := validateStoredManifest(db, migrations, current); err != nil {
		return err
	}
	if current >= firstPersonalMigrationVersion {
		if _, err := validateStoreIdentityForVersion(db, profile, current); err != nil {
			return err
		}
	}

	for _, migration := range migrations {
		if migration.Version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin %s: %w", migration.Name, err)
		}
		disposition := "applied"
		if migration.Version < firstPersonalMigrationVersion || migration.Policy.StoreKinds[profile.Kind] {
			if _, err := tx.Exec(migration.SQL); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply %s: %w", migration.Name, err)
			}
			if migration.Version == 39 && profile.Kind == StoreKindPersonal {
				if err := backfillPublishedPersonalCapsulesTx(tx); err != nil {
					_ = tx.Rollback()
					return fmt.Errorf("adopt published Personal capsules: %w", err)
				}
			}
		} else {
			disposition = "skipped"
		}
		appliedAt := time.Now().UTC().Format(time.RFC3339Nano)
		if migration.Version == firstPersonalMigrationVersion {
			if err := createStoreIdentityTx(tx, profile, appliedAt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("create store identity: %w", err)
			}
		}
		if _, err := tx.Exec(
			"INSERT INTO schema_meta(version, applied) VALUES (?, ?)",
			migration.Version, appliedAt,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", migration.Name, err)
		}
		if migration.Version >= firstPersonalMigrationVersion {
			// Migration 033 adopts all earlier migrations into the immutable
			// fingerprint chain in the same transaction that creates it.
			start := migration.Version - 1
			if migration.Version == firstPersonalMigrationVersion {
				start = 0
			}
			for _, item := range migrations[start:migration.Version] {
				if _, err := tx.Exec(`
					INSERT INTO schema_migration_manifest(version, name, sha256, applied_at)
					VALUES (?, ?, ?, ?)`, item.Version, item.Name, item.SHA256, appliedAt); err != nil {
					_ = tx.Rollback()
					return fmt.Errorf("record migration fingerprint %s: %w", item.Name, err)
				}
			}
		}
		if migration.Version >= firstPersonalMigrationVersion {
			if _, err := tx.Exec(`
				INSERT INTO schema_migration_applicability(
					version, store_kind, disposition, min_reader_version, min_writer_version, recorded_at
				) VALUES (?, ?, ?, ?, ?, ?)`, migration.Version, profile.Kind, disposition,
				migration.Policy.MinReaderVersion, migration.Policy.MinWriterVersion, appliedAt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("record migration applicability %s: %w", migration.Name, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", migration.Name, err)
		}
		current = migration.Version
	}
	if err := validateStoredManifest(db, migrations, current); err != nil {
		return err
	}
	if _, err := validateStoreIdentity(db, profile); err != nil {
		return err
	}
	return validateMigrationApplicability(db, migrations, current, profile.Kind)
}

// backfillPublishedPersonalCapsulesTx adopts capsules written by the official
// 0.6.7 schema into the scoped Personal object index. The capsule rows remain
// byte-for-byte intact and become Personal Global so an upgrade cannot make
// existing memories disappear from fresh sessions.
func backfillPublishedPersonalCapsulesTx(tx *sql.Tx) error {
	type legacyCapsule struct {
		id, sourceHost, sourceTimestamp, createdAt string
	}
	rows, err := tx.Query(`
		SELECT capsule.id, capsule.source_host, capsule.source_timestamp, capsule.created_at
		  FROM memory_capsules capsule
		  LEFT JOIN private_memory_objects object ON object.object_id=capsule.id
		 WHERE object.object_id IS NULL`)
	if err != nil {
		return err
	}
	var capsules []legacyCapsule
	for rows.Next() {
		var capsule legacyCapsule
		if err := rows.Scan(&capsule.id, &capsule.sourceHost, &capsule.sourceTimestamp, &capsule.createdAt); err != nil {
			_ = rows.Close()
			return err
		}
		capsules = append(capsules, capsule)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, capsule := range capsules {
		digestBytes := sha256.Sum256([]byte("pulse-personal-v067-capsule\x1f" + capsule.id))
		digest := fmt.Sprintf("%x", digestBytes[:])
		suffix := digest[:24]
		ledgerID := "legacy_ledger_" + suffix
		candidateID := "legacy_candidate_" + suffix
		sessionID := "legacy_session_" + suffix
		turnID := "legacy_turn_" + suffix
		eventKey := "legacy_event_" + suffix
		idempotencyKey := "legacy_idempotency_" + suffix
		if _, err := tx.Exec(`
			INSERT INTO turn_ledgers(
			  ledger_id, finalize_receipt_id, host, session_id, turn_id, source_event_key,
			  idempotency_key, binding_digest, destination_store_id, destination_class,
			  policy_epoch, resolver_epoch, request_digest, state, created_at, finalized_at)
			VALUES (?, ?, 'pulse-cli', ?, ?, ?, ?, 'published-personal-v0.6.7',
			        (SELECT store_id FROM store_identity WHERE singleton=1), 'personal',
			        0, 0, ?, 'candidates', ?, ?)`,
			ledgerID, "legacy_finalize_"+suffix, sessionID, turnID, eventKey,
			idempotencyKey, digest, capsule.createdAt, capsule.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO memory_tray_candidates(
			  candidate_id, ledger_id, candidate_kind, operation, version, content_digest,
			  payload_json, state, grace_expires_at, canonical_object_id,
			  created_at, updated_at, terminal_at)
			VALUES (?, ?, 'memory_capsule', 'create', 1, ?, '{}', 'committed', ?, ?, ?, ?, ?)`,
			candidateID, ledgerID, digest, capsule.createdAt, capsule.id,
			capsule.createdAt, capsule.createdAt, capsule.createdAt); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO private_memory_objects(
			  object_id, candidate_kind, content_digest, created_from_candidate_id, created_at,
			  logical_memory_id, logical_generation, project_namespace_id, original_repository_id,
			  memory_scope, modified_at, capture_host, capture_session_ref, captured_at)
			VALUES (?, 'memory_capsule', ?, ?, ?, ?, 1, '', '', 'personal_global', ?, ?, ?, ?)`,
			capsule.id, digest, candidateID, capsule.createdAt, capsule.id, capsule.createdAt,
			capsule.sourceHost, sessionID, capsule.sourceTimestamp); err != nil {
			return err
		}
	}
	return nil
}

func createStoreIdentityTx(tx *sql.Tx, profile storeOpenProfile, appliedAt string) error {
	storeID := profile.ExpectedStoreID
	if storeID == "" {
		generated, err := randomStoreInstanceID()
		if err != nil {
			return err
		}
		storeID = generated
	}
	_, err := tx.Exec(`
		INSERT INTO store_identity(
			singleton, store_id, store_kind, min_reader_version, min_writer_version, created_at
		) VALUES (1, ?, ?, 33, 33, ?)`, storeID, profile.Kind, appliedAt)
	return err
}

func randomStoreInstanceID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate physical store identity: %w", err)
	}
	return fmt.Sprintf("store_instance_%x", value[:]), nil
}

func validateMigrationApplicability(db *sql.DB, migrations []migrationDescriptor, current int, kind StoreKind) error {
	if current < firstPersonalMigrationVersion {
		return nil
	}
	rows, err := db.Query(`
		SELECT version, store_kind, disposition, min_reader_version, min_writer_version
		  FROM schema_migration_applicability ORDER BY version`)
	if err != nil {
		return fmt.Errorf("read migration applicability: %w", err)
	}
	defer rows.Close()
	wantVersion := firstPersonalMigrationVersion
	for rows.Next() {
		var version, readerFloor, writerFloor int
		var storedKind StoreKind
		var disposition string
		if err := rows.Scan(&version, &storedKind, &disposition, &readerFloor, &writerFloor); err != nil {
			return fmt.Errorf("scan migration applicability: %w", err)
		}
		if version != wantVersion || version > current || storedKind != kind {
			return fmt.Errorf("migration applicability sequence invalid at version %d", version)
		}
		policy := migrations[version-1].Policy
		wantDisposition := "skipped"
		if policy.StoreKinds[kind] {
			wantDisposition = "applied"
		}
		if disposition != wantDisposition || readerFloor != policy.MinReaderVersion || writerFloor != policy.MinWriterVersion {
			return fmt.Errorf("migration applicability mismatch at version %d", version)
		}
		wantVersion++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read migration applicability: %w", err)
	}
	if wantVersion != current+1 {
		return fmt.Errorf("migration applicability has %d rows, want %d",
			wantVersion-firstPersonalMigrationVersion, current-firstPersonalMigrationVersion+1)
	}
	return nil
}

func validateAppliedSequence(db *sql.DB, latest int) (int, error) {
	rows, err := db.Query(`SELECT version FROM schema_meta ORDER BY version`)
	if err != nil {
		return 0, fmt.Errorf("read schema versions: %w", err)
	}
	defer rows.Close()
	current := 0
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return 0, fmt.Errorf("scan schema version: %w", err)
		}
		if version != current+1 {
			return 0, fmt.Errorf("applied migration sequence has version %d after %d", version, current)
		}
		if version > latest {
			return 0, fmt.Errorf("database schema version %d is newer than binary version %d", version, latest)
		}
		current = version
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read schema versions: %w", err)
	}
	return current, nil
}

func validateStoredManifest(db *sql.DB, migrations []migrationDescriptor, current int) error {
	var exists int
	if err := db.QueryRow(`
		SELECT count(*) FROM sqlite_master
		 WHERE type = 'table' AND name = 'schema_migration_manifest'`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect migration manifest: %w", err)
	}
	if exists == 0 {
		if current >= 33 {
			return fmt.Errorf("migration fingerprint manifest missing at schema version %d", current)
		}
		return nil
	}
	if current < 33 {
		return fmt.Errorf("migration fingerprint manifest exists before migration 033")
	}

	rows, err := db.Query(`SELECT version, name, sha256 FROM schema_migration_manifest ORDER BY version`)
	if err != nil {
		return fmt.Errorf("read migration fingerprint manifest: %w", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var version int
		var name, fingerprint string
		if err := rows.Scan(&version, &name, &fingerprint); err != nil {
			return fmt.Errorf("scan migration fingerprint: %w", err)
		}
		seen++
		if version != seen || version > current {
			return fmt.Errorf("migration fingerprint sequence invalid at version %d", version)
		}
		want := migrations[version-1]
		if name != want.Name || fingerprint != want.SHA256 {
			return fmt.Errorf("migration fingerprint mismatch at version %d", version)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read migration fingerprint manifest: %w", err)
	}
	if seen != current {
		return fmt.Errorf("migration fingerprint manifest has %d rows, want %d", seen, current)
	}
	return nil
}
