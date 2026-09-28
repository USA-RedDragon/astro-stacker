package stacking

import (
	"math/rand/v2"
	"testing"
)

func noisySub(r *rand.Rand, n int, sky, noise float64) []float32 {
	p := make([]float32, n)
	for i := range p {
		p[i] = float32(sky + noise*r.NormFloat64())
	}
	return p
}

// A trail in the very first sub must not survive the warm-up stack once
// three subs cover the pixel.
func TestMedianAnchoredRemovesFirstSubTrail(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(5, 5))
	const n = 2500
	var subs []memSub
	for i := range 4 {
		sub := noisySub(r, n, 0.002, 0.0001)
		if i == 0 {
			for j := 100; j < 150; j++ {
				sub[j] = 0.4
			}
		}
		subs = append(subs, toMemSub(sub, 300, 300, DefaultOptions.SaturationLevel))
	}
	acc := medianAnchored(subs, 50, 50, DefaultOptions)
	for j := 100; j < 150; j++ {
		if acc.Count[j] != 3 {
			t.Fatalf("pixel %d kept %v values, want 3 (trail rejected)", j, acc.Count[j])
		}
		if acc.Mean[j] > 1e-6 {
			t.Fatalf("pixel %d mean %v still has the trail", j, acc.Mean[j])
		}
	}
	if acc.Count[0] != 4 {
		t.Errorf("clean pixel kept %v of 4", acc.Count[0])
	}
}

// Two subs can't outvote each other, so both are kept.
func TestMedianAnchoredKeepsBothWithTwoSubs(t *testing.T) {
	t.Parallel()
	a := toMemSub([]float32{0.002, 0.4}, 300, 300, 0.9)
	b := toMemSub([]float32{0.002, 0.002}, 300, 300, 0.9)
	acc := medianAnchored([]memSub{a, b}, 2, 1, DefaultOptions)
	if acc.Count[1] != 2 {
		t.Errorf("kept %v, want 2", acc.Count[1])
	}
}

func TestAddAgainstRejectsWithFixedStats(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(6, 6))
	first := NewAccumulator(50, 50)
	for range 10 {
		if _, err := first.Add(noisySub(r, 2500, 0.002, 0.0001), 300, 300, DefaultOptions); err != nil {
			t.Fatal(err)
		}
	}
	mean, std := first.Stats()
	second := NewAccumulator(50, 50)
	trail := noisySub(r, 2500, 0.002, 0.0001)
	for j := 0; j < 30; j++ {
		trail[j] = 0.3
	}
	res, err := second.AddAgainst(trail, 300, 300, mean, std, noiseLevel(trail, 0.9), DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected < 30 || res.Rejected > 45 {
		t.Errorf("rejected %d, want the 30 trail pixels and few others", res.Rejected)
	}
}

func TestQuantizeKeepsNegativeSky(t *testing.T) {
	t.Parallel()
	for _, v := range []float32{-0.003, -0.0001, 0.0015, 0.5, 0.95} {
		if got := dequantize(quantize(v)); abs32(got-v) > 2e-5 {
			t.Errorf("%v -> %v", v, got)
		}
	}
	if quantize(0) != 0 || quantize(-0.00001) == 0 {
		t.Error("only exact zeros may map to the empty value")
	}
}

// Sky below zero must still give the right background, not one measured from
// the few bright pixels.
func TestBackgroundWithNegativeSky(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(7, 7))
	p := noisySub(r, 100_000, -0.002, 0.0003)
	for i := 0; i < 1000; i++ {
		p[i*97] = 0.3 // stars
	}
	if bg := background(p, 0.9); abs32(float32(bg)+0.002) > 0.0001 {
		t.Errorf("background %v, want about -0.002", bg)
	}
}
