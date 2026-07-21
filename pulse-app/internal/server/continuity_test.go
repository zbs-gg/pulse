package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

func TestContinuityCheckpointResumeAndViewerEndpoints(t *testing.T) {
	_, ts := newMemoryServerWithBilling(t, BillingStatus{
		Mode:              "local-auto",
		Host:              "claude-code",
		BackendLLMEnabled: false,
		RawCaptureEnabled: false,
	})
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodPost, "/continuity/checkpoint", map[string]any{
		"thread_id":         "pulse-distribution",
		"session_id":        "claude-code:pulse-distribution:test",
		"host":              "claude-code",
		"project_id":        "garden",
		"summary":           "Pulse continuity v0 writes checkpoints at Stop.",
		"decisions":         []string{"Pulse owns memory and continuity."},
		"open_loops":        []string{"Show what Pulse will inject next session."},
		"do_not_repeat":     []string{"Do not expose raw transcripts through MCP."},
		"emotional_anchors": []string{"The product wedge changed after a painful call."},
		"state_signals":     []string{"User wants effortless continuation."},
		"source_refs":       []string{"pulse:checkpoint:test"},
		"confidence":        0.9,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("checkpoint status=%d", resp.StatusCode)
	}

	resp = pulseJSON(t, ts, http.MethodPost, "/continuity/resume", map[string]any{
		"thread_id":    "pulse-distribution",
		"project_id":   "garden",
		"host":         "claude-code",
		"token_budget": 900,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status=%d", resp.StatusCode)
	}
	var resume struct {
		ResumeMarkdown string `json:"resume_markdown"`
		TokenEstimate  int    `json:"token_estimate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resume); err != nil {
		t.Fatalf("decode resume: %v", err)
	}
	if resume.TokenEstimate > 900 {
		t.Fatalf("resume exceeded budget: %d", resume.TokenEstimate)
	}
	if !strings.Contains(resume.ResumeMarkdown, "## Open loops") ||
		!strings.Contains(resume.ResumeMarkdown, "Show what Pulse will inject") {
		t.Fatalf("bad resume:\n%s", resume.ResumeMarkdown)
	}

	resp = pulseJSON(t, ts, http.MethodGet, "/viewer/data?thread_id=pulse-distribution&key=secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer data status=%d", resp.StatusCode)
	}
	var viewer struct {
		NextResume struct {
			ResumeMarkdown string `json:"resume_markdown"`
		} `json:"next_resume"`
		MaterialGraph struct {
			Schema   string `json:"schema"`
			ThreadID string `json:"thread_id"`
			Nodes    []struct {
				ID         string   `json:"id"`
				Kind       string   `json:"kind"`
				Label      string   `json:"label"`
				SourceRefs []string `json:"source_refs"`
			} `json:"nodes"`
			ContinuityPack struct {
				MaterialRefs []string `json:"material_refs"`
			} `json:"continuity_pack"`
		} `json:"material_graph"`
		RecentSessions []any    `json:"recent_sessions"`
		OpenLoops      []string `json:"open_loops"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&viewer); err != nil {
		t.Fatalf("decode viewer: %v", err)
	}
	if !strings.Contains(viewer.NextResume.ResumeMarkdown, "What Pulse will tell") &&
		!strings.Contains(viewer.NextResume.ResumeMarkdown, "# Pulse Resume") {
		t.Fatalf("viewer should expose next resume: %#v", viewer.NextResume)
	}
	if len(viewer.OpenLoops) == 0 {
		t.Fatalf("viewer should expose open loops: %#v", viewer)
	}
	if viewer.MaterialGraph.Schema != "pulse.material_graph.v0" {
		t.Fatalf("viewer should expose material graph: %#v", viewer.MaterialGraph)
	}
	if viewer.MaterialGraph.ThreadID != "pulse-distribution" {
		t.Fatalf("viewer material graph thread mismatch: %#v", viewer.MaterialGraph)
	}
	var foundDecision bool
	for _, node := range viewer.MaterialGraph.Nodes {
		if node.Kind == "decision" && strings.Contains(node.Label, "Pulse owns memory") {
			foundDecision = true
			if len(node.SourceRefs) == 0 {
				t.Fatalf("viewer material graph decision missing source refs: %#v", node)
			}
		}
	}
	if !foundDecision || len(viewer.MaterialGraph.ContinuityPack.MaterialRefs) == 0 {
		t.Fatalf("viewer material graph should carry focused continuity refs: %#v", viewer.MaterialGraph)
	}
}

func TestFirstRunViewerKeepsTrustBoundaries(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodGet, "/viewer?key=secret&thread_id=cli&first_run=1&host=claude-code", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer page status=%d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read viewer page: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		"Your first memory starts here",
		"Nothing is pending until a harness submits a visible Memory Tray candidate.",
		"Hide in this preview",
		"Pulse guess: maybe curiosity?",
		"No emotion is stored until you choose one.",
		"Connector: available. Run doctor for actual MCP and hook readiness. Sessions: not scanned yet.",
		"Connector: available. Source: not scanned yet. Sessions: not scanned yet.",
		"Connector: archive import. Source: choose an archive first. Sessions: not scanned yet.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("first-run viewer missing trust copy %q:\n%s", want, body)
		}
	}
	for _, blocked := range []string{
		"Your first memory is already here",
		"overload",
		"Detected:",
		"Sessions: ready",
		"Ready to import structured memory",
	} {
		if strings.Contains(body, blocked) {
			t.Fatalf("first-run viewer leaked overclaim %q:\n%s", blocked, body)
		}
	}
}

