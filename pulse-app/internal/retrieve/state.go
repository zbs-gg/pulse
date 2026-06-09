package retrieve

// UserState mirrors retrieval_v3.UserState (Python prototype). Optional fields;
// retrieval routing and boosts only fire on signals that are actually present.
type UserState struct {
	// Plutchik-10 mood vector. Keys: joy, sadness, anger, fear, trust,
	// disgust, anticipation, surprise, shame, guilt. Values 0..1.
	MoodVector map[string]float64 `json:"mood_vector,omitempty"`

	// Body / biometric signals (any nullable; nil means "unknown")
	SleepQuality *float64 `json:"sleep_quality,omitempty"`
	SleepHours   *float64 `json:"sleep_hours,omitempty"`
	HRV          *float64 `json:"hrv,omitempty"`
	HRTrend      *string  `json:"hr_trend,omitempty"`  // "elevated_3d" | "stable" | "low" | "elevated_overnight"
	HRVTrend     *string  `json:"hrv_trend,omitempty"` // "declining_3d" | "stable" | "rising"
	StressProxy  *float64 `json:"stress_proxy,omitempty"`

	RecentLifeEvents7d []string `json:"recent_life_events_7d,omitempty"`
	TimeOfDay          *string  `json:"time_of_day,omitempty"`       // "morning" | "evening" | "night"
	SnapshotDaysAgo    *float64 `json:"snapshot_days_ago,omitempty"` // anchor for date_proximity boost

	// Cartographer-derived profile signals. All are optional: absent or empty
	// means identity scoring, preserving the Pulse v3 baseline.
	ShadowThemes       []string          `json:"shadow_themes,omitempty"`
	CuriositySignals   []string          `json:"curiosity_signals,omitempty"`
	SensoryPreferences map[string]string `json:"sensory_preferences,omitempty"`
	VoiceRegister      string            `json:"voice_register,omitempty"`
	RelationalBaseline map[string]any    `json:"relational_baseline,omitempty"`
	ActiveTriggers     []TriggerSignal   `json:"active_triggers,omitempty"`
}

type TriggerSignal struct {
	Stimulus  string  `json:"stimulus"`
	Reaction  string  `json:"reaction"`
	Intensity float64 `json:"intensity"`
}

// HasDominantEmotion returns (true, value, key) if MoodVector has any
// emotion >= threshold. Otherwise (false, 0, "").
func (s *UserState) HasDominantEmotion(threshold float64) (bool, float64, string) {
	if s == nil || len(s.MoodVector) == 0 {
		return false, 0, ""
	}
	var topKey string
	var topVal float64
	for k, v := range s.MoodVector {
		if v > topVal {
			topVal = v
			topKey = k
		}
	}
	return topVal >= threshold, topVal, topKey
}

// IsBodyStressed returns true when biometric signal indicates body load.
// Mirrors Python UserState.is_body_stressed().
func (s *UserState) IsBodyStressed() bool {
	if s == nil {
		return false
	}
	if s.StressProxy != nil && *s.StressProxy >= 0.6 {
		return true
	}
	if s.SleepQuality != nil && *s.SleepQuality <= 0.4 {
		return true
	}
	if s.HRTrend != nil && (*s.HRTrend == "elevated_3d" || *s.HRTrend == "elevated_overnight") {
		return true
	}
	if s.HRVTrend != nil && *s.HRVTrend == "declining_3d" {
		return true
	}
	if s.HRV != nil && *s.HRV < 55 {
		return true
	}
	return false
}

// IsBodyRestored returns true when biometric signal indicates good baseline.
func (s *UserState) IsBodyRestored() bool {
	if s == nil {
		return false
	}
	if s.StressProxy != nil && *s.StressProxy <= 0.3 {
		if s.SleepQuality == nil || *s.SleepQuality >= 0.7 {
			return true
		}
	}
	return false
}

// RecentLifeEventsCount returns the number of recent_life_events_7d entries
// (used by router as a state-loaded signal).
func (s *UserState) RecentLifeEventsCount() int {
	if s == nil {
		return 0
	}
	return len(s.RecentLifeEvents7d)
}
