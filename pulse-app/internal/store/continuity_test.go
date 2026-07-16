package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTrimMarkdownToBudgetPreservesUTF8Boundaries(t *testing.T) {
	markdown := strings.Repeat("я🌱", 30)
	got := trimMarkdownToBudget(markdown, 20)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated resume is not valid UTF-8: %q", got)
	}
	if len([]byte(got)) > 80 {
		t.Fatalf("truncated resume bytes=%d, want <=80", len([]byte(got)))
	}
	if !strings.Contains(got, "[truncated to Pulse resume budget]") {
		t.Fatalf("truncated resume lost marker: %q", got)
	}
}

func TestCheckpointResumeBuildsStructuredBlockUnderBudget(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.SaveCheckpoint(ContinuityCheckpoint{
		ThreadID:         "pulse-distribution",
		SessionID:        "claude-code:pulse-distribution:2026-06-02T12-00-00Z",
		Host:             "claude-code",
		ProjectID:        "garden",
		Summary:          "We narrowed Pulse v1 to memory plus continuity for Claude Code first.",
		Decisions:        []string{"Pulse owns continuity primitives; Heart and Atlas stay outside MCP v1."},
		OpenLoops:        []string{"Build local viewer showing what will be injected next."},
		DoNotRepeat:      []string{"Do not pitch Pulse as a generic graph memory product."},
		EmotionalAnchors: []string{"The Vitaly call hurt and changed the product wedge toward context relief."},
		StateSignals:     []string{"User wants pleasant continuity without re-explaining the project."},
		SourceRefs:       []string{"pulse:checkpoint:manual"},
		Confidence:       0.91,
	}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	resume, err := s.BuildResume(ResumeQuery{
		ThreadID:    "pulse-distribution",
		ProjectID:   "garden",
		Host:        "claude-code",
		TokenBudget: 900,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	if ContinuitySchema != "pulse.continuity.v2" || resume.Schema != ContinuitySchema {
		t.Fatalf("bad schema: %q", resume.Schema)
	}
	if resume.ThreadID != "pulse-distribution" {
		t.Fatalf("bad thread: %q", resume.ThreadID)
	}
	for _, want := range []string{
		"# Pulse Resume",
		"## Where we left off",
		"## Active decisions",
		"## Open loops",
		"## Do-not-repeat",
		"## Relevant emotional/state context",
		"## Suggested next step",
		"## Evidence refs",
		"Pulse owns continuity primitives",
		"Vitaly call hurt",
	} {
		if !strings.Contains(resume.ResumeMarkdown, want) {
			t.Fatalf("resume missing %q:\n%s", want, resume.ResumeMarkdown)
		}
	}
	if resume.TokenEstimate > 900 {
		t.Fatalf("resume exceeded budget: %d", resume.TokenEstimate)
	}
}

func TestResumeTokenEconomyReportsOnlyExactLocalOfferFacts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.SaveCheckpoint(ContinuityCheckpoint{
		ThreadID:  "pulse-honest-ledger",
		SessionID: "codex:pulse-honest-ledger:test",
		Host:      "codex",
		ProjectID: "garden",
		Summary:   "Pulse must explain every token-economy number from immutable receipts.",
	}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	resume, err := s.BuildResume(ResumeQuery{
		ThreadID:    "pulse-honest-ledger",
		ProjectID:   "garden",
		Host:        "codex",
		TokenBudget: 900,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	if resume.TokenEconomy.State != TokenEconomyCollectingBaseline {
		t.Fatalf("state = %q, want collecting_baseline", resume.TokenEconomy.State)
	}
	if resume.TokenEconomy.RenderedBytes != len([]byte(resume.ResumeMarkdown)) {
		t.Fatalf("rendered bytes = %d, want %d", resume.TokenEconomy.RenderedBytes, len([]byte(resume.ResumeMarkdown)))
	}
	if resume.TokenEconomy.PulseTokens != resume.TokenEstimate {
		t.Fatalf("pulse tokens = %d, want exact local estimate %d", resume.TokenEconomy.PulseTokens, resume.TokenEstimate)
	}
	if resume.TokenEconomy.MethodID != TokenEconomyMethodUTF8BytesDiv4Ceil || resume.TokenEconomy.MethodVersion != "1" {
		t.Fatalf("unexpected local count method: %#v", resume.TokenEconomy)
	}
	if resume.BaselineKind != "canonical_structured_resume_v1" || resume.SourceEquivalentTokens == nil ||
		*resume.SourceEquivalentTokens < resume.TokenEstimate || resume.CoverageCounted < 1 ||
		resume.CoverageCounted > resume.CoverageTotal {
		t.Fatalf("resume baseline is not exact and covered: %#v", resume)
	}

	encoded, err := json.Marshal(resume)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"estimated_raw_tokens", "estimated_saved_tokens", "resume_tokens"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("resume API still exposes fabricated economy field %q: %s", forbidden, encoded)
		}
	}
}

func TestResumeOmitsBaselineWhenNoCanonicalSourceManifestIsPresent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	resume, err := s.BuildResume(ResumeQuery{ThreadID: "empty", ProjectID: "garden", Host: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if resume.BaselineKind != "" || resume.SourceEquivalentTokens != nil ||
		resume.CoverageCounted != 0 || resume.CoverageTotal != 0 {
		t.Fatalf("empty resume invented a comparison baseline: %#v", resume)
	}
}

func TestObserveRejectsRawRefsUnlessEnabled(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	err = s.SaveObservation(ContinuityObservation{
		ThreadID:        "pulse-distribution",
		SessionID:       "claude-code:pulse-distribution:test",
		Host:            "claude-code",
		EventType:       "UserPromptSubmit",
		RedactedSummary: "User prompt captured by Pulse local-auto hook.",
		RawRef:          "raw:prompt:1",
	}, false)
	if err == nil {
		t.Fatal("expected raw_ref to be rejected when raw refs are disabled")
	}

	err = s.SaveObservation(ContinuityObservation{
		ThreadID:        "pulse-distribution",
		SessionID:       "claude-code:pulse-distribution:test",
		Host:            "claude-code",
		EventType:       "UserPromptSubmit",
		RedactedSummary: "User prompt captured by Pulse local-auto hook.",
		RawRef:          "raw:prompt:1",
	}, true)
	if err != nil {
		t.Fatalf("expected raw_ref to be accepted when explicitly enabled: %v", err)
	}
}

func TestResumePromotesRememberObservationIntoActiveDecision(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.SaveObservation(ContinuityObservation{
		ThreadID:        "pulse-distribution",
		SessionID:       "claude-code:pulse-distribution:test",
		Host:            "claude-code",
		EventType:       "PostToolUse",
		RedactedSummary: "Decision: Atlas must not own the People Graph; Pulse owns portable continuity memory.",
		SourceRef:       "pulse:hook:PostToolUse:test",
	}, false); err != nil {
		t.Fatalf("observation: %v", err)
	}

	resume, err := s.BuildResume(ResumeQuery{
		ThreadID:    "pulse-distribution",
		ProjectID:   "garden",
		Host:        "claude-code",
		TokenBudget: 900,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	if !strings.Contains(resume.ResumeMarkdown, "## Active decisions\n- Atlas must not own the People Graph; Pulse owns portable continuity memory.") {
		t.Fatalf("resume did not promote remembered decision:\n%s", resume.ResumeMarkdown)
	}
	if len(resume.Sections.ActiveDecisions) != 1 || !strings.Contains(resume.Sections.ActiveDecisions[0], "Atlas must not own") {
		t.Fatalf("active decisions not populated from remember observation: %#v", resume.Sections.ActiveDecisions)
	}
}

func TestResumeIncludesHostExtractedDecisionCapsuleWithoutCheckpoint(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ids, err := s.RememberCapsule(MemoryCapsule{
		Schema: MemoryCapsuleSchema,
		Source: CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         "2026-06-08T06:20:00Z",
		},
		Items: []MemoryCapsuleItem{{
			Kind:            "decision",
			RedactedSummary: "Atlas must not own the People Graph; Pulse owns portable continuity memory.",
			Confidence:      1.0,
			EvidenceHint:    "user_selected",
			PrivacyTier:     "private",
			Retention:       "project",
			Tags:            []string{"first_proof", "atlas", "people_graph", "pulse_continuity"},
		}},
		RawInputIncluded: false,
	})
	if err != nil {
		t.Fatalf("remember capsule: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected one memory id, got %#v", ids)
	}

	resume, err := s.BuildResume(ResumeQuery{
		ThreadID:    "pulse-v041-proof",
		ProjectID:   "garden",
		Host:        "claude-code",
		TokenBudget: 900,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	if !strings.Contains(resume.ResumeMarkdown, "## Active decisions\n- Atlas must not own the People Graph; Pulse owns portable continuity memory.") {
		t.Fatalf("resume did not include host-extracted decision capsule:\n%s", resume.ResumeMarkdown)
	}
	if len(resume.Sections.ActiveDecisions) != 1 || !strings.Contains(resume.Sections.ActiveDecisions[0], "Atlas must not own") {
		t.Fatalf("active decisions not populated from memory capsule: %#v", resume.Sections.ActiveDecisions)
	}
	if len(resume.EvidenceRefs) != 1 || resume.EvidenceRefs[0] != "pulse:"+ids[0] {
		t.Fatalf("memory capsule evidence refs missing: %#v", resume.EvidenceRefs)
	}
	if len(resume.IncludedObjectIDs) != 1 || resume.IncludedObjectIDs[0] != ids[0] {
		t.Fatalf("exact rendered manifest lost canonical object: %#v", resume.IncludedObjectIDs)
	}
	if len(resume.IncludedEvidenceIDs) != 1 || resume.IncludedEvidenceIDs[0] != "pulse:"+ids[0] {
		t.Fatalf("exact rendered manifest lost evidence ref: %#v", resume.IncludedEvidenceIDs)
	}
}

func TestIncludedResumeManifestExcludesReferencesTrimmedOutOfExactMarkdown(t *testing.T) {
	includedObjects, includedEvidence := includedResumeManifest(
		"# Pulse Resume\n\n## Active decisions\n- Rendered decision.\n## Evidence refs\n- pulse:memory_rendered\n",
		[]RecalledMemoryItem{
			{ID: "memory_rendered", Summary: "Rendered decision."},
			{ID: "memory_trimmed", Summary: "This decision was trimmed out."},
		},
		[]string{"pulse:memory_rendered", "pulse:memory_trimmed"},
	)
	if len(includedObjects) != 1 || includedObjects[0] != "memory_rendered" ||
		len(includedEvidence) != 1 || includedEvidence[0] != "pulse:memory_rendered" {
		t.Fatalf("manifest claimed trimmed provenance: objects=%#v evidence=%#v", includedObjects, includedEvidence)
	}
}

func TestIncludedResumeManifestDoesNotInferOverlappingSummariesOrReferencePrefixes(t *testing.T) {
	objects, evidence := includedResumeManifest(
		"# Pulse Resume\n\n## Active decisions\n- Keep Pulse local and private.\n## Evidence refs\n- pulse:memory_010\n",
		[]RecalledMemoryItem{
			{ID: "memory_01", Summary: "Keep Pulse local"},
			{ID: "memory_010", Summary: "Keep Pulse local and private."},
		},
		[]string{"pulse:memory_01", "pulse:memory_010"},
	)
	if len(objects) != 1 || objects[0] != "memory_010" || len(evidence) != 1 || evidence[0] != "pulse:memory_010" {
		t.Fatalf("overlapping text forged provenance: objects=%#v evidence=%#v", objects, evidence)
	}
}

func TestContinuityRejectsUnsafeSourceRefs(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for _, ref := range []string{
		"/Users/example/private/file.txt",
		"file:///Users/example/private/file.txt",
		"pulse:hook:token=abc",
		"https://example.com/?token=abc",
		"ghp_secret",
	} {
		err := s.SaveCheckpoint(ContinuityCheckpoint{
			ThreadID:   "pulse-distribution",
			SessionID:  "claude-code:pulse-distribution:test",
			Host:       "claude-code",
			ProjectID:  "garden",
			Summary:    "A safe checkpoint summary.",
			SourceRefs: []string{ref},
			Confidence: 0.8,
		})
		if err == nil {
			t.Fatalf("expected unsafe source ref to be rejected: %q", ref)
		}
	}

	err = s.SaveObservation(ContinuityObservation{
		ThreadID:        "pulse-distribution",
		SessionID:       "claude-code:pulse-distribution:test",
		Host:            "claude-code",
		EventType:       "UserPromptSubmit",
		RedactedSummary: "A safe redacted observation.",
		SourceRef:       "file:///Users/example/private/file.txt",
	}, false)
	if err == nil {
		t.Fatal("expected unsafe observation source_ref to be rejected")
	}

	err = s.SaveObservation(ContinuityObservation{
		ThreadID:        "pulse-distribution",
		SessionID:       "claude-code:pulse-distribution:test",
		Host:            "claude-code",
		EventType:       "UserPromptSubmit",
		RedactedSummary: "A safe redacted observation.",
		RawRef:          "/Users/example/private/raw.txt",
	}, true)
	if err == nil {
		t.Fatal("expected unsafe raw_ref to be rejected even when raw refs are enabled")
	}
}

func TestWipeMemoryClearsContinuityFromFutureResume(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := s.SaveCheckpoint(ContinuityCheckpoint{
		ThreadID:   "pulse-distribution",
		SessionID:  "claude-code:pulse-distribution:test",
		Host:       "claude-code",
		ProjectID:  "garden",
		Summary:    "A checkpoint that should disappear after wipe.",
		OpenLoops:  []string{"This loop should disappear."},
		Confidence: 0.8,
	}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	if err := s.WipeMemory(); err != nil {
		t.Fatalf("wipe: %v", err)
	}

	resume, err := s.BuildResume(ResumeQuery{ThreadID: "pulse-distribution", TokenBudget: 900})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if strings.Contains(resume.ResumeMarkdown, "should disappear") {
		t.Fatalf("wipe left continuity data in resume:\n%s", resume.ResumeMarkdown)
	}
}