func TestViewerFirstMemoryPendingAndSavedStates(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodGet, "/viewer/data?thread_id=cli&key=secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer data before status=%d", resp.StatusCode)
	}
	var before struct {
		FirstMemory struct {
			Status  string `json:"status"`
			ID      string `json:"id"`
			Summary string `json:"summary"`
		} `json:"first_memory"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&before); err != nil {
		t.Fatalf("decode viewer before: %v", err)
	}
	if before.FirstMemory.Status != "pending" || before.FirstMemory.ID != "" {
		t.Fatalf("first memory should be pending before install memory is saved: %#v", before.FirstMemory)
	}

	resp = pulseJSON(t, ts, http.MethodPost, "/memory/remember", map[string]any{
		"schema": "pulse.memory_capsule.v1",
		"source": map[string]any{
			"host":               "pulse-cli",
			"conversation_scope": "install_event",
			"timestamp":          "2026-06-07T09:00:00Z",
		},
		"items": []map[string]any{
			{
				"kind":             "system_event",
				"redacted_summary": "User installed Pulse MCP and connected it to Claude Code.",
				"confidence":       1.0,
				"evidence_hint":    "user_confirmed",
				"privacy_tier":     "private",
				"retention":        "project",
				"tags":             []string{"pulse_install", "first_memory", "claude_code"},
			},
		},
		"raw_input_included": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remember install memory status=%d", resp.StatusCode)
	}

	resp = pulseJSON(t, ts, http.MethodGet, "/viewer/data?thread_id=cli&key=secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer data after status=%d", resp.StatusCode)
	}
	var after struct {
		FirstMemory struct {
			Status  string `json:"status"`
			ID      string `json:"id"`
			Summary string `json:"summary"`
		} `json:"first_memory"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&after); err != nil {
		t.Fatalf("decode viewer after: %v", err)
	}
	if after.FirstMemory.Status != "saved" || after.FirstMemory.ID == "" {
		t.Fatalf("first memory should include a saved ID after install memory is persisted: %#v", after.FirstMemory)
	}
	if !strings.Contains(after.FirstMemory.Summary, "connected it to Claude Code") {
		t.Fatalf("first memory summary mismatch: %#v", after.FirstMemory)
	}
}

