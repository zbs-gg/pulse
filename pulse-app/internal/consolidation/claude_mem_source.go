package consolidation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	claudeMemAdapterManifest = "claude_mem_schema_21_32_v1"
	claudeMemMinSchema       = 21
	claudeMemMaxSchema       = 32
)

func (e *Engine) inspectClaudeMemSource(
	ctx context.Context,
	tx *sql.Tx,
	source inspectedSource,
	tables map[string]bool,
) (inspectedSource, error) {
	var schemaVersion int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_versions").Scan(&schemaVersion); err != nil {
		return source, errors.New("unsupported_schema")
	}
	if schemaVersion < claudeMemMinSchema || schemaVersion > claudeMemMaxSchema {
		return source, errors.New("unsupported_schema")
	}
	observationColumns, err := tableColumns(ctx, tx, "observations")
	if err != nil || !hasColumns(observationColumns, "id", "project") {
		return source, errors.New("unsupported_schema")
	}
	summaryColumns, err := tableColumns(ctx, tx, "session_summaries")
	if err != nil || !hasColumns(summaryColumns, "id", "project") {
		return source, errors.New("unsupported_schema")
	}
	observationFields := existingColumns(observationColumns, []string{
		"text", "title", "subtitle", "narrative", "facts", "concepts",
	})
	summaryFields := existingColumns(summaryColumns, []string{
		"request", "investigated", "learned", "completed", "next_steps", "notes",
	})
	if len(observationFields) == 0 || len(summaryFields) == 0 {
		return source, errors.New("unsupported_schema")
	}

	observationCount, err := countQuery(ctx, tx, "SELECT COUNT(*) FROM observations")
	if err != nil || observationCount > e.limits.MaxRowsPerSource {
		return source, errors.New("resource_limit")
	}
	summaryCount, err := countQuery(ctx, tx, "SELECT COUNT(*) FROM session_summaries")
	if err != nil || observationCount+summaryCount > e.limits.MaxRowsPerSource {
		return source, errors.New("resource_limit")
	}
	source.counts["source_rows"] = observationCount + summaryCount
	source.counts["structured_candidates"] = observationCount + summaryCount
	if tables["user_prompts"] {
		source.counts["excluded_material"] += mustCountOrZero(ctx, tx, "SELECT COUNT(*) FROM user_prompts")
	}
	if tables["pending_messages"] {
		source.counts["pending_extraction"] += mustCountOrZero(
			ctx, tx, "SELECT COUNT(*) FROM pending_messages WHERE status IN ('pending','processing')",
		)
	}
	for table := range tables {
		lower := strings.ToLower(table)
		if strings.Contains(lower, "fts") || strings.Contains(lower, "chroma") {
			source.counts["excluded_material"]++
		}
	}

	items, err := e.readClaudeMemItems(ctx, tx, "observations", "obs", observationFields)
	if err != nil {
		return source, err
	}
	source.items = append(source.items, items...)
	items, err = e.readClaudeMemItems(ctx, tx, "session_summaries", "summary", summaryFields)
	if err != nil {
		return source, err
	}
	source.items = append(source.items, items...)
	source.classification = ClassificationClaudeMem
	source.reasonCode = claudeMemAdapterManifest
	return source, nil
}

func (e *Engine) readClaudeMemItems(
	ctx context.Context,
	tx *sql.Tx,
	table, stableKind string,
	contentColumns []string,
) ([]inventoryItem, error) {
	selectColumns := append([]string{"id", "project"}, contentColumns...)
	query := fmt.Sprintf("SELECT %s FROM %s ORDER BY id LIMIT ?", strings.Join(selectColumns, ", "), table)
	rows, err := tx.QueryContext(ctx, query, e.limits.MaxRowsPerSource+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]inventoryItem, 0)
	for rows.Next() {
		values := make([]sql.NullString, len(selectColumns))
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		id := values[0].String
		project := values[1].String
		parts := make([]string, 0, len(values)-2)
		for _, value := range values[2:] {
			if value.Valid && strings.TrimSpace(value.String) != "" {
				parts = append(parts, value.String)
			}
		}
		normalized := normalizeContent(strings.Join(parts, " "))
		if id == "" || normalized == "" {
			continue
		}
		items = append(items, inventoryItem{
			stableKey:   "claude-mem:" + stableKind + ":" + id,
			fingerprint: e.contentFingerprint(normalized),
			projectKey:  e.manager.mac([]byte("project-v1\x1f" + normalizeContent(project))),
		})
	}
	return items, rows.Err()
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func hasColumns(actual map[string]bool, required ...string) bool {
	for _, column := range required {
		if !actual[column] {
			return false
		}
	}
	return true
}

func existingColumns(actual map[string]bool, candidates []string) []string {
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if actual[candidate] {
			result = append(result, candidate)
		}
	}
	sort.Strings(result)
	return result
}
