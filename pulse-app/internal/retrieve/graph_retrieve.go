package retrieve

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
)

// Temporal entity-graph retrieval — "graph-as-recall-injector" (NOT graph-as-king).
//
// Default-OFF: only runs when RetrieveRequest.GraphMode is "anchored" or "walk".
// Produces a candidate event-id list that the caller fuses as an EXTRA RRF input
// alongside cosine + BM25. It never touches scoreEventsV3 and never reorders the
// salience ranking on its own — it only injects candidates the lexical/dense
// paths go blind to (multi-hop bridges, entity-centric recall). Validated on the
// graph-necessary bench: V1 entity-anchored lifts entity-centric recall, V2 walk
// lifts multi-hop, with no single-hop/control regression (see
// ~/elle/eval/graph-bench + docs/pulse_entity_graph_retrieval_2026_06_28.md).
//
// Hard caps mirror the Pro-ratified anti-walk-explosion defaults:
const (
	graphMaxSeeds        = 5  // entity_seeds <= 5
	graphMaxHops         = 2  // hop_limit = 2
	graphMaxEdgesPerNode = 8  // max_edges_per_node_per_type
	graphMaxFrontier     = 40 // max_frontier_nodes
	graphMaxEventsPerEnt = 10 // max_events_per_entity
	graphMinEdgeStrength = 0.5
)

var graphTokenRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// ftsOrQuery turns a free-text query into a safe FTS5 MATCH expression
// (`tok1 OR tok2 …`), dropping FTS operators so natural-language queries
// can't throw a syntax error.
func ftsOrQuery(q string) string {
	toks := graphTokenRe.FindAllString(strings.ToLower(q), -1)
	seen := map[string]bool{}
	var parts []string
	for _, t := range toks {
		if len(t) < 2 || seen[t] {
			continue
		}
		seen[t] = true
		parts = append(parts, `"`+t+`"`)
	}
	return strings.Join(parts, " OR ")
}

// retrieveGraphCandidates returns candidate event ids reached through the entity
// graph. mode: "anchored" (entity → its events) or "walk" (+ typed relation walk).
// Best-effort: any DB error yields nil (the caller just loses the extra RRF input).
func (e *Engine) retrieveGraphCandidates(ctx context.Context, query, mode string, topK int) []int64 {
	if e.store == nil || (mode != "anchored" && mode != "walk") {
		return nil
	}
	db := e.store.DB()
	match := ftsOrQuery(query)
	if match == "" {
		return nil
	}
	// 1) entity seeds: lexical match on entity names (entities_fts.rowid = entity id)
	seeds := queryInt64s(ctx, db,
		`SELECT rowid FROM entities_fts WHERE entities_fts MATCH ?1
		 ORDER BY bm25(entities_fts) LIMIT ?2`, match, graphMaxSeeds)
	if len(seeds) == 0 {
		return nil
	}

	ents := seeds
	if mode == "walk" {
		ents = e.walkEntities(ctx, db, seeds)
	}

	// 2) gather events linked to the reached entities (event_entities), capped.
	var out []int64
	seenEv := map[int64]bool{}
	for _, ent := range ents {
		evs := queryInt64s(ctx, db,
			`SELECT event_id FROM event_entities WHERE entity_id = ?1 LIMIT ?2`,
			ent, graphMaxEventsPerEnt)
		for _, ev := range evs {
			if !seenEv[ev] {
				seenEv[ev] = true
				out = append(out, ev)
			}
		}
	}
	if len(out) > topK*4 {
		out = out[:topK*4]
	}
	return out
}

// walkEntities does a typed, capped relation walk (<=2 hops, strength-filtered)
// from the seed entities and returns seeds + reached entities.
func (e *Engine) walkEntities(ctx context.Context, db *sql.DB, seeds []int64) []int64 {
	seen := map[int64]bool{}
	for _, s := range seeds {
		seen[s] = true
	}
	frontier := append([]int64(nil), seeds...)
	for hop := 0; hop < graphMaxHops && len(frontier) > 0; hop++ {
		var next []int64
		for _, node := range frontier {
			if len(next) >= graphMaxFrontier {
				break
			}
			nbrs := queryInt64s(ctx, db,
				`SELECT to_entity_id FROM relations
				   WHERE from_entity_id = ?1 AND strength >= ?2
				 UNION
				 SELECT from_entity_id FROM relations
				   WHERE to_entity_id = ?1 AND strength >= ?2
				 ORDER BY 1 LIMIT ?3`,
				node, graphMinEdgeStrength, graphMaxEdgesPerNode)
			for _, nb := range nbrs {
				if !seen[nb] {
					seen[nb] = true
					next = append(next, nb)
				}
			}
		}
		frontier = next
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

// queryInt64s runs a query returning a single int64 column; nil on error.
func queryInt64s(ctx context.Context, db *sql.DB, q string, args ...any) []int64 {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if rows.Scan(&v) == nil {
			out = append(out, v)
		}
	}
	return out
}