func TestViewerDataActivityLogsAreHumanReadable(t *testing.T) {
	_, ts := newMemoryServerWithBilling(t, BillingStatus{
		Mode:              "local-auto",
		Host:              "claude-code",
		BackendLLMEnabled: false,
		RawCaptureEnabled: false,
	})
	defer ts.Close()

	for _, body := range []map[string]any{
		{
			"thread_id":        "pulse-distribution",
			"session_id":       "claude-code:pulse-distribution:test",
			"host":             "claude-code",
			"event_type":       "PostToolUse",
			"redacted_summary": "event_triggered_llm_logged",
			"source_ref":       "pulse:hook:PostToolUse:2026-06-07T10:00:00Z",
		},
		{
			"thread_id":        "pulse-distribution",
			"session_id":       "claude-code:pulse-distribution:test",
			"host":             "claude-code",
			"event_type":       "UserPromptSubmit",
			"redacted_summary": "UserPromptSubmit: user prompt submitted. Raw prompt hidden by default.",
			"source_ref":       "pulse:hook:UserPromptSubmit:2026-06-07T10:01:00Z",
		},
	} {
		resp := pulseJSON(t, ts, http.MethodPost, "/continuity/observe", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("observe status=%d", resp.StatusCode)
		}
	}

	resp := pulseJSON(t, ts, http.MethodGet, "/viewer/data?thread_id=pulse-distribution&key=secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer data status=%d", resp.StatusCode)
	}
	var viewer struct {
		Activity []struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"activity"`
		NextResume struct {
			Sections struct {
				Recent []string `json:"recent_local_auto_observations"`
			} `json:"sections"`
		} `json:"next_resume"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&viewer); err != nil {
		t.Fatalf("decode viewer: %v", err)
	}
	if len(viewer.Activity) < 2 {
		t.Fatalf("viewer activity should expose recent human-readable events: %#v", viewer.Activity)
	}
	joined, _ := json.Marshal(viewer)
	for _, bad := range []string{
		"event_triggered_llm_logged",
		"UserPromptSubmit:",
		"PostToolUse:",
	} {
		if strings.Contains(string(joined), bad) {
			t.Fatalf("viewer activity/resume leaked machine wording %q:\n%s", bad, joined)
		}
	}
	for _, want := range []string{
		"AI activity recorded",
		"Pulse recorded a background model event",
		"Prompt noticed",
		"raw text is hidden",
	} {
		if !strings.Contains(string(joined), want) {
			t.Fatalf("viewer activity missing human copy %q:\n%s", want, joined)
		}
	}
}

func TestContinuityObserveRawRefRequiresLocalAutoRawCapture(t *testing.T) {
	_, ts := newMemoryServerWithBilling(t, BillingStatus{
		Mode:              "local-auto",
		Host:              "claude-code",
		BackendLLMEnabled: false,
		RawCaptureEnabled: false,
	})
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodPost, "/continuity/observe", map[string]any{
		"thread_id":        "pulse-distribution",
		"session_id":       "claude-code:pulse-distribution:test",
		"host":             "claude-code",
		"event_type":       "UserPromptSubmit",
		"redacted_summary": "User prompt captured by Pulse local-auto hook.",
		"raw_ref":          "raw:prompt:1",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected raw_ref rejection with raw capture disabled, got %d", resp.StatusCode)
	}
}

func TestContinuityWriteEndpointsRequireLocalAutoMode(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	for _, tc := range []struct {
		path string
		body map[string]any
	}{
		{
			path: "/continuity/checkpoint",
			body: map[string]any{
				"thread_id":  "pulse-distribution",
				"session_id": "claude-code:pulse-distribution:test",
				"host":       "claude-code",
				"summary":    "A safe checkpoint summary.",
				"confidence": 0.8,
			},
		},
		{
			path: "/continuity/observe",
			body: map[string]any{
				"thread_id":        "pulse-distribution",
				"session_id":       "claude-code:pulse-distribution:test",
				"host":             "claude-code",
				"event_type":       "UserPromptSubmit",
				"redacted_summary": "A safe redacted observation.",
			},
		},
	} {
		resp := pulseJSON(t, ts, http.MethodPost, tc.path, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s expected 403 in host-extracted mode, got %d", tc.path, resp.StatusCode)
		}
	}
}

func TestMemoryWipeRequiresServerSideConfirm(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodPost, "/memory/wipe", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected wipe without confirm to fail, got %d", resp.StatusCode)
	}

	resp = pulseJSON(t, ts, http.MethodPost, "/memory/wipe", map[string]any{
		"confirm": "wipe pulse memory",
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected confirmed wipe to pass, got %d", resp.StatusCode)
	}
}

