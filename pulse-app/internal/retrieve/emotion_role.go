package retrieve

import "strings"

// EmotionRole is affective routing intent. It is deliberately separate from
// emotion similarity: the same shame state can ask for origins, soothing,
// repair, or boundaries.
type EmotionRole string

const (
	EmotionRoleExplain       EmotionRole = "explain"
	EmotionRoleSoothe        EmotionRole = "soothe"
	EmotionRoleContrast      EmotionRole = "contrast"
	EmotionRoleRepair        EmotionRole = "repair"
	EmotionRoleWarn          EmotionRole = "warn"
	EmotionRoleRelationTruth EmotionRole = "relation-truth"
	EmotionRoleTaskFocus     EmotionRole = "task-focus"
	EmotionRoleBoundaryCheck EmotionRole = "boundary-check"
)

// EmotionRoleDecision is returned with retrieval traces so callers can see
// which affective intent was inferred and why.
type EmotionRoleDecision struct {
	Role               EmotionRole `json:"role"`
	Confidence         float64     `json:"confidence"`
	Classifier         string      `json:"classifier"`
	Reasoning          string      `json:"reasoning,omitempty"`
	StateEmotion       string      `json:"state_emotion,omitempty"`
	Fragile            bool        `json:"fragile,omitempty"`
	ExplicitPainIntent bool        `json:"explicit_pain_intent,omitempty"`
}

// SurfaceabilityAction is the minimal safety/usefulness action retrieval took
// after ranking. Empty means no intervention.
type SurfaceabilityAction string

const (
	SurfaceabilityPairWithRepairAnchor SurfaceabilityAction = "pair_with_repair_anchor"
	SurfaceabilitySuppress             SurfaceabilityAction = "suppress"
)

var (
	boundaryRoleKeywords = []string{
		"границ", "граница", "нельзя", "можно ли", "стоп", "отказать",
		"не хочу", "слишком", "boundary", "boundaries", "say no", "limit",
	}
	warnRoleKeywords = []string{
		"опас", "риск", "чем рискую", "предупреди", "красн", "ошибк",
		"что может пойти не так", "danger", "risk", "warn", "red flag",
	}
	relationTruthRoleKeywords = []string{
		"на самом деле", "между нами", "что у меня с", "что у нас с",
		"отношен", "где мы", "where do we stand", "between us",
		"relationship", "relation",
	}
	taskFocusRoleKeywords = []string{
		"что делать", "следующий шаг", "план", "задач", "сфокус",
		"приоритет", "дедлайн", "next step", "plan", "task", "focus",
		"ship", "todo",
	}
	repairRoleKeywords = []string{
		"почин", "исправ", "помир", "извин", "восстанов", "заглад",
		"repair", "fix", "make it right", "apologize", "restore",
	}
	sootheRoleKeywords = []string{
		"что поможет", "помоги", "поддерж", "успокой", "заземл",
		"как пережить", "мне плохо", "побудь", "стабилиз", "help",
		"soothe", "support", "calm", "ground me", "what helps",
	}
	contrastRoleKeywords = []string{
		"сравни", "контраст", "чем отличается", "раньше", "до и после",
		"тогда и сейчас", "compare", "contrast", "difference", "before and after",
	}
	explainRoleKeywords = []string{
		"почему", "откуда", "из-за чего", "разбери", "причин",
		"что это значит", "почему меня", "why", "where does", "explain",
		"what caused", "origin",
	}
	painIntentKeywords = []string{
		"боль", "боли", "болит", "рана", "травм", "pain", "wound", "trauma",
	}
	explicitPainVerbs = []string{
		"расскажи", "покажи", "разбери", "почему", "откуда", "найди",
		"tell me", "show me", "explain", "trace", "find",
	}
)

// InferEmotionRole returns a deterministic MVP role from query + current_state.
// No LLM calls and no scoring constants are involved.
func InferEmotionRole(query string, state *UserState) EmotionRoleDecision {
	q := strings.ToLower(strings.TrimSpace(query))
	_, _, stateEmotion := dominantEmotion(state, 0.5)

	role := EmotionRoleExplain
	reason := "default explain"
	conf := 0.55

	switch {
	case containsAny(q, boundaryRoleKeywords):
		role, reason, conf = EmotionRoleBoundaryCheck, "boundary intent keyword", 0.9
	case containsAny(q, warnRoleKeywords):
		role, reason, conf = EmotionRoleWarn, "risk/warn intent keyword", 0.9
	case containsAny(q, relationTruthRoleKeywords):
		role, reason, conf = EmotionRoleRelationTruth, "relationship truth intent keyword", 0.92
	case containsAny(q, repairRoleKeywords):
		role, reason, conf = EmotionRoleRepair, "repair intent keyword", 0.88
	case containsAny(q, sootheRoleKeywords):
		role, reason, conf = EmotionRoleSoothe, "soothing/support intent keyword", 0.9
	case containsAny(q, contrastRoleKeywords):
		role, reason, conf = EmotionRoleContrast, "contrast intent keyword", 0.86
	case containsAny(q, taskFocusRoleKeywords):
		role, reason, conf = EmotionRoleTaskFocus, "task/focus intent keyword", 0.85
	case containsAny(q, explainRoleKeywords):
		role, reason, conf = EmotionRoleExplain, "explanation/origin intent keyword", 0.9
	case state != nil && state.IsBodyStressed():
		role, reason, conf = EmotionRoleSoothe, "body-stressed state without stronger intent", 0.72
	}

	return EmotionRoleDecision{
		Role:               role,
		Confidence:         conf,
		Classifier:         "rule-based",
		Reasoning:          reason,
		StateEmotion:       stateEmotion,
		Fragile:            IsFragileState(state),
		ExplicitPainIntent: HasExplicitPainIntent(q),
	}
}

func dominantEmotion(state *UserState, threshold float64) (bool, float64, string) {
	if state == nil {
		return false, 0, ""
	}
	return state.HasDominantEmotion(threshold)
}

// IsFragileState is intentionally conservative: it only marks strong body load
// or high pain-family emotions, so neutral task queries are not over-protected.
func IsFragileState(state *UserState) bool {
	if state == nil {
		return false
	}
	if state.IsBodyStressed() {
		return true
	}
	for _, emo := range []string{"sadness", "fear", "shame", "guilt"} {
		if state.MoodVector != nil && state.MoodVector[emo] >= 0.75 {
			return true
		}
	}
	return false
}

func HasExplicitPainIntent(qLower string) bool {
	if qLower == "" || !containsAny(qLower, painIntentKeywords) {
		return false
	}
	return containsAny(qLower, explicitPainVerbs)
}

func isPainEmotionVector(em []float32) bool {
	if len(em) < 10 {
		return false
	}
	return em[1] >= 0.6 || em[2] >= 0.6 || em[3] >= 0.6 || em[8] >= 0.6 || em[9] >= 0.6
}

func isRepairEmotionVector(em []float32) bool {
	if len(em) < 10 {
		return false
	}
	return !isPainEmotionVector(em) && (em[0] >= 0.45 || em[4] >= 0.55)
}

func allPainEventIDs(ids []int64, emotions map[int64][]float32) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !isPainEmotionVector(emotions[id]) {
			return false
		}
	}
	return true
}
