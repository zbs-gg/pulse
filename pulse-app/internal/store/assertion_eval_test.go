package store

import (
	"testing"
)

// This file is the eval meter for the assertion layer. The layer shipped with
// a real supersession model (SaveAssertion / SupersedeAssertion /
// CurrentAssertions) but no aggregate sensor over a population of superseded
// fact-pairs. These tests seed a deterministic corpus through the real write
// path and compute three metrics:
//
//   - temporal-accuracy: current query returns the current value, not the stale
//     one.
//   - staleness-rate:    a superseded value is returned as current.
//   - contradiction-rate: >1 active, currently-believed, currently-true row for
//     one claim_key+scope.
//
// In-order pairs are HARD-GATED at accuracy==1.0, staleness==0,
// contradiction==0 — invariants the code guarantees today. Out-of-order pairs
// and the no-DB-guard contradiction case are MEASURED AND LOGGED (t.Logf),
// non-gating, so the two known correctness gaps stop being invisible without
// breaking `make verify` on a pre-existing defect. A negative-control case
// proves the contradiction sensor actually fires.

// factPair is one claim-identity-linked superseded fact-pair. olderValue is the
// world-value true at olderFrom; newerValue at newerFrom (newerFrom > olderFrom
// in world time). The value that SHOULD be current is always newerValue.
type factPair struct {
	subject    string
	predicate  string
	scope      Scope
	olderValue string
	newerValue string
	olderFrom  string
	newerFrom  string
}

func (p factPair) key() string { return MakeClaimKey(p.subject, p.predicate) }

// inOrderCorpus is 12 superseded fact-pairs across a mix of scopes, with fixed
// RFC3339 world-times so the run is fully deterministic (no time.Now in the
// gated path).
func inOrderCorpus() []factPair {
	personal := Scope{Type: "personal"}
	project := Scope{Type: "project", ID: "garden"}
	repo := Scope{Type: "repo", ID: "pulse"}
	agent := Scope{Type: "agent", ID: "elle"}
	session := Scope{Type: "session", ID: "s-2026-07-04"}
	return []factPair{
		{"employer", "is", personal, "Acme", "Globex", "2025-01-01T00:00:00Z", "2026-06-01T00:00:00Z"},
		{"effort level", "is", project, "medium", "xhigh", "2026-06-23T00:00:00Z", "2026-06-24T00:00:00Z"},
		{"home city", "is", personal, "Moscow", "Bali", "2024-05-01T00:00:00Z", "2026-03-01T00:00:00Z"},
		{"role", "is", personal, "engineer", "founder", "2023-01-01T00:00:00Z", "2025-09-01T00:00:00Z"},
		{"default model", "is", repo, "v2_pure", "v3", "2026-01-01T00:00:00Z", "2026-06-22T00:00:00Z"},
		{"preview tag", "is", repo, "0.6.4", "0.6.5", "2026-06-29T00:00:00Z", "2026-06-30T00:00:00Z"},
		{"reasoning effort", "is", agent, "high", "xhigh", "2026-05-01T00:00:00Z", "2026-07-01T00:00:00Z"},
		{"active branch", "is", session, "main", "feat/assertion-eval-gate", "2026-07-04T00:00:00Z", "2026-07-04T01:00:00Z"},
		{"favourite tea", "is", personal, "green", "puerh", "2022-01-01T00:00:00Z", "2026-02-01T00:00:00Z"},
		{"sprint format", "is", project, "1-day", "2-day", "2026-04-29T00:00:00Z", "2026-05-01T00:00:00Z"},
		{"embedder", "is", repo, "cohere", "bge-m3", "2026-03-01T00:00:00Z", "2026-06-22T00:00:00Z"},
		{"mood", "is", personal, "drained", "restored", "2026-07-03T00:00:00Z", "2026-07-04T00:00:00Z"},
	}
}

