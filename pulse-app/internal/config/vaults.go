package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

type VaultKind string

const (
	VaultPersonal VaultKind = "personal"
	VaultDesk     VaultKind = "desk"
	VaultCommons  VaultKind = "commons"
)

var vaultIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,127}$`)

type VaultSpec struct {
	Kind          VaultKind `json:"kind"`
	StoreID       string    `json:"store_id"`
	TeamID        string    `json:"team_id,omitempty"`
	PrincipalID   string    `json:"principal_id,omitempty"`
	DataDir       string    `json:"data_dir,omitempty"`
	CacheDir      string    `json:"cache_dir,omitempty"`
	ListenAddr    string    `json:"listen_addr,omitempty"`
	Resource      string    `json:"resource,omitempty"`
	CredentialRef string    `json:"credential_ref"`
}

type VaultTopology struct {
	Mode     string     `json:"mode"`
	Personal *VaultSpec `json:"personal,omitempty"`
	Desk     *VaultSpec `json:"desk,omitempty"`
	Commons  *VaultSpec `json:"commons,omitempty"`
	Fallback bool       `json:"fallback"`
}

type TeamTopologyRequest struct {
	RootDir           string
	PrincipalID       string
	TeamID            string
	DeskStoreID       string
	DeskAddr          string
	DeskCredential    string
	CommonsStoreID    string
	CommonsResource   string
	CommonsCredential string
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

func NewTeamTopology(request TeamTopologyRequest) (VaultTopology, error) {
	root, err := canonicalRoot(request.RootDir)
	if err != nil {
		return VaultTopology{}, err
	}
	topology := VaultTopology{
		Mode: "team",
		Desk: &VaultSpec{
			Kind: VaultDesk, StoreID: request.DeskStoreID, TeamID: request.TeamID,
			PrincipalID: request.PrincipalID,
			DataDir:     filepath.Join(root, "vaults", "desks", request.TeamID, request.PrincipalID),
			CacheDir:    filepath.Join(root, "caches", "desks", request.TeamID, request.PrincipalID),
			ListenAddr:  request.DeskAddr, CredentialRef: request.DeskCredential,
		},
		Commons: &VaultSpec{
			Kind: VaultCommons, StoreID: request.CommonsStoreID, TeamID: request.TeamID,
			PrincipalID: request.PrincipalID, Resource: request.CommonsResource,
			CacheDir:      "commons:" + request.TeamID + ":" + request.PrincipalID,
			CredentialRef: request.CommonsCredential,
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
	switch topology.Mode {
	case "personal":
		if topology.Personal == nil || topology.Desk != nil || topology.Commons != nil {
			return errors.New("personal topology must contain only one personal vault")
		}
		return validateLocalVault(*topology.Personal, VaultPersonal)
	case "team":
		if topology.Personal != nil || topology.Desk == nil || topology.Commons == nil {
			return errors.New("team topology must contain one desk and one commons")
		}
		if err := validateLocalVault(*topology.Desk, VaultDesk); err != nil {
			return err
		}
		if err := validateCommonsVault(*topology.Commons); err != nil {
			return err
		}
		if topology.Desk.StoreID == topology.Commons.StoreID ||
			topology.Desk.CredentialRef == topology.Commons.CredentialRef ||
			topology.Desk.CacheDir == topology.Commons.CacheDir {
			return errors.New("desk and commons identities, credentials, and caches must be distinct")
		}
		if topology.Desk.TeamID != topology.Commons.TeamID || topology.Desk.PrincipalID != topology.Commons.PrincipalID {
			return errors.New("desk and commons must be pinned to the same team and principal")
		}
		return nil
	default:
		return errors.New("vault topology mode must be personal or team")
	}
}

func validateLocalVault(spec VaultSpec, expected VaultKind) error {
	if spec.Kind != expected || !validVaultIdentifier(spec.StoreID) || spec.DataDir == "" || spec.CacheDir == "" ||
		spec.DataDir == spec.CacheDir || spec.Resource != "" || !validCredentialRef(spec.CredentialRef) {
		return fmt.Errorf("invalid %s vault", expected)
	}
	if !isLoopbackAddr(spec.ListenAddr) {
		return fmt.Errorf("%s vault must listen on loopback", expected)
	}
	if expected == VaultDesk && (!validVaultIdentifier(spec.TeamID) || !validVaultIdentifier(spec.PrincipalID)) {
		return errors.New("desk vault requires fixed team and principal identities")
	}
	return nil
}

func validateCommonsVault(spec VaultSpec) error {
	if spec.Kind != VaultCommons || !validVaultIdentifier(spec.StoreID) || !validVaultIdentifier(spec.TeamID) ||
		!validVaultIdentifier(spec.PrincipalID) || spec.DataDir != "" || spec.ListenAddr != "" ||
		!validCredentialRef(spec.CredentialRef) || spec.CacheDir == "" {
		return errors.New("invalid commons vault")
	}
	parsed, err := url.Parse(spec.Resource)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		strings.EqualFold(parsed.Hostname(), "localhost") || isLoopbackHost(parsed.Hostname()) {
		return errors.New("commons must use a dedicated HTTPS resource")
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

// LoadVault opens configuration for a product Personal or Desk process. It
// never accepts Commons: remote Commons has its own dedicated runtime.
func LoadVault(dataDir string, kind VaultKind, storeID string) (*Config, error) {
	if kind != VaultPersonal && kind != VaultDesk {
		return nil, errors.New("local vault kind must be personal or desk")
	}
	if !validVaultIdentifier(storeID) {
		return nil, errors.New("local vault store ID is invalid")
	}
	if err := ensurePrivateTeamDataDir(dataDir); err != nil {
		return nil, err
	}
	secret, err := loadOrCreateTeamSecret(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return nil, err
	}
	return &Config{
		DataDir: dataDir, IPCSecret: secret, AnthropicAPIKey: "",
		DBPath: filepath.Join(dataDir, "pulse.db"), Mode: loadMode(dataDir),
		VaultKind: kind, StoreID: storeID,
	}, nil
}