func TestProductMemoryWipeRejectsCallerConfirmationWithoutOSPresence(t *testing.T) {
	vault, ts := newProductMemoryServer(t)
	defer ts.Close()

	remember := pulseJSON(t, ts, http.MethodPost, "/memory/remember", map[string]any{
		"schema": store.MemoryCapsuleSchema,
		"source": map[string]any{
			"host": "codex", "conversation_scope": "current_turn", "timestamp": "2026-07-14T09:00:00Z",
		},
		"items": []map[string]any{{
			"kind": "decision", "redacted_summary": "Wipe guard fixture",
			"confidence": 0.95, "evidence_hint": "current_turn", "privacy_tier": "normal", "retention": "project",
		}},
		"raw_input_included": false,
	})
	if remember.StatusCode != http.StatusOK {
		t.Fatalf("seed protected product memory status=%d", remember.StatusCode)
	}
	var prepared store.TurnFinalizeResult
	if err := json.NewDecoder(remember.Body).Decode(&prepared); err != nil {
		t.Fatalf("decode protected product memory: %v", err)
	}
	if len(prepared.Receipts) != 1 {
		t.Fatalf("expected one protected product memory candidate: %#v", prepared)
	}
	presentedAt := time.Now().UTC()
	presentProductCandidate(t, vault, prepared.Receipts[0], presentedAt, 10*time.Second)
	if _, err := vault.CommitMemoryTrayCandidate(prepared.Receipts[0].CandidateID, 1, presentedAt.Add(10*time.Second)); err != nil {
		t.Fatalf("commit protected product memory: %v", err)
	}

	resp := pulseJSON(t, ts, http.MethodPost, "/memory/wipe", map[string]any{
		"confirm": "wipe pulse memory",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected caller-confirmed product wipe to fail closed, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read product wipe denial: %v", err)
	}
	if !strings.Contains(string(body), "OS-backed user presence") {
		t.Fatalf("product wipe denial must name the missing trust boundary: %q", body)
	}

	status, err := vault.MemoryStatus()
	if err != nil {
		t.Fatalf("read protected product memory status: %v", err)
	}
	if status.ItemCount != 1 {
		t.Fatalf("product memory changed despite denied wipe: %#v", status)
	}

	viewer := pulseJSON(t, ts, http.MethodGet, "/viewer?key=secret&first_run=1", nil)
	viewerBody, err := io.ReadAll(viewer.Body)
	if err != nil {
		t.Fatalf("read product viewer: %v", err)
	}
	if !strings.Contains(string(viewerBody), "OS confirmation required") ||
		strings.Contains(string(viewerBody), `id="wipe-confirm"`) ||
		strings.Contains(string(viewerBody), `id="delete-id"`) ||
		!strings.Contains(string(viewerBody), `data-first-memory-action="forget" disabled`) {
		t.Fatalf("product viewer exposed a caller-authorized destructive control")
	}
}

func TestViewerPageIncludesTrustControls(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodGet, "/viewer?key=secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("viewer should set Referrer-Policy no-referrer, got %q", got)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read viewer: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		"Pulse Home",
		"data-design=\"pulse-first-run-home-v0\"",
		"Pulse keeps the thread",
		"data-flow=\"pulse-onboarding-v2\"",
		"data-motion=\"pulse-ecg\"",
		"Pulse is breathing locally.",
		"Onboarding path",
		"Now",
		"Later",
		"Bound vault",
		"harness readiness: see doctor",
		"Proof",
		"Import",
		"Preview",
		"Review",
		"Resume",
		"Pulse is alive",
		"What Pulse will provide next",
		"You can inspect this before every new session.",
		"Pulse will provide:",
		"View raw resume block",
		"Choose next step",
		"Start with one memory first. Old chats can wait.",
		"Start first memory proof",
		"data-primary-action=\"first-memory-proof\"",
		"data-secondary-action=\"import-later\"",
		"Try first memory",
		"Import old chats later",
		"Open import wizard",
		"First memory proof",
		"Proof checklist",
		"Remember one decision",
		"Open a fresh session",
		"Confirm recall",
		"<details class=\"surface import-wizard\"",
		"Import wizard",
		"What should Pulse learn from?",
		"Claude Code sessions",
		"Codex sessions",
		"ChatGPT export",
		"Claude export",
		"Manual capsule",
		"Example threads after preview",
		"Pulse onboarding",
		"Garden demo UX",
		"Needs your decision",
		"Review actions update this page only.",
		"data-review-status",
		"data-review-action=\"confirm\"",
		"data-review-action=\"private\"",
		"Confirm",
		"Edit",
		"Ignore",
		"Mark private",
		"Merge",
		"Import gate preview",
		"Will save",
		"Will not save",
		"Default privacy: private",
		"No resume block yet",
		"viewerLoadInFlight",
		"window.setInterval",
		"if (!document.hidden) loadViewerData()",
		"Activity",
		"Token economy",
		"No Pulse activity yet.",
		"Remember this in Pulse: Atlas must not own the People Graph; Pulse owns portable continuity memory.",
		"Recent Pulse actions",
		"No tool calls yet.",
		"After your first memory proof",
		"Thread map and source details",
		"Search memory",
		"Delete memory",
		"Wipe memory",
		"/memory/recall",
		"/memory/delete",
		"Correct committed memory",
		"Edit the exact structured semantic candidate",
		"/correct",
		"/memory/wipe",
		"raw refs disabled",
		"backend LLM off",
		"No backend model is running by default.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("viewer missing %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{
		"People Pulse is confident about",
		"Graph counts after review",
		"Fun facts",
		"person profiles",
		"What Claude will get next",
		"<h3>Start working</h3>",
		"Recent MCP activity and token economy are visible here",
		"undefined / undefined",
		"--paper: #f7f2e8",
		"Charter, Georgia",
		"background-size: 36px 36px",
		"radial-gradient",
	} {
		if strings.Contains(body, bad) {
			t.Fatalf("viewer should not include old/buggy marker %q:\n%s", bad, body)
		}
	}
}

func TestViewerFirstRunDelightKeepsEmotionOptionalAndEditable(t *testing.T) {
	_, ts := newMemoryServerWithBilling(t, BillingStatus{
		Mode:              "local-auto",
		Host:              "claude-code",
		BackendLLMEnabled: false,
		RawCaptureEnabled: false,
	})
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodGet, "/viewer?key=secret&thread_id=cli&first_run=1&host=claude-code", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer status=%d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read viewer: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		"data-first-run=\"true\"",
		"Welcome to Pulse.",
		"Pulse helps remember what actually mattered.",
		"data-first-memory-title",
		"data-first-memory-id",
		"Your first memory starts here",
		"No saved structured memory is loaded for Claude Code yet.",
		"How did that feel?",
		"Pulse guess: maybe curiosity?",
		"No emotion is stored until you choose one. Silence is not consent.",
		"Change this",
		"Curious",
		"Relieved",
		"Skeptical",
		"Annoyed",
		"Something else",
		"Skip",
		"Pulse works quietly even if you never touch this screen.",
		"Feedback makes Pulse more personal",
		"Memory",
		"Why it matters",
		"Edit preview only",
		"Mark important preview only",
		"Change emotion feedback",
		"Forget",
		"Edit preview only and Mark important preview only do not persist yet.",
		"Change emotion saves only after you choose a feeling.",
		"Forget deletes only when a saved memory ID is loaded.",
		"Nothing is pending until a harness submits a visible Memory Tray candidate.",
		"Available sources",
		"Source scanner",
		"Preview sources after proof",
		"Claude Code sessions",
		"Codex sessions",
		"Gemini CLI",
		"ChatGPT export",
		"Claude app export",
		"Manual capsule",
		"Run doctor for actual MCP and hook readiness",
		"Connector: available",
		"Source: choose an archive first",
		"Source: not scanned yet",
		"Sessions: not scanned yet",
		"Sessions: not applicable",
		"Preview first. Import later.",
		"Context continuity",
		"Token savings appear after your first resume.",
		"Possible state signals",
		"needs confirmation",
		"private by default",
		"no diagnosis",
		"confirm",
		"change",
		"ignore",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("first-run viewer missing %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{
		"Your first memory is already here",
		"Detected: local hooks ready",
		"Detected: local conversations can be previewed",
		"Source detected:",
		"Sessions found:",
		"Status: ready",
		"Ready to import structured memory",
		"<button type=\"button\" data-first-memory-action=\"edit\">Edit</button>",
		"<button type=\"button\" data-first-memory-action=\"important\">Mark important</button>",
		"Edit, Mark important, and Change emotion update this local preview in this build.",
		"<h3>overload</h3>",
		"source: recent thread · confidence: medium",
		"Pulse knows you felt",
		"Pulse knows your mood",
		"saved 11,560 tokens",
		"Import old chats</h3>",
	} {
		if strings.Contains(body, bad) {
			t.Fatalf("first-run viewer should not include unsafe/old marker %q:\n%s", bad, body)
		}
	}
}

func TestViewerDataIncludesSavedFirstMemoryID(t *testing.T) {
	_, ts := newMemoryServerWithBilling(t, BillingStatus{
		Mode:              "local-auto",
		Host:              "claude-code",
		BackendLLMEnabled: false,
		RawCaptureEnabled: false,
	})
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodPost, "/memory/remember", map[string]any{
		"schema": "pulse.memory_capsule.v1",
		"source": map[string]any{
			"host":               "pulse-cli",
			"conversation_scope": "install_event",
			"timestamp":          "2026-06-06T12:00:00Z",
		},
		"items": []map[string]any{{
			"kind":             "system_event",
			"redacted_summary": "User installed Pulse MCP and connected it to Claude Code.",
			"confidence":       1,
			"evidence_hint":    "user_confirmed",
			"privacy_tier":     "private",
			"retention":        "project",
			"tags":             []string{"pulse_install", "first_memory", "claude_code"},
		}},
		"raw_input_included": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remember status=%d", resp.StatusCode)
	}

	resp = pulseJSON(t, ts, http.MethodGet, "/viewer/data?thread_id=cli&key=secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer data status=%d", resp.StatusCode)
	}
	var viewer struct {
		FirstMemory struct {
			Status  string `json:"status"`
			ID      string `json:"id"`
			Summary string `json:"summary"`
		} `json:"first_memory"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&viewer); err != nil {
		t.Fatalf("decode viewer: %v", err)
	}
	if viewer.FirstMemory.Status != "saved" {
		t.Fatalf("first memory should be saved, got %#v", viewer.FirstMemory)
	}
	if !strings.HasPrefix(viewer.FirstMemory.ID, "pulse:") {
		t.Fatalf("first memory should expose real pulse id, got %#v", viewer.FirstMemory)
	}
	if !strings.Contains(viewer.FirstMemory.Summary, "Claude Code") {
		t.Fatalf("first memory should expose saved summary, got %#v", viewer.FirstMemory)
	}
}

func TestViewerServesVendoredAnimeAssetWithoutMemoryKey(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/assets/anime.min.js")
	if err != nil {
		t.Fatalf("get asset: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("asset status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("asset content type=%q", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("asset should set nosniff, got %q", got)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		"Anime.js - UMD minified bundle",
		"@version v4.4.1",
		"license MIT",
		").anime={}",
		"t.animate=",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("anime asset missing %q", want)
		}
	}
}

func TestViewerShowsImportedGraphProfiles(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodPost, "/graph/delta", map[string]any{
		"schema": "pulse.semantic_delta.v1",
		"source": map[string]any{
			"host":               "chatgpt",
			"conversation_scope": "project_context",
			"timestamp":          "2026-06-03T08:00:00Z",
			"thread_id":          "archive-import",
			"session_id":         "archive-import:test",
			"project_id":         "pulse-migration",
		},
		"nodes": []map[string]any{
			{
				"client_id":        "person:vitaly",
				"kind":             "person",
				"canonical_name":   "Vitaly",
				"summary":          "Person candidate from Pulse archive import preview.",
				"salience":         0.7,
				"emotional_weight": 0.4,
				"privacy_tier":     "normal",
				"domain":           "real",
			},
			{
				"client_id":      "project:pulse-viewer",
				"kind":           "project",
				"canonical_name": "Pulse Viewer",
				"summary":        "Beautiful profile viewer for people, memories, relationships, and fun facts.",
				"privacy_tier":   "normal",
				"domain":         "real",
			},
		},
		"edges": []map[string]any{
			{
				"from":         "person:vitaly",
				"to":           "project:pulse-viewer",
				"kind":         "reviewing",
				"summary":      "Vitaly is connected to the Pulse viewer import flow.",
				"strength":     0.6,
				"privacy_tier": "normal",
			},
		},
		"facts": []map[string]any{
			{
				"node":         "person:vitaly",
				"text":         "Vitaly appeared in 1 safe preview signal",
				"confidence":   0.6,
				"privacy_tier": "normal",
				"domain":       "real",
			},
		},
		"events": []map[string]any{
			{
				"client_id":        "event:archive-import-preview",
				"title":            "Pulse archive import preview committed",
				"summary":          "Committed safe graph profile candidates from an archive preview.",
				"entity_refs":      []string{"person:vitaly", "project:pulse-viewer"},
				"emotional_weight": 0.45,
				"confidence":       0.6,
				"privacy_tier":     "normal",
				"domain":           "real",
			},
		},
		"raw_input_included": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graph delta status=%d", resp.StatusCode)
	}

	resp = pulseJSON(t, ts, http.MethodGet, "/viewer/data?thread_id=archive-import&key=secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer data status=%d", resp.StatusCode)
	}
	var data struct {
		GraphProfile struct {
			People         []string `json:"people"`
			Memories       []string `json:"memories"`
			Relationships  []string `json:"relationships"`
			FunFacts       []string `json:"fun_facts"`
			PersonProfiles []struct {
				Name            string   `json:"name"`
				Summary         string   `json:"summary"`
				EmotionalWeight float64  `json:"emotional_weight"`
				Facts           []string `json:"facts"`
				Relationships   []string `json:"relationships"`
				Memories        []string `json:"memories"`
			} `json:"person_profiles"`
		} `json:"graph_profile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("decode viewer data: %v", err)
	}
	for _, want := range []struct {
		name  string
		items []string
		match string
	}{
		{"people", data.GraphProfile.People, "Vitaly"},
		{"memories", data.GraphProfile.Memories, "Pulse archive import preview committed"},
		{"relationships", data.GraphProfile.Relationships, "Vitaly reviewing Pulse Viewer"},
		{"fun facts", data.GraphProfile.FunFacts, "Vitaly appeared in 1 safe preview signal"},
	} {
		if !containsString(want.items, want.match) {
			t.Fatalf("viewer graph profile %s missing %q: %#v", want.name, want.match, want.items)
		}
	}
	if len(data.GraphProfile.PersonProfiles) != 1 {
		t.Fatalf("viewer should expose one person profile, got %#v", data.GraphProfile.PersonProfiles)
	}
	profile := data.GraphProfile.PersonProfiles[0]
	if profile.Name != "Vitaly" {
		t.Fatalf("viewer person profile name mismatch: %#v", profile)
	}
	if !strings.Contains(profile.Summary, "Person candidate") {
		t.Fatalf("viewer person profile should include summary: %#v", profile)
	}
	if profile.EmotionalWeight <= 0 {
		t.Fatalf("viewer person profile should include emotional weight: %#v", profile)
	}
	if !containsString(profile.Facts, "Vitaly appeared in 1 safe preview signal") {
		t.Fatalf("viewer person profile facts missing imported fact: %#v", profile)
	}
	if !containsString(profile.Relationships, "Vitaly reviewing Pulse Viewer") {
		t.Fatalf("viewer person profile relationships missing imported relation: %#v", profile)
	}
	if !containsString(profile.Memories, "Pulse archive import preview committed") {
		t.Fatalf("viewer person profile memories missing imported event: %#v", profile)
	}

	resp = pulseJSON(t, ts, http.MethodGet, "/viewer?key=secret&thread_id=archive-import", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer page status=%d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read viewer page: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		"Pulse keeps the thread",
		"What Pulse will provide next",
		"Thread map and source details",
		"People found in reviewed sources",
		"Saved memory",
		"Relationships",
		"Low-stakes notes",
		"graph-count-people",
		"graph-count-memories",
		"graph-count-emotions",
		"graph-count-relationships",
		"graph-count-notes",
		"graph-count-profiles",
		"graph-filter",
		"graph-filter-status",
		"graph-profile-cards",
		"Hide profile",
		"/graph/entity/hide",
		"hide graph entity",
		"Hidden profiles",
		"hidden-profiles",
		"/graph/entity/restore",
		"restore graph entity",
		"Import old chats",
		"connect-chatgpt",
		"connect-claude",
		"connect-codex",
		"connect-claude-code",
		"pulse migrate request chatgpt --open",
		"pulse migrate request claude --open",
		"pulse migrate request codex --open",
		"pulse migrate request claude-code --open",
		`pulse migrate commit pulse-preview.json --confirm "import pulse graph" --open`,
		"data-copy",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("viewer page missing graph profile marker %q:\n%s", want, body)
		}
	}
}

