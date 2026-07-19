package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	ProductMemoryWipeSnapshotSchemaV1 = "pulse.product_memory_wipe_snapshot.v1"
	ProductMemoryWipeReceiptSchemaV1  = "pulse.product_memory_wipe_receipt.v1"
	productMemoryWipeSavepoint        = "pulse_product_memory_wipe_preview"
)

var (
	ErrProductMemoryWipeEmpty           = errors.New("product memory wipe has no affected data")
	ErrProductMemoryWipeSnapshotInvalid = errors.New("product memory wipe snapshot is invalid")
	ErrProductMemoryWipeSnapshotStale   = errors.New("product memory wipe snapshot is stale")
)

// ProductMemoryWipeSnapshotV1 binds a protected wipe ceremony to the exact
// database transition it showed the human. The digest is content-derived but
// content-free: no memory body leaves the Store through this type.
type ProductMemoryWipeSnapshotV1 struct {
	Schema              string `json:"schema"`
	Version             uint64 `json:"version"`
	StoreID             string `json:"store_id"`
	BindingDigest       string `json:"binding_digest"`
	PolicyEpoch         uint64 `json:"policy_epoch"`
	AffectedDataCount   uint64 `json:"affected_data_count"`
	AffectedDataDigest  string `json:"affected_data_digest"`
	AffectedDataVersion uint64 `json:"affected_data_version"`
}

type ProductMemoryWipeReceiptV1 struct {
	Schema            string    `json:"schema"`
	Version           uint64    `json:"version"`
	StoreID           string    `json:"store_id"`
	BindingDigest     string    `json:"binding_digest"`
	PolicyEpoch       uint64    `json:"policy_epoch"`
	AffectedDataCount uint64    `json:"affected_data_count"`
	SnapshotDigest    string    `json:"snapshot_digest"`
	WipedAt           time.Time `json:"wiped_at"`
}

// PrepareProductMemoryWipe computes the exact transition a later protected
// action may apply. The destructive statements run only under a SAVEPOINT and
// are rolled back before this method returns.
func (s *Store) PrepareProductMemoryWipe(ctx context.Context) (ProductMemoryWipeSnapshotV1, error) {
	if _, err := s.trayDestination(); err != nil {
		return ProductMemoryWipeSnapshotV1{}, err
	}
	s.runtimeAuthorityMu.RLock()
	defer s.runtimeAuthorityMu.RUnlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProductMemoryWipeSnapshotV1{}, err
	}
	defer tx.Rollback()
	return s.productMemoryWipeSnapshotTx(ctx, tx)
}

