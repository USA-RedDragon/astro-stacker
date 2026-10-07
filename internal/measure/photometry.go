package measure

import (
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// Photometry is the starlight of a sub's brightest stars, for telling haze
// (see quality's transparency): the flux of the star at each of Ranks, the
// stars ranked by flux, saturated ones included so the ranks of two subs of
// one field hold the same stars however clear the sky. Saturated marks the
// ranks whose star clips, whose flux is short; a flux of 0 is past the last
// star.
type Photometry struct {
	// Noise is the sky's σ in ADU, to judge which ranks are well above it.
	Noise     float64   `json:"noise"`
	Flux      []float64 `json:"flux"` // ADU, summed over PhotometryAperture
	Saturated []bool    `json:"sat"`
}

// PhotometryRevision is how Photometry is measured; lights measured by an
// older one are measured again.
const PhotometryRevision = 1

const (
	// PhotometryAperture is the radius, in pixels, a star's flux is summed
	// over, around its peak, the background from a ring 4 to 8 px beyond it.
	// A fixed 6 px holds seeing apart from haze: Orion's Luminance subs at
	// HFR 2.4 and 2.5 (measured here; NINA 2.03, 2.11) read 1.09 and 1.02 of
	// a clear sub at 1.9; IC 4604 Panel 12's of 2026-07-20 at HFR 2.2 to 2.9
	// read 0.95 to 1, those of 2026-08-04 at the same HFR and altitude 0.28
	// to 0.58. Scaling the radius with HFR (4·HFR, so
	// 12 px holds 95% at HFR 3) was tried and is worse: subs measured with
	// different radii don't compare (Orion's 02:45 sub read 1.31 at 10 px
	// against a reference at 8), and a wider aperture counts the light haze
	// scatters a few pixels out as the star's (the hazy 01:11 sub reads 0.47
	// at 6 px, 0.60 at 10).
	PhotometryAperture = 6
	// photometrySeparation keeps one peak per star: a clipped star's flat
	// top has many.
	photometrySeparation = 8
	// photometryStars is the most stars measured.
	photometryStars = 4000
)

// Ranks are the star ranks Photometry keeps: 32 up to 4096, four a doubling.
func Ranks() []int {
	out := make([]int, 0, 29)
	for i := range 29 {
		out = append(out, int(math.Round(32*math.Pow(2, float64(i)/4))))
	}
	return out
}

// Measure measures a sub's Photometry from its first channel.
func Measure(im *imagedata.Image) Photometry {
	return measureAt(im, PhotometryAperture)
}

// measureAt is Measure with an aperture of radius r, the ring 4 to 8 px
// beyond it.
func measureAt(im *imagedata.Image, r int) Photometry {
	w, h := im.W, im.H
	d := im.Plane(0)
	med, sigma := skyStats(d)
	thresh := float32(med + detectSig*sigma)
	ringIn, ringOut := r+4, r+8
	border := ringOut + 2

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
			if v < thresh {
				continue
			}
			if v < d[i-1] || v < d[i+1] || v < d[i-w] || v < d[i+w] ||
				v < d[i-w-1] || v < d[i-w+1] || v < d[i+w-1] || v < d[i+w+1] {
				continue
			}
			if d[i-1] < thresh && d[i+1] < thresh && d[i-w] < thresh && d[i+w] < thresh {
				continue // a lone hot pixel
			}
			peaks = append(peaks, peak{i, v})
		}
	}
	slices.SortStableFunc(peaks, func(a, b peak) int {
		switch {
		case a.v > b.v:
			return -1
		case a.v < b.v:
			return 1
		}
		return 0
	})

	// One peak a star: the brightest, none within photometrySeparation of
	// one taken, found through a grid of that cell size.
	const sep = photometrySeparation
	gw := w/sep + 1
	grid := map[int][]int{}
	type star struct {
		flux      float64
		saturated bool
	}
	var stars []star
	ring := make([]float64, 0, 4*ringOut*ringOut)
	for _, p := range peaks {
		if len(stars) == photometryStars {
			break
		}
		px, py := p.i%w, p.i/w
		gx, gy := px/sep, py/sep
		clash := false
		for dy := -1; dy <= 1 && !clash; dy++ {
			for dx := -1; dx <= 1 && !clash; dx++ {
				for _, c := range grid[(gy+dy)*gw+gx+dx] {
					if ex, ey := c%w-px, c/w-py; ex*ex+ey*ey < sep*sep {
						clash = true
						break
					}
				}
			}
		}
		if clash {
			continue
		}
		grid[gy*gw+gx] = append(grid[gy*gw+gx], p.i)

		ring = ring[:0]
		var sum float64
		n := 0
		sat := false
		for y := py - ringOut; y <= py+ringOut; y++ {
			for x := px - ringOut; x <= px+ringOut; x++ {
				dx, dy := x-px, y-py
				r2 := dx*dx + dy*dy
				v := d[y*w+x]
				switch {
				case r2 <= r*r:
					sum += float64(v)
					n++
					sat = sat || v >= saturated
				case r2 > ringIn*ringIn && r2 <= ringOut*ringOut:
					ring = append(ring, float64(v))
				}
			}
		}
		slices.Sort(ring)
		bg := ring[len(ring)/2]
		stars = append(stars, star{flux: (sum - float64(n)*bg) * adu, saturated: sat})
	}
	slices.SortFunc(stars, func(a, b star) int {
		switch {
		case a.flux > b.flux:
			return -1
		case a.flux < b.flux:
			return 1
		}
		return 0
	})

	ranks := Ranks()
	ph := Photometry{Noise: sigma * adu, Flux: make([]float64, len(ranks)), Saturated: make([]bool, len(ranks))}
	for j, k := range ranks {
		if k >= len(stars) {
			continue
		}
		ph.Flux[j], ph.Saturated[j] = stars[k].flux, stars[k].saturated
	}
	return ph
}
