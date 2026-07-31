//go:build windows

// pulse-platform-adapter is the npm-bundled, pre-download Windows trust
// adapter. It deliberately exposes a small JSON protocol instead of a shell
// surface so the Node installer never falls back to PowerShell or icacls.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/platform"
)

const (
	requestSchema  = "pulse.windows_bootstrap_adapter.request.v1"
	responseSchema = "pulse.windows_bootstrap_adapter.response.v1"
	contractSchema = "pulse.windows_bootstrap_adapter.contract.v1"
	lockSchema     = "pulse.windows_bootstrap_lock.v1"
	maximumInput   = 64 * 1024 * 1024
)

var operations = []string{
	"acquire_private_lock", "atomic_write_private_file", "ensure_private_directory",
	"batch",
	"digest_private_tree", "inspect_executable", "inspect_path_identity", "inspect_private_state", "inspect_private_tree", "inspect_process",
	"read_integrity_file", "read_private_file", "release_private_lock", "remove_private_file",
	"terminate_process",
}

func adapterTarget(architecture string) string {
	switch architecture {
	case "amd64":
		return "win32-x64"
	case "arm64":
		return "win32-arm64"
	default:
		return ""
	}
}

type request struct {
	Schema            string             `json:"schema"`
	Path              string             `json:"path,omitempty"`
	Kind              string             `json:"kind,omitempty"`
	Owner             string             `json:"owner,omitempty"`
	Bytes             []byte             `json:"bytes_base64,omitempty"`
	Encoding          string             `json:"encoding,omitempty"`
	EnsureParent      bool               `json:"ensure_parent,omitempty"`
	Missing           bool               `json:"missing,omitempty"`
	MinimumBytes      int64              `json:"minimum_bytes,omitempty"`
	MaximumBytes      int64              `json:"maximum_bytes,omitempty"`
	PID               int                `json:"pid,omitempty"`
	IdentityToken     string             `json:"identity_token,omitempty"`
	StaleAfterMS      int64              `json:"stale_after_ms,omitempty"`
	TimeoutMS         int64              `json:"timeout_ms,omitempty"`
	Lease             string             `json:"lease,omitempty"`
	Entries           []privateTreeEntry `json:"entries,omitempty"`
	MaximumDepth      int                `json:"maximum_depth,omitempty"`
	MaximumEntries    int                `json:"maximum_entries,omitempty"`
	MaximumTotalBytes int64              `json:"maximum_total_bytes,omitempty"`
	ExcludeRootFile   string             `json:"exclude_root_file,omitempty"`
	Requests          []batchOperation   `json:"requests,omitempty"`
}

type batchOperation struct {
	Operation string  `json:"operation"`
	Request   request `json:"request"`
}

var batchOperations = map[string]bool{
	"contract":              true,
	"digest_private_tree":   true,
	"inspect_executable":    true,
	"inspect_path_identity": true,
	"inspect_private_state": true,
	"inspect_private_tree":  true,
	"inspect_process":       true,
	"read_integrity_file":   true,
	"read_private_file":     true,
}

type privateTreeEntry struct {
	Path       string `json:"path"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
	Executable bool   `json:"executable"`
}

type response struct {
	Schema string `json:"schema"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type lockRecord struct {
	Schema          string `json:"schema"`
	CreatedAt       string `json:"created_at"`
	PID             int    `json:"pid"`
	ProcessIdentity string `json:"process_identity"`
	Token           string `json:"token"`
}

func main() {
	if len(os.Args) != 2 {
		writeResponse(response{Schema: responseSchema, OK: false, Error: "operation_required"})
		os.Exit(2)
	}
	if os.Args[1] == "contract" {
		writeResponse(response{Schema: responseSchema, OK: true, Result: contract()})
		return
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, maximumInput+1))
	if err != nil || len(input) > maximumInput {
		abort("request_invalid")
	}
	var value request
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || value.Schema != requestSchema {
		abort("request_invalid")
	}
	result, err := dispatch(os.Args[1], value)
	if err != nil {
		abort(errorCode(err))
	}
	writeResponse(response{Schema: responseSchema, OK: true, Result: result})
}

