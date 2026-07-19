//go:build !darwin

package userpresence

import (
	"context"
	"testing"
	"time"
)

func TestNonDarwinPlatformEnhancedPresenceRemainsUnavailable(t *testing.T) {
	profile := NewPlatformEnhancedAuthorizer(context.Background(), time.Now, nil).Profile()
	if profile.Kind != EnhancedPresenceUnavailable || profile.Available ||
		len(profile.ProtectedActions) != 0 || profile.ReasonCode != "enhanced_presence_unavailable" {
		t.Fatalf("non-darwin profile = %#v", profile)
	}
}
