package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const SemanticDeltaSchema = "pulse.semantic_delta.v1"

var semanticRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{1,95}$`)

type SemanticDelta struct {
	Schema           string              `json:"schema"`
	Source           SemanticDeltaSource `json:"source"`
	Nodes            []SemanticNode      `json:"nodes,omitempty"`
	Edges            []SemanticEdge      `json:"edges,omitempty"`
	Facts            []SemanticFact      `json:"facts,omitempty"`
	Events           []SemanticEvent     `json:"events,omitempty"`
	Continuity       *SemanticContinuity `json:"continuity,omitempty"`
	RawInputIncluded bool                `json:"raw_input_included"`
}

type SemanticDeltaSource struct {
	Host              string `json:"host"`
	ConversationScope string `json:"conversation_scope"`
	Timestamp         string `json:"timestamp"`
	ThreadID          string `json:"thread_id,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	ProjectID         string `json:"project_id,omitempty"`
}

type SemanticNode struct {
	ClientID        string   `json:"client_id"`
	Kind            string   `json:"kind"`
	CanonicalName   string   `json:"canonical_name"`
	Summary         string   `json:"summary,omitempty"`
	Aliases         []string `json:"aliases,omitempty"`
	Salience        float64  `json:"salience,omitempty"`
	EmotionalWeight float64  `json:"emotional_weight,omitempty"`
	PrivacyTier     string   `json:"privacy_tier"`
	Domain          string   `json:"domain,omitempty"`
}

type SemanticEdge struct {
	From        string  `json:"from"`
	To          string  `json:"to"`
	Kind        string  `json:"kind"`
	Summary     string  `json:"summary,omitempty"`
	Strength    float64 `json:"strength,omitempty"`
	PrivacyTier string  `json:"privacy_tier"`
}

type SemanticFact struct {
	Node        string  `json:"node"`
	Text        string  `json:"text"`
	Confidence  float64 `json:"confidence"`
	PrivacyTier string  `json:"privacy_tier"`
	Domain      string  `json:"domain,omitempty"`
}

type SemanticEvent struct {
	ClientID        string   `json:"client_id"`
	Title           string   `json:"title"`
	Summary         string   `json:"summary"`
	EntityRefs      []string `json:"entity_refs,omitempty"`
	Sentiment       string   `json:"sentiment,omitempty"`
	EmotionalWeight float64  `json:"emotional_weight,omitempty"`
	Confidence      float64  `json:"confidence"`
	PrivacyTier     string   `json:"privacy_tier"`
	Domain          string   `json:"domain,omitempty"`
}

type SemanticContinuity struct {
	Summary          string   `json:"summary"`
	Decisions        []string `json:"decisions,omitempty"`
	OpenLoops        []string `json:"open_loops,omitempty"`
	DoNotRepeat      []string `json:"do_not_repeat,omitempty"`
	EmotionalAnchors []string `json:"emotional_anchors,omitempty"`
	StateSignals     []string `json:"state_signals,omitempty"`
	ActiveThreads    []string `json:"active_threads,omitempty"`
	ReviewInsights   []string `json:"review_insights,omitempty"`
}

type SemanticDeltaResult struct {
	OK              bool    `json:"ok"`
	NodesUpserted   int     `json:"nodes_upserted"`
	EdgesUpserted   int     `json:"edges_upserted"`
	FactsUpserted   int     `json:"facts_upserted"`
	EventsInserted  int     `json:"events_inserted"`
	EventIDs        []int64 `json:"event_ids,omitempty"`
	CheckpointSaved bool    `json:"checkpoint_saved"`
}

