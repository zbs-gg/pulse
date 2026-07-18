package userpresence

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"
)

const (
	EnhancedPresenceProfileSchemaV1    = "pulse.enhanced_presence.profile.v1"
	ProtectedActionTargetSchemaV1      = "pulse.protected_action_target.v1"
	EnhancedPresenceCeremonySchemaV1   = "pulse.enhanced_presence.ceremony.v1"
	EnhancedPresenceCompletionSchemaV1 = "pulse.enhanced_presence.completion.v1"
	EnhancedPresenceAssertionSchemaV1  = "pulse.enhanced_presence.assertion.v1"
	maximumPendingEnhancedCeremonies   = 16
)

type EnhancedPresenceKind string

const (
	EnhancedPresenceWebAuthn    EnhancedPresenceKind = "webauthn"
	EnhancedPresenceMacOSNative EnhancedPresenceKind = "macos_native"
	EnhancedPresenceUnavailable EnhancedPresenceKind = "unavailable"
)

var (
	ErrEnhancedActionUnavailable    = errors.New("enhanced user-presence action is unavailable")
	ErrEnhancedCeremonyInvalid      = errors.New("enhanced user-presence ceremony is invalid")
	ErrEnhancedCeremonyExpired      = errors.New("enhanced user-presence ceremony expired")
	ErrEnhancedCeremonyCapacity     = errors.New("enhanced user-presence ceremony capacity reached")
	ErrProtectedActionTargetInvalid = errors.New("protected-action target is invalid")
	enhancedIdentityPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	enhancedReasonCodePattern       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	enhancedProtectedActions        = []Action{ActionBindingChange, ActionVaultWipe}
)

// EnhancedPresenceProfile is capability truth, not a promise that a verifier
// will later appear. An unavailable mechanism always advertises zero protected
// actions, including the intentionally fail-closed WebAuthn placeholder.
type EnhancedPresenceProfile struct {
	Schema           string               `json:"schema"`
	Version          uint64               `json:"version"`
	Kind             EnhancedPresenceKind `json:"kind"`
	Available        bool                 `json:"available"`
	ProtectedActions []Action             `json:"protected_actions"`
	ReasonCode       string               `json:"reason_code"`
}

func (p EnhancedPresenceProfile) clone() EnhancedPresenceProfile {
	p.ProtectedActions = append([]Action(nil), p.ProtectedActions...)
	return p
}

func unavailableProfile(kind EnhancedPresenceKind, reason string) EnhancedPresenceProfile {
	if !enhancedReasonCodePattern.MatchString(reason) {
		reason = "enhanced_presence_unavailable"
	}
	return EnhancedPresenceProfile{
		Schema: EnhancedPresenceProfileSchemaV1, Version: 1, Kind: kind,
		Available: false, ProtectedActions: []Action{}, ReasonCode: reason,
	}
}

func nativeProfile() EnhancedPresenceProfile {
	return EnhancedPresenceProfile{
		Schema: EnhancedPresenceProfileSchemaV1, Version: 1, Kind: EnhancedPresenceMacOSNative,
		Available: true, ProtectedActions: append([]Action(nil), enhancedProtectedActions...),
	}
}

// WebAuthnUnavailableProfile is the only WebAuthn profile until a real
// userVerification-required verifier lands. It cannot authorize anything.
func WebAuthnUnavailableProfile() EnhancedPresenceProfile {
	return unavailableProfile(EnhancedPresenceWebAuthn, "webauthn_verifier_unavailable")
}

func IsEnhancedProtectedAction(action Action) bool {
	return action == ActionBindingChange || action == ActionVaultWipe
}

// ProtectedActionTargetV1 is the complete content bound into an enhanced
// presence ceremony. Identities are opaque IDs, never filesystem paths or
// ambient process state.
type ProtectedActionTargetV1 struct {
	Action              Action `json:"action"`
	AffectedDataCount   uint64 `json:"affected_data_count"`
	AffectedDataDigest  string `json:"affected_data_digest"`
	AffectedDataVersion uint64 `json:"affected_data_version"`
	BindingDigest       string `json:"binding_digest"`
	PolicyEpoch         uint64 `json:"policy_epoch"`
	ProjectID           string `json:"project_id"`
	RepositoryID        string `json:"repository_id"`
	Schema              string `json:"schema"`
	StoreID             string `json:"store_id"`
	VaultID             string `json:"vault_id"`
}

func exactlyOneIdentity(first, second string) bool {
	return (first == "") != (second == "")
}

func validEnhancedIdentity(value string) bool {
	return enhancedIdentityPattern.MatchString(value)
}

func nonzeroDigest(value string) bool {
	return hexDigestPattern.MatchString(value) && value != "0000000000000000000000000000000000000000000000000000000000000000"
}

