package retrieve

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
)

// state_fit / anchor / date boost helpers — ported 1:1 from the frozen Python
// reference implementation. Each is gated so a
// neutral signal yields a 1.0 boost (collapse to v2_pure).

// bioSnapshot mirrors the event's biometric_snapshot JSON. Pointer fields
// distinguish "absent" (nil) from a real 0 — the Python code uses presence
// checks (isinstance), not truthiness.
type bioSnapshot struct {
	HRV          *float64 `json:"hrv"`
	SleepQuality *float64 `json:"sleep_quality"`
	StressProxy  *float64 `json:"stress_proxy"`
	HRTrend      *string  `json:"hr_trend"`
	HRVTrend     *string  `json:"hrv_trend"`
	Workout      *bool    `json:"workout"`
}

func parseBioSnapshot(s string) *bioSnapshot {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var b bioSnapshot
	if err := json.Unmarshal([]byte(s), &b); err != nil {
		return nil
	}
	return &b
}

func anySubstr(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// eventIsDepletion mirrors retrieval_v3._event_is_depletion. text/label lowercased.
func eventIsDepletion(bio *bioSnapshot, text, label string) bool {
	if bio != nil {
		if bio.HRV != nil && *bio.HRV < 60 {
			return true
		}
		if bio.SleepQuality != nil && *bio.SleepQuality <= 0.4 {
			return true
		}
		if bio.StressProxy != nil && *bio.StressProxy >= 0.6 {
			return true
		}
		if bio.HRVTrend != nil && *bio.HRVTrend == "declining_3d" {
			return true
		}
		if bio.HRTrend != nil && (*bio.HRTrend == "elevated_3d" || *bio.HRTrend == "elevated_overnight") {
			return true
		}
	}
	if anySubstr(label, "burden", "wound") {
		return true
	}
	return anySubstr(text, "hrv 5", "declining", "anxious sleep", "overload")
}

// eventIsRestoration mirrors retrieval_v3._event_is_restoration.
func eventIsRestoration(bio *bioSnapshot, text, label string) bool {
	if bio != nil {
		if bio.HRV != nil && *bio.HRV >= 70 {
			return true
		}
		if bio.SleepQuality != nil && *bio.SleepQuality >= 0.7 {
			sp := 1.0 // Python: bio.get("stress_proxy", 1.0) — default 1.0 when absent
			if bio.StressProxy != nil {
				sp = *bio.StressProxy
			}
			if sp <= 0.3 {
				return true
			}
		}
		if bio.Workout != nil && *bio.Workout {
			return true
		}
	}
	if anySubstr(label, "ship", "milestone", "repair") {
		return true
	}
	return anySubstr(text, "hrv 7", "hrv 8", "hrv 9", "post-workout", "ship day")
}

// computeStateFit mirrors retrieval_v3.compute_state_fit (0..1). text/label
// lowercased by the caller; state gates already checked by caller.
func computeStateFit(bio *bioSnapshot, text, label string, state *UserState) float64 {
	score := 0.0
	if state.IsBodyStressed() && eventIsDepletion(bio, text, label) {
		score = 1.0
	}
	if state.IsBodyRestored() && eventIsRestoration(bio, text, label) {
		score = math.Max(score, 1.0)
	}
	if len(state.RecentLifeEvents7d) > 0 {
		hints := strings.ToLower(strings.Join(state.RecentLifeEvents7d, " "))
		if anySubstr(hints, "conflict", "ссора") &&
			(anySubstr(label, "marriage", "repair") || anySubstr(text, "conflict", "ссора")) {
			score = math.Max(score, 0.7)
		}
		if anySubstr(hints, "anniversary", "unknown") && anySubstr(label, "origin", "wound") {
			score = math.Max(score, 0.7)
		}
	}
	return score
}

// dateTemporalKeywords → implicit days_ago reference. Mirrors retrieval_v3.TEMPORAL_KEYWORDS.
var dateTemporalKeywords = []struct {
	kws  []string
	days float64
}{
	{[]string{"today", "сегодня"}, 0.0},
	{[]string{"yesterday", "вчера"}, 1.0},
	{[]string{"this week", "на этой неделе", "recently", "недавно", "last few days", "за последние дни"}, 3.0},
	{[]string{"last week", "на прошлой неделе", "неделю назад"}, 10.0},
	{[]string{"this month", "в этом месяце", "этом месяце"}, 15.0},
	{[]string{"last month", "в прошлом месяце", "месяц назад"}, 40.0},
}

// inferQueryDate scans the query for a temporal marker (first match wins).
// Mirrors retrieval_v3.infer_query_date.
func inferQueryDate(query string) (float64, bool) {
	q := strings.ToLower(query)
	for _, tk := range dateTemporalKeywords {
		if anySubstr(q, tk.kws...) {
			return tk.days, true
		}
	}
	return 0, false
}

// resolveDateRef chooses the date reference: explicit snapshot_days_ago, else
// inferred from the query. Mirrors retrieval_v3.retrieve() date_ref selection.
func resolveDateRef(query string, state *UserState) (float64, bool) {
	if state != nil && state.SnapshotDaysAgo != nil {
		return *state.SnapshotDaysAgo, true
	}
	return inferQueryDate(query)
}

// computeDateProximity mirrors retrieval_v3.compute_date_proximity (stepped).
func computeDateProximity(eventDaysAgo, refDaysAgo float64) float64 {
	switch diff := math.Abs(eventDaysAgo - refDaysAgo); {
	case diff <= 1.0:
		return 1.0
	case diff <= 3.0:
		return 0.7
	case diff <= 7.0:
		return 0.3
	default:
		return 0.0
	}
}

// anchorTopNSet marks the indices whose base score is in the top-N. Anchor boost
// applies only to anchors already in the top-N by BASE (retrieval_v3.py:595).
func anchorTopNSet(base []float64, topN int) []bool {
	n := len(base)
	in := make([]bool, n)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return base[idx[a]] > base[idx[b]] })
	for k := 0; k < topN && k < n; k++ {
		in[idx[k]] = true
	}
	return in
}

// queryEmoKeywords mirrors retrieval_v3.EMO_KEYWORDS — substring markers per Plutchik
// emotion for the keyword-emotion fallback (used when no mood_vector is given).
var queryEmoKeywords = map[string][]string{
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

// inferQueryEmotionsKeyword mirrors retrieval_v3.infer_query_emotions_keyword:
// substring match → 0.7 per matched emotion (only matched keys present).
func inferQueryEmotionsKeyword(query string) map[string]float64 {
	q := strings.ToLower(query)
	out := make(map[string]float64, len(queryEmoKeywords))
	for emo, kws := range queryEmoKeywords {
		if anySubstr(q, kws...) {
			out[emo] = 0.7
		}
	}
	return out
}

func maxVal(m map[string]float64) float64 {
	var mx float64
	for _, v := range m {
		if v > mx {
			mx = v
		}
	}
	return mx
}
