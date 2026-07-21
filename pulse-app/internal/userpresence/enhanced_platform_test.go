package userpresence

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestInspectedPlatformEnhancedAuthorizerStaysUnavailableWhenInspectionFails(t *testing.T) {
	prover := &proverStub{}
	authorizer := newInspectedPlatformEnhancedAuthorizer(
		context.Background(),
		func(context.Context) error { return errors.New("helper is not trusted") },
		prover,
		time.Now,
		bytes.NewReader(bytes.Repeat([]byte{0x4c}, 32)),
	)

	profile := authorizer.Profile()
	if profile.Schema != EnhancedPresenceProfileSchemaV1 || profile.Version != 1 ||
		profile.Kind != EnhancedPresenceUnavailable || profile.Available ||
		len(profile.ProtectedActions) != 0 || profile.ReasonCode != "enhanced_presence_unavailable" {
		t.Fatalf("unavailable profile = %#v", profile)
	}
	if prover.calls != 0 {
		t.Fatalf("startup inspection prompted the prover %d time(s)", prover.calls)
	}
}

func TestInspectedPlatformEnhancedAuthorizerAdvertisesNativeOnlyAfterInspection(t *testing.T) {
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	inspectionCalls := 0
	prover := &proverStub{}
	authorizer := newInspectedPlatformEnhancedAuthorizer(
		context.Background(),
		func(context.Context) error {
			inspectionCalls++
			return nil
		},
		prover,
		func() time.Time { return now },
		bytes.NewReader(bytes.Repeat([]byte{0x4d}, 64)),
	)

	profile := authorizer.Profile()
	if inspectionCalls != 1 || profile.Kind != EnhancedPresenceMacOSNative || !profile.Available ||
		len(profile.ProtectedActions) != 2 || profile.ReasonCode != "" {
		t.Fatalf("inspection calls=%d profile=%#v", inspectionCalls, profile)
	}
	if prover.calls != 0 {
		t.Fatalf("authorizer construction prompted the prover %d time(s)", prover.calls)
	}

	ceremony, err := authorizer.Begin(context.Background(), validProtectedTarget())
	if err != nil {
		t.Fatal(err)
	}
	if prover.calls != 0 {
		t.Fatalf("begin prompted the prover %d time(s)", prover.calls)
	}
	_, err = authorizer.Complete(context.Background(), EnhancedPresenceCompletionV1{
		Schema: EnhancedPresenceCompletionSchemaV1, CeremonyID: ceremony.CeremonyID,
		TargetDigest: ceremony.TargetDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prover.calls != 1 {
		t.Fatalf("complete prompted the prover %d time(s), want 1", prover.calls)
	}
}
