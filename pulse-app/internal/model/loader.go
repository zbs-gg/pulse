package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// EnvModelsPath overrides the registry config file path.
	EnvModelsPath = "PULSE_MODELS_PATH"
	// DefaultModelsFilename is the default config filename inside dataDir.
	DefaultModelsFilename = "models.json"
)

// LoadRegistryFromBytes parses JSON registry config and constructs a Registry.
// Empty input falls back to DefaultRegistryConfig().
// Unknown fields are rejected to prevent silently accepting tampered configs.
func LoadRegistryFromBytes(data []byte) (*Registry, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return NewRegistry(DefaultRegistryConfig())
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg RegistryConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse registry config: %w", err)
	}
	return NewRegistry(cfg)
}

// LoadRegistryFromFile reads JSON registry config from path.
// If the file does not exist, returns DefaultRegistryConfig().
func LoadRegistryFromFile(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NewRegistry(DefaultRegistryConfig())
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return LoadRegistryFromBytes(data)
}

// LoadRegistry resolves the registry path with this precedence:
//  1. PULSE_MODELS_PATH env var if set
//  2. dataDir/models.json
//  3. DefaultRegistryConfig()
func LoadRegistry(dataDir string) (*Registry, error) {
	if envPath := os.Getenv(EnvModelsPath); envPath != "" {
		return LoadRegistryFromFile(envPath)
	}
	if dataDir == "" {
		return NewRegistry(DefaultRegistryConfig())
	}
	return LoadRegistryFromFile(filepath.Join(dataDir, DefaultModelsFilename))
}
