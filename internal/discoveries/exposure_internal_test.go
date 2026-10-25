package discoveries

import (
	"math"
	"testing"
)

// orionMixed is candidate 298277's light (Orion, H-a, 2026-10-05, run 44)
// as the run stored it, in time order: 120 s and 600 s subs interleaved.
func orionMixed(short float64) *cand {
	expo := []float64{120, 600, 120, 600, 120, 600, 120, 600, 120}
	flux := []float64{-0.0005, 0.0023, -0.0021, 0.0033, 0.0033, 0.0018, 0.0004, 0.0020, 0.0025}
	errs := []float64{0.0025, 0.0007, 0.0026, 0.0007, 0.0025, 0.0007, 0.0025, 0.0007, 0.0027}
	c := &cand{}
	c.Sign = 1
	for k := range expo {
		f := flux[k]
		if expo[k] == 120 && !math.IsNaN(short) {
			f = short
		}
		c.fluxes, c.errs, c.expo = append(c.fluxes, f), append(c.errs, errs[k]), append(c.expo, expo[k])
		c.Light = append(c.Light, LightPoint{Filter: testNarrowband})
	}
	return c
}

// TestMixedExposuresJudgeEachGroup: 298277's 120 s subs alone hold about
// nothing where its 600 s subs stand at 6.7 sigma, and the change must be
// rejected; with the 600 s subs' light in the 120 s subs too it stands.
func TestMixedExposuresJudgeEachGroup(t *testing.T) {
	t.Parallel()
	c := orionMixed(math.NaN())
	var short []int
	for k, e := range c.expo {
		if e == 120 {
			short = append(short, k)
		}
	}
	if s := c.pick(short).perSub().meanSigma; math.Abs(s) > 1 {
		t.Errorf("120 s subs alone: %.2f sigma", s)
	}
	why := c.exposureGroups()
	if why == "" {
		t.Fatal("the 600 s subs alone carried it")
	}
	t.Log(why)
	if why := orionMixed(0.0024).exposureGroups(); why != "" {
		t.Errorf("in both groups: %s", why)
	}
}
