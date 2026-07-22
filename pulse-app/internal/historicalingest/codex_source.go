package historicalingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const CodexParserVersionV1 = "codex-tree-v1"

var ErrInsufficientValidCodexRoots = errors.New("fewer valid Codex roots than requested")

type CodexCohortError struct {
	Requested      int
	Available      int
	InvalidReasons map[string]string
}

func (err *CodexCohortError) Error() string {
	return fmt.Sprintf("%v: requested %d, available %d", ErrInsufficientValidCodexRoots, err.Requested, err.Available)
}

func (err *CodexCohortError) Unwrap() error { return ErrInsufficientValidCodexRoots }

type CodexSourceOptions struct {
	Roots              []string
	RootLimit          int
	Cutoff             time.Time
	ExcludedSessionIDs map[string]struct{}
	MaxRecordBytes     int
}

type CodexSourcePrefix struct {
	Alias         string `json:"alias"`
	SessionID     string `json:"session_id"`
	RootID        string `json:"root_id"`
	CapturedBytes int64  `json:"captured_bytes"`
	PrefixDigest  string `json:"prefix_digest"`
	ParserVersion string `json:"parser_version"`
	RecordCount   int64  `json:"record_count"`
	IncludedCount int64  `json:"included_count"`
	ExcludedCount int64  `json:"excluded_count"`
	BlockingCount int64  `json:"blocking_count"`
}

type CodexTree struct {
	RootID        string    `json:"root_id"`
	SessionIDs    []string  `json:"session_ids"`
	LatestOwnedAt time.Time `json:"latest_owned_at"`
}

type CodexSnapshot struct {
	ParserVersion   string              `json:"parser_version"`
	Cutoff          time.Time           `json:"cutoff"`
	Digest          string              `json:"digest"`
	RootCount       int                 `json:"root_count"`
	DescendantCount int                 `json:"descendant_count"`
	Trees           []CodexTree         `json:"trees"`
	Sources         []CodexSourcePrefix `json:"sources"`
	InvalidReasons  map[string]string   `json:"invalid_reasons,omitempty"`
	sourcePaths     map[string]string
	sourceEvidence  map[string][]CodexEvidence
}

type discoveredCodexSession struct {
	id       string
	parentID string
	path     string
	result   CodexParseResult
}

