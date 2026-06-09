package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	DataDir         string
	IPCSecret       string // 32-byte hex, used as X-Pulse-Key header value
	AnthropicAPIKey string
	DBPath          string
	Mode            string
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
