package retrieve

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/nkkmnk/pulse/internal/store"
)

// BM25Search runs an FTS5 lexical search across events / facts / entities
// and returns event IDs ranked by combined BM25 score.
//
// Why three sources: a query like "release plan" matches an entity
// named `Q3 release plan` directly (entities_fts) and the facts that
// mention it (facts_fts → event via facts.event_id), while the source
// events themselves may not contain those exact words in title/description.
// Joining via the graph catches all three.
//
// The query is tokenized by splitting on non-letter/digit chars; each
// token is wrapped in double quotes and given a prefix '*' so 'корневой'
// matches 'корневая'. Tokens shorter than 2 runes are dropped.
//
// Returns at most topK event IDs, best first. Empty result if query
// parses to nothing or DB is nil.
func BM25Search(ctx context.Context, db *sql.DB, query string, topK int) ([]int64, error) {
	return BM25SearchScoped(ctx, db, query, topK, nil)
}

// BM25SearchScoped applies Personal project/global eligibility inside every
// lexical source before LIMIT or RRF. This is deliberately not a post-filter:
// an excellent foreign-project match must be unable to crowd an eligible event
// out of the lexical candidate window.
func BM25SearchScoped(
	ctx context.Context,
	db *sql.DB,
	query string,
	topK int,
	scope *store.PersonalMemoryScopeSnapshot,
) ([]int64, error) {
	if db == nil || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	matchExpr := buildFTSMatch(query)
	if matchExpr == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}

	// Score per event_id, lower is better in BM25 (negative ranks).
	// Three sub-queries union into a UNION ALL, then we aggregate per
	// event in Go (taking the BEST/lowest score across sources).
	sqlStr := `
WITH events_hits AS (
    SELECT rowid AS event_id, bm25(events_fts) AS score
    FROM events_fts WHERE events_fts MATCH ?1
    ORDER BY score LIMIT ?2
),
facts_hits AS (
    -- facts have no direct event_id; they hang off an entity, and
    -- entities link to events via event_entities (M:N). Take the
    -- entity's events as candidates with the fact's BM25 score.
    SELECT ee.event_id AS event_id, bm25(facts_fts) AS score
    FROM facts_fts
      JOIN facts f ON f.id = facts_fts.rowid
      JOIN event_entities ee ON ee.entity_id = f.entity_id
    WHERE facts_fts MATCH ?1
    ORDER BY score LIMIT ?2
),
entity_hits AS (
    SELECT ee.event_id AS event_id, bm25(entities_fts) AS score
    FROM entities_fts
      JOIN event_entities ee ON ee.entity_id = entities_fts.rowid
    WHERE entities_fts MATCH ?1
    ORDER BY score LIMIT ?2
)
SELECT event_id, MIN(score) AS best_score
FROM (
    SELECT * FROM events_hits
    UNION ALL SELECT * FROM facts_hits
    UNION ALL SELECT * FROM entity_hits
)
GROUP BY event_id
ORDER BY best_score ASC
LIMIT ?2`
	args := []any{matchExpr, topK * 3}
	if scope != nil {
		sqlStr = `
WITH eligible_objects AS (
    SELECT object_id
      FROM private_memory_objects
     WHERE lifecycle='active'
       AND (
           memory_scope='personal_global' OR
           (memory_scope='project' AND project_namespace_id=?3)
       )
),
eligible_events(event_id) AS (
    SELECT capsule.event_id
      FROM eligible_objects object
      JOIN memory_capsules capsule ON capsule.id=object.object_id
     WHERE capsule.event_id IS NOT NULL
    UNION
    SELECT CAST(projection.row_ref AS INTEGER)
      FROM eligible_objects object
      JOIN private_semantic_projection_rows projection
        ON projection.object_id=object.object_id
       AND projection.row_kind='event'
),
events_hits AS (
    SELECT fts.rowid AS event_id, bm25(events_fts) AS score
      FROM events_fts fts
      JOIN eligible_events eligible ON eligible.event_id=fts.rowid
     WHERE events_fts MATCH ?1
     ORDER BY score LIMIT ?2
),
facts_hits AS (
    SELECT ee.event_id AS event_id, bm25(facts_fts) AS score
      FROM facts_fts
      JOIN facts fact ON fact.id=facts_fts.rowid
      JOIN private_semantic_projection_rows fact_projection
        ON fact_projection.row_kind='fact'
       AND fact_projection.row_ref=CAST(fact.id AS TEXT)
      JOIN eligible_objects fact_object
        ON fact_object.object_id=fact_projection.object_id
      JOIN event_entities ee ON ee.entity_id=fact.entity_id
      JOIN eligible_events eligible ON eligible.event_id=ee.event_id
     WHERE facts_fts MATCH ?1
     ORDER BY score LIMIT ?2
),
entity_hits AS (
    SELECT ee.event_id AS event_id, bm25(entities_fts) AS score
      FROM entities_fts
      JOIN private_semantic_projection_rows entity_projection
        ON entity_projection.row_kind='entity'
       AND entity_projection.row_ref=CAST(entities_fts.rowid AS TEXT)
      JOIN eligible_objects entity_object
        ON entity_object.object_id=entity_projection.object_id
      JOIN event_entities ee ON ee.entity_id=entities_fts.rowid
      JOIN eligible_events eligible ON eligible.event_id=ee.event_id
     WHERE entities_fts MATCH ?1
     ORDER BY score LIMIT ?2
)
SELECT event_id, MIN(score) AS best_score
  FROM (
      SELECT * FROM events_hits
      UNION ALL SELECT * FROM facts_hits
      UNION ALL SELECT * FROM entity_hits
  )
 GROUP BY event_id
 ORDER BY best_score ASC
 LIMIT ?2`
		args = []any{matchExpr, topK * 3, scope.ProjectNamespaceID}
	}
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("bm25 query (%q): %w", matchExpr, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var score float64
		if err := rows.Scan(&id, &score); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > topK {
		ids = ids[:topK]
	}
	return ids, nil
}

