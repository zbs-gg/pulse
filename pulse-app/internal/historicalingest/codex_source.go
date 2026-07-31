package historicalingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const CodexParserVersionV1 = "codex-tree-v1"

var ErrInsufficientValidCodexRoots = errors.New("fewer valid Codex roots than requested")

var codexFilenameSessionPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

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
	sourceVersions  map[string]codexSourceVersion
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
	paths, invalid, err := discoverCodexJSONL(options.Roots)
	if err != nil {
		return CodexSnapshot{}, err
	}
	sessions := make(map[string]*discoveredCodexSession, len(paths))
	for _, path := range paths {
		identity, err := probeCodexFile(path)
		if err != nil {
			if errors.Is(err, ErrUnsafeCodexSource) {
				invalid[codexUnsafePathAlias(path)] = "unsafe_file_identity"
				continue
			}
			return CodexSnapshot{}, err
		}
		if identity.SessionID == "" {
			return CodexSnapshot{}, fmt.Errorf("%w: source has no session identity", ErrUnsafeCodexSource)
		}
		if identity.LatestOwnedAt.After(cutoff) {
			invalid[identity.SessionID] = "after_cutoff"
			continue
		}
		if invalid[identity.SessionID] == "duplicate_session_id" {
			continue
		}
		if _, exists := sessions[identity.SessionID]; exists {
			invalid[identity.SessionID] = "duplicate_session_id"
			delete(sessions, identity.SessionID)
			continue
		}
		sessions[identity.SessionID] = &discoveredCodexSession{
			id:       identity.SessionID,
			parentID: identity.ParentThreadID,
			path:     path,
			result:   identity,
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
	for treeIndex := range selected {
		for _, sessionID := range selected[treeIndex].SessionIDs {
			session := sessions[sessionID]
			parsed, err := ParseCodexFile(session.path, CodexParseOptions{
				ExpectedSessionID: sessionID, CapturedBytes: session.result.CapturedBytes, MaxRecordBytes: options.MaxRecordBytes, AllowMultipleLinks: true,
			})
			if err != nil {
				return CodexSnapshot{}, err
			}
			if session.parentID != "" {
				compactCodexChildResult(&parsed)
			} else {
				compactCodexRootEvidence(&parsed)
			}
			alias := codexSourceAlias(sessionID, parsed.PrefixDigest)
			for evidenceIndex := range parsed.Evidence {
				parsed.Evidence[evidenceIndex].SourceAlias = alias
			}
			session.result = parsed
		}
		selected[treeIndex].LatestOwnedAt = sessions[selected[treeIndex].RootID].result.LatestOwnedAt
	}

	snapshot := CodexSnapshot{
		ParserVersion:  CodexParserVersionV1,
		Cutoff:         cutoff,
		RootCount:      len(selected),
		Trees:          selected,
		InvalidReasons: invalid,
		sourcePaths:    map[string]string{},
		sourceEvidence: map[string][]CodexEvidence{},
		sourceVersions: map[string]codexSourceVersion{},
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
			snapshot.sourceVersions[alias] = session.result.sourceVersion
		}
	}
	digest, err := codexSnapshotDigest(snapshot)
	if err != nil {
		return CodexSnapshot{}, err
	}
	snapshot.Digest = digest
	return snapshot, nil
}

func compactCodexChildResult(result *CodexParseResult) {
	if result == nil || len(result.Evidence) == 0 {
		return
	}
	lastAssistant := -1
	lastAgentResult := -1
	lastFallback := -1
	for index, evidence := range result.Evidence {
		if evidence.Kind == "message" && evidence.Role == "assistant" {
			lastAssistant = index
		}
		if evidence.Kind == "agent_result" {
			lastAgentResult = index
		}
		lastFallback = index
	}
	if lastAssistant < 0 {
		lastAssistant = lastAgentResult
	}
	kept := make([]CodexEvidence, 0)
	for index, evidence := range result.Evidence {
		if evidence.Kind == "message" && evidence.Role == "user" || index == lastAssistant || lastAssistant < 0 && index == lastFallback {
			kept = append(kept, evidence)
		}
	}
	dropped := int64(len(result.Evidence) - len(kept))
	if dropped > 0 {
		result.Coverage.Included -= dropped
		result.Coverage.Excluded += dropped
		result.Exclusions["child_intermediate"] += dropped
	}
	result.Evidence = kept
}

func compactCodexRootEvidence(result *CodexParseResult) {
	if result == nil || len(result.Evidence) == 0 {
		return
	}
	keep := make([]bool, len(result.Evidence))
	pendingTool := -1
	for index, evidence := range result.Evidence {
		if evidence.Kind == "message" && evidence.Role == "user" {
			if pendingTool >= 0 {
				keep[pendingTool] = true
				pendingTool = -1
			}
			keep[index] = true
			continue
		}
		if evidence.Kind == "message" && evidence.Role == "assistant" {
			keep[index] = true
			continue
		}
		if evidence.Kind == "tool_result" {
			pendingTool = index
		}
	}
	if pendingTool >= 0 {
		keep[pendingTool] = true
	}
	compacted := make([]CodexEvidence, 0, len(result.Evidence))
	for index, evidence := range result.Evidence {
		if keep[index] {
			compacted = append(compacted, evidence)
			continue
		}
		result.Coverage.Included--
		result.Coverage.Excluded++
		if evidence.Kind == "agent_result" {
			result.Exclusions["root_agent_result_mirror"]++
		} else {
			result.Exclusions["root_tool_intermediate"]++
		}
	}
	result.Evidence = compacted
}

