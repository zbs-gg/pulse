package retrieve

import "math"

// Pulse v3 conditional-boost terms, ported 1:1 from the frozen Python reference
// implementation. These multiply the v2_pure
// base (cosine × recency) ONLY when their signal is genuinely present; when a
// signal is neutral the term is 1.0 and the score collapses to v2_pure exactly.
// Always-on multiplicative terms monotonically HURT retrieval (the negative
// result that motivated gating) — the conditional gating is load-bearing.
//
// Do NOT change these constants without re-running the v3 parity harness.
const (
	betaEmotion = 0.15 // emotion_alignment multiplier (retrieval_v3.py β)
	gammaState  = 0.15 // state_fit multiplier (γ)
	deltaAnchor = 0.05 // anchor-priority bump (δ_a)
	deltaDate   = 0.25 // date-proximity bump (δ_d)
	anchorTopN  = 8    // anchor boost applies only within top-N by base score
)

// plutchikOrder is the canonical Plutchik-10 key order. Must match the column
// order in event_emotions / Engine.eventEmo and retrieval_v3.py EMOTION_KEYS.
var plutchikOrder = [10]string{
	"joy", "sadness", "anger", "fear", "trust",
	"disgust", "anticipation", "surprise", "shame", "guilt",
}

// emotionVec converts a mood map into a Plutchik-10 float32 vector in canonical
// order (missing keys → 0). Mirrors retrieval_v3.emotion_vec.
func emotionVec(mood map[string]float64) []float32 {
	v := make([]float32, 10)
	for i, k := range plutchikOrder {
		v[i] = float32(mood[k])
	}
	return v
}

// queryEmotion holds the precomputed query-emotion vector for the emotion boost.
// active=false means the boost is OFF (no dominant emotion) → collapse to v2_pure.
type queryEmotion struct {
	active bool
	vec    []float32
	norm   float64
}

// prepareQueryEmotion derives the emotion boost context, mirroring the boost_emo
// branch of retrieval_v3.retrieve() (retrieval_v3.py:558-577):
//
//	(a) UserState.MoodVector present → use it directly;
//	(b) else → infer query emotion from text via keyword matching (the
//	    use_llm_query_emo=False path; the in-engine LLM inference path is the
//	    only thing kept out of scope — it is never exercised by the baseline).
//
// Returns inactive when no dominant emotion (≥0.5) is present, so the formula
// stays identical to v2_pure.
func prepareQueryEmotion(query string, state *UserState) queryEmotion {
	var mood map[string]float64
	var domOk bool
	if state != nil && len(state.MoodVector) > 0 {
		mood = state.MoodVector
		domOk, _, _ = state.HasDominantEmotion(0.5)
	} else {
		mood = inferQueryEmotionsKeyword(query)
		domOk = maxVal(mood) >= 0.5
	}
	if !domOk {
		return queryEmotion{}
	}
	v := emotionVec(mood)
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	norm := math.Sqrt(s)
	if norm <= 1e-6 {
		return queryEmotion{}
	}
	return queryEmotion{active: true, vec: v, norm: norm}
}

// boost returns the per-event emotion boost. 1.0 when inactive or the event has
// a zero emotion vector. Otherwise 1 + β·max(cosine(eventEmo, q_emo), 0).
// Mirrors retrieval_v3.py:568-577 (np.clip(align, 0, None), zero-vec masking).
func (qe queryEmotion) boost(eventEmo []float32) float64 {
	if !qe.active {
		return 1.0
	}
	var dot, en float64
	for i, x := range eventEmo {
		xv := float64(x)
		en += xv * xv
		if i < len(qe.vec) {
			dot += xv * float64(qe.vec[i])
		}
	}
	en = math.Sqrt(en)
	if en <= 1e-6 {
		return 1.0
	}
	align := dot / (en * qe.norm)
	if align < 0 {
		align = 0
	}
	return 1.0 + betaEmotion*align
}
