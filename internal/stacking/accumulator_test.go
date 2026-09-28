package stacking_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
)

// frame makes a sub: flat sky plus a star-like signal at pixel 0, noise, and
// an exposure-proportional signal, on Siril's 0-1 scale.
func frame(r *rand.Rand, n int, sky, signalPerSec, exposure, noise float64) []float32 {
	p := make([]float32, n)
	for i := range p {
		p[i] = float32(sky + noise*r.NormFloat64())
	}
	p[0] = float32(sky + signalPerSec*exposure + noise*r.NormFloat64())
	return p
}

func TestWeightedMeanMatchesBatch(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 1))
	a := stacking.NewAccumulator(100, 100)
	var wsum, xsum float64
	for i := range 20 {
		exp := 300.0
		if i%2 == 0 {
			exp = 600
		}
		sub := frame(r, 10000, 0.002, 1e-5, exp, 0.0001)
		w := float64(1+i%3) * exp
		if _, err := a.Add(sub, exp, w, stacking.DefaultOptions); err != nil {
			t.Fatal(err)
		}
		// The same quantity, computed directly: signal per second, weighted.
		bg := 0.002 // true sky; the accumulator uses the measured median
		wsum += w
		xsum += w * (float64(sub[0]) - bg) / exp
	}
	got := float64(a.Mean[0])
	want := xsum / wsum
	if math.Abs(got-want) > 2e-7 {
		t.Errorf("mean per second = %.8g, batch = %.8g", got, want)
	}
	if math.Abs(got-1e-5) > 1e-6 {
		t.Errorf("signal per second = %.3g, want about 1e-5", got)
	}
}

func TestMoonlitSkyIsNormalizedNotRejected(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(2, 2))
	a := stacking.NewAccumulator(50, 50)
	for i := range 20 {
		sky := 0.002
		if i >= 10 {
			sky = 0.03 // moonlit
		}
		res, err := a.Add(frame(r, 2500, sky, 1e-5, 300, 0.0001), 300, 300, stacking.DefaultOptions)
		if err != nil {
			t.Fatal(err)
		}
		if res.Rejected > 25 { // ~1% false rejections at most
			t.Fatalf("sub %d: %d pixels rejected", i, res.Rejected)
		}
	}
}

func TestSatelliteTrailIsRejectedAfterWarmUp(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(3, 3))
	a := stacking.NewAccumulator(50, 50)
	for range 12 {
		if _, err := a.Add(frame(r, 2500, 0.002, 0, 300, 0.0001), 300, 300, stacking.DefaultOptions); err != nil {
			t.Fatal(err)
		}
	}
	before := a.Mean[100]
	trail := frame(r, 2500, 0.002, 0, 300, 0.0001)
	for i := 100; i < 150; i++ {
		trail[i] = 0.5
	}
	res, err := a.Add(trail, 300, 300, stacking.DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected < 50 {
		t.Errorf("rejected %d, want the 50 trail pixels", res.Rejected)
	}
	if a.Mean[100] != before {
		t.Errorf("trail pixel moved the mean: %v -> %v", before, a.Mean[100])
	}
}

func TestSaturatedAndEmptyPixelsAreSkipped(t *testing.T) {
	t.Parallel()
	a := stacking.NewAccumulator(2, 2)
	sub := []float32{0, 0.95, 0.002, 0.003}
	res, err := a.Add(sub, 600, 600, stacking.DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	if res.Empty != 1 || res.Saturated != 1 || res.Used != 2 {
		t.Errorf("result %+v", res)
	}
	if a.Weight[0] != 0 || a.Weight[1] != 0 {
		t.Errorf("skipped pixels got weight: %v", a.Weight)
	}
	// A 300 s sub where that star is below saturation fills it in.
	if _, err := a.Add([]float32{0, 0.5, 0.002, 0.003}, 300, 300, stacking.DefaultOptions); err != nil {
		t.Fatal(err)
	}
	if a.Weight[1] == 0 {
		t.Error("shorter sub did not fill the saturated pixel")
	}
}

func TestMasterScalesAndRestoresSky(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(4, 4))
	a := stacking.NewAccumulator(100, 100)
	for range 10 {
		if _, err := a.Add(frame(r, 10000, 0.004, 2e-5, 600, 0.00005), 600, 600, stacking.DefaultOptions); err != nil {
			t.Fatal(err)
		}
	}
	m := a.Master(600)
	// Sky about 0.004 and the star 2e-5 × 600 = 0.012 above it.
	if math.Abs(float64(m[1])-0.004) > 0.0002 || math.Abs(float64(m[0])-0.016) > 0.0005 {
		t.Errorf("sky %v, star %v", m[1], m[0])
	}
}

func TestPlanesRoundTrip(t *testing.T) {
	t.Parallel()
	a := stacking.NewAccumulator(3, 2)
	if _, err := a.Add([]float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6}, 300, 150, stacking.DefaultOptions); err != nil {
		t.Fatal(err)
	}
	b, err := stacking.FromPlanes(3, 2, a.Planes(), a.BackgroundSum, a.WeightSum, a.Subs)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a.Mean {
		if a.Mean[i] != b.Mean[i] || a.Weight[i] != b.Weight[i] || a.M2[i] != b.M2[i] || a.Count[i] != b.Count[i] {
			t.Fatalf("pixel %d differs", i)
		}
	}
	if _, err := stacking.FromPlanes(3, 3, a.Planes(), 0, 0, 0); err == nil {
		t.Error("wrong geometry should fail")
	}
}