func (t ProtectedActionTargetV1) validate() error {
	if t.Schema != ProtectedActionTargetSchemaV1 || !IsEnhancedProtectedAction(t.Action) ||
		!exactlyOneIdentity(t.ProjectID, t.RepositoryID) || !exactlyOneIdentity(t.VaultID, t.StoreID) ||
		!nonzeroDigest(t.BindingDigest) || !nonzeroDigest(t.AffectedDataDigest) ||
		t.AffectedDataCount < 1 || t.AffectedDataCount > 1_000_000_000 ||
		t.AffectedDataVersion < 1 || t.PolicyEpoch < 1 {
		return ErrProtectedActionTargetInvalid
	}
	for _, value := range []string{t.ProjectID, t.RepositoryID, t.VaultID, t.StoreID} {
		if value != "" && !validEnhancedIdentity(value) {
			return ErrProtectedActionTargetInvalid
		}
	}
	return nil
}

func (t ProtectedActionTargetV1) CanonicalBytes() ([]byte, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("%w: encode", ErrProtectedActionTargetInvalid)
	}
	return encoded, nil
}

func (t ProtectedActionTargetV1) Digest() (string, error) {
	encoded, err := t.CanonicalBytes()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("pulse-protected-action-target-v1\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

type EnhancedPresenceCeremonyV1 struct {
	Schema       string               `json:"schema"`
	CeremonyID   string               `json:"ceremony_id"`
	ProfileKind  EnhancedPresenceKind `json:"profile_kind"`
	TargetDigest string               `json:"target_digest"`
	ExpiresAt    time.Time            `json:"expires_at"`
}

type EnhancedPresenceCompletionV1 struct {
	Schema       string `json:"schema"`
	CeremonyID   string `json:"ceremony_id"`
	TargetDigest string `json:"target_digest"`
}

type EnhancedPresenceAssertionV1 struct {
	Schema          string               `json:"schema"`
	Action          Action               `json:"action"`
	CeremonyID      string               `json:"ceremony_id"`
	ProfileKind     EnhancedPresenceKind `json:"profile_kind"`
	TargetDigest    string               `json:"target_digest"`
	PolicyEpoch     uint64               `json:"policy_epoch"`
	ApprovedAt      time.Time            `json:"approved_at"`
	ExpiresAt       time.Time            `json:"expires_at"`
	AssertionDigest string               `json:"assertion_digest"`
}

type EnhancedPresenceAuthorizer interface {
	Profile() EnhancedPresenceProfile
	Begin(context.Context, ProtectedActionTargetV1) (EnhancedPresenceCeremonyV1, error)
	Complete(context.Context, EnhancedPresenceCompletionV1) (EnhancedPresenceAssertionV1, error)
}

type UnavailableAuthorizer struct {
	profile EnhancedPresenceProfile
}

func NewUnavailableAuthorizer(reasonCode string) *UnavailableAuthorizer {
	return &UnavailableAuthorizer{profile: unavailableProfile(EnhancedPresenceUnavailable, reasonCode)}
}

func (a *UnavailableAuthorizer) Profile() EnhancedPresenceProfile {
	if a == nil {
		return unavailableProfile(EnhancedPresenceUnavailable, "enhanced_presence_unavailable")
	}
	return a.profile.clone()
}

func (*UnavailableAuthorizer) Begin(context.Context, ProtectedActionTargetV1) (EnhancedPresenceCeremonyV1, error) {
	return EnhancedPresenceCeremonyV1{}, ErrEnhancedActionUnavailable
}

func (*UnavailableAuthorizer) Complete(context.Context, EnhancedPresenceCompletionV1) (EnhancedPresenceAssertionV1, error) {
	return EnhancedPresenceAssertionV1{}, ErrEnhancedActionUnavailable
}

type pendingNativeCeremony struct {
	challenge Challenge
	target    ProtectedActionTargetV1
}

// SynchronousGateAuthorizer adapts the installed macOS native Gate without
// weakening it into a bearer token. Begin only creates content-bound pending
// state; Complete synchronously invokes the OS prompt and consumes the state.
type SynchronousGateAuthorizer struct {
	gate   *Gate
	now    func() time.Time
	random io.Reader
	mu     sync.Mutex
	items  map[string]pendingNativeCeremony
}

func NewSynchronousGateAuthorizer(gate *Gate, now func() time.Time, randomSource io.Reader) (*SynchronousGateAuthorizer, error) {
	if gate == nil {
		return nil, errors.New("synchronous presence authorizer requires a Gate")
	}
	if now == nil {
		now = time.Now
	}
	if randomSource == nil {
		randomSource = rand.Reader
	}
	return &SynchronousGateAuthorizer{gate: gate, now: now, random: randomSource, items: make(map[string]pendingNativeCeremony)}, nil
}

func (*SynchronousGateAuthorizer) Profile() EnhancedPresenceProfile { return nativeProfile().clone() }

func (a *SynchronousGateAuthorizer) Begin(ctx context.Context, target ProtectedActionTargetV1) (EnhancedPresenceCeremonyV1, error) {
	if err := ctx.Err(); err != nil {
		return EnhancedPresenceCeremonyV1{}, err
	}
	targetDigest, err := target.Digest()
	if err != nil {
		return EnhancedPresenceCeremonyV1{}, err
	}
	now := a.now().UTC()
	nonceBytes := make([]byte, 32)
	if _, err := io.ReadFull(a.random, nonceBytes); err != nil {
		return EnhancedPresenceCeremonyV1{}, errors.New("generate enhanced presence ceremony")
	}
	nonce := hex.EncodeToString(nonceBytes)
	ceremonyHash := sha256.Sum256([]byte("pulse-enhanced-presence-ceremony-v1\x00" + targetDigest + "\x00" + nonce))
	ceremonyID := hex.EncodeToString(ceremonyHash[:])
	expiresAt := now.Add(90 * time.Second)
	challenge := Challenge{
		Action: target.Action, Digest: targetDigest, Nonce: nonce,
		PolicyEpoch: target.PolicyEpoch, ExpiresAt: expiresAt,
		Display: protectedActionDisplay(target.Action, target.AffectedDataCount),
	}
	a.mu.Lock()
	for id, pending := range a.items {
		if !now.Before(pending.challenge.ExpiresAt.UTC()) {
			delete(a.items, id)
		}
	}
	if len(a.items) >= maximumPendingEnhancedCeremonies {
		a.mu.Unlock()
		return EnhancedPresenceCeremonyV1{}, ErrEnhancedCeremonyCapacity
	}
	a.items[ceremonyID] = pendingNativeCeremony{challenge: challenge, target: target}
	a.mu.Unlock()
	return EnhancedPresenceCeremonyV1{
		Schema: EnhancedPresenceCeremonySchemaV1, CeremonyID: ceremonyID,
		ProfileKind: EnhancedPresenceMacOSNative, TargetDigest: targetDigest, ExpiresAt: expiresAt,
	}, nil
}

func protectedActionDisplay(action Action, count uint64) string {
	switch action {
	case ActionBindingChange:
		return fmt.Sprintf("Authorize Pulse binding change affecting %d item(s)", count)
	case ActionVaultWipe:
		return fmt.Sprintf("Authorize Pulse memory wipe affecting %d item(s)", count)
	default:
		return "Authorize protected Pulse action"
	}
}

func (a *SynchronousGateAuthorizer) Complete(ctx context.Context, completion EnhancedPresenceCompletionV1) (EnhancedPresenceAssertionV1, error) {
	if completion.Schema != EnhancedPresenceCompletionSchemaV1 || !hexDigestPattern.MatchString(completion.CeremonyID) ||
		!hexDigestPattern.MatchString(completion.TargetDigest) {
		return EnhancedPresenceAssertionV1{}, ErrEnhancedCeremonyInvalid
	}
	a.mu.Lock()
	pending, exists := a.items[completion.CeremonyID]
	if exists {
		delete(a.items, completion.CeremonyID)
	}
	a.mu.Unlock()
	if !exists || pending.challenge.Digest != completion.TargetDigest {
		return EnhancedPresenceAssertionV1{}, ErrEnhancedCeremonyInvalid
	}
	if !a.now().UTC().Before(pending.challenge.ExpiresAt.UTC()) {
		return EnhancedPresenceAssertionV1{}, ErrEnhancedCeremonyExpired
	}
	presenceAssertion, err := a.gate.Authorize(ctx, pending.challenge)
	if err != nil {
		return EnhancedPresenceAssertionV1{}, err
	}
	assertionDigest := sha256.Sum256([]byte(fmt.Sprintf(
		"pulse-enhanced-presence-assertion-v1\x00%s\x00%s\x00%s\x00%d\x00%s",
		completion.CeremonyID, completion.TargetDigest, presenceAssertion.NonceHash,
		presenceAssertion.PolicyEpoch, presenceAssertion.ApprovedAt.UTC().Format(time.RFC3339Nano),
	)))
	return EnhancedPresenceAssertionV1{
		Schema: EnhancedPresenceAssertionSchemaV1, Action: pending.target.Action,
		CeremonyID: completion.CeremonyID, ProfileKind: EnhancedPresenceMacOSNative,
		TargetDigest: completion.TargetDigest, PolicyEpoch: pending.target.PolicyEpoch,
		ApprovedAt: presenceAssertion.ApprovedAt.UTC(), ExpiresAt: presenceAssertion.ExpiresAt.UTC(),
		AssertionDigest: hex.EncodeToString(assertionDigest[:]),
	}, nil
}

var (
	_ EnhancedPresenceAuthorizer = (*UnavailableAuthorizer)(nil)
	_ EnhancedPresenceAuthorizer = (*SynchronousGateAuthorizer)(nil)
)
