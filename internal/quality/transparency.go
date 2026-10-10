package quality

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/measure"
)

// Transparency.
//
// RawWeight sees the sky and the size of the stars, but not haze or thin
// cloud, which take light away before it reaches the camera. NINA's HFR
// doesn't see them either: it is measured on the stars NINA detects, and haze
// leaves it only the bright, compact ones. Orion's Luminance sub at
// 2025-12-26 02:36 local, in haze, had a NINA HFR of 1.70 against 1.75 on a
// clear night, while measured on every star it was 5.45 against 3.04; it
// went into the master with a score of 0.77.
//
// A sub's transparency t is its starlight against the best subs of the same
// field (target, filter, exposure, gain and framing). Haze cuts the signal by
// t at about the same noise, so the score takes t^2.
//
// Starlight is the stacker's own photometry (measure.Photometry): each star's
// flux within 6 px over a ring 10 to 14 px out, compared rank by rank, the
// stars ranked by flux so the same rank holds the same star in every sub of a
// field, clear or hazy, with no matching. Only ranks that rarely clip and
// stay 100σ above the sky are used, and t is their median ratio. Checked
// against the same stars matched across 70 raw subs of four targets, it is
// within 0.05 for 69:
//
//   - Orion, Luminance, 2025-12-26: 0.65, 0.42 and 0.42 at 00:29, 01:11 and
//     02:36 local, against 0.67, 0.44 and 0.44 matched; clear subs of
//     2025-12-19 to 24 high in the sky 0.88 to 1.
//   - Cygnus Loop Panel 2, Red, the hazy night of 2025-06-29: 0.43, 0.39 and
//     0.32 against 0.44, 0.39 and 0.32.
//   - Seeing is not haze: Andromeda's Green subs of 2025-10-17 at NINA HFR
//     1.58 and 1.75 read 0.95 and 0.96 (photometry 0.95, 0.97), and Orion's
//     at HFR 2.03 and 2.11 read 1.00 and 0.96.
//
// The ring keeps the glow haze spreads round stars, which reaches much
// further, out of the star's light. The light above the sky, ADUMean -
// ADUMedian in Target Scheduler's metadata, counts that glow: it read 0.71
// for the 02:36 sub, whose stars kept 0.44. It is the fallback for a sub
// without photometry, or whose field has too few subs with it; it follows
// absorbing haze (0.45, 0.41, 0.35 for the Cygnus Loop subs above).
//
// NINA's DetectedStars was tried and is not used: it follows the seeing more
// than the sky. On Andromeda's clear night of 2025-10-17 it found 1174 stars
// at HFR 1.58 and 3497 at 1.75, where photometry had the two within 3%.
//
// The ADU median is a whole number, and hot pixels and the vignetted sky add
// a few ADU of their own, so the light above the sky is only used where the
// best subs have at least transparencyMinExcess ADU of it: nebulae and star
// fields, not a small galaxy in a dark field (the Markarian Chain panels have
// 4 to 7). There, and for a sub with neither measure, t is 1 and the score is
// what it was.

// transparencyMinExcess is the least light above the sky, in ADU, the
// reference of a group must have for t to be measured.
const transparencyMinExcess = 15

// transparencyMinSubs is the least subs a group needs for a reference: with
// fewer, the "best subs" may all be hazy ones.
const transparencyMinSubs = 10

// framingStep is the bucket, in degrees of rotator angle, subs are grouped by
// for transparency: a field turned to another angle frames other stars and
// nebula. Angles a half turn apart frame the same field (the meridian flip,
// or 90 and 270).
const framingStep = 15

// Framing buckets a rotator angle for transparency (see framingStep).
func Framing(rotation float64) int {
	if math.IsNaN(rotation) || math.IsInf(rotation, 0) {
		return -1
	}
	r := math.Mod(rotation, 180)
	if r < 0 {
		r += 180
	}
	n := int(180 / framingStep)
	return int(math.Round(r/framingStep)) % n
}

// Excess is the light above the sky in a sub, ADUMean - ADUMedian, or NaN
// when either is missing (Target Scheduler leaves out what it couldn't
// measure, which decodes as 0). It may be 0 or below for a sub that recorded
// nothing but sky, as under cloud.
func Excess(aduMean, aduMedian float64) float64 {
	if !(aduMean > 0) || !(aduMedian > 0) {
		return math.NaN()
	}
	return aduMean - aduMedian
}

