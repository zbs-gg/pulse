package consolidation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
	_ "modernc.org/sqlite"
)

type fileSnapshot struct {
	size        int64
	modified    int64
	device      uint64
	inode       uint64
	walExists   bool
	walSize     int64
	walModified int64
}

func (e *Engine) inspectDatabase(
	ctx context.Context,
	candidate sourceCandidate,
	rowBudget, workingBudget int64,
) inspectedSource {
	base := inspectedSource{
		path: candidate.path, classification: candidate.hint,
		reasonCode: "recognized_database", counts: map[string]int64{}, canonical: candidate.canonical,
	}
	if candidate.canonical {
		base.classification = ClassificationCanonicalVault
	}
	if rowBudget < 1 || workingBudget < 1 {
		return partialSource(candidate, "resource_limit")
	}

	lease, before, err := openReadLease(candidate.path, e.homeDir)
	if err != nil {
		return partialSource(candidate, safeInspectionReason(err))
	}
	defer lease.Close()
	base.stateDigest = snapshotStateDigest(e.manager, before)
	if before.size > e.limits.MaxBytesPerSource {
		return partialSource(candidate, "resource_limit")
	}

	db := e.canonicalDB
	closeDB := false
	if !candidate.canonical || db == nil {
		if before.walExists && before.walSize > 0 {
			return partialSource(candidate, "active_wal")
		}
		dsnURL := url.URL{Scheme: "file", Path: candidate.path}
		dsn := dsnURL.String() + "?mode=ro&immutable=1&_pragma=query_only(1)&_pragma=busy_timeout(50)"
		db, err = sql.Open("sqlite", dsn)
		if err != nil {
			return partialSource(candidate, "source_locked")
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		closeDB = true
	}
	if closeDB {
		defer db.Close()
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return partialSource(candidate, "source_locked")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var queryOnly int
	if err := tx.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil ||
		(queryOnly != 1 && (!candidate.canonical || e.canonicalDB == nil)) {
		return partialSource(candidate, "read_only_unavailable")
	}
	var integrity string
	if err := tx.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&integrity); err != nil || integrity != "ok" {
		return partialSource(candidate, "integrity_failed")
	}
	var schemaBefore, dataBefore int64
	if err := tx.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&schemaBefore); err != nil {
		return partialSource(candidate, "unsupported_schema")
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA data_version").Scan(&dataBefore); err != nil {
		return partialSource(candidate, "source_changed")
	}
	tables, err := sqliteTables(ctx, tx)
	if err != nil {
		return partialSource(candidate, "unsupported_schema")
	}

	switch {
	case candidate.canonical:
		if !tables["store_identity"] {
			return partialSource(candidate, "unsupported_schema")
		}
		base, err = e.inspectPulseSource(ctx, tx, base, tables, rowBudget, workingBudget)
	case tables["schema_versions"] && tables["observations"] && tables["session_summaries"]:
		base, err = e.inspectClaudeMemSource(ctx, tx, base, tables, rowBudget, workingBudget)
	case tables["observations"] || tables["memory_capsules"] || tables["private_memory_objects"]:
		base.classification = ClassificationLegacyPulseDB
		base, err = e.inspectPulseSource(ctx, tx, base, tables, rowBudget, workingBudget)
	default:
		return partialSource(candidate, "unsupported_schema")
	}
	if err != nil {
		return partialSource(candidate, safeInspectionReason(err))
	}

	var schemaAfter, dataAfter int64
	if err := tx.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&schemaAfter); err != nil {
		return partialSource(candidate, "source_changed")
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA data_version").Scan(&dataAfter); err != nil {
		return partialSource(candidate, "source_changed")
	}
	if schemaAfter != schemaBefore || dataAfter != dataBefore {
		base.stale = true
		base.reasonCode = "source_changed"
	}
	if err := tx.Commit(); err != nil {
		return partialSource(candidate, "source_locked")
	}
	committed = true
	after, err := snapshotFile(candidate.path)
	if err != nil || before != after {
		base.stale = true
		base.reasonCode = "source_changed"
	}
	identityParts := []string{
		"sqlite-v1", fmt.Sprint(before.size), fmt.Sprint(before.modified),
		fmt.Sprint(before.device), fmt.Sprint(before.inode), fmt.Sprint(schemaBefore), fmt.Sprint(dataBefore),
	}
	itemDigests := make([]string, 0, len(base.items))
	for _, item := range base.items {
		itemDigests = append(itemDigests, e.manager.mac([]byte(item.stableKey+"\x1f"+item.fingerprint)))
	}
	sort.Strings(itemDigests)
	identityParts = append(identityParts, itemDigests...)
	base.identityDigest = e.manager.mac([]byte(strings.Join(identityParts, "\x1f")))
	return base
}

