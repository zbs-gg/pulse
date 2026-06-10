// Package retrieve hosts Phase G hybrid retrieval: query router + factual
// (atomic-fact cosine) + empathic (full v3 conditional formula) + chain
// (predecessor BFS) modes.
package retrieve

import (
	"context"
	"regexp"
	"strings"
	"sync"
)

// QueryMode is the retrieval strategy chosen by the router.
type QueryMode string

const (
	ModeFactual  QueryMode = "factual"
	ModeEmpathic QueryMode = "empathic"
	ModeChain    QueryMode = "chain"
	ModeAuto     QueryMode = "auto" // input only — never returned by Classify
)

// RouteDecision is the router's verdict for one query.
type RouteDecision struct {
	Mode       QueryMode
	Confidence float64 // 0..1
	Classifier string  // "heuristic" | "llm" | "default"
	Reasoning  string
}

// LLMClassifier is the optional LLM fallback used when heuristic confidence
// drops below the router threshold. Implementations should return the chosen
// mode, classifier label ("claude-haiku-4-5" etc.), and any error encountered.
type LLMClassifier interface {
	Classify(ctx context.Context, query string) (QueryMode, string, error)
}

// EmotionState is the minimal interface query_router needs from a UserState.
// Production caller passes a UserState type (defined in retrieve/state.go);
// tests can use a stub.
type EmotionState interface {
	HasDominantEmotion(threshold float64) (ok bool, value float64, key string)
	IsBodyStressed() bool
	RecentLifeEventsCount() int
}

// Router classifies queries into modes. Heuristic-first; LLM fallback only
// fires when heuristic confidence < ConfidenceThreshold AND LLM != nil.
type Router struct {
	ConfidenceThreshold float64
	LLM                 LLMClassifier
	cache               sync.Map // map[string]RouteDecision keyed by query+state-sig
}

// NewRouter returns a router with sensible defaults (threshold 0.6, no LLM).
func NewRouter() *Router {
	return &Router{ConfidenceThreshold: 0.6}
}

// Plutchik-10 emotion keywords (Russian + English) — mirrors retrieval_v3.py:267.
//
// Short keywords (≤4 chars) MUST match whole tokens to avoid false positives
// like "рад" matching "парад" or "пра_зд_ник". Long keywords (root forms,
// 5+ chars) are matched as substrings — they're stem-like and rarely collide.
var emoKeywords = map[string][]string{
	"joy":          {"рад", "кайф", "joy", "радост", "счаст"},
	"sadness":      {"груст", "печал", "тоск", "потер", "sad"},
	"anger":        {"зл", "ярос", "раздраж", "бес", "anger", "angry", "mad"},
	"fear":         {"страх", "тревог", "паник", "боюсь", "scared", "fear", "anxious"},
	"trust":        {"довер", "близос", "принят", "trust", "safe"},
	"disgust":      {"отвращ", "брезг", "disgust"},
	"anticipation": {"предвкуш", "надежд", "интерес", "excited", "anticipate"},
	"surprise":     {"удивл", "шок", "недоум", "surprise"},
	"shame":        {"стыд", "смущ", "shame", "заслуживат"},
	"guilt":        {"вин", "сожал", "guilt", "накосяч", "виноват"},
}

// Strong chain markers — explicit causal/temporal trace requests
var chainKeywords = []string{
	// English
	"lead to", "led to", "leads to", "trace", "chain", "sequence",
	"what caused", "how did this", "what's behind",
	// Russian
	"почему", "цепочка", "из-за чего", "что привело", "как так",
	"вследствие", "до этого", "перед этим", "предшеств",
}

// Temporal markers (used to disambiguate factual+temporal vs empathic-temporal)
var temporalKeywords = []string{
	"today", "yesterday", "this week", "last week", "this month", "last month",
	"recently", "just now",
	"сегодня", "вчера", "на этой неделе", "на прошлой неделе",
	"в этом месяце", "в прошлом месяце", "недавно", "только что",
}

