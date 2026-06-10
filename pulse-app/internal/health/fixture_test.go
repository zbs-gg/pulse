package health

import (
	"testing"
	"time"
)

func TestFixtureProviderReturns4Days(t *testing.T) {
	p := NewFixtureProvider(time.Now())
	days := p.Days()
	if len(days) != 4 {
		t.Fatalf("expected 4 days, got %d", len(days))
	}
}

func TestFixtureTodayIsBadRecovery(t *testing.T) {
	// "Today" must signal stress so demos show how Heart reacts to
	// poor body state. If this drifts, the demo loses its punch.
	p := NewFixtureProvider(time.Now())
	today := p.Latest()
	if today.HRV >= 50 {
		t.Errorf("expected today HRV < 50 (poor recovery), got %d", today.HRV)
	}
	if today.SleepQuality > 0.5 {
		t.Errorf("expected today sleep_quality <= 0.5, got %.2f", today.SleepQuality)
	}
	if today.StressProxy < 0.6 {
		t.Errorf("expected today stress_proxy >= 0.6, got %.2f", today.StressProxy)
	}
	if today.Source != "mock" {
		t.Errorf("expected source=mock, got %q", today.Source)
	}
}

func TestFixtureTrendImproveBackInTime(t *testing.T) {
	// -3d should be the best day; today the worst. Verifies the trend
	// arc demos rely on (recovery → stress).
	p := NewFixtureProvider(time.Now())
	d := p.Days()
	if d[0].HRV >= d[3].HRV {
		t.Errorf("expected today HRV (%d) < -3d HRV (%d)", d[0].HRV, d[3].HRV)
	}
	if d[0].SleepQuality >= d[3].SleepQuality {
		t.Errorf("expected today sleep_quality (%.2f) < -3d (%.2f)",
			d[0].SleepQuality, d[3].SleepQuality)
	}
	if d[0].StressProxy <= d[3].StressProxy {
		t.Errorf("expected today stress_proxy (%.2f) > -3d (%.2f)",
			d[0].StressProxy, d[3].StressProxy)
	}
}

func TestFixtureTimestampsDescendBy24h(t *testing.T) {
	anchor := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	p := NewFixtureProvider(anchor)
	d := p.Days()
	for i := 0; i < len(d)-1; i++ {
		gap := d[i].Timestamp.Sub(d[i+1].Timestamp)
		if gap != 24*time.Hour {
			t.Errorf("day %d→%d gap = %v, want 24h", i, i+1, gap)
		}
	}
	if !d[0].Timestamp.Equal(anchor) {
		t.Errorf("today timestamp = %v, want %v", d[0].Timestamp, anchor)
	}
}

func TestFixtureNonZeroFields(t *testing.T) {
	// Smoke check: every day populated, no accidental zero fields that
	// would render as "missing" in JSON.
	p := NewFixtureProvider(time.Now())
	for i, d := range p.Days() {
		if d.HRV == 0 {
			t.Errorf("day %d: HRV is zero", i)
		}
		if d.SleepHoursLast == 0 {
			t.Errorf("day %d: sleep_hours_last is zero", i)
		}
		if d.StepsToday == 0 {
			t.Errorf("day %d: steps_today is zero", i)
		}
		if d.HRTrend == "" {
			t.Errorf("day %d: hr_trend is empty", i)
		}
		if d.HRVTrend == "" {
			t.Errorf("day %d: hrv_trend is empty", i)
		}
	}
}
