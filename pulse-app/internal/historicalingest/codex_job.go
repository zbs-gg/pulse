package historicalingest

import (
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nkkmnk/pulse/internal/platform"
)

const (
	codexSourceIndexSchema    = "pulse.historical_ingest.codex_source_index.v1"
	maxCodexSourceIndex       = 4 << 20
	defaultCodexChunkBytes    = 16 << 10
	HistoricalPromptVersionV5 = "historical_prompt_v5"
)

//go:embed historical_prompt_v5.txt
var historicalPromptV5 string

type CodexSourceStoreConfig struct {
	RootDir     string
	Key         []byte
	SourceRoots []string
}

type CodexSourceStore struct {
	mu          sync.Mutex
	rootDir     string
	key         []byte
	sourceRoots []string
	cache       map[string]codexSourceIndex
	snapshots   map[string]CodexSnapshot
}

type CodexPrepareOptions struct {
	RootLimit          int
	Cutoff             time.Time
	ExcludedSessionIDs map[string]struct{}
	MaxChunkBytes      int
}

type PreparedCodexJob struct {
	Snapshot SourceSnapshot `json:"snapshot"`
	Units    []WorkUnit     `json:"units"`
}

type codexIndexedSource struct {
	CodexSourcePrefix
	Path    string             `json:"path"`
	Version codexSourceVersion `json:"version"`
}

type codexSourceIndex struct {
	Schema         string               `json:"schema"`
	JobID          string               `json:"job_id"`
	SnapshotDigest string               `json:"snapshot_digest"`
	ParserVersion  string               `json:"parser_version"`
	Cutoff         time.Time            `json:"cutoff"`
	MaxChunkBytes  int                  `json:"max_chunk_bytes"`
	Trees          []CodexTree          `json:"trees"`
	Sources        []codexIndexedSource `json:"sources"`
	InvalidReasons map[string]string    `json:"invalid_reasons,omitempty"`
}

type codexSourceIndexEnvelope struct {
	Index     codexSourceIndex `json:"index"`
	Integrity string           `json:"integrity"`
}

type codexModelEvidence struct {
	RootID  string                `json:"root_id"`
	Ordinal int                   `json:"ordinal"`
	Sources []codexEvidenceSource `json:"sources"`
	Records []CodexEvidence       `json:"records"`
}

type codexEvidenceSource struct {
	Alias        string `json:"alias"`
	PrefixDigest string `json:"prefix_digest"`
}

func NewCodexSourceStore(cfg CodexSourceStoreConfig) (*CodexSourceStore, error) {
	if !filepath.IsAbs(cfg.RootDir) || len(cfg.Key) < 32 || len(cfg.SourceRoots) == 0 {
		return nil, errors.New("Codex source store requires an absolute private root, key, and source root")
	}
	roots := make([]string, 0, len(cfg.SourceRoots))
	for _, root := range cfg.SourceRoots {
		absolute, err := filepath.Abs(root)
		if err != nil || !filepath.IsAbs(absolute) {
			return nil, errors.New("Codex source root is invalid")
		}
		info, err := os.Lstat(absolute)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, ErrUnsafeCodexSource
		}
		roots = append(roots, absolute)
	}
	if err := platform.EnsurePrivateDirectory(cfg.RootDir); err != nil {
		return nil, err
	}
	return &CodexSourceStore{rootDir: cfg.RootDir, key: append([]byte(nil), cfg.Key...), sourceRoots: roots, cache: map[string]codexSourceIndex{}, snapshots: map[string]CodexSnapshot{}}, nil
}

