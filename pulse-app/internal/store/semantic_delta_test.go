package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveSemanticDeltaMaterializesGraphAndContinuity(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	res, err := s.SaveSemanticDelta(validSemanticDelta())
	if err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	if res.NodesUpserted != 2 {
		t.Fatalf("expected 2 nodes, got %#v", res)
	}
	if res.EdgesUpserted != 1 {
		t.Fatalf("expected 1 edge, got %#v", res)
	}
	if res.FactsUpserted != 1 {
		t.Fatalf("expected 1 fact, got %#v", res)
	}
	if res.EventsInserted != 1 {
		t.Fatalf("expected 1 event, got %#v", res)
	}
	if !res.CheckpointSaved {
		t.Fatalf("expected continuity checkpoint, got %#v", res)
	}

	var nodes int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM entities WHERE canonical_name IN ('Pulse', 'Host-extracted graph')`).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if nodes != 2 {
		t.Fatalf("expected 2 graph nodes, got %d", nodes)
	}

	var relations int
	if err := s.DB().QueryRow(`
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

	var facts int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM facts WHERE text LIKE '%current host model%'`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("expected fact, got %d", facts)
	}

	resume, err := s.BuildResume(ResumeQuery{ThreadID: "pulse-distribution", TokenBudget: 1200})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(resume.ResumeMarkdown, "Use host subscription for extraction") {
		t.Fatalf("resume missing decision:\n%s", resume.ResumeMarkdown)
	}
	if !strings.Contains(resume.ResumeMarkdown, "Implement pulse_graph_delta") {
		t.Fatalf("resume missing open loop:\n%s", resume.ResumeMarkdown)
	}
}

func TestSaveSemanticDeltaAssimilatesStructuredFactsIntoAssertions(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	delta := validSemanticDelta()
	delta.Source.ProjectID = "garden"
	delta.Facts = []SemanticFact{{
		Node:        "project:pulse",
		Text:        "Pulse canonical memory store is local-first.",
		Predicate:   "canonical memory store",
		ObjectText:  "local-first Pulse store",
		ValidFrom:   "2026-06-30T00:00:00Z",
		Confidence:  0.91,
		PrivacyTier: "private",
		Domain:      "real",
	}}

	res, err := s.SaveSemanticDelta(delta)
	if err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	if res.AssertionsUpserted != 1 {
		t.Fatalf("expected one assertion upserted, got %#v", res)
	}

	key := MakeClaimKey("Pulse", "canonical memory store")
	cur, err := s.CurrentAssertions(key, Scope{Type: "project", ID: "garden"})
	if err != nil {
		t.Fatalf("CurrentAssertions: %v", err)
	}
	if len(cur) != 1 {
		t.Fatalf("expected one current assertion, got %#v", cur)
	}
	got := cur[0]
	if got.ObjectText != "local-first Pulse store" {
		t.Fatalf("assertion object = %q", got.ObjectText)
	}
	if got.ValidFrom != "2026-06-30T00:00:00Z" {
		t.Fatalf("assertion valid_from = %q", got.ValidFrom)
	}
	if got.Confidence != 0.91 {
		t.Fatalf("assertion confidence = %v", got.Confidence)
	}
	if got.ExtractorVersion != SemanticDeltaSchema {
		t.Fatalf("assertion extractor = %q", got.ExtractorVersion)
	}
	if got.Scope.Visibility != "private" {
		t.Fatalf("assertion visibility = %q", got.Scope.Visibility)
	}
	if got.SubjectEntityID == nil {
		t.Fatalf("assertion should keep subject entity id: %#v", got)
	}
	if len(got.SourceEventIDs) != 1 || len(res.EventIDs) != 1 || got.SourceEventIDs[0] != res.EventIDs[0] {
		t.Fatalf("assertion source events = %#v, result events = %#v", got.SourceEventIDs, res.EventIDs)
	}
}

func TestSaveSemanticDeltaSupersedesChangedStructuredAssertion(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	first := validSemanticDelta()
	first.Source.ProjectID = "garden"
	first.Facts = []SemanticFact{{
		Node:        "project:pulse",
		Text:        "Pulse canonical memory store is local.",
		Predicate:   "canonical memory store",
		ObjectText:  "local",
		ValidFrom:   "2026-06-30T00:00:00Z",
		Confidence:  0.8,
		PrivacyTier: "private",
		Domain:      "real",
	}}
	if _, err := s.SaveSemanticDelta(first); err != nil {
		t.Fatalf("SaveSemanticDelta first: %v", err)
	}

	second := validSemanticDelta()
	second.Source.ProjectID = "garden"
	second.Source.Timestamp = "2026-07-01T00:00:00Z"
	second.Events[0].ClientID = "event:pulse-store-hosted-decision"
	second.Events[0].Title = "Pulse hosted store decision"
	second.Facts = []SemanticFact{{
		Node:        "project:pulse",
		Text:        "Pulse canonical memory store is hosted for mobile clients.",
		Predicate:   "canonical memory store",
		ObjectText:  "hosted with authorization",
		ValidFrom:   "2026-07-01T00:00:00Z",
		Confidence:  0.87,
		PrivacyTier: "private",
		Domain:      "real",
	}}
	res, err := s.SaveSemanticDelta(second)
	if err != nil {
		t.Fatalf("SaveSemanticDelta second: %v", err)
	}
	if res.AssertionsUpserted != 1 {
		t.Fatalf("expected one assertion upserted, got %#v", res)
	}

	key := MakeClaimKey("Pulse", "canonical memory store")
	cur, err := s.CurrentAssertions(key, Scope{Type: "project", ID: "garden"})
	if err != nil {
		t.Fatalf("CurrentAssertions: %v", err)
	}
	if len(cur) != 1 || cur[0].ObjectText != "hosted with authorization" {
		t.Fatalf("expected hosted current assertion, got %#v", cur)
	}

	var superseded, active int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM assertions WHERE claim_key=? AND status='superseded'`, key).Scan(&superseded); err != nil {
		t.Fatalf("count superseded: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM assertions WHERE claim_key=? AND status='active'`, key).Scan(&active); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if superseded != 1 || active != 1 {
		t.Fatalf("expected one superseded and one active assertion, got superseded=%d active=%d", superseded, active)
	}
}

