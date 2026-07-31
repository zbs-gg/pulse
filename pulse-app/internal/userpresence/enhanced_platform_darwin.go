//go:build darwin

package userpresence

import (
	"context"
	"errors"
	"io"
	"os"
	"time"
)

const maximumPresencePublicKeyBytes = 4096

// NewPlatformEnhancedAuthorizer advertises the macOS native adapter only
// after a non-interactive inspection of the exact installed helper and trust
// key. The user-presence prompt itself remains deferred until Complete.
func NewPlatformEnhancedAuthorizer(ctx context.Context, now func() time.Time, randomSource io.Reader) EnhancedPresenceAuthorizer {
	return newInspectedPlatformEnhancedAuthorizer(
		ctx,
		inspectInstalledDarwinEnhancedPresence,
		NewPlatformProver(),
		now,
		randomSource,
	)
}

func inspectInstalledDarwinEnhancedPresence(ctx context.Context) error {
	return inspectDarwinEnhancedPresence(
		ctx,
		defaultPresenceHelper,
		defaultPresenceKey,
		verifyInstalledPresenceHelper,
		requireRootTrustFile,
		os.ReadFile,
	)
}

func inspectDarwinEnhancedPresence(
	ctx context.Context,
	helperPath string,
	publicKeyPath string,
	verifyHelper func(context.Context, string) error,
	requireTrustFile func(string, bool) error,
	readFile func(string) ([]byte, error),
) error {
	if helperPath != defaultPresenceHelper || publicKeyPath != defaultPresenceKey {
		return errors.New("presence helper trust paths are not canonical")
	}
	if verifyHelper == nil || requireTrustFile == nil || readFile == nil {
		return errors.New("presence helper inspection is incomplete")
	}
	if err := verifyHelper(ctx, helperPath); err != nil {
		return err
	}
	if err := requireTrustFile(publicKeyPath, false); err != nil {
		return err
	}
	publicKeyPEM, err := readFile(publicKeyPath)
	if err != nil {
		return err
	}
	if len(publicKeyPEM) == 0 || len(publicKeyPEM) > maximumPresencePublicKeyBytes {
		return errors.New("presence public key has an invalid size")
	}
	_, _, err = decodePresencePublicKey(publicKeyPEM)
	return err
}
