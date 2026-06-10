package capture

import (
	"encoding/json"
	"time"
)

type ActorRef struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Display string `json:"display,omitempty"`
}

type MediaRef struct {
	Kind     string `json:"kind"`
	URL      string `json:"url,omitempty"`
	Filename string `json:"filename,omitempty"`
	Duration int    `json:"duration_sec,omitempty"`
}

type Observation struct {
	SourceKind  string          `json:"source_kind"`
	SourceID    string          `json:"source_id"`
	ContentHash string          `json:"content_hash"`
	Version     int             `json:"version"`
	Scope       string          `json:"scope"`
	CapturedAt  time.Time       `json:"captured_at"`
	ObservedAt  time.Time       `json:"observed_at"`
	Actors      []ActorRef      `json:"actors"`
	ContentText string          `json:"content_text,omitempty"`
	MediaRefs   []MediaRef      `json:"media_refs,omitempty"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
	RawJSON     json.RawMessage `json:"raw_json,omitempty"`
	Redacted    bool            `json:"-"`
}
