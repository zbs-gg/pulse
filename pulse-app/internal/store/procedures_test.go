package store

import (
	"strings"
	"testing"
)

func TestProcedureRoundTrip(t *testing.T) {
	s := openTestStore(t)
	p := Procedure{
		Name:        "Prep Call",
		Description: "Prepare a briefing before a call.",
		Params: map[string]any{
			"contact": "string",
			"minutes": float64(15),
		},
		Steps: []any{
			map[string]any{"do": "research contact"},
			map[string]any{"do": "draft agenda"},
		},
		Scope: Scope{Type: "project", ID: "garden"},
	}
	id, err := s.UpsertProcedure(p)
	if err != nil {
		t.Fatalf("UpsertProcedure: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	got, err := s.GetProcedure("Prep Call", Scope{Type: "project", ID: "garden"})
	if err != nil {
		t.Fatalf("GetProcedure: %v", err)
	}
	if got == nil {
		t.Fatal("expected a procedure, got nil")
	}
	if got.Name != "Prep Call" {
		t.Fatalf("name mismatch: %q", got.Name)
	}
	if got.Description != p.Description {
		t.Fatalf("description mismatch: %q", got.Description)
	}
	if got.Params["contact"] != "string" {
		t.Fatalf("params did not survive: %#v", got.Params)
	}
	if got.Params["minutes"] != float64(15) {
		t.Fatalf("params numeric did not survive: %#v", got.Params)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d (%#v)", len(got.Steps), got.Steps)
	}
	step0, ok := got.Steps[0].(map[string]any)
	if !ok || step0["do"] != "research contact" {
		t.Fatalf("step[0] did not survive: %#v", got.Steps[0])
	}
	if got.Scope.Type != "project" || got.Scope.ID != "garden" {
		t.Fatalf("scope mismatch: %+v", got.Scope)
	}
}

func TestProcedureNameKeyStability(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "personal"}

	id1, err := s.UpsertProcedure(Procedure{Name: "Prep Call", Scope: scope})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// A different surface form of the same name must resolve to the same row.
	id2, err := s.UpsertProcedure(Procedure{Name: "  prep-call ", Description: "v2", Scope: scope})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected same row on re-learn, got %d then %d", id1, id2)
	}

	list, err := s.ListProcedures(scope)
	if err != nil {
		t.Fatalf("ListProcedures: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 row after upsert-supersede, got %d", len(list))
	}
	if list[0].Description != "v2" {
		t.Fatalf("expected replaced definition, got %q", list[0].Description)
	}
}

func TestProcedureUpsertPreservesSuccessCount(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "personal"}

	id, err := s.UpsertProcedure(Procedure{Name: "Deploy", Scope: scope})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.IncrementProcedureSuccess(id, ""); err != nil {
		t.Fatalf("increment: %v", err)
	}
	if err := s.IncrementProcedureSuccess(id, ""); err != nil {
		t.Fatalf("increment: %v", err)
	}

	// Re-learn the procedure; success_count must not be reset.
	if _, err := s.UpsertProcedure(Procedure{Name: "Deploy", Description: "updated", Scope: scope}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, err := s.GetProcedure("Deploy", scope)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SuccessCount != 2 {
		t.Fatalf("expected success_count preserved at 2, got %d", got.SuccessCount)
	}

	// A caller supplying a higher value raises the floor.
	if _, err := s.UpsertProcedure(Procedure{Name: "Deploy", SuccessCount: 5, Scope: scope}); err != nil {
		t.Fatalf("re-upsert with higher count: %v", err)
	}
	got, err = s.GetProcedure("Deploy", scope)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SuccessCount != 5 {
		t.Fatalf("expected success_count raised to 5, got %d", got.SuccessCount)
	}

	// A caller supplying a lower value must not lower the stored count.
	if _, err := s.UpsertProcedure(Procedure{Name: "Deploy", SuccessCount: 1, Scope: scope}); err != nil {
		t.Fatalf("re-upsert with lower count: %v", err)
	}
	got, err = s.GetProcedure("Deploy", scope)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SuccessCount != 5 {
		t.Fatalf("expected success_count kept at 5, got %d", got.SuccessCount)
	}
}

func TestIncrementProcedureSuccessAdvancesUpdatedAt(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "personal"}

	if _, err := s.UpsertProcedure(Procedure{
		Name:      "Backup",
		Scope:     scope,
		UpdatedAt: "2026-07-04T00:00:00Z",
		CreatedAt: "2026-07-04T00:00:00Z",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	before, err := s.GetProcedure("Backup", scope)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if before.SuccessCount != 0 {
		t.Fatalf("expected fresh count 0, got %d", before.SuccessCount)
	}

	if err := s.IncrementProcedureSuccess(before.ID, "2026-07-04T01:00:00Z"); err != nil {
		t.Fatalf("increment: %v", err)
	}
	if err := s.IncrementProcedureSuccess(before.ID, "2026-07-04T02:00:00Z"); err != nil {
		t.Fatalf("increment: %v", err)
	}

	after, err := s.GetProcedure("Backup", scope)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.SuccessCount != 2 {
		t.Fatalf("expected count 2, got %d", after.SuccessCount)
	}
	if after.UpdatedAt <= before.UpdatedAt {
		t.Fatalf("expected updated_at to advance, before=%q after=%q", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestProcedureScopeIsolation(t *testing.T) {
	s := openTestStore(t)
	garden := Scope{Type: "project", ID: "garden"}
	atlas := Scope{Type: "project", ID: "atlas"}

	if _, err := s.UpsertProcedure(Procedure{Name: "Release", Description: "garden release", Scope: garden}); err != nil {
		t.Fatalf("upsert garden: %v", err)
	}
	if _, err := s.UpsertProcedure(Procedure{Name: "Release", Description: "atlas release", Scope: atlas}); err != nil {
		t.Fatalf("upsert atlas: %v", err)
	}

	g, err := s.GetProcedure("Release", garden)
	if err != nil || g == nil {
		t.Fatalf("get garden: %v (nil=%v)", err, g == nil)
	}
	if g.Description != "garden release" {
		t.Fatalf("scope bleed: garden got %q", g.Description)
	}
	a, err := s.GetProcedure("Release", atlas)
	if err != nil || a == nil {
		t.Fatalf("get atlas: %v (nil=%v)", err, a == nil)
	}
	if a.Description != "atlas release" {
		t.Fatalf("scope bleed: atlas got %q", a.Description)
	}
	if g.ID == a.ID {
		t.Fatal("expected two distinct rows across scopes")
	}

	list, err := s.ListProcedures(garden)
	if err != nil {
		t.Fatalf("list garden: %v", err)
	}
	if len(list) != 1 || list[0].Scope.ID != "garden" {
		t.Fatalf("ListProcedures leaked out-of-scope rows: %+v", list)
	}

	// Absent procedure returns (nil, nil), not an error.
	missing, err := s.GetProcedure("Nonexistent", garden)
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for missing procedure, got %+v", missing)
	}
}

func TestProcedureListMostRecentFirst(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "personal"}

	if _, err := s.UpsertProcedure(Procedure{Name: "First", Scope: scope, UpdatedAt: "2026-07-04T00:00:00Z"}); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	if _, err := s.UpsertProcedure(Procedure{Name: "Second", Scope: scope, UpdatedAt: "2026-07-04T01:00:00Z"}); err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	if _, err := s.UpsertProcedure(Procedure{Name: "Third", Scope: scope, UpdatedAt: "2026-07-04T02:00:00Z"}); err != nil {
		t.Fatalf("upsert third: %v", err)
	}

	list, err := s.ListProcedures(scope)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(list))
	}
	if list[0].Name != "Third" || list[2].Name != "First" {
		t.Fatalf("expected most-recent-first ordering, got %q..%q", list[0].Name, list[2].Name)
	}
}

// TestUpsertProcedureRejectsUnsafeContent mirrors the negative-smoke style:
// one dangerous payload per rejected class, each of which must error and
// leave nothing behind in the store. Same guard classes as memory capsules
// (validateMemoryCapsule): secrets, absolute paths, transcript-like content.
func TestUpsertProcedureRejectsUnsafeContent(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "personal"}

	longHex := strings.Repeat("a1b2c3d4", 8) // 64 hex chars — IPC-secret shape
	transcript := "user: hi\nassistant: hello\nuser: ok\nassistant: sure\nuser: bye\nassistant: later"

	cases := []struct {
		label string
		p     Procedure
	}{
		{"sk- secret in steps_json", Procedure{Name: "Leaky", Scope: scope,
			Steps: []any{map[string]any{"do": "export the key sk-abc123def456ghi789"}}}},
		{"api_key in params_json", Procedure{Name: "Keyed", Scope: scope,
			Params: map[string]any{"api_key": "supplied-at-runtime"}}},
		{"X-Pulse-Key header in steps_json", Procedure{Name: "Header", Scope: scope,
			Steps: []any{map[string]any{"do": "curl -H 'X-Pulse-Key: <value>' localhost"}}}},
		{"long hex secret in params_json", Procedure{Name: "Hex", Scope: scope,
			Params: map[string]any{"token": longHex}}},
		{"absolute /Users path in steps_json", Procedure{Name: "MacPath", Scope: scope,
			Steps: []any{map[string]any{"do": "read /Users/someone/notes.txt"}}}},
		{"absolute /home path in params_json", Procedure{Name: "LinuxPath", Scope: scope,
			Params: map[string]any{"dir": "/home/someone/project"}}},
		{"transcript-like steps_json", Procedure{Name: "Chat", Scope: scope,
			Steps: []any{transcript}}},
		{"secret marker in name", Procedure{Name: "store sk-key somewhere", Scope: scope}},
		{"path-like description", Procedure{Name: "Described", Scope: scope,
			Description: "See /Users/someone/plan.md for details"}},
	}
	for _, tc := range cases {
		if _, err := s.UpsertProcedure(tc.p); err == nil {
			t.Errorf("%s: expected rejection, got nil error", tc.label)
		}
	}

	list, err := s.ListProcedures(scope)
	if err != nil {
		t.Fatalf("list after rejections: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty store after rejections, got %d rows: %+v", len(list), list)
	}

	// Positive control: a clean procedure carrying a 40-char git commit SHA
	// must still be accepted — the long-hex guard targets 32-byte secrets,
	// not commit references.
	sha := strings.Repeat("ab12", 10) // 40 hex chars
	if _, err := s.UpsertProcedure(Procedure{
		Name:  "Pin Release",
		Scope: scope,
		Steps: []any{map[string]any{"do": "check out commit " + sha}},
	}); err != nil {
		t.Fatalf("clean procedure with commit SHA rejected: %v", err)
	}
}

