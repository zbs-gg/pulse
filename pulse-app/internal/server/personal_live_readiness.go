package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

const (
	personalLiveReadinessSchema      = "pulse.personal_live_readiness.v1"
	supportedHostLiveReadinessSchema = "pulse.supported_host_live_readiness.v1"
)

type personalLiveReadinessSnapshot struct {
	Schema     string                     `json:"schema"`
	TargetHost string                     `json:"target_host,omitempty"`
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

var supportedHostLiveReadinessContracts = map[string]personalLiveReadinessContract{
	"supported_host_live_ready": {
		Outcome: store.MemoryHomeReadinessReady,
		Action:  store.MemoryHomeNextAction{Code: "continue_working", Label: "Continue working"},
	},
	"presence_required":       personalLiveReadinessContracts["presence_required"],
	"binding_repair_required": personalLiveReadinessContracts["binding_repair_required"],
	"host_adapter_unavailable": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "repair_host_adapter", Label: "Run pulse repair"},
	},
	"host_activation_incomplete": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "repair_host_activation", Label: "Run pulse repair"},
	},
	"host_lifecycle_required": {
		Outcome: store.MemoryHomeReadinessActionRequired,
		Action:  store.MemoryHomeNextAction{Code: "complete_host_lifecycle", Label: "Complete one normal agent turn"},
	},
	"daemon_unavailable":         personalLiveReadinessContracts["daemon_unavailable"],
	"full_retrieval_unavailable": personalLiveReadinessContracts["full_retrieval_unavailable"],
	"local_embedder_warming":     personalLiveReadinessContracts["local_embedder_warming"],
}

func supportedHostID(value string) bool {
	return value == "claude-code" || value == "codex" || value == "cursor" || value == "opencode"
}

func validatePersonalLiveReadiness(snapshot personalLiveReadinessSnapshot) error {
	contracts := personalLiveReadinessContracts
	if snapshot.Schema == supportedHostLiveReadinessSchema {
		if !supportedHostID(snapshot.TargetHost) {
			return errors.New("invalid Personal live readiness target_host")
		}
		contracts = supportedHostLiveReadinessContracts
	} else if snapshot.Schema != personalLiveReadinessSchema || snapshot.TargetHost != "" {
		return errors.New("invalid Personal live readiness schema")
	}
	contract, ok := contracts[snapshot.ReasonCode]
	if !ok || snapshot.Outcome != contract.Outcome ||
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
	fields := []string{
		"pulse-personal-live-readiness-v1", snapshot.Schema, snapshot.Outcome, snapshot.ReasonCode,
		snapshot.NextAction.Code, snapshot.NextAction.Label, snapshot.CheckedAt,
	}
	if snapshot.Schema == supportedHostLiveReadinessSchema {
		fields = []string{
			"pulse-supported-host-live-readiness-v1", snapshot.Schema, snapshot.TargetHost,
			snapshot.Outcome, snapshot.ReasonCode, snapshot.NextAction.Code, snapshot.NextAction.Label,
			snapshot.CheckedAt,
		}
	}
	digest := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(digest[:]), nil
}

func personalLiveReadinessForReason(reasonCode, checkedAt string) personalLiveReadinessSnapshot {
	contract := personalLiveReadinessContracts[reasonCode]
	return personalLiveReadinessSnapshot{
		Schema: personalLiveReadinessSchema, Outcome: contract.Outcome, ReasonCode: reasonCode,
		NextAction: contract.Action, CheckedAt: checkedAt,
	}
}
