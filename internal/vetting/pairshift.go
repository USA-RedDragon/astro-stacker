package vetting

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// Pair shifts: ZTF measures a star and a Gaia neighbour closer than its
// seeing as one object, whose position is their light-weighted centre.
// When the neighbour's share of the light changes, from seeing or the
// neighbour itself, the object brightens toward the neighbour; when the
// star itself changes, it brightens away from it. 299 (Barnard's E,
// G 15.50, 2.05" from a G 16.35 star) varied by 0.25 mag in ZTF r and
// moved 1.33 +- 0.14" toward the neighbour per magnitude brighter, where a
// changing neighbour share gives 1.30" toward it and the star changing
// 0.59" away.
const (
	// pairMinPoints is how many ZTF points with positions a band needs.
	pairMinPoints = 20
	// pairMinSrc is how many points one ZTF object needs: each object has
	// its own zero point and astrometry, so each is centred on its own.
	pairMinSrc = 3
	// pairMatch is how many sigma the measured shift may sit from the one
	// predicted, and pairRuleOut how many it must sit from the other.
	pairMatch   = 2.0
	pairRuleOut = 3.0
	// pairClip drops points whose position sits this many robust sigma
	// off the fit, once.
	pairClip = 5.0
	// magK turns a relative change in light into magnitudes, 2.5 / ln 10.
	magK = 1.0857362047581294
)

// pairShift is how ZTF's position of a star with one close Gaia
// neighbour moves with its magnitude, in arcsec per magnitude along the
// line toward the neighbour: negative when it moves toward the neighbour
// as it brightens.
type pairShift struct {
	n          surveys.Neighbour
	slope, err float64
	// share is the slope a changing share of the neighbour's light gives,
	// own the slope the star changing gives.
	share, own float64
	points     int
	bands      []string
}

// pairShiftOf fits the shift over the ZTF bands with enough points that
// carry a position, or false when the star has no single close neighbour
// or too few such points.
func pairShiftOf(t surveys.Target, ss []lc.Series) (pairShift, bool) {
	var p pairShift
	n, ok := surveys.Pair(t)
	if !ok {
		return p, false
	}
	cosd := math.Cos(t.Dec * math.Pi / 180)
	ax, ay := (n.RA-t.RA)*cosd*3600, (n.Dec-t.Dec)*3600
	sep := math.Hypot(ax, ay)
	if !(sep > 0) {
		return p, false
	}
	ux, uy := ax/sep, ay/sep
	f := 1 / (1 + math.Pow(10, -0.4*(t.G-n.G)))
	p.n, p.share, p.own = n, -sep*(1-f)/magK, sep*f/magK
	byBand := map[string][]lc.Point{}
	for _, s := range ss {
		if s.Survey != lc.ZTF || s.Relative {
			continue
		}
		for _, pt := range s.Points {
			if pt.RA != 0 || pt.Dec != 0 {
				byBand[s.Band] = append(byBand[s.Band], pt)
			}
		}
	}
	var wsum, wslope float64
	for _, b := range []string{"g", "r", "i"} {
		pts := byBand[b]
		if len(pts) < pairMinPoints {
			continue
		}
		xs := make([]float64, len(pts))
		for i, pt := range pts {
			xs[i] = (pt.RA-t.RA)*cosd*3600*ux + (pt.Dec-t.Dec)*3600*uy
		}
		slope, se, used := shiftFit(pts, xs)
		if used < pairMinPoints || !(se > 0) {
			continue
		}
		w := 1 / (se * se)
		wsum += w
		wslope += w * slope
		p.points += used
		p.bands = append(p.bands, b)
	}
	if wsum == 0 {
		return p, false
	}
	p.slope, p.err = wslope/wsum, 1/math.Sqrt(wsum)
	return p, true
}

