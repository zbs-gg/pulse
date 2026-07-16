package userpresence

import (
	"context"
	"errors"
	"testing"
	"time"
)

type proverStub struct {
	calls int
	err   error
	prove func()
}

func (p *proverStub) Prove(context.Context, Challenge) error {
	p.calls++
	if p.prove != nil {
		p.prove()
	}
	return p.err
}

func validChallenge() Challenge {
	return Challenge{
		Action:      ActionBindingChange,
		Digest:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Nonce:       "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		PolicyEpoch: 7,
		ExpiresAt:   time.Date(2026, 7, 14, 12, 1, 0, 0, time.UTC),
		Display:     "Bind this workspace to Team ZBS as principal Nik",
	}
}

func TestGateBindsPresenceToExactActionDigestNonceAndEpoch(t *testing.T) {
	prover := &proverStub{}
	gate, err := NewGate(prover, func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("new gate: %v", err)
	}
	assertion, err := gate.Authorize(context.Background(), validChallenge())
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if assertion.Action != ActionBindingChange || assertion.PolicyEpoch != 7 || assertion.Digest != validChallenge().Digest {
		t.Fatalf("unexpected assertion: %#v", assertion)
	}
	if assertion.Nonce != "" || assertion.NonceHash == "" {
		t.Fatal("reusable nonce leaked from assertion")
	}
	if prover.calls != 1 {
		t.Fatalf("prover calls = %d", prover.calls)
	}
}

func TestGateAllowsFreshHomeOpenPresence(t *testing.T) {
	prover := &proverStub{}
	gate, err := NewGate(prover, func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	challenge := validChallenge()
	challenge.Action = ActionHomeOpen
	challenge.Display = "Open Pulse Memory Home"
	assertion, err := gate.Authorize(context.Background(), challenge)
	if err != nil {
		t.Fatalf("authorize home.open: %v", err)
	}
	if assertion.Action != challenge.Action || assertion.Digest != challenge.Digest || prover.calls != 1 {
		t.Fatalf("home.open assertion=%#v prover calls=%d", assertion, prover.calls)
	}
}

func TestGateRejectsPresenceThatCompletesAfterChallengeExpiry(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	prover := &proverStub{prove: func() { now = now.Add(61 * time.Second) }}
	gate, err := NewGate(prover, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	challenge := validChallenge()
	challenge.ExpiresAt = now.Add(time.Minute)
	if _, err := gate.Authorize(context.Background(), challenge); !errors.Is(err, ErrPresenceExpired) {
		t.Fatalf("late presence error=%v, want ErrPresenceExpired", err)
	}
}

func TestGateRejectsReplayExpiryMutationAndDeniedPresence(t *testing.T) {
	prover := &proverStub{}
	gate, err := NewGate(prover, func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	challenge := validChallenge()
	if _, err := gate.Authorize(context.Background(), challenge); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Authorize(context.Background(), challenge); !errors.Is(err, ErrPresenceReplay) {
		t.Fatalf("replay error = %v", err)
	}

	expired := validChallenge()
	expired.Nonce = "cccccccccccccccccccccccccccccccc"
	expired.ExpiresAt = time.Date(2026, 7, 14, 11, 59, 0, 0, time.UTC)
	if _, err := gate.Authorize(context.Background(), expired); !errors.Is(err, ErrPresenceExpired) {
		t.Fatalf("expiry error = %v", err)
	}

	mutated := validChallenge()
	mutated.Nonce = "dddddddddddddddddddddddddddddddd"
	mutated.Digest = "not-a-digest"
	if _, err := gate.Authorize(context.Background(), mutated); !errors.Is(err, ErrPresenceInvalid) {
		t.Fatalf("mutation error = %v", err)
	}

	denied := &proverStub{err: errors.New("denied")}
	deniedGate, err := NewGate(denied, func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	fresh := validChallenge()
	fresh.Nonce = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, err := deniedGate.Authorize(context.Background(), fresh); !errors.Is(err, ErrPresenceDenied) {
		t.Fatalf("denied error = %v", err)
	}
}
