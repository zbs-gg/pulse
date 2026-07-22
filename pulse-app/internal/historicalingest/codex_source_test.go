package historicalingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildCodexSnapshotSelectsLatestRootsAndFoldsDescendants(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCodexSession(t, dir, "root_old", "", "2026-07-20T10:00:00Z", messageRecord("old", "2026-07-20T10:01:00Z", "old root"))
	writeCodexSession(t, dir, "root_mid", "", "2026-07-21T10:00:00Z", messageRecord("mid", "2026-07-21T10:01:00Z", "mid root"))
	writeCodexSession(t, dir, "root_new", "", "2026-07-22T10:00:00Z", messageRecord("new", "2026-07-22T10:01:00Z", "new root"))
	writeCodexSession(t, dir, "child_new", "root_new", "2026-07-22T10:02:00Z", messageRecord("child", "2026-07-22T10:03:00Z", "child result"))
	writeCodexSession(t, dir, "importer", "", "2026-07-22T11:00:00Z", messageRecord("importer", "2026-07-22T11:01:00Z", "must be excluded"))

	snapshot, err := BuildCodexSnapshot(CodexSourceOptions{
		Roots:              []string{dir},
		RootLimit:          2,
		Cutoff:             mustTime(t, "2026-07-22T12:00:00Z"),
		ExcludedSessionIDs: map[string]struct{}{"importer": {}},
	})
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if got := []string{snapshot.Trees[0].RootID, snapshot.Trees[1].RootID}; !reflect.DeepEqual(got, []string{"root_new", "root_mid"}) {
		t.Fatalf("roots = %v, want latest valid roots", got)
	}
	if got := snapshot.Trees[0].SessionIDs; !reflect.DeepEqual(got, []string{"root_new", "child_new"}) {
		t.Fatalf("new tree sessions = %v, want root plus child", got)
	}
	if snapshot.RootCount != 2 || snapshot.DescendantCount != 1 {
		t.Fatalf("coverage = roots %d descendants %d", snapshot.RootCount, snapshot.DescendantCount)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), dir) {
		t.Fatalf("snapshot leaks source root: %s", encoded)
	}
}

func TestBuildCodexSnapshotSkipsInvalidGraphAndFailsWhenCohortTooSmall(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCodexSession(t, dir, "valid", "", "2026-07-22T10:00:00Z", messageRecord("valid", "2026-07-22T10:01:00Z", "valid"))
	writeCodexSession(t, dir, "orphan", "missing_parent", "2026-07-22T10:02:00Z", messageRecord("orphan", "2026-07-22T10:03:00Z", "orphan"))
	writeCodexSession(t, dir, "cycle_a", "cycle_b", "2026-07-22T10:04:00Z", messageRecord("a", "2026-07-22T10:05:00Z", "a"))
	writeCodexSession(t, dir, "cycle_b", "cycle_a", "2026-07-22T10:06:00Z", messageRecord("b", "2026-07-22T10:07:00Z", "b"))

	_, err := BuildCodexSnapshot(CodexSourceOptions{Roots: []string{dir}, RootLimit: 2, Cutoff: mustTime(t, "2026-07-22T12:00:00Z")})
	if err == nil || !errors.Is(err, ErrInsufficientValidCodexRoots) {
		t.Fatalf("error = %v, want insufficient valid roots", err)
	}
	var cohortErr *CodexCohortError
	if !errors.As(err, &cohortErr) || cohortErr.InvalidReasons["orphan"] != "unresolved_parent" {
		t.Fatalf("cohort error = %#v, want visible orphan reason", cohortErr)
	}
}

