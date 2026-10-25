package discoveries

import (
	"math"
	"testing"
	"time"
)

// TestRosetteSubsCountTheReferenceOnce is 41445 (Rosette Nebula SHO,
// H-a, 2025-02-28, candidate 289359 of run 804): the eight subs' light and
// picture-wide errors as the run stored them, the local noise 1.51 to 1.66
// times the picture's that 300 empty apertures around the spot measured,
// and the reference of 22 H-a subs of the same exposure on six nights
// (2025-02-01 holding 10). The subs read 10.6 sigma as stored and 6.4 to
// 7.0 with the local noise alone; the audit's own photometry gave 4.3 to 5
// on the stack and 5.3 from its per-frame values with the reference counted
// once. The subs must read 4.5 to 6.2 sigma.
func TestRosetteSubsCountTheReferenceOnce(t *testing.T) {
	t.Parallel()
	flux := []float64{0.0036769, 0.0023902, 0.0013652, 0.0002915, 0.0036796, 0.0028264, 0.0045582, 0.0027023}
	errs := []float64{0.0007051, 0.0007199, 0.0006946, 0.0006857, 0.0007087, 0.0007274, 0.0007260, 0.0007790}
	var ref []Sub
	id := 0
	for _, n := range []struct {
		day, subs int
	}{{1, 10}, {2, 1}, {23, 3}, {24, 3}, {25, 3}, {27, 2}} {
		for range n.subs {
			id++
			ref = append(ref, Sub{FrameID: id, Night: time.Date(2025, 2, n.day, 0, 0, 0, 0, time.UTC)})
		}
	}
	fs := &filterStacks{f: FilterInput{Filter: testNarrowband, Ref: ref}, cache: &prepCache{m: map[int]prep{}}}
	for _, s := range ref {
		fs.cache.m[s.FrameID] = prep{rms: 1}
	}
	share := refShare(fs.refNoise(), 1)
	for _, scale := range []float64{1.51, 1.66} {
		c := &cand{noiseScale: scale}
		alone := make([]float64, 0, len(flux))
		for k := range flux {
			e := errs[k] * c.localScale()
			c.fluxes, c.errs = append(c.fluxes, flux[k]), append(c.errs, e)
			c.shared = append(c.shared, share*e)
			c.Light = append(c.Light, LightPoint{Filter: testNarrowband})
			alone = append(alone, e)
		}
		indep := perSub(c.fluxes, alone).meanSigma
		got := c.perSub().meanSigma
		t.Logf("local noise %.2f times: %.2f sigma with independent errors, %.2f with the reference once (share %.3f)", scale, indep, got, share)
		if got < 4.5 || got > 6.2 {
			t.Errorf("local noise %.2f times: %.2f sigma", scale, got)
		}
		if ratio := indep / got; math.Abs(ratio-1.147) > 0.03 {
			t.Errorf("the reference's noise changes the subs' sigma by %.3f", ratio)
		}
	}
}

// TestSharedNoiseCancelsBetweenNights: the reference-night check compares
// two measures minus the same reference, so it takes each sub's error
// without the reference's part.
func TestSharedNoiseCancelsBetweenNights(t *testing.T) {
	t.Parallel()
	c := &cand{fluxes: []float64{1}, errs: []float64{0.5}, shared: []float64{0.3}}
	if got := c.ownErr(0); math.Abs(got-0.4) > 1e-12 {
		t.Errorf("own error %.3f", got)
	}
	if got := sharedVar([]float64{0.3, 0.3, 0.2}, []string{"R", "R", "G"}); math.Abs(got-0.18) > 1e-12 {
		t.Errorf("shared variance %.3f", got)
	}
}
