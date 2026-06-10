package retrieve

// Go==Python v3 retrieval parity test — the acceptance gate for the v3-boost port.
//
// It loads the frozen golden reference produced by the reference Python
// dump script into an in-memory store
// with every migration applied (incl. 021_event_state_fields), inserts each
// corpus event with its FROZEN golden document embedding, emotions, chain edges,
// and the v3 state fields (user_flag / sentiment_label / biometric_json), then
// for each of the 35 tests:
//
//   - rebuilds UserState in Go from the golden's merged user_state (mood_vector
//     path — the bench's deterministic config, use_llm_query_emo=False),
//   - feeds the engine the FROZEN per-test query_vector (the emotion-hint
//     augmentation already changed the text on the Python side, so we must use
//     the exact vector that was embedded, not re-embed),
//   - scores via the SAME internal methods the production engine uses
//     (scoreEventsV3 for the per-event score map, retrieveChain for chain top5),
//   - asserts per-event |score_go - score_py| < 1e-5 and Go top-5 == Python top-5.
//
// The score map and top5 are compared against the raw v3 multiplicative formula
// (return_scores path, no BM25/RRF/surfaceability — those layers live above the
// scoring substrate and the golden does not model them, so we exercise the
// scoring methods directly rather than Retrieve()).
//
// All 35 tests match the Python reference to float precision (maxErr ~1e-6).
//
// History worth keeping: T26/T33 initially diverged (~0.09) and an earlier pass
// mislabeled it "MLX embedding non-determinism". The real cause was a genuine Go
// port GAP — the keyword-emotion fallback branch was missing (Python infers query
// emotion from the text via EMO_KEYWORDS when no mood_vector is given; e.g.
// a shame-marker keyword → shame). After porting that branch (prepareQueryEmotion's
// keyword path) both pass strict. The lesson: a real port bug almost got laundered
// into a "documented divergence" — never excuse a divergence without finding its
// deterministic cause.
//
// The only non-strict case left is T28, a legitimate chain-ordering tie (see
// parityChainTieTests): Go and Python differ only by swapping events fully tied on
// the chain sort key (CPython set-iteration vs Go id-stable order).

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/store"
)

// ── Golden file schema ──────────────────────────────────────────────────────

type goldenFile struct {
	Meta            map[string]any       `json:"_meta"`
	EventEmbeddings map[string][]float32 `json:"event_embeddings"`
	Tests           []goldenTest         `json:"tests"`
}

type goldenTest struct {
	TestID         string             `json:"test_id"`
	TestName       string             `json:"test_name"`
	TestType       string             `json:"test_type"`
	Query          string             `json:"query"`
	EffectiveQuery string             `json:"effective_query"`
	HintApplied    bool               `json:"hint_applied"`
	QueryVector    []float32          `json:"query_vector"`
	UserState      *goldenUserState   `json:"user_state"`
	Biometric      map[string]any     `json:"biometric"`
	ExpandChain    bool               `json:"expand_chain"`
	Scores         map[string]float64 `json:"scores"`
	Top5           []int64            `json:"top5"`
}

// goldenUserState is the MERGED state the Python bench built (user_state dict +
// biometric overlay folded in by build_user_state, bench:202-225). It maps 1:1
// to the fields we hydrate into retrieve.UserState.
type goldenUserState struct {
	MoodVector         map[string]float64 `json:"mood_vector"`
	SleepQuality       *float64           `json:"sleep_quality"`
	SleepHours         *float64           `json:"sleep_hours"`
	HRV                *float64           `json:"hrv"`
	HRTrend            *string            `json:"hr_trend"`
	HRVTrend           *string            `json:"hrv_trend"`
	StressProxy        *float64           `json:"stress_proxy"`
	RecentLifeEvents7d []string           `json:"recent_life_events_7d"`
	TimeOfDay          *string            `json:"time_of_day"`
	SnapshotDaysAgo    *float64           `json:"snapshot_days_ago"`
}

// corpusEvent is the subset of the bench corpus we need to populate the store:
// the per-event fields the v3 boosts read (anchor flag, sentiment label, the
// emotion vector, biometric snapshot, days_ago, predecessor chain edges).
type corpusEvent struct {
	ID             int64              `json:"id"`
	Text           string             `json:"text"`
	SentimentLabel string             `json:"sentiment_label"`
	UserFlag       bool               `json:"user_flag"`
	DaysAgo        float64            `json:"days_ago"`
	EmotionTags    map[string]float64 `json:"emotion_tags"`
	PredecessorIDs []int64            `json:"predecessor_ids"`
	// biometric_snapshot lives on corpus events 51-60 (event-level, distinct from
	// the query-side UserState biometric). It feeds _event_is_depletion /
	// _event_is_restoration via compute_state_fit and so must be stored per-event.
	BiometricSnapshot map[string]any `json:"biometric_snapshot"`
}

