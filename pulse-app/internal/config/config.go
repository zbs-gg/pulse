package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

type Config struct {
	DataDir             string
	IPCSecret           string // 32-byte hex, used as X-Pulse-Key header value
	AnthropicAPIKey     string
	DBPath              string
	Mode                string
	VaultKind           VaultKind
	StoreID             string
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
	if err := ensurePrivateTeamDataDir(dataDir); err != nil {
		return nil, err
	}
	secret, err := loadOrCreateTeamSecret(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		DataDir: dataDir, IPCSecret: secret, DBPath: filepath.Join(dataDir, "pulse.db"),
		Mode: "host-extracted",
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

func ensurePrivateTeamDataDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0700); err != nil {
			return fmt.Errorf("create team data directory: %w", err)
		}
		if err := os.Chmod(path, 0700); err != nil {
			return fmt.Errorf("secure team data directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect team data directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !ownedByCurrentUser(info) {
		return errors.New("team data directory must be an owner-only 0700 directory")
	}
	return nil
}

func loadOrCreateTeamSecret(path string) (string, error) {
	secret, err := readStrictTeamSecret(path)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOENT) {
		return "", err
	}

	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate team IPC secret: %w", err)
	}
	encoded := hex.EncodeToString(random[:])
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if errors.Is(err, syscall.EEXIST) {
		return readStrictTeamSecret(path)
	}
	if err != nil {
		return "", fmt.Errorf("create team IPC secret: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	removeOnFailure := true
	defer func() {
		_ = file.Close()
		if removeOnFailure {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0600); err != nil {
		return "", fmt.Errorf("secure team IPC secret: %w", err)
	}
	if _, err := io.WriteString(file, encoded); err != nil {
		return "", fmt.Errorf("write team IPC secret: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync team IPC secret: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close team IPC secret: %w", err)
	}
	removeOnFailure = false
	return readStrictTeamSecret(path)
}

func readStrictTeamSecret(path string) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("open team IPC secret: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect team IPC secret: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ownedByCurrentUser(info) {
		return "", errors.New("team IPC secret must be an owner-only 0600 regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil {
		return "", fmt.Errorf("read team IPC secret: %w", err)
	}
	if len(data) != 64 || !isLowerHex(data) {
		return "", errors.New("team IPC secret must contain exactly 32 random bytes as lowercase hex")
	}
	return string(data), nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func isLowerHex(value []byte) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
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
