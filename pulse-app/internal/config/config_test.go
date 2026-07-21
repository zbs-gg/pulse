package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func teamTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

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
	t.Setenv("PULSE_TEAM_BOOTSTRAP_ISSUER", "https://issuer.example/")
	t.Setenv("PULSE_TEAM_BOOTSTRAP_SUBJECT", "")
	t.Setenv("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", "")
	if _, err := LoadTeam(teamTempDir(t)); err == nil {
		t.Fatal("partial team bootstrap root should fail")
	}

	t.Setenv("PULSE_TEAM_BOOTSTRAP_SUBJECT", "owner-subject")
	t.Setenv("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", "admin-client")
	cfg, err := LoadTeam(teamTempDir(t))
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
	if _, err := LoadTeam(teamTempDir(t)); err == nil {
		t.Fatal("partial expected team identity should fail")
	}

	t.Setenv("PULSE_TEAM_EXPECTED_TEAM_ID", "team_123")
	cfg, err := LoadTeam(teamTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExpectedTeamStoreID != "store_123" || cfg.ExpectedTeamID != "team_123" {
		t.Fatalf("expected team identity not loaded: %+v", cfg)
	}
}

func TestLoadTeamPreservesCanonicalBootstrapIdentityExactly(t *testing.T) {
	t.Setenv("PULSE_TEAM_BOOTSTRAP_ISSUER", "https://issuer.example/OIDC")
	t.Setenv("PULSE_TEAM_BOOTSTRAP_SUBJECT", "Owner Subject")
	t.Setenv("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", "Admin/Client")

	cfg, err := LoadTeam(teamTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	wantIssuer := "https://issuer.example/OIDC"
	wantSubject := "Owner Subject"
	wantClient := "Admin/Client"
	if cfg.TeamBootstrapRoot == nil ||
		cfg.TeamBootstrapRoot.Issuer != wantIssuer ||
		cfg.TeamBootstrapRoot.Subject != wantSubject ||
		cfg.TeamBootstrapRoot.AdminClientID != wantClient {
		t.Fatalf("bootstrap identity was normalized: %+v", cfg.TeamBootstrapRoot)
	}
}

func TestLoadTeamRejectsNoncanonicalOrInsecureBootstrapIssuer(t *testing.T) {
	issuers := []string{
		"http://issuer.example/",
		"https://Issuer.Example/OIDC",
		"https://issuer.example",
		"https://issuer.example:443/",
		"https://user@issuer.example/",
		"https://issuer.example/?tenant=one",
		"https://issuer.example/#fragment",
		"https://issuer.example/a/../b",
	}
	for _, issuer := range issuers {
		t.Run(issuer, func(t *testing.T) {
			t.Setenv("PULSE_TEAM_BOOTSTRAP_ISSUER", issuer)
			t.Setenv("PULSE_TEAM_BOOTSTRAP_SUBJECT", "owner-subject")
			t.Setenv("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", "admin-client")
			if _, err := LoadTeam(teamTempDir(t)); err == nil {
				t.Fatalf("LoadTeam accepted noncanonical issuer %q", issuer)
			}
		})
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
			t.Setenv("PULSE_TEAM_BOOTSTRAP_ISSUER", "https://issuer.example/")
			t.Setenv("PULSE_TEAM_BOOTSTRAP_SUBJECT", "owner-subject")
			t.Setenv("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID", "admin-client")
			t.Setenv("PULSE_TEAM_EXPECTED_STORE_ID", "store_123")
			t.Setenv("PULSE_TEAM_EXPECTED_TEAM_ID", "team_123")
			t.Setenv(tt.env, tt.value)

			if _, err := LoadTeam(teamTempDir(t)); err == nil {
				t.Fatalf("LoadTeam accepted surrounding whitespace in %s", tt.env)
			}
		})
	}
}

func TestLoadTeamRequiresStrictIPCSecretAndPrivateDataDirectory(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	t.Run("creates an exact private secret", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadTeam(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.IPCSecret) != 64 {
			t.Fatalf("secret length = %d", len(cfg.IPCSecret))
		}
		info, err := os.Lstat(filepath.Join(dir, "secret.key"))
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatalf("secret mode = %v", info.Mode())
		}
	})

	for _, tc := range []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "short", content: "tiny", mode: 0600},
		{name: "uppercase", content: strings.ToUpper(valid), mode: 0600},
		{name: "group readable", content: valid, mode: 0640},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte(tc.content), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(dir, "secret.key"), tc.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadTeam(dir); err == nil {
				t.Fatal("LoadTeam accepted an unsafe secret")
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.key")
		if err := os.WriteFile(target, []byte(valid), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "secret.key")); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTeam(dir); err == nil {
			t.Fatal("LoadTeam followed a secret symlink")
		}
	})

	t.Run("permissive data directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTeam(dir); err == nil {
			t.Fatal("LoadTeam accepted a group/world accessible data directory")
		}
	})
}
