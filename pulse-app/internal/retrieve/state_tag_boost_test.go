package retrieve

import (
	"context"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

// Fixture texts are neutral/synthetic (generic release advice) — no personal
// data may ever enter this public repo's tests.

func stateTagEngine(tags map[int64][]string) *Engine {
	ids := []int64{1, 2, 3}
	e := &Engine{eventIDs: ids}
	for _, id := range ids {
		e.eventTags = append(e.eventTags, tags[id])
	}
	return e
}

func assertSameOrder(t *testing.T, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestStateTagBoost_NoUserStateIsIdentity(t *testing.T) {
	e := stateTagEngine(map[int64][]string{2: {"state:deadline_pressure"}})
	in := []int64{1, 2, 3}
	out, mults := e.applyStateTagBoost(in, nil, "")
	assertSameOrder(t, out, in)
	if mults != nil {
		t.Fatalf("neutral pass must not report multipliers: %v", mults)
	}
}

func TestStateTagBoost_UntaggedEventsAreIdentity(t *testing.T) {
	e := stateTagEngine(nil) // no event carries any tag
	in := []int64{1, 2, 3}
	state := &UserState{ContextFlags: map[string]float64{"deadline_pressure": 0.9}}
	out, mults := e.applyStateTagBoost(in, state, "")
	assertSameOrder(t, out, in)
	if mults != nil {
		t.Fatalf("neutral pass must not report multipliers: %v", mults)
	}
}

func TestStateTagBoost_NonMatchingAndInactiveFlagsAreIdentity(t *testing.T) {
	e := stateTagEngine(map[int64][]string{
		2: {"state:deadline_pressure"},
		3: {"state:calm"},
	})
	in := []int64{1, 2, 3}
	// Flag present but below the 0.5 activity floor ⇒ deadline tag inert; and
	// because the flag map is non-empty-but-inactive, calm... calm fires only
	// when NO flag is active — an inactive flag map counts as "none active",
	// so calm DOES apply here. Use a genuinely active non-matching flag
	// instead to assert full neutrality.
	state := &UserState{ContextFlags: map[string]float64{"burnout": 0.9}}
	out, mults := e.applyStateTagBoost(in, state, "")
	assertSameOrder(t, out, in)
	if mults != nil {
		t.Fatalf("neutral pass must not report multipliers: %v", mults)
	}
}

func TestStateTagBoost_ActiveFlagPicksMatchingTag(t *testing.T) {
	e := stateTagEngine(map[int64][]string{
		2: {"advice", "state:deadline_pressure"},
		3: {"state:calm"},
	})
	in := []int64{1, 2, 3}
	state := &UserState{ContextFlags: map[string]float64{"deadline_pressure": 0.9}}
	out, mults := e.applyStateTagBoost(in, state, "")
	assertSameOrder(t, out, []int64{2, 1, 3})
	if m := mults[2]; m != stateTagBoostMult {
		t.Fatalf("mult[2] = %v, want %v", m, stateTagBoostMult)
	}
	if m := mults[3]; m != 1.0 {
		t.Fatalf("calm must stay neutral while a flag is active, got %v", m)
	}
}

func TestStateTagBoost_CalmWinsWhenStatePresentAndNoActiveFlag(t *testing.T) {
	e := stateTagEngine(map[int64][]string{
		2: {"state:deadline_pressure"},
		3: {"state:calm"},
	})
	in := []int64{1, 2, 3}
	// user_state present, no active context flag (empty map) ⇒ calm affinity.
	state := &UserState{ContextFlags: map[string]float64{}}
	out, mults := e.applyStateTagBoost(in, state, "")
	assertSameOrder(t, out, []int64{3, 1, 2})
	if m := mults[3]; m != stateTagBoostMult {
		t.Fatalf("mult[3] = %v, want %v", m, stateTagBoostMult)
	}
	if m := mults[2]; m != 1.0 {
		t.Fatalf("deadline tag must stay neutral without its flag, got %v", m)
	}
}

// setupStateTagStore seeds three same-timestamp events whose embeddings are
// EXACTLY tied for any query (identical vectors), each tagged for a different
// state — the near-tie eval shape. Also returns nothing else: engine tests
// load via Init.
func setupStateTagStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/state-tag.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	db := s.DB()
	rows := []struct {
		id    int64
		title string
		tags  string
	}{
		{1, "checklist advice for the release", `["state:deadline_pressure"]`},
		{2, "triage advice for the release", `["state:calm"]`},
		{3, "ownership advice for the release", `["state:job_insecurity"]`},
	}
	vec := []float32{1, 0, 0, 0, 0}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO events(id, title, ts, tags) VALUES (?, ?, ?, ?)`,
			r.id, r.title, "2026-07-01T09:00:00Z", r.tags); err != nil {
			t.Fatalf("insert event %d: %v", r.id, err)
		}
		if _, err := db.Exec(
			`INSERT INTO event_embeddings(event_id, model, dim, vector_json, text_source, updated_at)
			 VALUES (?, ?, ?, '[1,0,0,0,0]', ?, ?)`,
			r.id, "fake-embed", len(vec), r.title, "2026-07-01T09:00:00Z"); err != nil {
			t.Fatalf("insert embedding %d: %v", r.id, err)
		}
	}
	return s
}

func retrieveOrder(t *testing.T, eng *Engine, state *UserState) []int64 {
	t.Helper()
	resp, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query:     "advice for the upcoming release",
		Mode:      ModeEmpathic,
		TopK:      3,
		UserState: state,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	return resp.EventIDs
}

func TestStateTagBoost_EnvOptOutIsIdentity(t *testing.T) {
	s := setupStateTagStore(t)
	now := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)

	// Baseline: the frozen pipeline with no user_state at all.
	t.Setenv("PULSE_STATE_TAG_BOOST", "")
	base := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &now})
	if err := base.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	baseline := retrieveOrder(t, base, nil)

	// Flag off: even with an active context flag the order stays identical.
	t.Setenv("PULSE_STATE_TAG_BOOST", "off")
	off := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &now})
	if err := off.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	got := retrieveOrder(t, off, &UserState{
		ContextFlags: map[string]float64{"deadline_pressure": 0.9},
	})
	assertSameOrder(t, got, baseline)

	// Sanity: default-on with no user_state is also byte-identical.
	assertSameOrder(t, retrieveOrder(t, base, nil), baseline)
}

// TestStateTagBoost_EvalShapedThreeStatesThreeWinners is the eval-shaped
// integration test: three near-tie remembered capsules + three user states ⇒
// three different correct top-1 answers, through the full RememberCapsule →
// projection → EmbedAndIndexEvents → Retrieve path.
func TestStateTagBoost_EvalShapedThreeStatesThreeWinners(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/eval-shaped.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	item := func(summary string, tags ...string) store.MemoryCapsuleItem {
		return store.MemoryCapsuleItem{
			Kind:            "decision",
			RedactedSummary: summary,
			Confidence:      0.9,
			EvidenceHint:    "current_turn",
			PrivacyTier:     "normal",
			Retention:       "project",
			Tags:            tags,
		}
	}
	ids, err := s.RememberCapsule(store.MemoryCapsule{
		Schema: "pulse.memory_capsule.v1",
		Source: store.CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         "2026-07-01T09:00:00Z",
		},
		Items: []store.MemoryCapsuleItem{
			item("Cut scope to the committed checklist and ship the smallest safe release.",
				"advice", "state:deadline_pressure"),
			item("Plan the triage: sort open items by impact before the release.",
				"advice", "state:calm"),
			item("Write decisions down and confirm ownership in writing before the release.",
				"advice", "state:job_insecurity"),
		},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	docs, err := s.CapsuleEventDocs(ids)
	if err != nil {
		t.Fatalf("capsule event docs: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("expected 3 projected docs, got %d", len(docs))
	}

	now := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	eng := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &now})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Same embed-index path the /memory/remember handler uses. All three docs
	// share the "decision\n" prefix, so the fake embedder (first-5-runes)
	// yields EXACT cosine ties — only the state channel can separate them.
	indexDocs := make([]IndexEventDoc, len(docs))
	for i, d := range docs {
		indexDocs[i] = IndexEventDoc{EventID: d.EventID, Text: d.Text}
	}
	if err := eng.EmbedAndIndexEvents(context.Background(), indexDocs); err != nil {
		t.Fatalf("EmbedAndIndexEvents: %v", err)
	}

	checklist, triage, ownership := docs[0].EventID, docs[1].EventID, docs[2].EventID
	cases := []struct {
		name  string
		state *UserState
		want  int64
	}{
		{"deadline_pressure", &UserState{ContextFlags: map[string]float64{"deadline_pressure": 0.9}}, checklist},
		{"job_insecurity", &UserState{ContextFlags: map[string]float64{"job_insecurity": 0.8}}, ownership},
		{"calm", &UserState{ContextFlags: map[string]float64{"deadline_pressure": 0.1}}, triage},
	}
	seen := map[int64]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := retrieveOrder(t, eng, tc.state)
			if len(got) == 0 || got[0] != tc.want {
				t.Fatalf("top-1 = %v, want event %d (%s)", got, tc.want, tc.name)
			}
			seen[got[0]] = true
		})
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 different top-1 winners across states, got %d", len(seen))
	}
}

// The v1 positional multiplier could lift a match by at most 4 ranks; the
// partition must bring an explicitly-labeled match to the front from ANY
// depth. Would fail under the old ×1.15·ρ^rank mechanics.
func TestStateTagBoost_PartitionLiftsMatchFromAnyDepth(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5, 6, 7}
	e := &Engine{eventIDs: ids}
	for _, id := range ids {
		if id == 7 {
			e.eventTags = append(e.eventTags, []string{"state:deadline_pressure"})
		} else {
			e.eventTags = append(e.eventTags, nil)
		}
	}
	state := &UserState{ContextFlags: map[string]float64{"deadline_pressure": 0.9}}
	out, _ := e.applyStateTagBoost(ids, state, "")
	assertSameOrder(t, out, []int64{7, 1, 2, 3, 4, 5, 6})
}

// Inside an active state group the lexical tie-break must prefer the item
// whose text covers the query terms, prefix-tolerant (Russian inflections).
func TestStateTagBoost_LexicalTieBreakInsideActiveGroup(t *testing.T) {
	ids := []int64{1, 2, 3}
	e := &Engine{eventIDs: ids}
	tag := []string{"state:deadline_pressure"}
	e.eventTags = [][]string{nil, tag, tag}
	// e.eventTexts is stored lowercased; id 3 covers "релиз" (inflected), id 2
	// is about an unrelated incident.
	e.eventTexts = []string{"", "мажорный инцидент стабилизируй сначала", "срок горит не тащи в этот релиз лишнего"}
	state := &UserState{ContextFlags: map[string]float64{"deadline_pressure": 0.9}}
	out, _ := e.applyStateTagBoost(ids, state, "Как мне вести этот релиз?")
	// Both 2 and 3 partition above 1; lexical coverage puts 3 first.
	assertSameOrder(t, out, []int64{3, 2, 1})
}

// On the calm path the coherence tie-break must prefer the calm item most
// similar to the frozen ranking's own top-1 (the thematic anchor).
func TestStateTagBoost_CalmCoherenceTieBreakToAnchor(t *testing.T) {
	ids := []int64{1, 2, 3}
	e := &Engine{eventIDs: ids}
	calm := []string{"state:calm"}
	e.eventTags = [][]string{nil, calm, calm}
	// Unit vectors: anchor = id 1; id 3 is aligned with the anchor, id 2 is
	// orthogonal — coherence must put 3 first inside the calm group.
	e.eventVecs = [][]float32{{1, 0}, {0, 1}, {1, 0}}
	state := &UserState{ContextFlags: map[string]float64{}}
	out, _ := e.applyStateTagBoost(ids, state, "")
	assertSameOrder(t, out, []int64{3, 2, 1})
}
