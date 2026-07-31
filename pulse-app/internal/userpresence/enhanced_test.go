package userpresence

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func validProtectedTarget() ProtectedActionTargetV1 {
	return ProtectedActionTargetV1{
		Schema:              ProtectedActionTargetSchemaV1,
		Action:              ActionBindingChange,
		ProjectID:           "project_alpha",
		BindingDigest:       strings.Repeat("a", 64),
		VaultID:             "vault_personal",
		AffectedDataDigest:  strings.Repeat("b", 64),
		AffectedDataCount:   1,
		AffectedDataVersion: 3,
		PolicyEpoch:         7,
	}
}

func TestEnhancedPresenceProfilesAreVersionedAndNeverProtectHomeOpen(t *testing.T) {
	unavailable := NewUnavailableAuthorizer("enhanced_presence_unavailable").Profile()
	if unavailable.Schema != EnhancedPresenceProfileSchemaV1 || unavailable.Version != 1 ||
		unavailable.Kind != EnhancedPresenceUnavailable || unavailable.Available ||
		len(unavailable.ProtectedActions) != 0 {
		t.Fatalf("unavailable profile = %#v", unavailable)
	}
	webauthn := WebAuthnUnavailableProfile()
	if webauthn.Kind != EnhancedPresenceWebAuthn || webauthn.Available ||
		len(webauthn.ProtectedActions) != 0 || webauthn.ReasonCode != "webauthn_verifier_unavailable" {
		t.Fatalf("fail-closed WebAuthn profile = %#v", webauthn)
	}
	if IsEnhancedProtectedAction(ActionHomeOpen) ||
		!IsEnhancedProtectedAction(ActionBindingChange) || !IsEnhancedProtectedAction(ActionVaultWipe) {
		t.Fatal("enhanced protected-action set is wrong")
	}
}

func TestProtectedActionTargetCanonicalBytesBindEverySecurityField(t *testing.T) {
	target := validProtectedTarget()
	encoded, err := target.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"action":"binding.change","affected_data_count":1,"affected_data_digest":"` + strings.Repeat("b", 64) +
		`","affected_data_version":3,"binding_digest":"` + strings.Repeat("a", 64) +
		`","policy_epoch":7,"project_id":"project_alpha","repository_id":"","schema":"pulse.protected_action_target.v1","store_id":"","vault_id":"vault_personal"}`
	if string(encoded) != want {
		t.Fatalf("canonical target\n got: %s\nwant: %s", encoded, want)
	}
	digest, err := target.Digest()
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	mutated := target
	mutated.AffectedDataCount++
	mutatedDigest, err := mutated.Digest()
	if err != nil || mutatedDigest == digest {
		t.Fatal("affected-data count was not content-bound")
	}
}