func (s *Store) SaveSemanticDelta(delta SemanticDelta) (SemanticDeltaResult, error) {
	if err := validateSemanticDelta(delta); err != nil {
		return SemanticDeltaResult{}, err
	}
	now := delta.Source.Timestamp
	result := SemanticDeltaResult{OK: true}
	tx, err := s.db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	nodeIDs := make(map[string]int64, len(delta.Nodes))
	for _, node := range delta.Nodes {
		id, err := upsertSemanticNode(tx, node, now)
		if err != nil {
			return result, err
		}
		nodeIDs[node.ClientID] = id
		result.NodesUpserted++
	}
	for _, edge := range delta.Edges {
		if err := upsertSemanticRelation(tx, nodeIDs, edge, now); err != nil {
			return result, err
		}
		result.EdgesUpserted++
	}
	for _, fact := range delta.Facts {
		if err := upsertSemanticFact(tx, nodeIDs, fact, now); err != nil {
			return result, err
		}
		result.FactsUpserted++
	}
	for _, event := range delta.Events {
		id, err := insertSemanticEvent(tx, nodeIDs, event, now)
		if err != nil {
			return result, err
		}
		result.EventIDs = append(result.EventIDs, id)
		result.EventsInserted++
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}

	if delta.Continuity != nil {
		threadID := normalizeThreadID(delta.Source.ThreadID, delta.Source.ProjectID, delta.Source.SessionID)
		sessionID := strings.TrimSpace(delta.Source.SessionID)
		if sessionID == "" {
			sessionID = threadID + ":semantic-delta"
		}
		err := s.SaveCheckpoint(ContinuityCheckpoint{
			ThreadID:         threadID,
			SessionID:        sessionID,
			Host:             delta.Source.Host,
			ProjectID:        delta.Source.ProjectID,
			Summary:          delta.Continuity.Summary,
			Decisions:        delta.Continuity.Decisions,
			OpenLoops:        delta.Continuity.OpenLoops,
			DoNotRepeat:      delta.Continuity.DoNotRepeat,
			EmotionalAnchors: delta.Continuity.EmotionalAnchors,
			StateSignals:     delta.Continuity.StateSignals,
			ActiveThreads:    delta.Continuity.ActiveThreads,
			ReviewInsights:   delta.Continuity.ReviewInsights,
			SourceRefs:       []string{fmt.Sprintf("pulse:semantic_delta:%d", time.Now().UTC().UnixNano())},
			Confidence:       0.8,
			CreatedAt:        now,
		})
		if err != nil {
			return result, err
		}
		result.CheckpointSaved = true
	}
	return result, nil
}

