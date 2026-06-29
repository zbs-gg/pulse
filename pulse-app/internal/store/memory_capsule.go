package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const MemoryCapsuleSchema = "pulse.memory_capsule.v1"

// Unicode-aware: tags/slugs may be Cyrillic etc. (RU users). Still excludes
// whitespace and path/secret-like content (the looksSensitiveOrPathLike guard
// runs separately). \p{L}=letters, \p{N}=numbers in any script.
var safeTagPattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._:-]{0,63}$`)

type CapsuleSource struct {
	Host              string `json:"host"`
	ConversationScope string `json:"conversation_scope"`
	Timestamp         string `json:"timestamp"`
}

type MemoryCapsuleItem struct {
	Kind            string   `json:"kind"`
	RedactedSummary string   `json:"redacted_summary"`
	Confidence      float64  `json:"confidence"`
	EvidenceHint    string   `json:"evidence_hint"`
	PrivacyTier     string   `json:"privacy_tier"`
	Retention       string   `json:"retention"`
	Tags            []string `json:"tags,omitempty"`
}

type MemoryCapsule struct {
	Schema           string              `json:"schema"`
	Source           CapsuleSource       `json:"source"`
	Items            []MemoryCapsuleItem `json:"items"`
	RawInputIncluded bool                `json:"raw_input_included"`
}

type RecallMemoryQuery struct {
	Query          string `json:"query"`
	Scope          string `json:"scope,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	PrivacyCeiling string `json:"privacy_ceiling,omitempty"`
}

type RecalledMemoryItem struct {
	ID          string   `json:"id"`
	Summary     string   `json:"summary"`
	Kind        string   `json:"kind"`
	Confidence  float64  `json:"confidence"`
	Source      string   `json:"source"`
	EvidenceRef string   `json:"evidence_ref,omitempty"`
	PrivacyTier string   `json:"privacy_tier"`
	Retention   string   `json:"retention"`
	Tags        []string `json:"tags,omitempty"`
	CreatedAt   string   `json:"created_at"`
}

type MemoryExport struct {
	Schema   string               `json:"schema"`
	Exported string               `json:"exported_at"`
	Items    []MemoryExportedItem `json:"items"`
}

type MemoryExportedItem struct {
	ID               string        `json:"id"`
	Schema           string        `json:"schema"`
	Source           CapsuleSource `json:"source"`
	Kind             string        `json:"kind"`
	RedactedSummary  string        `json:"redacted_summary"`
	Confidence       float64       `json:"confidence"`
	EvidenceHint     string        `json:"evidence_hint"`
	PrivacyTier      string        `json:"privacy_tier"`
	Retention        string        `json:"retention"`
	Tags             []string      `json:"tags,omitempty"`
	CreatedAt        string        `json:"created_at"`
	RawInputIncluded bool          `json:"raw_input_included"`
}

type MemoryStoreStatus struct {
	ItemCount int    `json:"item_count"`
	LastWrite string `json:"last_write,omitempty"`
}

