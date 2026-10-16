package discoveries

import "testing"

// TestMovesWithTheSky replays Pelican Nebula Panel 2's night of
// 2026-09-14: its defect pixels sat still on the sky in five subs whose
// footprints were read up to 4.3 px apart, as the night was not dithered.
func TestMovesWithTheSky(t *testing.T) {
	t.Parallel()
	mk := func(sensor [][2]float64, off [2]float64) *cand {
		c := &cand{}
		for _, s := range sensor {
			c.fluxes, c.errs = append(c.fluxes, 1), append(c.errs, 0.1)
			c.sharp = append(c.sharp, 0.1)
			c.sensor, c.off = append(c.sensor, s), append(c.off, off)
		}
		return c
	}
	pelican := [][2]float64{{1257.0, 264.7}, {1256.6, 264.8}, {1256.3, 263.9}, {1258.3, 260.9}, {1257.3, 261.9}}
	dithered := [][2]float64{{1257, 264}, {1268, 259}, {1249, 271}, {1262, 276}}
	for _, tc := range []struct {
		name   string
		c      *cand
		fixed  bool
		sharpX float64
	}{
		{"still on the sky, undithered", mk(pelican, [2]float64{0.2, -0.3}), false, 1},
		{"still on the sky across dithers", mk(dithered, [2]float64{0.2, -0.3}), true, 1},
		{"walking across dithers", mk(dithered, [2]float64{1.8, -1.1}), false, 1},
	} {
		sharp, fixed := tc.c.inSubs(0.1)
		if fixed != tc.fixed || sharp != tc.sharpX {
			t.Errorf("%s: fixed %v, sharpness %.1f times a star", tc.name, fixed, sharp)
		}
	}
}

// TestSharpInTheSubsHoldingItsLight replays a defect pixel of Cygnis Loop
// Panel 2's night of 2026-08-19: sharp in the two subs of one dither
// position, faint in three more it counts as present in.
func TestSharpInTheSubsHoldingItsLight(t *testing.T) {
	t.Parallel()
	c := &cand{}
	for _, v := range [][2]float64{{0.3, 1.2}, {5.2, 3.9}, {7.1, 6.4}, {0.4, 1.2}, {0.7, 1.0}, {0.8, 1.4}, {0, 3.9}} {
		c.fluxes, c.errs, c.sharp = append(c.fluxes, v[0]), append(c.errs, 0.1), append(c.sharp, v[1]*0.1)
		c.sensor, c.off = append(c.sensor, [2]float64{0, 0}), append(c.off, [2]float64{0, 0})
	}
	if sharp, _ := c.inSubs(0.1); sharp < maxSubSharpness {
		t.Errorf("%.1f times a star's sharpness", sharp)
	}
}
