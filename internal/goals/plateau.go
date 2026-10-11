package goals

import (
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

type Plateau struct {
	Hours      *float64 `json:"hours"`
	Progress   *float64 `json:"progress"`
	Unmeasured string   `json:"unmeasured,omitempty"`
	GainPct    float64  `json:"gainPct"`
}

const (
	PlateauNoMeasurement = "no noise measurement yet"
	PlateauNoFit         = "no noise fit"
)

func HoursToPlateau(a, b, gainPct float64) float64 {
	if a <= 0 || gainPct <= 0 || gainPct >= 100 {
		return math.Inf(1)
	}
	r := 1 - gainPct/100
	q := r * r
	k := 1 - q
	aa, bb := a*a, b*b
	if bb == 0 {
		return q / k
	}
	lin := k * (aa + bb)
	return (-lin + math.Sqrt(lin*lin+4*k*bb*q*aa)) / (2 * k * bb)
}

func PlateauOf(m app.GoalMeasurement) Plateau {
	switch {
	case m.Subs <= 0 || m.EffectiveHours <= 0:
		return Plateau{Unmeasured: PlateauNoMeasurement, GainPct: PlateauGainPct}
	case !FloorMeasurable(m.Levels):
		return Plateau{Unmeasured: FloorReason(m.Levels), GainPct: PlateauGainPct}
	case m.NoiseA <= 0:
		return Plateau{Unmeasured: PlateauNoFit, GainPct: PlateauGainPct}
	}
	h := HoursToPlateau(m.NoiseA, m.NoiseB, PlateauGainPct)
	if math.IsInf(h, 0) || math.IsNaN(h) {
		return Plateau{Unmeasured: PlateauNoFit, GainPct: PlateauGainPct}
	}
	p := math.Min(m.EffectiveHours/h, 0.999)
	if IsPlateau(m) {
		p = 1
	}
	return Plateau{Hours: &h, Progress: &p, GainPct: PlateauGainPct}
}
