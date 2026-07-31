package config

import (
	"os"
	"testing"
)

func TestPersonalTopologyIsLocalAndPrivate(t *testing.T) {
	root := t.TempDir()
	topology, err := NewPersonalTopology(root, "store_personal_nik", "127.0.0.1:18800", "keychain:pulse/personal/nik")
	if err != nil {
		t.Fatalf("personal topology: %v", err)
	}
	if topology.Personal == nil || topology.Personal.Kind != VaultPersonal {
		t.Fatalf("unexpected topology: %#v", topology)
	}
	if topology.Personal.DataDir == "" || topology.Personal.CacheDir == "" || topology.Personal.ListenAddr == "" {
		t.Fatalf("Personal must remain local: %#v", topology.Personal)
	}
}

func TestLoadVaultPinsPersonalIdentity(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadVault(dataDir, VaultPersonal, "store_personal_nik")
	if err != nil {
		t.Fatalf("load Personal vault: %v", err)
	}
	if cfg.VaultKind != VaultPersonal || cfg.StoreID != "store_personal_nik" || cfg.DataDir != dataDir {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if _, err := LoadVault(dataDir, "unsupported", "store_other"); err == nil {
		t.Fatal("unsupported local vault kind was accepted")
	}
	otherDir := t.TempDir()
	if err := os.Chmod(otherDir, 0700); err != nil {
		t.Fatal(err)
	}
	other, err := LoadVault(otherDir, VaultPersonal, "store_personal_other")
	if err != nil {
		t.Fatal(err)
	}
	if other.IPCSecret == cfg.IPCSecret || other.DBPath == cfg.DBPath {
		t.Fatal("separate Personal vaults reused a secret or database")
	}
}
