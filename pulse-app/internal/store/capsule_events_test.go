package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Fixture texts are neutral/synthetic (generic release advice) — no personal
// data may ever enter this public repo's tests.

func openCapsuleEventsStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "capsule-events.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func adviceCapsule(items ...MemoryCapsuleItem) MemoryCapsule {
	return MemoryCapsule{
		Schema: "pulse.memory_capsule.v1",
		Source: CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         "2026-07-01T09:00:00Z",
		},
		Items: items,
	}
}

func normalAdviceItem(summary string, tags ...string) MemoryCapsuleItem {
	return MemoryCapsuleItem{
		Kind:            "decision",
		RedactedSummary: summary,
		Confidence:      0.9,
		EvidenceHint:    "current_turn",
		PrivacyTier:     "normal",
		Retention:       "project",
		Tags:            tags,
	}
}

func capsuleEventID(t *testing.T, s *Store, capsuleID string) sql.NullInt64 {
	t.Helper()
	var eventID sql.NullInt64
	if err := s.DB().QueryRow(
		`SELECT event_id FROM memory_capsules WHERE id=?`, capsuleID).Scan(&eventID); err != nil {
		t.Fatalf("select event_id: %v", err)
	}
	return eventID
}

// countEvents counts all event rows — in these tests the only events are
// capsule projections, so this doubles as the projected-event count.
func countEvents(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

func TestRememberCapsuleProjectsNormalTierToLinkedTaggedEvent(t *testing.T) {
	s := openCapsuleEventsStore(t)

	sensitive := normalAdviceItem("Track the follow-up owner for the release retro.")
	sensitive.PrivacyTier = "sensitive"
	private := normalAdviceItem("Review the vendor terms before renewal.")
	private.PrivacyTier = "private"

	ids, err := s.RememberCapsule(adviceCapsule(
		normalAdviceItem(
			"Cut scope to the committed checklist and ship the smallest safe release.",
			"advice", "state:deadline_pressure"),
		sensitive,
		private,
	))
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 capsule ids, got %d", len(ids))
	}

	// Normal tier → linked event with title=kind, description=summary,
	// ts=source timestamp, provenance/domain markers and tags copied over.
	link := capsuleEventID(t, s, ids[0])
	if !link.Valid {
		t.Fatalf("normal-tier capsule has no event_id link")
	}
	var title, description, ts, provenance, domain, tags, scorer string
	if err := s.DB().QueryRow(`
		SELECT title, description, ts, COALESCE(provenance,''), COALESCE(domain,''),
		       COALESCE(tags,''), COALESCE(scorer_version,'')
		  FROM events WHERE id=?`, link.Int64).Scan(
		&title, &description, &ts, &provenance, &domain, &tags, &scorer); err != nil {
		t.Fatalf("select projected event: %v", err)
	}
	if title != "decision" {
		t.Fatalf("title = %q, want capsule kind", title)
	}
	if description != "Cut scope to the committed checklist and ship the smallest safe release." {
		t.Fatalf("description = %q", description)
	}
	if ts != "2026-07-01T09:00:00Z" {
		t.Fatalf("ts = %q, want source timestamp", ts)
	}
	// provenance uses the CHECK-allowed interactive_memory value; the
	// authoritative capsule-origin marker is the event_id link asserted above.
	if provenance != "interactive_memory" || domain != "real" || scorer != "host-extracted" {
		t.Fatalf("provenance/domain/scorer = %q/%q/%q", provenance, domain, scorer)
	}
	if tags != `["advice","state:deadline_pressure"]` {
		t.Fatalf("tags = %q", tags)
	}

	// Sensitive/private stay out of the graph.
	for _, id := range ids[1:] {
		if capsuleEventID(t, s, id).Valid {
			t.Fatalf("capsule %s (non-normal tier) must not project an event", id)
		}
	}
	if got := countEvents(t, s); got != 1 {
		t.Fatalf("expected exactly 1 capsule event, got %d", got)
	}

	// CapsuleEventDocs returns only the projected item, kind+summary text.
	docs, err := s.CapsuleEventDocs(ids)
	if err != nil {
		t.Fatalf("capsule event docs: %v", err)
	}
	if len(docs) != 1 || docs[0].EventID != link.Int64 {
		t.Fatalf("docs = %#v", docs)
	}
	if docs[0].Text != "decision\nCut scope to the committed checklist and ship the smallest safe release." {
		t.Fatalf("doc text = %q", docs[0].Text)
	}
}

