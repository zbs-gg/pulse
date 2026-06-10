// Package health serves Apple Health snapshots. In M0 it returns static
// fixture data so demos and Heart integration can develop against a
// stable shape without a real Apple Health bridge.
//
// Replace Provider with a real implementation later (e.g. SQLite-backed
// reader pointing at a health.db on the host) — handler stays the same.
package health

import (
	"time"
)

// Snapshot is the public shape returned by GET /health/snapshot. Field
// names match a chat client's UserState shape so a caller can pluck
// values directly into a /retrieve user_state without remapping.
//
// Source distinguishes mock fixtures from real bridge data downstream
// callers may decide to log or surface differently.
type Snapshot struct {
	HRV             int       `json:"hrv"`              // ms, RMSSD-like
	StressProxy     float64   `json:"stress_proxy"`     // 0..1, 1 = high stress
	SleepQuality    float64   `json:"sleep_quality"`    // 0..1, 1 = great
	SleepHoursLast  float64   `json:"sleep_hours_last"` // last night
	SleepHoursAvg7d float64   `json:"sleep_hours_avg_7d"`
	StepsToday      int       `json:"steps_today"`
	LastWorkoutDays int       `json:"last_workout_days"` // days since last workout
	HRTrend         string    `json:"hr_trend"`          // "stable" | "elevated_3d" | "elevated_overnight" | "low"
	HRVTrend        string    `json:"hrv_trend"`         // "stable" | "declining_3d" | "rising"
	Timestamp       time.Time `json:"timestamp"`
	Source          string    `json:"source"` // "mock" in M0
}

// Provider returns a fixed-length history of recent days. Index 0 is the
// most recent ("today"), index N-1 is the oldest. Length matches what
// Days() exposes; callers can request prefixes via slicing.
type Provider interface {
	Days() []Snapshot
}

// FixtureProvider returns canned data designed to support a "trend" demo:
// 3 days ago = great recovery, yesterday + today = poor recovery. The
// pattern matches a realistic high-stress week (sleep loss → HRV drop →
// stress proxy climbing).
//
// All snapshots share a single anchor timestamp so demos are
// reproducible. Override anchor in tests via NewFixtureProvider.
type FixtureProvider struct {
	anchor time.Time
}

// NewFixtureProvider builds a provider anchored at the given timestamp.
// Pass time.Now() in production wiring; pass a fixed time in tests.
func NewFixtureProvider(anchor time.Time) *FixtureProvider {
	return &FixtureProvider{anchor: anchor.UTC()}
}

// Days returns 4 snapshots (today, -1d, -2d, -3d) — enough to show a
// trend in demos. Order: index 0 = today, index 3 = -3d.
func (p *FixtureProvider) Days() []Snapshot {
	t := p.anchor
	return []Snapshot{
		// today: still recovering, slightly better than yesterday
		{
			HRV:             35,
			StressProxy:     0.72,
			SleepQuality:    0.40,
			SleepHoursLast:  4.0,
			SleepHoursAvg7d: 5.3,
			StepsToday:      1850,
			LastWorkoutDays: 4,
			HRTrend:         "elevated_overnight",
			HRVTrend:        "declining_3d",
			Timestamp:       t,
			Source:          "mock",
		},
		// -1d: bad night
		{
			HRV:             38,
			StressProxy:     0.65,
			SleepQuality:    0.45,
			SleepHoursLast:  4.2,
			SleepHoursAvg7d: 5.6,
			StepsToday:      2400,
			LastWorkoutDays: 3,
			HRTrend:         "elevated_3d",
			HRVTrend:        "declining_3d",
			Timestamp:       t.Add(-24 * time.Hour),
			Source:          "mock",
		},
		// -2d: average
		{
			HRV:             50,
			StressProxy:     0.45,
			SleepQuality:    0.60,
			SleepHoursLast:  6.1,
			SleepHoursAvg7d: 6.0,
			StepsToday:      6800,
			LastWorkoutDays: 2,
			HRTrend:         "stable",
			HRVTrend:        "stable",
			Timestamp:       t.Add(-48 * time.Hour),
			Source:          "mock",
		},
		// -3d: great recovery
		{
			HRV:             65,
			StressProxy:     0.22,
			SleepQuality:    0.85,
			SleepHoursLast:  8.0,
			SleepHoursAvg7d: 6.4,
			StepsToday:      11200,
			LastWorkoutDays: 1,
			HRTrend:         "stable",
			HRVTrend:        "rising",
			Timestamp:       t.Add(-72 * time.Hour),
			Source:          "mock",
		},
	}
}

// Latest returns just today's snapshot — the most common demo path.
func (p *FixtureProvider) Latest() Snapshot {
	return p.Days()[0]
}