func dispatch(operation string, value request) (any, error) {
	switch operation {
	case "contract":
		return contract(), nil
	case "batch":
		if len(value.Requests) < 1 || len(value.Requests) > 16 {
			return nil, fmt.Errorf("%w: batch_size", platform.ErrUnsafe)
		}
		for _, item := range value.Requests {
			if !batchOperations[item.Operation] || item.Request.Schema != "" || len(item.Request.Requests) != 0 {
				return nil, fmt.Errorf("%w: batch_operation", platform.ErrUnsafe)
			}
		}
		results := make([]any, 0, len(value.Requests))
		for _, item := range value.Requests {
			result, err := dispatch(item.Operation, item.Request)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		}
		return map[string]any{"results": results}, nil
	case "digest_private_tree":
		return digestPrivateTree(value)
	case "inspect_executable":
		data, _, err := platform.ReadPrivateFileWithInfo(value.Path, platform.FilePolicy{
			MaximumBytes: 512 * 1024 * 1024, NoUntrustedWrite: true, SingleLink: true, Executable: true,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"canonical_path": canonicalPath(value.Path), "executable": true,
			"owner_only": true, "regular_file": true, "reparse_point": false,
			"sha256": fmt.Sprintf("%x", sha256.Sum256(data))}, nil
	case "inspect_private_state":
		return inspectPrivateState(value.Path, value.Kind)
	case "inspect_path_identity":
		directory, err := pathKind(value.Kind)
		if err != nil {
			return nil, err
		}
		info, err := platform.InspectWindowsPathIdentity(value.Path, directory)
		if err != nil {
			return nil, err
		}
		return map[string]any{"canonical_path": canonicalPath(value.Path), "identity_token": info.Identity,
			"kind": value.Kind, "reparse_point": false}, nil
	case "inspect_private_tree":
		return inspectPrivateTree(value)
	case "read_integrity_file":
		return readIntegrityFile(value)
	case "ensure_private_directory":
		if err := platform.EnsurePrivateDirectory(value.Path); err != nil {
			return nil, err
		}
		return map[string]any{"ready": true}, nil
	case "read_private_file":
		data, err := platform.ReadPrivateFile(value.Path, platform.FilePolicy{
			MinimumBytes: value.MinimumBytes, MaximumBytes: value.MaximumBytes,
			RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"bytes_base64": data}, nil
	case "atomic_write_private_file":
		if value.MaximumBytes < 0 || int64(len(value.Bytes)) > value.MaximumBytes {
			return nil, platform.ErrUnsafe
		}
		parent := filepath.Dir(value.Path)
		if value.EnsureParent {
			if err := platform.EnsurePrivateDirectory(parent); err != nil {
				return nil, err
			}
		} else if _, err := platform.InspectPrivateDirectory(parent, false); err != nil {
			return nil, err
		}
		if err := platform.AtomicWritePrivateFile(value.Path, value.Bytes); err != nil {
			return nil, err
		}
		return map[string]any{"written": true}, nil
	case "remove_private_file":
		info, err := platform.InspectPrivateFile(value.Path, platform.FilePolicy{
			RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
		})
		if err != nil {
			if value.Missing && errors.Is(err, os.ErrNotExist) {
				return map[string]any{"removed": false}, nil
			}
			return nil, err
		}
		if err := platform.RemovePrivateFileIfIdentity(value.Path, info.Identity); err != nil {
			return nil, err
		}
		return map[string]any{"removed": true}, nil
	case "inspect_process":
		return inspectProcess(value.PID)
	case "terminate_process":
		terminated, err := platform.TerminateWindowsProcess(value.PID, value.IdentityToken)
		if err != nil {
			return nil, err
		}
		return map[string]any{"terminated": terminated}, nil
	case "acquire_private_lock":
		return acquireLock(value)
	case "release_private_lock":
		return releaseLock(value)
	default:
		return nil, fmt.Errorf("%w: operation", platform.ErrUnsupported)
	}
}

func contract() map[string]any {
	return map[string]any{
		"operations": operations, "schema": contractSchema,
		"target": adapterTarget(runtime.GOARCH), "version": 1,
	}
}

func inspectPrivateState(path, kind string) (any, error) {
	directory, err := pathKind(kind)
	if err != nil {
		return nil, err
	}
	if directory {
		if _, err := platform.InspectPrivateDirectory(path, false); err != nil {
			return nil, err
		}
	} else if _, err := platform.InspectPrivateFile(path, platform.FilePolicy{
		RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	}); err != nil {
		return nil, err
	}
	return map[string]any{"canonical_path": canonicalPath(path), "kind": kind,
		"owner_only": true, "reparse_point": false}, nil
}

func validPrivateTreePath(value string) bool {
	if value == "" || len(value) > 512 || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func digestPrivateTree(value request) (any, error) {
	if value.Path == "" || !filepath.IsAbs(value.Path) ||
		value.MaximumEntries < 1 || value.MaximumEntries > 1_000_000 ||
		value.MaximumDepth < 1 || value.MaximumDepth > 128 ||
		value.MaximumTotalBytes < 1 || value.MaximumTotalBytes > 64*1024*1024*1024 ||
		(value.ExcludeRootFile != "" && (!validPrivateTreePath(value.ExcludeRootFile) ||
			strings.Contains(value.ExcludeRootFile, "/"))) {
		return nil, platform.ErrUnsafe
	}
	if _, err := platform.InspectPrivateDirectory(value.Path, false); err != nil {
		return nil, err
	}
	hash := sha256.New()
	visited := 0
	files := 0
	var actualBytes int64
	err := filepath.Walk(value.Path, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(value.Path, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		visited++
		if visited > value.MaximumEntries || strings.Count(relative, "/") > value.MaximumDepth ||
			info.Mode()&os.ModeSymlink != 0 {
			return platform.ErrUnsafe
		}
		if info.IsDir() {
			_, err := platform.InspectPrivateDirectory(path, false)
			return err
		}
		if !info.Mode().IsRegular() {
			return platform.ErrUnsafe
		}
		remaining := value.MaximumTotalBytes - actualBytes
		if remaining < 0 {
			return platform.ErrUnsafe
		}
		maximumBytes := remaining + 1
		if maximumBytes > 512*1024*1024 {
			maximumBytes = 512 * 1024 * 1024
		}
		data, details, err := platform.ReadPrivateFileWithInfo(path, platform.FilePolicy{
			MaximumBytes: maximumBytes, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
		})
		if err != nil || details.Size != int64(len(data)) {
			return platform.ErrUnsafe
		}
		actualBytes += int64(len(data))
		if actualBytes < 0 || actualBytes > value.MaximumTotalBytes {
			return platform.ErrUnsafe
		}
		files++
		if relative != value.ExcludeRootFile {
			hash.Write([]byte(relative))
			hash.Write([]byte{0})
			hash.Write(data)
			hash.Write([]byte{0})
		}
		return nil
	})
	if err != nil || files < 1 {
		if err != nil {
			return nil, err
		}
		return nil, platform.ErrUnsafe
	}
	return map[string]any{
		"bytes": actualBytes, "files": files, "tree_digest": fmt.Sprintf("%x", hash.Sum(nil)),
	}, nil
}

func inspectPrivateTree(value request) (any, error) {
	if value.Path == "" || !filepath.IsAbs(value.Path) || len(value.Entries) == 0 ||
		value.MaximumEntries < len(value.Entries) || value.MaximumEntries > 1_000_000 ||
		value.MaximumDepth < 1 || value.MaximumDepth > 128 ||
		value.MaximumTotalBytes < 1 || value.MaximumTotalBytes > 64*1024*1024*1024 {
		return nil, platform.ErrUnsafe
	}
	if _, err := platform.InspectPrivateDirectory(value.Path, false); err != nil {
		return nil, err
	}
	expected := make(map[string]privateTreeEntry, len(value.Entries))
	allowedDirectories := map[string]bool{"": true}
	var expectedBytes int64
	for _, entry := range value.Entries {
		decoded, err := hex.DecodeString(entry.SHA256)
		if !validPrivateTreePath(entry.Path) || err != nil || len(decoded) != sha256.Size ||
			entry.SHA256 != strings.ToLower(entry.SHA256) || entry.Bytes < 0 || entry.Bytes > 512*1024*1024 {
			return nil, platform.ErrUnsafe
		}
		if _, exists := expected[entry.Path]; exists {
			return nil, platform.ErrUnsafe
		}
		expected[entry.Path] = entry
		expectedBytes += entry.Bytes
		if expectedBytes < 0 || expectedBytes > value.MaximumTotalBytes {
			return nil, platform.ErrUnsafe
		}
		parts := strings.Split(entry.Path, "/")
		for count := 1; count < len(parts); count++ {
			allowedDirectories[strings.Join(parts[:count], "/")] = true
		}
	}
	seen := make(map[string]bool, len(expected))
	visited := 0
	var actualBytes int64
	err := filepath.Walk(value.Path, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(value.Path, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		visited++
		if visited > value.MaximumEntries || strings.Count(relative, "/") > value.MaximumDepth {
			return platform.ErrUnsafe
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return platform.ErrUnsafe
		}
		if info.IsDir() {
			if !allowedDirectories[relative] {
				return platform.ErrUnsafe
			}
			_, err := platform.InspectPrivateDirectory(path, false)
			return err
		}
		entry, exists := expected[relative]
		if !exists || seen[relative] || !info.Mode().IsRegular() {
			return platform.ErrUnsafe
		}
		maximumBytes := entry.Bytes + 1
		if maximumBytes < 1 {
			maximumBytes = 1
		}
		data, details, err := platform.ReadPrivateFileWithInfo(path, platform.FilePolicy{
			MaximumBytes: maximumBytes, RequireCurrentOwner: true, OwnerOnly: true,
			SingleLink: true, Executable: entry.Executable,
		})
		if err != nil || details.Size != entry.Bytes || int64(len(data)) != entry.Bytes ||
			fmt.Sprintf("%x", sha256.Sum256(data)) != entry.SHA256 {
			return platform.ErrUnsafe
		}
		actualBytes += entry.Bytes
		if actualBytes < 0 || actualBytes > value.MaximumTotalBytes {
			return platform.ErrUnsafe
		}
		seen[relative] = true
		return nil
	})
	if err != nil || len(seen) != len(expected) || actualBytes != expectedBytes {
		if err != nil {
			return nil, err
		}
		return nil, platform.ErrUnsafe
	}
	return map[string]any{"bytes": actualBytes, "files": len(seen)}, nil
}

func readIntegrityFile(value request) (any, error) {
	policy := platform.FilePolicy{MaximumBytes: value.MaximumBytes, NoUntrustedWrite: true, SingleLink: true}
	switch value.Owner {
	case "current":
		policy.RequireCurrentOwner = true
	case "root":
		policy.AllowRootOwner = true
	case "root-or-current":
		policy.RequireCurrentOwner = true
		policy.AllowRootOwner = true
	default:
		return nil, platform.ErrUnsafe
	}
	data, err := platform.ReadPrivateFile(value.Path, policy)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bytes_base64": data, "canonical_path": canonicalPath(value.Path),
		"owner": value.Owner, "regular_file": true, "reparse_point": false}, nil
}

func inspectProcess(pid int) (any, error) {
	identity, err := platform.ProcessIdentity(pid)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{"command": "", "identity_token": nil, "pid": pid, "running": false}, nil
		}
		return nil, err
	}
	command, err := platform.WindowsProcessCommand(pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"command": command, "identity_token": identity, "pid": pid, "running": true}, nil
}