// BM25CapsuleSearchScoped ranks only active memory capsules. The scope fence is
// applied inside the FTS query, before LIMIT, so a foreign project or archive
// event cannot crowd a usable Personal capsule out of the lexical window.
func BM25CapsuleSearchScoped(
	ctx context.Context,
	db *sql.DB,
	query string,
	topK int,
	scope *store.PersonalMemoryScopeSnapshot,
) ([]int64, error) {
	if db == nil || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	matchExpr := buildFTSCorroborationMatch(query)
	if matchExpr == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	querySQL := `
SELECT fts.rowid, bm25(events_fts)
  FROM events_fts fts
  JOIN memory_capsules capsule ON capsule.event_id=fts.rowid
 WHERE events_fts MATCH ?
   AND capsule.status='active'
 ORDER BY bm25(events_fts), fts.rowid
 LIMIT ?`
	args := []any{matchExpr, topK}
	if scope != nil {
		querySQL = `
SELECT fts.rowid, bm25(events_fts)
  FROM events_fts fts
  JOIN memory_capsules capsule ON capsule.event_id=fts.rowid
  JOIN private_memory_objects object ON object.object_id=capsule.id
 WHERE events_fts MATCH ?
   AND capsule.status='active'
   AND object.lifecycle='active'
   AND (
       object.memory_scope='personal_global' OR
       (object.memory_scope='project' AND object.project_namespace_id=?)
   )
 ORDER BY bm25(events_fts), fts.rowid
 LIMIT ?`
		args = []any{matchExpr, scope.ProjectNamespaceID, topK}
	}
	rows, err := db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("BM25 capsule query (%q): %w", matchExpr, err)
	}
	defer rows.Close()
	type capsuleHit struct {
		id      int64
		bm25    float64
		overlap int
	}
	queryTerms := distinctiveTerms(query)
	hits := make([]capsuleHit, 0, topK)
	for rows.Next() {
		var id int64
		var score float64
		if err := rows.Scan(&id, &score); err != nil {
			return nil, err
		}
		hits = append(hits, capsuleHit{id: id, bm25: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}
	ids := make([]int64, len(hits))
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hits)), ",")
	args = args[:0]
	for i, hit := range hits {
		ids[i] = hit.id
		args = append(args, hit.id)
	}
	textRows, err := db.QueryContext(ctx,
		`SELECT rowid, title || ' ' || description FROM events WHERE rowid IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	texts := make(map[int64]string, len(hits))
	for textRows.Next() {
		var id int64
		var text string
		if err := textRows.Scan(&id, &text); err != nil {
			textRows.Close()
			return nil, err
		}
		texts[id] = text
	}
	if err := textRows.Close(); err != nil {
		return nil, err
	}
	for index := range hits {
		memoryTerms := distinctiveTerms(texts[hits[index].id])
		for term := range queryTerms {
			if _, ok := memoryTerms[term]; ok {
				hits[index].overlap++
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].overlap != hits[j].overlap {
			return hits[i].overlap > hits[j].overlap
		}
		if hits[i].bm25 != hits[j].bm25 {
			return hits[i].bm25 < hits[j].bm25
		}
		return hits[i].id < hits[j].id
	})
	for index, hit := range hits {
		ids[index] = hit.id
	}
	return ids, nil
}

func distinctiveTerms(value string) map[string]struct{} {
	terms := make(map[string]struct{})
	field := strings.Builder{}
	flush := func() {
		term := strings.ToLower(strings.TrimSpace(field.String()))
		field.Reset()
		if len([]rune(term)) < 4 {
			return
		}
		if _, stopped := corroborationStopwords[term]; stopped {
			return
		}
		terms[term] = struct{}{}
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			field.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return terms
}

// buildFTSMatch turns a free-text query into an FTS5 MATCH expression.
// Splits on non-letter/digit, drops short tokens, wraps each remaining
// token in double quotes with a trailing '*' prefix marker, joins with
// OR so ANY word can hit (recall over precision; the cosine + RRF rank
// later trims false positives).
//
//	"release plan"    → `"release"* OR "plan"*`
//	"что у меня с"    → (everything dropped — too short) → ""
func buildFTSMatch(q string) string {
	var tokens []string
	field := strings.Builder{}
	flush := func() {
		s := strings.TrimSpace(field.String())
		field.Reset()
		if len([]rune(s)) < 2 {
			return
		}
		// FTS5 is case-insensitive but escape any embedded quotes
		s = strings.ReplaceAll(s, `"`, ``)
		tokens = append(tokens, fmt.Sprintf(`"%s"*`, strings.ToLower(s)))
	}
	for _, r := range q {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			field.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	if len(tokens) == 0 {
		return ""
	}
	return strings.Join(tokens, " OR ")
}