func probeCodexFile(path string) (CodexParseResult, error) {
	file, info, err := openCodexFile(path, true)
	if err != nil {
		return CodexParseResult{}, err
	}
	defer file.Close()
	if info.Size() < 2 {
		return CodexParseResult{}, ErrUnsafeCodexSource
	}
	const headLimit = int64(1 << 20)
	headSize := info.Size()
	if headSize > headLimit {
		headSize = headLimit
	}
	head := make([]byte, headSize)
	if _, err := file.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return CodexParseResult{}, err
	}
	desiredID := codexFilenameSessionPattern.FindString(filepath.Base(path))
	var selected codexSessionMeta
	var selectedTime time.Time
	for _, line := range bytes.Split(head, []byte{'\n'}) {
		var envelope codexEnvelope
		if json.Unmarshal(bytes.TrimSpace(line), &envelope) != nil || envelope.Type != "session_meta" {
			continue
		}
		var meta codexSessionMeta
		if json.Unmarshal(envelope.Payload, &meta) != nil {
			continue
		}
		id := meta.ID
		if id == "" {
			id = meta.SessionID
		}
		if id == "" {
			continue
		}
		selected = meta
		selected.ID = id
		selectedTime, _ = parseCodexTime(envelope.Timestamp)
		if desiredID != "" && id == desiredID {
			break
		}
	}
	if selected.ID == "" || (desiredID != "" && selected.ID != desiredID) {
		return CodexParseResult{}, fmt.Errorf("%w: session metadata probe failed", ErrUnsafeCodexSource)
	}
	parentID := selected.ParentThreadID
	if parentID == "" {
		var structured codexStructuredSource
		if len(selected.Source) > 0 && selected.Source[0] == '{' && json.Unmarshal(selected.Source, &structured) == nil {
			parentID = structured.Subagent.ThreadSpawn.ParentThreadID
		}
	}
	latest := selectedTime
	const tailLimit = int64(256 << 10)
	tailSize := info.Size()
	if tailSize > tailLimit {
		tailSize = tailLimit
	}
	tail := make([]byte, tailSize)
	if _, err := file.ReadAt(tail, info.Size()-tailSize); err != nil && !errors.Is(err, io.EOF) {
		return CodexParseResult{}, err
	}
	lines := bytes.Split(tail, []byte{'\n'})
	for index := len(lines) - 1; index >= 0; index-- {
		var envelope codexEnvelope
		if json.Unmarshal(bytes.TrimSpace(lines[index]), &envelope) != nil {
			continue
		}
		if timestamp, ok := parseCodexTime(envelope.Timestamp); ok {
			latest = timestamp
			break
		}
	}
	if latest.IsZero() {
		return CodexParseResult{}, fmt.Errorf("%w: session timestamp probe failed", ErrUnsafeCodexSource)
	}
	return CodexParseResult{SessionID: selected.ID, ParentThreadID: parentID, ForkedFromID: selected.ForkedFromID, LatestOwnedAt: latest, CapturedBytes: info.Size()}, nil
}

func VerifyCodexSnapshot(snapshot CodexSnapshot) error {
	for _, source := range snapshot.Sources {
		path := snapshot.sourcePaths[source.Alias]
		if path == "" {
			return fmt.Errorf("%w: source map unavailable", ErrCodexPrefixStale)
		}
		if err := verifyCodexSourcePrefix(path, source); err != nil {
			return err
		}
	}
	return nil
}

func verifyCodexSourcePrefix(path string, source CodexSourcePrefix) error {
	file, initial, err := openCodexFile(path, true)
	if err != nil {
		return err
	}
	if initial.Size() < source.CapturedBytes {
		_ = file.Close()
		return ErrCodexPrefixStale
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(hasher, io.LimitReader(file, source.CapturedBytes))
	final, statErr := file.Stat()
	closeErr := file.Close()
	if copyErr != nil || statErr != nil || closeErr != nil || written != source.CapturedBytes || !os.SameFile(initial, final) || final.Size() < source.CapturedBytes {
		return ErrCodexPrefixStale
	}
	if hex.EncodeToString(hasher.Sum(nil)) != source.PrefixDigest {
		return ErrCodexPrefixStale
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

func discoverCodexJSONL(roots []string) ([]string, map[string]string, error) {
	if len(roots) == 0 {
		return nil, nil, errors.New("Codex source roots are required")
	}
	seen := map[string]struct{}{}
	invalid := map[string]string{}
	paths := make([]string, 0)
	for _, root := range roots {
		canonical, err := filepath.Abs(root)
		if err != nil {
			return nil, nil, err
		}
		rootInfo, err := os.Lstat(canonical)
		if err != nil {
			return nil, nil, err
		}
		if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
			return nil, nil, ErrUnsafeCodexSource
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
					invalid[codexUnsafePathAlias(path)] = "symlink_source"
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
			return nil, nil, err
		}
	}
	sort.Strings(paths)
	return paths, invalid, nil
}

func codexUnsafePathAlias(path string) string {
	digest := sha256.Sum256([]byte(path))
	return "unsafe_" + hex.EncodeToString(digest[:16])
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
	copy.sourceVersions = nil
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