func (s *CodexSourceStore) Prepare(jobID string, options CodexPrepareOptions) (PreparedCodexJob, error) {
	if !jobIDPattern.MatchString(jobID) {
		return PreparedCodexJob{}, errors.New("invalid Codex historical job id")
	}
	maxChunkBytes := options.MaxChunkBytes
	if maxChunkBytes == 0 {
		maxChunkBytes = defaultCodexChunkBytes
	}
	if maxChunkBytes < 128 || maxChunkBytes > 4<<20 {
		return PreparedCodexJob{}, errors.New("invalid Codex historical chunk size")
	}
	snapshot, err := BuildCodexSnapshot(CodexSourceOptions{
		Roots: s.sourceRoots, RootLimit: options.RootLimit, Cutoff: options.Cutoff,
		ExcludedSessionIDs: options.ExcludedSessionIDs,
	})
	if err != nil {
		return PreparedCodexJob{}, err
	}
	sourceSnapshot := sourceSnapshotFromCodex(snapshot)
	units, err := workUnitsFromCodex(snapshot, maxChunkBytes)
	if err != nil {
		return PreparedCodexJob{}, err
	}
	if len(units) == 0 {
		return PreparedCodexJob{}, errors.New("Codex historical snapshot contains no normalized evidence")
	}
	index := sourceIndexFromCodex(jobID, snapshot, maxChunkBytes)
	if err := s.saveIndex(index); err != nil {
		return PreparedCodexJob{}, err
	}
	s.mu.Lock()
	s.snapshots[snapshot.Digest] = snapshot
	s.mu.Unlock()
	return PreparedCodexJob{Snapshot: sourceSnapshot, Units: units}, nil
}

func (s *CodexSourceStore) Load(unit WorkUnit) (string, string, error) {
	if validateWorkUnit(unit, unit.SnapshotDigest) != nil {
		return "", "", errors.New("invalid Codex historical work unit")
	}
	index, snapshot, err := s.loadSnapshot(unit.SnapshotDigest)
	if err != nil {
		return "", "", err
	}
	if err := verifyCodexAliases(snapshot, unit.SourceAliases); err != nil {
		return "", "", err
	}
	records, err := CodexTreeEvidence(snapshot, unit.RootID)
	if err != nil {
		return "", "", err
	}
	chunks, err := ChunkCodexEvidence(unit.RootID, records, index.MaxChunkBytes)
	if err != nil || unit.Ordinal >= len(chunks) {
		return "", "", ErrCodexPrefixStale
	}
	chunk := chunks[unit.Ordinal]
	allowed := map[string]codexEvidenceSource{}
	for _, source := range snapshot.Sources {
		allowed[source.Alias] = codexEvidenceSource{Alias: source.Alias, PrefixDigest: source.PrefixDigest}
	}
	sources := make([]codexEvidenceSource, 0, len(unit.SourceAliases))
	for _, alias := range unit.SourceAliases {
		ref, exists := allowed[alias]
		if !exists {
			return "", "", ErrCodexPrefixStale
		}
		sources = append(sources, ref)
	}
	payload := codexModelEvidence{RootID: unit.RootID, Ordinal: unit.Ordinal, Sources: sources, Records: chunk.Records}
	encoded, err := json.Marshal(payload)
	if err != nil || digestBytes(encoded) != unit.EvidenceDigest || unsafeEvidencePattern.Match(encoded) {
		return "", "", ErrUnsafeCodexSource
	}
	return trustedHistoricalPrompt(index.JobID, unit.SnapshotDigest), string(encoded), nil
}

func (s *CodexSourceStore) Verify(snapshot SourceSnapshot) error {
	_, restored, err := s.loadSnapshot(snapshot.Digest)
	if err != nil {
		return err
	}
	if err := VerifyCodexSnapshot(restored); err != nil {
		return err
	}
	encodedExpected, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	encodedActual, err := json.Marshal(sourceSnapshotFromCodex(restored))
	if err != nil || !hmac.Equal(encodedExpected, encodedActual) {
		return ErrCodexPrefixStale
	}
	return nil
}

func (s *CodexSourceStore) VerifyDigest(snapshotDigest string) error {
	_, snapshot, err := s.loadSnapshot(snapshotDigest)
	if err != nil {
		return err
	}
	return VerifyCodexSnapshot(snapshot)
}

