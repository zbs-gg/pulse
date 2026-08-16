package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const (
	memoryActivitySchema     = "pulse.memory_activity.v1"
	memoryRecallMaximumItems = 24
)

var memoryActivityDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type memoryRecallActivity struct {
	Host         string `json:"host"`
	RepositoryID string `json:"repository_id"`
	RecalledAt   string `json:"recalled_at"`
	ResultCount  int    `json:"result_count"`
	ResultDigest string `json:"result_digest"`
}

type memoryActivitySnapshot struct {
	Schema string                          `json:"schema"`
	Hosts  map[string]memoryRecallActivity `json:"hosts"`
}

type memoryRecallActivityRequest struct {
	Schema       string `json:"schema"`
	ResultCount  int    `json:"result_count"`
	ResultDigest string `json:"result_digest"`
}

func readMemoryActivity(path string) (memoryActivitySnapshot, error) {
	value := memoryActivitySnapshot{Schema: memoryActivitySchema, Hosts: map[string]memoryRecallActivity{}}
	if path == "" {
		return value, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 64<<10 {
		return memoryActivitySnapshot{}, fmt.Errorf("memory activity file is unsafe")
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &value) != nil || value.Schema != memoryActivitySchema || value.Hosts == nil {
		return memoryActivitySnapshot{}, fmt.Errorf("memory activity file is invalid")
	}
	for host, activity := range value.Hosts {
		if (host != "codex" && host != "claude-code") || activity.Host != host ||
			activity.RepositoryID == "" || activity.ResultCount < 0 || activity.ResultCount > memoryRecallMaximumItems ||
			!memoryActivityDigest.MatchString(activity.ResultDigest) ||
			activity.RecalledAt == "" || (time.Time{}).Equal(mustParseActivityTime(activity.RecalledAt)) {
			return memoryActivitySnapshot{}, fmt.Errorf("memory activity file is invalid")
		}
	}
	return value, nil
}

func mustParseActivityTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func writeMemoryActivity(path string, value memoryActivitySnapshot) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("memory activity path is invalid")
	}
	parent := filepath.Dir(path)
	if info, err := os.Lstat(parent); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("memory activity directory is unsafe")
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil || len(raw) > 64<<10 {
		return fmt.Errorf("memory activity snapshot is invalid")
	}
	temporary := fmt.Sprintf("%s.new-%d", path, os.Getpid())
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()
	if _, err = file.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (s *Server) recordMemoryRecallActivity(
	authority productBindingAuthority,
	host string,
	request memoryRecallActivityRequest,
	now time.Time,
) error {
	if s.cfg.MemoryActivityPath == "" {
		return fmt.Errorf("memory activity is unavailable")
	}
	s.memoryActivityMu.Lock()
	defer s.memoryActivityMu.Unlock()
	value, err := readMemoryActivity(s.cfg.MemoryActivityPath)
	if err != nil {
		return err
	}
	value.Hosts[host] = memoryRecallActivity{
		Host: host, RepositoryID: authority.RepositoryID,
		RecalledAt:  now.UTC().Format(time.RFC3339Nano),
		ResultCount: request.ResultCount, ResultDigest: request.ResultDigest,
	}
	return writeMemoryActivity(s.cfg.MemoryActivityPath, value)
}
