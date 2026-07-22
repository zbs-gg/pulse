package historicalingest

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	defaultCodexRecordBytes = 8 << 20
	maxNormalizedTextBytes  = 32 << 10
)

var (
	ErrUnsafeCodexSource     = errors.New("unsafe Codex source")
	ErrCodexPrefixStale      = errors.New("Codex source prefix is stale")
	ErrCodexEvidenceTooLarge = errors.New("Codex evidence record exceeds the work-unit budget")
	unsafeEvidencePattern    = regexp.MustCompile(`(?i)(/users/|/home/|\\users\\|api[_-]?key|access[_-]?token|secret[_-]?key|authorization:\s*bearer)`)
)

type CodexParseOptions struct {
	ExpectedSessionID string
	CapturedBytes     int64
	MaxRecordBytes    int
	SourceAlias       string
}

type CodexCoverage struct {
	Total    int64 `json:"total"`
	Included int64 `json:"included"`
	Excluded int64 `json:"excluded"`
	Blocking int64 `json:"blocking"`
}

type CodexEvidence struct {
	SourceAlias       string    `json:"source_alias"`
	Locator           string    `json:"locator"`
	Timestamp         time.Time `json:"timestamp"`
	Kind              string    `json:"kind"`
	Role              string    `json:"role,omitempty"`
	Text              string    `json:"text,omitempty"`
	AttachmentDigests []string  `json:"attachment_digests,omitempty"`
}

type CodexParseResult struct {
	SessionID             string
	ParentThreadID        string
	ForkedFromID          string
	LatestOwnedAt         time.Time
	CapturedBytes         int64
	PrefixDigest          string
	Coverage              CodexCoverage
	Exclusions            map[string]int64
	BlockingKinds         map[string]int64
	AttachmentOccurrences map[string]int64
	Evidence              []CodexEvidence
}

type codexEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexSessionMeta struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"session_id"`
	ParentThreadID string          `json:"parent_thread_id"`
	ForkedFromID   string          `json:"forked_from_id"`
	Timestamp      string          `json:"timestamp"`
	Source         json.RawMessage `json:"source"`
}

type codexStructuredSource struct {
	Subagent struct {
		ThreadSpawn struct {
			ParentThreadID string `json:"parent_thread_id"`
		} `json:"thread_spawn"`
	} `json:"subagent"`
}

type codexResponseItem struct {
	ID      string          `json:"id"`
	CallID  string          `json:"call_id"`
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Output  json.RawMessage `json:"output"`
}

type codexContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
	Path     string `json:"path"`
}

func ParseCodexFile(path string, options CodexParseOptions) (CodexParseResult, error) {
	file, initial, err := openRegularCodexFile(path)
	if err != nil {
		return CodexParseResult{}, err
	}
	defer file.Close()

	captured := options.CapturedBytes
	if captured == 0 {
		captured = initial.Size()
	}
	if captured < 0 || initial.Size() < captured {
		return CodexParseResult{}, ErrCodexPrefixStale
	}
	maxRecord := options.MaxRecordBytes
	if maxRecord <= 0 {
		maxRecord = defaultCodexRecordBytes
	}

	hasher := sha256.New()
	reader := bufio.NewReaderSize(io.TeeReader(io.LimitReader(file, captured), hasher), 64<<10)
	result := CodexParseResult{
		CapturedBytes:         captured,
		Exclusions:            map[string]int64{},
		BlockingKinds:         map[string]int64{},
		AttachmentOccurrences: map[string]int64{},
	}
	seen := map[string]struct{}{}
	boundaryFound := options.ExpectedSessionID == ""
	lineNumber := 0

	for {
		line, oversized, readErr := readCodexLine(reader, maxRecord)
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		lineNumber++
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return CodexParseResult{}, readErr
			}
			continue
		}
		result.Coverage.Total++
		if oversized {
			if bytes.Contains(trimmed, []byte(`"type":"compacted"`)) || bytes.Contains(trimmed, []byte(`"type": "compacted"`)) {
				result.exclude("compacted_history")
			} else {
				result.block("oversized_record")
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return CodexParseResult{}, readErr
			}
			continue
		}

		var envelope codexEnvelope
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			result.block("malformed_json")
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return CodexParseResult{}, readErr
			}
			continue
		}
		timestamp, timestampOK := parseCodexTime(envelope.Timestamp)
		if !timestampOK {
			result.block("invalid_timestamp")
			continue
		}

		if envelope.Type == "session_meta" {
			var meta codexSessionMeta
			if err := json.Unmarshal(envelope.Payload, &meta); err != nil {
				result.block("invalid_session_metadata")
				continue
			}
			id := meta.ID
			if id == "" {
				id = meta.SessionID
			}
			if options.ExpectedSessionID != "" && id != options.ExpectedSessionID && !boundaryFound {
				result.exclude("pre_root_inherited")
				continue
			}
			if options.ExpectedSessionID == "" || id == options.ExpectedSessionID {
				boundaryFound = true
				result.SessionID = id
				result.ParentThreadID = meta.ParentThreadID
				if result.ParentThreadID == "" {
					var structured codexStructuredSource
					if len(meta.Source) > 0 && meta.Source[0] == '{' && json.Unmarshal(meta.Source, &structured) == nil {
						result.ParentThreadID = structured.Subagent.ThreadSpawn.ParentThreadID
					}
				}
				result.ForkedFromID = meta.ForkedFromID
				result.LatestOwnedAt = timestamp
				result.exclude("session_metadata")
				continue
			}
		}

		if !boundaryFound {
			result.exclude("pre_root_inherited")
			continue
		}
		if timestamp.After(result.LatestOwnedAt) {
			result.LatestOwnedAt = timestamp
		}

		switch envelope.Type {
		case "response_item":
			evidence, key, disposition := normalizeCodexResponse(envelope, timestamp, lineNumber, options.SourceAlias)
			if disposition != "" {
				if strings.HasPrefix(disposition, "unknown_response_item:") || disposition == "malformed_response_item" {
					result.block(disposition)
				} else {
					result.exclude(disposition)
				}
				break
			}
			if key == "" {
				result.block("unkeyed_response_item")
				break
			}
			if _, exists := seen[key]; exists {
				result.exclude("duplicate_record")
				break
			}
			seen[key] = struct{}{}
			result.Evidence = append(result.Evidence, evidence)
			for _, digest := range evidence.AttachmentDigests {
				result.AttachmentOccurrences[digest]++
			}
			result.Coverage.Included++
		case "event_msg":
			result.exclude("mirrored_event")
		case "compacted":
			result.exclude("compacted_history")
		case "turn_context":
			result.exclude("runtime_context")
		case "world_state":
			result.exclude("world_state")
		case "inter_agent_communication_metadata":
			result.exclude("transport_metadata")
		default:
			result.block("unknown_top_level:" + envelope.Type)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return CodexParseResult{}, readErr
		}
	}

	if options.ExpectedSessionID != "" && (!boundaryFound || result.SessionID != options.ExpectedSessionID) {
		return CodexParseResult{}, fmt.Errorf("%w: session boundary %s not found", ErrUnsafeCodexSource, options.ExpectedSessionID)
	}
	final, err := file.Stat()
	if err != nil || !os.SameFile(initial, final) || final.Size() < captured {
		return CodexParseResult{}, ErrCodexPrefixStale
	}
	result.PrefixDigest = hex.EncodeToString(hasher.Sum(nil))
	return result, nil
}

func (result *CodexParseResult) exclude(reason string) {
	result.Coverage.Excluded++
	result.Exclusions[reason]++
}

func (result *CodexParseResult) block(reason string) {
	result.Coverage.Blocking++
	result.BlockingKinds[reason]++
}

func normalizeCodexResponse(envelope codexEnvelope, timestamp time.Time, lineNumber int, sourceAlias string) (CodexEvidence, string, string) {
	var item codexResponseItem
	if err := json.Unmarshal(envelope.Payload, &item); err != nil {
		return CodexEvidence{}, "", "malformed_response_item"
	}
	key := item.ID
	if key == "" {
		key = item.CallID
	}
	if key != "" {
		key = item.Type + ":" + key
	} else {
		digest := sha256.Sum256(append([]byte(envelope.Timestamp+":"+item.Type+":"), envelope.Payload...))
		key = item.Type + ":" + hex.EncodeToString(digest[:])
	}
	base := CodexEvidence{
		SourceAlias: sourceAlias,
		Locator:     fmt.Sprintf("record:%d", lineNumber),
		Timestamp:   timestamp,
	}

	switch item.Type {
	case "message":
		if item.Role != "user" && item.Role != "assistant" {
			return CodexEvidence{}, key, "non_memory_role"
		}
		text, attachments, ok := normalizeCodexContent(item.Content)
		if !ok || text == "" {
			return CodexEvidence{}, key, "empty_or_unsafe_message"
		}
		base.Kind = "message"
		base.Role = item.Role
		base.Text = text
		base.AttachmentDigests = attachments
		return base, key, ""
	case "agent_message":
		text, attachments, ok := normalizeCodexContent(item.Content)
		if !ok || text == "" {
			return CodexEvidence{}, key, "empty_or_unsafe_message"
		}
		base.Kind = "message"
		base.Role = "assistant"
		base.Text = text
		base.AttachmentDigests = attachments
		return base, key, ""
	case "function_call_output", "custom_tool_call_output":
		text, ok := normalizeToolOutput(item.Output)
		if !ok {
			return CodexEvidence{}, key, "unsafe_tool_output"
		}
		base.Kind = "tool_result"
		base.Text = text
		return base, key, ""
	case "reasoning":
		return CodexEvidence{}, key, "hidden_reasoning"
	case "function_call", "custom_tool_call":
		return CodexEvidence{}, key, "tool_invocation"
	default:
		return CodexEvidence{}, key, "unknown_response_item:" + item.Type
	}
}

func normalizeCodexContent(raw json.RawMessage) (string, []string, bool) {
	var parts []codexContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil, false
	}
	texts := make([]string, 0, len(parts))
	attachments := make([]string, 0)
	for _, part := range parts {
		switch part.Type {
		case "input_text", "output_text", "summary_text":
			text := strings.TrimSpace(part.Text)
			if text == "" || len(text) > maxNormalizedTextBytes || unsafeEvidencePattern.MatchString(text) {
				continue
			}
			texts = append(texts, text)
		case "input_image", "image":
			identity := part.ImageURL
			if identity == "" {
				identity = part.Path
			}
			if identity != "" {
				digest := sha256.Sum256([]byte(identity))
				attachments = append(attachments, hex.EncodeToString(digest[:]))
			}
		}
	}
	sort.Strings(attachments)
	return strings.Join(texts, "\n"), compactStrings(attachments), true
}

func normalizeToolOutput(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	var text string
	if stringValue, ok := value.(string); ok {
		text = stringValue
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", false
		}
		text = string(encoded)
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxNormalizedTextBytes || unsafeEvidencePattern.MatchString(text) {
		return "", false
	}
	return text, true
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func parseCodexTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func readCodexLine(reader *bufio.Reader, maxBytes int) ([]byte, bool, error) {
	var kept bytes.Buffer
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if kept.Len() < maxBytes {
			remaining := maxBytes - kept.Len()
			if len(fragment) <= remaining {
				kept.Write(fragment)
			} else {
				kept.Write(fragment[:remaining])
				oversized = true
			}
		} else if len(fragment) > 0 {
			oversized = true
		}
		if err == nil {
			return kept.Bytes(), oversized, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return kept.Bytes(), oversized, err
	}
}

func openRegularCodexFile(path string) (*os.File, os.FileInfo, error) {
	initial, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: lstat source: %v", ErrUnsafeCodexSource, err)
	}
	if !initial.Mode().IsRegular() || initial.Mode()&os.ModeSymlink != 0 || hasMultipleLinks(initial) {
		return nil, nil, ErrUnsafeCodexSource
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open source: %v", ErrUnsafeCodexSource, err)
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(initial, opened) || hasMultipleLinks(opened) {
		file.Close()
		return nil, nil, ErrUnsafeCodexSource
	}
	return file, opened, nil
}

func hasMultipleLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}