type corpusFile struct {
	Events []corpusEvent `json:"events"`
}

const parityEmbedModel = "parity-golden"

// referenceTime is a fixed clock so computeDaysAgo reproduces each integer
// days_ago EXACTLY (the ≤1 / ≤3 / ≤7 date-proximity cliffs are discontinuous,
// so any fractional drift would change the date boost). We set each event's ts
// to referenceTime - days_ago*24h, which round-trips through computeDaysAgo
// (= ref.Sub(ts).Hours()/24) to the exact integer days_ago.
var parityReferenceTime = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// No tests are excused from strict score+top5 parity: T26/T33 were fixed by
// porting the keyword-emotion branch. T28's chain-ordering tie is handled
// separately via parityChainTieTests (a real tie, not a score divergence).
var parityKnownDivergent = map[string]bool{}

// parityChainTieTests carries chain tests whose Go top5 can differ from the
// golden top5 ONLY by swapping events that are fully tied on the chain-ordering
// sort key. expand_chain_from_seeds (retrieval_v3.py:266) orders the visited set
// by (ancestor_depth, days_ago * -1); when two events tie on BOTH keys their
// relative order falls to CPython's set-iteration order — an interpreter
// artifact, not part of the retrieval algorithm. Go's stable sort breaks the tie
// by event id instead. We accept these IFF the top5 is set-identical and every
// positional difference is between fully-tied events (verified at runtime against
// the corpus); a real reordering bug would still fail. T28 swaps events 38 and
// 52 (both days_ago=7, both predecessor_ids=[37] → ancestor_depth=1).
var parityChainTie = map[string]bool{"T28": true}

// parityScoreTol is the per-event score tolerance. float32 dot products + float64
// boost combine vs Python's float32 numpy + float64 boosts agree well within this.
const parityScoreTol = 1e-5

func loadGolden(t *testing.T) goldenFile {
	t.Helper()
	path := filepath.Join("testdata", "parity_golden.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// The golden is intentionally NOT committed: it is generated from a private
		// evaluation corpus. Regenerate it locally with the reference dump script
		// to run this gate.
		t.Skip("parity golden absent (private, not committed) — regenerate locally to run this gate")
	}
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(g.EventEmbeddings) == 0 || len(g.Tests) == 0 {
		t.Fatalf("golden looks empty: %d embeddings, %d tests",
			len(g.EventEmbeddings), len(g.Tests))
	}
	return g
}

func loadCorpus(t *testing.T) map[int64]corpusEvent {
	t.Helper()
	// Resolve the corpus path from the golden _meta (single source of truth).
	g := loadGolden(t)
	corpusPath, _ := g.Meta["source_corpus"].(string)
	if corpusPath == "" {
		t.Fatal("golden _meta.source_corpus missing")
	}
	raw, err := os.ReadFile(corpusPath)
	if os.IsNotExist(err) {
		t.Skipf("corpus absent (private, not committed): %s — regenerate to run parity", corpusPath)
	}
	if err != nil {
		t.Fatalf("read corpus %s: %v", corpusPath, err)
	}
	var c corpusFile
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	out := make(map[int64]corpusEvent, len(c.Events))
	for _, e := range c.Events {
		out[e.ID] = e
	}
	return out
}

