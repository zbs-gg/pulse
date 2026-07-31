package historicalingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

type CodexEvidenceChunk struct {
	RootID       string          `json:"root_id"`
	Ordinal      int             `json:"ordinal"`
	Digest       string          `json:"digest"`
	EncodedBytes int             `json:"encoded_bytes"`
	Records      []CodexEvidence `json:"records"`
}

type codexChunkPayload struct {
	RootID  string          `json:"root_id"`
	Ordinal int             `json:"ordinal"`
	Records []CodexEvidence `json:"records"`
}

func ChunkCodexEvidence(rootID string, records []CodexEvidence, maxBytes int) ([]CodexEvidenceChunk, error) {
	if rootID == "" || maxBytes < 128 {
		return nil, errors.New("Codex chunk contract is invalid")
	}
	ordered := append([]CodexEvidence(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].Timestamp.Equal(ordered[j].Timestamp) {
			return ordered[i].Timestamp.Before(ordered[j].Timestamp)
		}
		if ordered[i].SourceAlias != ordered[j].SourceAlias {
			return ordered[i].SourceAlias < ordered[j].SourceAlias
		}
		return ordered[i].Locator < ordered[j].Locator
	})
	if len(ordered) == 0 {
		return []CodexEvidenceChunk{}, nil
	}

	chunks := make([]CodexEvidenceChunk, 0)
	current := make([]CodexEvidence, 0)
	currentBytes, err := emptyCodexChunkBytes(rootID, 0)
	if err != nil {
		return nil, err
	}
	for _, record := range ordered {
		encodedRecord, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		separatorBytes := 0
		if len(current) > 0 {
			separatorBytes = 1
		}
		if currentBytes+separatorBytes+len(encodedRecord) <= maxBytes {
			current = append(current, record)
			currentBytes += separatorBytes + len(encodedRecord)
			continue
		}
		if len(current) == 0 {
			return nil, ErrCodexEvidenceTooLarge
		}
		chunk, err := finalizeCodexChunk(rootID, len(chunks), current)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
		current = []CodexEvidence{record}
		currentBytes, err = emptyCodexChunkBytes(rootID, len(chunks))
		if err != nil {
			return nil, err
		}
		currentBytes += len(encodedRecord)
		if currentBytes > maxBytes {
			return nil, ErrCodexEvidenceTooLarge
		}
	}
	if len(current) > 0 {
		chunk, err := finalizeCodexChunk(rootID, len(chunks), current)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func emptyCodexChunkBytes(rootID string, ordinal int) (int, error) {
	empty, err := encodeCodexChunk(rootID, ordinal, []CodexEvidence{})
	if err != nil {
		return 0, err
	}
	return len(empty) - 2, nil
}

func finalizeCodexChunk(rootID string, ordinal int, records []CodexEvidence) (CodexEvidenceChunk, error) {
	encoded, err := encodeCodexChunk(rootID, ordinal, records)
	if err != nil {
		return CodexEvidenceChunk{}, err
	}
	digest := sha256.Sum256(encoded)
	return CodexEvidenceChunk{
		RootID:       rootID,
		Ordinal:      ordinal,
		Digest:       hex.EncodeToString(digest[:]),
		EncodedBytes: len(encoded),
		Records:      append([]CodexEvidence(nil), records...),
	}, nil
}

func encodeCodexChunk(rootID string, ordinal int, records []CodexEvidence) ([]byte, error) {
	return json.Marshal(codexChunkPayload{RootID: rootID, Ordinal: ordinal, Records: records})
}
