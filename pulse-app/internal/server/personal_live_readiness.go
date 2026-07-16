package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

const personalLiveReadinessSchema = "pulse.personal_live_readiness.v1"

type personalLiveReadinessSnapshot struct {
	Schema     string                     `json:"schema"`
	Outcome    string                     `json:"outcome"`
	ReasonCode string                     `json:"reason_code"`
	NextAction store.MemoryHomeNextAction `json:"next_action"`
	CheckedAt  string                     `json:"checked_at"`
}

type personalLiveReadinessContract struct {
	Outcome string
	Action  store.MemoryHomeNextAction
}

var personalLiveReadinessContracts = map[string]personalLiveReadinessContract{
	"personal_live_ready": {
		Outcome: store.MemoryHomeReadinessReady,
		Action:  store.MemoryHomeNextAction{Code: "continue_working", Label: "Continue working"},
	},
	"presence_required": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "install_presence_trust", Label: "Install Pulse presence helper"},
	},
	"binding_repair_required": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "repair_binding", Label: "Run pulse repair"},
	},
	"codex_activation_incomplete": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "repair_codex_activation", Label: "Run pulse repair"},
	},
	"codex_plugin_unavailable": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "repair_codex_plugin", Label: "Run pulse repair"},
	},
	"codex_hook_trust_required": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "trust_codex_hooks", Label: "Trust the Pulse hook bundle"},
	},
	"codex_hook_lifecycle_required": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "complete_codex_lifecycle", Label: "Complete one normal Codex turn"},
	},
	"codex_native_lifecycle_attestation_unavailable": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "use_pulse_mcp", Label: "Use Pulse MCP tools explicitly"},
	},
	"daemon_unavailable": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "repair_daemon", Label: "Run pulse repair"},
	},
	"full_retrieval_unavailable": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "repair_retrieval", Label: "Run pulse repair"},
	},
	"local_embedder_warming": {
		Outcome: store.MemoryHomeReadinessWarming,
		Action:  store.MemoryHomeNextAction{Code: "wait_for_embedder", Label: "Keep Pulse open while the local model warms"},
	},
}

func validatePersonalLiveReadiness(snapshot personalLiveReadinessSnapshot) error {
	contract, ok := personalLiveReadinessContracts[snapshot.ReasonCode]
	if !ok || snapshot.Schema != personalLiveReadinessSchema || snapshot.Outcome != contract.Outcome ||
		snapshot.NextAction != contract.Action || snapshot.CheckedAt == "" {
		return errors.New("invalid Personal live readiness snapshot")
	}
	checkedAt, err := time.Parse(time.RFC3339Nano, snapshot.CheckedAt)
	if err != nil || checkedAt.IsZero() || checkedAt.Location() != time.UTC ||
		checkedAt.UTC().Format(time.RFC3339Nano) != snapshot.CheckedAt {
		return errors.New("invalid Personal live readiness checked_at")
	}
	return nil
}

func personalLiveReadinessDigest(snapshot personalLiveReadinessSnapshot) (string, error) {
	if err := validatePersonalLiveReadiness(snapshot); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"pulse-personal-live-readiness-v1", snapshot.Schema, snapshot.Outcome, snapshot.ReasonCode,
		snapshot.NextAction.Code, snapshot.NextAction.Label, snapshot.CheckedAt,
	}, "\x00")))
	return hex.EncodeToString(digest[:]), nil
}

func personalLiveReadinessForReason(reasonCode, checkedAt string) personalLiveReadinessSnapshot {
	contract := personalLiveReadinessContracts[reasonCode]
	return personalLiveReadinessSnapshot{
		Schema: personalLiveReadinessSchema, Outcome: contract.Outcome, ReasonCode: reasonCode,
		NextAction: contract.Action, CheckedAt: checkedAt,
	}
}