func acquireLock(value request) (any, error) {
	if value.PID < 2 || value.IdentityToken == "" || value.StaleAfterMS < 0 || value.TimeoutMS < 0 || value.TimeoutMS > 60000 {
		return nil, platform.ErrUnsafe
	}
	if current, err := platform.ProcessIdentity(value.PID); err != nil || current != value.IdentityToken {
		return nil, platform.ErrUnsafe
	}
	if err := platform.EnsurePrivateDirectory(filepath.Dir(value.Path)); err != nil {
		return nil, err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	record := lockRecord{Schema: lockSchema, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		PID: value.PID, ProcessIdentity: value.IdentityToken, Token: hex.EncodeToString(random[:])}
	raw, _ := json.Marshal(record)
	raw = append(raw, '\n')
	deadline := time.Now().Add(time.Duration(value.TimeoutMS) * time.Millisecond)
	for {
		if info, err := platform.CreatePrivateFileExclusive(value.Path, raw); err == nil {
			return map[string]any{"identity_token": info.Identity, "lease": record.Token}, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		existing, info, err := readLock(value.Path)
		if err != nil {
			return nil, err
		}
		created, parseErr := time.Parse(time.RFC3339Nano, existing.CreatedAt)
		if parseErr == nil && time.Since(created) >= time.Duration(value.StaleAfterMS)*time.Millisecond &&
			!platform.ProcessAlive(existing.PID, existing.ProcessIdentity) {
			if err := platform.RemovePrivateFileIfIdentity(value.Path, info.Identity); err == nil || errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		if !time.Now().Before(deadline) {
			return nil, platform.ErrLockBusy
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func releaseLock(value request) (any, error) {
	record, info, err := readLock(value.Path)
	if err != nil {
		return nil, err
	}
	if value.Lease == "" || record.Token != value.Lease || record.PID != value.PID ||
		record.ProcessIdentity != value.IdentityToken {
		return nil, platform.ErrUnsafe
	}
	if err := platform.RemovePrivateFileIfIdentity(value.Path, info.Identity); err != nil {
		return nil, err
	}
	return map[string]any{"released": true}, nil
}

func readLock(path string) (lockRecord, platform.FileInfo, error) {
	data, info, err := platform.ReadPrivateFileWithInfo(path, platform.FilePolicy{
		MinimumBytes: 1, MaximumBytes: 2048, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true,
	})
	if err != nil {
		return lockRecord{}, platform.FileInfo{}, err
	}
	var record lockRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(&struct{}{}) != io.EOF || record.Schema != lockSchema ||
		record.PID < 2 || len(record.Token) != 64 || strings.TrimSpace(record.ProcessIdentity) != record.ProcessIdentity ||
		record.ProcessIdentity == "" {
		return lockRecord{}, platform.FileInfo{}, platform.ErrUnsafe
	}
	return record, info, nil
}

func pathKind(kind string) (bool, error) {
	if kind == "directory" {
		return true, nil
	}
	if kind == "file" {
		return false, nil
	}
	return false, platform.ErrUnsafe
}

func canonicalPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(absolute)
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, platform.ErrLockBusy):
		return "lock_occupied"
	case errors.Is(err, platform.ErrUnsupported):
		return "operation_unsupported"
	case errors.Is(err, platform.ErrUnsafe):
		return "unsafe"
	default:
		return "operation_failed"
	}
}

func abort(code string) {
	writeResponse(response{Schema: responseSchema, OK: false, Error: code})
	os.Exit(1)
}

func writeResponse(value response) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}
