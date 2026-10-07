package measure

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// starField is a sub of n Gaussian stars of width sigma, their light scaled
// by gain, some bright enough to clip, on a sky with noise, the same field
// for the same seed.
func starField(seed uint64, n int, sigma, gain float64) *imagedata.Image {
	const w, h = 1600, 1200
	field := rand.New(rand.NewPCG(seed, 1))
	noise := rand.New(rand.NewPCG(seed, 2))
	im := &imagedata.Image{W: w, H: h, C: 1, Data: make([]float32, w*h)}
	for i := range im.Data {
		im.Data[i] = float32(0.01 + 0.0005*noise.NormFloat64())
	}
	for range n {
		// Fluxes spread over 6 magnitudes, as in a real field.
		sx, sy := 30+field.Float64()*(w-60), 30+field.Float64()*(h-60)
		flux := gain * 40 * math.Pow(10, -0.4*6*field.Float64())
		peak := flux / (2 * math.Pi * sigma * sigma)
		r := int(math.Ceil(5 * sigma))
		for y := int(sy) - r; y <= int(sy)+r; y++ {
			for x := int(sx) - r; x <= int(sx)+r; x++ {
				if x < 0 || y < 0 || x >= w || y >= h {
					continue
				}
				dx, dy := float64(x)-sx, float64(y)-sy
				i := y*w + x
				im.Data[i] = min(1, im.Data[i]+float32(peak*math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma))))
			}
		}
	}
	return im
}

// medianRatio is the median over ranks unclipped in both of a's flux over b's.
func medianRatio(t *testing.T, a, b Photometry) float64 {
	t.Helper()
	var rs []float64
	for j := range a.Flux {
		if a.Saturated[j] || b.Saturated[j] || !(a.Flux[j] > 0) || !(b.Flux[j] > 0) {
			continue
		}
		rs = append(rs, a.Flux[j]/b.Flux[j])
	}
	if len(rs) < 5 {
		t.Fatalf("only %d ranks to compare", len(rs))
	}
	slices.Sort(rs)
	return rs[len(rs)/2]
}

// Haze that takes 56% of the light, as on Orion's 2025-12-26 02:36 sub, is
// read as 0.44 by rank, though fewer of its stars clip; seeing that widens the
// stars from HFR 1.8 to 2.3 is not taken for haze.
func TestPhotometryRanks(t *testing.T) {
	t.Parallel()
	clear := Measure(starField(7, 3000, 1.45, 1))
	clipped := 0
	for _, s := range clear.Saturated {
		if s {
			clipped++
		}
	}
	if clipped == 0 {
		t.Fatal("no clipped ranks: the test should cover them")
	}
	if got := medianRatio(t, Measure(starField(7, 3000, 1.45, 0.44)), clear); math.Abs(got-0.44) > 0.03 {
		t.Errorf("hazy sub reads %.3f of the clear one, want 0.44", got)
	}
	if got := medianRatio(t, Measure(starField(7, 3000, 1.8, 1)), clear); got < 0.93 {
		t.Errorf("softer seeing reads %.3f of the clear sub, want 0.93 or more", got)
	}
	// Star wings in so dense a field add a little to the sky's spread.
	if want := 0.0005 * 65535; clear.Noise < want || clear.Noise > 1.25*want {
		t.Errorf("noise = %.1f ADU, want %.1f to 25%% more", clear.Noise, want)
	}
}
