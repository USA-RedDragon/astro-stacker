package stacking

import (
	"math"
	"math/rand/v2"
	"testing"
)

// The fit recovers the relation between two masters despite noise and a
// patch of structure only the target filter sees.
func TestFitLinearRecoversScaleAndOffset(t *testing.T) {
	t.Parallel()
	const w, h = 400, 300
	r := rand.New(rand.NewPCG(3, 3))
	ref := make([]float32, w*h)
	target := make([]float32, w*h)
	for i := range ref {
		sky := 0.05 + 0.1*float64(i%w)/w + 0.05*r.Float64() // gradient plus faint signal
		ref[i] = float32(sky + 0.002*r.NormFloat64())
		target[i] = float32((sky-0.02)/2.5 + 0.001*r.NormFloat64()) // ref = 0.02 + 2.5×target
	}
	for y := 50; y < 100; y++ { // emission only the target filter sees
		for x := 50; x < 150; x++ {
			target[y*w+x] += 0.2
		}
	}
	target[0], ref[1] = 0, 0.99 // no data; saturated
	offset, scale, ok := fitLinear(target, ref, w, Rect{W: w, H: h})
	if !ok || math.Abs(scale-2.5) > 0.02 || math.Abs(offset-0.02) > 0.002 {
		t.Errorf("offset %v scale %v ok %v, want 0.02 2.5", offset, scale, ok)
	}
	applyLinear(target, offset, scale)
	if target[0] != 0 {
		t.Errorf("empty pixel = %v, want 0", target[0])
	}
}

func TestSexagesimal(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		v      float64
		signed bool
		want   string
	}{
		{0.7123, false, "00 42 44.28"},
		{41.2692, true, "+41 16 09.1"},
		{-5.391, true, "-05 23 27.6"},
		{23.99999999, false, "00 00 00.00"},
	} {
		if got := sexagesimal(c.v, c.signed); got != c.want {
			t.Errorf("sexagesimal(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}