func TestSaveSemanticDeltaRejectsNonPersonalAssertionScopeWithoutID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	delta := validSemanticDelta()
	delta.Source.ProjectID = "garden"
	delta.Facts = []SemanticFact{{
		Node:        "project:pulse",
		Text:        "Pulse canonical memory store is local-first.",
		Predicate:   "canonical memory store",
		ObjectText:  "local-first Pulse store",
		ScopeType:   "project",
		Confidence:  0.91,
		PrivacyTier: "private",
		Domain:      "real",
	}}

	_, err = s.SaveSemanticDelta(delta)
	if err == nil {
		t.Fatal("expected project scope without scope_id to be rejected")
	}
	if !strings.Contains(err.Error(), "scope_id") {
		t.Fatalf("expected scope_id error, got %v", err)
	}
}

func TestSaveSemanticDeltaPreservesZeroConfidenceAssertion(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	delta := validSemanticDelta()
	delta.Source.ProjectID = "garden"
	delta.Facts = []SemanticFact{{
		Node:        "project:pulse",
		Text:        "Pulse canonical memory store is uncertain.",
		Predicate:   "canonical memory store",
		ObjectText:  "uncertain",
		Confidence:  0,
		PrivacyTier: "private",
		Domain:      "real",
	}}

	if _, err := s.SaveSemanticDelta(delta); err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	key := MakeClaimKey("Pulse", "canonical memory store")
	cur, err := s.CurrentAssertions(key, Scope{Type: "project", ID: "garden"})
	if err != nil {
		t.Fatalf("CurrentAssertions: %v", err)
	}
	if len(cur) != 1 {
		t.Fatalf("expected one current assertion, got %#v", cur)
	}
	if cur[0].Confidence != 0 {
		t.Fatalf("expected zero confidence to stay zero, got %v", cur[0].Confidence)
	}
}

