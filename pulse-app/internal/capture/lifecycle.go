package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	LifecycleSchema    = "pulse.lifecycle_event.v1"
	WriteReceiptSchema = "pulse.write_receipt.v1"
	InjectionSchema    = "pulse.context.v1"
	BindingSchema      = "pulse.binding.v1"
	ContextLeaseSchema = "pulse.context_lease.v1"
)

type Host string

const (
	HostCodex      Host = "codex"
	HostClaudeCode Host = "claude-code"
)

type LifecycleEventKind string

const (
	EventSessionStart  LifecycleEventKind = "session_start"
	EventTurnStart     LifecycleEventKind = "turn_start"
	EventToolReceipt   LifecycleEventKind = "tool_receipt"
	EventPreCompact    LifecycleEventKind = "pre_compact"
	EventSubagentStart LifecycleEventKind = "subagent_start"
	EventSubagentStop  LifecycleEventKind = "subagent_stop"
	EventTurnFinalize  LifecycleEventKind = "turn_finalize"
	EventSessionResume LifecycleEventKind = "session_resume"
)

type LifecycleEvent struct {
	Schema         string             `json:"schema"`
	Host           Host               `json:"host"`
	Event          LifecycleEventKind `json:"event"`
	SessionID      string             `json:"session_id"`
	TurnID         string             `json:"turn_id"`
	Workspace      string             `json:"workspace"`
	Model          string             `json:"model"`
	Source         string             `json:"source"`
	StopHookActive bool               `json:"stop_hook_active"`
}

