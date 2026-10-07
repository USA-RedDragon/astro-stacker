package quality

import "math"

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
// A sub's transparency t is how much light from beyond the sky it recorded,
// against the best subs of the same field. ADUMean - ADUMedian is that light:
// the mean over the frame of what stands above the sky's median, stars and
// nebula together, so it depends neither on seeing nor on which stars a
// detector finds. Haze cuts the signal by t at about the same noise, so the
// weight drops by t^2.
//
// Checked against aperture photometry of the same stars matched across raw
// subs (r = 6 px, 700 to 22,000 stars a sub, 70 subs of four targets), t is
// within 0.05 of the starlight for 53 of the 70:
//
//   - Cygnus Loop Panel 2, Red, the hazy night of 2025-06-29: 0.45, 0.41 and
//     0.35 against 0.44, 0.39 and 0.32 measured. Those subs scored 0.70 to
//     0.74 and now 0.09 to 0.15; clear subs of 2025-06, 2025-11 and 2026-08
//     come out 0.88 to 1.
//   - Andromeda, Green, 2025-10-17: 0.86 to 0.98 against 0.91 to 1, the
//     lower ones at airmass 1.6, where the extinction is real.
//   - Orion, Luminance: clear subs high in the sky 0.93 to 1, and 0.77 to
//     0.90 at airmass 1.75 to 2.4 against 0.71 to 0.91 measured. The hazy
//     sub of 2025-12-26 02:36 comes out 0.71, as did the flux of its 300
//     brightest unsaturated stars (0.70 of a clear sub's), but its star cores
//     kept only 0.44: the haze spread the rest into a halo, which still
//     counts as light above the sky. t takes the light lost, not the blur,
//     so that sub's score (0.73 today) halves to 0.36 rather than falling
//     under the cut.
//
// NINA's DetectedStars was tried and is not used: it follows the seeing more
// than the sky. On Andromeda's clear night of 2025-10-17 it found 1174 stars
// at HFR 1.58 and 3497 at 1.75, where photometry had the two within 3%.
//
// The ADU median is a whole number, and hot pixels and the vignetted sky add
// a few ADU of their own, so t is only measured where the best subs have
// at least transparencyMinExcess ADU of light: nebulae and star fields, not
// a small galaxy in a dark field (the Markarian Chain panels have 4 to 7).
// There, and for a sub without ADU statistics, t is 1 and the score is what
// it was.

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
