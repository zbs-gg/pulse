package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadGeneratesSecretIfMissing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(cfg.IPCSecret) != 64 {
		t.Errorf("expected 64-char hex secret, got %d chars", len(cfg.IPCSecret))
	}
	keyPath := filepath.Join(dir, "secret.key")
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("secret.key not created: %v", err)
	}
	if string(data) != cfg.IPCSecret {
		t.Errorf("secret on disk does not match config")
	}
	info, _ := os.Stat(keyPath)
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %o", info.Mode().Perm())
	}
}

func TestLoadReusesExistingSecret(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret.key")
	existing := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(keyPath, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.IPCSecret != existing {
		t.Errorf("expected existing secret to be reused, got new one")
	}
}