// seedInOrder writes each pair through the real write path: SaveAssertion(older)
// then SupersedeAssertion(newer). World time is monotonic (newerFrom later), so
// the code's id-DESC ordering agrees with world-time ordering.
func seedInOrder(t *testing.T, s *Store, pairs []factPair) {
	t.Helper()
	for _, p := range pairs {
		older := Assertion{
			Subject:    p.subject,
			Predicate:  p.predicate,
			ObjectText: p.olderValue,
			ValidFrom:  p.olderFrom,
			SystemFrom: p.olderFrom,
			Scope:      p.scope,
		}
		if _, err := s.SaveAssertion(older); err != nil {
			t.Fatalf("SaveAssertion(%s=%s): %v", p.subject, p.olderValue, err)
		}
		newer := Assertion{
			Subject:    p.subject,
			Predicate:  p.predicate,
			ObjectText: p.newerValue,
			ValidFrom:  p.newerFrom,
			SystemFrom: p.newerFrom,
			Scope:      p.scope,
		}
		if _, err := s.SupersedeAssertion(newer); err != nil {
			t.Fatalf("SupersedeAssertion(%s=%s): %v", p.subject, p.newerValue, err)
		}
	}
}

// seedOutOfOrder writes the newer world-value FIRST, then supersedes it with the
// OLDER world-value arriving later. This exercises the known ordering gap:
// SupersedeAssertion / upsertAssertionTx pick the current row by id (last
// inserted), not by valid_from, so the older value wrongly becomes current.
func seedOutOfOrder(t *testing.T, s *Store, pairs []factPair) {
	t.Helper()
	for _, p := range pairs {
		newer := Assertion{
			Subject:    p.subject,
			Predicate:  p.predicate,
			ObjectText: p.newerValue,
			ValidFrom:  p.newerFrom,
			SystemFrom: p.newerFrom, // recorded first...
			Scope:      p.scope,
		}
		if _, err := s.SaveAssertion(newer); err != nil {
			t.Fatalf("SaveAssertion(%s=%s): %v", p.subject, p.newerValue, err)
		}
		older := Assertion{
			Subject:    p.subject,
			Predicate:  p.predicate,
			ObjectText: p.olderValue,
			ValidFrom:  p.olderFrom, // ...but is true EARLIER in the world
			SystemFrom: p.newerFrom, // and arrives LATER in belief-time
			Scope:      p.scope,
		}
		if _, err := s.SupersedeAssertion(older); err != nil {
			t.Fatalf("SupersedeAssertion(%s=%s): %v", p.subject, p.olderValue, err)
		}
	}
}

type evalMetrics struct {
	n            int
	accurate     int // current query returned exactly the expected-current value
	stale        int // current query returned a superseded (older) value
	contradicted int // >1 active currently-believed row for the claim_key+scope
}

func (m evalMetrics) accuracy() float64 {
	if m.n == 0 {
		return 0
	}
	return float64(m.accurate) / float64(m.n)
}

func (m evalMetrics) stalenessRate() float64 {
	if m.n == 0 {
		return 0
	}
	return float64(m.stale) / float64(m.n)
}

func (m evalMetrics) contradictionRate() float64 {
	if m.n == 0 {
		return 0
	}
	return float64(m.contradicted) / float64(m.n)
}

// evaluate queries CurrentAssertions for each pair and classifies the result.
// The value that SHOULD be current is always p.newerValue.
func evaluate(t *testing.T, s *Store, pairs []factPair) evalMetrics {
	t.Helper()
	m := evalMetrics{n: len(pairs)}
	for _, p := range pairs {
		cur, err := s.CurrentAssertions(p.key(), p.scope)
		if err != nil {
			t.Fatalf("CurrentAssertions(%s): %v", p.subject, err)
		}
		if len(cur) > 1 {
			m.contradicted++
		}
		accurate := len(cur) == 1 && cur[0].ObjectText == p.newerValue
		if accurate {
			m.accurate++
		}
		for _, a := range cur {
			if a.ObjectText == p.olderValue {
				m.stale++
				break
			}
		}
	}
	return m
}

