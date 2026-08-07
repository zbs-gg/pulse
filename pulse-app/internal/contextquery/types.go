package contextquery

import (
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

const SchemaVersion = "pulse.context.v1"

type ContextQueryRequest struct {
	Query        string              `json:"query"`
	Mode         string              `json:"mode,omitempty"`
	TopK         int                 `json:"top_k,omitempty"`
	Scope        string              `json:"scope,omitempty"`
	Audience     string              `json:"audience,omitempty"`
	PrivacyFloor string              `json:"privacy_floor,omitempty"`
	IncludeTrace bool                `json:"include_trace,omitempty"`
	UserState    *retrieve.UserState `json:"user_state,omitempty"`
	// GraphMode overrides the service default temporal entity-graph mode for this
	// call ("" = use service default, "off"/"anchored"/"walk").
	GraphMode   string   `json:"graph_mode,omitempty"`
	DomainHints []string `json:"domain_hints,omitempty"`
	// DomainsAllowed restricts facts/events to the listed domains
	// (real | fiction_content | fiction_meta | meta_authorial). Empty/nil
	// means no filter — caller is responsible for not conflating fiction
	// with reality. Heart normal chat must send ["real"] (or
	// ["real","meta_authorial"]) so book-canon doesn't leak into
	// answers about real life.
	DomainsAllowed []string `json:"domains_allowed,omitempty"`
}

type ContextResult struct {
	SchemaVersion           string                      `json:"schema_version"`
	Query                   string                      `json:"query"`
	ModeUsed                string                      `json:"mode_used"`
	Scope                   string                      `json:"scope"`
	Facts                   []ContextFact               `json:"facts"`
	EmotionalAnchors        []ContextEmotionalAnchor    `json:"emotional_anchors"`
	Events                  []ContextEvent              `json:"events"`
	Entities                []ContextEntity             `json:"entities"`
	Relations               []ContextRelation           `json:"relations"`
	Forbidden               []ContextRedaction          `json:"forbidden"`
	Private                 []ContextRedaction          `json:"private"`
	Uncertainty             []ContextUncertainty        `json:"uncertainty"`
	ImportanceQuestions     []ContextImportanceQuestion `json:"importance_questions"`
	Trace                   *ContextTrace               `json:"trace,omitempty"`
	CurrentEmotionalContext store.CurrentEmotionContext `json:"current_emotional_context"`
	EmotionalStateSource    string                      `json:"emotional_state_source"`
	EffectiveMoodVector     map[string]float64          `json:"effective_mood_vector"`
}

type ContextFact struct {
	ID           int64   `json:"id"`
	Kind         string  `json:"kind"`
	Text         string  `json:"text"`
	Score        float64 `json:"score"`
	Confidence   float64 `json:"confidence"`
	Provenance   string  `json:"provenance"`
	EvidenceIDs  []int64 `json:"evidence_ids"`
	SourceScope  string  `json:"source_scope"`
	PrivacyFloor string  `json:"privacy_floor"`
	DoNotProbe   bool    `json:"do_not_probe"`
	// Domain marks whether this fact describes real life or fictional /
	// creative content (fiction_content = work canon,
	// fiction_meta = meta-talk about producing the work,
	// meta_authorial = the author's commentary on the work/process).
	// Always populated; defaults to "real" for legacy rows.
	Domain string `json:"domain"`
}

type ContextEvent struct {
	ID           int64   `json:"id"`
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	Summary      string  `json:"summary"`
	Score        float64 `json:"score"`
	Confidence   float64 `json:"confidence"`
	Provenance   string  `json:"provenance"`
	EvidenceIDs  []int64 `json:"evidence_ids"`
	SourceScope  string  `json:"source_scope"`
	PrivacyFloor string  `json:"privacy_floor"`
	DoNotProbe   bool    `json:"do_not_probe"`
	Domain       string  `json:"domain"`
}

type ContextEntity struct {
	ID            int64    `json:"id"`
	Kind          string   `json:"kind"`
	CanonicalName string   `json:"canonical_name"`
	Summary       string   `json:"summary"`
	Score         float64  `json:"score"`
	Confidence    float64  `json:"confidence"`
	Provenance    string   `json:"provenance"`
	EvidenceIDs   []int64  `json:"evidence_ids"`
	SourceScope   string   `json:"source_scope"`
	PrivacyFloor  string   `json:"privacy_floor"`
	DoNotProbe    bool     `json:"do_not_probe"`
	Aliases       []string `json:"aliases,omitempty"`
}

type ContextRelation struct {
	ID           int64   `json:"id"`
	Kind         string  `json:"kind"`
	FromEntityID int64   `json:"from_entity_id"`
	ToEntityID   int64   `json:"to_entity_id"`
	Summary      string  `json:"summary"`
	Score        float64 `json:"score"`
	Confidence   float64 `json:"confidence"`
	Provenance   string  `json:"provenance"`
	EvidenceIDs  []int64 `json:"evidence_ids"`
	SourceScope  string  `json:"source_scope"`
	PrivacyFloor string  `json:"privacy_floor"`
	DoNotProbe   bool    `json:"do_not_probe"`
}

type ContextEmotionalAnchor struct {
	EventID      int64              `json:"event_id"`
	Summary      string             `json:"summary"`
	Emotions     map[string]float64 `json:"emotions"`
	Score        float64            `json:"score"`
	Confidence   float64            `json:"confidence"`
	Provenance   string             `json:"provenance"`
	EvidenceIDs  []int64            `json:"evidence_ids"`
	SourceScope  string             `json:"source_scope"`
	PrivacyFloor string             `json:"privacy_floor"`
	DoNotProbe   bool               `json:"do_not_probe"`
}

type ContextRedaction struct {
	SubjectKind string `json:"subject_kind"`
	SubjectID   int64  `json:"subject_id"`
	Reason      string `json:"reason"`
	Policy      string `json:"policy"`
}

type ContextUncertainty struct {
	SubjectKind string  `json:"subject_kind"`
	SubjectID   int64   `json:"subject_id"`
	Question    string  `json:"question"`
	Confidence  float64 `json:"confidence"`
}

type ContextImportanceQuestion struct {
	ID              int64  `json:"id"`
	SubjectEntityID *int64 `json:"subject_entity_id,omitempty"`
	QuestionText    string `json:"question_text"`
	State           string `json:"state"`
}

type ContextTrace struct {
	Router     map[string]any `json:"router,omitempty"`
	Retrieval  map[string]any `json:"retrieval,omitempty"`
	Redactions []string       `json:"redactions,omitempty"`
}
