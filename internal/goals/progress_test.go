package goals

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestHoursForSNR(t *testing.T) {
	t.Parallel()
	if got := HoursForSNR(10, 5, 10); math.Abs(got-30) > 1e-9 {
		t.Fatalf("got %v", got)
	}
	if got := HoursForSNR(10, 12, 10); got != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestGainPerHourPure(t *testing.T) {
	t.Parallel()
	g := GainPerHour(1, 0, 33)
	if g < 1.4 || g > 1.6 {
		t.Fatalf("gain at 33 h %v", g)
	}
}

func TestEvaluateSNR(t *testing.T) {
	t.Parallel()
	m := app.GoalMeasurement{Object: "Garlic", Filter: "O-III", Subs: 47, Levels: 4, SNR: 4.4, EffectiveHours: 7.8, GainPerHourPct: 5.8}
	p := Evaluate(m, DefaultGoal("O-III"))
	if p.Done || math.Abs(p.HoursNeeded-7.8*((10/4.4)*(10/4.4)-1)) > 1e-9 || math.Abs(p.Progress-0.1936) > 1e-3 {
		t.Fatalf("%+v", p)
	}
	m.GainPerHourPct = 1.2
	if p := Evaluate(m, DefaultGoal("O-III")); !p.Done || !p.Plateau {
		t.Fatalf("plateau should finish %+v", p)
	}
	g := DefaultGoal("O-III")
	g.PlateauStop = false
	if p := Evaluate(m, g); p.Done {
		t.Fatalf("plateau stop off %+v", p)
	}
}

func TestEvaluateDepth(t *testing.T) {
	t.Parallel()
	d := 24.25
	m := app.GoalMeasurement{Filter: "Red", Subs: 50, SNR: 20, EffectiveHours: 4, Depth: &d, GainPerHourPct: 10}
	g := Goal{Kind: KindDepth, Depth: 25.5}
	p := Evaluate(m, g)
	if math.Abs(p.HoursNeeded-4*9) > 1e-6 || math.Abs(p.Progress-0.1) > 1e-9 {
		t.Fatalf("%+v", p)
	}
}

func TestEvaluateDepthWithoutZeroPointStaysADepthGoal(t *testing.T) {
	t.Parallel()
	m := app.GoalMeasurement{Filter: "H-a", Subs: 50, SNR: 20, EffectiveHours: 4, GainPerHourPct: 10, PixelScale: 1.9, ZeroPointStars: 4, LowReason: "faint band too small"}
	p := Evaluate(m, Goal{Kind: KindDepth, Depth: 25.5})
	if p.Kind != KindDepth || p.Progress != 0 || p.Done || p.HoursNeeded != -1 || p.Unmeasured != "no depth: 4 Gaia stars matched, 10 needed" || p.LowReason != "faint band too small" {
		t.Fatalf("%+v", p)
	}
}

func pelicanP3Blue() app.GoalMeasurement {
	return app.GoalMeasurement{Object: "Pelican Nebula Panel 3", Filter: "Blue", Subs: 9, Levels: 1, EffectiveHours: 0.6364448280832384,
		Signal: 4.150851964368485e-7, NoiseA: 2.591412588243134e-9, NoiseB: 7.731075059249109e-8, NoiseNow: 7.737896104594509e-8,
		SNR: 5.36, GainPerHourPct: 0.05385824306729514,
		Points: `[{"n":4,"t":0.2783315944633578,"sigma":7.73107505937816e-8},{"n":4,"t":0.2795662755549598,"sigma":7.753653950533588e-8},{"n":4,"t":0.2862299851603613,"sigma":7.755059968964953e-8}]`}
}

func TestEvaluateNeverPlateausWithoutANoiseFloor(t *testing.T) {
	t.Parallel()
	m := pelicanP3Blue()
	for levels := range MinFloorLevels {
		m.Levels = levels
		if p := Evaluate(m, DefaultGoal("Blue")); p.Plateau || p.Done {
			t.Errorf("%d levels: %+v", levels, p)
		}
	}
	m.Levels = MinFloorLevels
	if p := Evaluate(m, DefaultGoal("Blue")); !p.Plateau || !p.Done {
		t.Errorf("%d levels should plateau: %+v", m.Levels, p)
	}
}

func TestRefitWithoutFloor(t *testing.T) {
	t.Parallel()
	m := pelicanP3Blue()
	zp, d := 21.0, 0.0
	m.ZeroPoint, m.Depth, m.PixelScale = &zp, &d, 1.5
	m.LowReason = "frame-filling nebula"
	if !RefitWithoutFloor(&m) {
		t.Fatal("no change")
	}
	wantGain := 100 * (1 - math.Sqrt(m.EffectiveHours/(m.EffectiveHours+1)))
	if m.NoiseB != 0 || math.Abs(m.GainPerHourPct-wantGain) > 1e-9 || math.Abs(m.NoiseNow-m.NoiseA/math.Sqrt(m.EffectiveHours)) > 1e-18 {
		t.Fatalf("%+v", m)
	}
	if math.Abs(m.SNR-8.06) > 0.05 || !m.LowConfidence || m.LowReason != "frame-filling nebula; noise floor not measurable from 1 draw levels" {
		t.Fatalf("snr %v reason %q", m.SNR, m.LowReason)
	}
	if want, _ := Depth(zp, m.NoiseNow, 1.5); m.Depth == nil || *m.Depth != want {
		t.Fatalf("depth %v want %v", m.Depth, want)
	}
	if RefitWithoutFloor(&m) {
		t.Fatal("second refit changed the row")
	}
	m3 := pelicanP3Blue()
	m3.Levels = MinFloorLevels
	if RefitWithoutFloor(&m3) {
		t.Fatal("refit a measurement with a measurable floor")
	}
}