func openReadLease(path, ownerRoot string) (*os.File, fileSnapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fileSnapshot{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fileSnapshot{}, errors.New("unsafe_file_type")
	}
	rootInfo, err := os.Stat(ownerRoot)
	if err != nil {
		return nil, fileSnapshot{}, err
	}
	if owner, ok := numericStatField(info, "Uid"); ok {
		if expected, expectedOK := numericStatField(rootInfo, "Uid"); expectedOK && owner != expected {
			return nil, fileSnapshot{}, errors.New("owner_mismatch")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fileSnapshot{}, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		_ = file.Close()
		return nil, fileSnapshot{}, errors.New("source_changed")
	}
	snapshot, err := snapshotFile(path)
	if err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, err
	}
	return file, snapshot, nil
}

func snapshotFile(path string) (fileSnapshot, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fileSnapshot{}, errors.New("unsafe_file_type")
	}
	snapshot := fileSnapshot{size: info.Size(), modified: info.ModTime().UnixNano()}
	snapshot.device, _ = numericStatField(info, "Dev")
	snapshot.inode, _ = numericStatField(info, "Ino")
	walInfo, walErr := os.Lstat(path + "-wal")
	if walErr == nil {
		if !walInfo.Mode().IsRegular() || walInfo.Mode()&os.ModeSymlink != 0 {
			return fileSnapshot{}, errors.New("unsafe_wal")
		}
		snapshot.walExists = true
		snapshot.walSize = walInfo.Size()
		snapshot.walModified = walInfo.ModTime().UnixNano()
	} else if !errors.Is(walErr, os.ErrNotExist) {
		return fileSnapshot{}, walErr
	}
	return snapshot, nil
}

func currentPathStateDigest(path string, manager *Manager) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("unsafe_file_type")
	}
	if info.IsDir() {
		payload := fmt.Sprintf("directory-state-v1\x1f%d\x1f%d", info.Size(), info.ModTime().UnixNano())
		if manager != nil {
			return manager.mac([]byte(payload)), nil
		}
		return sha256Hex([]byte(payload)), nil
	}
	snapshot, err := snapshotFile(path)
	if err != nil {
		return "", err
	}
	if manager != nil {
		return snapshotStateDigest(manager, snapshot), nil
	}
	payload := []byte(snapshotStatePayload(snapshot))
	return sha256Hex(payload), nil
}

func snapshotStateDigest(manager *Manager, snapshot fileSnapshot) string {
	return manager.mac([]byte(snapshotStatePayload(snapshot)))
}

func snapshotStatePayload(snapshot fileSnapshot) string {
	return fmt.Sprintf(
		"sqlite-state-v1\x1f%d\x1f%d\x1f%d\x1f%d\x1f%t\x1f%d\x1f%d",
		snapshot.size, snapshot.modified, snapshot.device, snapshot.inode,
		snapshot.walExists, snapshot.walSize, snapshot.walModified,
	)
}

func numericStatField(info os.FileInfo, name string) (uint64, bool) {
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return 0, false
	}
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, false
	}
	field := value.FieldByName(name)
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return field.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Int() < 0 {
			return 0, false
		}
		return uint64(field.Int()), true
	default:
		return 0, false
	}
}