func TestSaveSemanticDeltaLinksAssertionToDeclaredSourceEventsOnly(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	delta := validSemanticDelta()
	delta.Source.ProjectID = "garden"
	delta.Events = []SemanticEvent{
		{
			ClientID:    "event:vitaly-call",
			Title:       "Vitaly call",
			Summary:     "Vitaly reviewed the wedge.",
			EntityRefs:  []string{"project:pulse"},
			Confidence:  0.83,
			PrivacyTier: "private",
			Domain:      "real",
		},
		{
			ClientID:    "event:material-graph-plan",
			Title:       "Material Graph plan",
			Summary:     "Pulse planned assertion-backed graph projection.",
			EntityRefs:  []string{"project:pulse"},
			Confidence:  0.88,
			PrivacyTier: "private",
			Domain:      "real",
		},
	}
	delta.Facts = []SemanticFact{
		{
			Node:            "project:pulse",
			Text:            "Vitaly rejected the emotional companion wedge.",
			Predicate:       "review verdict",
			ObjectText:      "needs wedge hardening",
			SourceEventRefs: []string{"event:vitaly-call"},
			Confidence:      0.82,
			PrivacyTier:     "private",
			Domain:          "real",
		},
		{
			Node:            "project:pulse",
			Text:            "Pulse should project assertions into Material Graph next.",
			Predicate:       "next layer",
			ObjectText:      "assertion-backed material graph",
			SourceEventRefs: []string{"event:material-graph-plan"},
			Confidence:      0.86,
			PrivacyTier:     "private",
			Domain:          "real",
		},
	}

	res, err := s.SaveSemanticDelta(delta)
	if err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	if len(res.EventIDs) != 2 {
		t.Fatalf("expected two inserted events, got %#v", res.EventIDs)
	}

	reviewClaims, err := s.CurrentAssertions(MakeClaimKey("Pulse", "review verdict"), Scope{Type: "project", ID: "garden"})
	if err != nil {
		t.Fatalf("CurrentAssertions review verdict: %v", err)
	}
	nextClaims, err := s.CurrentAssertions(MakeClaimKey("Pulse", "next layer"), Scope{Type: "project", ID: "garden"})
	if err != nil {
		t.Fatalf("CurrentAssertions next layer: %v", err)
	}
	if len(reviewClaims) != 1 || len(nextClaims) != 1 {
		t.Fatalf("expected one assertion per claim, got review=%#v next=%#v", reviewClaims, nextClaims)
	}
	if got := reviewClaims[0].SourceEventIDs; len(got) != 1 || got[0] != res.EventIDs[0] {
		t.Fatalf("review claim source events = %#v, want only %#v", got, res.EventIDs[:1])
	}
	if got := nextClaims[0].SourceEventIDs; len(got) != 1 || got[0] != res.EventIDs[1] {
		t.Fatalf("next claim source events = %#v, want only %#v", got, res.EventIDs[1:])
	}
}

