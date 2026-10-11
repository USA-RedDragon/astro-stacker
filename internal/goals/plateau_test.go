package goals

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestHoursToPlateauIsWhereThePlateauStopFires(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ a, b float64 }{{1, 0}, {1, 0.05}, {1, 0.2}, {3.7, 0.4}, {0.02, 0.001}} {
		h := HoursToPlateau(c.a, c.b, PlateauGainPct)
		if !(h > 0) || math.IsInf(h, 0) {
			t.Fatalf("a=%v b=%v: hours %v", c.a, c.b, h)
		}
		if g := GainPerHour(c.a, c.b, h); math.Abs(g-PlateauGainPct) > 1e-9 {
			t.Fatalf("a=%v b=%v: gain at %v h is %v", c.a, c.b, h, g)
		}
		if GainPerHour(c.a, c.b, h*0.99) <= PlateauGainPct || GainPerHour(c.a, c.b, h*1.01) >= PlateauGainPct {
			t.Fatalf("a=%v b=%v: gain does not cross the threshold at %v h", c.a, c.b, h)
		}
	}
	if h := HoursToPlateau(1, 0, PlateauGainPct); math.Abs(h-32.585) > 1e-3 {
		t.Fatalf("pure sqrt(t) plateau %v", h)
	}
	if HoursToPlateau(1, 0.2, PlateauGainPct) >= HoursToPlateau(1, 0.05, PlateauGainPct) {
		t.Fatal("a higher noise floor must plateau sooner")
	}
	if !math.IsInf(HoursToPlateau(0, 0.1, PlateauGainPct), 1) {
		t.Fatal("no shot-noise term has no plateau")
	}
}

func TestPlateauOfNeedsAMeasurableFloor(t *testing.T) {
	t.Parallel()
	m := app.GoalMeasurement{Subs: 40, Levels: 4, EffectiveHours: 2, NoiseA: 1, NoiseB: 0.05}
	m.GainPerHourPct = GainPerHour(m.NoiseA, m.NoiseB, m.EffectiveHours)
	p := PlateauOf(m)
	h := HoursToPlateau(1, 0.05, PlateauGainPct)
	if p.Hours == nil || p.Progress == nil || p.Unmeasured != "" || math.Abs(*p.Hours-h) > 1e-9 || math.Abs(*p.Progress-2/h) > 1e-9 {
		t.Fatalf("%+v", p)
	}
	m.EffectiveHours = h * 2
	m.GainPerHourPct = GainPerHour(m.NoiseA, m.NoiseB, m.EffectiveHours)
	if p := PlateauOf(m); p.Progress == nil || *p.Progress != 1 || !IsPlateau(m) {
		t.Fatalf("past the plateau %+v", p)
	}
	m.Levels = 2
	if p := PlateauOf(m); p.Hours != nil || p.Progress != nil || p.Unmeasured != FloorReason(2) {
		t.Fatalf("no floor %+v", p)
	}
	if p := PlateauOf(app.GoalMeasurement{}); p.Progress != nil || p.Unmeasured != PlateauNoMeasurement {
		t.Fatalf("empty %+v", p)
	}
	if p := PlateauOf(app.GoalMeasurement{Subs: 4, Levels: 3, EffectiveHours: 1}); p.Progress != nil || p.Unmeasured != PlateauNoFit {
		t.Fatalf("no fit %+v", p)
	}
}

func TestEvaluateCarriesThePlateau(t *testing.T) {
	t.Parallel()
	m := app.GoalMeasurement{Filter: filterHa, Subs: 60, Levels: 4, SNR: 20, EffectiveHours: 2.3, NoiseA: 1, NoiseB: 0.02}
	m.GainPerHourPct = GainPerHour(m.NoiseA, m.NoiseB, m.EffectiveHours)
	p := Evaluate(m, DefaultGoal(filterHa))
	if !p.Done || p.Progress < 1 || p.PlateauAt.Progress == nil || *p.PlateauAt.Progress > 0.2 || p.Plateau {
		t.Fatalf("goal met far from the plateau %+v", p)
	}
}