func TestCodexSnapshotAllowsAppendAndRejectsPrefixMutation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeCodexSession(t, dir, "root", "", "2026-07-22T10:00:00Z", messageRecord("one", "2026-07-22T10:01:00Z", "one"))
	snapshot, err := BuildCodexSnapshot(CodexSourceOptions{Roots: []string{dir}, RootLimit: 1, Cutoff: mustTime(t, "2026-07-22T12:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	appended, _ := json.Marshal(messageRecord("two", "2026-07-22T11:00:00Z", "two"))
	if _, err := file.Write(append(appended, '\n')); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if err := VerifyCodexSnapshot(snapshot); err != nil {
		t.Fatalf("append should preserve snapshot: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCodexSnapshot(snapshot); !errors.Is(err, ErrCodexPrefixStale) {
		t.Fatalf("mutation error = %v, want stale prefix", err)
	}
}

func TestParseCodexRecordsAccountsCanonicalExcludedAndUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeCodexSession(t, dir, "root", "", "2026-07-22T10:00:00Z",
		messageRecord("stable", "2026-07-22T10:01:00Z", "remember this"),
		map[string]any{"timestamp": "2026-07-22T10:01:00Z", "type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "mirror"}},
		messageRecord("stable", "2026-07-22T10:01:00Z", "remember this"),
		map[string]any{"timestamp": "2026-07-22T10:02:00Z", "type": "compacted", "payload": map[string]any{"replacement_history": []any{messageRecord("old", "2025-01-01T00:00:00Z", "old")}}},
		map[string]any{"timestamp": "2026-07-22T10:03:00Z", "type": "response_item", "payload": map[string]any{"type": "reasoning", "encrypted_content": "hidden"}},
		map[string]any{"timestamp": "2026-07-22T10:04:00Z", "type": "future_record", "payload": map[string]any{"value": "unknown"}},
	)
	result, err := ParseCodexFile(path, CodexParseOptions{ExpectedSessionID: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Text != "remember this" {
		t.Fatalf("evidence = %#v, want one canonical message", result.Evidence)
	}
	if result.Coverage.Included != 1 || result.Coverage.Excluded != 5 || result.Coverage.Blocking != 1 || result.Coverage.Total != 7 {
		t.Fatalf("coverage = %+v, want 1 included 5 excluded 1 blocking", result.Coverage)
	}
	if result.Exclusions["session_metadata"] != 1 || result.Exclusions["mirrored_event"] != 1 || result.Exclusions["duplicate_record"] != 1 || result.Exclusions["compacted_history"] != 1 || result.Exclusions["hidden_reasoning"] != 1 {
		t.Fatalf("exclusions = %#v", result.Exclusions)
	}
}

func TestParseCodexRecordsExcludesForkInheritanceBeforeOwnBoundary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-root.jsonl")
	writeCodexJSONL(t, path,
		sessionMetaRecord("inherited", "", "2025-01-01T00:00:00Z"),
		messageRecord("old", "2025-01-01T00:01:00Z", "inherited text"),
		sessionMetaRecord("root", "", "2026-07-22T10:00:00Z"),
		messageRecord("own", "2026-07-22T10:01:00Z", "owned text"),
	)
	result, err := ParseCodexFile(path, CodexParseOptions{ExpectedSessionID: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Text != "owned text" {
		t.Fatalf("evidence = %#v", result.Evidence)
	}
	if result.Exclusions["pre_root_inherited"] != 2 || result.Exclusions["session_metadata"] != 1 {
		t.Fatalf("exclusions = %#v", result.Exclusions)
	}
}

func TestParseCodexRecordsDeduplicatesAttachmentIdentityAndCountsOccurrences(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	image := "data:image/png;base64,c3ludGhldGlj"
	withImage := func(id, at, text string) map[string]any {
		record := messageRecord(id, at, text)
		payload := record["payload"].(map[string]any)
		payload["content"] = []map[string]any{
			{"type": "input_text", "text": text},
			{"type": "input_image", "image_url": image},
		}
		return record
	}
	path := writeCodexSession(t, dir, "root", "", "2026-07-22T10:00:00Z",
		withImage("one", "2026-07-22T10:01:00Z", "first"),
		withImage("two", "2026-07-22T10:02:00Z", "second"),
	)
	result, err := ParseCodexFile(path, CodexParseOptions{ExpectedSessionID: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AttachmentOccurrences) != 1 {
		t.Fatalf("attachment identities = %#v, want one", result.AttachmentOccurrences)
	}
	for digest, count := range result.AttachmentOccurrences {
		if len(digest) != 64 || count != 2 {
			t.Fatalf("attachment %s count = %d", digest, count)
		}
	}
}

func TestBuildCodexSnapshotRejectsSymlinkSource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := writeCodexSession(t, dir, "target", "", "2026-07-22T10:00:00Z", messageRecord("one", "2026-07-22T10:01:00Z", "one"))
	link := filepath.Join(dir, "rollout-2026-07-22T10-00-00-root.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ParseCodexFile(link, CodexParseOptions{ExpectedSessionID: "root"}); !errors.Is(err, ErrUnsafeCodexSource) {
		t.Fatalf("symlink error = %v, want unsafe source", err)
	}
}

func TestChunkCodexEvidenceIsDeterministicAndBounded(t *testing.T) {
	t.Parallel()

	records := []CodexEvidence{
		{SourceAlias: "source_0000000000000001", Locator: "record:1", Timestamp: mustTime(t, "2026-07-22T10:00:00Z"), Kind: "message", Role: "user", Text: strings.Repeat("a", 20)},
		{SourceAlias: "source_0000000000000001", Locator: "record:2", Timestamp: mustTime(t, "2026-07-22T10:01:00Z"), Kind: "message", Role: "assistant", Text: strings.Repeat("b", 20)},
		{SourceAlias: "source_0000000000000002", Locator: "record:1", Timestamp: mustTime(t, "2026-07-22T10:02:00Z"), Kind: "tool_result", Text: strings.Repeat("c", 20)},
	}
	first, err := ChunkCodexEvidence("root", records, 360)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ChunkCodexEvidence("root", records, 360)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || len(first) < 2 {
		t.Fatalf("chunks are not deterministic/bounded: %#v", first)
	}
	for _, chunk := range first {
		if chunk.EncodedBytes > 360 {
			t.Fatalf("chunk bytes = %d, max 360", chunk.EncodedBytes)
		}
	}
	if _, err := ChunkCodexEvidence("root", []CodexEvidence{{SourceAlias: "source_0000000000000001", Locator: "record:1", Text: strings.Repeat("x", 1000)}}, 200); !errors.Is(err, ErrCodexEvidenceTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
}

func writeCodexSession(t *testing.T, dir, id, parent, timestamp string, records ...map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("rollout-%s-%s.jsonl", strings.NewReplacer(":", "-", "T", "_").Replace(timestamp), id))
	all := append([]map[string]any{sessionMetaRecord(id, parent, timestamp)}, records...)
	writeCodexJSONL(t, path, all...)
	return path
}

func sessionMetaRecord(id, parent, timestamp string) map[string]any {
	meta := map[string]any{
		"timestamp": timestamp,
		"type":      "session_meta",
		"payload": map[string]any{
			"id":         id,
			"session_id": id,
			"timestamp":  timestamp,
			"source":     "codex",
		},
	}
	if parent != "" {
		meta["payload"].(map[string]any)["parent_thread_id"] = parent
		meta["payload"].(map[string]any)["source"] = map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": parent}}}
	}
	return meta
}

func writeCodexJSONL(t *testing.T, path string, records ...map[string]any) {
	t.Helper()
	var body strings.Builder
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		body.Write(encoded)
		body.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func messageRecord(id, timestamp, text string) map[string]any {
	return map[string]any{
		"timestamp": timestamp,
		"type":      "response_item",
		"payload": map[string]any{
			"id":   id,
			"type": "message",
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": text,
			}},
		},
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