// shiftFit fits position against magnitude by least squares, each ZTF
// object centred on its own means, with one pass dropping points far off
// the fit: the slope, its standard error and the points used.
func shiftFit(pts []lc.Point, xs []float64) (float64, float64, int) {
	keep := make([]bool, len(pts))
	count := map[int64]int{}
	for _, pt := range pts {
		count[pt.Src]++
	}
	for i, pt := range pts {
		keep[i] = count[pt.Src] >= pairMinSrc
	}
	var slope, se float64
	used := 0
	for pass := range 2 {
		sm, sx, cnt := map[int64]float64{}, map[int64]float64{}, map[int64]float64{}
		for i, pt := range pts {
			if keep[i] {
				sm[pt.Src] += pt.Mag
				sx[pt.Src] += xs[i]
				cnt[pt.Src]++
			}
		}
		dm, dx := make([]float64, len(pts)), make([]float64, len(pts))
		var smm, smx float64
		used = 0
		for i, pt := range pts {
			if !keep[i] {
				continue
			}
			dm[i], dx[i] = pt.Mag-sm[pt.Src]/cnt[pt.Src], xs[i]-sx[pt.Src]/cnt[pt.Src]
			smm += dm[i] * dm[i]
			smx += dm[i] * dx[i]
			used++
		}
		if used < 3 || smm == 0 {
			return 0, 0, used
		}
		slope = smx / smm
		res := make([]float64, 0, used)
		var ss float64
		for i := range pts {
			if keep[i] {
				r := dx[i] - slope*dm[i]
				res = append(res, math.Abs(r))
				ss += r * r
			}
		}
		dof := used - 1 - len(cnt)
		if dof < 1 {
			return 0, 0, used
		}
		se = math.Sqrt(ss / float64(dof) / smm)
		if pass == 1 {
			break
		}
		slices.Sort(res)
		limit := pairClip * 1.4826 * res[len(res)/2]
		for i := range pts {
			if keep[i] && math.Abs(dx[i]-slope*dm[i]) > limit {
				keep[i] = false
			}
		}
	}
	return slope, se, used
}

func (p pairShift) refutes() bool {
	return math.Abs(p.slope-p.share) <= pairMatch*p.err && math.Abs(p.slope-p.own) >= pairRuleOut*p.err
}

func (p pairShift) supports() bool {
	return math.Abs(p.slope-p.own) <= pairMatch*p.err && math.Abs(p.slope-p.share) >= pairRuleOut*p.err
}

// toward words a slope as the move per magnitude brighter.
func toward(slope, err float64) string {
	v := -slope
	pm := ""
	if err > 0 {
		pm = fmt.Sprintf(" ± %.2f", err)
	}
	if v >= 0 {
		return fmt.Sprintf("%.2f%s\" toward it", v, pm)
	}
	return fmt.Sprintf("%.2f%s\" away from it", -v, pm)
}

func (p pairShift) words() string {
	head := fmt.Sprintf("ZTF measures it as one object with the Gaia star of G %.2f %.1f\" away, and in %s (%d points) the object moves %s per magnitude brighter.",
		p.n.G, p.n.Sep, strings.Join(p.bands, " and "), p.points, toward(p.slope, p.err))
	pred := fmt.Sprintf("A changing share of that star's light moves it %s, and the star itself changing %s.", toward(p.share, 0), toward(p.own, 0))
	switch {
	case p.refutes():
		return "That change is not the star's own: " + head + " " + pred
	case p.supports():
		return head + " " + pred + " The move is the star's own."
	}
	return ""
}

// pairVaries weighs a ZTF row that says the star varies with no period
// against how ZTF's position of it moves with its brightness, for a star
// ZTF measures together with one close Gaia neighbour: the row turns to a
// fail when the move is a changing share of the neighbour's light and
// not the star changing, and says so when it is the star's own.
func pairVaries(pr *period.Result, t surveys.Target, ss []lc.Series) {
	if pr == nil || !pr.Irregular {
		return
	}
	i := slices.IndexFunc(pr.Checks, func(c check.Check) bool { return c.ID == check.ZTF && c.Status == check.Pass })
	if i < 0 {
		return
	}
	p, ok := pairShiftOf(t, ss)
	if !ok {
		return
	}
	c := pr.Checks[i]
	switch {
	case p.refutes():
		pr.Checks[i] = check.New(check.ZTF, check.Fail, joinSentences(c.Evidence, p.words()))
	case p.supports():
		pr.Checks[i].Evidence = joinSentences(c.Evidence, p.words())
	}
}
