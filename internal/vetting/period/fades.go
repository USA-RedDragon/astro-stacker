package period

import (
	"fmt"
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// Fade tests of a flat ZTF light curve whose points reach the reported
// change on several nights. 15523 (Cygnus Loop Panel 1, a dipper) had
// 1,302 ZTF r points steady at 0.03 mag scatter and fades of 0.11 to 0.55
// mag on 10 nights, each caught by two or more frames or two bands, while
// 132 stars within 3 arcmin stayed flat in the same exposures.
const (
	// fadeReach is the share of the reported change a point must reach.
	fadeReach = 0.6
	// fadeBacked is how close in days two fading points must be for the
	// night to count as backed.
	fadeBacked = 0.5
	// fadeNights is how many backed nights must show it.
	fadeNights = 2
	// fadeSameExp is how close in days a comparison point must be to
	// come from the same exposure.
	fadeSameExp = 0.0002
	// fadeMinComps is how many comparison stars an exposure needs.
	fadeMinComps = 10
	// fadeSteady is how far in magnitudes the comparisons' median may sit
	// from normal for the exposure to count as clean.
	fadeSteady = 0.05
	// fadeRank is the percentile of the comparisons the star must fade
	// beyond.
	fadeRank = 95
	// fadeAlso is how many comparison stars may reach the change in the
	// same exposure: 2 and 6 of the stars around 15523 brighten together
	// on 8 nights, while the rest stay steady.
	fadeAlso = 0
)

// Fades is how a flat star's ZTF fades compare with nearby stars in the
// same exposures: Nights is how many nights two or more fading points
// back up, Steady how many of them the star fades beyond nearby stars
// that stay at their normal level, Field how many the nearby stars fade
// too, and Stars how many nearby stars were read.
type Fades struct {
	Nights int `json:"nights"`
	Steady int `json:"steady"`
	Field  int `json:"field"`
	Stars  int `json:"stars"`
}

// Confirms says the star fades on its own on enough nights and the
// nearby stars fade with it on none.
func (f *Fades) Confirms() bool {
	return f != nil && f.Steady >= fadeNights && f.Field == 0
}

// FieldProblem says nearby stars fade with it.
func (f *Fades) FieldProblem() bool {
	return f != nil && f.Field > 0 && f.Field >= f.Steady
}

type fadePt struct {
	t, y float64
	band string
}

// backedFades are the star's ZTF points that reach fadeReach of the
// change c, measured from the median of their own light curve
// (lc.Point.Src) as the comparison stars' are, in runs of points within
// fadeBacked days of each other, and how many of those runs hold two or
// more points.
func (a *analysis) backedFades(c float64) ([][]fadePt, int) {
	var pts []fadePt
	for _, m := range compOffsets([][]lc.Series{a.ztf}, math.Copysign(1, c)) {
		for band, ps := range m {
			for _, p := range ps {
				if p.d >= fadeReach*math.Abs(c) {
					pts = append(pts, fadePt{t: p.t, y: p.d, band: band})
				}
			}
		}
	}
	slices.SortFunc(pts, func(x, y fadePt) int {
		switch {
		case x.t < y.t:
			return -1
		case x.t > y.t:
			return 1
		}
		return 0
	})
	var runs [][]fadePt
	backed := 0
	for i, p := range pts {
		if i == 0 || p.t-pts[i-1].t > fadeBacked {
			runs = append(runs, nil)
		}
		runs[len(runs)-1] = append(runs[len(runs)-1], p)
	}
	for _, r := range runs {
		if len(r) >= 2 {
			backed++
		}
	}
	return runs, backed
}

type compPt struct{ t, d float64 }

// compOffsets are each comparison star's points per band, as magnitudes
// fainter than the median of their own light curve (lc.Point.Src),
// sorted in time.
func compOffsets(comps [][]lc.Series, sign float64) []map[string][]compPt {
	out := make([]map[string][]compPt, 0, len(comps))
	for _, ss := range comps {
		m := map[string][]compPt{}
		for _, s := range ss {
			if s.Survey != lc.ZTF {
				continue
			}
			bySrc := map[int64][]lc.Point{}
			for _, p := range cleanPoints(s.Points) {
				bySrc[p.Src] = append(bySrc[p.Src], p)
			}
			for _, ps := range bySrc {
				if len(ps) < 3 {
					continue
				}
				mags := make([]float64, len(ps))
				for i, p := range ps {
					mags[i] = p.Mag
				}
				med := median(mags)
				for _, p := range ps {
					m[s.Band] = append(m[s.Band], compPt{t: p.BJD, d: sign * (p.Mag - med)})
				}
			}
		}
		for b := range m {
			slices.SortFunc(m[b], func(x, y compPt) int {
				switch {
				case x.t < y.t:
					return -1
				case x.t > y.t:
					return 1
				}
				return 0
			})
		}
		if len(m) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// sameExposure is each comparison star's offset in the exposure at t.
func sameExposure(comps []map[string][]compPt, band string, t float64) []float64 {
	var ds []float64
	for _, m := range comps {
		ps := m[band]
		i, _ := slices.BinarySearchFunc(ps, t-fadeSameExp, func(p compPt, v float64) int {
			switch {
			case p.t < v:
				return -1
			case p.t > v:
				return 1
			}
			return 0
		})
		best, bestDt := math.NaN(), math.Inf(1)
		for ; i < len(ps) && ps[i].t <= t+fadeSameExp; i++ {
			if dt := math.Abs(ps[i].t - t); dt < bestDt {
				best, bestDt = ps[i].d, dt
			}
		}
		if !math.IsNaN(best) {
			ds = append(ds, best)
		}
	}
	return ds
}

// testFades tests the backed runs of fading points against nearby stars in
// the same exposures. A night is steady when two of its fading points
// each sit beyond the fadeRank percentile of the nearby stars while their
// median stays within fadeSteady of normal, and a field night when their
// median fades by more than fadeSteady.
func (a *analysis) testFades(runs [][]fadePt, backed int, c float64) *Fades {
	f := &Fades{Nights: backed}
	if a.opts.Neighbours == nil {
		return f
	}
	comps := compOffsets(a.opts.Neighbours(), math.Copysign(1, c))
	f.Stars = len(comps)
	if len(comps) < fadeMinComps {
		return f
	}
	for _, r := range runs {
		if len(r) < 2 {
			continue
		}
		steady, field := 0, false
		for _, p := range r {
			ds := sameExposure(comps, p.band, p.t)
			if len(ds) < fadeMinComps {
				continue
			}
			med := median(ds)
			switch {
			case med > fadeSteady:
				field = true
			case math.Abs(med) <= fadeSteady && p.y > percentile(ds, fadeRank) && reaching(ds, fadeReach*math.Abs(c)) <= fadeAlso:
				steady++
			}
		}
		switch {
		case field:
			f.Field++
		case steady >= 2:
			f.Steady++
		}
	}
	return f
}

// fadeRow is the survey row of a flat light curve whose fading points
// back each other up on two or more nights, from what the nearby stars
// did in the same exposures.
func (a *analysis) fadeRow(id, label string, si surveyInfo, level, scatter float64, reached string) check.Check {
	f := a.fades
	base := fmt.Sprintf("%s %s, normally at %s with %s mag scatter; %s", label, si.span, mag2(level), mag2(scatter), reached)
	stars := plural(f.Stars, "nearby star", "nearby stars")
	switch {
	case f.Confirms():
		return check.New(id, check.Pass, fmt.Sprintf("%s on %s while %s stay steady: %s", fadeWord(a.opts.ChangeMag), plural(f.Steady, "night", "nights"), stars, base))
	case f.FieldProblem():
		return check.New(id, check.Fail, fmt.Sprintf("Nearby stars %s with it: on %d of %s the %s %s in the same exposures, so the sky or the camera made it, not the star. %s",
			fadeVerb(a.opts.ChangeMag), f.Field, plural(f.Nights, "night", "nights"), stars, fadeVerb(a.opts.ChangeMag), base))
	case f.Stars < fadeMinComps:
		return check.New(id, check.Pass, fmt.Sprintf("Seen on %s, but too few nearby stars to rule out the sky or the camera: %s",
			plural(f.Nights, "night", "nights"), base))
	}
	return check.New(id, check.Pass, fmt.Sprintf("Seen on %s, but %s cannot tell it from the sky or the camera: %s",
		plural(f.Nights, "night", "nights"), stars, base))
}

func fadeVerb(c float64) string {
	if c > 0 {
		return "fade"
	}
	return "brighten"
}

func fadeWord(c float64) string {
	if c > 0 {
		return "Fades"
	}
	return "Brightens"
}

func reaching(ds []float64, v float64) int {
	n := 0
	for _, d := range ds {
		if d >= v {
			n++
		}
	}
	return n
}
