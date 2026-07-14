package userpresence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

type Action string

const (
	ActionBindingChange     Action = "binding.change"
	ActionVaultWipe         Action = "vault.wipe"
	ActionAirlockApprove    Action = "airlock.approve"
	ActionMandatoryActivate Action = "mandatory.activate"
	ActionMembershipChange  Action = "membership.change"
)

var (
	ErrPresenceInvalid = errors.New("user-presence challenge is invalid")
	ErrPresenceExpired = errors.New("user-presence challenge expired")
	ErrPresenceReplay  = errors.New("user-presence challenge replayed")
	ErrPresenceDenied  = errors.New("OS user presence was denied")
	hexDigestPattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	hexNoncePattern    = regexp.MustCompile(`^[a-f0-9]{32,128}$`)
)

type Challenge struct {
	Action      Action    `json:"action"`
	Digest      string    `json:"digest"`
	Nonce       string    `json:"nonce"`
	PolicyEpoch uint64    `json:"policy_epoch"`
	ExpiresAt   time.Time `json:"expires_at"`
	Display     string    `json:"display"`
}

// Assertion intentionally omits the raw nonce and any reusable OS
// authorization reference. It is a receipt, never an authorization token.
type Assertion struct {
	Action      Action    `json:"action"`
	Digest      string    `json:"digest"`
	Nonce       string    `json:"-"`
	NonceHash   string    `json:"nonce_hash"`
	PolicyEpoch uint64    `json:"policy_epoch"`
	ApprovedAt  time.Time `json:"approved_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Prover must perform a fresh OS-backed user-presence operation. It must not
// return or persist an AuthorizationRef, password, biometric data, or key.
type Prover interface {
	Prove(context.Context, Challenge) error
}

type Gate struct {
	prover Prover
	now    func() time.Time
	mu     sync.Mutex
	used   map[string]struct{}
}

func NewGate(prover Prover, now func() time.Time) (*Gate, error) {
	if prover == nil {
		return nil, errors.New("user-presence gate requires an OS prover")
	}
	if now == nil {
		now = time.Now
	}
	return &Gate{prover: prover, now: now, used: make(map[string]struct{})}, nil
}

func (g *Gate) Authorize(ctx context.Context, challenge Challenge) (Assertion, error) {
	now := g.now().UTC()
	if err := validateChallenge(challenge, now); err != nil {
		return Assertion{}, err
	}
	key := challengeReplayKey(challenge)
	g.mu.Lock()
	_, replayed := g.used[key]
	g.mu.Unlock()
	if replayed {
		return Assertion{}, ErrPresenceReplay
	}
	if err := g.prover.Prove(ctx, challenge); err != nil {
		return Assertion{}, fmt.Errorf("%w", ErrPresenceDenied)
	}
	g.mu.Lock()
	if _, replayed := g.used[key]; replayed {
		g.mu.Unlock()
		return Assertion{}, ErrPresenceReplay
	}
	g.used[key] = struct{}{}
	g.mu.Unlock()
	nonceHash := sha256.Sum256([]byte("pulse-user-presence-nonce-v1\x00" + challenge.Nonce))
	return Assertion{
		Action: challenge.Action, Digest: challenge.Digest,
		NonceHash: hex.EncodeToString(nonceHash[:]), PolicyEpoch: challenge.PolicyEpoch,
		ApprovedAt: now, ExpiresAt: challenge.ExpiresAt.UTC(),
	}, nil
}

func validateChallenge(challenge Challenge, now time.Time) error {
	switch challenge.Action {
	case ActionBindingChange, ActionVaultWipe, ActionAirlockApprove, ActionMandatoryActivate, ActionMembershipChange:
	default:
		return ErrPresenceInvalid
	}
	if !hexDigestPattern.MatchString(challenge.Digest) || !hexNoncePattern.MatchString(challenge.Nonce) ||
		challenge.PolicyEpoch < 1 || challenge.Display == "" || len(challenge.Display) > 500 ||
		challenge.ExpiresAt.IsZero() {
		return ErrPresenceInvalid
	}
	for _, char := range challenge.Display {
		if char < 0x20 || char == 0x7f {
			return ErrPresenceInvalid
		}
	}
	expires := challenge.ExpiresAt.UTC()
	if !expires.After(now) {
		return ErrPresenceExpired
	}
	if expires.Sub(now) > 2*time.Minute {
		return ErrPresenceInvalid
	}
	return nil
}

func challengeReplayKey(challenge Challenge) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("pulse-user-presence-v1\x00%s\x00%s\x00%s\x00%d",
		challenge.Action, challenge.Digest, challenge.Nonce, challenge.PolicyEpoch)))
	return hex.EncodeToString(digest[:])
}
