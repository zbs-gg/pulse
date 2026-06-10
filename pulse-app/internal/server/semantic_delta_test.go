package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestGraphDeltaEndpointMaterializesGraph(t *testing.T) {
	store, ts := newMemoryServer(t)
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodPost, "/graph/delta", semanticDeltaBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graph delta status=%d", resp.StatusCode)
	}
	var out struct {
		OK              bool `json:"ok"`
		NodesUpserted   int  `json:"nodes_upserted"`
		EdgesUpserted   int  `json:"edges_upserted"`
		FactsUpserted   int  `json:"facts_upserted"`
		EventsInserted  int  `json:"events_inserted"`
		CheckpointSaved bool `json:"checkpoint_saved"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.NodesUpserted != 2 || out.EdgesUpserted != 1 || out.FactsUpserted != 1 || out.EventsInserted != 1 || !out.CheckpointSaved {
		t.Fatalf("bad graph delta response: %#v", out)
	}

	var relations int
	if err := store.DB().QueryRow(`
		SELECT COUNT(*)
		  FROM relations r
		  JOIN entities f ON f.id = r.from_entity_id
		  JOIN entities t ON t.id = r.to_entity_id
		 WHERE f.canonical_name='Pulse'
		   AND t.canonical_name='Host-extracted graph'
		   AND r.kind='implements'`).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if relations != 1 {
		t.Fatalf("expected relation, got %d", relations)
	}
}

func TestGraphDeltaEndpointRejectsRawTranscript(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	body := semanticDeltaBody()
	events := body["events"].([]map[string]any)
	events[0]["summary"] = strings.Repeat("User: save all chat\nAssistant: ok\n", 80)

	resp := pulseJSON(t, ts, http.MethodPost, "/graph/delta", body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for raw transcript-like delta, got %d", resp.StatusCode)
	}
}

func semanticDeltaBody() map[string]any {
	return map[string]any{
		"schema": "pulse.semantic_delta.v1",
		"source": map[string]any{
			"host":               "claude-code",
			"conversation_scope": "current_turn",
			"timestamp":          "2026-06-02T16:00:00Z",
			"thread_id":          "pulse-distribution",
			"session_id":         "claude-code:pulse-distribution:test",
			"project_id":         "pulse-public-clean",
		},
		"nodes": []map[string]any{
			{
				"client_id":        "project:pulse",
				"kind":             "project",
				"canonical_name":   "Pulse",
				"summary":          "Pulse keeps memory and continuity across AI harnesses.",
				"aliases":          []string{"Pulse MCP"},
				"salience":         0.8,
				"emotional_weight": 0.4,
				"privacy_tier":     "normal",
				"domain":           "real",
			},
			{
				"client_id":      "concept:host-extracted-graph",
				"kind":           "concept",
				"canonical_name": "Host-extracted graph",
				"summary":        "The active host model extracts graph deltas while Pulse stores them.",
				"privacy_tier":   "normal",
				"domain":         "real",
			},
		},
		"edges": []map[string]any{
			{
				"from":         "project:pulse",
				"to":           "concept:host-extracted-graph",
				"kind":         "implements",
				"summary":      "Pulse stores graph deltas extracted by the active host model.",
				"strength":     0.8,
				"privacy_tier": "normal",
			},
		},
		"facts": []map[string]any{
			{
				"node":         "project:pulse",
				"text":         "Pulse should build semantic graph structure while ingestion is performed by the current host model.",
				"confidence":   0.9,
				"privacy_tier": "normal",
				"domain":       "real",
			},
		},
		"events": []map[string]any{
			{
				"client_id":        "event:pulse-graph-ingestion-decision",
				"title":            "Pulse graph ingestion decision",
				"summary":          "We decided host models ingest meaning and Pulse owns graph storage.",
				"entity_refs":      []string{"project:pulse", "concept:host-extracted-graph"},
				"emotional_weight": 0.3,
				"confidence":       0.9,
				"privacy_tier":     "normal",
				"domain":           "real",
			},
		},
		"continuity": map[string]any{
			"summary":           "We moved from API-only memory ingestion to host-extracted graph deltas.",
			"decisions":         []string{"Use host subscription for extraction, not Pulse backend LLM by default."},
			"open_loops":        []string{"Implement pulse_graph_delta and materialize it into graph tables."},
			"do_not_repeat":     []string{"Do not pitch Pulse as generic memory without continuity."},
			"emotional_anchors": []string{"The product should feel easy and continuous, not like another memory chore."},
			"state_signals":     []string{"Claude Code first; remote Claude Chat second."},
		},
		"raw_input_included": false,
	}
}
