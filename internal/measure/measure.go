// Package measure reads a sub's sky level and star sizes from its pixels,
// for subs Target Scheduler has no record of.
package measure

import (
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// Result is a sub's sky and stars in NINA's terms: ADU on the 16-bit scale,
// and the half-flux radius in pixels.
type Result struct {
	SkyADU float64
	HFR    float64
	Stars  int
}

const (
	adu        = 65535
	detectSig  = 5   // a star's peak is this many σ above the sky
	saturated  = 0.9 // peaks at or above this are clipped and left out
	radius     = 10  // pixels summed around a star
	separation = 20  // stars closer than this to a brighter one are left out
	maxStars   = 300 // the brightest stars measured
	border     = 16  // pixels ignored at the edges
)

// Sub measures one frame's first channel.
func Sub(im *imagedata.Image) Result {
	w, h := im.W, im.H
	d := im.Plane(0)
	med, sigma := skyStats(d)
	thresh := float32(med + detectSig*sigma)

	type peak struct {
		i int
		v float32
	}
	var peaks []peak
	for y := border; y < h-border; y++ {
		row := y * w
		for x := border; x < w-border; x++ {
			i := row + x
			v := d[i]
			if v < thresh || v >= saturated {
				continue
			}
			if v < d[i-1] || v < d[i+1] || v < d[i-w] || v < d[i+w] ||
				v < d[i-w-1] || v < d[i-w+1] || v < d[i+w-1] || v < d[i+w+1] {
				continue
			}
			// A lone hot pixel has no neighbours above the threshold.
			if d[i-1] < thresh && d[i+1] < thresh && d[i-w] < thresh && d[i+w] < thresh {
				continue
			}
			peaks = append(peaks, peak{i, v})
		}
	}
	slices.SortFunc(peaks, func(a, b peak) int {
		switch {
		case a.v > b.v:
			return -1
		case a.v < b.v:
			return 1
		}
		return 0
	})
	var chosen []int
	var hfrs []float64
	for _, p := range peaks {
		if len(chosen) == maxStars {
			break
		}
		px, py := p.i%w, p.i/w
		tooClose := false
		for _, c := range chosen {
			if dx, dy := c%w-px, c/w-py; dx*dx+dy*dy < separation*separation {
				tooClose = true
				break
			}
		}
		if tooClose {
			continue
		}
		if hfr, ok := starHFR(d, w, h, px, py, med); ok {
			chosen = append(chosen, p.i)
			hfrs = append(hfrs, hfr)
		}
	}
	r := Result{SkyADU: med * adu, Stars: len(peaks)}
	if len(hfrs) > 0 {
		slices.Sort(hfrs)
		r.HFR = hfrs[len(hfrs)/2]
	}
	return r
}

// starHFR is the half-flux radius of the star peaking at (px, py): the
// flux-weighted mean distance from its centroid, over the local background.
func starHFR(d []float32, w, h, px, py int, sky float64) (float64, bool) {
	if px < radius+2 || py < radius+2 || px >= w-radius-2 || py >= h-radius-2 {
		return 0, false
	}
	// Background from a ring just outside the star.
	var ring []float64
	for y := py - radius - 2; y <= py+radius+2; y++ {
		for x := px - radius - 2; x <= px+radius+2; x++ {
			dx, dy := x-px, y-py
			if r2 := dx*dx + dy*dy; r2 > radius*radius && r2 <= (radius+2)*(radius+2) {
				ring = append(ring, float64(d[y*w+x]))
			}
		}
	}
	bg := sky
	if len(ring) > 8 {
		slices.Sort(ring)
		bg = ring[len(ring)/2]
	}
	var sum, sx, sy float64
	for y := py - radius; y <= py+radius; y++ {
		for x := px - radius; x <= px+radius; x++ {
			dx, dy := x-px, y-py
			if dx*dx+dy*dy > radius*radius {
				continue
			}
			// Signed: dropping pixels below the background would let noise
			// only add flux, far from the centre, inflating the radius.
			f := float64(d[y*w+x]) - bg
			sum += f
			sx += f * float64(x)
			sy += f * float64(y)
		}
	}
	if sum <= 0 {
		return 0, false
	}
	cx, cy := sx/sum, sy/sum
	var sr float64
	for y := py - radius; y <= py+radius; y++ {
		for x := px - radius; x <= px+radius; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy > radius*radius {
				continue
			}
			f := float64(d[y*w+x]) - bg
			sr += f * math.Sqrt(dx*dx+dy*dy)
		}
	}
	return sr / sum, true
}

// skyStats is the median and MAD-derived σ of a strided sample.
func skyStats(d []float32) (med, sigma float64) {
	stride := max(1, len(d)/400_000)
	s := make([]float64, 0, len(d)/stride+1)
	for i := 0; i < len(d); i += stride {
		if v := d[i]; v > 0 {
			s = append(s, float64(v))
		}
	}
	if len(s) == 0 {
		return 0, 0
	}
	slices.Sort(s)
	med = s[len(s)/2]
	for i := range s {
		s[i] = math.Abs(s[i] - med)
	}
	slices.Sort(s)
	return med, 1.4826 * s[len(s)/2]
}