// seedParityStore builds an in-memory store with all migrations and inserts every
// event with its FROZEN golden embedding, emotions, chain edges, and v3 state
// fields. ts is derived from days_ago so computeDaysAgo round-trips exactly.
func seedParityStore(t *testing.T, g goldenFile, corpus map[int64]corpusEvent) *store.Store {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "parity.db")
	s, err := store.Open(tmp)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	db := s.DB()

	for idStr, vec := range g.EventEmbeddings {
		var id int64
		if _, err := scanInt(idStr, &id); err != nil {
			t.Fatalf("event id %q: %v", idStr, err)
		}
		ev, ok := corpus[id]
		if !ok {
			t.Fatalf("event %d in golden but not corpus", id)
		}

		// ts = ref - days_ago*24h → computeDaysAgo(ts, ref) == days_ago exactly.
		ts := parityReferenceTime.Add(-time.Duration(ev.DaysAgo * 24 * float64(time.Hour)))
		// title carries the full corpus text so eventTexts (= lower(title+" "+desc))
		// contains the corpus text — matching Python's event["text"].lower() used
		// by the depletion/restoration heuristics. description stays empty.
		userFlag := 0
		if ev.UserFlag {
			userFlag = 1
		}
		var bioJSON any
		if len(ev.BiometricSnapshot) > 0 {
			b, _ := json.Marshal(ev.BiometricSnapshot)
			bioJSON = string(b)
		}
		if _, err := db.Exec(
			`INSERT INTO events(id, title, description, ts, user_flag, sentiment_label, biometric_json)
			 VALUES (?, ?, '', ?, ?, ?, ?)`,
			id, ev.Text, ts.Format(time.RFC3339Nano), userFlag, ev.SentimentLabel, bioJSON,
		); err != nil {
			t.Fatalf("insert event %d: %v", id, err)
		}

		vecJSON, _ := json.Marshal(vec)
		if _, err := db.Exec(
			`INSERT INTO event_embeddings(event_id, model, dim, vector_json, text_source, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, parityEmbedModel, len(vec), string(vecJSON), ev.Text,
			time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			t.Fatalf("insert embedding %d: %v", id, err)
		}

		// Plutchik-10 emotions in canonical order.
		em := ev.EmotionTags
		if _, err := db.Exec(
			`INSERT INTO event_emotions(event_id, joy, sadness, anger, fear, trust,
			    disgust, anticipation, surprise, shame, guilt, tagger)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, em["joy"], em["sadness"], em["anger"], em["fear"], em["trust"],
			em["disgust"], em["anticipation"], em["surprise"], em["shame"], em["guilt"],
			"parity",
		); err != nil {
			t.Fatalf("insert emotions %d: %v", id, err)
		}
	}

	// Chain edges: predecessor_ids[i] is a PARENT of event i (parent → child).
	// Mirrors build_chain_graph (retrieval_v3.py:217-227): child_to_parents[eid]
	// = predecessor_ids; parent_to_children[p].append(eid).
	for _, ev := range corpus {
		for _, p := range ev.PredecessorIDs {
			if _, err := db.Exec(
				`INSERT OR IGNORE INTO event_chains(parent_id, child_id) VALUES (?, ?)`,
				p, ev.ID,
			); err != nil {
				t.Fatalf("insert chain %d->%d: %v", p, ev.ID, err)
			}
		}
	}
	return s
}

// stubEmbedder returns a fixed frozen query vector regardless of input text.
// Faithful to the dump: the emotion-hint augmentation already changed the text,
// so the engine must use the golden's query_vector, not re-embed.
type stubEmbedder struct{ vec []float32 }

func (s *stubEmbedder) Model() string { return parityEmbedModel }
func (s *stubEmbedder) Embed(_ context.Context, texts []string, _ embed.InputType) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = s.vec
	}
	return out, nil
}

// buildUserState hydrates retrieve.UserState from the golden's merged state.
// nil golden state → nil UserState (the Python `state = ... if (...) else None`
// guard; verified: golden user_state is null exactly when Python passed None).
func buildUserState(gs *goldenUserState) *UserState {
	if gs == nil {
		return nil
	}
	return &UserState{
		MoodVector:         gs.MoodVector,
		SleepQuality:       gs.SleepQuality,
		SleepHours:         gs.SleepHours,
		HRV:                gs.HRV,
		HRTrend:            gs.HRTrend,
		HRVTrend:           gs.HRVTrend,
		StressProxy:        gs.StressProxy,
		RecentLifeEvents7d: gs.RecentLifeEvents7d,
		TimeOfDay:          gs.TimeOfDay,
		SnapshotDaysAgo:    gs.SnapshotDaysAgo,
	}
}

