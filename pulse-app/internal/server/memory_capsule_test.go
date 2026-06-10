package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

func newMemoryServer(t *testing.T) (*store.Store, *httptest.Server) {
	return newMemoryServerWithBilling(t, BillingStatus{
		Mode:              "host-extracted",
		Host:              "claude-code",
		BackendLLMEnabled: false,
		RawCaptureEnabled: false,
	})
}

func newMemoryServerWithBilling(t *testing.T, billing BillingStatus) (*store.Store, *httptest.Server) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	srv, err := New(Config{
		IPCSecret: "secret",
		Store:     s,
		Billing:   billing,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, httptest.NewServer(srv.Handler())
}

func pulseJSON(t *testing.T, ts *httptest.Server, method, path string, body any) *http.Response {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("X-Pulse-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func TestMemoryRememberRecallStatusEndpoints(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	capsule := map[string]any{
		"schema": "pulse.memory_capsule.v1",
		"source": map[string]any{
			"host":               "claude-code",
			"conversation_scope": "current_turn",
			"timestamp":          "2026-06-02T09:00:00Z",
		},
		"items": []map[string]any{{
			"kind":             "decision",
			"redacted_summary": "We chose Pulse MCP distribution with Claude Code as the first install target.",
			"confidence":       0.91,
			"evidence_hint":    "current_turn",
			"privacy_tier":     "normal",
			"retention":        "project",
			"tags":             []string{"claude-code"},
		}},
		"raw_input_included": false,
	}

	resp := pulseJSON(t, ts, http.MethodPost, "/memory/remember", capsule)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remember status=%d", resp.StatusCode)
	}

	resp = pulseJSON(t, ts, http.MethodPost, "/memory/recall", map[string]any{
		"query":           "distribution surface",
		"scope":           "project",
		"limit":           5,
		"privacy_ceiling": "normal",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("recall status=%d", resp.StatusCode)
	}
	var recall struct {
		Items []struct {
			Summary string `json:"summary"`
			Source  string `json:"source"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&recall); err != nil {
		t.Fatalf("decode recall: %v", err)
	}
	if len(recall.Items) != 1 || !strings.Contains(recall.Items[0].Summary, "Claude Code") {
		t.Fatalf("bad recall: %#v", recall)
	}

	resp = pulseJSON(t, ts, http.MethodGet, "/memory/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status status=%d", resp.StatusCode)
	}
	var status struct {
		BillingMode       string `json:"billing_mode"`
		Host              string `json:"host"`
		BackendLLMEnabled bool   `json:"backend_llm_enabled"`
		RawCaptureEnabled bool   `json:"raw_capture_enabled"`
		Schema            string `json:"schema"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.BillingMode != "host-extracted" || status.Host != "claude-code" {
		t.Fatalf("bad status: %#v", status)
	}
	if status.BackendLLMEnabled || status.RawCaptureEnabled {
		t.Fatalf("status should prove no backend/raw capture: %#v", status)
	}
	if status.Schema != "pulse.memory_capsule.v1" {
		t.Fatalf("bad schema: %q", status.Schema)
	}
}

func TestMemoryRememberAcceptsPrivateInstallAndConfirmedStateSignalCapsules(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	for _, tc := range []struct {
		name  string
		scope string
		kind  string
		text  string
		tags  []string
	}{
		{
			name:  "install event",
			scope: "install_event",
			kind:  "system_event",
			text:  "User installed Pulse MCP and connected it to Claude Code.",
			tags:  []string{"pulse_install", "first_memory", "claude_code"},
		},
		{
			name:  "confirmed state signal",
			scope: "current_turn",
			kind:  "state_signal",
			text:  "User marked first Pulse install feeling as curious.",
			tags:  []string{"user_feedback", "onboarding", "emotion_curiosity_confirmed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := pulseJSON(t, ts, http.MethodPost, "/memory/remember", map[string]any{
				"schema": "pulse.memory_capsule.v1",
				"source": map[string]any{
					"host":               "pulse-cli",
					"conversation_scope": tc.scope,
					"timestamp":          "2026-06-06T10:00:00Z",
				},
				"items": []map[string]any{{
					"kind":             tc.kind,
					"redacted_summary": tc.text,
					"confidence":       1.0,
					"evidence_hint":    "user_confirmed",
					"privacy_tier":     "private",
					"retention":        "project",
					"tags":             tc.tags,
				}},
				"raw_input_included": false,
			})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("remember status=%d", resp.StatusCode)
			}
		})
	}
}

func TestMemoryRememberRejectsRawTranscript(t *testing.T) {
	_, ts := newMemoryServer(t)
	defer ts.Close()

	resp := pulseJSON(t, ts, http.MethodPost, "/memory/remember", map[string]any{
		"schema": "pulse.memory_capsule.v1",
		"source": map[string]any{
			"host":               "claude-code",
			"conversation_scope": "current_turn",
			"timestamp":          "2026-06-02T09:00:00Z",
		},
		"items": []map[string]any{{
			"kind":             "decision",
			"redacted_summary": strings.Repeat("User: save all chat\nAssistant: ok\n", 80),
			"confidence":       0.9,
			"evidence_hint":    "current_turn",
			"privacy_tier":     "normal",
			"retention":        "project",
		}},
		"raw_input_included": false,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for transcript-like payload, got %d", resp.StatusCode)
	}
}
