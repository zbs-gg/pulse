//go:build darwin

package userpresence

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	defaultPresenceHelper = "/Library/PrivilegedHelperTools/gg.zbs.pulse.presence-helper"
	defaultPresenceKey    = "/Library/Application Support/Pulse/trust/workspace-bindings.pub.pem"
	presenceHelperID      = "gg.zbs.pulse.presence-helper"
	presenceHelperTeamID  = "44N4NZ86S5"
)

type DarwinProver struct {
	HelperPath    string
	PublicKeyPath string
}

type presenceProofPayload struct {
	Schema      string `json:"schema"`
	Action      Action `json:"action"`
	Digest      string `json:"digest"`
	Nonce       string `json:"nonce"`
	PolicyEpoch uint64 `json:"policy_epoch"`
	ExpiresAt   string `json:"expires_at"`
}

type helperProof struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

func NewPlatformProver() Prover {
	return &DarwinProver{HelperPath: defaultPresenceHelper, PublicKeyPath: defaultPresenceKey}
}

func (p *DarwinProver) Prove(ctx context.Context, challenge Challenge) error {
	if p == nil || p.HelperPath != defaultPresenceHelper || p.PublicKeyPath != defaultPresenceKey {
		return ErrPresenceDenied
	}
	if err := verifyInstalledPresenceHelper(ctx, p.HelperPath); err != nil {
		return ErrPresenceDenied
	}
	if err := requireRootTrustFile(p.PublicKeyPath, false); err != nil {
		return ErrPresenceDenied
	}
	payload, err := json.Marshal(presenceProofPayload{
		Schema: "pulse.user-presence.challenge.v1", Action: challenge.Action,
		Digest: challenge.Digest, Nonce: challenge.Nonce, PolicyEpoch: challenge.PolicyEpoch,
		ExpiresAt: challenge.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
	})
	if err != nil {
		return ErrPresenceDenied
	}
	dir, err := os.MkdirTemp("", "pulse-presence-proof.")
	if err != nil {
		return ErrPresenceDenied
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0700); err != nil {
		return ErrPresenceDenied
	}
	payloadPath := filepath.Join(dir, "challenge.json")
	if err := os.WriteFile(payloadPath, payload, 0600); err != nil {
		return ErrPresenceDenied
	}
	cmd := exec.CommandContext(ctx, p.HelperPath, "prove", "--payload", payloadPath)
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil || stdout.Len() > 4096 {
		return ErrPresenceDenied
	}
	publicKeyPEM, err := os.ReadFile(p.PublicKeyPath)
	if err != nil || len(publicKeyPEM) == 0 || len(publicKeyPEM) > maximumPresencePublicKeyBytes {
		return ErrPresenceDenied
	}
	if err := verifyHelperProof(payload, stdout.Bytes(), publicKeyPEM); err != nil {
		return ErrPresenceDenied
	}
	return nil
}

func verifyInstalledPresenceHelper(ctx context.Context, path string) error {
	if err := requireRootTrustFile(path, true); err != nil {
		return err
	}
	verifyCommand := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "--verbose=2", path)
	if output, err := verifyCommand.CombinedOutput(); err != nil {
		return fmt.Errorf("verify helper signature: %w: %s", err, strings.TrimSpace(string(output)))
	}
	detailsCommand := exec.CommandContext(ctx, "/usr/bin/codesign", "-d", "--verbose=4", path)
	details, err := detailsCommand.CombinedOutput()
	if err != nil {
		return fmt.Errorf("read helper signature: %w", err)
	}
	text := string(details)
	if !strings.Contains(text, "Identifier="+presenceHelperID) || !strings.Contains(text, "TeamIdentifier="+presenceHelperTeamID) {
		return errors.New("presence helper has an unexpected signing identity")
	}
	return nil
}

func requireRootTrustFile(path string, executable bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("presence trust file is mutable or not regular")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("presence trust file must be root-owned")
	}
	if executable && info.Mode().Perm()&0111 == 0 {
		return errors.New("presence helper is not executable")
	}
	return nil
}

func verifyHelperProof(payload, encodedProof, publicKeyPEM []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encodedProof))
	decoder.DisallowUnknownFields()
	var proof helperProof
	if err := decoder.Decode(&proof); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("presence helper returned trailing JSON")
	}
	if proof.Algorithm != "es256" || !hexDigestPattern.MatchString(proof.KeyID) {
		return errors.New("presence helper returned an invalid proof contract")
	}
	publicKey, publicKeyDER, err := decodePresencePublicKey(publicKeyPEM)
	if err != nil {
		return err
	}
	keyID := sha256.Sum256(publicKeyDER)
	if hex.EncodeToString(keyID[:]) != proof.KeyID {
		return errors.New("presence key ID mismatch")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(proof.Signature)
	if err != nil || len(signature) == 0 || len(signature) > 80 {
		return errors.New("presence signature is invalid")
	}
	digest := sha256.Sum256(payload)
	if !ecdsa.VerifyASN1(publicKey, digest[:], signature) {
		return errors.New("presence signature does not verify")
	}
	return nil
}

func decodePresencePublicKey(publicKeyPEM []byte) (*ecdsa.PublicKey, []byte, error) {
	block, rest := pem.Decode(publicKeyPEM)
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, errors.New("presence public key is invalid")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve.Params().Name != "P-256" {
		return nil, nil, errors.New("presence public key must be P-256")
	}
	return publicKey, block.Bytes, nil
}