func TestProtectedActionTargetRejectsAmbiguousOrOrdinaryTargets(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ProtectedActionTargetV1)
	}{
		{"home open", func(v *ProtectedActionTargetV1) { v.Action = ActionHomeOpen }},
		{"two project identities", func(v *ProtectedActionTargetV1) { v.RepositoryID = "repo_alpha" }},
		{"no project identity", func(v *ProtectedActionTargetV1) { v.ProjectID = "" }},
		{"two stores", func(v *ProtectedActionTargetV1) { v.StoreID = "store_alpha" }},
		{"no store", func(v *ProtectedActionTargetV1) { v.VaultID = "" }},
		{"no binding", func(v *ProtectedActionTargetV1) { v.BindingDigest = "" }},
		{"empty affected set", func(v *ProtectedActionTargetV1) { v.AffectedDataCount = 0 }},
		{"no affected version", func(v *ProtectedActionTargetV1) { v.AffectedDataVersion = 0 }},
		{"no policy epoch", func(v *ProtectedActionTargetV1) { v.PolicyEpoch = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := validProtectedTarget()
			test.mutate(&value)
			if _, err := value.CanonicalBytes(); !errors.Is(err, ErrProtectedActionTargetInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestUnavailableAuthorizerAlwaysReturnsStableActionUnavailable(t *testing.T) {
	authorizer := NewUnavailableAuthorizer("enhanced_presence_unavailable")
	if _, err := authorizer.Begin(context.Background(), validProtectedTarget()); !errors.Is(err, ErrEnhancedActionUnavailable) {
		t.Fatalf("begin error=%v", err)
	}
	if _, err := authorizer.Complete(context.Background(), EnhancedPresenceCompletionV1{}); !errors.Is(err, ErrEnhancedActionUnavailable) {
		t.Fatalf("complete error=%v", err)
	}
}

func TestSynchronousGateAuthorizerCompletesOneContentBoundNativeCeremony(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	prover := &proverStub{}
	gate, err := NewGate(prover, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := NewSynchronousGateAuthorizer(gate, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x4a}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	profile := authorizer.Profile()
	if profile.Kind != EnhancedPresenceMacOSNative || !profile.Available ||
		len(profile.ProtectedActions) != 2 || IsEnhancedProtectedAction(ActionHomeOpen) == true {
		t.Fatalf("native profile=%#v", profile)
	}
	target := validProtectedTarget()
	ceremony, err := authorizer.Begin(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	completion := EnhancedPresenceCompletionV1{
		Schema:       EnhancedPresenceCompletionSchemaV1,
		CeremonyID:   ceremony.CeremonyID,
		TargetDigest: ceremony.TargetDigest,
	}
	assertion, err := authorizer.Complete(context.Background(), completion)
	if err != nil {
		t.Fatal(err)
	}
	if assertion.Action != target.Action || assertion.TargetDigest != ceremony.TargetDigest ||
		assertion.ProfileKind != EnhancedPresenceMacOSNative || prover.calls != 1 {
		t.Fatalf("assertion=%#v calls=%d", assertion, prover.calls)
	}
	if _, err := authorizer.Complete(context.Background(), completion); !errors.Is(err, ErrEnhancedCeremonyInvalid) {
		t.Fatalf("replay error=%v", err)
	}
}

func TestSynchronousGateAuthorizerRejectsCompletionTargetMutationBeforePrompt(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	prover := &proverStub{}
	gate, _ := NewGate(prover, func() time.Time { return now })
	authorizer, _ := NewSynchronousGateAuthorizer(gate, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x4b}, 64)))
	ceremony, err := authorizer.Begin(context.Background(), validProtectedTarget())
	if err != nil {
		t.Fatal(err)
	}
	_, err = authorizer.Complete(context.Background(), EnhancedPresenceCompletionV1{
		Schema: EnhancedPresenceCompletionSchemaV1, CeremonyID: ceremony.CeremonyID,
		TargetDigest: strings.Repeat("f", 64),
	})
	if !errors.Is(err, ErrEnhancedCeremonyInvalid) || prover.calls != 0 {
		t.Fatalf("mutation error=%v prover calls=%d", err, prover.calls)
	}
}

func TestSynchronousGateAuthorizerCapsPendingAndPrunesExpiredBeforeInsert(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	prover := &proverStub{}
	gate, _ := NewGate(prover, func() time.Time { return now })
	randomBytes := make([]byte, (maximumPendingEnhancedCeremonies+2)*32)
	for index := 0; index < maximumPendingEnhancedCeremonies+2; index++ {
		for offset := 0; offset < 32; offset++ {
			randomBytes[index*32+offset] = byte(index + 1)
		}
	}
	authorizer, err := NewSynchronousGateAuthorizer(gate, func() time.Time { return now }, bytes.NewReader(randomBytes))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maximumPendingEnhancedCeremonies; index++ {
		if _, err := authorizer.Begin(context.Background(), validProtectedTarget()); err != nil {
			t.Fatalf("begin %d: %v", index, err)
		}
	}
	if len(authorizer.items) != maximumPendingEnhancedCeremonies {
		t.Fatalf("pending=%d", len(authorizer.items))
	}
	if _, err := authorizer.Begin(context.Background(), validProtectedTarget()); !errors.Is(err, ErrEnhancedCeremonyCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	if len(authorizer.items) != maximumPendingEnhancedCeremonies {
		t.Fatalf("pending grew past cap: %d", len(authorizer.items))
	}
	now = now.Add(91 * time.Second)
	if _, err := authorizer.Begin(context.Background(), validProtectedTarget()); err != nil {
		t.Fatalf("begin after expiry: %v", err)
	}
	if len(authorizer.items) != 1 {
		t.Fatalf("expired ceremonies were not pruned; pending=%d", len(authorizer.items))
	}
}
