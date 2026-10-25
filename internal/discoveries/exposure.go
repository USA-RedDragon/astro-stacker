package discoveries

import (
	"fmt"
	"math"
	"slices"
)

// exposureGroups says which exposure time's subs of a filter do not hold
// a change the filter's other subs show, or "": where one night mixes
// exposure times, each group of minExposureSubs subs or more that could
// see the change, the others' mean light standing meanAgreeSigma over the
// group's noise, must hold it at meanAgreeSigma. Orion's H-a of 2026-10-05
// interleaved five 120 s and four 600 s subs at dawn, and candidate 298277,
// a star's wing under a blurred PSF, was in the 600 s subs alone: 6.7 sigma
// there, 0.6 in the 120 s subs where their noise let the 600 s light show
// at 2.1.
func (c *cand) exposureGroups() string {
	if len(c.expo) != len(c.fluxes) {
		return ""
	}
	for _, f := range filtersOf(c.Light) {
		groups := map[float64][]int{}
		var times []float64
		for k, pt := range c.Light {
			if pt.Filter != f || k >= len(c.fluxes) || !(c.expo[k] > 0) {
				continue
			}
			e := math.Round(c.expo[k])
			if groups[e] == nil {
				times = append(times, e)
			}
			groups[e] = append(groups[e], k)
		}
		if len(times) < 2 {
			continue
		}
		slices.Sort(times)
		for _, e := range times {
			g := groups[e]
			if len(g) < minExposureSubs {
				continue
			}
			var others []float64
			for _, o := range times {
				if o != e {
					for _, k := range groups[o] {
						others = append(others, c.fluxes[k])
					}
				}
			}
			m := c.pick(g)
			sigma := m.meanNoise()
			if !(sigma > 0) || len(others) == 0 {
				continue
			}
			var sum, want float64
			for _, v := range m.fluxes {
				sum += v
			}
			for _, v := range others {
				want += v
			}
			got, could := sum/float64(len(g))/sigma, want/float64(len(others))/sigma
			if could >= meanAgreeSigma && got < meanAgreeSigma {
				return fmt.Sprintf("not in the %s %.0f s subs: %.1f sigma where the other subs' light would stand at %.1f", f, e, got, could)
			}
		}
	}
	return ""
}

// minExposureSubs is the fewest subs of one exposure time judged alone.
const minExposureSubs = 2

// meanNoise is the error of the mean of the candidate's subs, the
// reference's noise counted once per filter.
func (c *cand) meanNoise() float64 {
	n := len(c.fluxes)
	if n == 0 {
		return 0
	}
	groups := make([]string, n)
	var v float64
	for k := range n {
		v += c.errs[k] * c.errs[k]
		if k < len(c.Light) {
			groups[k] = c.Light[k].Filter
		}
	}
	if len(c.shared) == n {
		v += sharedVar(c.shared, groups)
	}
	return math.Sqrt(v) / float64(n)
}
