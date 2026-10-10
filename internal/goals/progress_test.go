package goals

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestHoursForSNR(t *testing.T) {
	if got := HoursForSNR(10, 5, 10); math.Abs(got-30) > 1e-9 {
		t.Fatalf("got %v", got)
	}
	if got := HoursForSNR(10, 12, 10); got != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestGainPerHourPure(t *testing.T) {
	g := GainPerHour(1, 0, 33)
	if g < 1.4 || g > 1.6 {
		t.Fatalf("gain at 33 h %v", g)
	}
}

func TestEvaluateSNR(t *testing.T) {
	m := app.GoalMeasurement{Object: "Garlic", Filter: "O-III", Subs: 47, SNR: 4.4, EffectiveHours: 7.8, GainPerHourPct: 5.8}
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
	d := 24.25
	m := app.GoalMeasurement{Filter: "Red", Subs: 50, SNR: 20, EffectiveHours: 4, Depth: &d, GainPerHourPct: 10}
	g := Goal{Kind: KindDepth, Depth: 25.5}
	p := Evaluate(m, g)
	if math.Abs(p.HoursNeeded-4*9) > 1e-6 || math.Abs(p.Progress-0.1) > 1e-9 {
		t.Fatalf("%+v", p)
	}
}
