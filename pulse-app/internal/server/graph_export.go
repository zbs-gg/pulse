package server

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// graphExport is the read-only entity-graph snapshot consumed by the local
// visual graph editor (Cytoscape + timeline). Local-only, behind the same
// X-Pulse-Key auth as the other store endpoints; never includes raw transcripts.
type graphExportNode struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	Salience        float64 `json:"salience"`
	EmotionalWeight float64 `json:"emotional_weight"`
	IsSelf          bool    `json:"is_self"`
}
type graphExportEdge struct {
	ID       string  `json:"id"`
	Source   string  `json:"source"`
	Target   string  `json:"target"`
	Kind     string  `json:"kind"`
	Strength float64 `json:"strength"`
}
type graphExportEvent struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	TS    string `json:"ts"`
	Label string `json:"sentiment_label"`
}
type graphExportLink struct {
	Event  string `json:"event"`
	Entity string `json:"entity"`
}

// handleGraphExport serves GET /graph/export → the full entity graph as JSON
// for the local editor. Read-only; mutations happen via the separate
// feedback-apply path, never directly from the editor.
func (s *Server) handleGraphExport(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Store == nil {
		http.Error(w, "store not configured", http.StatusServiceUnavailable)
		return
	}
	db := s.cfg.Store.DB()
	ctx := r.Context()
	out := struct {
		Nodes  []graphExportNode  `json:"nodes"`
		Edges  []graphExportEdge  `json:"edges"`
		Events []graphExportEvent `json:"events"`
		Links  []graphExportLink  `json:"event_entities"`
		Stats  map[string]int     `json:"stats"`
	}{Stats: map[string]int{}}

	if rows, err := db.QueryContext(ctx,
		`SELECT id, canonical_name, kind, salience_score, emotional_weight, is_self FROM entities`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var n graphExportNode
			var id int64
			var self int
			if rows.Scan(&id, &n.Name, &n.Kind, &n.Salience, &n.EmotionalWeight, &self) == nil {
				n.ID = itoa(id)
				n.IsSelf = self != 0
				out.Nodes = append(out.Nodes, n)
			}
		}
	}
	if rows, err := db.QueryContext(ctx,
		`SELECT id, from_entity_id, to_entity_id, kind, strength FROM relations`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var rid, from, to int64
			var e graphExportEdge
			if rows.Scan(&rid, &from, &to, &e.Kind, &e.Strength) == nil {
				e.ID, e.Source, e.Target = "r"+itoa(rid), itoa(from), itoa(to)
				out.Edges = append(out.Edges, e)
			}
		}
	}
	if rows, err := db.QueryContext(ctx,
		`SELECT id, title, ts, COALESCE(sentiment_label,'') FROM events`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id int64
			var ev graphExportEvent
			if rows.Scan(&id, &ev.Title, &ev.TS, &ev.Label) == nil {
				ev.ID = "e" + itoa(id)
				out.Events = append(out.Events, ev)
			}
		}
	}
	if rows, err := db.QueryContext(ctx,
		`SELECT event_id, entity_id FROM event_entities`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var ev, en int64
			if rows.Scan(&ev, &en) == nil {
				out.Links = append(out.Links, graphExportLink{Event: "e" + itoa(ev), Entity: itoa(en)})
			}
		}
	}
	out.Stats["nodes"], out.Stats["edges"], out.Stats["events"] = len(out.Nodes), len(out.Edges), len(out.Events)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
