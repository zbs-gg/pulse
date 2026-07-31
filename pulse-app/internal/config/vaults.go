package config

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
)

type VaultKind string

const VaultPersonal VaultKind = "personal"

var vaultIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,127}$`)

type VaultSpec struct {
	Kind          VaultKind `json:"kind"`
	StoreID       string    `json:"store_id"`
	DataDir       string    `json:"data_dir,omitempty"`
	CacheDir      string    `json:"cache_dir,omitempty"`
	ListenAddr    string    `json:"listen_addr,omitempty"`
	CredentialRef string    `json:"credential_ref"`
}

type VaultTopology struct {
	Mode     string     `json:"mode"`
	Personal *VaultSpec `json:"personal,omitempty"`
	Fallback bool       `json:"fallback"`
}

func NewPersonalTopology(rootDir, storeID, addr, credentialRef string) (VaultTopology, error) {
	root, err := canonicalRoot(rootDir)
	if err != nil {
		return VaultTopology{}, err
	}
	topology := VaultTopology{
		Mode: "personal",
		Personal: &VaultSpec{
			Kind: VaultPersonal, StoreID: storeID,
			DataDir:    filepath.Join(root, "vaults", "personal", storeID),
			CacheDir:   filepath.Join(root, "caches", "personal", storeID),
			ListenAddr: addr, CredentialRef: credentialRef,
		},
		Fallback: false,
	}
	if err := topology.Validate(); err != nil {
		return VaultTopology{}, err
	}
	return topology, nil
}

func (topology VaultTopology) Validate() error {
	if topology.Fallback {
		return errors.New("vault topology cannot enable fallback")
	}
	if topology.Mode != "personal" || topology.Personal == nil {
		return errors.New("Personal topology must contain exactly one local vault")
	}
	return validateLocalVault(*topology.Personal, VaultPersonal)
}

func validateLocalVault(spec VaultSpec, expected VaultKind) error {
	if spec.Kind != expected || !validVaultIdentifier(spec.StoreID) || spec.DataDir == "" || spec.CacheDir == "" ||
		spec.DataDir == spec.CacheDir || !validCredentialRef(spec.CredentialRef) {
		return fmt.Errorf("invalid %s vault", expected)
	}
	if !isLoopbackAddr(spec.ListenAddr) {
		return fmt.Errorf("%s vault must listen on loopback", expected)
	}
	return nil
}

func validVaultIdentifier(value string) bool {
	return vaultIdentifierPattern.MatchString(value)
}

func validCredentialRef(value string) bool {
	return strings.HasPrefix(value, "keychain:") && len(value) > len("keychain:") && strings.TrimSpace(value) == value
}

func isLoopbackAddr(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return false
	}
	return isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func canonicalRoot(value string) (string, error) {
	if value == "" {
		return "", errors.New("vault root is required")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve vault root: %w", err)
	}
	return filepath.Clean(abs), nil
}

// LoadVault opens configuration for one local Personal process.
func LoadVault(dataDir string, kind VaultKind, storeID string) (*Config, error) {
	if kind != VaultPersonal {
		return nil, errors.New("Pulse Personal accepts only a personal vault")
	}
	if !validVaultIdentifier(storeID) {
		return nil, errors.New("local vault store ID is invalid")
	}
	if err := ensurePrivateDataDir(dataDir); err != nil {
		return nil, err
	}
	secret, err := loadOrCreateVaultSecret(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return nil, err
	}
	return &Config{
		DataDir: dataDir, IPCSecret: secret, AnthropicAPIKey: "",
		DBPath: filepath.Join(dataDir, "pulse.db"), Mode: loadMode(dataDir),
		VaultKind: kind, StoreID: storeID,
	}, nil
}
