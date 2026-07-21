//go:build darwin

package userpresence

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyHelperProofBindsExactBytesToPinnedP256Key(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"schema":"pulse.user-presence.challenge.v1","action":"binding.change"}`)
	digest := sha256.Sum256(payload)
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	keyID := sha256.Sum256(der)
	proof, err := json.Marshal(helperProof{
		Algorithm: "es256", KeyID: hex.EncodeToString(keyID[:]),
		Signature: base64.StdEncoding.EncodeToString(signature),
	})
	if err != nil {
		t.Fatal(err)
	}
	publicKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if err := verifyHelperProof(payload, proof, publicKey); err != nil {
		t.Fatalf("verify proof: %v", err)
	}
	mutated := append([]byte(nil), payload...)
	mutated[len(mutated)-2] ^= 1
	if err := verifyHelperProof(mutated, proof, publicKey); err == nil {
		t.Fatal("proof verified for mutated bytes")
	}
}

func TestPresenceTrustFilesRejectSameUIDReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("fake"), 0500); err != nil {
		t.Fatal(err)
	}
	if err := requireRootTrustFile(path, true); err == nil {
		t.Fatal("same-UID helper replacement was trusted")
	}
}