func upsertSemanticNode(tx *sql.Tx, node SemanticNode, now string) (int64, error) {
	id, canonicalName, existingAliases, err := findSemanticEntity(tx, node)
	if err == sql.ErrNoRows {
		aliases, _ := json.Marshal(cleanSemanticAliases(node.CanonicalName, node.Aliases))
		res, err := tx.Exec(`
			INSERT INTO entities
			  (canonical_name, kind, aliases, first_seen, last_seen, salience_score,
			   emotional_weight, scorer_version, description_md, extractor_version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			node.CanonicalName, node.Kind, string(aliases), now, now, node.Salience,
			node.EmotionalWeight, "host-extracted", node.Summary, "pulse.semantic_delta.v1")
		if err != nil {
			return 0, fmt.Errorf("insert semantic node %q: %w", node.ClientID, err)
		}
		return res.LastInsertId()
	}
	if err != nil {
		return 0, fmt.Errorf("select semantic node %q: %w", node.ClientID, err)
	}
	mergedAliases := mergeSemanticAliases(canonicalName, existingAliases, append(node.Aliases, node.CanonicalName))
	aliases, _ := json.Marshal(mergedAliases)
	if _, err := tx.Exec(`
		UPDATE entities
		   SET aliases = ?,
		       last_seen = ?,
		       salience_score = MAX(salience_score, ?),
		       emotional_weight = MAX(emotional_weight, ?),
		       description_md = COALESCE(NULLIF(?, ''), description_md),
		       scorer_version = 'host-extracted'
		 WHERE id = ?`,
		string(aliases), now, node.Salience, node.EmotionalWeight, node.Summary, id); err != nil {
		return 0, fmt.Errorf("update semantic node %q: %w", node.ClientID, err)
	}
	return id, nil
}

func findSemanticEntity(tx *sql.Tx, node SemanticNode) (int64, string, []string, error) {
	nodeKeys := semanticEntityKeys(node.Kind, node.CanonicalName, node.Aliases)
	rows, err := tx.Query(`
		SELECT id, canonical_name, COALESCE(aliases, '[]')
		  FROM entities
		 WHERE kind = ?
		 ORDER BY salience_score DESC, last_seen DESC, id ASC`, node.Kind)
	if err != nil {
		return 0, "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var canonicalName string
		var aliasesJSON string
		if err := rows.Scan(&id, &canonicalName, &aliasesJSON); err != nil {
			return 0, "", nil, err
		}
		aliases := parseSemanticAliases(aliasesJSON)
		if semanticKeysOverlap(nodeKeys, semanticEntityKeys(node.Kind, canonicalName, aliases)) {
			return id, canonicalName, aliases, nil
		}
	}
	if err := rows.Err(); err != nil {
		return 0, "", nil, err
	}
	return 0, "", nil, sql.ErrNoRows
}

func semanticEntityKeys(kind, canonicalName string, aliases []string) map[string]bool {
	keys := map[string]bool{}
	for _, value := range append([]string{canonicalName}, aliases...) {
		key := semanticEntityKey(kind, value)
		if key != "" {
			keys[key] = true
		}
	}
	return keys
}

func semanticEntityKey(kind, value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'а' && r <= 'я':
			b.WriteRune(r)
		case r == 'ё':
			b.WriteRune('е')
		}
	}
	normalized := b.String()
	if normalized == "" {
		return ""
	}
	return kind + ":" + normalized
}

func semanticKeysOverlap(left, right map[string]bool) bool {
	for key := range left {
		if right[key] {
			return true
		}
	}
	return false
}

func parseSemanticAliases(raw string) []string {
	var aliases []string
	if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
		return nil
	}
	return aliases
}

func cleanSemanticAliases(canonicalName string, aliases []string) []string {
	return mergeSemanticAliases(canonicalName, nil, aliases)
}

func mergeSemanticAliases(canonicalName string, groups ...[]string) []string {
	out := []string{}
	seen := map[string]bool{semanticEntityKey("", canonicalName): true}
	for _, group := range groups {
		for _, alias := range group {
			alias = strings.TrimSpace(alias)
			key := semanticEntityKey("", alias)
			if alias == "" || key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, alias)
		}
	}
	return out
}

func upsertSemanticRelation(tx *sql.Tx, ids map[string]int64, edge SemanticEdge, now string) error {
	fromID, ok := ids[edge.From]
	if !ok {
		return fmt.Errorf("edge.from references unknown node %q", edge.From)
	}
	toID, ok := ids[edge.To]
	if !ok {
		return fmt.Errorf("edge.to references unknown node %q", edge.To)
	}
	_, err := tx.Exec(`
		INSERT INTO relations
		  (from_entity_id, to_entity_id, kind, strength, first_seen, last_seen, context)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(from_entity_id, to_entity_id, kind) DO UPDATE SET
		  strength = MAX(relations.strength, excluded.strength),
		  last_seen = excluded.last_seen,
		  context = COALESCE(NULLIF(excluded.context, ''), relations.context)`,
		fromID, toID, edge.Kind, edge.Strength, now, now, nullableString(edge.Summary))
	if err != nil {
		return fmt.Errorf("upsert semantic relation %q->%q: %w", edge.From, edge.To, err)
	}
	return nil
}

func upsertSemanticFact(tx *sql.Tx, ids map[string]int64, fact SemanticFact, now string) error {
	entityID, ok := ids[fact.Node]
	if !ok {
		return fmt.Errorf("fact.node references unknown node %q", fact.Node)
	}
	_, err := tx.Exec(`
		INSERT INTO facts
		  (entity_id, text, confidence, scorer_version, created_at, verified,
		   extractor_version, belief_class, confidence_floor, provenance, domain)
		VALUES (?, ?, ?, ?, ?, 0, ?, 'operational', 0, 'interactive_memory', ?)
		ON CONFLICT(entity_id, text) DO UPDATE SET
		  confidence = MAX(facts.confidence, excluded.confidence),
		  scorer_version = excluded.scorer_version,
		  domain = excluded.domain`,
		entityID, fact.Text, fact.Confidence, "host-extracted", now,
		"pulse.semantic_delta.v1", normalizeDomain(fact.Domain))
	if err != nil {
		return fmt.Errorf("upsert semantic fact %q: %w", fact.Node, err)
	}
	return nil
}

func insertSemanticEvent(tx *sql.Tx, ids map[string]int64, event SemanticEvent, now string) (int64, error) {
	res, err := tx.Exec(`
		INSERT INTO events
		  (title, description, sentiment, emotional_weight, scorer_version, ts,
		   belief_class, confidence_floor, provenance, domain)
		VALUES (?, ?, ?, ?, ?, ?, 'operational', ?, 'interactive_memory', ?)`,
		event.Title, event.Summary, normalizeSemanticOptional(event.Sentiment), event.EmotionalWeight, "host-extracted", now,
		event.Confidence, normalizeDomain(event.Domain))
	if err != nil {
		return 0, fmt.Errorf("insert semantic event %q: %w", event.ClientID, err)
	}
	eventID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, ref := range event.EntityRefs {
		entityID, ok := ids[ref]
		if !ok {
			return 0, fmt.Errorf("event.entity_refs references unknown node %q", ref)
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO event_entities(event_id, entity_id)
			VALUES (?, ?)`, eventID, entityID); err != nil {
			return 0, fmt.Errorf("insert event entity ref %q: %w", ref, err)
		}
	}
	return eventID, nil
}

