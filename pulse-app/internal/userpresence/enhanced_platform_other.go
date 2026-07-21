//go:build !darwin

package userpresence

import (
	"context"
	"io"
	"time"
)

// NewPlatformEnhancedAuthorizer stays unavailable until a real
// user-verifying WebAuthn adapter exists on non-macOS platforms.
func NewPlatformEnhancedAuthorizer(context.Context, func() time.Time, io.Reader) EnhancedPresenceAuthorizer {
	return NewUnavailableAuthorizer("enhanced_presence_unavailable")
}
