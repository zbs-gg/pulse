package historicalingest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/platform"
)

const (
	ingestCheckpointSchema = "pulse.historical_ingest.checkpoint.v1"
	maxIngestCheckpoint    = 16 << 20
	maxAcceptedResult      = 4 << 20
	maxIngestManifest      = 64 << 20
)

var ErrIngestCheckpointIntegrity = errors.New("historical ingest checkpoint integrity failure")

type unitState string

const (
	unitPending  unitState = "pending"
	unitLeased   unitState = "leased"
	unitAccepted unitState = "accepted"
	unitFailed   unitState = "failed"
)

type unitCheckpoint struct {
	Unit               WorkUnit   `json:"unit"`
	State              unitState  `json:"state"`
	LeaseHash          string     `json:"lease_hash,omitempty"`
	LeaseExpiresAt     *time.Time `json:"lease_expires_at,omitempty"`
	ResultDigest       string     `json:"result_digest,omitempty"`
	AcceptedGeneration uint64     `json:"accepted_generation,omitempty"`
	Usage              TokenUsage `json:"usage"`
}

type ingestCheckpoint struct {
	Schema           string           `json:"schema"`
	Generation       uint64           `json:"generation"`
	JobID            string           `json:"job_id"`
	State            JobState         `json:"state"`
	Snapshot         SourceSnapshot   `json:"snapshot"`
	Contract         RunnerContract   `json:"contract"`
	Units            []unitCheckpoint `json:"units"`
	ManifestRevision int64            `json:"manifest_revision,omitempty"`
	ManifestDigest   string           `json:"manifest_digest,omitempty"`
	ReasonCode       string           `json:"reason_code,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

type checkpointEnvelope struct {
	Checkpoint ingestCheckpoint `json:"checkpoint"`
	Integrity  string           `json:"integrity"`
}

func (m *IngestManager) checkpointMAC(payload []byte) string {
	mac := hmac.New(sha256.New, m.key)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *IngestManager) commitLocked(checkpoint *ingestCheckpoint) error {
	checkpoint.Generation = m.nextGeneration
	m.nextGeneration++
	checkpoint.UpdatedAt = m.clock().UTC()
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	envelope := checkpointEnvelope{Checkpoint: cloneCheckpoint(*checkpoint), Integrity: m.checkpointMAC(payload)}
	encoded, err := json.Marshal(envelope)
	if err != nil || len(encoded) > maxIngestCheckpoint {
		return errors.New("historical ingest checkpoint too large")
	}
	name := fmt.Sprintf("checkpoint-%020d.json", checkpoint.Generation)
	if err := m.checkpointWrite(filepath.Join(m.rootDir, name), encoded); err != nil {
		return err
	}
	m.jobs[checkpoint.JobID] = cloneCheckpoint(*checkpoint)
	return nil
}

func (m *IngestManager) loadCheckpoints() error {
	entries, err := os.ReadDir(m.rootDir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "checkpoint-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		encoded, err := platform.ReadPrivateFile(filepath.Join(m.rootDir, entry.Name()), privateIngestPolicy(maxIngestCheckpoint))
		if err != nil {
			return ErrIngestCheckpointIntegrity
		}
		var envelope checkpointEnvelope
		if json.Unmarshal(encoded, &envelope) != nil {
			return ErrIngestCheckpointIntegrity
		}
		payload, err := json.Marshal(envelope.Checkpoint)
		if err != nil || !hmac.Equal([]byte(envelope.Integrity), []byte(m.checkpointMAC(payload))) || validateCheckpoint(envelope.Checkpoint) != nil {
			return ErrIngestCheckpointIntegrity
		}
		current, exists := m.jobs[envelope.Checkpoint.JobID]
		if !exists || current.Generation < envelope.Checkpoint.Generation {
			m.jobs[envelope.Checkpoint.JobID] = cloneCheckpoint(envelope.Checkpoint)
		}
		if envelope.Checkpoint.Generation >= m.nextGeneration {
			m.nextGeneration = envelope.Checkpoint.Generation + 1
		}
	}
	return nil
}

func validateCheckpoint(checkpoint ingestCheckpoint) error {
	if checkpoint.Schema != ingestCheckpointSchema || checkpoint.Generation == 0 || !jobIDPattern.MatchString(checkpoint.JobID) ||
		checkpoint.CreatedAt.IsZero() || checkpoint.UpdatedAt.Before(checkpoint.CreatedAt) || validateSourceSnapshot(checkpoint.Snapshot) != nil ||
		checkpoint.Contract.Validate() != nil || !validManagerState(checkpoint.State) || len(checkpoint.Units) == 0 {
		return ErrIngestCheckpointIntegrity
	}
	seen := map[string]struct{}{}
	for _, unit := range checkpoint.Units {
		if validateWorkUnit(unit.Unit, checkpoint.Snapshot.Digest) != nil {
			return ErrIngestCheckpointIntegrity
		}
		if _, ok := seen[unit.Unit.ID]; ok {
			return ErrIngestCheckpointIntegrity
		}
		seen[unit.Unit.ID] = struct{}{}
		switch unit.State {
		case unitPending:
			if unit.LeaseHash != "" || unit.LeaseExpiresAt != nil || unit.ResultDigest != "" || unit.AcceptedGeneration != 0 {
				return ErrIngestCheckpointIntegrity
			}
		case unitLeased:
			if !hexDigestPattern.MatchString(unit.LeaseHash) || unit.LeaseExpiresAt == nil || unit.ResultDigest != "" || unit.AcceptedGeneration != 0 {
				return ErrIngestCheckpointIntegrity
			}
		case unitAccepted:
			if unit.LeaseHash != "" || unit.LeaseExpiresAt != nil || !hexDigestPattern.MatchString(unit.ResultDigest) || unit.AcceptedGeneration == 0 || unit.Usage.Validate() != nil {
				return ErrIngestCheckpointIntegrity
			}
		default:
			return ErrIngestCheckpointIntegrity
		}
	}
	if checkpoint.ManifestDigest != "" && (!hexDigestPattern.MatchString(checkpoint.ManifestDigest) || checkpoint.ManifestRevision < 1) {
		return ErrIngestCheckpointIntegrity
	}
	return nil
}

func privateIngestPolicy(maximum int64) platform.FilePolicy {
	return platform.FilePolicy{MinimumBytes: 2, MaximumBytes: maximum, RequireCurrentOwner: true, OwnerOnly: true, SingleLink: true}
}

func cloneCheckpoint(value ingestCheckpoint) ingestCheckpoint {
	encoded, _ := json.Marshal(value)
	var cloned ingestCheckpoint
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}
