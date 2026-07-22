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
	for _, record := range ordered {
		candidate := append(append([]CodexEvidence(nil), current...), record)
		encoded, err := encodeCodexChunk(rootID, len(chunks), candidate)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= maxBytes {
			current = candidate
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
		encoded, err = encodeCodexChunk(rootID, len(chunks), current)
		if err != nil {
			return nil, err
		}
		if len(encoded) > maxBytes {
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
