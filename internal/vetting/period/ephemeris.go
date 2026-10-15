package period

import (
	"maps"
	"math"
	"slices"
)

func phaseOf(e *Ephemeris, bjd float64) float64 {
	if e == nil || e.Period <= 0 {
		return 0
	}
	x := frac((bjd - e.T0) / e.Period)
	if x >= 0.75 {
		x--
	}
	return x
}

func meanOf(m map[string]float64) float64 {
	if len(m) == 0 {
		return 0
	}
	var s float64
	for _, k := range slices.Sorted(maps.Keys(m)) {
		s += m[k]
	}
	return s / float64(len(m))
}

func predict(e *Ephemeris, bjd float64, band string) float64 {
	if e == nil || e.Period <= 0 {
		return 0
	}
	x := (bjd - e.T0) / e.Period
	switch e.Kind {
	case KindSinusoidal:
		a, ok := e.Amplitude[band]
		if !ok {
			a = meanOf(e.Amplitude)
		}
		return a / 2 * math.Cos(2*math.Pi*x)
	case KindEclipsing:
		var v float64
		for k, ec := range e.Eclipses {
			sh := shapeOnly(wrapPhase(x-ec.Phase), ec.Width, ec.Shape)
			if sh > 0 {
				v += e.depthFor(k, band) * sh
			}
		}
		return v
	case KindFlat, KindIrregular:
		return 0
	}
	return 0
}