func BuildCodexSnapshot(options CodexSourceOptions) (CodexSnapshot, error) {
	limit := options.RootLimit
	if limit <= 0 {
		limit = 50
	}
	cutoff := options.Cutoff.UTC()
	if cutoff.IsZero() {
		cutoff = time.Now().UTC()
	}
	paths, err := discoverCodexJSONL(options.Roots)
	if err != nil {
		return CodexSnapshot{}, err
	}
	sessions := make(map[string]*discoveredCodexSession, len(paths))
	invalid := map[string]string{}
	for _, path := range paths {
		identity, err := ParseCodexFile(path, CodexParseOptions{MaxRecordBytes: options.MaxRecordBytes})
		if err != nil {
			return CodexSnapshot{}, err
		}
		if identity.SessionID == "" {
			return CodexSnapshot{}, fmt.Errorf("%w: source has no session identity", ErrUnsafeCodexSource)
		}
		alias := codexSourceAlias(identity.SessionID, identity.PrefixDigest)
		parsed, err := ParseCodexFile(path, CodexParseOptions{
			ExpectedSessionID: identity.SessionID,
			CapturedBytes:     identity.CapturedBytes,
			MaxRecordBytes:    options.MaxRecordBytes,
			SourceAlias:       alias,
		})
		if err != nil {
			return CodexSnapshot{}, err
		}
		if parsed.LatestOwnedAt.After(cutoff) {
			invalid[parsed.SessionID] = "after_cutoff"
			continue
		}
		if invalid[parsed.SessionID] == "duplicate_session_id" {
			continue
		}
		if _, exists := sessions[parsed.SessionID]; exists {
			invalid[parsed.SessionID] = "duplicate_session_id"
			delete(sessions, parsed.SessionID)
			continue
		}
		sessions[parsed.SessionID] = &discoveredCodexSession{
			id:       parsed.SessionID,
			parentID: parsed.ParentThreadID,
			path:     path,
			result:   parsed,
		}
	}

	for id, session := range sessions {
		if session.parentID != "" {
			if _, ok := sessions[session.parentID]; !ok {
				invalid[id] = "unresolved_parent"
			}
		}
	}
	markCodexCycles(sessions, invalid)

	children := make(map[string][]string)
	for id, session := range sessions {
		if session.parentID != "" && invalid[id] == "" && invalid[session.parentID] == "" {
			children[session.parentID] = append(children[session.parentID], id)
		}
	}
	for parent := range children {
		sort.Strings(children[parent])
	}

	excludedRoots := map[string]struct{}{}
	for excludedID := range options.ExcludedSessionIDs {
		rootID := codexAncestorRoot(excludedID, sessions, invalid)
		if rootID != "" {
			excludedRoots[rootID] = struct{}{}
		}
	}

	candidates := make([]CodexTree, 0)
	for id, session := range sessions {
		if session.parentID != "" || invalid[id] != "" {
			continue
		}
		if _, excluded := excludedRoots[id]; excluded {
			invalid[id] = "excluded_importer_tree"
			continue
		}
		ids := []string{id}
		appendCodexDescendants(id, children, &ids)
		candidates = append(candidates, CodexTree{RootID: id, SessionIDs: ids, LatestOwnedAt: session.result.LatestOwnedAt})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].LatestOwnedAt.Equal(candidates[j].LatestOwnedAt) {
			return candidates[i].RootID < candidates[j].RootID
		}
		return candidates[i].LatestOwnedAt.After(candidates[j].LatestOwnedAt)
	})
	if len(candidates) < limit {
		return CodexSnapshot{}, &CodexCohortError{Requested: limit, Available: len(candidates), InvalidReasons: invalid}
	}
	selected := append([]CodexTree(nil), candidates[:limit]...)

	snapshot := CodexSnapshot{
		ParserVersion:  CodexParserVersionV1,
		Cutoff:         cutoff,
		RootCount:      len(selected),
		Trees:          selected,
		InvalidReasons: invalid,
		sourcePaths:    map[string]string{},
		sourceEvidence: map[string][]CodexEvidence{},
	}
	for _, tree := range selected {
		snapshot.DescendantCount += len(tree.SessionIDs) - 1
		for _, sessionID := range tree.SessionIDs {
			session := sessions[sessionID]
			alias := codexSourceAlias(sessionID, session.result.PrefixDigest)
			snapshot.Sources = append(snapshot.Sources, CodexSourcePrefix{
				Alias:         alias,
				SessionID:     sessionID,
				RootID:        tree.RootID,
				CapturedBytes: session.result.CapturedBytes,
				PrefixDigest:  session.result.PrefixDigest,
				ParserVersion: CodexParserVersionV1,
				RecordCount:   session.result.Coverage.Total,
				IncludedCount: session.result.Coverage.Included,
				ExcludedCount: session.result.Coverage.Excluded,
				BlockingCount: session.result.Coverage.Blocking,
			})
			snapshot.sourcePaths[alias] = session.path
			snapshot.sourceEvidence[alias] = append([]CodexEvidence(nil), session.result.Evidence...)
		}
	}
	digest, err := codexSnapshotDigest(snapshot)
	if err != nil {
		return CodexSnapshot{}, err
	}
	snapshot.Digest = digest
	return snapshot, nil
}

