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
	// over, around its peak: 6 holds 95% of a star at HFR 1.7 to 2.1 against
	// a 24 px aperture, so ordinary seeing barely moves it (Orion's subs at
	// HFR 2.03 and 2.11 kept 1.00 and 0.96 of a clear sub's).
	PhotometryAperture = 6
	// photometryRing is the ring, in pixels, the star's background is the
	// median of. Close in, so the glow haze spreads round bright stars, which
	// reaches far wider, counts as sky and not as the star's light.
	photometryRingIn, photometryRingOut = 10, 14
	// photometrySeparation keeps one peak per star: a clipped star's flat
	// top has many.
	photometrySeparation = 8
	// photometryStars is the most stars measured.
	photometryStars  = 4000
	photometryBorder = photometryRingOut + 2
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
	w, h := im.W, im.H
	d := im.Plane(0)
	med, sigma := skyStats(d)
	thresh := float32(med + detectSig*sigma)

	type peak struct {
		i int
		v float32
	}
	var peaks []peak
	for y := photometryBorder; y < h-photometryBorder; y++ {
		row := y * w
		for x := photometryBorder; x < w-photometryBorder; x++ {
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
	ring := make([]float64, 0, 4*photometryRingOut*photometryRingOut)
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
		for y := py - photometryRingOut; y <= py+photometryRingOut; y++ {
			for x := px - photometryRingOut; x <= px+photometryRingOut; x++ {
				dx, dy := x-px, y-py
				r2 := dx*dx + dy*dy
				v := d[y*w+x]
				switch {
				case r2 <= PhotometryAperture*PhotometryAperture:
					sum += float64(v)
					n++
					sat = sat || v >= saturated
				case r2 > photometryRingIn*photometryRingIn && r2 <= photometryRingOut*photometryRingOut:
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
