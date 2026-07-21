//go:build darwin

package userpresence

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
)

func validPresencePublicKeyPEM(t *testing.T) []byte {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func TestDarwinEnhancedPresenceInspectionChecksExactInstalledTrustWithoutPrompt(t *testing.T) {
	helperVerified := false
	keyTrustVerified := false
	keyRead := false
	err := inspectDarwinEnhancedPresence(
		context.Background(), defaultPresenceHelper, defaultPresenceKey,
		func(_ context.Context, path string) error {
			helperVerified = path == defaultPresenceHelper
			return nil
		},
		func(path string, executable bool) error {
			keyTrustVerified = path == defaultPresenceKey && !executable
			return nil
		},
		func(path string) ([]byte, error) {
			keyRead = path == defaultPresenceKey
			return validPresencePublicKeyPEM(t), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !helperVerified || !keyTrustVerified || !keyRead {
		t.Fatalf("helper=%t key trust=%t key read=%t", helperVerified, keyTrustVerified, keyRead)
	}
}

func TestDarwinEnhancedPresenceInspectionFailsClosed(t *testing.T) {
	validKey := validPresencePublicKeyPEM(t)
	tests := []struct {
		name       string
		helperPath string
		verify     func(context.Context, string) error
		trust      func(string, bool) error
		read       func(string) ([]byte, error)
	}{
		{
			name: "noncanonical helper path", helperPath: "/tmp/presence-helper",
			verify: func(context.Context, string) error { return nil },
			trust:  func(string, bool) error { return nil },
			read:   func(string) ([]byte, error) { return validKey, nil },
		},
		{
			name: "untrusted helper", helperPath: defaultPresenceHelper,
			verify: func(context.Context, string) error { return errors.New("bad signature") },
			trust:  func(string, bool) error { return nil },
			read:   func(string) ([]byte, error) { return validKey, nil },
		},
		{
			name: "untrusted public key", helperPath: defaultPresenceHelper,
			verify: func(context.Context, string) error { return nil },
			trust:  func(string, bool) error { return errors.New("not root owned") },
			read:   func(string) ([]byte, error) { return validKey, nil },
		},
		{
			name: "invalid public key", helperPath: defaultPresenceHelper,
			verify: func(context.Context, string) error { return nil },
			trust:  func(string, bool) error { return nil },
			read:   func(string) ([]byte, error) { return []byte("not a public key"), nil },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := inspectDarwinEnhancedPresence(
				context.Background(), test.helperPath, defaultPresenceKey,
				test.verify, test.trust, test.read,
			); err == nil {
				t.Fatal("inspection accepted unavailable native presence")
			}
		})
	}
}
