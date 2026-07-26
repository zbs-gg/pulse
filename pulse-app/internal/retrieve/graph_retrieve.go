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
	seedQuery := `SELECT rowid FROM entities_fts WHERE entities_fts MATCH ?1
		 ORDER BY bm25(entities_fts) LIMIT ?2`
	seedArgs := []any{match, graphMaxSeeds}
	if e.personalScope != nil {
		seedQuery = `
			SELECT rowid
			  FROM entities_fts
			 WHERE entities_fts MATCH ?1
			   AND EXISTS (
			       SELECT 1
			         FROM private_semantic_projection_rows projection
			         JOIN private_memory_objects object
			           ON object.object_id=projection.object_id
			        WHERE projection.row_kind='entity'
			          AND projection.row_ref=CAST(entities_fts.rowid AS TEXT)
			          AND object.lifecycle='active'
			          AND (
			              object.memory_scope='personal_global' OR
			              (object.memory_scope='project' AND object.project_namespace_id=?3)
			          )
			   )
			 ORDER BY bm25(entities_fts) LIMIT ?2`
		seedArgs = []any{match, graphMaxSeeds, e.personalScope.ProjectNamespaceID}
	}
	seeds := queryInt64s(ctx, db, seedQuery, seedArgs...)
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
		eventQuery := `SELECT event_id FROM event_entities WHERE entity_id = ?1 LIMIT ?2`
		eventArgs := []any{ent, graphMaxEventsPerEnt}
		if e.personalScope != nil {
			eventQuery = `
				WITH eligible_objects AS (
				    SELECT object_id
				      FROM private_memory_objects
				     WHERE lifecycle='active'
				       AND (
				           memory_scope='personal_global' OR
				           (memory_scope='project' AND project_namespace_id=?1)
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
				    UNION
				    SELECT projection.event_id
				      FROM git_memory_shared_projection projection
				     WHERE projection.status='active'
				       AND projection.event_id IS NOT NULL
				       AND projection.repository_id=?2
				       AND projection.binding_digest=?3
				)
				SELECT link.event_id
				  FROM event_entities link
				  JOIN eligible_events eligible ON eligible.event_id=link.event_id
				 WHERE link.entity_id=?4
				 LIMIT ?5`
			eventArgs = []any{
				e.personalScope.ProjectNamespaceID,
				e.personalScope.RepositoryID,
				e.personalScope.BindingDigest,
				ent, graphMaxEventsPerEnt,
			}
		}
		evs := queryInt64s(ctx, db, eventQuery, eventArgs...)
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
			relationQuery := `SELECT to_entity_id FROM relations
				   WHERE from_entity_id = ?1 AND strength >= ?2
				 UNION
				 SELECT from_entity_id FROM relations
				   WHERE to_entity_id = ?1 AND strength >= ?2
				 ORDER BY 1 LIMIT ?3`
			relationArgs := []any{node, graphMinEdgeStrength, graphMaxEdgesPerNode}
			if e.personalScope != nil {
				relationQuery = `
					WITH eligible_relations AS (
					    SELECT relation.id, relation.from_entity_id, relation.to_entity_id, relation.strength
					      FROM relations relation
					     WHERE EXISTS (
					         SELECT 1
					           FROM private_semantic_projection_rows projection
					           JOIN private_memory_objects object
					             ON object.object_id=projection.object_id
					          WHERE projection.row_kind='relation'
					            AND projection.row_ref=CAST(relation.id AS TEXT)
					            AND object.lifecycle='active'
					            AND (
					                object.memory_scope='personal_global' OR
					                (object.memory_scope='project' AND object.project_namespace_id=?1)
					            )
					     )
					)
					SELECT to_entity_id FROM eligible_relations
					 WHERE from_entity_id=?2 AND strength>=?3
					UNION
					SELECT from_entity_id FROM eligible_relations
					 WHERE to_entity_id=?2 AND strength>=?3
					ORDER BY 1 LIMIT ?4`
				relationArgs = []any{
					e.personalScope.ProjectNamespaceID,
					node, graphMinEdgeStrength, graphMaxEdgesPerNode,
				}
			}
			nbrs := queryInt64s(ctx, db, relationQuery, relationArgs...)
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
