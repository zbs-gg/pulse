package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

type Config struct {
	DataDir             string
	IPCSecret           string // 32-byte hex, used as X-Pulse-Key header value
	AnthropicAPIKey     string
	DBPath              string
	Mode                string
	TeamBootstrapRoot   *teamauth.BootstrapRoot
	ExpectedTeamStoreID string
	ExpectedTeamID      string
}

// Load reads config from dataDir. Generates secret.key if missing.
// ANTHROPIC_API_KEY is optional in host-extracted memory mode.
func Load(dataDir string) (*Config, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dataDir, err)
	}

	secret, err := loadOrCreateSecret(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		DataDir:         dataDir,
		IPCSecret:       secret,
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		DBPath:          filepath.Join(dataDir, "pulse.db"),
		Mode:            loadMode(dataDir),
	}
	return cfg, nil
}

// LoadTeam reads configuration for the authenticated team runtime.
func LoadTeam(dataDir string) (*Config, error) {
	cfg, err := Load(dataDir)
	if err != nil {
		return nil, err
	}

	bootstrapRoot, err := loadTeamBootstrapRoot()
	if err != nil {
		return nil, err
	}
	expectedStoreID, expectedTeamID, err := loadExpectedTeamIdentity()
	if err != nil {
		return nil, err
	}

	cfg.TeamBootstrapRoot = bootstrapRoot
	cfg.ExpectedTeamStoreID = expectedStoreID
	cfg.ExpectedTeamID = expectedTeamID
	return cfg, nil
}

func loadTeamBootstrapRoot() (*teamauth.BootstrapRoot, error) {
	issuer, err := loadExactTeamValue("PULSE_TEAM_BOOTSTRAP_ISSUER")
	if err != nil {
		return nil, err
	}
	subject, err := loadExactTeamValue("PULSE_TEAM_BOOTSTRAP_SUBJECT")
	if err != nil {
		return nil, err
	}
	adminClientID, err := loadExactTeamValue("PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID")
	if err != nil {
		return nil, err
	}

	root := teamauth.BootstrapRoot{
		Issuer:        issuer,
		Subject:       subject,
		AdminClientID: adminClientID,
	}
	set := 0
	for _, value := range []string{root.Issuer, root.Subject, root.AdminClientID} {
		if value != "" {
			set++
		}
	}
	if set == 0 {
		return nil, nil
	}
	if set != 3 {
		return nil, fmt.Errorf("team bootstrap root must set issuer, subject, and admin client binding together")
	}
	return &root, nil
}

func loadExpectedTeamIdentity() (string, string, error) {
	storeID, err := loadExactTeamValue("PULSE_TEAM_EXPECTED_STORE_ID")
	if err != nil {
		return "", "", err
	}
	teamID, err := loadExactTeamValue("PULSE_TEAM_EXPECTED_TEAM_ID")
	if err != nil {
		return "", "", err
	}
	if (storeID == "") != (teamID == "") {
		return "", "", fmt.Errorf("expected team store ID and team ID must be set together")
	}
	return storeID, teamID, nil
}

func loadExactTeamValue(name string) (string, error) {
	value := os.Getenv(name)
	if strings.TrimSpace(value) != value {
		return "", fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	return value, nil
}

func loadMode(dataDir string) string {
	mode := strings.TrimSpace(os.Getenv("PULSE_MODE"))
	if mode == "" {
		if data, err := os.ReadFile(filepath.Join(dataDir, "mode")); err == nil {
			mode = strings.TrimSpace(string(data))
		}
	}
	switch mode {
	case "local-auto":
		return "local-auto"
	default:
		return "host-extracted"
	}
}

func loadOrCreateSecret(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	hx := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(hx), 0600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return hx, nil
}
