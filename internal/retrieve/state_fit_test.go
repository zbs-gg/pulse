package retrieve

import "testing"

func f64(v float64) *float64 { return &v }
func boolp(b bool) *bool     { return &b }

func TestDateProximity(t *testing.T) {
	cases := []struct{ diff, want float64 }{
		{0.5, 1.0}, {1.0, 1.0}, {2.0, 0.7}, {3.0, 0.7}, {5.0, 0.3}, {7.0, 0.3}, {10.0, 0.0},
	}
	for _, c := range cases {
		if got := computeDateProximity(c.diff, 0); got != c.want {
			t.Errorf("proximity(diff=%v)=%v want %v", c.diff, got, c.want)
		}
	}
}

func TestInferQueryDate(t *testing.T) {
	if d, ok := inferQueryDate("что было на этой неделе?"); !ok || d != 3.0 {
		t.Errorf("this week → %v,%v want 3,true", d, ok)
	}
	if d, ok := inferQueryDate("сегодня тяжело"); !ok || d != 0.0 {
		t.Errorf("today → %v,%v want 0,true", d, ok)
	}
	if _, ok := inferQueryDate("что сейчас важно?"); ok {
		t.Error("neutral query (no past marker) should infer no date")
	}
}

func TestStateFit_StressedDepletion(t *testing.T) {
	stressed := &UserState{StressProxy: f64(0.8)} // IsBodyStressed
	if got := computeStateFit(&bioSnapshot{HRV: f64(50)}, "", "", stressed); got != 1.0 {
		t.Errorf("stressed+depletion=%v want 1.0", got)
	}
	// Restored-looking event under a stressed state → no boost (anti-match no-op).
	if got := computeStateFit(&bioSnapshot{HRV: f64(80)}, "", "", stressed); got != 0.0 {
		t.Errorf("stressed+restoration=%v want 0.0", got)
	}
}

func TestStateFit_RestoredRestoration(t *testing.T) {
	restored := &UserState{StressProxy: f64(0.2), SleepQuality: f64(0.8)} // IsBodyRestored
	if got := computeStateFit(&bioSnapshot{Workout: boolp(true)}, "", "", restored); got != 1.0 {
		t.Errorf("restored+restoration=%v want 1.0", got)
	}
}

func TestRestoration_StressProxyDefault(t *testing.T) {
	// sleep>=0.7 but stress_proxy absent → defaults to 1.0 → NOT restoration.
	if eventIsRestoration(&bioSnapshot{SleepQuality: f64(0.8)}, "", "") {
		t.Error("absent stress_proxy should default to 1.0 (not restoration)")
	}
	if !eventIsRestoration(&bioSnapshot{SleepQuality: f64(0.8), StressProxy: f64(0.2)}, "", "") {
		t.Error("sleep>=0.7 + low stress should be restoration")
	}
}

func TestAnchorTopN(t *testing.T) {
	base := []float64{0.1, 0.9, 0.5, 0.3, 0.7} // top-2 by base: idx 1 (0.9), 4 (0.7)
	in := anchorTopNSet(base, 2)
	if !in[1] || !in[4] || in[0] || in[2] || in[3] {
		t.Errorf("top-2 set wrong: %v", in)
	}
}