var corroborationStopwords = map[string]struct{}{
	"about": {}, "could": {}, "does": {}, "from": {}, "have": {}, "should": {}, "that": {},
	"what": {}, "when": {}, "where": {}, "which": {}, "with": {}, "would": {},
	"будет": {}, "были": {}, "должен": {}, "должна": {}, "должны": {}, "какая": {},
	"какие": {}, "какой": {}, "когда": {}, "после": {}, "почему": {}, "чтобы": {},
}

// buildFTSCorroborationMatch keeps only distinctive prompt words. It is used
// to prove that a dense archive candidate also contains a literal textual
// anchor, without letting high-frequency question words crowd rare names out
// of the global BM25 window.
func buildFTSCorroborationMatch(q string) string {
	var tokens []string
	seen := map[string]struct{}{}
	field := strings.Builder{}
	flush := func() {
		token := strings.ToLower(strings.TrimSpace(field.String()))
		field.Reset()
		if len([]rune(token)) < 4 {
			return
		}
		if _, stopped := corroborationStopwords[token]; stopped {
			return
		}
		if _, duplicate := seen[token]; duplicate {
			return
		}
		seen[token] = struct{}{}
		tokens = append(tokens, fmt.Sprintf(`"%s"*`, strings.ReplaceAll(token, `"`, ``)))
	}
	for _, r := range q {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			field.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return strings.Join(tokens, " OR ")
}

// FTSCorroborateCandidates returns only candidate IDs whose own event text
// matches at least one distinctive prompt token. Candidate IDs already came
// from the scope-fenced dense index, so this cannot broaden project access.
func FTSCorroborateCandidates(
	ctx context.Context,
	db *sql.DB,
	query string,
	candidateIDs []int64,
) ([]int64, error) {
	if db == nil || len(candidateIDs) == 0 {
		return nil, nil
	}
	matchExpr := buildFTSCorroborationMatch(query)
	if matchExpr == "" {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(candidateIDs)), ",")
	args := make([]any, 0, len(candidateIDs)+1)
	args = append(args, matchExpr)
	for _, id := range candidateIDs {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx, `
SELECT rowid
  FROM events_fts
 WHERE events_fts MATCH ?
   AND rowid IN (`+placeholders+`)
 ORDER BY rowid`, args...)
	if err != nil {
		return nil, fmt.Errorf("FTS candidate corroboration: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RRFFuse combines several ranked ID lists using Reciprocal Rank Fusion.
// k=60 is the canonical default (Cormack et al. 2009). Higher k softens
// rank differences; lower k amplifies the top-1 effect.
//
// Each list is treated as already deduplicated and ordered best-first.
// IDs missing from a list contribute 0 from that source. Returns IDs
// sorted by total RRF score descending, capped at topK.
func RRFFuse(lists [][]int64, topK int, k float64) []int64 {
	if k <= 0 {
		k = 60
	}
	scores := map[int64]float64{}
	for _, list := range lists {
		for rank, id := range list {
			scores[id] += 1.0 / (k + float64(rank+1))
		}
	}
	type pair struct {
		id    int64
		score float64
	}
	pairs := make([]pair, 0, len(scores))
	for id, s := range scores {
		pairs = append(pairs, pair{id, s})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].score != pairs[j].score {
			return pairs[i].score > pairs[j].score
		}
		return pairs[i].id < pairs[j].id // stable tie-break
	})
	if len(pairs) > topK {
		pairs = pairs[:topK]
	}
	out := make([]int64, len(pairs))
	for i, p := range pairs {
		out[i] = p.id
	}
	return out
}
