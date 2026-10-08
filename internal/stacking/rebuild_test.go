package stacking

import (
	"math"
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
		subs = append(subs, toMemSub(sub, 300, 300, DefaultOptions().SaturationLevel))
	}
	acc := medianAnchored(subs, 50, 50, DefaultOptions())
	for j := 100; j < 150; j++ {
		if acc.Count[j] != 3 {
			t.Fatalf("pixel %d kept %v values, want 3 (trail rejected)", j, acc.Count[j])
		}
		if acc.Mean[j] > 1e-6 {
			t.Fatalf("pixel %d mean %v still has the trail", j, acc.Mean[j])
		}
	}
	if acc.Count[2000] != 4 {
		t.Errorf("clean pixel kept %v of 4", acc.Count[2000])
	}
}

// Two subs can't outvote each other, so both are kept.
func TestMedianAnchoredKeepsBothWithTwoSubs(t *testing.T) {
	t.Parallel()
	a := toMemSub([]float32{0.002, 0.4}, 300, 300, 0.9)
	b := toMemSub([]float32{0.002, 0.002}, 300, 300, 0.9)
	acc := medianAnchored([]memSub{a, b}, 2, 1, DefaultOptions())
	if acc.Count[1] != 2 {
		t.Errorf("kept %v, want 2", acc.Count[1])
	}
}

// A trail in one of 12 subs pulls the plain mean and σ so much it can never
// be 4σ out; judged against the other 11 it's far out.
func TestAddAgainstLeavesTheSubOut(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(6, 6))
	var subs [][]float32
	for i := range 12 {
		sub := noisySub(r, 2500, 0.002, 0.0001)
		if i == 5 {
			for j := range 30 {
				sub[j] = 0.01 // 80σ
			}
		}
		subs = append(subs, sub)
	}
	first := NewAccumulator(50, 50)
	noRejection := DefaultOptions()
	noRejection.MinSamples = math.MaxFloat32
	for _, sub := range subs {
		if _, err := first.Add(sub, 300, 300, noRejection); err != nil {
			t.Fatal(err)
		}
	}
	for _, leaveOut := range []bool{false, true} {
		second := NewAccumulator(50, 50)
		var trail AddResult
		for i, sub := range subs {
			res, err := second.AddAgainst(sub, 300, 300, first, leaveOut, noiseLevel(sub, 0.9), DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			if i == 5 {
				trail = res
			}
		}
		switch {
		case !leaveOut && trail.Rejected >= 30:
			t.Errorf("with the sub in its own statistics, rejected %d; the test no longer shows the masking", trail.Rejected)
		case leaveOut && (trail.Rejected < 93 || trail.Rejected > 97):
			// The 30-pixel trail in row 0: its inner 28 pixels grown 2 px, rows 0-2, x 0-30.
			t.Errorf("leaving the sub out rejected %d, want the 93 pixels around the trail", trail.Rejected)
		}
		if leaveOut && second.Mean[0] > 1e-7 {
			t.Errorf("trail pixel mean %v, want sky", second.Mean[0])
		}
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

func TestGrowWidensClustersNotLonePixels(t *testing.T) {
	t.Parallel()
	const w, h = 20, 20
	mask := make([]bool, w*h)
	mask[3*w+3] = true // lone
	for x := 8; x < 14; x++ {
		mask[10*w+x] = true // a short trail
	}
	got := grow(mask, w, h, 2)
	count := func(m []bool) (n int) {
		for _, v := range m {
			if v {
				n++
			}
		}
		return n
	}
	// The lone pixel stays one pixel; the trail's inner 4 pixels are seeds,
	// widening to x 7-14 over rows 8-12, and the trail's ends stay rejected.
	if !got[3*w+3] || got[3*w+4] {
		t.Error("lone rejection should stay exactly one pixel")
	}
	if want := 1 + 8*5; count(got) != want {
		t.Errorf("rejected %d, want %d", count(got), want)
	}
}
