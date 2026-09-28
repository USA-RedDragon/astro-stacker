// Package calmatch picks the calibration frames for a group of lights and
// says how good the match is. The pipeline uses the choice to calibrate, and
// the UI shows the quality so missing or stale calibration is visible.
package calmatch

import (
	"math"
	"sort"
	"time"
)

// Quality ranks a match from best to worst.
type Quality string

const (
	Exact    Quality = "exact"    // same night (flats, bias) or same setpoint (darks)
	Fallback Quality = "fallback" // usable, but from another night or temperature
	Missing  Quality = "missing"  // nothing compatible
)

// Tolerances for treating two frames as taken with the same setup.
const (
	RotationToleranceDeg = 1.0
	ExposureTolerance    = 0.01 // relative
	SetTempExactC        = 1.0
	SetTempFallbackC     = 5.0
)

// Group describes a set of lights that share calibration.
type Group struct {
	Night    time.Time
	Filter   string
	Exposure float64
	Gain     float64
	Offset   float64
	SetTemp  float64 // NaN when unknown
	BinX     float64
	Rotator  float64 // NaN when there is no rotator
}

// Set is a candidate calibration set: frames of one type taken together.
type Set struct {
	Type     string // FLAT, DARK, BIAS
	Night    time.Time
	Filter   string
	Exposure float64
	Gain     float64
	Offset   float64
	SetTemp  float64
	BinX     float64
	Rotator  float64
	Count    int
}

// Match is the chosen set for one calibration type.
type Match struct {
	Quality Quality
	Set     *Set
	AgeDays int     // nights between the lights and the calibration
	TempOff float64 // setpoint difference in °C, darks only
	// RotationMismatch is set when the chosen flat was taken at another
	// rotator angle than the lights.
	RotationMismatch bool
}

// Result holds the choice for each calibration type.
type Result struct {
	Flat, Dark, Bias Match
}

func same(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return a == b
}

func days(a, b time.Time) int {
	d := a.Sub(b).Hours() / 24
	return int(math.Round(math.Abs(d)))
}

func angleDiff(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 360)
	return math.Min(d, 360-d)
}

// rotationOK treats a missing rotator reading on either side as compatible,
// since older frames were taken without one.
func rotationOK(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return true
	}
	return angleDiff(a, b) <= RotationToleranceDeg
}

// Choose picks the best flat, dark and bias set for a group of lights.
func Choose(g Group, sets []Set) Result {
	return Result{
		Flat: chooseFlat(g, sets),
		Dark: chooseDark(g, sets),
		Bias: chooseBias(g, sets),
	}
}

// Flats must match filter, binning, gain and offset: the flat's shape
// changes little with gain, but its pedestal does. Rotation is preferred but
// not required. The rotator turns the camera and filter wheel together, so
// dust on the sensor window and filters stays put relative to the pixels;
// only the dimmer, rotation-symmetric vignetting and dust on the optics in
// front of the rotator move. A flat at another angle is a fallback.
func chooseFlat(g Group, sets []Set) Match {
	var cands []Set
	for _, s := range sets {
		if s.Type == "FLAT" && s.Filter == g.Filter && same(s.BinX, g.BinX) &&
			same(s.Gain, g.Gain) && same(s.Offset, g.Offset) {
			cands = append(cands, s)
		}
	}
	if len(cands) == 0 {
		return Match{Quality: Missing}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		di, dj := days(g.Night, cands[i].Night), days(g.Night, cands[j].Night)
		if di != dj {
			return di < dj
		}
		ri, rj := rotationOK(cands[i].Rotator, g.Rotator), rotationOK(cands[j].Rotator, g.Rotator)
		if ri != rj {
			return ri
		}
		return cands[i].Night.After(cands[j].Night)
	})
	best := cands[0]
	m := Match{Set: &best, AgeDays: days(g.Night, best.Night), Quality: Fallback,
		RotationMismatch: !rotationOK(best.Rotator, g.Rotator)}
	if m.AgeDays == 0 && !m.RotationMismatch {
		m.Quality = Exact
	}
	return m
}

// Bias must match gain, offset and binning. Its temperature barely matters on
// CMOS sensors, so the nearest night wins.
func chooseBias(g Group, sets []Set) Match {
	var cands []Set
	for _, s := range sets {
		if s.Type == "BIAS" && same(s.Gain, g.Gain) && same(s.Offset, g.Offset) && same(s.BinX, g.BinX) {
			cands = append(cands, s)
		}
	}
	return nearestInTime(g, cands)
}

// Darks must match exposure, gain, offset and binning. Among those, the
// closest setpoint wins, then the nearest night. Darks are a library, so a
// different night is still exact when the setpoint matches.
func chooseDark(g Group, sets []Set) Match {
	var cands []Set
	for _, s := range sets {
		if s.Type != "DARK" || !same(s.Gain, g.Gain) || !same(s.Offset, g.Offset) || !same(s.BinX, g.BinX) {
			continue
		}
		if math.Abs(s.Exposure-g.Exposure) > ExposureTolerance*g.Exposure {
			continue
		}
		if tempOff(g, s) > SetTempFallbackC {
			continue
		}
		cands = append(cands, s)
	}
	if len(cands) == 0 {
		return Match{Quality: Missing}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		ti, tj := tempOff(g, cands[i]), tempOff(g, cands[j])
		if ti != tj {
			return ti < tj
		}
		return days(g.Night, cands[i].Night) < days(g.Night, cands[j].Night)
	})
	best := cands[0]
	m := Match{Set: &best, AgeDays: days(g.Night, best.Night), TempOff: tempOff(g, best), Quality: Fallback}
	if m.TempOff <= SetTempExactC {
		m.Quality = Exact
	}
	return m
}

func tempOff(g Group, s Set) float64 {
	if math.IsNaN(g.SetTemp) || math.IsNaN(s.SetTemp) {
		// Unknown temperature can never be an exact match.
		return SetTempFallbackC
	}
	return math.Abs(g.SetTemp - s.SetTemp)
}

func nearestInTime(g Group, cands []Set) Match {
	if len(cands) == 0 {
		return Match{Quality: Missing}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		di, dj := days(g.Night, cands[i].Night), days(g.Night, cands[j].Night)
		if di != dj {
			return di < dj
		}
		// On a tie, prefer the set taken after the lights: flats at dawn
		// capture the same dust as the night's lights.
		return cands[i].Night.After(cands[j].Night)
	})
	best := cands[0]
	m := Match{Set: &best, AgeDays: days(g.Night, best.Night), Quality: Fallback}
	if m.AgeDays == 0 {
		m.Quality = Exact
	}
	return m
}
