package historicalingest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nkkmnk/pulse/internal/platform"
)

type RetentionReason string

const (
	RetentionSuccess     RetentionReason = "success"
	RetentionCanceled    RetentionReason = "canceled"
	RetentionFailure     RetentionReason = "failure"
	RetentionSuperseded  RetentionReason = "superseded"
	RetentionExpired     RetentionReason = "expired"
	RetentionExported    RetentionReason = "exported"
	RetentionDestructive RetentionReason = "destructive_wipe"
)

func (m *IngestManager) CleanupJob(jobID string, reason RetentionReason) error {
	if !jobIDPattern.MatchString(jobID) || !validRetentionReason(reason) {
		return errors.New("invalid historical ingest cleanup")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	checkpoint, ok := m.jobs[jobID]
	if !ok {
		return ErrIngestJobNotFound
	}
	entries, err := os.ReadDir(m.rootDir)
	if err != nil {
		return err
	}
	// Inspect every recognized private artifact before deleting any one. A
	// substituted symlink or special file makes cleanup fail atomically.
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "result-") || strings.HasPrefix(entry.Name(), "manifest-") {
			if _, err := platform.InspectPrivateFile(filepath.Join(m.rootDir, entry.Name()), privateIngestPolicy(maxIngestManifest)); err != nil {
				return err
			}
		}
	}
	if reason == RetentionSuccess || reason == RetentionExported {
		return nil
	}
	targets := make([]string, 0, len(checkpoint.Units)+1)
	for _, unit := range checkpoint.Units {
		if unit.ResultDigest != "" {
			targets = append(targets, "result-"+unit.ResultDigest+".json")
		}
	}
	if checkpoint.ManifestDigest != "" {
		targets = append(targets, "manifest-"+checkpoint.ManifestDigest+".json")
	}
	for _, name := range targets {
		path := filepath.Join(m.rootDir, name)
		info, err := platform.InspectPrivateFile(path, privateIngestPolicy(maxIngestManifest))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := platform.RemovePrivateFileIfIdentity(path, info.Identity); err != nil {
			return err
		}
	}
	checkpoint.State = JobCanceled
	checkpoint.ReasonCode = "retention_" + string(reason)
	return m.commitLocked(&checkpoint)
}

func validRetentionReason(value RetentionReason) bool {
	switch value {
	case RetentionSuccess, RetentionCanceled, RetentionFailure, RetentionSuperseded, RetentionExpired, RetentionExported, RetentionDestructive:
		return true
	default:
		return false
	}
}