func TestParityGoldenGoEqualsPython(t *testing.T) {
	g := loadGolden(t)
	corpus := loadCorpus(t)
	s := seedParityStore(t, g, corpus)

	ref := parityReferenceTime
	ctx := context.Background()

	type counts struct{ pass, fail, divergent int }
	byType := map[string]*counts{}
	bump := func(tt string) *counts {
		if byType[tt] == nil {
			byType[tt] = &counts{}
		}
		return byType[tt]
	}

	for _, tst := range g.Tests {
		tst := tst
		t.Run(tst.TestID+"_"+tst.TestType, func(t *testing.T) {
			eng := New(Config{
				Store:         s,
				Embedder:      &stubEmbedder{vec: tst.QueryVector},
				ReferenceTime: &ref,
			})
			if err := eng.Init(ctx); err != nil {
				t.Fatalf("engine init: %v", err)
			}
			if got := len(eng.eventIDs); got != len(g.EventEmbeddings) {
				t.Fatalf("loaded %d events, want %d (migration/load mismatch)",
					got, len(g.EventEmbeddings))
			}

			state := buildUserState(tst.UserState)
			qVec := tst.QueryVector

			// Per-event score map (raw v3 formula — return_scores path).
			scores, breakdowns := eng.scoreEventsV3(qVec, tst.Query, state)
			if scores == nil {
				t.Fatalf("scoreEventsV3 returned nil")
			}

			divergent := parityKnownDivergent[tst.TestID]

			// ── Score parity (per event) ──────────────────────────────────────
			maxErr := 0.0
			var worstID int64
			for i, id := range eng.eventIDs {
				py, ok := tst.Scores[itoa(id)]
				if !ok {
					t.Fatalf("event %d missing from golden score map", id)
				}
				d := math.Abs(scores[i] - py)
				if d > maxErr {
					maxErr, worstID = d, id
				}
			}
			if divergent {
				// Embedding non-determinism in the golden: the score map was built
				// from a different query embedding than the dumped query_vector.
				// We require the divergence to stay BOUNDED (regression guard) and
				// log it — but do not require <1e-5.
				const divergentBound = 0.2
				if maxErr > divergentBound {
					t.Fatalf("[%s] documented-divergent test exceeded bound: maxErr=%.6f at event %d (>%.2f) — investigate, this is larger than known embedding noise",
						tst.TestID, maxErr, worstID, divergentBound)
				}
				t.Logf("[%s] KNOWN EMBEDDING NON-DETERMINISM (golden self-inconsistent): score maxErr=%.6f at event %d — golden score map built from a different MLX query embedding than the dumped query_vector (dump_parity_golden.py:156 vs :163). Not a Go port bug.",
					tst.TestID, maxErr, worstID)
			} else {
				if maxErr >= parityScoreTol {
					t.Errorf("[%s] score parity FAILED: maxErr=%.3e at event %d (tol %.0e)",
						tst.TestID, maxErr, worstID, parityScoreTol)
				} else {
					t.Logf("[%s] score parity ok: maxErr=%.3e", tst.TestID, maxErr)
				}
			}

			// ── v2_pure collapse assertion (neutral tests) ────────────────────
			// Core tests have no state, no mood. Every conditional boost must be
			// exactly 1.0 EXCEPT the date boost on the two core queries that carry
			// a temporal keyword (T2 "this week", T4 "recently") — those are a
			// genuine, expected boost, so we assert their date boost is the only
			// non-identity term and emotion/state/anchor stay 1.0.
			if tst.TestType == "core" {
				_, dateActive := resolveDateRef(tst.Query, state)
				for id, bd := range breakdowns {
					if bd.EmotionBoost != nil {
						t.Errorf("[%s] core test event %d has emotion boost %v (want collapse to 1.0)", tst.TestID, id, *bd.EmotionBoost)
					}
					if bd.StateBoost != nil {
						t.Errorf("[%s] core test event %d has state boost %v (want 1.0)", tst.TestID, id, *bd.StateBoost)
					}
					if !dateActive && bd.DateBoost != nil {
						t.Errorf("[%s] core test event %d has date boost %v but no temporal keyword (want 1.0)", tst.TestID, id, *bd.DateBoost)
					}
					// anchor boost CAN fire on core tests (top-N anchors) — that is
					// part of the v3 formula, not a violation. We don't forbid it.
				}
			}

			// ── Top-5 parity ──────────────────────────────────────────────────
			var got []int64
			if tst.ExpandChain {
				got = eng.retrieveChain(qVec, tst.Query, 5, state)
			} else {
				ids, _ := eng.retrieveEmpathicScored(qVec, tst.Query, 5, state)
				got = ids
			}
			top5Match := sliceEq(got, tst.Top5)

			// Documented chain-ordering tie (e.g. T28): accept a non-identical top5
			// IFF it is set-equal to the golden and every positional mismatch is
			// between events that are FULLY TIED on the chain sort key — identical
			// days_ago AND identical predecessor set (→ identical ancestor_depth in
			// any expansion where the parents are visited). That is exactly the case
			// where retrieval_v3.expand_chain_from_seeds leaves the order to CPython
			// set iteration; Go breaks the tie by id. A genuine reordering bug
			// (different set, or a mismatch between non-tied events) still fails.
			if !top5Match && parityChainTie[tst.TestID] {
				if ok, reason := isChainTieEquivalent(got, tst.Top5, corpus); ok {
					t.Logf("[%s] DOCUMENTED CHAIN-ORDERING TIE (not a port bug): go=%v python=%v — differs only by swapping events fully tied on (ancestor_depth, days_ago); Python's order is CPython set-iteration, Go's is id-stable. %s",
						tst.TestID, got, tst.Top5, reason)
					top5Match = true
				} else {
					t.Errorf("[%s] top5 differs and is NOT a pure chain tie: go=%v python=%v (%s)",
						tst.TestID, got, tst.Top5, reason)
				}
			}

			c := bump(tst.TestType)
			if divergent {
				c.divergent++
				if !top5Match {
					t.Logf("[%s] KNOWN top5 divergence (embedding non-determinism): go=%v python=%v — golden top5 was chain-expanded from a different MLX query embedding than the dumped query_vector. Go is self-consistent with the dumped vector; the golden is not. Documented, not fudged.",
						tst.TestID, got, tst.Top5)
				} else {
					t.Logf("[%s] top5 happened to match despite golden self-inconsistency: %v", tst.TestID, got)
				}
				return
			}

			if !top5Match {
				c.fail++
				t.Errorf("[%s] top5 parity FAILED: go=%v python=%v", tst.TestID, got, tst.Top5)
			} else {
				c.pass++
				t.Logf("[%s] top5 parity ok: %v", tst.TestID, got)
			}
		})
	}

	// ── Per-type summary ────────────────────────────────────────────────────
	t.Run("summary", func(t *testing.T) {
		order := []string{"core", "stateful", "multi_signal", "chain"}
		totalPass, totalFail, totalDiv := 0, 0, 0
		for _, tt := range order {
			c := byType[tt]
			if c == nil {
				continue
			}
			totalPass += c.pass
			totalFail += c.fail
			totalDiv += c.divergent
			t.Logf("type=%-13s pass=%d fail=%d documented-divergent=%d", tt, c.pass, c.fail, c.divergent)
		}
		t.Logf("TOTAL: pass=%d fail=%d documented-divergent=%d (of %d tests)",
			totalPass, totalFail, totalDiv, len(g.Tests))
		if totalFail > 0 {
			t.Errorf("parity gate NOT met: %d strict tests failed", totalFail)
		}
	})
}

