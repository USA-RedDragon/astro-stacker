package skyevents

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	// PeakADU is the peak a short sub aims for: half the 16-bit range, so the
	// bias, seeing that is better than typical, and the colour error below
	// cannot push a planet into saturation.
	PeakADU = 32768.0
	// ColourAllowance is how much brighter a planet is taken to be in the
	// imaging band than its V magnitude: the measured zero points are in
	// Gaia G, which runs about 0.2 to 0.4 mag brighter than V for sunlit
	// bodies.
	ColourAllowance = 0.4
	MinShortSeconds = 0.0001
	hfrToSigma      = 1.1774
)

// Photometry is what the short exposure is estimated from.
type Photometry struct {
	Filter    string
	ZeroPoint *float64
	ZPSamples int
	Scale     *float64
	HFR       *float64
	Normal    *float64
	NormalSrc string
}

// ShortSeconds is the longest sub that keeps a body's brightest pixel near
// PeakADU: the body's flux from its magnitude and the measured zero point,
// spread over its disc and the typical star profile.
func ShortSeconds(mag, diameterDeg, zeroPoint, scale, hfr float64) float64 {
	flux := math.Pow(10, -0.4*(mag-ColourAllowance-zeroPoint))
	dpx := diameterDeg * 3600 / scale
	sigma := hfr / scale / hfrToSigma
	area := math.Pi*dpx*dpx/4 + 2*math.Pi*sigma*sigma
	t := PeakADU * area / flux
	if t < MinShortSeconds {
		return MinShortSeconds
	}
	p := math.Pow(10, math.Floor(math.Log10(t))-1)
	v, err := strconv.ParseFloat(strconv.FormatFloat(math.Floor(t/p)*p, 'g', 2, 64), 64)
	if err != nil {
		return MinShortSeconds
	}
	return v
}

// EstimateExposure picks the short sub that keeps every body below
// saturation, and the normal sub for the deep-sky objects.
func EstimateExposure(e Event, p Photometry) Exposure {
	var out Exposure
	var notes []string
	var limit *Member
	short := math.Inf(1)
	known := false
	for i := range e.Members {
		m := &e.Members[i]
		if m.Kind == KindDSO || m.Magnitude == nil {
			continue
		}
		known = true
		if p.ZeroPoint == nil || p.Scale == nil || p.HFR == nil {
			continue
		}
		if s := ShortSeconds(*m.Magnitude, m.Diameter, *p.ZeroPoint, *p.Scale, *p.HFR); s < short {
			short, limit = s, m
		}
	}
	switch {
	case !known:
		notes = append(notes, "No member has a known magnitude, so there is no short set.")
	case p.ZeroPoint == nil:
		notes = append(notes, fmt.Sprintf("Short set not measured yet: no %s zero point has been measured from Gaia stars in the goal measurements.", p.Filter))
	case limit == nil:
		notes = append(notes, "Short set not measured yet: the image scale or the typical star HFR is not measured.")
	default:
		s := short
		out.ShortSeconds = &s
		notes = append(notes, fmt.Sprintf("Short set: %s s keeps the brightest pixel of %s (magnitude %.1f) near %.0f ADU, from the %s zero point %.2f (median of %d measured stacks), %.2f″/px, typical HFR %.1f″, and %.1f mag allowed for the band.",
			trimFloat(s), limit.Name, *limit.Magnitude, PeakADU, p.Filter, *p.ZeroPoint, p.ZPSamples, *p.Scale, *p.HFR, ColourAllowance))
		if s <= MinShortSeconds {
			notes = append(notes, fmt.Sprintf("That is the shortest sub planned (%s s); %s may still saturate at it.", trimFloat(MinShortSeconds), limit.Name))
		}
	}
	if e.Kind == EventBodyDSO {
		if p.Normal != nil {
			n := *p.Normal
			out.NormalSeconds = &n
			notes = append(notes, fmt.Sprintf("Normal set: %s s, %s.", trimFloat(n), p.NormalSrc))
		} else {
			notes = append(notes, "Normal set not measured yet: no "+p.Filter+" exposure length is known.")
		}
	}
	out.Basis = strings.Join(notes, " ")
	return out
}

func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', 3, 64)
}
