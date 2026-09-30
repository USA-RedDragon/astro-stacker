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
	// SetTempScaleMaxC is how far a dark's setpoint may be from the lights'
	// when the dark's thermal signal is scaled to fit (dark optimization).
	// Dark current roughly doubles every 6 °C, which bias-subtracted scaling
	// absorbs; beyond this the hot pixel population drifts too far.
	SetTempScaleMaxC = 10.0
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
	Type string // FLAT, DARK, BIAS
	// Night is a flat set's night; for dark and bias sets, which may span
	// nights, it is the night of the newest frame.
	Night    time.Time
	Object   string // Target Scheduler names flats after their target
	Filter   string
	Exposure float64
	Gain     float64
	Offset   float64
	SetTemp  float64
	BinX     float64
	Rotator  float64
	Count    int
	// From and To are the DATE-OBS of the first and last frame of a dark or
	// bias set (coverage.Sets); zero for flats, which go by night.
	From, To time.Time
	// Uploaded is when the set's newest frame was uploaded; zero when
	// unknown.
	Uploaded time.Time
	// Master is the key, in the source bucket, of a master made elsewhere
	// for a set whose frames were never uploaded. Empty for sets built from
	// their frames.
	Master string
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
	// Scaled is set when a dark needs its thermal signal scaled because the
	// setpoint or exposure differs; that needs a master bias.
	Scaled bool
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

// Flats must match filter and binning. The same gain and offset are
// preferred but not required: a master flat is calibrated with the bias
// for its own gain and offset and then normalized, which leaves only the
// illumination's shape, and that doesn't depend on gain. Rotation is
// preferred but not required either. The rotator turns the camera and
// filter wheel together, so dust on the sensor window and filters stays put
// relative to the pixels; only the dimmer, rotation-symmetric vignetting and
// dust on the optics in front of the rotator move. A flat at another gain
// or angle is a fallback.
func chooseFlat(g Group, sets []Set) Match {
	var cands []Set
	for _, s := range sets {
		if s.Type == "FLAT" && buildable(s) && s.Filter == g.Filter && same(s.BinX, g.BinX) {
			cands = append(cands, s)
		}
	}
	if len(cands) == 0 {
		return Match{Quality: Missing}
	}
	sameGain := func(s Set) bool { return same(s.Gain, g.Gain) && same(s.Offset, g.Offset) }
	sort.SliceStable(cands, func(i, j int) bool {
		if gi, gj := sameGain(cands[i]), sameGain(cands[j]); gi != gj {
			return gi
		}
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
	if m.AgeDays == 0 && !m.RotationMismatch && sameGain(best) {
		m.Quality = Exact
	}
	return m
}

// Bias must match gain, offset and binning. Its temperature barely matters on
// CMOS sensors, so the nearest night wins.
func chooseBias(g Group, sets []Set) Match {
	var cands []Set
	for _, s := range sets {
		if s.Type == "BIAS" && buildable(s) && same(s.Gain, g.Gain) && same(s.Offset, g.Offset) && same(s.BinX, g.BinX) {
			cands = append(cands, s)
		}
	}
	return nearestInTime(g, cands)
}

// Darks must match gain, offset and binning. Their thermal signal is scaled
// to the lights (bias-subtracted dark optimization), and the IMX571 has no amp
// glow, so neither the setpoint nor the exposure has to match exactly. The
// closest setpoint wins, then a same-exposure dark, then the longest one
// (scaling down adds less noise), then the nearest night.
func chooseDark(g Group, sets []Set) Match {
	var cands []Set
	for _, s := range sets {
		if s.Type != "DARK" || !buildable(s) || !same(s.Gain, g.Gain) || !same(s.Offset, g.Offset) || !same(s.BinX, g.BinX) {
			continue
		}
		if TempOff(g, s) > SetTempScaleMaxC || !(s.Exposure > 0) {
			continue
		}
		cands = append(cands, s)
	}
	if len(cands) == 0 {
		return Match{Quality: Missing}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		ti, tj := TempOff(g, cands[i]), TempOff(g, cands[j])
		if ti != tj {
			return ti < tj
		}
		ei, ej := sameExposure(g, cands[i]), sameExposure(g, cands[j])
		if ei != ej {
			return ei
		}
		if cands[i].Exposure != cands[j].Exposure {
			return cands[i].Exposure > cands[j].Exposure
		}
		return days(g.Night, cands[i].Night) < days(g.Night, cands[j].Night)
	})
	best := cands[0]
	m := Match{Set: &best, AgeDays: days(g.Night, best.Night), TempOff: TempOff(g, best), Quality: Fallback}
	m.Scaled = m.TempOff > SetTempExactC || !sameExposure(g, best)
	if !m.Scaled {
		m.Quality = Exact
	}
	return m
}

// MinFrames is the fewest frames a master is built from; a set with fewer,
// such as darks mostly left out for a light leak, is passed over for the
// next best rather than failing every light that matches it.
const MinFrames = 3

// buildable reports whether a set can give a master: a master made
// elsewhere (Master), a set of at least MinFrames frames, or one whose count
// isn't known (0).
func buildable(s Set) bool { return s.Master != "" || s.Count == 0 || s.Count >= MinFrames }

func sameExposure(g Group, s Set) bool {
	return math.Abs(s.Exposure-g.Exposure) <= ExposureTolerance*g.Exposure
}

// TempOff is how far a dark's setpoint is from the lights', in °C.
func TempOff(g Group, s Set) float64 {
	if math.IsNaN(g.SetTemp) || math.IsNaN(s.SetTemp) {
		// Unknown temperature can never be an exact match, but is usable.
		return SetTempScaleMaxC
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
