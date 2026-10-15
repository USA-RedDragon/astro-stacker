package discoveries

import (
	"math"
	"testing"
)

func TestHotPixelsAreSharperThanStars(t *testing.T) {
	t.Parallel()
	// Real differences, sign applied, in units of their noise, centred on the
	// candidate's pixel: the hot pixels of candidates 13956 (Garlic Nebula
	// O-III, 2026-10-08) and 13767 (Caldwell 14, 2026-10-09), the hot pixel the
	// night of Cygnis Loop Panel 2's O-III 2026-10-12 put 2.7" from a VSX star
	// (candidate 12738), and the M 31 nova (candidate 13267).
	sharpCases := []struct {
		id             int
		dx, dy, hn, hr float64
		hot            bool
		d              [7][7]float64
	}{
		{13956, -0.498, 0.287, 1.588, 1.728, true, [7][7]float64{
			{-1.5, 0.7, 0.1, -0.9, -1.0, 1.5, 0.1},
			{-0.4, -0.5, -0.7, 0.2, -0.8, 1.1, -0.7},
			{0.2, -1.6, -1.6, 44.3, -0.8, 0.9, -1.3},
			{-1.0, -0.9, 77.7, 33.8, -0.3, 2.1, 0.0},
			{-0.9, -1.3, -1.9, -0.3, 0.0, 0.2, -0.7},
			{-0.4, -0.4, -2.5, 0.7, -0.6, 1.7, -1.3},
			{-1.4, 0.0, 0.4, -0.1, 0.1, 0.6, -1.4}}},
		{13767, 0.103, 0.422, 1.691, 1.772, true, [7][7]float64{
			{-0.5, 0.9, 1.8, 1.9, 0.3, 3.9, -0.1},
			{0.7, 0.8, -0.3, -0.7, -1.7, 1.3, 0.1},
			{-0.9, -0.4, 0.4, 31.8, 5.9, 0.4, -0.1},
			{-0.4, 1.2, 0.3, 41.0, 3.0, 1.0, 0.9},
			{0.0, 1.6, -0.2, 1.9, 0.5, 0.7, 0.1},
			{0.6, 0.7, -0.4, 1.1, -0.4, -1.3, 0.0},
			{-0.1, 0.7, 0.8, 0.6, -0.5, -0.7, 1.2}}},
		{12738, -0.103, 0.418, 1.574, 1.564, true, [7][7]float64{
			{-0.4, 0.8, 0.5, -2.4, 4.6, 4.9, 1.1},
			{-0.8, -2.1, 6.0, -5.2, -8.2, 4.2, 2.7},
			{0.1, 0.9, 54.7, -1.3, 3.2, 6.5, 2.0},
			{1.1, 0.8, 10.3, 160.7, -1.4, 1.2, 1.8},
			{0.0, -1.1, -0.5, -0.3, -0.6, -0.6, -1.6},
			{0.5, -0.5, 0.4, 0.6, -1.0, -1.0, -1.2},
			{1.3, -1.5, 0.0, 0.5, -1.5, -1.4, -0.9}}},
		{13267, -0.107, 0.082, 1.706, 1.748, false, [7][7]float64{
			{1.2, 2.2, 1.2, 0.9, 0.7, -0.4, -0.2},
			{1.0, 2.1, 0.4, 0.1, 1.0, 0.4, -0.4},
			{0.6, 3.7, 1.6, 3.0, 1.1, 0.4, 1.2},
			{-0.4, -0.2, 2.3, 4.2, 2.2, -0.8, 0.7},
			{1.2, 0.6, 1.4, 3.4, 2.3, 1.4, -1.6},
			{0.2, 0.6, 0.6, 0.5, -0.4, -0.1, 0.9},
			{0.0, 0.9, 1.3, 0.7, 0.7, 0.0, 0.6}}},
	}
	const n, off = 41, 17
	for _, c := range sharpCases {
		p := NewPlane(n, n)
		for y := range 7 {
			for x := range 7 {
				p.Data[(off+y)*n+off+x] = float32(c.d[y][x])
			}
		}
		rms := make([]float32, n*n)
		for i := range rms {
			rms[i] = 1
		}
		x, y := off+3+c.dx, off+3+c.dy
		limit := maxSharpness * psfSharpness(math.Max(c.hn, c.hr)/hfrToSigma)
		s := sharpness(p, x, y)
		t.Logf("candidate %d: sharpness %.3f, limit %.3f (%.2f times a star)", c.id, s, limit, s/limit*maxSharpness)
		if c.hot != (s > limit) {
			t.Errorf("candidate %d: sharpness %.3f against the limit %.3f, hot pixel %v", c.id, s, limit, c.hot)
		}
		if !c.hot {
			continue
		}
		srcs := Extract(p, rms, nil, DetectSigma, 3)
		if len(srcs) != 1 {
			t.Fatalf("candidate %d: %d sources", c.id, len(srcs))
		}
		o := srcs[0]
		sigma := c.hn / hfrToSigma
		if o.B/o.A <= minAxisRatio || o.NPix < minPixels || o.A >= 4*sigma {
			t.Errorf("candidate %d: the shape cut alone now rejects it (a %.2f b %.2f npix %d); this test shows why it did not", c.id, o.A, o.B, o.NPix)
		}
	}
}

func TestPSFSharpness(t *testing.T) {
	t.Parallel()
	if a, b := psfSharpness(1.0), psfSharpness(2.0); !(a > b) || a > 0.5 || b < 0.03 {
		t.Errorf("sharpness of sigma 1 %.3f, of sigma 2 %.3f", a, b)
	}
}