func (s *Store) RememberCapsule(capsule MemoryCapsule) ([]string, error) {
	if err := validateMemoryCapsule(capsule); err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	ids := make([]string, 0, len(capsule.Items))
	createdAt := time.Now().UTC().Format(time.RFC3339)
	for i, item := range capsule.Items {
		id, err := newMemoryID(i)
		if err != nil {
			return nil, err
		}
		tags, err := json.Marshal(item.Tags)
		if err != nil {
			return nil, fmt.Errorf("marshal tags: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO memory_capsules
			  (id, schema_version, source_host, conversation_scope, source_timestamp,
			   kind, redacted_summary, confidence, evidence_hint, privacy_tier,
			   retention, tags, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, capsule.Schema, capsule.Source.Host, capsule.Source.ConversationScope,
			capsule.Source.Timestamp, item.Kind, item.RedactedSummary, item.Confidence,
			item.EvidenceHint, item.PrivacyTier, item.Retention, string(tags), createdAt,
		); err != nil {
			return nil, fmt.Errorf("insert memory capsule: %w", err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) RecallMemory(q RecallMemoryQuery) ([]RecalledMemoryItem, error) {
	query := strings.TrimSpace(q.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 5
	} else if limit > 50 {
		limit = 50
	}
	ceiling := strings.TrimSpace(q.PrivacyCeiling)
	if ceiling == "" {
		ceiling = "sensitive"
	}
	retention := retentionFilter(q.Scope)

	rows, err := s.db.Query(`
		SELECT id, redacted_summary, kind, confidence, privacy_tier, retention, tags, created_at
		  FROM memory_capsules
		 WHERE redacted_summary LIKE ?
		   AND privacy_tier IN (`+privacyPlaceholders(ceiling)+`)
		   AND (? = '' OR retention = ?)
		 ORDER BY created_at DESC
		 LIMIT ?`,
		append([]any{"%" + query + "%"}, append(privacyArgs(ceiling), retention, retention, limit)...)...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RecalledMemoryItem{}
	for rows.Next() {
		item, err := scanRecalledMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		return out, nil
	}

	return s.recallMemoryFallback(query, ceiling, retention, limit)
}

func (s *Store) recallMemoryFallback(query, ceiling, retention string, limit int) ([]RecalledMemoryItem, error) {
	terms := significantTerms(query)
	if len(terms) == 0 {
		return []RecalledMemoryItem{}, nil
	}
	where := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms)+6)
	for _, term := range terms {
		where = append(where, "redacted_summary LIKE ?")
		args = append(args, "%"+term+"%")
	}
	args = append(args, privacyArgs(ceiling)...)
	args = append(args, retention, retention, limit)

	rows, err := s.db.Query(`
		SELECT id, redacted_summary, kind, confidence, privacy_tier, retention, tags, created_at
		  FROM memory_capsules
		 WHERE (`+strings.Join(where, " OR ")+`)
		   AND privacy_tier IN (`+privacyPlaceholders(ceiling)+`)
		   AND (? = '' OR retention = ?)
		 ORDER BY created_at DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RecalledMemoryItem{}
	for rows.Next() {
		item, err := scanRecalledMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ExportMemory() (MemoryExport, error) {
	rows, err := s.db.Query(`
		SELECT id, schema_version, source_host, conversation_scope, source_timestamp,
		       kind, redacted_summary, confidence, evidence_hint, privacy_tier,
		       retention, tags, created_at
		  FROM memory_capsules
		 ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return MemoryExport{}, err
	}
	defer rows.Close()

	out := MemoryExport{
		Schema:   MemoryCapsuleSchema,
		Exported: time.Now().UTC().Format(time.RFC3339),
		Items:    []MemoryExportedItem{},
	}
	for rows.Next() {
		var item MemoryExportedItem
		var tags string
		if err := rows.Scan(
			&item.ID, &item.Schema, &item.Source.Host, &item.Source.ConversationScope,
			&item.Source.Timestamp, &item.Kind, &item.RedactedSummary, &item.Confidence,
			&item.EvidenceHint, &item.PrivacyTier, &item.Retention, &tags, &item.CreatedAt,
		); err != nil {
			return MemoryExport{}, err
		}
		_ = json.Unmarshal([]byte(tags), &item.Tags)
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}

func (s *Store) ImportMemory(in MemoryExport) ([]string, error) {
	ids := make([]string, 0, len(in.Items))
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, item := range in.Items {
		if item.ID == "" {
			return nil, fmt.Errorf("item id is required")
		}
		capsule := MemoryCapsule{
			Schema: item.Schema,
			Source: item.Source,
			Items: []MemoryCapsuleItem{{
				Kind:            item.Kind,
				RedactedSummary: item.RedactedSummary,
				Confidence:      item.Confidence,
				EvidenceHint:    item.EvidenceHint,
				PrivacyTier:     item.PrivacyTier,
				Retention:       item.Retention,
				Tags:            item.Tags,
			}},
			RawInputIncluded: item.RawInputIncluded,
		}
		if err := validateMemoryCapsule(capsule); err != nil {
			return nil, err
		}
		tags, err := json.Marshal(item.Tags)
		if err != nil {
			return nil, err
		}
		createdAt := item.CreatedAt
		if createdAt == "" {
			createdAt = time.Now().UTC().Format(time.RFC3339)
		}
		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO memory_capsules
			  (id, schema_version, source_host, conversation_scope, source_timestamp,
			   kind, redacted_summary, confidence, evidence_hint, privacy_tier,
			   retention, tags, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ID, item.Schema, item.Source.Host, item.Source.ConversationScope,
			item.Source.Timestamp, item.Kind, item.RedactedSummary, item.Confidence,
			item.EvidenceHint, item.PrivacyTier, item.Retention, string(tags), createdAt,
		); err != nil {
			return nil, err
		}
		ids = append(ids, item.ID)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) DeleteMemory(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}
	_, err := s.db.Exec(`DELETE FROM memory_capsules WHERE id=?`, id)
	return err
}

func (s *Store) WipeMemory() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM memory_capsules`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		DELETE FROM continuity_observations;
		DELETE FROM continuity_checkpoints;
		DELETE FROM continuity_sessions;
		DELETE FROM continuity_threads;`); err != nil {
		return err
	}
	if err := wipeHostExtractedGraph(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func wipeHostExtractedGraph(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		DELETE FROM event_entities
		 WHERE event_id IN (
		       SELECT id FROM events WHERE scorer_version='host-extracted'
		 )
		    OR entity_id IN (
		       SELECT id FROM entities WHERE scorer_version='host-extracted'
		 );`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		DELETE FROM facts
		 WHERE scorer_version='host-extracted'
		    OR extractor_version='pulse.semantic_delta.v1';`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		DELETE FROM relations
		 WHERE from_entity_id IN (
		       SELECT id FROM entities WHERE scorer_version='host-extracted'
		 )
		    OR to_entity_id IN (
		       SELECT id FROM entities WHERE scorer_version='host-extracted'
		 );`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM events WHERE scorer_version='host-extracted'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM entities WHERE scorer_version='host-extracted'`); err != nil {
		return err
	}
	return nil
}

func (s *Store) MemoryStatus() (MemoryStoreStatus, error) {
	var status MemoryStoreStatus
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(created_at), '') FROM memory_capsules`).Scan(&status.ItemCount, &status.LastWrite); err != nil {
		return status, err
	}
	return status, nil
}

func validateMemoryCapsule(capsule MemoryCapsule) error {
	if capsule.Schema != MemoryCapsuleSchema {
		return fmt.Errorf("schema must be %s", MemoryCapsuleSchema)
	}
	if capsule.RawInputIncluded {
		return fmt.Errorf("raw_input_included must be false")
	}
	if !validHost(capsule.Source.Host) {
		return fmt.Errorf("source.host is unsupported or missing")
	}
	if !validConversationScope(capsule.Source.ConversationScope) {
		return fmt.Errorf("source.conversation_scope is unsupported or missing")
	}
	if strings.TrimSpace(capsule.Source.Timestamp) == "" {
		return fmt.Errorf("source.timestamp is required")
	}
	if _, err := time.Parse(time.RFC3339, capsule.Source.Timestamp); err != nil {
		return fmt.Errorf("source.timestamp must be RFC3339")
	}
	if len(capsule.Items) == 0 {
		return fmt.Errorf("items are required")
	}
	if len(capsule.Items) > 20 {
		return fmt.Errorf("too many items: max 20")
	}
	for i, item := range capsule.Items {
		if !validKind(item.Kind) {
			return fmt.Errorf("items[%d].kind is unsupported or missing", i)
		}
		summary := strings.TrimSpace(item.RedactedSummary)
		if summary == "" {
			return fmt.Errorf("items[%d].redacted_summary is required", i)
		}
		if len(summary) > 1200 {
			return fmt.Errorf("items[%d].redacted_summary is too long", i)
		}
		if looksLikeTranscript(summary) {
			return fmt.Errorf("items[%d].redacted_summary looks like raw transcript", i)
		}
		if looksSensitiveOrPathLike(summary) {
			return fmt.Errorf("items[%d].redacted_summary contains secret/path-like text", i)
		}
		if item.Confidence < 0 || item.Confidence > 1 {
			return fmt.Errorf("items[%d].confidence must be 0..1", i)
		}
		if !validEvidenceHint(item.EvidenceHint) {
			return fmt.Errorf("items[%d].evidence_hint is unsupported or missing", i)
		}
		if !validPrivacyTier(item.PrivacyTier) {
			return fmt.Errorf("items[%d].privacy_tier is unsupported or missing", i)
		}
		if !validRetention(item.Retention) {
			return fmt.Errorf("items[%d].retention is unsupported or missing", i)
		}
		for j, tag := range item.Tags {
			if !validMemoryTag(tag) {
				return fmt.Errorf("items[%d].tags[%d] is unsafe", i, j)
			}
		}
	}
	return nil
}

func scanRecalledMemory(rows *sql.Rows) (RecalledMemoryItem, error) {
	var item RecalledMemoryItem
	var tags string
	if err := rows.Scan(&item.ID, &item.Summary, &item.Kind, &item.Confidence, &item.PrivacyTier, &item.Retention, &tags, &item.CreatedAt); err != nil {
		return item, err
	}
	_ = json.Unmarshal([]byte(tags), &item.Tags)
	item.Source = "pulse"
	item.EvidenceRef = "pulse:" + item.ID
	return item, nil
}

func newMemoryID(index int) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("pulse:%d:%d:%s", time.Now().UTC().UnixNano(), index, hex.EncodeToString(b[:])), nil
}

func validHost(host string) bool {
	switch host {
	case "chatgpt", "claude", "codex", "claude-code", "gemini-cli", "cursor", "langchain", "crewai", "pulse-cli":
		return true
	default:
		return false
	}
}

func validConversationScope(scope string) bool {
	switch scope {
	case "current_turn", "user_selected_excerpt", "project_context", "install_event":
		return true
	default:
		return false
	}
}

func validKind(kind string) bool {
	switch kind {
	case "fact", "decision", "preference", "project_state", "open_loop", "correction", "relationship_note", "do_not_repeat", "system_event", "state_signal":
		return true
	default:
		return false
	}
}

func validEvidenceHint(hint string) bool {
	switch hint {
	case "user_selected", "current_turn", "assistant_inferred", "tool_result", "user_confirmed":
		return true
	default:
		return false
	}
}

func validPrivacyTier(tier string) bool {
	switch tier {
	case "normal", "sensitive", "private":
		return true
	default:
		return false
	}
}

func validRetention(retention string) bool {
	switch retention {
	case "session", "project", "long_term":
		return true
	default:
		return false
	}
}

func looksLikeTranscript(text string) bool {
	lower := strings.ToLower(text)
	return strings.Count(lower, "user:") >= 3 ||
		strings.Count(lower, "assistant:") >= 3 ||
		strings.Count(lower, "\n") > 30
}

func looksSensitiveOrPathLike(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"/users/",
		"file://",
		"token=",
		"api_key",
		"apikey",
		"password",
		"secret",
		"private_key",
		"begin private key",
		"sk-",
		"akia",
		"xoxb-",
		"ghp_",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func validMemoryTag(tag string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" || len(tag) > 64 {
		return false
	}
	if looksSensitiveOrPathLike(tag) {
		return false
	}
	return safeTagPattern.MatchString(tag)
}

func privacyArgs(ceiling string) []any {
	switch ceiling {
	case "normal":
		return []any{"normal"}
	case "private":
		return []any{"normal", "sensitive", "private"}
	default:
		return []any{"normal", "sensitive"}
	}
}

func privacyPlaceholders(ceiling string) string {
	return strings.TrimRight(strings.Repeat("?,", len(privacyArgs(ceiling))), ",")
}

func retentionFilter(scope string) string {
	switch scope {
	case "session", "project":
		return scope
	default:
		return ""
	}
}

func significantTerms(query string) []string {
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && !(r >= 'а' && r <= 'я')
	})
	out := []string{}
	seen := map[string]bool{}
	for _, w := range words {
		if len(w) < 4 || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}