func VerifyCodexSnapshot(snapshot CodexSnapshot) error {
	for _, source := range snapshot.Sources {
		path := snapshot.sourcePaths[source.Alias]
		if path == "" {
			return fmt.Errorf("%w: source map unavailable", ErrCodexPrefixStale)
		}
		probe, _, err := openRegularCodexFile(path)
		if err != nil {
			return err
		}
		_ = probe.Close()
		result, err := ParseCodexFile(path, CodexParseOptions{
			ExpectedSessionID: source.SessionID,
			CapturedBytes:     source.CapturedBytes,
			SourceAlias:       source.Alias,
		})
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCodexPrefixStale, err)
		}
		if result.PrefixDigest != source.PrefixDigest {
			return ErrCodexPrefixStale
		}
	}
	return nil
}

func CodexTreeEvidence(snapshot CodexSnapshot, rootID string) ([]CodexEvidence, error) {
	var tree *CodexTree
	for index := range snapshot.Trees {
		if snapshot.Trees[index].RootID == rootID {
			tree = &snapshot.Trees[index]
			break
		}
	}
	if tree == nil {
		return nil, errors.New("Codex root is not in the snapshot")
	}
	evidence := make([]CodexEvidence, 0)
	for _, sessionID := range tree.SessionIDs {
		for _, source := range snapshot.Sources {
			if source.SessionID == sessionID {
				evidence = append(evidence, snapshot.sourceEvidence[source.Alias]...)
				break
			}
		}
	}
	return evidence, nil
}

func discoverCodexJSONL(roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, errors.New("Codex source roots are required")
	}
	seen := map[string]struct{}{}
	paths := make([]string, 0)
	for _, root := range roots {
		canonical, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		rootInfo, err := os.Lstat(canonical)
		if err != nil {
			return nil, err
		}
		if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
			return nil, ErrUnsafeCodexSource
		}
		err = filepath.WalkDir(canonical, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				if strings.HasSuffix(entry.Name(), ".jsonl") {
					return ErrUnsafeCodexSource
				}
				return nil
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			if _, exists := seen[path]; !exists {
				seen[path] = struct{}{}
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func codexSourceAlias(sessionID, prefixDigest string) string {
	digest := sha256.Sum256([]byte(sessionID + ":" + prefixDigest))
	return "source_" + hex.EncodeToString(digest[:16])
}

func markCodexCycles(sessions map[string]*discoveredCodexSession, invalid map[string]string) {
	state := map[string]uint8{}
	stack := make([]string, 0)
	position := map[string]int{}
	var visit func(string)
	visit = func(id string) {
		if invalid[id] != "" || state[id] == 2 {
			return
		}
		if state[id] == 1 {
			start := position[id]
			for _, cycleID := range stack[start:] {
				invalid[cycleID] = "cycle"
			}
			return
		}
		state[id] = 1
		position[id] = len(stack)
		stack = append(stack, id)
		parent := sessions[id].parentID
		if parent != "" {
			if _, exists := sessions[parent]; exists {
				visit(parent)
			}
		}
		stack = stack[:len(stack)-1]
		delete(position, id)
		state[id] = 2
	}
	ids := make([]string, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		visit(id)
	}
}

func codexAncestorRoot(id string, sessions map[string]*discoveredCodexSession, invalid map[string]string) string {
	seen := map[string]struct{}{}
	for id != "" {
		if invalid[id] != "" {
			return ""
		}
		if _, exists := seen[id]; exists {
			return ""
		}
		seen[id] = struct{}{}
		session := sessions[id]
		if session == nil {
			return ""
		}
		if session.parentID == "" {
			return id
		}
		id = session.parentID
	}
	return ""
}

func appendCodexDescendants(parent string, children map[string][]string, out *[]string) {
	for _, child := range children[parent] {
		*out = append(*out, child)
		appendCodexDescendants(child, children, out)
	}
}

func codexSnapshotDigest(snapshot CodexSnapshot) (string, error) {
	copy := snapshot
	copy.Digest = ""
	copy.sourcePaths = nil
	copy.sourceEvidence = nil
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