var stableLifecycleID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$`)

var authorityFields = map[string]struct{}{
	"audience": {}, "principal": {}, "role": {}, "scope": {}, "team_id": {},
	"vault": {}, "visibility": {}, "workspace": {},
}

var supportedLifecycleEvents = map[LifecycleEventKind]struct{}{
	EventSessionStart: {}, EventTurnStart: {}, EventToolReceipt: {}, EventPreCompact: {},
	EventSubagentStart: {}, EventSubagentStop: {}, EventTurnFinalize: {}, EventSessionResume: {},
}

func NormalizeLifecycleEvent(host Host, event LifecycleEventKind, input map[string]any) (LifecycleEvent, error) {
	if host != HostCodex && host != HostClaudeCode {
		return LifecycleEvent{}, errors.New("unsupported_host")
	}
	if _, ok := supportedLifecycleEvents[event]; !ok {
		return LifecycleEvent{}, errors.New("unsupported_event")
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, strings.ToLower(strings.TrimSpace(key)))
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, forbidden := authorityFields[key]; forbidden {
			return LifecycleEvent{}, fmt.Errorf("authority_field_forbidden:%s", key)
		}
	}

	sessionID, ok := input["session_id"].(string)
	if !ok || !stableLifecycleID.MatchString(sessionID) {
		return LifecycleEvent{}, errors.New("invalid_session_id")
	}
	turnID, ok := input["turn_id"].(string)
	if !ok || !stableLifecycleID.MatchString(turnID) {
		return LifecycleEvent{}, errors.New("invalid_turn_id")
	}
	cwd, ok := input["cwd"].(string)
	if !ok || cwd == "" || !filepath.IsAbs(cwd) || hasControl(cwd) {
		return LifecycleEvent{}, errors.New("invalid_workspace")
	}
	model, ok := input["model"].(string)
	if !ok || model == "" || hasControl(model) {
		return LifecycleEvent{}, errors.New("invalid_model")
	}
	source, ok := input["source"].(string)
	if !ok || source == "" || hasControl(source) {
		return LifecycleEvent{}, errors.New("invalid_source")
	}
	stopHookActive := false
	if raw, exists := input["stop_hook_active"]; exists {
		var boolOK bool
		stopHookActive, boolOK = raw.(bool)
		if !boolOK {
			return LifecycleEvent{}, errors.New("invalid_stop_hook_active")
		}
	}
	return LifecycleEvent{
		Schema: LifecycleSchema, Host: host, Event: event, SessionID: sessionID,
		TurnID: turnID, Workspace: filepath.Clean(cwd), Model: model, Source: source,
		StopHookActive: stopHookActive,
	}, nil
}

func (e LifecycleEvent) IdempotencyKey() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		e.Schema, string(e.Host), string(e.Event), e.SessionID, e.TurnID, e.Workspace, e.Source,
	}, "\x1f")))
	return "lifecycle:" + hex.EncodeToString(sum[:])
}

type Destination string

const (
	DestinationPersonal Destination = "personal"
	DestinationDesk     Destination = "desk"
)

type ReceiptStatus string

const (
	ReceiptPending      ReceiptStatus = "pending"
	ReceiptCreated      ReceiptStatus = "created"
	ReceiptUpdated      ReceiptStatus = "updated"
	ReceiptDeduplicated ReceiptStatus = "deduplicated"
	ReceiptCanceled     ReceiptStatus = "canceled"
	ReceiptRejected     ReceiptStatus = "rejected"
	ReceiptFailed       ReceiptStatus = "failed"
)

type MeasurementKind string

const (
	MeasurementEstimated      MeasurementKind = "estimated"
	MeasurementProviderActual MeasurementKind = "provider_actual"
)

type TokenMeasurement struct {
	Kind   MeasurementKind `json:"kind"`
	Source string          `json:"source"`
}

type WriteReceipt struct {
	Schema            string            `json:"schema"`
	Status            ReceiptStatus     `json:"status"`
	ReceiptID         string            `json:"receipt_id"`
	Destination       Destination       `json:"destination"`
	ObjectID          string            `json:"object_id,omitempty"`
	ActualInputTokens int               `json:"actual_input_tokens,omitempty"`
	Measurement       *TokenMeasurement `json:"measurement,omitempty"`
}

func ValidateWriteReceipt(receipt WriteReceipt) error {
	if receipt.Schema != WriteReceiptSchema {
		return errors.New("invalid_receipt_schema")
	}
	if !stableLifecycleID.MatchString(receipt.ReceiptID) {
		return errors.New("invalid_receipt_id")
	}
	if receipt.Destination != DestinationPersonal && receipt.Destination != DestinationDesk {
		return errors.New("invalid_destination")
	}
	requiresObject := receipt.Status == ReceiptCreated || receipt.Status == ReceiptUpdated || receipt.Status == ReceiptDeduplicated
	if requiresObject && !stableLifecycleID.MatchString(receipt.ObjectID) {
		return errors.New("object_id_required")
	}
	if !requiresObject && receipt.ObjectID != "" {
		return errors.New("object_id_forbidden")
	}
	switch receipt.Status {
	case ReceiptPending, ReceiptCreated, ReceiptUpdated, ReceiptDeduplicated, ReceiptCanceled, ReceiptRejected, ReceiptFailed:
	default:
		return errors.New("invalid_receipt_status")
	}
	if receipt.ActualInputTokens < 0 {
		return errors.New("invalid_actual_input_tokens")
	}
	if receipt.ActualInputTokens > 0 {
		if receipt.Measurement == nil || receipt.Measurement.Kind != MeasurementProviderActual || strings.TrimSpace(receipt.Measurement.Source) == "" {
			return errors.New("provider_measurement_required")
		}
	}
	return nil
}

type BindingKind string

const (
	BindingPersonal BindingKind = "personal"
	BindingTeam     BindingKind = "team"
)

type BindingDecision struct {
	Schema           string       `json:"schema"`
	Workspace        string       `json:"workspace"`
	Kind             BindingKind  `json:"kind"`
	TeamDeployment   string       `json:"team_deployment,omitempty"`
	ReadVaults       []VaultClass `json:"read_vaults"`
	WriteDestination Destination  `json:"write_destination"`
}

func ValidateBindingDecision(binding BindingDecision) error {
	if binding.Schema != BindingSchema {
		return errors.New("invalid_binding_schema")
	}
	if !filepath.IsAbs(binding.Workspace) || hasControl(binding.Workspace) {
		return errors.New("invalid_binding_workspace")
	}
	switch binding.Kind {
	case BindingPersonal:
		if binding.TeamDeployment != "" || binding.WriteDestination != DestinationPersonal || !sameVaultSet(binding.ReadVaults, []VaultClass{VaultPersonal}) {
			return errors.New("binding_destination_mismatch")
		}
	case BindingTeam:
		if !stableLifecycleID.MatchString(binding.TeamDeployment) || binding.WriteDestination != DestinationDesk || !sameVaultSet(binding.ReadVaults, []VaultClass{VaultDesk, VaultCommons}) {
			return errors.New("binding_destination_mismatch")
		}
	default:
		return errors.New("invalid_binding_kind")
	}
	return nil
}

func sameVaultSet(actual, expected []VaultClass) bool {
	if len(actual) != len(expected) {
		return false
	}
	counts := map[VaultClass]int{}
	for _, value := range actual {
		counts[value]++
	}
	for _, value := range expected {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

type ContextLease struct {
	Schema               string    `json:"schema"`
	BindingDigest        string    `json:"binding_digest"`
	PolicyEpoch          int64     `json:"policy_epoch"`
	MembershipGeneration int64     `json:"membership_generation"`
	ObjectGeneration     int64     `json:"object_generation"`
	ExpiresAt            time.Time `json:"expires_at"`
}

func ValidateContextLease(lease ContextLease, now time.Time, policyEpoch, membershipGeneration, objectGeneration int64) error {
	if lease.Schema != ContextLeaseSchema || !strings.HasPrefix(lease.BindingDigest, "sha256:") {
		return errors.New("invalid_context_lease")
	}
	if !lease.ExpiresAt.After(now) {
		return errors.New("context_lease_expired")
	}
	if lease.PolicyEpoch != policyEpoch || lease.MembershipGeneration != membershipGeneration || lease.ObjectGeneration != objectGeneration {
		return errors.New("context_lease_stale")
	}
	return nil
}

func ValidateAirlockApproval(preparedDigest, approvedDigest string) error {
	if !strings.HasPrefix(preparedDigest, "sha256:") || preparedDigest != approvedDigest {
		return errors.New("airlock_digest_mismatch")
	}
	return nil
}

func ValidateMandatoryApplication(active bool, evidenceIDs []string) error {
	if !active {
		return errors.New("mandatory_inactive")
	}
	if len(evidenceIDs) == 0 {
		return errors.New("mandatory_evidence_required")
	}
	for _, evidenceID := range evidenceIDs {
		if !stableLifecycleID.MatchString(evidenceID) {
			return errors.New("mandatory_evidence_invalid")
		}
	}
	return nil
}

type LifecycleState string

const (
	StatePending                     LifecycleState = "pending"
	StateCanceled                    LifecycleState = "canceled"
	StateCommittedPrivate            LifecycleState = "committed_private"
	StateCorrected                   LifecycleState = "corrected"
	StateAirlockPrepared             LifecycleState = "airlock_prepared"
	StateAirlockApproved             LifecycleState = "airlock_approved"
	StateAirlockExpired              LifecycleState = "airlock_expired"
	StateAirlockCanceled             LifecycleState = "airlock_canceled"
	StateInFlight                    LifecycleState = "in_flight"
	StateRemoteCommittedLocalPending LifecycleState = "remote_committed_local_pending"
	StateReconciled                  LifecycleState = "reconciled"
	StateFailed                      LifecycleState = "failed"
	StateRetrieved                   LifecycleState = "retrieved"
)

var lifecycleTransitions = map[LifecycleState]map[LifecycleState]struct{}{
	StatePending:                     {StatePending: {}, StateCanceled: {}, StateCommittedPrivate: {}},
	StateCommittedPrivate:            {StateCorrected: {}, StateAirlockPrepared: {}},
	StateCorrected:                   {StateCommittedPrivate: {}},
	StateAirlockPrepared:             {StateAirlockApproved: {}, StateAirlockExpired: {}, StateAirlockCanceled: {}},
	StateAirlockApproved:             {StateAirlockPrepared: {}, StateAirlockCanceled: {}, StateInFlight: {}},
	StateInFlight:                    {StateRemoteCommittedLocalPending: {}, StateReconciled: {}, StateFailed: {}},
	StateRemoteCommittedLocalPending: {StateReconciled: {}},
}

func CanTransition(from, to LifecycleState) bool {
	_, ok := lifecycleTransitions[from][to]
	return ok
}

type VaultClass string

const (
	VaultPersonal VaultClass = "personal"
	VaultDesk     VaultClass = "desk"
	VaultCommons  VaultClass = "commons"
	VaultAirlock  VaultClass = "airlock"
)

type ProvenanceRef struct {
	VaultClass VaultClass `json:"vault_class"`
	ObjectID   string     `json:"object_id"`
}

func ValidateCommonsProvenance(refs []ProvenanceRef) error {
	if len(refs) == 0 {
		return errors.New("provenance_required")
	}
	for _, ref := range refs {
		if ref.VaultClass != VaultCommons && ref.VaultClass != VaultAirlock {
			return errors.New("private_lineage_forbidden")
		}
		if !stableLifecycleID.MatchString(ref.ObjectID) {
			return errors.New("invalid_provenance_object_id")
		}
	}
	return nil
}

type InjectionPack struct {
	Schema    string   `json:"schema"`
	Evidence  []string `json:"evidence"`
	Practices []string `json:"practices"`
}

func RenderInjection(pack InjectionPack) ([]byte, error) {
	if pack.Schema != InjectionSchema {
		return nil, errors.New("invalid_injection_schema")
	}
	return json.Marshal(pack)
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}