// WipeProductMemoryIfSnapshot recomputes and compares the approved transition
// after acquiring the same immediate write transaction that performs deletion.
// A concurrent write therefore either lands first and makes the snapshot stale,
// or waits until this transaction commits; it can never be silently included.
func (s *Store) WipeProductMemoryIfSnapshot(
	ctx context.Context,
	expected ProductMemoryWipeSnapshotV1,
) (ProductMemoryWipeReceiptV1, error) {
	s.runtimeAuthorityMu.RLock()
	defer s.runtimeAuthorityMu.RUnlock()
	if err := s.validateProductMemoryWipeSnapshot(expected); err != nil {
		return ProductMemoryWipeReceiptV1{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProductMemoryWipeReceiptV1{}, err
	}
	defer tx.Rollback()
	current, err := s.productMemoryWipeSnapshotTx(ctx, tx)
	if err != nil {
		return ProductMemoryWipeReceiptV1{}, err
	}
	if !sameProductMemoryWipeSnapshot(expected, current) {
		return ProductMemoryWipeReceiptV1{}, ErrProductMemoryWipeSnapshotStale
	}
	wipedAt := s.clock().UTC()
	if err := s.applyProductMemoryWipeTx(tx, wipedAt); err != nil {
		return ProductMemoryWipeReceiptV1{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProductMemoryWipeReceiptV1{}, err
	}
	return ProductMemoryWipeReceiptV1{
		Schema: ProductMemoryWipeReceiptSchemaV1, Version: 1,
		StoreID: expected.StoreID, BindingDigest: expected.BindingDigest,
		PolicyEpoch: expected.PolicyEpoch, AffectedDataCount: expected.AffectedDataCount,
		SnapshotDigest: expected.AffectedDataDigest, WipedAt: wipedAt,
	}, nil
}

func (s *Store) validateProductMemoryWipeSnapshot(snapshot ProductMemoryWipeSnapshotV1) error {
	binding, policyEpoch, _ := s.productRuntimeAuthorityLocked()
	if snapshot.Schema != ProductMemoryWipeSnapshotSchemaV1 || snapshot.Version != 1 ||
		snapshot.StoreID != s.storeID || snapshot.BindingDigest != binding ||
		policyEpoch < 1 || snapshot.PolicyEpoch != uint64(policyEpoch) ||
		snapshot.AffectedDataVersion != 1 || snapshot.AffectedDataCount == 0 ||
		!validDigest(snapshot.AffectedDataDigest) {
		return ErrProductMemoryWipeSnapshotInvalid
	}
	return nil
}

func sameProductMemoryWipeSnapshot(first, second ProductMemoryWipeSnapshotV1) bool {
	return first.Schema == second.Schema && first.Version == second.Version &&
		first.StoreID == second.StoreID && first.BindingDigest == second.BindingDigest &&
		first.PolicyEpoch == second.PolicyEpoch &&
		first.AffectedDataVersion == second.AffectedDataVersion &&
		first.AffectedDataCount == second.AffectedDataCount &&
		subtle.ConstantTimeCompare([]byte(first.AffectedDataDigest), []byte(second.AffectedDataDigest)) == 1
}

func (s *Store) productMemoryWipeSnapshotTx(
	ctx context.Context,
	tx *sql.Tx,
) (ProductMemoryWipeSnapshotV1, error) {
	binding, policyEpoch, _ := s.productRuntimeAuthorityLocked()
	if !trayBindingDigestPattern.MatchString(binding) || policyEpoch < 1 {
		return ProductMemoryWipeSnapshotV1{}, ErrProductMemoryWipeSnapshotInvalid
	}
	before, err := captureProductVaultState(ctx, tx)
	if err != nil {
		return ProductMemoryWipeSnapshotV1{}, err
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+productMemoryWipeSavepoint); err != nil {
		return ProductMemoryWipeSnapshotV1{}, err
	}
	previewOpen := true
	defer func() {
		if previewOpen {
			_, _ = tx.Exec("ROLLBACK TO " + productMemoryWipeSavepoint)
			_, _ = tx.Exec("RELEASE " + productMemoryWipeSavepoint)
		}
	}()
	if err := s.applyProductMemoryWipeTx(tx, s.clock().UTC()); err != nil {
		return ProductMemoryWipeSnapshotV1{}, err
	}
	after, err := captureProductVaultState(ctx, tx)
	if err != nil {
		return ProductMemoryWipeSnapshotV1{}, err
	}
	affectedDigest, affectedCount, err := productMemoryWipeAffectedSet(before, after)
	if err != nil {
		return ProductMemoryWipeSnapshotV1{}, err
	}
	if affectedCount == 0 {
		return ProductMemoryWipeSnapshotV1{}, ErrProductMemoryWipeEmpty
	}
	transition := sha256.New()
	writeWipeDigestFrame(transition, []byte("pulse-product-memory-wipe-transition-v1"))
	writeWipeDigestFrame(transition, []byte(s.storeID))
	writeWipeDigestFrame(transition, []byte(binding))
	writeWipeDigestUint64(transition, uint64(policyEpoch))
	writeWipeDigestUint64(transition, affectedCount)
	writeWipeDigestFrame(transition, affectedDigest[:])
	transitionDigest := hex.EncodeToString(transition.Sum(nil))
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO "+productMemoryWipeSavepoint); err != nil {
		return ProductMemoryWipeSnapshotV1{}, err
	}
	if _, err := tx.ExecContext(ctx, "RELEASE "+productMemoryWipeSavepoint); err != nil {
		return ProductMemoryWipeSnapshotV1{}, err
	}
	previewOpen = false
	return ProductMemoryWipeSnapshotV1{
		Schema: ProductMemoryWipeSnapshotSchemaV1, Version: 1,
		StoreID: s.storeID, BindingDigest: binding, PolicyEpoch: uint64(policyEpoch),
		AffectedDataCount: affectedCount, AffectedDataDigest: transitionDigest,
		AffectedDataVersion: 1,
	}, nil
}

type productVaultTable struct {
	name string
	sql  string
}

type productVaultTableState struct {
	productVaultTable
	columns    []string
	rowDigests [][32]byte
}

// captureProductVaultState snapshots ordinary application tables and excludes
// SQLite internals plus FTS virtual/shadow tables. The later multiset diff keeps
// only rows the wipe actually removes, so unrelated daemon activity cannot make
// an otherwise exact human approval stale.
func captureProductVaultState(ctx context.Context, tx *sql.Tx) ([]productVaultTableState, error) {
	tables, err := productVaultTables(ctx, tx)
	if err != nil {
		return nil, err
	}
	result := make([]productVaultTableState, 0, len(tables))
	for _, table := range tables {
		query := "SELECT * FROM " + quoteSQLiteIdentifier(table.name)
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("snapshot product table %s: %w", table.name, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		rowDigests := make([][32]byte, 0)
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				return nil, err
			}
			rowDigest, err := productVaultRowDigest(values)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("snapshot product table %s: %w", table.name, err)
			}
			rowDigests = append(rowDigests, rowDigest)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		sort.Slice(rowDigests, func(i, j int) bool {
			return bytes.Compare(rowDigests[i][:], rowDigests[j][:]) < 0
		})
		result = append(result, productVaultTableState{
			productVaultTable: table, columns: append([]string(nil), columns...), rowDigests: rowDigests,
		})
	}
	return result, nil
}

