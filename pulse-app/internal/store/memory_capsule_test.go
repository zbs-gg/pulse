package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

func TestRememberCapsuleStoresStrictItemsAndRecallFindsThem(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	capsule := MemoryCapsule{
		Schema: "pulse.memory_capsule.v1",
		Source: CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         "2026-06-02T09:00:00Z",
		},
		Items: []MemoryCapsuleItem{{
			Kind:            "decision",
			RedactedSummary: "We chose Pulse MCP distribution with Claude Code as the first install target.",
			Confidence:      0.92,
			EvidenceHint:    "current_turn",
			PrivacyTier:     "normal",
			Retention:       "project",
			Tags:            []string{"distribution", "claude-code"},
		}},
		RawInputIncluded: false,
	}

	ids, err := s.RememberCapsule(capsule)
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("ids: %#v", ids)
	}

	items, err := s.RecallMemory(RecallMemoryQuery{
		Query:          "what did we choose for distribution",
		Scope:          "project",
		Limit:          5,
		PrivacyCeiling: "normal",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 recall item, got %d", len(items))
	}
	if items[0].ID != ids[0] {
		t.Fatalf("expected id %q, got %q", ids[0], items[0].ID)
	}
	if !strings.Contains(items[0].Summary, "Claude Code") {
		t.Fatalf("summary was not returned: %#v", items[0])
	}
	if items[0].Source != "pulse" {
		t.Fatalf("expected source pulse, got %q", items[0].Source)
	}
}

// A fresh capsule matching a single weak term must not outrank an older
// capsule matching every query term. Recall ranks by term coverage, with
// recency only as a tiebreak — not the other way around.
func TestRecallRanksByTermCoverageNotRecency(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	insert := func(id, summary, createdAt string) {
		t.Helper()
		if _, err := s.db.Exec(`
			INSERT INTO memory_capsules
			  (id, schema_version, source_host, conversation_scope, source_timestamp,
			   kind, redacted_summary, confidence, evidence_hint, privacy_tier,
			   retention, tags, created_at)
			VALUES (?, 'pulse.memory_capsule.v1', 'claude-code', 'current_turn', ?,
			        'note', ?, 0.9, 'current_turn', 'normal', 'project', '[]', ?)`,
			id, createdAt, summary, createdAt); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	// Older, matches all three query terms.
	insert("cap-relevant", "The kubernetes migration rollback plan was approved by the team.", "2026-06-01T09:00:00Z")
	// Newer, matches only "kubernetes" — old recency-only ranking floated this to the top.
	insert("cap-noise", "Weekly kubernetes cluster cost review for the finance team.", "2026-06-30T09:00:00Z")

	// Terms are scattered so the exact-phrase primary path misses and the
	// term-coverage fallback path runs.
	items, err := s.RecallMemory(RecallMemoryQuery{
		Query:          "rollback migration kubernetes",
		Scope:          "project",
		Limit:          5,
		PrivacyCeiling: "normal",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("expected both capsules recalled, got %d: %#v", len(items), items)
	}
	if items[0].ID != "cap-relevant" {
		t.Fatalf("term-coverage ranking broken: expected cap-relevant first, got %q (recency drowned relevance)", items[0].ID)
	}
}

func TestRememberCapsuleRejectsRawOrTranscriptLikePayloads(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	valid := MemoryCapsule{
		Schema: "pulse.memory_capsule.v1",
		Source: CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         time.Now().UTC().Format(time.RFC3339),
		},
		Items: []MemoryCapsuleItem{{
			Kind:            "fact",
			RedactedSummary: "Pulse stores only minimal structured capsules.",
			Confidence:      0.8,
			EvidenceHint:    "current_turn",
			PrivacyTier:     "normal",
			Retention:       "project",
		}},
	}

	raw := valid
	raw.RawInputIncluded = true
	if _, err := s.RememberCapsule(raw); err == nil {
		t.Fatal("expected raw_input_included=true to be rejected")
	}

	missingSummary := valid
	missingSummary.Items[0].RedactedSummary = ""
	if _, err := s.RememberCapsule(missingSummary); err == nil {
		t.Fatal("expected missing redacted_summary to be rejected")
	}

	transcript := valid
	transcript.Items[0].RedactedSummary = strings.Repeat("User: hello\nAssistant: hi\n", 80)
	if _, err := s.RememberCapsule(transcript); err == nil {
		t.Fatal("expected transcript-like payload to be rejected")
	}

	badTimestamp := valid
	badTimestamp.Source.Timestamp = "yesterday"
	if _, err := s.RememberCapsule(badTimestamp); err == nil {
		t.Fatal("expected non-RFC3339 timestamp to be rejected")
	}
}

func TestRememberCapsuleRejectsSecretPathAndUnsafeTags(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	valid := MemoryCapsule{
		Schema: "pulse.memory_capsule.v1",
		Source: CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         "2026-06-02T09:00:00Z",
		},
		Items: []MemoryCapsuleItem{{
			Kind:            "fact",
			RedactedSummary: "Pulse stores minimal structured capsules.",
			Confidence:      0.8,
			EvidenceHint:    "current_turn",
			PrivacyTier:     "normal",
			Retention:       "project",
			Tags:            []string{"safe-tag"},
		}},
	}

	for _, summary := range []string{
		"User OpenAI key is sk-test",
		"token=abc123",
		"password is hunter2",
		"/Users/example/private/file.txt",
		"file:///Users/example/private/file.txt",
		"-----BEGIN PRIVATE KEY-----",
		"GitHub token ghp_abcdef",
	} {
		capsule := valid
		capsule.Items[0].RedactedSummary = summary
		if _, err := s.RememberCapsule(capsule); err == nil {
			t.Fatalf("expected secret/path-like summary to be rejected: %q", summary)
		}
	}

	for _, tag := range []string{
		"/Users/example",
		"token=abc",
		strings.Repeat("a", 65),
		"unsafe tag with spaces",
	} {
		capsule := valid
		capsule.Items[0].Tags = []string{tag}
		if _, err := s.RememberCapsule(capsule); err == nil {
			t.Fatalf("expected unsafe tag to be rejected: %q", tag)
		}
	}
}

