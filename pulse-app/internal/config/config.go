package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nkkmnk/pulse/internal/platform"
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
	// InspectPrivateDirectory is the cross-platform ownership boundary. POSIX
	// proves the effective UID and mode; Windows proves the current-user owner,
	// protected DACL, and absence of reparse points. Requiring a numeric UID here
	// made every native Windows Personal/Desk vault fail before secret creation.
	exists, err := platform.InspectPrivateDirectory(path, true)
	if err != nil {
		return errors.New("team data directory must be an owner-only 0700 directory")
	}
	if exists {
		return nil
	}
	if err := platform.EnsurePrivateDirectory(path); err != nil {
		return fmt.Errorf("create team data directory: %w", err)
	}
	return nil
}

func loadOrCreateTeamSecret(path string) (string, error) {
	secret, err := readStrictTeamSecret(path)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate team IPC secret: %w", err)
	}
	encoded := hex.EncodeToString(random[:])
	_, err = platform.CreatePrivateFileExclusive(path, []byte(encoded))
	if errors.Is(err, os.ErrExist) {
		return readStrictTeamSecret(path)
	}
	if err != nil {
		return "", fmt.Errorf("create team IPC secret: %w", err)
	}
	return readStrictTeamSecret(path)
}

func readStrictTeamSecret(path string) (string, error) {
	data, err := platform.ReadPrivateFile(path, platform.FilePolicy{
		MinimumBytes: 64, MaximumBytes: 64, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", errors.New("team IPC secret must be an owner-only 0600 regular file")
	}
	if len(data) != 64 || !isLowerHex(data) {
		return "", errors.New("team IPC secret must contain exactly 32 random bytes as lowercase hex")
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
	if !isCanonicalHTTPSIssuer(root.Issuer) {
		return nil, fmt.Errorf("PULSE_TEAM_BOOTSTRAP_ISSUER must be an exact canonical HTTPS issuer URL")
	}
	return &root, nil
}

func isCanonicalHTTPSIssuer(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Host == "" || parsed.Path == "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.String() != value {
		return false
	}
	hostname := parsed.Hostname()
	if hostname == "" || hostname != strings.ToLower(hostname) || strings.HasSuffix(parsed.Host, ":") {
		return false
	}
	for _, char := range hostname {
		if char > 127 {
			return false
		}
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 || strconv.Itoa(value) != port || value == 443 {
			return false
		}
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
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