// TestScanProcedureSurfacesCorruptJSON seeds corrupt rows directly (bypassing
// UpsertProcedure) and expects reads to return an error instead of silently
// dropping the payload.
func TestScanProcedureSurfacesCorruptJSON(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.db.Exec(`
		INSERT INTO procedures
		  (name, name_key, description, params_json, steps_json,
		   scope_type, scope_id, created_at, updated_at)
		VALUES ('Broken Params', 'brokenparams', '', '{not json', '[]',
		        'personal', '', '2026-07-04T00:00:00Z', '2026-07-04T00:00:00Z')`); err != nil {
		t.Fatalf("seed corrupt params row: %v", err)
	}
	if _, err := s.GetProcedure("Broken Params", Scope{Type: "personal"}); err == nil {
		t.Fatal("expected corrupt params_json to surface an error from GetProcedure")
	}

	if _, err := s.db.Exec(`
		INSERT INTO procedures
		  (name, name_key, description, params_json, steps_json,
		   scope_type, scope_id, created_at, updated_at)
		VALUES ('Broken Steps', 'brokensteps', '', '{}', '[broken',
		        'personal', '', '2026-07-04T00:00:00Z', '2026-07-04T00:00:00Z')`); err != nil {
		t.Fatalf("seed corrupt steps row: %v", err)
	}
	if _, err := s.GetProcedure("Broken Steps", Scope{Type: "personal"}); err == nil {
		t.Fatal("expected corrupt steps_json to surface an error from GetProcedure")
	}

	if _, err := s.ListProcedures(Scope{Type: "personal"}); err == nil {
		t.Fatal("expected corrupt rows to surface an error from ListProcedures")
	}
}