// Factual lookup signals — wh-questions about names/dates/lists, summary asks.
// Compiled once at package init for cheap matching.
//
// Note: Go regexp `\b` is ASCII-only, which fails on Cyrillic. We use
// `(?:^|\W|\s)` and `(?:$|\W|\s)` (Unicode whitespace + punctuation)
// for Russian patterns, and standard `\b` for English.
var factualPatterns = compilePatterns([]string{
	// English wh-questions
	`(?i)\bwhen (?:did|was|is)\b`,
	`(?i)\bwhat (?:is|are|was) (?:my|the|a)\b`,
	`(?i)\bwhere (?:did|is|was)\b`,
	`(?i)\bwho (?:is|was|are)\b`,
	`(?i)\bhow many\b`,
	`(?i)\blist (?:of|all)\b`,
	`(?i)\bname of\b`,
	// English context-summary asks
	`(?i)\bwhat (?:has happened|happened)\b`,
	`(?i)\bwhat'?s been (?:going on|happening)\b`,
	`(?i)\btell me about\b`,
	`(?i)\bcatch me up\b`,
	`(?i)\bwhat (?:should|do) i know about\b`,
	// Russian wh-questions — anchored with Unicode-friendly boundaries
	`(?i)(?:^|[^\pL])когда(?:[^\pL]|$)`,
	`(?i)(?:^|[^\pL])какой (?:у меня|у нас|в|на)`,
	`(?i)(?:^|[^\pL])какая (?:у меня|у нас)`,
	`(?i)(?:^|[^\pL])как зовут(?:[^\pL]|$)`,
	`(?i)(?:^|[^\pL])сколько(?:[^\pL]|$)`,
	`(?i)(?:^|[^\pL])список(?:[^\pL]|$)`,
	`(?i)(?:^|[^\pL])имя(?:[^\pL]|$)`,
	`(?i)(?:^|[^\pL])что произошло(?:[^\pL]|$)`,
	`(?i)(?:^|[^\pL])что случилось(?:[^\pL]|$)`,
	`(?i)(?:^|[^\pL])что нового(?:[^\pL]|$)`,
	`(?i)(?:^|[^\pL])расскажи (?:мне )?(?:о|про)(?:[^\pL]|$)`,
	`(?i)(?:^|[^\pL])введи меня в курс(?:[^\pL]|$)`,
})

func compilePatterns(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, regexp.MustCompile(p))
	}
	return out
}

// Classify routes a query. user_state may be nil; that disables state-loaded
// heuristics. ctx is used only when LLM fallback fires.
func (r *Router) Classify(ctx context.Context, query string,
	state EmotionState) RouteDecision {
	cacheKey := strings.ToLower(strings.TrimSpace(query)) + "|" + stateSig(state)
	if v, ok := r.cache.Load(cacheKey); ok {
		return v.(RouteDecision)
	}

	d := r.classifyHeuristic(query, state)
	if d.Confidence < r.ConfidenceThreshold && r.LLM != nil {
		mode, label, err := r.LLM.Classify(ctx, query)
		if err == nil && (mode == ModeFactual || mode == ModeEmpathic || mode == ModeChain) {
			d = RouteDecision{
				Mode:       mode,
				Confidence: 0.7,
				Classifier: label,
				Reasoning:  "llm fallback (heuristic conf below threshold)",
			}
		}
	}

	r.cache.Store(cacheKey, d)
	return d
}

