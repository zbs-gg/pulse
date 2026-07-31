package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nkkmnk/pulse/internal/platform"
)

type Config struct {
	DataDir         string
	IPCSecret       string // 32-byte hex, used as X-Pulse-Key header value
	AnthropicAPIKey string
	DBPath          string
	Mode            string
	VaultKind       VaultKind
	StoreID         string
}

// Load reads config from dataDir. Generates secret.key if missing.
// ANTHROPIC_API_KEY is optional in host-extracted memory mode.
func Load(dataDir string) (*Config, error) {
	if err := platform.EnsurePrivateDirectory(dataDir); err != nil {
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

func ensurePrivateDataDir(path string) error {
	// InspectPrivateDirectory is the cross-platform ownership boundary. POSIX
	// proves the effective UID and mode; Windows proves the current-user owner,
	// protected DACL, and absence of reparse points. Requiring a numeric UID here
	// made every native Windows Personal vault fail before secret creation.
	exists, err := platform.InspectPrivateDirectory(path, true)
	if err != nil {
		return errors.New("data directory must be private and owned by the current user")
	}
	if exists {
		return nil
	}
	if err := platform.EnsurePrivateDirectory(path); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	return nil
}

func loadOrCreateVaultSecret(path string) (string, error) {
	secret, err := readStrictVaultSecret(path)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate IPC secret: %w", err)
	}
	encoded := hex.EncodeToString(random[:])
	_, err = platform.CreatePrivateFileExclusive(path, []byte(encoded))
	if errors.Is(err, os.ErrExist) {
		return readStrictVaultSecret(path)
	}
	if err != nil {
		return "", fmt.Errorf("create IPC secret: %w", err)
	}
	return readStrictVaultSecret(path)
}

func readStrictVaultSecret(path string) (string, error) {
	data, err := platform.ReadPrivateFile(path, platform.FilePolicy{
		MinimumBytes: 64, MaximumBytes: 64, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", errors.New("IPC secret must be an owner-only regular file")
	}
	if len(data) != 64 || !isLowerHex(data) {
		return "", errors.New("IPC secret must contain exactly 32 random bytes as lowercase hex")
	}
	return string(data), nil
}

func isLowerHex(value []byte) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func loadMode(dataDir string) string {
	mode := strings.TrimSpace(os.Getenv("PULSE_MODE"))
	if mode == "" {
		if data, err := platform.ReadPrivateFile(filepath.Join(dataDir, "mode"), platform.FilePolicy{
			MaximumBytes: 64, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
		}); err == nil {
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
	data, err := platform.ReadPrivateFile(path, platform.FilePolicy{
		MinimumBytes: 64, MaximumBytes: 64, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	})
	if err == nil {
		if !isLowerHex(data) {
			return "", errors.New("personal IPC secret must contain exactly 32 random bytes as lowercase hex")
		}
		return string(data), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	hx := hex.EncodeToString(buf)
	if _, err := platform.CreatePrivateFileExclusive(path, []byte(hx)); errors.Is(err, os.ErrExist) {
		return loadOrCreateSecret(path)
	} else if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return hx, nil
}