func TestSaveSemanticDeltaMergesPersonAliasesForViewer(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	delta := validSemanticDelta()
	delta.Nodes = []SemanticNode{
		{
			ClientID:        "person:nik",
			Kind:            "person",
			CanonicalName:   "Nik",
			Aliases:         []string{"Nikita", "Ник", "Никита"},
			Summary:         "Primary user profile from safe archive preview.",
			Salience:        0.9,
			EmotionalWeight: 0.7,
			PrivacyTier:     "normal",
			Domain:          "real",
		},
		{
			ClientID:        "person:nikita",
			Kind:            "person",
			CanonicalName:   "Nikita",
			Summary:         "Duplicate English name variant from archive preview.",
			Salience:        0.4,
			EmotionalWeight: 0.2,
			PrivacyTier:     "normal",
			Domain:          "real",
		},
		{
			ClientID:        "person:nikita-ru",
			Kind:            "person",
			CanonicalName:   "Никита",
			Summary:         "Duplicate Russian name variant from archive preview.",
			Salience:        0.5,
			EmotionalWeight: 0.3,
			PrivacyTier:     "normal",
			Domain:          "real",
		},
		{
			ClientID:      "project:pulse-dashboard",
			Kind:          "project",
			CanonicalName: "Pulse Dashboard",
			Summary:       "Viewer for cleaned Pulse memory graph.",
			PrivacyTier:   "normal",
			Domain:        "real",
		},
	}
	delta.Edges = []SemanticEdge{{
		From:        "person:nikita-ru",
		To:          "project:pulse-dashboard",
		Kind:        "mentioned_in",
		Summary:     "Nikita is connected to Pulse Dashboard work.",
		Strength:    0.6,
		PrivacyTier: "normal",
	}}
	delta.Facts = []SemanticFact{{
		Node:        "person:nikita",
		Text:        "Nikita appeared in 8 safe preview signals",
		Confidence:  0.7,
		PrivacyTier: "normal",
		Domain:      "real",
	}}
	delta.Events = []SemanticEvent{{
		ClientID:        "event:pulse-dashboard-cleanup",
		Title:           "Pulse Dashboard cleanup",
		Summary:         "Cleaned duplicate person variants before showing the dashboard.",
		EntityRefs:      []string{"person:nik", "person:nikita", "person:nikita-ru", "project:pulse-dashboard"},
		Sentiment:       "relief",
		EmotionalWeight: 0.5,
		Confidence:      0.7,
		PrivacyTier:     "normal",
		Domain:          "real",
	}}

	if _, err := s.SaveSemanticDelta(delta); err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	graph, err := s.ViewerGraphProfile(24)
	if err != nil {
		t.Fatalf("ViewerGraphProfile: %v", err)
	}
	if len(graph.People) != 1 {
		t.Fatalf("viewer should merge Nik/Nikita/Никита into one person, got %#v", graph.People)
	}
	if graph.People[0] != "Nik" {
		t.Fatalf("viewer should keep the first canonical display name, got %#v", graph.People)
	}
	if len(graph.PersonProfiles) != 1 {
		t.Fatalf("viewer should expose one merged profile, got %#v", graph.PersonProfiles)
	}
	profile := graph.PersonProfiles[0]
	if !containsString(profile.Aliases, "Nikita") || !containsString(profile.Aliases, "Никита") {
		t.Fatalf("merged profile should expose aliases for review: %#v", profile)
	}
	if !containsString(profile.Facts, "Nikita appeared in 8 safe preview signals") {
		t.Fatalf("merged profile should include duplicate-alias fact: %#v", profile)
	}
	if !containsString(profile.Relationships, "Nik mentioned in Pulse Dashboard") {
		t.Fatalf("merged profile should include readable relation from alias edge: %#v", profile)
	}
	if !containsString(profile.Memories, "Pulse Dashboard cleanup") {
		t.Fatalf("merged profile should include event from all alias refs: %#v", profile)
	}
}

