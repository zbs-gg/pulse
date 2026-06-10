package retrieve

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPrepareQueryEmotion_Gating(t *testing.T) {
	// nil state → inactive (collapse to v2_pure)
	if prepareQueryEmotion("", nil).active {
		t.Fatal("nil state should be inactive")
	}
	// no dominant emotion (all < 0.5) → inactive
	weak := &UserState{MoodVector: map[string]float64{"anger": 0.3, "fear": 0.2}}
	if prepareQueryEmotion("", weak).active {
		t.Fatal("sub-threshold mood should be inactive")
	}
	// dominant emotion (>= 0.5) → active
	strong := &UserState{MoodVector: map[string]float64{"anger": 0.8}}
	qe := prepareQueryEmotion("", strong)
	if !qe.active {
		t.Fatal("dominant anger should be active")
	}
	// anger is index 2 in plutchikOrder
	if qe.vec[2] != 0.8 {
		t.Fatalf("emotionVec anger=%v, want 0.8", qe.vec[2])
	}
}

func TestEmotionBoost_Formula(t *testing.T) {
	// q = pure anger (unit vector). Event = pure anger → align=1 → boost = 1 + β.
	qe := prepareQueryEmotion("", &UserState{MoodVector: map[string]float64{"anger": 1.0}})
	angerEvent := emotionVec(map[string]float64{"anger": 1.0})
	if got := qe.boost(angerEvent); !approx(got, 1.0+betaEmotion) {
		t.Fatalf("aligned event boost = %v, want %v", got, 1.0+betaEmotion)
	}
	// Orthogonal event (pure joy) → align=0 → boost = 1.0.
	joyEvent := emotionVec(map[string]float64{"joy": 1.0})
	if got := qe.boost(joyEvent); !approx(got, 1.0) {
		t.Fatalf("orthogonal event boost = %v, want 1.0", got)
	}
	// Zero-vector event → masked → boost = 1.0.
	if got := qe.boost(make([]float32, 10)); !approx(got, 1.0) {
		t.Fatalf("zero-vec event boost = %v, want 1.0", got)
	}
	// Inactive context → always 1.0 (collapse to v2_pure).
	inactive := prepareQueryEmotion("", nil)
	if got := inactive.boost(angerEvent); !approx(got, 1.0) {
		t.Fatalf("inactive boost = %v, want 1.0", got)
	}
}

func TestEmotionBoost_PartialAlignment(t *testing.T) {
	// q = anger=0.8,fear=0.6 ; event = anger=1.0. align = 0.8/sqrt(0.8²+0.6²) = 0.8.
	qe := prepareQueryEmotion("", &UserState{MoodVector: map[string]float64{"anger": 0.8, "fear": 0.6}})
	ev := emotionVec(map[string]float64{"anger": 1.0})
	want := 1.0 + betaEmotion*0.8
	if got := qe.boost(ev); math.Abs(got-want) > 1e-6 {
		t.Fatalf("partial-align boost = %v, want ~%v", got, want)
	}
}