func sqliteTables(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables[name] = true
	}
	return tables, rows.Err()
}

func (e *Engine) inspectPulseSource(
	ctx context.Context,
	tx *sql.Tx,
	source inspectedSource,
	tables map[string]bool,
	rowBudget, workingBudget int64,
) (inspectedSource, error) {
	counts := source.counts
	addRows := func(count int64) error {
		if count < 0 || count > rowBudget-counts["source_rows"] {
			return errors.New("resource_limit")
		}
		counts["source_rows"] += count
		return nil
	}
	if tables["observations"] {
		count, err := countQuery(ctx, tx, "SELECT COUNT(*) FROM observations")
		if err != nil {
			return source, err
		}
		if err := addRows(count); err != nil {
			return source, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT source_id, COALESCE(content_text, '') FROM observations ORDER BY id LIMIT ?", rowBudget+1)
		if err != nil {
			return source, err
		}
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				rows.Close()
				return source, err
			}
			var stableKey, content string
			if err := rows.Scan(&stableKey, &content); err != nil {
				rows.Close()
				return source, err
			}
			if normalized := normalizeContent(content); normalized != "" {
				if err := appendInventoryItem(&source, inventoryItem{stableKey: stableKey, fingerprint: e.contentFingerprint(normalized)}, workingBudget); err != nil {
					rows.Close()
					return source, err
				}
			} else {
				counts["excluded_material"]++
			}
		}
		if err := rows.Close(); err != nil {
			return source, err
		}
	}
	if tables["memory_capsules"] {
		count, err := countQuery(ctx, tx, "SELECT COUNT(*) FROM memory_capsules")
		if err != nil {
			return source, err
		}
		counts["structured_candidates"] += count
		if err := addRows(count); err != nil {
			return source, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT id, redacted_summary FROM memory_capsules ORDER BY id LIMIT ?", rowBudget+1)
		if err != nil {
			return source, err
		}
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				rows.Close()
				return source, err
			}
			var stableKey, content string
			if err := rows.Scan(&stableKey, &content); err != nil {
				rows.Close()
				return source, err
			}
			if normalized := normalizeContent(content); normalized != "" {
				if err := appendInventoryItem(&source, inventoryItem{stableKey: stableKey, fingerprint: e.contentFingerprint(normalized)}, workingBudget); err != nil {
					rows.Close()
					return source, err
				}
			}
		}
		if err := rows.Close(); err != nil {
			return source, err
		}
	}
	if tables["memory_tray_candidates"] {
		count, err := schemaCountQuery(ctx, tx, "SELECT COUNT(*) FROM memory_tray_candidates WHERE state IN ('pending','committing')")
		if err != nil {
			return source, err
		}
		counts["structured_candidates"] += count
	}
	if tables["private_memory_objects"] {
		activeObjects, err := schemaCountQuery(ctx, tx, "SELECT COUNT(*) FROM private_memory_objects WHERE lifecycle='active'")
		if err != nil {
			return source, err
		}
		counts["approved_canonical"] += activeObjects
		counts["retrieval_visible"] += counts["approved_canonical"]
		if tables["memory_tray_candidates"] {
			if err := addRows(activeObjects); err != nil {
				return source, err
			}
			rows, err := tx.QueryContext(ctx, `
				SELECT object.object_id, candidate.payload_json
				  FROM private_memory_objects object
				  JOIN memory_tray_candidates candidate ON candidate.candidate_id=object.created_from_candidate_id
				 WHERE object.lifecycle='active'
				 ORDER BY object.object_id LIMIT ?`, rowBudget+1)
			if err != nil {
				return source, err
			}
			for rows.Next() {
				if err := ctx.Err(); err != nil {
					rows.Close()
					return source, err
				}
				var stableKey, payload string
				if err := rows.Scan(&stableKey, &payload); err != nil {
					rows.Close()
					return source, err
				}
				if normalized := normalizeContent(memoryTextFromJSON(payload)); normalized != "" {
					if err := appendInventoryItem(&source, inventoryItem{stableKey: stableKey, fingerprint: e.contentFingerprint(normalized)}, workingBudget); err != nil {
						rows.Close()
						return source, err
					}
				}
			}
			if err := rows.Close(); err != nil {
				return source, err
			}
		}
	}
	if tables["extraction_jobs"] {
		count, err := schemaCountQuery(ctx, tx, "SELECT COUNT(*) FROM extraction_jobs WHERE state IN ('pending','running')")
		if err != nil {
			return source, err
		}
		counts["pending_extraction"] = count
	}
	for _, table := range []string{"continuity_observations", "continuity_checkpoints", "continuity_delivery_receipts"} {
		if tables[table] {
			count, err := schemaCountQuery(ctx, tx, "SELECT COUNT(*) FROM "+table)
			if err != nil {
				return source, err
			}
			counts["continuity_records"] += count
		}
	}
	for _, table := range []string{"entities", "relations", "facts", "events", "private_semantic_projection_rows"} {
		if tables[table] {
			count, err := schemaCountQuery(ctx, tx, "SELECT COUNT(*) FROM "+table)
			if err != nil {
				return source, err
			}
			counts["graph_projections"] += count
		}
	}
	for _, table := range []string{"event_embeddings", "entity_embeddings", "atomic_fact_embeddings"} {
		if tables[table] {
			count, err := schemaCountQuery(ctx, tx, "SELECT COUNT(*) FROM "+table)
			if err != nil {
				return source, err
			}
			counts["embeddings"] += count
		}
	}
	if source.canonical {
		source.reasonCode = "signed_bound_destination"
	} else {
		source.reasonCode = "recognized_pulse_schema"
	}
	return source, nil
}