func TestViewerGraphProfileFiltersTechnicalAndPromptLikePeople(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	delta := validSemanticDelta()
	delta.Nodes = []SemanticNode{
		{
			ClientID:      "person:nikita",
			Kind:          "person",
			CanonicalName: "Nikita",
			Summary:       "Real person profile from reviewed archive import.",
			PrivacyTier:   "normal",
			Domain:        "real",
		},
		{
			ClientID:      "person:uuid",
			Kind:          "person",
			CanonicalName: "09c3f230-a42f-4dc7-a27e-4e60cc4f01d4",
			Summary:       "Technical identifier that should not become a visible person.",
			PrivacyTier:   "normal",
			Domain:        "real",
		},
		{
			ClientID:      "person:prompt-word",
			Kind:          "person",
			CanonicalName: "Назови",
			Summary:       "Prompt verb that should stay out of the people surface.",
			PrivacyTier:   "normal",
			Domain:        "real",
		},
	}
	delta.Edges = []SemanticEdge{
		{From: "person:nikita", To: "person:uuid", Kind: "related_to", PrivacyTier: "normal"},
		{From: "person:prompt-word", To: "person:nikita", Kind: "related_to", PrivacyTier: "normal"},
	}
	delta.Facts = []SemanticFact{
		{Node: "person:nikita", Text: "Nikita appeared in 8 safe preview signals", Confidence: 0.7, PrivacyTier: "normal", Domain: "real"},
		{Node: "person:uuid", Text: "source:memory/contacts/personal-contacts.md", Confidence: 0.7, PrivacyTier: "normal", Domain: "real"},
	}
	delta.Events = nil
	delta.Continuity = nil

	if _, err := s.SaveSemanticDelta(delta); err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	graph, err := s.ViewerGraphProfile(24)
	if err != nil {
		t.Fatalf("ViewerGraphProfile: %v", err)
	}
	if !containsString(graph.People, "Nikita") || len(graph.People) != 1 {
		t.Fatalf("viewer should only show the real person label, got %#v", graph.People)
	}
	for _, items := range [][]string{graph.People, graph.Relationships, graph.FunFacts} {
		if containsString(items, "09c3f230") || containsString(items, "Назови") || containsString(items, "source:memory") {
			t.Fatalf("viewer leaked technical or prompt-like graph material: %#v", graph)
		}
	}
}

func TestViewerGraphProfileSplitsImportedEmotionLayers(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	delta := validSemanticDelta()
	delta.Events = []SemanticEvent{
		{
			ClientID:        "event:emotion-layers",
			Title:           "Pulse emotion layer import",
			Summary:         "Imported safe emotional signals from archive preview.",
			EntityRefs:      []string{"project:pulse"},
			Sentiment:       "relief, joy, anxiety, relief",
			EmotionalWeight: 0.7,
			Confidence:      0.7,
			PrivacyTier:     "normal",
			Domain:          "real",
		},
	}

	if _, err := s.SaveSemanticDelta(delta); err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	graph, err := s.ViewerGraphProfile(24)
	if err != nil {
		t.Fatalf("ViewerGraphProfile: %v", err)
	}
	for _, emotion := range []string{"relief", "joy", "anxiety"} {
		if !containsString(graph.Emotions, emotion) {
			t.Fatalf("viewer emotions missing %q: %#v", emotion, graph.Emotions)
		}
	}
	if len(graph.Emotions) != 3 {
		t.Fatalf("viewer should expose deduped emotion layers, got %#v", graph.Emotions)
	}
}

func TestSaveSemanticDeltaRejectsUnsafePayloads(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	cases := []struct {
		name string
		edit func(*SemanticDelta)
	}{
		{"raw flag", func(d *SemanticDelta) { d.RawInputIncluded = true }},
		{"secret fact", func(d *SemanticDelta) { d.Facts[0].Text = "api_key=abc" }},
		{"local path node summary", func(d *SemanticDelta) { d.Nodes[0].Summary = "/Users/nik/private" }},
		{"bad timestamp", func(d *SemanticDelta) { d.Source.Timestamp = "today" }},
		{"unknown edge ref", func(d *SemanticDelta) { d.Edges[0].To = "missing:node" }},
		{"empty delta", func(d *SemanticDelta) {
			d.Nodes = nil
			d.Edges = nil
			d.Facts = nil
			d.Events = nil
			d.Continuity = nil
		}},
		{"transcript event", func(d *SemanticDelta) {
			d.Events[0].Summary = strings.Repeat("User: hello\nAssistant: hi\n", 80)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			delta := validSemanticDelta()
			tc.edit(&delta)
			if _, err := s.SaveSemanticDelta(delta); err == nil {
				t.Fatalf("expected rejection")
			}
		})
	}
}

