package retrieve

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode"
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
	rows, err := db.QueryContext(ctx, sqlStr, matchExpr, topK*3)
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