func verifyCodexAliases(snapshot CodexSnapshot, aliases []string) error {
	byAlias := make(map[string]CodexSourcePrefix, len(snapshot.Sources))
	for _, source := range snapshot.Sources {
		byAlias[source.Alias] = source
	}
	for _, alias := range aliases {
		source, exists := byAlias[alias]
		if !exists {
			return ErrCodexPrefixStale
		}
		current, err := currentCodexSourceVersion(snapshot.sourcePaths[alias])
		if err != nil || !codexSourceVersionPreservesPrefix(snapshot.sourceVersions[alias], current, source.CapturedBytes) {
			return ErrCodexPrefixStale
		}
		if err := verifyCodexSourcePrefix(snapshot.sourcePaths[alias], source); err != nil {
			return ErrCodexPrefixStale
		}
	}
	return nil
}

func codexSourceVersionPreservesPrefix(expected, current codexSourceVersion, capturedBytes int64) bool {
	return capturedBytes > 0 &&
		validCodexSourceVersion(expected) &&
		validCodexSourceVersion(current) &&
		expected.Size >= capturedBytes &&
		current.Size >= capturedBytes &&
		expected.Device == current.Device &&
		expected.Inode == current.Inode
}

func (s *CodexSourceStore) loadSnapshot(snapshotDigest string) (codexSourceIndex, CodexSnapshot, error) {
	s.mu.Lock()
	index, indexOK := s.cache[snapshotDigest]
	snapshot, snapshotOK := s.snapshots[snapshotDigest]
	s.mu.Unlock()
	if snapshotOK && indexOK {
		return index, snapshot, nil
	}
	if !indexOK {
		var err error
		index, err = s.readIndex(snapshotDigest)
		if err != nil {
			return codexSourceIndex{}, CodexSnapshot{}, err
		}
	}
	restored, err := restoreCodexSnapshot(index, s.sourceRoots)
	if err != nil {
		return codexSourceIndex{}, CodexSnapshot{}, err
	}
	s.mu.Lock()
	s.snapshots[snapshotDigest] = restored
	s.mu.Unlock()
	return index, restored, nil
}

func sourceSnapshotFromCodex(snapshot CodexSnapshot) SourceSnapshot {
	files := make([]SourceFilePrefix, 0, len(snapshot.Sources))
	for _, source := range snapshot.Sources {
		files = append(files, SourceFilePrefix{
			Alias: source.Alias, CapturedBytes: source.CapturedBytes, PrefixDigest: source.PrefixDigest,
			RootID: source.RootID, ParserVersion: source.ParserVersion, RecordCount: source.RecordCount,
			IncludedCount: source.IncludedCount, ExcludedCount: source.ExcludedCount, BlockingCount: source.BlockingCount,
		})
	}
	return SourceSnapshot{Digest: snapshot.Digest, Cutoff: snapshot.Cutoff, RootCount: snapshot.RootCount, Files: files, ParserVersion: snapshot.ParserVersion}
}

func workUnitsFromCodex(snapshot CodexSnapshot, maxChunkBytes int) ([]WorkUnit, error) {
	units := make([]WorkUnit, 0)
	for _, tree := range snapshot.Trees {
		records, err := CodexTreeEvidence(snapshot, tree.RootID)
		if err != nil {
			return nil, err
		}
		chunks, err := ChunkCodexEvidence(tree.RootID, records, maxChunkBytes)
		if err != nil {
			return nil, err
		}
		for _, chunk := range chunks {
			aliases := make([]string, 0)
			seen := map[string]struct{}{}
			for _, record := range chunk.Records {
				if _, exists := seen[record.SourceAlias]; !exists {
					seen[record.SourceAlias] = struct{}{}
					aliases = append(aliases, record.SourceAlias)
				}
			}
			sort.Strings(aliases)
			payload, err := modelEvidenceForChunk(snapshot, chunk, aliases)
			if err != nil {
				return nil, err
			}
			evidenceDigest := digestBytes(payload)
			unitDigest := sha256.Sum256([]byte(snapshot.Digest + ":" + tree.RootID + ":" + fmt.Sprint(chunk.Ordinal) + ":" + evidenceDigest))
			units = append(units, WorkUnit{
				ID: "unit_" + hex.EncodeToString(unitDigest[:16]), RootID: tree.RootID, SnapshotDigest: snapshot.Digest,
				EvidenceDigest: evidenceDigest, EvidenceBytes: int64(len(payload)), SourceAliases: aliases, Ordinal: chunk.Ordinal,
			})
		}
	}
	return units, nil
}