// TestAssertionEvalInOrderInvariants is the hard gate. In-order supersession is
// something the code guarantees today, so these thresholds must hold or
// `make verify` fails.
func TestAssertionEvalInOrderInvariants(t *testing.T) {
	s := openTestStore(t)
	pairs := inOrderCorpus()
	seedInOrder(t, s, pairs)
	m := evaluate(t, s, pairs)

	t.Logf("in-order (n=%d): temporal-accuracy=%.3f staleness-rate=%.3f contradiction-rate=%.3f",
		m.n, m.accuracy(), m.stalenessRate(), m.contradictionRate())

	if m.accuracy() != 1.0 {
		t.Errorf("in-order temporal-accuracy = %.3f, want 1.0 (%d/%d pairs accurate)",
			m.accuracy(), m.accurate, m.n)
	}
	if m.stalenessRate() != 0.0 {
		t.Errorf("in-order staleness-rate = %.3f, want 0.0 (%d/%d pairs returned a stale value)",
			m.stalenessRate(), m.stale, m.n)
	}
	if m.contradictionRate() != 0.0 {
		t.Errorf("in-order contradiction-rate = %.3f, want 0.0 (%d/%d groups had >1 active row)",
			m.contradictionRate(), m.contradicted, m.n)
	}
}

// TestAssertionEvalOutOfOrderKnownGap MEASURES the out-of-order world-time gap
// (older value arriving later becomes current). It is intentionally NON-GATING:
// the current code picks the active row by id, not valid_from, so it fails this
// case today. Logging the real numbers turns an invisible bug into a tracked
// one; the fix PR (order by valid_from) flips these logs to a gate.
func TestAssertionEvalOutOfOrderKnownGap(t *testing.T) {
	s := openTestStore(t)
	pairs := inOrderCorpus()
	seedOutOfOrder(t, s, pairs)
	m := evaluate(t, s, pairs)

	t.Logf("out-of-order KNOWN GAP (n=%d): temporal-accuracy=%.3f staleness-rate=%.3f contradiction-rate=%.3f",
		m.n, m.accuracy(), m.stalenessRate(), m.contradictionRate())
	t.Logf("out-of-order is measured, NOT gated: SupersedeAssertion picks the current row by id, not valid_from (assertions.go), so a late-arriving older value wrongly becomes current. Fix is a separate behavior-changing PR.")
}

// TestAssertionEvalContradictionSensorFires is the negative control. It proves
// the contradiction metric actually detects a contradiction rather than always
// reporting 0. SaveAssertion does not supersede and there is no DB unique index,
// so two SaveAssertion calls for the same claim_key+scope leave two active rows.
// The sensor MUST see contradiction-rate rise above 0. This gap (no DB-level
// single-active guard) is real; the guard is a separate behavior-changing PR.
func TestAssertionEvalContradictionSensorFires(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "personal"}
	pair := factPair{
		subject:    "employer",
		predicate:  "is",
		scope:      scope,
		olderValue: "Acme",
		newerValue: "Globex",
		olderFrom:  "2025-01-01T00:00:00Z",
		newerFrom:  "2026-06-01T00:00:00Z",
	}

	// Two plain SaveAssertion writes — neither supersedes the other.
	for _, v := range []struct{ obj, from string }{
		{pair.olderValue, pair.olderFrom},
		{pair.newerValue, pair.newerFrom},
	} {
		if _, err := s.SaveAssertion(Assertion{
			Subject:    pair.subject,
			Predicate:  pair.predicate,
			ObjectText: v.obj,
			ValidFrom:  v.from,
			SystemFrom: v.from,
			Scope:      scope,
		}); err != nil {
			t.Fatalf("SaveAssertion(%s): %v", v.obj, err)
		}
	}

	m := evaluate(t, s, []factPair{pair})
	t.Logf("negative control (no supersede): contradiction-rate=%.3f (want > 0)", m.contradictionRate())
	if m.contradictionRate() == 0.0 {
		t.Fatalf("contradiction sensor did not fire: two active rows for one claim_key+scope but contradiction-rate=0.0 — the meter is broken")
	}
}
