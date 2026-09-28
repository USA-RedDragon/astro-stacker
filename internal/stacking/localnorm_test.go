package stacking

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// lnScene is a nebula with stars, in counts per second without sky.
func lnScene(w, h int, r *rand.Rand) []float64 {
	truth := make([]float64, w*h)
	for y := range h {
		for x := range w {
			dx, dy := float64(x-w/2)/float64(w/5), float64(y-h/2)/float64(h/4)
			truth[y*w+x] = 2e-6 * math.Exp(-(dx*dx+dy*dy)/2)
		}
	}
	for range 3000 { // stars with a 1.5 px PSF
		sx, sy, f := r.Float64()*float64(w), r.Float64()*float64(h), 2e-5*math.Pow(r.Float64(), 3)
		for y := max(0, int(sy)-5); y < min(h, int(sy)+6); y++ {
			for x := max(0, int(sx)-5); x < min(w, int(sx)+6); x++ {
				dx, dy := float64(x)-sx, float64(y)-sy
				truth[y*w+x] += f * math.Exp(-(dx*dx+dy*dy)/(2*1.5*1.5))
			}
		}
	}
	return truth
}

// lnSub is the scene seen through a sky with its own gradient and
// transparency, on Siril's 0-1 scale.
func lnSub(truth []float64, w, h int, exposure float64, r *rand.Rand) (sub []float32, scale float64) {
	scale = 0.7 + 0.4*r.Float64()
	a, gx, gy, q := 3e-6, 2e-6*(r.Float64()-0.5), 2e-6*(r.Float64()-0.5), 1e-6*r.Float64()
	sub = make([]float32, w*h)
	for y := range h {
		for x := range w {
			nx, ny := 2*float64(x)/float64(w)-1, 2*float64(y)/float64(h)-1
			sky := a + gx*nx + gy*ny + q*nx*nx
			v := (sky + scale*truth[y*w+x]) * exposure
			sub[y*w+x] = float32(v + 2e-4*r.NormFloat64())
		}
	}
	return sub, scale
}

// seam is the step in a master's background at column edge: the median of
// master - truth in a strip just left of it minus one just right of it.
func seam(acc *Accumulator, truth []float64, edge int) float64 {
	strip := func(x0, x1 int) float64 {
		var d []float64
		for y := range acc.H {
			for x := x0; x < x1; x++ {
				i := y*acc.W + x
				d = append(d, float64(acc.Mean[i])-truth[i])
			}
		}
		slices.Sort(d)
		return d[len(d)/2]
	}
	return math.Abs(strip(edge-16, edge-2) - strip(edge+2, edge+16))
}

// Subs covering only part of the frame, as after a meridian flip or on a
// rotated night, bring their own gradients: without local normalization the
// master steps where their coverage ends.
func TestLocalNormalization(t *testing.T) {
	t.Parallel()
	const w, h, n, exposure = 1536, 1024, 20, 300.0 // 12×8 cells
	edge := w * 6 / 10
	r := rand.New(rand.NewPCG(9, 9))
	truth := lnScene(w, h, r)
	subs := make([][]float32, n)
	scales := make([]float64, n)
	for i := range subs {
		subs[i], scales[i] = lnSub(truth, w, h, exposure, r)
		if i%2 == 1 {
			for y := range h {
				for x := edge; x < w; x++ {
					subs[i][y*w+x] = 0 // outside this sub's footprint
				}
			}
		}
	}
	stored := make([]storedSub, n)
	for i := range stored {
		stored[i] = storedSub{exposure: exposure, weight: exposure}
	}
	stack := func(ln bool) *Accumulator {
		opts := DefaultOptions
		opts.LocalNorm = ln
		acc, err := streamStack(stored, 2, opts, func(i int, _ storedSub) ([]float32, int, int, error) {
			return slices.Clone(subs[i]), w, h, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return acc
	}
	mean := 0.0
	for _, s := range scales {
		mean += s
	}
	mean /= n
	scaled := make([]float64, len(truth))
	for i, v := range truth {
		scaled[i] = v * mean
	}
	flat, local := seam(stack(false), scaled, edge), seam(stack(true), scaled, edge)
	t.Logf("step at the coverage edge: flat %.3g, local %.3g counts/s (noise per pixel %.3g)", flat, local, 2e-4/exposure)
	if local > flat/4 {
		t.Errorf("local normalization left a step of %.3g, flat %.3g", local, flat)
	}

	// A sub fitted against a clean reference recovers its transparency.
	ref := NewAccumulator(w, h)
	for i, v := range scaled {
		ref.Mean[i], ref.Count[i] = float32(v), 10
	}
	ref.Subs = 10
	sky := fitSky(func(i int) float32 { return subs[0][i] }, w, h, exposure, ref, 0.9, 0)
	if want := scales[0] / mean; math.Abs(sky.scale-want) > 0.03 {
		t.Errorf("scale = %.3f, want %.3f", sky.scale, want)
	}
	// Without a reference it falls back to the flat level it was given.
	if sky := fitSky(func(i int) float32 { return subs[0][i] }, w, h, exposure, nil, 0.9, 7); sky.scale != 1 || sky.coef[0] != 7 {
		t.Errorf("fallback = %+v", sky)
	}
}