func TestSaveSemanticDeltaAllowsContinuityOnly(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	delta := validSemanticDelta()
	delta.Nodes = nil
	delta.Edges = nil
	delta.Facts = nil
	delta.Events = nil
	delta.Continuity = &SemanticContinuity{
		Summary:          "Claude captured a durable continuity checkpoint without graph entities.",
		Decisions:        []string{"Allow continuity-only semantic deltas."},
		OpenLoops:        []string{"Keep public MCP easy for ordinary Claude Chat."},
		DoNotRepeat:      []string{"Do not force artificial nodes for every checkpoint."},
		EmotionalAnchors: []string{"Pulse should feel effortless."},
		StateSignals:     []string{"Claude Chat connector path is dev-proof, not store-grade yet."},
	}

	res, err := s.SaveSemanticDelta(delta)
	if err != nil {
		t.Fatalf("SaveSemanticDelta continuity-only: %v", err)
	}
	if res.NodesUpserted != 0 || res.EdgesUpserted != 0 || res.FactsUpserted != 0 || res.EventsInserted != 0 {
		t.Fatalf("expected no graph writes, got %#v", res)
	}
	if !res.CheckpointSaved {
		t.Fatalf("expected checkpoint, got %#v", res)
	}

	resume, err := s.BuildResume(ResumeQuery{ThreadID: "pulse-distribution", TokenBudget: 1200})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(resume.ResumeMarkdown, "Allow continuity-only semantic deltas") {
		t.Fatalf("resume missing continuity-only decision:\n%s", resume.ResumeMarkdown)
	}
}

func TestSaveSemanticDeltaReviewInsightBecomesActiveResumeThread(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	delta := validSemanticDelta()
	delta.Continuity.ActiveThreads = []string{"Garden launch"}
	delta.Continuity.ReviewInsights = []string{
		"Pulse insight: Why this may matter now: Garden launch. Next: Review this thread before import.",
		"Pulse insight: Why this may matter now: Codex entity hygiene session. Next: Review this thread before import.",
		"Active thread: Garden launch. User marked the Pulse insight as active during review.",
	}

	if _, err := s.SaveSemanticDelta(delta); err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}

	resume, err := s.BuildResume(ResumeQuery{ThreadID: "pulse-distribution", TokenBudget: 1200})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(resume.ResumeMarkdown, "## Active reviewed threads") {
		t.Fatalf("resume missing active reviewed threads section:\n%s", resume.ResumeMarkdown)
	}
	if !strings.Contains(resume.ResumeMarkdown, "Garden launch") {
		t.Fatalf("resume missing active thread:\n%s", resume.ResumeMarkdown)
	}
	if !strings.Contains(resume.ResumeMarkdown, "## Review insights") {
		t.Fatalf("resume missing review insights section:\n%s", resume.ResumeMarkdown)
	}
	if !strings.Contains(resume.ResumeMarkdown, "Why this may matter now: Garden launch") {
		t.Fatalf("resume missing review insight:\n%s", resume.ResumeMarkdown)
	}
	if strings.Contains(resume.ResumeMarkdown, "Codex entity hygiene session") {
		t.Fatalf("inactive review insight leaked into resume:\n%s", resume.ResumeMarkdown)
	}
	for _, item := range resume.Sections.ReviewInsights {
		if strings.Contains(item, "Codex entity hygiene session") {
			t.Fatalf("inactive review insight leaked into sections: %#v", resume.Sections.ReviewInsights)
		}
	}
	for _, item := range resume.Sections.RelevantEmotionalState {
		if strings.Contains(item, "Pulse insight:") || strings.Contains(item, "Active thread:") {
			t.Fatalf("review insight leaked into emotional state: %#v", resume.Sections.RelevantEmotionalState)
		}
	}
}