func TestCapsuleEventProjectionEnvOptOut(t *testing.T) {
	s := openCapsuleEventsStore(t)
	t.Setenv("PULSE_CAPSULE_EVENTS", "off")

	ids, err := s.RememberCapsule(adviceCapsule(
		normalAdviceItem("Plan the triage: sort open items by impact before the release.")))
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if capsuleEventID(t, s, ids[0]).Valid {
		t.Fatalf("PULSE_CAPSULE_EVENTS=off must skip projection")
	}
	if got := countEvents(t, s); got != 0 {
		t.Fatalf("expected 0 capsule events with opt-out, got %d", got)
	}

	// Backfill is a no-op while opted out too.
	docs, err := s.BackfillCapsuleEvents()
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("backfill with opt-out returned %d docs", len(docs))
	}
}

func TestBackfillCapsuleEventsIsIdempotent(t *testing.T) {
	s := openCapsuleEventsStore(t)

	// Write capsules with projection off, simulating a store from before the
	// capsule→event wiring (event_id IS NULL).
	t.Setenv("PULSE_CAPSULE_EVENTS", "off")
	sensitive := normalAdviceItem("Track the follow-up owner for the release retro.")
	sensitive.PrivacyTier = "sensitive"
	ids, err := s.RememberCapsule(adviceCapsule(
		normalAdviceItem(
			"Write decisions down and confirm ownership in writing before the release.",
			"state:job_insecurity"),
		normalAdviceItem("Plan the triage: sort open items by impact before the release."),
		sensitive,
	))
	if err != nil {
		t.Fatalf("remember: %v", err)
	}

	t.Setenv("PULSE_CAPSULE_EVENTS", "") // default = on
	docs, err := s.BackfillCapsuleEvents()
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 backfilled docs (normal tier only), got %d", len(docs))
	}
	for _, id := range ids[:2] {
		if !capsuleEventID(t, s, id).Valid {
			t.Fatalf("capsule %s not linked after backfill", id)
		}
	}
	if capsuleEventID(t, s, ids[2]).Valid {
		t.Fatalf("sensitive capsule must not be backfilled")
	}

	// Second run: nothing left to project.
	again, err := s.BackfillCapsuleEvents()
	if err != nil {
		t.Fatalf("backfill again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("backfill is not idempotent: second run produced %d docs", len(again))
	}
	if got := countEvents(t, s); got != 2 {
		t.Fatalf("expected 2 capsule events after double backfill, got %d", got)
	}
}

func TestDeleteAndWipeRemoveProjectedCapsuleEvents(t *testing.T) {
	s := openCapsuleEventsStore(t)

	ids, err := s.RememberCapsule(adviceCapsule(
		normalAdviceItem(
			"Cut scope to the committed checklist and ship the smallest safe release.",
			"state:deadline_pressure"),
		normalAdviceItem("Plan the triage: sort open items by impact before the release."),
	))
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if got := countEvents(t, s); got != 2 {
		t.Fatalf("expected 2 projected events, got %d", got)
	}

	// Forgetting one capsule removes its projected event copy.
	if err := s.DeleteMemory(ids[0]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := countEvents(t, s); got != 1 {
		t.Fatalf("expected 1 projected event after delete, got %d", got)
	}

	// Wipe stays clean: capsules AND their projections are gone.
	if err := s.WipeMemory(); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	if got := countEvents(t, s); got != 0 {
		t.Fatalf("expected 0 projected events after wipe, got %d", got)
	}
	status, err := s.MemoryStatus()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.ItemCount != 0 {
		t.Fatalf("expected empty capsule store after wipe, got %d", status.ItemCount)
	}
}