func validateSemanticDelta(delta SemanticDelta) error {
	if delta.Schema != SemanticDeltaSchema {
		return fmt.Errorf("schema must be %s", SemanticDeltaSchema)
	}
	if delta.RawInputIncluded {
		return fmt.Errorf("raw_input_included must be false")
	}
	if !validHost(delta.Source.Host) {
		return fmt.Errorf("source.host is unsupported or missing")
	}
	if !validConversationScope(delta.Source.ConversationScope) {
		return fmt.Errorf("source.conversation_scope is unsupported or missing")
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(delta.Source.Timestamp)); err != nil {
		return fmt.Errorf("source.timestamp must be RFC3339")
	}
	if len(delta.Nodes) == 0 && len(delta.Edges) == 0 && len(delta.Facts) == 0 && len(delta.Events) == 0 && delta.Continuity == nil {
		return fmt.Errorf("semantic delta must include graph content or continuity")
	}
	if len(delta.Nodes) > 30 {
		return fmt.Errorf("nodes has too many items: max 30")
	}
	if len(delta.Edges) > 50 {
		return fmt.Errorf("edges has too many items: max 50")
	}
	if len(delta.Facts) > 50 {
		return fmt.Errorf("facts has too many items: max 50")
	}
	if len(delta.Events) > 20 {
		return fmt.Errorf("events has too many items: max 20")
	}
	refs := map[string]bool{}
	for i, node := range delta.Nodes {
		if err := validateSemanticNode(i, node); err != nil {
			return err
		}
		if refs[node.ClientID] {
			return fmt.Errorf("nodes[%d].client_id is duplicate", i)
		}
		refs[node.ClientID] = true
	}
	for i, edge := range delta.Edges {
		if err := validateSemanticEdge(i, edge, refs); err != nil {
			return err
		}
	}
	for i, fact := range delta.Facts {
		if err := validateSemanticFact(i, fact, refs); err != nil {
			return err
		}
	}
	for i, event := range delta.Events {
		if err := validateSemanticEvent(i, event, refs); err != nil {
			return err
		}
	}
	if delta.Continuity != nil {
		if err := validateSemanticContinuity(*delta.Continuity); err != nil {
			return err
		}
	}
	return nil
}

func validateSemanticNode(i int, node SemanticNode) error {
	if !validSemanticRef(node.ClientID) {
		return fmt.Errorf("nodes[%d].client_id is unsafe", i)
	}
	if !validSemanticEntityKind(node.Kind) {
		return fmt.Errorf("nodes[%d].kind is unsupported or missing", i)
	}
	if err := validateSemanticText(fmt.Sprintf("nodes[%d].canonical_name", i), node.CanonicalName, 160, true); err != nil {
		return err
	}
	if err := validateSemanticText(fmt.Sprintf("nodes[%d].summary", i), node.Summary, 1200, false); err != nil {
		return err
	}
	if !validPrivacyTier(node.PrivacyTier) {
		return fmt.Errorf("nodes[%d].privacy_tier is unsupported or missing", i)
	}
	if !validDomain(node.Domain) {
		return fmt.Errorf("nodes[%d].domain is unsupported", i)
	}
	if node.Salience < 0 || node.Salience > 1 {
		return fmt.Errorf("nodes[%d].salience must be 0..1", i)
	}
	if node.EmotionalWeight < 0 || node.EmotionalWeight > 1 {
		return fmt.Errorf("nodes[%d].emotional_weight must be 0..1", i)
	}
	for j, alias := range node.Aliases {
		if err := validateSemanticText(fmt.Sprintf("nodes[%d].aliases[%d]", i, j), alias, 160, true); err != nil {
			return err
		}
	}
	return nil
}

func validateSemanticEdge(i int, edge SemanticEdge, refs map[string]bool) error {
	if !refs[edge.From] {
		return fmt.Errorf("edges[%d].from references unknown node", i)
	}
	if !refs[edge.To] {
		return fmt.Errorf("edges[%d].to references unknown node", i)
	}
	if !validSemanticSlug(edge.Kind) {
		return fmt.Errorf("edges[%d].kind is unsafe", i)
	}
	if err := validateSemanticText(fmt.Sprintf("edges[%d].summary", i), edge.Summary, 1200, false); err != nil {
		return err
	}
	if !validPrivacyTier(edge.PrivacyTier) {
		return fmt.Errorf("edges[%d].privacy_tier is unsupported or missing", i)
	}
	if edge.Strength < 0 || edge.Strength > 1 {
		return fmt.Errorf("edges[%d].strength must be 0..1", i)
	}
	return nil
}