func TestMemoryExportDeleteAndWipe(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ids, err := s.RememberCapsule(MemoryCapsule{
		Schema: "pulse.memory_capsule.v1",
		Source: CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         "2026-06-02T09:00:00Z",
		},
		Items: []MemoryCapsuleItem{{
			Kind:            "preference",
			RedactedSummary: "Narrow v1 to Claude Code before other ecosystems.",
			Confidence:      0.9,
			EvidenceHint:    "current_turn",
			PrivacyTier:     "normal",
			Retention:       "project",
		}},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}

	exported, err := s.ExportMemory()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(exported.Items) != 1 || exported.Items[0].ID != ids[0] {
		t.Fatalf("bad export: %#v", exported)
	}

	if err := s.DeleteMemory(ids[0]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	afterDelete, err := s.ExportMemory()
	if err != nil {
		t.Fatalf("export after delete: %v", err)
	}
	if len(afterDelete.Items) != 0 {
		t.Fatalf("expected delete to remove item, got %d", len(afterDelete.Items))
	}

	if _, err := s.ImportMemory(exported); err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := s.WipeMemory(); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	afterWipe, err := s.ExportMemory()
	if err != nil {
		t.Fatalf("export after wipe: %v", err)
	}
	if len(afterWipe.Items) != 0 {
		t.Fatalf("expected wipe to remove all items, got %d", len(afterWipe.Items))
	}
}

func syntheticTeamMemoryWrite() TeamMemoryWrite {
	return TeamMemoryWrite{
		Schema: TeamMemorySchema,
		Source: CapsuleSource{
			Host: "claude-code", ConversationScope: "current_turn",
			Timestamp: "2026-07-11T05:00:00+00:00",
		},
		Items: []TeamMemoryItem{
			{
				Kind: "decision", RedactedSummary: "The synthetic team chose a risk-based staged pilot rollout.",
				Confidence: 0.91, EvidenceHint: "current_turn", Tags: []string{"пилот", "rollout", "risk-based", "pilot"},
			},
			{
				Kind: "open_loop", RedactedSummary: "Confirm the synthetic pilot review owner next week.",
				Confidence: 0.82, EvidenceHint: "assistant_inferred", Tags: []string{"follow-up"},
			},
		},
		RawInputIncluded: false,
		PrivacyTier:      "normal",
		Retention:        "project",
		IdempotencyKey:   "team-memory-idempotency-0001",
	}
}

func cloneTeamMemoryWrite(write TeamMemoryWrite) TeamMemoryWrite {
	write.Items = append([]TeamMemoryItem(nil), write.Items...)
	for index := range write.Items {
		write.Items[index].Tags = append([]string(nil), write.Items[index].Tags...)
	}
	if write.TargetScope != nil {
		target := *write.TargetScope
		write.TargetScope = &target
	}
	return write
}

func TestStoreTeamMemoryCapsuleUsesCanonicalPersonalRootAndStaysOutOfLocalV1(t *testing.T) {
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()
	write := syntheticTeamMemoryWrite()
	originalInput := cloneTeamMemoryWrite(write)

	result, err := f.store.StoreTeamMemoryCapsule(
		context.Background(), f.permit, f.request.Writer, f.request.RequestID,
		f.actor.clientKey, write,
	)
	if err != nil {
		t.Fatalf("StoreTeamMemoryCapsule: %v", err)
	}
	if !reflect.DeepEqual(write, originalInput) {
		t.Fatalf("StoreTeamMemoryCapsule mutated caller input:\n got  %+v\n want %+v", write, originalInput)
	}
	if result.ObjectID == "" || result.AuditEventID == "" || result.Replayed ||
		result.Status != TeamObjectStatusStored || result.ProjectionState != TeamProjectionStatePending ||
		result.FullyProjected || len(result.CapsuleIDs) != 2 {
		t.Fatalf("team memory result = %+v", result)
	}
	if len(result.ProjectionJobs) != 2 {
		t.Fatalf("projection jobs = %+v", result.ProjectionJobs)
	}
	kinds := []string{result.ProjectionJobs[0].Kind, result.ProjectionJobs[1].Kind}
	sort.Strings(kinds)
	if !reflect.DeepEqual(kinds, []string{"embedding", "event"}) {
		t.Fatalf("projection kinds = %v", kinds)
	}

	var scopeType, scopeID, ownerID, authorID, privacy, retention string
	if err := f.store.DB().QueryRow(`
		SELECT scope_type, scope_id, COALESCE(owner_principal_id, ''),
		       author_principal_id, privacy_tier, retention
		  FROM team_object_registry WHERE object_id = ?`, result.ObjectID).Scan(
		&scopeType, &scopeID, &ownerID, &authorID, &privacy, &retention,
	); err != nil {
		t.Fatal(err)
	}
	if scopeType != string(teamauth.ScopePersonal) || scopeID != f.actor.member.PrincipalID ||
		ownerID != f.actor.member.PrincipalID || authorID != f.actor.binding.AgentPrincipalID ||
		privacy != write.PrivacyTier || retention != write.Retention {
		t.Fatalf("root scope/attribution = %q/%q owner=%q author=%q policy=%q/%q",
			scopeType, scopeID, ownerID, authorID, privacy, retention)
	}

	rows, err := f.store.DB().Query(`
		SELECT capsule_id, item_ordinal, schema_version, source_timestamp, kind,
		       redacted_summary, evidence_hint, tags_json, root_generation
		  FROM team_memory_capsules
		 WHERE root_object_id = ? ORDER BY item_ordinal`, result.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var storedIDs []string
	for rows.Next() {
		var id, schema, timestamp, kind, summary, evidence, tags string
		var ordinal, generation int
		if err := rows.Scan(&id, &ordinal, &schema, &timestamp, &kind, &summary, &evidence, &tags, &generation); err != nil {
			t.Fatal(err)
		}
		if ordinal < 0 || schema != TeamMemorySchema || timestamp != "2026-07-11T05:00:00.000Z" ||
			kind != write.Items[ordinal].Kind || summary != write.Items[ordinal].RedactedSummary ||
			evidence != write.Items[ordinal].EvidenceHint || generation != 1 {
			t.Fatalf("stored team capsule row is not canonical: ordinal=%d schema=%q timestamp=%q kind=%q summary=%q evidence=%q generation=%d",
				ordinal, schema, timestamp, kind, summary, evidence, generation)
		}
		storedIDs = append(storedIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(storedIDs, result.CapsuleIDs) {
		t.Fatalf("capsule IDs = %v, stored %v", result.CapsuleIDs, storedIDs)
	}
	var mappings int
	if err := f.store.DB().QueryRow(`
		SELECT count(*) FROM team_object_storage_map
		 WHERE object_id = ? AND representation_kind = 'memory_capsule'`, result.ObjectID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if mappings != len(result.CapsuleIDs) {
		t.Fatalf("storage mappings = %d, want %d", mappings, len(result.CapsuleIDs))
	}
	if _, err := f.store.CheckTeamPolicyReadiness(context.Background(), policyReadinessOptions(f.bootstrap, f.lease)); err != nil {
		t.Fatalf("valid team memory made policy readiness fail: %v", err)
	}

	localRecall, err := f.store.RecallMemory(RecallMemoryQuery{Query: "staged pilot rollout", PrivacyCeiling: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	localExport, err := f.store.ExportMemory()
	if err != nil {
		t.Fatal(err)
	}
	localStatus, err := f.store.MemoryStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(localRecall) != 0 || len(localExport.Items) != 0 || localStatus.ItemCount != 0 {
		t.Fatalf("team rows leaked into local v1: recall=%v export=%d status=%+v", localRecall, len(localExport.Items), localStatus)
	}
}

func TestStoreTeamMemoryCapsulePersistsAuthorizedProjectScope(t *testing.T) {
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()
	project, err := f.store.CreateTeamProject(context.Background(), f.bootstrap.OwnerPrincipalID, "Synthetic capsule project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GrantProjectAccess(context.Background(), GrantProjectAccessRequest{
		ActorPrincipalID: f.bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
		TargetPrincipalID: f.actor.binding.AgentPrincipalID, AccessLevel: "write",
	}); err != nil {
		t.Fatal(err)
	}
	authorization := mutationWriteRequest(f.bootstrap, f.actor)
	authorization.Context.ProjectID = project.ProjectID
	authorization.RequestedScope = &teamauth.CanonicalScope{Type: teamauth.ScopeProject, ID: project.ProjectID}
	permit, err := f.store.AuthorizeTeamMutation(context.Background(), authorization)
	if err != nil {
		t.Fatal(err)
	}
	write := syntheticTeamMemoryWrite()
	write.ActiveContext.ProjectID = project.ProjectID
	write.TargetScope = &TeamMemoryTarget{Type: teamauth.ScopeProject, ID: project.ProjectID}

	result, err := f.store.StoreTeamMemoryCapsule(
		context.Background(), permit, f.request.Writer, f.request.RequestID,
		f.actor.clientKey, write,
	)
	if err != nil {
		t.Fatal(err)
	}
	var scopeType, scopeID, ownerID string
	if err := f.store.DB().QueryRow(`
		SELECT scope_type, scope_id, COALESCE(owner_principal_id, '')
		  FROM team_object_registry WHERE object_id = ?`, result.ObjectID).Scan(&scopeType, &scopeID, &ownerID); err != nil {
		t.Fatal(err)
	}
	if scopeType != string(teamauth.ScopeProject) || scopeID != project.ProjectID || ownerID != f.actor.member.PrincipalID {
		t.Fatalf("project root = %q/%q owner=%q", scopeType, scopeID, ownerID)
	}
	var rows int
	if err := f.store.DB().QueryRow(`
		SELECT count(*) FROM team_memory_capsules
		 WHERE root_object_id = ? AND scope_type = 'project' AND scope_id = ? AND root_generation = 1`,
		result.ObjectID, project.ProjectID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != len(write.Items) {
		t.Fatalf("project capsule rows = %d, want %d", rows, len(write.Items))
	}
}

func TestStoreTeamMemoryCapsuleKeepsEmptyTagsReadyAndCorrelatesActiveProject(t *testing.T) {
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()
	project, err := f.store.CreateTeamProject(context.Background(), f.bootstrap.OwnerPrincipalID, "Empty tags audit project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GrantProjectAccess(context.Background(), GrantProjectAccessRequest{
		ActorPrincipalID: f.bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
		TargetPrincipalID: f.actor.binding.AgentPrincipalID, AccessLevel: "write",
	}); err != nil {
		t.Fatal(err)
	}
	authorization := mutationWriteRequest(f.bootstrap, f.actor)
	authorization.Context.ProjectID = project.ProjectID
	permit, err := f.store.AuthorizeTeamMutation(context.Background(), authorization)
	if err != nil {
		t.Fatal(err)
	}
	write := syntheticTeamMemoryWrite()
	write.ActiveContext.ProjectID = project.ProjectID
	write.Items[0].Tags = nil

	result, err := f.store.StoreTeamMemoryCapsule(
		context.Background(), permit, f.request.Writer, "request-empty-tags-project",
		f.actor.clientKey, write,
	)
	if err != nil {
		t.Fatal(err)
	}
	var tagsJSON, auditProject string
	if err := f.store.DB().QueryRow(`
		SELECT tags_json FROM team_memory_capsules WHERE root_object_id = ?`,
		result.ObjectID,
	).Scan(&tagsJSON); err != nil {
		t.Fatal(err)
	}
	if tagsJSON != "[]" {
		t.Fatalf("empty tags JSON = %q, want []", tagsJSON)
	}
	if err := f.store.DB().QueryRow(`
		SELECT COALESCE(project_id, '') FROM team_audit_events WHERE event_id = ?`,
		result.AuditEventID,
	).Scan(&auditProject); err != nil {
		t.Fatal(err)
	}
	if auditProject != project.ProjectID {
		t.Fatalf("personal write audit project = %q, want %q", auditProject, project.ProjectID)
	}
	if _, err := f.store.CheckTeamPolicyReadiness(
		context.Background(), policyReadinessOptions(f.bootstrap, f.lease),
	); err != nil {
		t.Fatalf("empty tags write degraded readiness: %v", err)
	}
}

func TestTeamMemoryServiceWithoutExplicitGrantedTargetCannotObtainPermit(t *testing.T) {
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()
	service, err := f.store.RegisterServicePrincipal(context.Background(), RegisterServicePrincipalRequest{
		ActorPrincipalID: f.bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		ClientID:         "team-memory-service",
	})
	if err != nil {
		t.Fatal(err)
	}
	before := teamMemoryTableCounts(t, f.store)
	_, err = f.store.AuthorizeTeamMutation(context.Background(), TeamMutationAuthorizationRequest{
		PrincipalID: service.PrincipalID,
		OAuthClientKey: teamauth.OAuthClientKey(
			"https://issuer.example", "team-memory-service",
		),
		Action:       teamauth.ActionWrite,
		Capabilities: []teamauth.Capability{teamauth.CapabilityWrite},
		Context:      teamauth.ActiveContext{TeamID: f.bootstrap.TeamID},
		ObjectKind:   "memory",
	})
	if !errors.Is(err, ErrTeamPolicyDenied) {
		t.Fatalf("service personal default authorization error = %v", err)
	}
	if after := teamMemoryTableCounts(t, f.store); !reflect.DeepEqual(after, before) {
		t.Fatalf("denied service write mutated memory tables: before=%v after=%v", before, after)
	}
}

func TestStoreTeamMemoryCapsuleRollsBackDomainRowsAfterLaterFailure(t *testing.T) {
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()
	before := teamMemoryTableCounts(t, f.store)
	if _, err := f.store.DB().Exec(`
		CREATE TRIGGER u9_audit_failure BEFORE INSERT ON team_audit_events
		WHEN NEW.action = 'team.object.write'
		BEGIN SELECT RAISE(ABORT, 'synthetic raw audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.StoreTeamMemoryCapsule(
		context.Background(), f.permit, f.request.Writer, f.request.RequestID,
		f.actor.clientKey, syntheticTeamMemoryWrite(),
	)
	if err != ErrTeamObjectCommitFailed || strings.Contains(err.Error(), "synthetic raw audit failure") {
		t.Fatalf("rollback error = %v", err)
	}
	if after := teamMemoryTableCounts(t, f.store); !reflect.DeepEqual(after, before) {
		t.Fatalf("team capsule partially committed: before=%v after=%v", before, after)
	}
}

func TestStoreTeamMemoryCapsuleResponseLossReplayReturnsOriginalIDs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "team.db")
	f := newTeamObjectWriteFixtureAt(t, path)
	write := syntheticTeamMemoryWrite()
	first, err := f.store.StoreTeamMemoryCapsule(ctx, f.permit, f.request.Writer,
		f.request.RequestID, f.actor.clientKey, write)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.StoreTeamMemoryCapsule(ctx, f.permit, f.request.Writer,
		"request-memory-replay-0002", f.actor.clientKey, write)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || first.ObjectID != replay.ObjectID || first.AuditEventID != replay.AuditEventID ||
		!reflect.DeepEqual(first.CapsuleIDs, replay.CapsuleIDs) || !reflect.DeepEqual(first.ProjectionJobs, replay.ProjectionJobs) {
		t.Fatalf("response-loss replay changed IDs: first=%+v replay=%+v", first, replay)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenTeam(path, reviewTeamOptions(testBootstrapRoot()))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := reopened.StoreTeamMemoryCapsule(ctx, f.permit, f.request.Writer,
		"request-memory-restart-0003", f.actor.clientKey, write)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.Replayed || first.ObjectID != restarted.ObjectID ||
		!reflect.DeepEqual(first.CapsuleIDs, restarted.CapsuleIDs) {
		t.Fatalf("restart replay changed IDs: first=%+v restarted=%+v", first, restarted)
	}
	changed := cloneTeamMemoryWrite(write)
	changed.Items[0].RedactedSummary = "The same key now describes a different synthetic decision."
	if _, err := reopened.StoreTeamMemoryCapsule(ctx, f.permit, f.request.Writer,
		"request-memory-conflict-0004", f.actor.clientKey, changed); !errors.Is(err, ErrTeamIdempotencyConflict) {
		t.Fatalf("changed body error = %v", err)
	}
}

func TestStoreTeamMemoryCapsuleRejectsUnsafeOrPermitSpoofingInputs(t *testing.T) {
	f := newTeamObjectWriteFixture(t)
	defer f.store.Close()
	tests := []struct {
		name   string
		mutate func(*TeamMemoryWrite, *string)
	}{
		{name: "local schema", mutate: func(write *TeamMemoryWrite, _ *string) { write.Schema = MemoryCapsuleSchema }},
		{name: "raw input", mutate: func(write *TeamMemoryWrite, _ *string) { write.RawInputIncluded = true }},
		{name: "secret", mutate: func(write *TeamMemoryWrite, _ *string) { write.Items[0].RedactedSummary = "token=synthetic-secret" }},
		{name: "path", mutate: func(write *TeamMemoryWrite, _ *string) { write.Items[0].RedactedSummary = "/Users/example/private.txt" }},
		{name: "transcript", mutate: func(write *TeamMemoryWrite, _ *string) {
			write.Items[0].RedactedSummary = strings.Repeat("User: hello\nAssistant: hi\n", 40)
		}},
		{name: "too many tags", mutate: func(write *TeamMemoryWrite, _ *string) {
			write.Items[0].Tags = make([]string, 33)
			for index := range write.Items[0].Tags {
				write.Items[0].Tags[index] = "tag" + strings.Repeat("x", index)
			}
		}},
		{name: "context spoof", mutate: func(write *TeamMemoryWrite, _ *string) { write.ActiveContext.ProjectID = "project_spoofed" }},
		{name: "personal owner spoof", mutate: func(write *TeamMemoryWrite, _ *string) {
			write.TargetScope = &TeamMemoryTarget{Type: teamauth.ScopePersonal, ID: "principal_spoofed"}
		}},
		{name: "team target", mutate: func(write *TeamMemoryWrite, _ *string) {
			write.TargetScope = &TeamMemoryTarget{Type: teamauth.ScopeTeam, ID: "team_spoofed"}
		}},
		{name: "invalid privacy", mutate: func(write *TeamMemoryWrite, _ *string) { write.PrivacyTier = "public" }},
		{name: "invalid retention", mutate: func(write *TeamMemoryWrite, _ *string) { write.Retention = "forever" }},
		{name: "invalid expiry", mutate: func(write *TeamMemoryWrite, _ *string) { write.ExpiresAt = "tomorrow" }},
		{name: "unsafe idempotency", mutate: func(write *TeamMemoryWrite, _ *string) { write.IdempotencyKey = "../../raw key" }},
		{name: "oauth client spoof", mutate: func(_ *TeamMemoryWrite, oauth *string) { *oauth = strings.Repeat("a", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			write := cloneTeamMemoryWrite(syntheticTeamMemoryWrite())
			oauth := f.actor.clientKey
			test.mutate(&write, &oauth)
			before := teamMemoryTableCounts(t, f.store)
			if _, err := f.store.StoreTeamMemoryCapsule(
				context.Background(), f.permit, f.request.Writer,
				"request-memory-invalid-"+strings.ReplaceAll(test.name, " ", "-"), oauth, write,
			); !errors.Is(err, ErrTeamMemoryInvalid) && !errors.Is(err, ErrTeamObjectInvalid) {
				t.Fatalf("invalid input error = %v", err)
			}
			if after := teamMemoryTableCounts(t, f.store); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid input wrote rows: before=%v after=%v", before, after)
			}
		})
	}
}

func TestLocalMemoryCapsuleV1StillRejectsTeamContract(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	teamAsLocal := MemoryCapsule{
		Schema: TeamMemorySchema, Source: syntheticTeamMemoryWrite().Source,
		Items: []MemoryCapsuleItem{{
			Kind: "fact", RedactedSummary: "Synthetic team memory must not enter local v1.",
			Confidence: 0.9, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
		}},
	}
	if _, err := s.RememberCapsule(teamAsLocal); err == nil {
		t.Fatal("local RememberCapsule accepted team schema")
	}
	local := teamAsLocal
	local.Schema = MemoryCapsuleSchema
	if _, err := s.RememberCapsule(local); err != nil {
		t.Fatalf("local v1 behavior changed: %v", err)
	}
}

func teamMemoryTableCounts(t *testing.T, s *Store) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{
		"team_memory_capsules", "team_object_registry", "team_object_storage_map",
		"team_idempotency_records", "team_audit_events", "team_audit_event_order", "team_projection_jobs",
	} {
		var count int
		if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}