func TestWipeMemoryRemovesHostExtractedSemanticGraph(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, err := s.SaveSemanticDelta(validSemanticDelta()); err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	if err := s.WipeMemory(); err != nil {
		t.Fatalf("wipe: %v", err)
	}

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"entities", `SELECT COUNT(*) FROM entities WHERE scorer_version='host-extracted'`},
		{"relations", `SELECT COUNT(*) FROM relations`},
		{"facts", `SELECT COUNT(*) FROM facts WHERE scorer_version='host-extracted' OR extractor_version='pulse.semantic_delta.v1'`},
		{"assertions", `SELECT COUNT(*) FROM assertions WHERE extractor_version='pulse.semantic_delta.v1'`},
		{"events", `SELECT COUNT(*) FROM events WHERE scorer_version='host-extracted'`},
		{"checkpoints", `SELECT COUNT(*) FROM continuity_checkpoints`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count int
			if err := s.DB().QueryRow(tc.query).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("expected wipe to remove %s, got %d", tc.name, count)
			}
		})
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if strings.Contains(item, want) {
			return true
		}
	}
	return false
}

func validSemanticDelta() SemanticDelta {
	return SemanticDelta{
		Schema: SemanticDeltaSchema,
		Source: SemanticDeltaSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         "2026-06-02T16:00:00Z",
			ThreadID:          "pulse-distribution",
			SessionID:         "claude-code:pulse-distribution:test",
			ProjectID:         "pulse-public-clean",
		},
		Nodes: []SemanticNode{
			{
				ClientID:        "project:pulse",
				Kind:            "project",
				CanonicalName:   "Pulse",
				Summary:         "Pulse keeps memory and continuity across AI harnesses.",
				Aliases:         []string{"Pulse MCP"},
				Salience:        0.8,
				EmotionalWeight: 0.4,
				PrivacyTier:     "normal",
				Domain:          "real",
			},
			{
				ClientID:      "concept:host-extracted-graph",
				Kind:          "concept",
				CanonicalName: "Host-extracted graph",
				Summary:       "The active host model extracts graph deltas while Pulse stores them.",
				PrivacyTier:   "normal",
				Domain:        "real",
			},
		},
		Edges: []SemanticEdge{
			{
				From:        "project:pulse",
				To:          "concept:host-extracted-graph",
				Kind:        "implements",
				Summary:     "Pulse stores graph deltas extracted by the active host model.",
				Strength:    0.8,
				PrivacyTier: "normal",
			},
		},
		Facts: []SemanticFact{
			{
				Node:        "project:pulse",
				Text:        "Pulse should build semantic graph structure while ingestion is performed by the current host model.",
				Confidence:  0.9,
				PrivacyTier: "normal",
				Domain:      "real",
			},
		},
		Events: []SemanticEvent{
			{
				ClientID:        "event:pulse-graph-ingestion-decision",
				Title:           "Pulse graph ingestion decision",
				Summary:         "We decided host models ingest meaning and Pulse owns graph storage.",
				EntityRefs:      []string{"project:pulse", "concept:host-extracted-graph"},
				EmotionalWeight: 0.3,
				Confidence:      0.9,
				PrivacyTier:     "normal",
				Domain:          "real",
			},
		},
		Continuity: &SemanticContinuity{
			Summary:          "We moved from API-only memory ingestion to host-extracted graph deltas.",
			Decisions:        []string{"Use host subscription for extraction, not Pulse backend LLM by default."},
			OpenLoops:        []string{"Implement pulse_graph_delta and materialize it into graph tables."},
			DoNotRepeat:      []string{"Do not pitch Pulse as generic memory without continuity."},
			EmotionalAnchors: []string{"The product should feel easy and continuous, not like another memory chore."},
			StateSignals:     []string{"Claude Code first; remote Claude Chat second."},
		},
		RawInputIncluded: false,
	}
}