func validateSemanticFact(i int, fact SemanticFact, refs map[string]bool) error {
	if !refs[fact.Node] {
		return fmt.Errorf("facts[%d].node references unknown node", i)
	}
	if err := validateSemanticText(fmt.Sprintf("facts[%d].text", i), fact.Text, 1200, true); err != nil {
		return err
	}
	if fact.Confidence < 0 || fact.Confidence > 1 {
		return fmt.Errorf("facts[%d].confidence must be 0..1", i)
	}
	if !validPrivacyTier(fact.PrivacyTier) {
		return fmt.Errorf("facts[%d].privacy_tier is unsupported or missing", i)
	}
	if !validDomain(fact.Domain) {
		return fmt.Errorf("facts[%d].domain is unsupported", i)
	}
	return nil
}

func validateSemanticEvent(i int, event SemanticEvent, refs map[string]bool) error {
	if !validSemanticRef(event.ClientID) {
		return fmt.Errorf("events[%d].client_id is unsafe", i)
	}
	if err := validateSemanticText(fmt.Sprintf("events[%d].title", i), event.Title, 180, true); err != nil {
		return err
	}
	if err := validateSemanticText(fmt.Sprintf("events[%d].summary", i), event.Summary, 1200, true); err != nil {
		return err
	}
	if err := validateSemanticText(fmt.Sprintf("events[%d].sentiment", i), event.Sentiment, 240, false); err != nil {
		return err
	}
	for j, ref := range event.EntityRefs {
		if !refs[ref] {
			return fmt.Errorf("events[%d].entity_refs[%d] references unknown node", i, j)
		}
	}
	if event.EmotionalWeight < 0 || event.EmotionalWeight > 1 {
		return fmt.Errorf("events[%d].emotional_weight must be 0..1", i)
	}
	if event.Confidence < 0 || event.Confidence > 1 {
		return fmt.Errorf("events[%d].confidence must be 0..1", i)
	}
	if !validPrivacyTier(event.PrivacyTier) {
		return fmt.Errorf("events[%d].privacy_tier is unsupported or missing", i)
	}
	if !validDomain(event.Domain) {
		return fmt.Errorf("events[%d].domain is unsupported", i)
	}
	return nil
}

func validateSemanticContinuity(c SemanticContinuity) error {
	if err := validateSemanticText("continuity.summary", c.Summary, 1200, true); err != nil {
		return err
	}
	if err := validateContinuityStrings("continuity.decisions", c.Decisions); err != nil {
		return err
	}
	if err := validateContinuityStrings("continuity.open_loops", c.OpenLoops); err != nil {
		return err
	}
	if err := validateContinuityStrings("continuity.do_not_repeat", c.DoNotRepeat); err != nil {
		return err
	}
	if err := validateContinuityStrings("continuity.emotional_anchors", c.EmotionalAnchors); err != nil {
		return err
	}
	if err := validateContinuityStrings("continuity.state_signals", c.StateSignals); err != nil {
		return err
	}
	if err := validateContinuityStrings("continuity.active_threads", c.ActiveThreads); err != nil {
		return err
	}
	if err := validateContinuityStrings("continuity.review_insights", c.ReviewInsights); err != nil {
		return err
	}
	return nil
}

func validateSemanticText(field, value string, max int, required bool) error {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if len(value) > max {
		return fmt.Errorf("%s is too long", field)
	}
	if looksLikeTranscript(value) || looksSensitiveOrPathLike(value) {
		return fmt.Errorf("%s is raw/secret/path-like", field)
	}
	return nil
}

func validSemanticRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return semanticRefPattern.MatchString(ref) && !looksSensitiveOrPathLike(ref)
}

func validSemanticSlug(slug string) bool {
	slug = strings.TrimSpace(slug)
	return slug != "" && len(slug) <= 64 && safeTagPattern.MatchString(slug) && !looksSensitiveOrPathLike(slug)
}

func validSemanticEntityKind(kind string) bool {
	switch kind {
	case "person", "place", "project", "org", "product", "community", "skill", "concept", "thing", "event_series":
		return true
	default:
		return false
	}
}

func validDomain(domain string) bool {
	switch normalizeDomain(domain) {
	case "real", "fiction_content", "fiction_meta", "meta_authorial":
		return true
	default:
		return false
	}
}

func normalizeDomain(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return "real"
	}
	return domain
}

func normalizeSemanticOptional(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}