// TransparencyReference is the excess of the best subs of a group: the
// ReferencePercentile of the measured ones, or NaN when there are too few or
// they have too little light to tell haze from the noise.
func TransparencyReference(excesses []float64) float64 {
	vals := make([]float64, 0, len(excesses))
	for _, e := range excesses {
		if !math.IsNaN(e) {
			vals = append(vals, e)
		}
	}
	if len(vals) < transparencyMinSubs {
		return math.NaN()
	}
	ref := Reference(vals)
	if !(ref >= transparencyMinExcess) {
		return math.NaN()
	}
	return ref
}

// Transparency is a sub's excess against its group's reference, between 0
// and 1, or 1 when either is unknown: the score is then left as it was.
func Transparency(excess, reference float64) float64 {
	if math.IsNaN(excess) || !(reference > 0) {
		return 1
	}
	return math.Min(1, math.Max(0, excess/reference))
}

// Core transparency, from the stacker's own photometry of every light
// (measure.Photometry): the flux of the star at each of a few ranks, against
// the best subs of the field at the same rank.
const (
	// coreMinSNR is the least signal to noise the stars of a rank must have
	// in the field's median sub, so they are still well measured in haze
	// that takes half the light.
	coreMinSNR = 100
	// coreMaxSaturated is the most of a field's subs whose star at a rank
	// may clip, for the rank to be used.
	coreMaxSaturated = 0.1
	// coreMinRanks is the least ranks a sub needs to be judged on.
	coreMinRanks = 3
)

// apertureArea is the number of pixels in measure.PhotometryAperture.
func apertureArea() float64 {
	r := measure.PhotometryAperture
	n := 0
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if x*x+y*y <= r*r {
				n++
			}
		}
	}
	return float64(n)
}

// CoreReference is a field's reference starlight, by rank: the
// ReferencePercentile of the unclipped fluxes of its subs, NaN for a rank
// that clips in too many of them or is too faint to measure through haze.
// It is nil when the field has too few subs measured.
func CoreReference(subs []*measure.Photometry) []float64 {
	n := 0
	for _, s := range subs {
		if s != nil {
			n++
		}
	}
	if n < transparencyMinSubs {
		return nil
	}
	ranks := len(measure.Ranks())
	area := apertureArea()
	ref := make([]float64, ranks)
	for j := range ranks {
		var flux, snr []float64
		clipped := 0
		for _, s := range subs {
			if s == nil || j >= len(s.Flux) {
				continue
			}
			switch {
			case s.Saturated[j]:
				clipped++
			case s.Flux[j] > 0:
				flux = append(flux, s.Flux[j])
				snr = append(snr, s.Flux[j]/(s.Noise*math.Sqrt(area)))
			}
		}
		ref[j] = math.NaN()
		if len(flux) < transparencyMinSubs || float64(clipped) > coreMaxSaturated*float64(len(flux)+clipped) {
			continue
		}
		slices.Sort(snr)
		if snr[len(snr)/2] < coreMinSNR {
			continue
		}
		ref[j] = Reference(flux)
	}
	return ref
}

// CoreTransparency is a sub's starlight against its field's reference: the
// median over the usable ranks of its flux over the reference's, capped at 1;
// NaN when it can't be told.
func CoreTransparency(sub *measure.Photometry, ref []float64) float64 {
	if sub == nil || ref == nil {
		return math.NaN()
	}
	var ratios []float64
	for j, r := range ref {
		if math.IsNaN(r) || j >= len(sub.Flux) || sub.Saturated[j] || !(sub.Flux[j] > 0) {
			continue
		}
		ratios = append(ratios, sub.Flux[j]/r)
	}
	if len(ratios) < coreMinRanks {
		return math.NaN()
	}
	slices.Sort(ratios)
	m := ratios[len(ratios)/2]
	if len(ratios)%2 == 0 {
		m = (ratios[len(ratios)/2-1] + m) / 2
	}
	return math.Min(1, m)
}

const (
	TransparencyPhotometry = "photometry"
	TransparencySkyExcess  = "sky_excess"
)

func transparencyMissing(hasPhot, fieldPhot, hasExcess bool) string {
	var why []string
	switch {
	case !hasPhot:
		why = append(why, "no star photometry of the sub")
	case !fieldPhot:
		why = append(why, fmt.Sprintf("fewer than %d subs of its field have star photometry", transparencyMinSubs))
	default:
		why = append(why, fmt.Sprintf("fewer than %d star ranks are bright and unclipped enough to compare", coreMinRanks))
	}
	if hasExcess {
		why = append(why, fmt.Sprintf("its field has fewer than %d subs, or under %d ADU of light above the sky, to compare against", transparencyMinSubs, transparencyMinExcess))
	} else {
		why = append(why, "no ADU mean in Target Scheduler's record")
	}
	return strings.Join(why, "; ")
}