func productMemoryWipeAffectedSet(
	before, after []productVaultTableState,
) ([32]byte, uint64, error) {
	afterByName := make(map[string]productVaultTableState, len(after))
	for _, table := range after {
		afterByName[table.name] = table
	}
	affected := sha256.New()
	writeWipeDigestFrame(affected, []byte("pulse-product-memory-wipe-affected-set-v1"))
	var affectedCount uint64
	for _, beforeTable := range before {
		afterTable, exists := afterByName[beforeTable.name]
		if !exists || beforeTable.sql != afterTable.sql || !equalStrings(beforeTable.columns, afterTable.columns) {
			return [32]byte{}, 0, ErrProductMemoryWipeSnapshotInvalid
		}
		removed, added := sortedDigestDifference(beforeTable.rowDigests, afterTable.rowDigests)
		if added != 0 {
			return [32]byte{}, 0, ErrProductMemoryWipeSnapshotInvalid
		}
		if len(removed) == 0 {
			continue
		}
		writeWipeDigestFrame(affected, []byte(beforeTable.name))
		writeWipeDigestFrame(affected, []byte(beforeTable.sql))
		for _, column := range beforeTable.columns {
			writeWipeDigestFrame(affected, []byte(column))
		}
		writeWipeDigestUint64(affected, uint64(len(removed)))
		for _, digest := range removed {
			writeWipeDigestFrame(affected, digest[:])
		}
		affectedCount += uint64(len(removed))
	}
	if len(before) != len(after) {
		return [32]byte{}, 0, ErrProductMemoryWipeSnapshotInvalid
	}
	var digest [32]byte
	copy(digest[:], affected.Sum(nil))
	return digest, affectedCount, nil
}

func sortedDigestDifference(before, after [][32]byte) (removed [][32]byte, added uint64) {
	for beforeIndex, afterIndex := 0, 0; beforeIndex < len(before) || afterIndex < len(after); {
		if beforeIndex >= len(before) {
			added += uint64(len(after) - afterIndex)
			break
		}
		if afterIndex >= len(after) {
			removed = append(removed, before[beforeIndex:]...)
			break
		}
		switch bytes.Compare(before[beforeIndex][:], after[afterIndex][:]) {
		case 0:
			beforeIndex++
			afterIndex++
		case -1:
			removed = append(removed, before[beforeIndex])
			beforeIndex++
		default:
			added++
			afterIndex++
		}
	}
	return removed, added
}

func equalStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func productVaultTables(ctx context.Context, tx *sql.Tx) ([]productVaultTable, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT name, sql
		  FROM sqlite_schema
		 WHERE type='table' AND name NOT LIKE 'sqlite_%' AND sql IS NOT NULL
		 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []productVaultTable
	var virtual []string
	for rows.Next() {
		var table productVaultTable
		if err := rows.Scan(&table.name, &table.sql); err != nil {
			return nil, err
		}
		all = append(all, table)
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(table.sql)), "CREATE VIRTUAL TABLE") {
			virtual = append(virtual, table.name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ordinary := make([]productVaultTable, 0, len(all))
	for _, table := range all {
		isVirtualOrShadow := false
		for _, base := range virtual {
			if table.name == base || strings.HasPrefix(table.name, base+"_") {
				isVirtualOrShadow = true
				break
			}
		}
		if !isVirtualOrShadow {
			ordinary = append(ordinary, table)
		}
	}
	return ordinary, nil
}

func productVaultRowDigest(values []any) ([32]byte, error) {
	row := sha256.New()
	writeWipeDigestFrame(row, []byte("pulse-product-vault-row-v1"))
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
			writeWipeDigestFrame(row, []byte{0})
		case int64:
			writeWipeDigestFrame(row, []byte{1})
			writeWipeDigestUint64(row, uint64(typed))
		case float64:
			writeWipeDigestFrame(row, []byte{2})
			writeWipeDigestUint64(row, math.Float64bits(typed))
		case bool:
			writeWipeDigestFrame(row, []byte{3})
			if typed {
				writeWipeDigestFrame(row, []byte{1})
			} else {
				writeWipeDigestFrame(row, []byte{0})
			}
		case []byte:
			writeWipeDigestFrame(row, []byte{4})
			writeWipeDigestFrame(row, typed)
		case string:
			writeWipeDigestFrame(row, []byte{5})
			writeWipeDigestFrame(row, []byte(typed))
		case time.Time:
			writeWipeDigestFrame(row, []byte{6})
			writeWipeDigestFrame(row, []byte(typed.UTC().Format(time.RFC3339Nano)))
		default:
			return [32]byte{}, fmt.Errorf("unsupported sqlite value %T", value)
		}
	}
	var digest [32]byte
	copy(digest[:], row.Sum(nil))
	return digest, nil
}

func writeWipeDigestFrame(target hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = target.Write(length[:])
	_, _ = target.Write(value)
}

func writeWipeDigestUint64(target hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	writeWipeDigestFrame(target, encoded[:])
}