// TestUpsertProcedureConflictKeepsCreatedAt exercises the atomic
// INSERT ... ON CONFLICT path: a re-learn must land on the same row, keep the
// original created_at, take the new definition/updated_at, and never leave a
// duplicate behind.
func TestUpsertProcedureConflictKeepsCreatedAt(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "project", ID: "garden"}

	id1, err := s.UpsertProcedure(Procedure{
		Name:      "Ship",
		Scope:     scope,
		CreatedAt: "2026-07-01T00:00:00Z",
		UpdatedAt: "2026-07-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	id2, err := s.UpsertProcedure(Procedure{
		Name:        "Ship",
		Description: "v2",
		Scope:       scope,
		CreatedAt:   "2026-07-04T09:00:00Z",
		UpdatedAt:   "2026-07-04T09:00:00Z",
	})
	if err != nil {
		t.Fatalf("conflicting upsert: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected conflict to land on the same row, got %d then %d", id1, id2)
	}

	got, err := s.GetProcedure("Ship", scope)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CreatedAt != "2026-07-01T00:00:00Z" {
		t.Fatalf("expected created_at preserved across upsert, got %q", got.CreatedAt)
	}
	if got.UpdatedAt != "2026-07-04T09:00:00Z" {
		t.Fatalf("expected updated_at replaced, got %q", got.UpdatedAt)
	}
	if got.Description != "v2" {
		t.Fatalf("expected replaced definition, got %q", got.Description)
	}

	list, err := s.ListProcedures(scope)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 row after conflict upsert, got %d", len(list))
	}
}

func TestProcedureEmptyJSONDefaults(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "personal"}

	// nil Params/Steps must store as {}/[] and read back as empty, not null.
	if _, err := s.UpsertProcedure(Procedure{Name: "Bare", Scope: scope}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.GetProcedure("Bare", scope)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Params == nil {
		t.Fatal("expected non-nil empty params map, got nil")
	}
	if len(got.Params) != 0 {
		t.Fatalf("expected empty params, got %#v", got.Params)
	}
	if got.Steps == nil {
		t.Fatal("expected non-nil empty steps slice, got nil")
	}
	if len(got.Steps) != 0 {
		t.Fatalf("expected empty steps, got %#v", got.Steps)
	}
}