func countQuery(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	var count int64
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	if count < 0 {
		return 0, errors.New("invalid_count")
	}
	return count, nil
}

func schemaCountQuery(ctx context.Context, tx *sql.Tx, query string) (int64, error) {
	count, err := countQuery(ctx, tx, query)
	if err != nil {
		return 0, fmt.Errorf("unsupported_schema: %w", err)
	}
	return count, nil
}

func normalizeContent(value string) string {
	value = norm.NFC.String(strings.ToLower(strings.TrimSpace(value)))
	return strings.Join(strings.Fields(value), " ")
}

func (e *Engine) contentFingerprint(normalized string) string {
	return e.manager.mac([]byte("normalized-content-v1\x1f" + normalized))
}

func memoryTextFromJSON(payload string) string {
	var value any
	if json.Unmarshal([]byte(payload), &value) != nil {
		return ""
	}
	var out []string
	collectMemoryText(value, &out)
	return strings.Join(out, " ")
}

func collectMemoryText(value any, out *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "redacted_summary", "summary", "subject", "predicate", "object", "fact", "decision", "open_loop", "do_not_repeat", "state_signal":
				if text, ok := child.(string); ok {
					*out = append(*out, text)
				}
			default:
				collectMemoryText(child, out)
			}
		}
	case []any:
		for _, child := range typed {
			collectMemoryText(child, out)
		}
	}
}

func safeInspectionReason(err error) string {
	if err == nil {
		return "inspection_failed"
	}
	message := strings.ToLower(err.Error())
	if errors.Is(err, context.DeadlineExceeded) {
		return "resource_limit"
	}
	for _, reason := range []string{
		"resource_limit", "unsafe_file_type", "owner_mismatch", "source_changed",
		"unsafe_wal", "active_wal", "source_locked", "read_only_unavailable", "integrity_failed", "unsupported_schema",
	} {
		if strings.Contains(message, reason) {
			return reason
		}
	}
	if strings.Contains(message, "locked") || strings.Contains(message, "busy") {
		return "source_locked"
	}
	if strings.Contains(message, "malformed") || strings.Contains(message, "not a database") {
		return "integrity_failed"
	}
	return "inspection_failed"
}
