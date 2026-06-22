package store

import (
	"fmt"
	"regexp"
	"strings"
)

const MaterialGraphSchema = "pulse.material_graph.v0"

var materialIDPartPattern = regexp.MustCompile(`[^a-z0-9]+`)

type MaterialGraphQuery struct {
	ThreadID  string `json:"thread_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type MaterialGraph struct {
	Schema         string                      `json:"schema"`
	ThreadID       string                      `json:"thread_id"`
	ProjectID      string                      `json:"project_id,omitempty"`
	SessionID      string                      `json:"session_id,omitempty"`
	Nodes          []MaterialGraphNode         `json:"nodes"`
	Edges          []MaterialGraphEdge         `json:"edges"`
	ContinuityPack MaterialGraphContinuityPack `json:"continuity_pack"`
}

type MaterialGraphNode struct {
	ID             string                `json:"id"`
	Kind           string                `json:"kind"`
	Label          string                `json:"label"`
	Summary        string                `json:"summary,omitempty"`
	SourceRefs     []string              `json:"source_refs"`
	SourceStatus   string                `json:"source_status"`
	PrivacyTier    string                `json:"privacy_tier"`
	Confidence     float64               `json:"confidence"`
	Status         string                `json:"status"`
	Scope          string                `json:"scope"`
	Salience       MaterialGraphSalience `json:"salience,omitempty"`
	ResumeEligible bool                  `json:"resume_eligible,omitempty"`
}

type MaterialGraphEdge struct {
	From         string   `json:"from"`
	To           string   `json:"to"`
	Kind         string   `json:"kind"`
	Summary      string   `json:"summary,omitempty"`
	SourceRefs   []string `json:"source_refs"`
	SourceStatus string   `json:"source_status"`
	PrivacyTier  string   `json:"privacy_tier"`
	Confidence   float64  `json:"confidence"`
	Status       string   `json:"status"`
	Scope        string   `json:"scope"`
}

type MaterialGraphSalience struct {
	Strategic string `json:"strategic,omitempty"`
	Trust     string `json:"trust,omitempty"`
	Emotional string `json:"emotional,omitempty"`
	State     string `json:"state,omitempty"`
}

type MaterialGraphContinuityPack struct {
	ActiveDecisions        []string `json:"active_decisions,omitempty"`
	ActiveReviewedThreads  []string `json:"active_reviewed_threads,omitempty"`
	ReviewInsights         []string `json:"review_insights,omitempty"`
	OpenLoops              []string `json:"open_loops,omitempty"`
	DoNotRepeat            []string `json:"do_not_repeat,omitempty"`
	RelevantEmotionalState []string `json:"relevant_emotional_state_context,omitempty"`
	EvidenceRefs           []string `json:"evidence_refs,omitempty"`
	MaterialRefs           []string `json:"material_refs,omitempty"`
}

type materialGraphBuilder struct {
	graph    *MaterialGraph
	nodeSeen map[string]int
	edgeSeen map[string]bool
	limit    int
	// scope marks rows added in the current phase. The checkpoint phase is
	// genuinely thread-scoped; the stored-row phase reads GLOBAL top entities/
	// relations/facts/events (no thread/project column exists on those tables),
	// so they are labeled "global" to avoid presenting unrelated neighbors as
	// focused thread context.
	scope string
}

func (s *Store) MaterialGraph(q MaterialGraphQuery) (MaterialGraph, error) {
	threadID := normalizeThreadID(q.ThreadID, q.ProjectID, q.SessionID)
	limit := materialGraphLimit(q.Limit)
	graph := MaterialGraph{
		Schema:    MaterialGraphSchema,
		ThreadID:  threadID,
		ProjectID: strings.TrimSpace(q.ProjectID),
		SessionID: strings.TrimSpace(q.SessionID),
		Nodes:     []MaterialGraphNode{},
		Edges:     []MaterialGraphEdge{},
	}
	builder := newMaterialGraphBuilder(&graph, limit)

	cp, hasCheckpoint, err := s.latestCheckpoint(threadID)
	if err != nil {
		return MaterialGraph{}, err
	}
	if hasCheckpoint {
		builder.scope = "thread"
		builder.addCheckpoint(cp)
	}
	builder.scope = "global"
	if err := s.addStoredMaterialGraphRows(builder, limit); err != nil {
		return MaterialGraph{}, err
	}
	return graph, nil
}

func newMaterialGraphBuilder(graph *MaterialGraph, limit int) *materialGraphBuilder {
	return &materialGraphBuilder{
		graph:    graph,
		nodeSeen: map[string]int{},
		edgeSeen: map[string]bool{},
		limit:    limit,
		scope:    "global",
	}
}

func (b *materialGraphBuilder) currentScope() string {
	if b.scope == "" {
		return "global"
	}
	return b.scope
}

func (b *materialGraphBuilder) addCheckpoint(cp ContinuityCheckpoint) {
	sourceRefs := materialCheckpointSourceRefs(cp)
	confidence := materialConfidence(cp.Confidence)
	salience := MaterialGraphSalience{Strategic: "high", Trust: "medium"}

	for _, thread := range cp.ActiveThreads {
		node := MaterialGraphNode{
			ID:             materialID("thread", thread),
			Kind:           "thread",
			Label:          strings.TrimSpace(thread),
			Summary:        "Active reviewed thread for the next Pulse resume.",
			SourceRefs:     sourceRefs,
			SourceStatus:   "source_backed",
			PrivacyTier:    "private",
			Confidence:     confidence,
			Status:         "reviewed",
			Salience:       salience,
			ResumeEligible: true,
		}
		if b.addNode(node) {
			b.graph.ContinuityPack.ActiveReviewedThreads = appendUniqueContinuityItem(b.graph.ContinuityPack.ActiveReviewedThreads, node.Label)
			b.graph.ContinuityPack.MaterialRefs = appendUniqueContinuityItem(b.graph.ContinuityPack.MaterialRefs, node.ID)
		}
	}

	for _, decision := range cp.Decisions {
		node := MaterialGraphNode{
			ID:             materialID("decision", decision),
			Kind:           "decision",
			Label:          strings.TrimSpace(decision),
			Summary:        "Active decision from the latest continuity checkpoint.",
			SourceRefs:     sourceRefs,
			SourceStatus:   "source_backed",
			PrivacyTier:    "private",
			Confidence:     confidence,
			Status:         "reviewed",
			Salience:       MaterialGraphSalience{Strategic: "high", Trust: "high", Emotional: "medium"},
			ResumeEligible: true,
		}
		if b.addNode(node) {
			b.graph.ContinuityPack.ActiveDecisions = appendUniqueContinuityItem(b.graph.ContinuityPack.ActiveDecisions, node.Label)
			b.graph.ContinuityPack.MaterialRefs = appendUniqueContinuityItem(b.graph.ContinuityPack.MaterialRefs, node.ID)
		}
	}

	for _, openLoop := range cp.OpenLoops {
		node := MaterialGraphNode{
			ID:             materialID("open-loop", openLoop),
			Kind:           "open_loop",
			Label:          strings.TrimSpace(openLoop),
			Summary:        "Open loop eligible for the next Pulse resume.",
			SourceRefs:     sourceRefs,
			SourceStatus:   "source_backed",
			PrivacyTier:    "private",
			Confidence:     confidence,
			Status:         "reviewed",
			Salience:       MaterialGraphSalience{Strategic: "high", Trust: "medium"},
			ResumeEligible: true,
		}
		if b.addNode(node) {
			b.graph.ContinuityPack.OpenLoops = appendUniqueContinuityItem(b.graph.ContinuityPack.OpenLoops, node.Label)
			b.graph.ContinuityPack.MaterialRefs = appendUniqueContinuityItem(b.graph.ContinuityPack.MaterialRefs, node.ID)
		}
	}

	for _, warning := range cp.DoNotRepeat {
		node := MaterialGraphNode{
			ID:             materialID("constraint", warning),
			Kind:           "constraint",
			Label:          strings.TrimSpace(warning),
			Summary:        "Do-not-repeat warning from reviewed continuity.",
			SourceRefs:     sourceRefs,
			SourceStatus:   "source_backed",
			PrivacyTier:    "private",
			Confidence:     confidence,
			Status:         "reviewed",
			Salience:       MaterialGraphSalience{Strategic: "high", Trust: "high"},
			ResumeEligible: true,
		}
		if b.addNode(node) {
			b.graph.ContinuityPack.DoNotRepeat = appendUniqueContinuityItem(b.graph.ContinuityPack.DoNotRepeat, node.Label)
			b.graph.ContinuityPack.MaterialRefs = appendUniqueContinuityItem(b.graph.ContinuityPack.MaterialRefs, node.ID)
		}
	}

	for _, anchor := range cp.EmotionalAnchors {
		node := MaterialGraphNode{
			ID:             materialID("emotion-anchor", anchor),
			Kind:           "emotion_anchor",
			Label:          strings.TrimSpace(anchor),
			Summary:        "Emotional salience overlay attached to this thread.",
			SourceRefs:     sourceRefs,
			SourceStatus:   "source_backed",
			PrivacyTier:    "private",
			Confidence:     confidence,
			Status:         "reviewed",
			Salience:       MaterialGraphSalience{Emotional: "high", State: "medium"},
			ResumeEligible: true,
		}
		if b.addNode(node) {
			b.graph.ContinuityPack.RelevantEmotionalState = appendUniqueContinuityItem(b.graph.ContinuityPack.RelevantEmotionalState, node.Label)
			b.graph.ContinuityPack.MaterialRefs = appendUniqueContinuityItem(b.graph.ContinuityPack.MaterialRefs, node.ID)
		}
	}

	for _, signal := range cp.StateSignals {
		node := MaterialGraphNode{
			ID:             materialID("state-signal", signal),
			Kind:           "state_signal",
			Label:          strings.TrimSpace(signal),
			Summary:        "State signal attached to this thread.",
			SourceRefs:     sourceRefs,
			SourceStatus:   "source_backed",
			PrivacyTier:    "private",
			Confidence:     confidence,
			Status:         "reviewed",
			Salience:       MaterialGraphSalience{Strategic: "medium", Emotional: "medium", State: "high"},
			ResumeEligible: true,
		}
		if b.addNode(node) {
			b.graph.ContinuityPack.RelevantEmotionalState = appendUniqueContinuityItem(b.graph.ContinuityPack.RelevantEmotionalState, node.Label)
			b.graph.ContinuityPack.MaterialRefs = appendUniqueContinuityItem(b.graph.ContinuityPack.MaterialRefs, node.ID)
		}
	}

	for _, insight := range activeReviewInsights(cp.ReviewInsights, cp.ActiveThreads) {
		node := MaterialGraphNode{
			ID:             materialID("review", insight),
			Kind:           "review",
			Label:          strings.TrimSpace(insight),
			Summary:        "Review insight from an active reviewed thread.",
			SourceRefs:     sourceRefs,
			SourceStatus:   "source_backed",
			PrivacyTier:    "private",
			Confidence:     confidence,
			Status:         "reviewed",
			Salience:       MaterialGraphSalience{Strategic: "medium", Trust: "medium"},
			ResumeEligible: true,
		}
		if b.addNode(node) {
			b.graph.ContinuityPack.ReviewInsights = appendUniqueContinuityItem(b.graph.ContinuityPack.ReviewInsights, node.Label)
			b.graph.ContinuityPack.MaterialRefs = appendUniqueContinuityItem(b.graph.ContinuityPack.MaterialRefs, node.ID)
		}
	}

	for _, ref := range sourceRefs {
		b.graph.ContinuityPack.EvidenceRefs = appendUniqueContinuityItem(b.graph.ContinuityPack.EvidenceRefs, ref)
	}
	b.addOwnershipBoundaryIfPresent(cp, sourceRefs, confidence)
}

func (b *materialGraphBuilder) addOwnershipBoundaryIfPresent(cp ContinuityCheckpoint, sourceRefs []string, confidence float64) {
	text := strings.ToLower(strings.Join([]string{
		cp.Summary,
		strings.Join(cp.Decisions, " "),
		strings.Join(cp.OpenLoops, " "),
		strings.Join(cp.DoNotRepeat, " "),
		strings.Join(cp.EmotionalAnchors, " "),
		strings.Join(cp.StateSignals, " "),
	}, " "))
	hasAtlas := strings.Contains(text, "atlas")
	hasPulse := strings.Contains(text, "pulse")
	hasPeopleGraph := strings.Contains(text, "people graph")
	hasMaterialGraph := strings.Contains(text, "material graph")
	if hasAtlas {
		b.addDerivedNode("project:atlas", "project", "Atlas", "Garden layer that may surface context, but must not own Pulse memory graphs.", sourceRefs, confidence)
	}
	if hasPulse {
		b.addDerivedNode("project:pulse", "project", "Pulse", "Local-first continuity memory and graph evidence layer.", sourceRefs, confidence)
	}
	if hasPeopleGraph {
		b.addDerivedNode("concept:people-graph", "concept", "People Graph", "Material concept that belongs in Pulse-owned memory/graph evidence, not Atlas storage.", sourceRefs, confidence)
	}
	if hasMaterialGraph {
		b.addDerivedNode("concept:material-graph", "concept", "Material Graph", "Pulse projection of what the user is living and working around.", sourceRefs, confidence)
	}
	if hasPeopleGraph && hasPulse {
		b.addEdge(MaterialGraphEdge{
			From:         "concept:people-graph",
			To:           "project:pulse",
			Kind:         "owned_by_layer",
			Summary:      "Hypothesis: People Graph likely belongs to Pulse (inferred from co-occurrence in the checkpoint text, not a reviewed semantic delta).",
			SourceRefs:   sourceRefs,
			SourceStatus: "derived_hypothesis",
			PrivacyTier:  "private",
			Confidence:   confidence,
			Status:       "hypothesis",
		})
	}
	if hasMaterialGraph && hasPulse {
		b.addEdge(MaterialGraphEdge{
			From:         "concept:material-graph",
			To:           "project:pulse",
			Kind:         "owned_by_layer",
			Summary:      "Hypothesis: Material Graph likely belongs to Pulse (inferred from co-occurrence in the checkpoint text, not a reviewed semantic delta).",
			SourceRefs:   sourceRefs,
			SourceStatus: "derived_hypothesis",
			PrivacyTier:  "private",
			Confidence:   confidence,
			Status:       "hypothesis",
		})
	}
	if hasAtlas {
		for _, decision := range cp.Decisions {
			if strings.Contains(strings.ToLower(decision), "atlas") {
				b.addEdge(MaterialGraphEdge{
					From:         materialID("decision", decision),
					To:           "project:atlas",
					Kind:         "do_not_repeat_for",
					Summary:      "Hypothesis: this reviewed decision likely protects the Atlas/Pulse ownership boundary (target inferred from co-occurrence).",
					SourceRefs:   sourceRefs,
					SourceStatus: "derived_hypothesis",
					PrivacyTier:  "private",
					Confidence:   confidence,
					Status:       "hypothesis",
				})
			}
		}
		for _, warning := range cp.DoNotRepeat {
			if strings.Contains(strings.ToLower(warning), "atlas") {
				b.addEdge(MaterialGraphEdge{
					From:         materialID("constraint", warning),
					To:           "project:atlas",
					Kind:         "do_not_repeat_for",
					Summary:      "Hypothesis: this reviewed do-not-repeat warning likely protects the Atlas/Pulse ownership boundary (target inferred from co-occurrence).",
					SourceRefs:   sourceRefs,
					SourceStatus: "derived_hypothesis",
					PrivacyTier:  "private",
					Confidence:   confidence,
					Status:       "hypothesis",
				})
			}
		}
	}
}

// addDerivedNode records an ownership/domain inference produced by the
// hardcoded Atlas/Pulse/people-graph/material-graph co-occurrence heuristic.
// These come from substring matching in checkpoint text, NOT from a reviewed
// semantic delta, so they are labeled honestly as hypotheses rather than
// reviewed truth.
func (b *materialGraphBuilder) addDerivedNode(id, kind, label, summary string, sourceRefs []string, confidence float64) {
	b.addNode(MaterialGraphNode{
		ID:           id,
		Kind:         kind,
		Label:        label,
		Summary:      summary,
		SourceRefs:   sourceRefs,
		SourceStatus: "derived_hypothesis",
		PrivacyTier:  "private",
		Confidence:   confidence,
		Status:       "hypothesis",
		Salience:     MaterialGraphSalience{Strategic: "medium", Trust: "low"},
	})
}

func (s *Store) addStoredMaterialGraphRows(b *materialGraphBuilder, limit int) error {
	if err := s.addMaterialEntityRows(b, limit); err != nil {
		return err
	}
	if err := s.addMaterialRelationRows(b, limit); err != nil {
		return err
	}
	if err := s.addMaterialFactRows(b, limit); err != nil {
		return err
	}
	return s.addMaterialEventRows(b, limit)
}

func (s *Store) addMaterialEntityRows(b *materialGraphBuilder, limit int) error {
	rows, err := s.db.Query(`
		SELECT id, canonical_name, kind, COALESCE(description_md, ''), salience_score, emotional_weight
		  FROM entities
		 WHERE NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = entities.id)
		 ORDER BY salience_score DESC, last_seen DESC, canonical_name ASC
		 LIMIT ?`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var label, kind, summary string
		var salience, emotionalWeight float64
		if err := rows.Scan(&id, &label, &kind, &summary, &salience, &emotionalWeight); err != nil {
			return err
		}
		if !materialVisibleEntityLabel(kind, label) {
			continue
		}
		b.addNode(MaterialGraphNode{
			ID:           materialEntityNodeID(kind, id, label),
			Kind:         materialEntityKind(kind),
			Label:        strings.TrimSpace(label),
			Summary:      strings.TrimSpace(summary),
			SourceRefs:   []string{fmt.Sprintf("pulse:entity:%d", id)},
			SourceStatus: "host_extracted",
			PrivacyTier:  "private",
			Confidence:   materialConfidence(salience),
			Status:       "host_extracted",
			Salience:     materialSalienceFromScores(salience, emotionalWeight),
		})
	}
	return rows.Err()
}

func (s *Store) addMaterialRelationRows(b *materialGraphBuilder, limit int) error {
	rows, err := s.db.Query(`
		SELECT r.id,
		       fe.id, fe.kind, fe.canonical_name,
		       te.id, te.kind, te.canonical_name,
		       r.kind, r.strength
		  FROM relations r
		  JOIN entities fe ON fe.id = r.from_entity_id
		  JOIN entities te ON te.id = r.to_entity_id
		 WHERE NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = fe.id)
		   AND NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = te.id)
		 ORDER BY r.strength DESC, r.last_seen DESC, r.id DESC
		 LIMIT ?`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, fromID, toID int64
		var fromKind, fromLabel, toKind, toLabel, kind string
		var strength float64
		if err := rows.Scan(&id, &fromID, &fromKind, &fromLabel, &toID, &toKind, &toLabel, &kind, &strength); err != nil {
			return err
		}
		if !materialVisibleEntityLabel(fromKind, fromLabel) || !materialVisibleEntityLabel(toKind, toLabel) {
			continue
		}
		edgeKind := materialEdgeKind(kind)
		b.addEdge(MaterialGraphEdge{
			From:         materialEntityNodeID(fromKind, fromID, fromLabel),
			To:           materialEntityNodeID(toKind, toID, toLabel),
			Kind:         edgeKind,
			Summary:      fmt.Sprintf("%s %s %s", strings.TrimSpace(fromLabel), humanizeRelationKind(edgeKind), strings.TrimSpace(toLabel)),
			SourceRefs:   []string{fmt.Sprintf("pulse:relation:%d", id)},
			SourceStatus: "host_extracted",
			PrivacyTier:  "private",
			Confidence:   materialConfidence(strength),
			Status:       "host_extracted",
		})
	}
	return rows.Err()
}

func (s *Store) addMaterialFactRows(b *materialGraphBuilder, limit int) error {
	rows, err := s.db.Query(`
		SELECT f.id, e.id, e.kind, e.canonical_name, f.text, f.confidence, f.verified
		  FROM facts f
		  JOIN entities e ON e.id = f.entity_id
		 WHERE NOT EXISTS (SELECT 1 FROM sensitive_actors sa WHERE sa.entity_id = e.id)
		 ORDER BY f.confidence DESC, f.created_at DESC, f.id DESC
		 LIMIT ?`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var factID, entityID int64
		var entityKind, entityLabel, text string
		var confidence float64
		var verified int
		if err := rows.Scan(&factID, &entityID, &entityKind, &entityLabel, &text, &confidence, &verified); err != nil {
			return err
		}
		if !materialVisibleEntityLabel(entityKind, entityLabel) || !viewerHumanReadableLabel(text) {
			continue
		}
		// Provenance is read from the real `verified` column: a fact the user
		// confirmed is user_confirmed; an unverified host-extracted fact must
		// not be presented as reviewed truth.
		factSourceStatus := "host_extracted"
		factStatus := "host_extracted"
		factSummary := "Host-extracted claim attached to a graph entity."
		if verified != 0 {
			factSourceStatus = "user_confirmed"
			factStatus = "user_confirmed"
			factSummary = "User-confirmed claim attached to a graph entity."
		}
		claimID := materialID("claim", text)
		sourceRefs := []string{fmt.Sprintf("pulse:fact:%d", factID)}
		if b.addNode(MaterialGraphNode{
			ID:           claimID,
			Kind:         "claim",
			Label:        strings.TrimSpace(text),
			Summary:      factSummary,
			SourceRefs:   sourceRefs,
			SourceStatus: factSourceStatus,
			PrivacyTier:  "private",
			Confidence:   materialConfidence(confidence),
			Status:       factStatus,
			Salience:     MaterialGraphSalience{Strategic: "medium"},
		}) {
			b.addEdge(MaterialGraphEdge{
				From:         claimID,
				To:           materialEntityNodeID(entityKind, entityID, entityLabel),
				Kind:         "mentions",
				Summary:      "Claim is attached to the graph entity.",
				SourceRefs:   sourceRefs,
				SourceStatus: factSourceStatus,
				PrivacyTier:  "private",
				Confidence:   materialConfidence(confidence),
				Status:       factStatus,
			})
		}
	}
	return rows.Err()
}

func (s *Store) addMaterialEventRows(b *materialGraphBuilder, limit int) error {
	rows, err := s.db.Query(`
		SELECT e.id, e.title, COALESCE(e.description, ''), e.emotional_weight, e.ts
		  FROM events e
		 WHERE NOT EXISTS (
		       SELECT 1
		         FROM event_entities ee
		         JOIN sensitive_actors sa ON sa.entity_id = ee.entity_id
		        WHERE ee.event_id = e.id)
		 ORDER BY e.ts DESC, e.id DESC
		 LIMIT ?`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var title, summary, ts string
		var emotionalWeight float64
		if err := rows.Scan(&id, &title, &summary, &emotionalWeight, &ts); err != nil {
			return err
		}
		if !viewerHumanReadableLabel(title) {
			continue
		}
		b.addNode(MaterialGraphNode{
			ID:           materialID("event", title),
			Kind:         "event",
			Label:        strings.TrimSpace(title),
			Summary:      strings.TrimSpace(summary),
			SourceRefs:   []string{fmt.Sprintf("pulse:event:%d", id)},
			SourceStatus: "host_extracted",
			PrivacyTier:  "private",
			Confidence:   0.8,
			Status:       "host_extracted",
			Salience:     materialSalienceFromScores(0.5, emotionalWeight),
		})
		_ = ts
	}
	return rows.Err()
}

func (b *materialGraphBuilder) addNode(node MaterialGraphNode) bool {
	node.ID = strings.TrimSpace(node.ID)
	node.Kind = strings.TrimSpace(node.Kind)
	node.Label = strings.TrimSpace(node.Label)
	if node.ID == "" || node.Kind == "" || node.Label == "" {
		return false
	}
	node.SourceRefs = compactMaterialRefs(node.SourceRefs)
	if len(node.SourceRefs) == 0 {
		return false
	}
	if node.SourceStatus == "" {
		node.SourceStatus = "source_backed"
	}
	if node.PrivacyTier == "" {
		node.PrivacyTier = "private"
	}
	if node.Status == "" {
		node.Status = "reviewed"
	}
	if node.Scope == "" {
		node.Scope = b.currentScope()
	}
	node.Confidence = materialConfidence(node.Confidence)

	if idx, ok := b.nodeSeen[node.ID]; ok {
		existing := &b.graph.Nodes[idx]
		if existing.Summary == "" {
			existing.Summary = node.Summary
		}
		existing.SourceRefs = compactMaterialRefs(append(existing.SourceRefs, node.SourceRefs...))
		if node.ResumeEligible {
			existing.ResumeEligible = true
		}
		return true
	}
	if b.limit > 0 && len(b.graph.Nodes) >= b.limit {
		return false
	}
	b.nodeSeen[node.ID] = len(b.graph.Nodes)
	b.graph.Nodes = append(b.graph.Nodes, node)
	return true
}

func (b *materialGraphBuilder) addEdge(edge MaterialGraphEdge) bool {
	edge.From = strings.TrimSpace(edge.From)
	edge.To = strings.TrimSpace(edge.To)
	edge.Kind = materialEdgeKind(edge.Kind)
	if edge.From == "" || edge.To == "" || edge.Kind == "" {
		return false
	}
	edge.SourceRefs = compactMaterialRefs(edge.SourceRefs)
	if len(edge.SourceRefs) == 0 {
		return false
	}
	if edge.SourceStatus == "" {
		edge.SourceStatus = "source_backed"
	}
	if edge.PrivacyTier == "" {
		edge.PrivacyTier = "private"
	}
	if edge.Status == "" {
		edge.Status = "reviewed"
	}
	if edge.Scope == "" {
		edge.Scope = b.currentScope()
	}
	edge.Confidence = materialConfidence(edge.Confidence)
	key := edge.From + "\x00" + edge.Kind + "\x00" + edge.To
	if b.edgeSeen[key] {
		return false
	}
	b.edgeSeen[key] = true
	b.graph.Edges = append(b.graph.Edges, edge)
	return true
}

func materialCheckpointSourceRefs(cp ContinuityCheckpoint) []string {
	refs := compactMaterialRefs(cp.SourceRefs)
	if cp.ID > 0 {
		refs = appendUniqueContinuityItem(refs, fmt.Sprintf("pulse:checkpoint:%d", cp.ID))
	}
	return refs
}

func materialRefsFromResumeSections(sections ResumeSections) []string {
	refs := []string{}
	for _, thread := range sections.ActiveReviewedThreads {
		refs = appendUniqueContinuityItem(refs, materialID("thread", thread))
	}
	for _, decision := range sections.ActiveDecisions {
		refs = appendUniqueContinuityItem(refs, materialID("decision", decision))
	}
	for _, openLoop := range sections.OpenLoops {
		refs = appendUniqueContinuityItem(refs, materialID("open-loop", openLoop))
	}
	for _, warning := range sections.DoNotRepeat {
		refs = appendUniqueContinuityItem(refs, materialID("constraint", warning))
	}
	for _, insight := range sections.ReviewInsights {
		refs = appendUniqueContinuityItem(refs, materialID("review", insight))
	}
	return refs
}

func materialRefsFromCheckpoint(cp ContinuityCheckpoint) []string {
	refs := []string{}
	for _, thread := range cp.ActiveThreads {
		refs = appendUniqueContinuityItem(refs, materialID("thread", thread))
	}
	for _, decision := range cp.Decisions {
		refs = appendUniqueContinuityItem(refs, materialID("decision", decision))
	}
	for _, openLoop := range cp.OpenLoops {
		refs = appendUniqueContinuityItem(refs, materialID("open-loop", openLoop))
	}
	for _, warning := range cp.DoNotRepeat {
		refs = appendUniqueContinuityItem(refs, materialID("constraint", warning))
	}
	for _, anchor := range cp.EmotionalAnchors {
		refs = appendUniqueContinuityItem(refs, materialID("emotion-anchor", anchor))
	}
	for _, signal := range cp.StateSignals {
		refs = appendUniqueContinuityItem(refs, materialID("state-signal", signal))
	}
	for _, insight := range activeReviewInsights(cp.ReviewInsights, cp.ActiveThreads) {
		refs = appendUniqueContinuityItem(refs, materialID("review", insight))
	}
	return refs
}

func materialGraphLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func materialID(kind, label string) string {
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind == "" {
		kind = "item"
	}
	slug := materialSlug(label)
	if slug == "" {
		slug = "item"
	}
	return kind + ":" + slug
}

func materialEntityNodeID(kind string, id int64, label string) string {
	_ = id
	return materialID(materialEntityKind(kind), label)
}

func materialEntityKind(kind string) string {
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "person", "project", "org", "product", "community", "skill", "concept", "place", "event_series":
		return strings.TrimSpace(strings.ToLower(kind))
	case "thing":
		return "concept"
	default:
		return "concept"
	}
}

func materialSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "&", " and ")
	value = materialIDPartPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if len(value) > 80 {
		value = strings.Trim(value[:80], "-")
	}
	return value
}

func materialEdgeKind(kind string) string {
	kind = materialSlug(kind)
	if kind == "" {
		return "related_to"
	}
	switch kind {
	case "mentioned-in":
		return "mentions"
	default:
		return strings.ReplaceAll(kind, "-", "_")
	}
}

func materialVisibleEntityLabel(kind, label string) bool {
	if strings.TrimSpace(kind) == "person" {
		return viewerHumanFacingPersonLabel(label)
	}
	return viewerHumanReadableLabel(label)
}

func materialSalienceFromScores(salience, emotionalWeight float64) MaterialGraphSalience {
	out := MaterialGraphSalience{}
	switch {
	case salience >= 0.75:
		out.Strategic = "high"
	case salience >= 0.35:
		out.Strategic = "medium"
	default:
		out.Strategic = "low"
	}
	switch {
	case emotionalWeight >= 0.75:
		out.Emotional = "high"
	case emotionalWeight >= 0.35:
		out.Emotional = "medium"
	case emotionalWeight > 0:
		out.Emotional = "low"
	}
	return out
}

func materialConfidence(confidence float64) float64 {
	switch {
	case confidence <= 0:
		return 0.8
	case confidence > 1:
		return 1
	default:
		return confidence
	}
}

func compactMaterialRefs(refs []string) []string {
	out := []string{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		out = appendUniqueContinuityItem(out, ref)
	}
	return out
}
