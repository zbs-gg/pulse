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

func TestLoadReadsLocalAutoModeMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("local-auto\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Mode != "local-auto" {
		t.Fatalf("expected local-auto mode, got %q", cfg.Mode)
	}
}

func TestLoadIgnoresTeamOnlyEnvironment(t *testing.T) {
	tests := []struct {
		name        string
		issuer      string
		subject     string
		adminClient string
		storeID     string
		teamID      string
	}{
		{
			name:        "complete values",
			issuer:      "https://issuer.example",
			subject:     "owner-subject",
			adminClient: "admin-client",
			storeID:     "store_123",
			teamID:      "team_123",
		},
		{
			name:    "malformed partial values",
			issuer:  " https://issuer.example",
			storeID: "store_123 ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PULSE_TEAM_BOOTSTRAP_ISSUER", tt.issuer)
			t.Setenv("PULSE_TEAM_BOOTSTRAP_SUBJECT", tt.subject)
			t.Setenv("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", tt.adminClient)
			t.Setenv("PULSE_TEAM_EXPECTED_STORE_ID", tt.storeID)
			t.Setenv("PULSE_TEAM_EXPECTED_TEAM_ID", tt.teamID)

			cfg, err := Load(t.TempDir())
			if err != nil {
				t.Fatalf("local Load must ignore team-only environment: %v", err)
			}
			if cfg.TeamBootstrapRoot != nil || cfg.ExpectedTeamStoreID != "" || cfg.ExpectedTeamID != "" {
				t.Fatalf("local Load leaked team-only configuration: %+v", cfg)
			}
		})
	}
}

func TestLoadTeamBootstrapRootIsAllOrNothing(t *testing.T) {
	t.Setenv("PULSE_TEAM_BOOTSTRAP_ISSUER", "https://issuer.example")
	t.Setenv("PULSE_TEAM_BOOTSTRAP_SUBJECT", "")
	t.Setenv("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", "")
	if _, err := LoadTeam(t.TempDir()); err == nil {
		t.Fatal("partial team bootstrap root should fail")
	}

	t.Setenv("PULSE_TEAM_BOOTSTRAP_SUBJECT", "owner-subject")
	t.Setenv("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", "admin-client")
	cfg, err := LoadTeam(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TeamBootstrapRoot == nil || cfg.TeamBootstrapRoot.Subject != "owner-subject" {
		t.Fatalf("team bootstrap root = %+v", cfg.TeamBootstrapRoot)
	}
}

func TestLoadExpectedTeamIdentityIsAllOrNothing(t *testing.T) {
	t.Setenv("PULSE_TEAM_EXPECTED_STORE_ID", "store_123")
	t.Setenv("PULSE_TEAM_EXPECTED_TEAM_ID", "")
	if _, err := LoadTeam(t.TempDir()); err == nil {
		t.Fatal("partial expected team identity should fail")
	}

	t.Setenv("PULSE_TEAM_EXPECTED_TEAM_ID", "team_123")
	cfg, err := LoadTeam(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExpectedTeamStoreID != "store_123" || cfg.ExpectedTeamID != "team_123" {
		t.Fatalf("expected team identity not loaded: %+v", cfg)
	}
}

func TestLoadTeamPreservesBootstrapIdentityExactly(t *testing.T) {
	t.Setenv("PULSE_TEAM_BOOTSTRAP_ISSUER", "https://Issuer.Example/OIDC")
	t.Setenv("PULSE_TEAM_BOOTSTRAP_SUBJECT", "Owner Subject")
	t.Setenv("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", "Admin/Client")

	cfg, err := LoadTeam(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wantIssuer := "https://Issuer.Example/OIDC"
	wantSubject := "Owner Subject"
	wantClient := "Admin/Client"
	if cfg.TeamBootstrapRoot == nil ||
		cfg.TeamBootstrapRoot.Issuer != wantIssuer ||
		cfg.TeamBootstrapRoot.Subject != wantSubject ||
		cfg.TeamBootstrapRoot.AdminClientID != wantClient {
		t.Fatalf("bootstrap identity was normalized: %+v", cfg.TeamBootstrapRoot)
	}
}

func TestLoadTeamRejectsSurroundingWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "issuer", env: "PULSE_TEAM_BOOTSTRAP_ISSUER", value: " https://issuer.example"},
		{name: "subject", env: "PULSE_TEAM_BOOTSTRAP_SUBJECT", value: "owner-subject\n"},
		{name: "admin client", env: "PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", value: "admin-client "},
		{name: "store ID", env: "PULSE_TEAM_EXPECTED_STORE_ID", value: " store_123"},
		{name: "team ID", env: "PULSE_TEAM_EXPECTED_TEAM_ID", value: "team_123\t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PULSE_TEAM_BOOTSTRAP_ISSUER", "https://issuer.example")
			t.Setenv("PULSE_TEAM_BOOTSTRAP_SUBJECT", "owner-subject")
			t.Setenv("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", "admin-client")
			t.Setenv("PULSE_TEAM_EXPECTED_STORE_ID", "store_123")
			t.Setenv("PULSE_TEAM_EXPECTED_TEAM_ID", "team_123")
			t.Setenv(tt.env, tt.value)

			if _, err := LoadTeam(t.TempDir()); err == nil {
				t.Fatalf("LoadTeam accepted surrounding whitespace in %s", tt.env)
			}
		})
	}
}
