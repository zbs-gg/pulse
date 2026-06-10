package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterialGraphProjectsAtlasPulseOwnershipDecision(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.SaveCheckpoint(ContinuityCheckpoint{
		ThreadID:  "pulse-architecture",
		SessionID: "claude-code:pulse-architecture:test",
		Host:      "claude-code",
		ProjectID: "garden",
		Summary:   "Atlas and Pulse graph ownership boundary was accepted.",
		Decisions: []string{"Atlas must not own People Graph; Pulse owns portable continuity memory and graph evidence."},
		OpenLoops: []string{"Implement Material Graph v0 as a Pulse projection before expanding extraction."},
		DoNotRepeat: []string{
			"Do not create People Graph or Material Graph storage in Atlas.",
		},
		EmotionalAnchors: []string{"Graphify raised competitor anxiety, but the product correction was architectural."},
		StateSignals:     []string{"Material entities matter because emotions wrap real projects, code, decisions, and people."},
		ActiveThreads:    []string{"Pulse architecture"},
		ReviewInsights: []string{
			"Pulse architecture: Graphify is a benchmark, not the implementation substrate.",
			"Codex entity hygiene: unrelated inactive review insight must stay out.",
		},
		SourceRefs: []string{"pulse:checkpoint:atlas-ownership-demo"},
		Confidence: 0.92,
	}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	graph, err := s.MaterialGraph(MaterialGraphQuery{ThreadID: "pulse-architecture", Limit: 50})
	if err != nil {
		t.Fatalf("MaterialGraph: %v", err)
	}
	if graph.Schema != MaterialGraphSchema {
		t.Fatalf("schema mismatch: %#v", graph.Schema)
	}
	if graph.ThreadID != "pulse-architecture" {
		t.Fatalf("thread mismatch: %#v", graph.ThreadID)
	}

	requireMaterialNode(t, graph, "project:atlas")
	requireMaterialNode(t, graph, "project:pulse")
	requireMaterialNode(t, graph, "concept:people-graph")
	decision := requireMaterialNodeContaining(t, graph, "decision", "Atlas must not own People Graph")
	openLoop := requireMaterialNodeContaining(t, graph, "open_loop", "Implement Material Graph v0")
	constraint := requireMaterialNodeContaining(t, graph, "constraint", "Do not create People Graph")

	if !decision.ResumeEligible || !openLoop.ResumeEligible || !constraint.ResumeEligible {
		t.Fatalf("continuity nodes should be resume eligible: %#v %#v %#v", decision, openLoop, constraint)
	}
	if decision.Salience.Strategic != "high" || decision.Salience.Trust != "high" {
		t.Fatalf("decision should carry strategic/trust salience: %#v", decision.Salience)
	}
	requireMaterialEdge(t, graph, "concept:people-graph", "project:pulse", "owned_by_layer")
	requireMaterialEdge(t, graph, decision.ID, "project:atlas", "do_not_repeat_for")

	for _, node := range graph.Nodes {
		if node.SourceStatus != "source_backed" && node.SourceStatus != "derived_from_reviewed_sources" {
			t.Fatalf("node should declare source status: %#v", node)
		}
		if len(node.SourceRefs) == 0 {
			t.Fatalf("node missing source refs: %#v", node)
		}
	}
	for _, edge := range graph.Edges {
		if edge.SourceStatus != "source_backed" && edge.SourceStatus != "derived_from_reviewed_sources" {
			t.Fatalf("edge should declare source status: %#v", edge)
		}
		if len(edge.SourceRefs) == 0 {
			t.Fatalf("edge missing source refs: %#v", edge)
		}
	}

	joined, _ := json.Marshal(graph.ContinuityPack)
	for _, want := range []string{
		"Atlas must not own People Graph",
		"Implement Material Graph v0",
		"Do not create People Graph",
		"Pulse architecture",
	} {
		if !strings.Contains(string(joined), want) {
			t.Fatalf("continuity pack missing %q: %s", want, joined)
		}
	}
	if strings.Contains(string(joined), "Codex entity hygiene") {
		t.Fatalf("inactive review insight leaked into material continuity pack: %s", joined)
	}
	if !containsString(graph.ContinuityPack.MaterialRefs, decision.ID) {
		t.Fatalf("continuity pack should include decision material ref: %#v", graph.ContinuityPack.MaterialRefs)
	}
}

func TestMaterialGraphKeepsInactiveReviewInsightsOut(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.SaveCheckpoint(ContinuityCheckpoint{
		ThreadID:      "garden-launch",
		SessionID:     "claude-code:garden-launch:test",
		Host:          "claude-code",
		ProjectID:     "garden",
		Summary:       "Garden launch continuity checkpoint.",
		ActiveThreads: []string{"Garden launch"},
		ReviewInsights: []string{
			"Garden launch: keep the first proof focused on local continuity.",
			"Codex entity hygiene session: unrelated inactive review insight.",
		},
		SourceRefs: []string{"pulse:checkpoint:garden-launch"},
		Confidence: 0.88,
	}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	graph, err := s.MaterialGraph(MaterialGraphQuery{ThreadID: "garden-launch", Limit: 50})
	if err != nil {
		t.Fatalf("MaterialGraph: %v", err)
	}
	joined, _ := json.Marshal(graph)
	if !strings.Contains(string(joined), "Garden launch") {
		t.Fatalf("material graph should contain active thread context: %s", joined)
	}
	if strings.Contains(string(joined), "Codex entity hygiene session") {
		t.Fatalf("inactive review insight leaked into material graph: %s", joined)
	}
}

func TestMaterialGraphExcludesHiddenEntities(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	delta := validSemanticDelta()
	delta.Nodes = []SemanticNode{
		{
			ClientID:      "person:noisy",
			Kind:          "person",
			CanonicalName: "Noisy Person",
			Summary:       "Reviewed preview entity that the user later hid.",
			Salience:      0.9,
			PrivacyTier:   "normal",
			Domain:        "real",
		},
		{
			ClientID:      "project:pulse-viewer",
			Kind:          "project",
			CanonicalName: "Pulse Viewer",
			Summary:       "Focused local view of Pulse memory.",
			Salience:      0.7,
			PrivacyTier:   "normal",
			Domain:        "real",
		},
	}
	delta.Edges = []SemanticEdge{{
		From:        "person:noisy",
		To:          "project:pulse-viewer",
		Kind:        "related_to",
		Summary:     "Noisy Person was related to Pulse Viewer in reviewed import.",
		Strength:    0.7,
		PrivacyTier: "normal",
	}}
	delta.Facts = nil
	delta.Events = nil
	delta.Continuity = nil

	if _, err := s.SaveSemanticDelta(delta); err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	var hiddenID int64
	if err := s.DB().QueryRow(`SELECT id FROM entities WHERE canonical_name='Noisy Person'`).Scan(&hiddenID); err != nil {
		t.Fatalf("lookup hidden entity: %v", err)
	}
	if err := s.HideGraphEntity(hiddenID); err != nil {
		t.Fatalf("HideGraphEntity: %v", err)
	}

	graph, err := s.MaterialGraph(MaterialGraphQuery{ThreadID: "pulse-distribution", Limit: 50})
	if err != nil {
		t.Fatalf("MaterialGraph: %v", err)
	}
	joined, _ := json.Marshal(graph)
	if !strings.Contains(string(joined), "Pulse Viewer") {
		t.Fatalf("material graph should keep visible reviewed entities: %s", joined)
	}
	if strings.Contains(string(joined), "Noisy Person") {
		t.Fatalf("hidden entity leaked into material graph: %s", joined)
	}
}

func requireMaterialNode(t *testing.T, graph MaterialGraph, id string) MaterialGraphNode {
	t.Helper()
	for _, node := range graph.Nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("missing material node %q in %#v", id, graph.Nodes)
	return MaterialGraphNode{}
}

func requireMaterialNodeContaining(t *testing.T, graph MaterialGraph, kind, text string) MaterialGraphNode {
	t.Helper()
	for _, node := range graph.Nodes {
		if node.Kind == kind && (strings.Contains(node.Label, text) || strings.Contains(node.Summary, text)) {
			return node
		}
	}
	t.Fatalf("missing material node kind=%q containing %q in %#v", kind, text, graph.Nodes)
	return MaterialGraphNode{}
}

func requireMaterialEdge(t *testing.T, graph MaterialGraph, from, to, kind string) MaterialGraphEdge {
	t.Helper()
	for _, edge := range graph.Edges {
		if edge.From == from && edge.To == to && edge.Kind == kind {
			return edge
		}
	}
	t.Fatalf("missing material edge %s -[%s]-> %s in %#v", from, kind, to, graph.Edges)
	return MaterialGraphEdge{}
}