func (r *Router) classifyHeuristic(query string, state EmotionState) RouteDecision {
	q := strings.ToLower(strings.TrimSpace(query))

	// 1. Explicit chain markers — highest priority
	for _, kw := range chainKeywords {
		if strings.Contains(q, kw) {
			return RouteDecision{
				Mode:       ModeChain,
				Confidence: 0.95,
				Classifier: "heuristic",
				Reasoning:  "chain keyword: " + kw,
			}
		}
	}

	// 2. Factual patterns — but downgrade to empathic if also has emotion+temporal
	for _, pat := range factualPatterns {
		if pat.MatchString(q) {
			emoHit := emotionKeywordHit(q)
			tempHit := containsAny(q, temporalKeywords)
			if emoHit != "" && tempHit {
				return RouteDecision{
					Mode:       ModeEmpathic,
					Confidence: 0.8,
					Classifier: "heuristic",
					Reasoning:  "factual pattern + emotion + temporal — empathic-temporal",
				}
			}
			return RouteDecision{
				Mode:       ModeFactual,
				Confidence: 0.9,
				Classifier: "heuristic",
				Reasoning:  "factual pattern: " + pat.String(),
			}
		}
	}

	// 3. State-loaded query — dominant emotion or body load in user_state
	if state != nil {
		if ok, v, _ := state.HasDominantEmotion(0.5); ok {
			return RouteDecision{
				Mode:       ModeEmpathic,
				Confidence: 0.85,
				Classifier: "heuristic",
				Reasoning:  formatStateReason("dominant emotion", v),
			}
		}
		if state.IsBodyStressed() {
			return RouteDecision{
				Mode:       ModeEmpathic,
				Confidence: 0.85,
				Classifier: "heuristic",
				Reasoning:  "user_state.is_body_stressed=true",
			}
		}
		if state.RecentLifeEventsCount() > 0 {
			return RouteDecision{
				Mode:       ModeEmpathic,
				Confidence: 0.7,
				Classifier: "heuristic",
				Reasoning:  "recent_life_events_7d non-empty",
			}
		}
	}

	// 4. Emotion keyword in query alone
	if hit := emotionKeywordHit(q); hit != "" {
		return RouteDecision{
			Mode:       ModeEmpathic,
			Confidence: 0.75,
			Classifier: "heuristic",
			Reasoning:  "emotion keyword: " + hit,
		}
	}

	// 5. Default — empathic with low confidence (triggers LLM fallback)
	return RouteDecision{
		Mode:       ModeEmpathic,
		Confidence: 0.5,
		Classifier: "default",
		Reasoning:  "no heuristic match — default to empathic (conservatism)",
	}
}

func emotionKeywordHit(qLower string) string {
	// Tokenize on whitespace + common punctuation for whole-word checks
	tokens := tokenize(qLower)
	tokenSet := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = true
	}
	for emo, kws := range emoKeywords {
		for _, kw := range kws {
			// Short keywords (≤4 runes) must match whole tokens to avoid
			// "рад" matching "парад" / "fear" matching "fearless".
			if runeLen(kw) <= 4 {
				if tokenSet[kw] {
					return emo + ":" + kw
				}
			} else {
				// Stem-like longer keywords — substring match is OK
				if strings.Contains(qLower, kw) {
					return emo + ":" + kw
				}
			}
		}
	}
	return ""
}

// tokenize splits text on whitespace and common punctuation. Cyrillic-safe.
func tokenize(s string) []string {
	out := make([]string, 0, 8)
	var cur strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' ||
			r == '.' || r == ',' || r == '!' || r == '?' || r == ';' ||
			r == ':' || r == '(' || r == ')' || r == '[' || r == ']' ||
			r == '"' || r == '\'' || r == '«' || r == '»' || r == '—' || r == '-' {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		} else {
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// runeLen counts UTF-8 runes (Cyrillic-safe; len() gives bytes which mislead).
func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func containsAny(qLower string, words []string) bool {
	for _, w := range words {
		if strings.Contains(qLower, w) {
			return true
		}
	}
	return false
}

func stateSig(s EmotionState) string {
	if s == nil {
		return ""
	}
	ok, v, k := s.HasDominantEmotion(0.5)
	if ok {
		return "emo=" + k + ":" + formatF(v)
	}
	return "neutral"
}

func formatStateReason(label string, v float64) string {
	return label + " (" + formatF(v) + ")"
}

func formatF(v float64) string {
	// Two-decimal format without fmt import bloat — keep router lean
	whole := int(v * 100)
	if whole < 0 {
		whole = 0
	}
	if whole > 100 {
		whole = 100
	}
	return string(rune('0'+whole/100)) + "." +
		string(rune('0'+(whole/10)%10)) +
		string(rune('0'+whole%10))
}