func TestViewerCanHideNoisyGraphEntity(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodPost, "/graph/delta", map[string]any{
		"schema": "pulse.semantic_delta.v1",
		"source": map[string]any{
			"host":               "chatgpt",
			"conversation_scope": "project_context",
			"timestamp":          "2026-06-03T08:00:00Z",
			"thread_id":          "archive-import",
			"session_id":         "archive-import:test",
			"project_id":         "pulse-migration",
		},
		"nodes": []map[string]any{
			{
				"client_id":      "person:noisy",
				"kind":           "person",
				"canonical_name": "NoisyName",
				"summary":        "Noisy person candidate from archive preview.",
				"privacy_tier":   "normal",
				"domain":         "real",
			},
			{
				"client_id":      "project:pulse-viewer",
				"kind":           "project",
				"canonical_name": "Pulse Viewer",
				"summary":        "Viewer target.",
				"privacy_tier":   "normal",
				"domain":         "real",
			},
		},
		"edges": []map[string]any{
			{
				"from":         "person:noisy",
				"to":           "project:pulse-viewer",
				"kind":         "mentioned_in",
				"summary":      "NoisyName was linked by the import preview.",
				"strength":     0.4,
				"privacy_tier": "normal",
			},
		},
		"facts": []map[string]any{
			{
				"node":         "person:noisy",
				"text":         "NoisyName appeared in 4 safe preview signals",
				"confidence":   0.6,
				"privacy_tier": "normal",
				"domain":       "real",
			},
		},
		"raw_input_included": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graph delta status=%d", resp.StatusCode)
	}

	resp = pulseJSON(t, ts, http.MethodGet, "/viewer/data?thread_id=archive-import&key=secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer data status=%d", resp.StatusCode)
	}
	var before struct {
		GraphProfile struct {
			People         []string `json:"people"`
			Relationships  []string `json:"relationships"`
			FunFacts       []string `json:"fun_facts"`
			PersonProfiles []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"person_profiles"`
		} `json:"graph_profile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&before); err != nil {
		t.Fatalf("decode viewer before: %v", err)
	}
	if len(before.GraphProfile.PersonProfiles) != 1 || before.GraphProfile.PersonProfiles[0].ID <= 0 {
		t.Fatalf("viewer should expose local profile id before hide: %#v", before.GraphProfile.PersonProfiles)
	}
	entityID := before.GraphProfile.PersonProfiles[0].ID

	resp = pulseJSON(t, ts, http.MethodPost, "/graph/entity/hide", map[string]any{
		"id":      entityID,
		"confirm": "hide graph entity",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hide entity status=%d", resp.StatusCode)
	}

	resp = pulseJSON(t, ts, http.MethodGet, "/viewer/data?thread_id=archive-import&key=secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer data after status=%d", resp.StatusCode)
	}
	var after struct {
		HiddenEntities []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"hidden_entities"`
		GraphProfile struct {
			People         []string `json:"people"`
			Relationships  []string `json:"relationships"`
			FunFacts       []string `json:"fun_facts"`
			PersonProfiles []struct {
				Name string `json:"name"`
			} `json:"person_profiles"`
		} `json:"graph_profile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&after); err != nil {
		t.Fatalf("decode viewer after: %v", err)
	}
	for _, items := range [][]string{
		after.GraphProfile.People,
		after.GraphProfile.Relationships,
		after.GraphProfile.FunFacts,
	} {
		if containsString(items, "NoisyName") {
			t.Fatalf("hidden entity should be removed from viewer graph: %#v", after.GraphProfile)
		}
	}
	if len(after.GraphProfile.PersonProfiles) != 0 {
		t.Fatalf("hidden entity should be removed from person profiles: %#v", after.GraphProfile.PersonProfiles)
	}
	if len(after.HiddenEntities) != 1 || after.HiddenEntities[0].ID != entityID || after.HiddenEntities[0].Name != "NoisyName" {
		t.Fatalf("viewer should expose hidden profile for restore: %#v", after.HiddenEntities)
	}

	resp = pulseJSON(t, ts, http.MethodPost, "/graph/entity/restore", map[string]any{
		"id":      entityID,
		"confirm": "restore graph entity",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore entity status=%d", resp.StatusCode)
	}

	resp = pulseJSON(t, ts, http.MethodGet, "/viewer/data?thread_id=archive-import&key=secret", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer data restored status=%d", resp.StatusCode)
	}
	var restored struct {
		HiddenEntities []struct {
			Name string `json:"name"`
		} `json:"hidden_entities"`
		GraphProfile struct {
			People         []string `json:"people"`
			Relationships  []string `json:"relationships"`
			PersonProfiles []struct {
				Name string `json:"name"`
			} `json:"person_profiles"`
		} `json:"graph_profile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&restored); err != nil {
		t.Fatalf("decode viewer restored: %v", err)
	}
	if len(restored.HiddenEntities) != 0 {
		t.Fatalf("restored entity should leave hidden list: %#v", restored.HiddenEntities)
	}
	if !containsString(restored.GraphProfile.People, "NoisyName") ||
		!containsString(restored.GraphProfile.Relationships, "NoisyName") ||
		len(restored.GraphProfile.PersonProfiles) != 1 {
		t.Fatalf("restored entity should return to viewer graph: %#v", restored.GraphProfile)
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