func modelEvidenceForChunk(snapshot CodexSnapshot, chunk CodexEvidenceChunk, aliases []string) ([]byte, error) {
	byAlias := map[string]codexEvidenceSource{}
	for _, source := range snapshot.Sources {
		byAlias[source.Alias] = codexEvidenceSource{Alias: source.Alias, PrefixDigest: source.PrefixDigest}
	}
	sources := make([]codexEvidenceSource, 0, len(aliases))
	for _, alias := range aliases {
		source, exists := byAlias[alias]
		if !exists {
			return nil, ErrCodexPrefixStale
		}
		sources = append(sources, source)
	}
	return json.Marshal(codexModelEvidence{RootID: chunk.RootID, Ordinal: chunk.Ordinal, Sources: sources, Records: chunk.Records})
}

func sourceIndexFromCodex(jobID string, snapshot CodexSnapshot, maxChunkBytes int) codexSourceIndex {
	index := codexSourceIndex{Schema: codexSourceIndexSchema, JobID: jobID, SnapshotDigest: snapshot.Digest, ParserVersion: snapshot.ParserVersion, Cutoff: snapshot.Cutoff, MaxChunkBytes: maxChunkBytes, Trees: append([]CodexTree(nil), snapshot.Trees...), InvalidReasons: cloneStringMap(snapshot.InvalidReasons)}
	for _, source := range snapshot.Sources {
		index.Sources = append(index.Sources, codexIndexedSource{CodexSourcePrefix: source, Path: snapshot.sourcePaths[source.Alias], Version: snapshot.sourceVersions[source.Alias]})
	}
	return index
}

func cloneStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	copy := make(map[string]string, len(value))
	for key, item := range value {
		copy[key] = item
	}
	return copy
}

func restoreCodexSnapshot(index codexSourceIndex, allowedRoots []string) (CodexSnapshot, error) {
	if validateCodexSourceIndex(index, allowedRoots) != nil {
		return CodexSnapshot{}, ErrUnsafeCodexSource
	}
	snapshot := CodexSnapshot{ParserVersion: index.ParserVersion, Cutoff: index.Cutoff, Digest: index.SnapshotDigest, RootCount: len(index.Trees), Trees: append([]CodexTree(nil), index.Trees...), InvalidReasons: cloneStringMap(index.InvalidReasons), sourcePaths: map[string]string{}, sourceEvidence: map[string][]CodexEvidence{}, sourceVersions: map[string]codexSourceVersion{}}
	for _, indexed := range index.Sources {
		parsed, err := ParseCodexFile(indexed.Path, CodexParseOptions{ExpectedSessionID: indexed.SessionID, CapturedBytes: indexed.CapturedBytes, SourceAlias: indexed.Alias, AllowMultipleLinks: true})
		if err != nil || parsed.PrefixDigest != indexed.PrefixDigest || !codexSourceVersionPreservesPrefix(indexed.Version, parsed.sourceVersion, indexed.CapturedBytes) {
			return CodexSnapshot{}, ErrCodexPrefixStale
		}
		if indexed.SessionID != indexed.RootID {
			compactCodexChildResult(&parsed)
		} else {
			compactCodexRootEvidence(&parsed)
		}
		snapshot.Sources = append(snapshot.Sources, indexed.CodexSourcePrefix)
		snapshot.sourcePaths[indexed.Alias] = indexed.Path
		snapshot.sourceEvidence[indexed.Alias] = parsed.Evidence
		snapshot.sourceVersions[indexed.Alias] = indexed.Version
	}
	for _, tree := range snapshot.Trees {
		snapshot.DescendantCount += len(tree.SessionIDs) - 1
	}
	digest, err := codexSnapshotDigest(snapshot)
	if err != nil || digest != index.SnapshotDigest {
		return CodexSnapshot{}, ErrCodexPrefixStale
	}
	return snapshot, nil
}