// ── small helpers (no extra imports) ────────────────────────────────────────

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func scanInt(s string, out *int64) (int, error) {
	var v int64
	neg := false
	i := 0
	if len(s) > 0 && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		i = 1
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, &parseErr{s}
		}
		v = v*10 + int64(s[i]-'0')
	}
	if neg {
		v = -v
	}
	*out = v
	return 1, nil
}

type parseErr struct{ s string }

func (e *parseErr) Error() string { return "not an integer: " + e.s }

// isChainTieEquivalent reports whether two top5 lists are equal up to swapping
// events that are FULLY TIED on the chain-expansion sort key. Requires: same set
// of ids, and every position where they differ holds two events with identical
// days_ago AND identical predecessor_ids set. Same parents + same days_ago ⇒
// identical ancestor_depth in any expansion that visits those parents ⇒ a true
// algorithmic tie that retrieval_v3.expand_chain_from_seeds leaves to CPython set
// iteration order. Anything else is a real divergence.
func isChainTieEquivalent(got, want []int64, corpus map[int64]corpusEvent) (bool, string) {
	if len(got) != len(want) {
		return false, "different lengths"
	}
	gs := map[int64]bool{}
	ws := map[int64]bool{}
	for _, x := range got {
		gs[x] = true
	}
	for _, x := range want {
		ws[x] = true
	}
	if len(gs) != len(ws) {
		return false, "different id sets"
	}
	for x := range gs {
		if !ws[x] {
			return false, "different id sets"
		}
	}
	for i := range got {
		if got[i] == want[i] {
			continue
		}
		a, oka := corpus[got[i]]
		b, okb := corpus[want[i]]
		if !oka || !okb {
			return false, "unknown event in top5"
		}
		if a.DaysAgo != b.DaysAgo {
			return false, "positional mismatch between events with DIFFERENT days_ago — real reorder"
		}
		if !int64SetEq(a.PredecessorIDs, b.PredecessorIDs) {
			return false, "positional mismatch between events with DIFFERENT predecessors — real reorder"
		}
	}
	return true, "all positional swaps are between fully-tied events (same days_ago, same predecessors)"
}

func int64SetEq(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[int64]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func sliceEq(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
