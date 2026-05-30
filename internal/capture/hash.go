package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// ComputeContentHash returns a deterministic sha256 of content_text joined with
// a canonical JSON encoding of metadata (keys sorted). Used to detect edits.
func ComputeContentHash(contentText string, metadata map[string]any) string {
	h := sha256.New()
	h.Write([]byte(contentText))
	h.Write([]byte{0x1f})

	if len(metadata) > 0 {
		keys := make([]string, 0, len(metadata))
		for k := range metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		canonical := make([][2]any, 0, len(keys))
		for _, k := range keys {
			canonical = append(canonical, [2]any{k, metadata[k]})
		}
		b, _ := json.Marshal(canonical)
		h.Write(b)
	}

	return hex.EncodeToString(h.Sum(nil))
}