func (s *CodexSourceStore) saveIndex(index codexSourceIndex) error {
	if err := validateCodexSourceIndex(index, s.sourceRoots); err != nil {
		return err
	}
	payload, err := json.Marshal(index)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(payload)
	envelope, err := json.Marshal(codexSourceIndexEnvelope{Index: index, Integrity: hex.EncodeToString(mac.Sum(nil))})
	if err != nil || len(envelope) > maxCodexSourceIndex {
		return errors.New("Codex source index is too large")
	}
	path := filepath.Join(s.rootDir, "source-index-"+index.SnapshotDigest+".json")
	if _, err := platform.CreatePrivateFileExclusive(path, envelope); errors.Is(err, os.ErrExist) {
		existing, readErr := platform.ReadPrivateFile(path, privateIngestPolicy(maxCodexSourceIndex))
		if readErr != nil || string(existing) != string(envelope) {
			return ErrIngestCheckpointIntegrity
		}
	} else if err != nil {
		return err
	}
	s.mu.Lock()
	s.cache[index.SnapshotDigest] = index
	s.mu.Unlock()
	return nil
}

func (s *CodexSourceStore) readIndex(snapshotDigest string) (codexSourceIndex, error) {
	if !hexDigestPattern.MatchString(snapshotDigest) {
		return codexSourceIndex{}, ErrIngestCheckpointIntegrity
	}
	encoded, err := platform.ReadPrivateFile(filepath.Join(s.rootDir, "source-index-"+snapshotDigest+".json"), privateIngestPolicy(maxCodexSourceIndex))
	if err != nil {
		return codexSourceIndex{}, err
	}
	var envelope codexSourceIndexEnvelope
	if json.Unmarshal(encoded, &envelope) != nil {
		return codexSourceIndex{}, ErrIngestCheckpointIntegrity
	}
	payload, _ := json.Marshal(envelope.Index)
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(payload)
	if !hmac.Equal([]byte(envelope.Integrity), []byte(hex.EncodeToString(mac.Sum(nil)))) || validateCodexSourceIndex(envelope.Index, s.sourceRoots) != nil {
		return codexSourceIndex{}, ErrIngestCheckpointIntegrity
	}
	s.mu.Lock()
	s.cache[snapshotDigest] = envelope.Index
	s.mu.Unlock()
	return envelope.Index, nil
}

func validateCodexSourceIndex(index codexSourceIndex, allowedRoots []string) error {
	if index.Schema != codexSourceIndexSchema || !jobIDPattern.MatchString(index.JobID) || !hexDigestPattern.MatchString(index.SnapshotDigest) || index.ParserVersion != CodexParserVersionV1 || index.Cutoff.IsZero() || index.MaxChunkBytes < 128 || index.MaxChunkBytes > 4<<20 || len(index.Trees) == 0 || len(index.Sources) == 0 {
		return errors.New("invalid Codex source index")
	}
	aliases := map[string]struct{}{}
	for _, source := range index.Sources {
		if !sourceAliasPattern.MatchString(source.Alias) || source.SessionID == "" || source.Path == "" || !filepath.IsAbs(source.Path) || !pathWithinRoots(source.Path, allowedRoots) || !validCodexSourceVersion(source.Version) || source.CapturedBytes > source.Version.Size {
			return errors.New("invalid Codex indexed source")
		}
		if _, exists := aliases[source.Alias]; exists {
			return errors.New("duplicate Codex indexed source")
		}
		aliases[source.Alias] = struct{}{}
	}
	return nil
}

func pathWithinRoots(path string, roots []string) bool {
	for _, root := range roots {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}

func trustedHistoricalPrompt(jobID, snapshotDigest string) string {
	return fmt.Sprintf(historicalPromptV5, jobID, snapshotDigest)
}
