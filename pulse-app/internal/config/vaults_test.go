package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVaultTopologiesKeepPersonalDeskAndCommonsPhysicallyDistinct(t *testing.T) {
	root := t.TempDir()
	personal, err := NewPersonalTopology(root, "store_personal_nik", "127.0.0.1:18800", "keychain:pulse/personal/nik")
	if err != nil {
		t.Fatalf("personal topology: %v", err)
	}
	team, err := NewTeamTopology(TeamTopologyRequest{
		RootDir:           root,
		PrincipalID:       "principal_nik",
		TeamID:            "team_zbs",
		DeskStoreID:       "store_desk_nik_zbs",
		DeskAddr:          "127.0.0.1:18801",
		DeskCredential:    "keychain:pulse/desk/nik/zbs",
		CommonsStoreID:    "store_commons_zbs",
		CommonsResource:   "https://pulse.zbs.gg/team_zbs",
		CommonsCredential: "keychain:pulse/commons/zbs/nik",
	})
	if err != nil {
		t.Fatalf("team topology: %v", err)
	}

	if personal.Personal == nil || team.Desk == nil || team.Commons == nil {
		t.Fatal("expected complete personal and team topologies")
	}
	if personal.Personal.StoreID == team.Desk.StoreID || personal.Personal.DataDir == team.Desk.DataDir ||
		personal.Personal.CacheDir == team.Desk.CacheDir || personal.Personal.CredentialRef == team.Desk.CredentialRef {
		t.Fatal("personal and desk resources must be physically distinct")
	}
	if team.Commons.DataDir != "" || team.Commons.CacheDir == team.Desk.CacheDir || team.Commons.CredentialRef == team.Desk.CredentialRef {
		t.Fatal("commons must be remote and partitioned from the local desk")
	}
	if filepath.Dir(personal.Personal.DataDir) == filepath.Dir(team.Desk.DataDir) {
		t.Fatal("personal and desk data roots must not share a leaf parent")
	}
}

func TestTeamTopologyRejectsFallbackAndAuthorityConfusion(t *testing.T) {
	valid := TeamTopologyRequest{
		RootDir: t.TempDir(), PrincipalID: "principal_dima", TeamID: "team_zbs",
		DeskStoreID: "store_desk_dima_zbs", DeskAddr: "127.0.0.1:18802",
		DeskCredential: "keychain:pulse/desk/dima/zbs", CommonsStoreID: "store_commons_zbs",
		CommonsResource: "https://pulse.zbs.gg/team_zbs", CommonsCredential: "keychain:pulse/commons/zbs/dima",
	}

	nonLoopback := valid
	nonLoopback.DeskAddr = "0.0.0.0:18802"
	if _, err := NewTeamTopology(nonLoopback); err == nil {
		t.Fatal("expected non-loopback desk address to fail")
	}
	localCommons := valid
	localCommons.CommonsResource = "http://127.0.0.1:18789"
	if _, err := NewTeamTopology(localCommons); err == nil {
		t.Fatal("expected local commons substitution to fail")
	}
	sameCredential := valid
	sameCredential.CommonsCredential = sameCredential.DeskCredential
	if _, err := NewTeamTopology(sameCredential); err == nil {
		t.Fatal("expected shared local/remote credential to fail")
	}
}

func TestLoadVaultPinsKindAndStoreIdentity(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadVault(dataDir, VaultDesk, "store_desk_nik_zbs")
	if err != nil {
		t.Fatalf("load desk: %v", err)
	}
	if cfg.VaultKind != VaultDesk || cfg.StoreID != "store_desk_nik_zbs" || cfg.DataDir != dataDir {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if _, err := LoadVault(dataDir, VaultCommons, "store_commons_zbs"); err == nil {
		t.Fatal("local config must never open a commons vault")
	}
	otherDir := t.TempDir()
	if err := os.Chmod(otherDir, 0700); err != nil {
		t.Fatal(err)
	}
	other, err := LoadVault(otherDir, VaultPersonal, "store_personal_nik")
	if err != nil {
		t.Fatal(err)
	}
	if other.IPCSecret == cfg.IPCSecret || other.DBPath == cfg.DBPath {
		t.Fatal("separate local vault processes reused a secret or database")
	}
}
