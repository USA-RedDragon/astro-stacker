package ownimage

import (
	"fmt"
	"math"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

const (
	ditherMin     = 3.0
	spreadShare   = 0.8
	consistent    = 0.75
	strongChange  = 3.0
	samePixel     = 1.5
	awayPixel     = 2.0
	darkHot       = 10.0
	trailShare    = 0.7
	trailMinSubs  = 4
	trailSigma    = 3.0
	trailLength   = 10.0
	trailElong    = 3.0
	trailCore     = 3.0
	psfMinRatio   = 0.6
	psfMaxRatio   = 1.6
	psfMaxElong   = 1.6
	psfFitR       = 5.0
	psfMinSNR     = 5.0
	psfFirmSNR    = 20.0
	psfFaintRatio = 0.45
	psfFaintElong = 2.2
	edgeNear      = 10.0
	satNearPx     = 10.0
	satAtShare    = 0.2
	satNearShare  = 0.5
	edgeShare     = 0.3
	satOrEdgeNone = "every frame"
)

// nightChange is one flagged night's subs against the reference they were
// compared with.
type nightChange struct {
	night         string
	subs          []int
	d, noise      []float64
	sx, sy        []float64
	sensed        []bool
	dir           float64
	spread        float64
	agree, strong int
}

func nightChanges(sp *spotPlan, e *ensemble, evals []refEval) []*nightChange {
	byNight := map[string]*nightChange{}
	var order []string
	for _, ev := range evals {
		nc := byNight[ev.set.night]
		if nc == nil {
			nc = &nightChange{night: ev.set.night, dir: -float64(sp.signOf(ev.set.night, ev.set.filter))}
			byNight[ev.set.night] = nc
			order = append(order, ev.set.night)
		}
		for _, i := range ev.set.nights {
			v, n, _, ok := level(sp, e, i)
			if !ok {
				continue
			}
			r := sp.recs[i]
			nc.subs = append(nc.subs, i)
			nc.d = append(nc.d, v-ev.refAll)
			nc.noise = append(nc.noise, n)
			nc.sx, nc.sy, nc.sensed = append(nc.sx, r.sx), append(nc.sy, r.sy), append(nc.sensed, r.sensOK)
		}
	}
	out := make([]*nightChange, 0, len(order))
	for _, k := range order {
		nc := byNight[k]
		if nc.dir == 0 {
			nc.dir = 1
			if median(nc.d) > 0 {
				nc.dir = -1
			}
		}
		for j, d := range nc.d {
			if -nc.dir*d > 0 {
				nc.agree++
			}
			if nc.isStrong(j) {
				nc.strong++
			}
		}
		nc.spread = sensorSpread(nc.sx, nc.sy, nc.sensed)
		out = append(out, nc)
	}
	return out
}

func (nc *nightChange) isStrong(j int) bool { return -nc.dir*nc.d[j] > strongChange*nc.noise[j] }

// sensorSpread is how far the spot moved on the sensor across the subs:
// twice the distance from the middle position that 80% of them stay
// within, so one sub with a poor footprint does not count.
func sensorSpread(xs, ys []float64, ok []bool) float64 {
	var px, py []float64
	for j := range xs {
		if ok[j] {
			px, py = append(px, xs[j]), append(py, ys[j])
		}
	}
	if len(px) < 2 {
		return math.NaN()
	}
	mx, my := median(px), median(py)
	d := make([]float64, len(px))
	for j := range px {
		d[j] = math.Hypot(px[j]-mx, py[j]-my)
	}
	sort.Float64s(d)
	return 2 * d[int(math.Ceil(spreadShare*float64(len(d))))-1]
}

// fixedPixel says whether the subs with a strong change all put the spot
// on one sensor pixel while the rest put it elsewhere.
func (nc *nightChange) fixedPixel() bool {
	var hx, hy []float64
	for j := range nc.d {
		if nc.sensed[j] && nc.isStrong(j) {
			hx, hy = append(hx, nc.sx[j]), append(hy, nc.sy[j])
		}
	}
	if len(hx) < 2 {
		return false
	}
	mx, my := median(hx), median(hy)
	for k := range hx {
		if math.Hypot(hx[k]-mx, hy[k]-my) > samePixel {
			return false
		}
	}
	for j := range nc.d {
		if nc.sensed[j] && !nc.isStrong(j) && math.Hypot(nc.sx[j]-mx, nc.sy[j]-my) <= awayPixel {
			return false
		}
	}
	return true
}

func hotPixel(ncs []*nightChange, darkZ float64, darkOK bool) check.Check {
	darkNote := ""
	hotDark := darkOK && darkZ >= darkHot
	switch {
	case hotDark:
		darkNote = fmt.Sprintf(" The master dark has a hot pixel under the spot, %.0f times its usual spread.", darkZ)
	case darkOK:
		darkNote = " The master dark is normal under the spot."
	}
	if len(ncs) == 0 {
		return check.Skipped(check.HotPixel, "Not measured yet. None of the night's frames could be measured at this spot."+darkNote)
	}
	minSpread := math.Inf(1)
	allGood := true
	for _, nc := range ncs {
		if math.IsNaN(nc.spread) {
			return check.Skipped(check.HotPixel, "Not measured yet. Where the frames sat on the sensor could not be worked out."+darkNote)
		}
		if nc.fixedPixel() {
			return check.New(check.HotPixel, check.Fail, fmt.Sprintf("On %s the change shows only in the %d frames that put the spot on the same sensor pixel.", nc.night, nc.strong)+darkNote)
		}
		minSpread = math.Min(minSpread, nc.spread)
		if nc.spread < ditherMin || float64(nc.agree) < consistent*float64(len(nc.d)) {
			allGood = false
		}
	}
	if allGood {
		lead := "The spot"
		if len(ncs) > 1 {
			lead = "On every flagged night the spot"
		}
		return check.New(check.HotPixel, check.Pass, fmt.Sprintf("%s moved %.0f px across the sensor with the dithers and the change moved with the sky.", lead, minSpread)+darkNote)
	}
	for _, nc := range ncs {
		if nc.spread < ditherMin && hotDark {
			return check.New(check.HotPixel, check.Fail, fmt.Sprintf("On %s the frames moved only %.1f px on the sensor, so the change could come from the sensor.", nc.night, nc.spread)+darkNote)
		}
		if nc.spread < ditherMin {
			return check.Skipped(check.HotPixel, fmt.Sprintf("Not measured yet. On %s the frames moved only %.1f px on the sensor, too little to tell a hot pixel from the sky.", nc.night, nc.spread)+darkNote)
		}
	}
	nc := ncs[0]
	for _, c := range ncs {
		if float64(c.agree) < consistent*float64(len(c.d)) {
			nc = c
			break
		}
	}
	return check.Skipped(check.HotPixel, fmt.Sprintf("Not measured yet. On %s the change shows in only %d of %d frames, too unevenly to tell.", nc.night, nc.agree, len(nc.d))+darkNote)
}

func trail(subs []Sub, sp *spotPlan, e *ensemble, ncs []*nightChange) check.Check {
	var up []*nightChange
	for _, nc := range ncs {
		if nc.dir < 0 {
			up = append(up, nc)
		}
	}
	if len(up) == 0 && sp.fadeSign < 0 {
		return check.New(check.Trail, check.Pass, "A trail only adds light, and this spot got fainter.")
	}
	ncs = up
	if len(ncs) == 0 {
		return check.Skipped(check.Trail, "Not measured yet. None of the night's frames could be measured at this spot.")
	}
	var judged *nightChange
	for _, nc := range ncs {
		if len(nc.d) < trailMinSubs {
			continue
		}
		judged = nc
		top, share := topShare(nc)
		if share < trailShare {
			continue
		}
		for _, j := range top {
			if l, ok := streak(subs, sp, e, nc, j); ok {
				return check.New(check.Trail, check.Fail, fmt.Sprintf("On %s the brightening comes from %d of %d frames, and a streak %.0f px long crosses the spot in one.", nc.night, len(top), len(nc.d), l))
			}
		}
		return check.New(check.Trail, check.Pass, fmt.Sprintf("On %s the brightening comes mostly from %d of %d frames, but no streak crosses the spot in them.", nc.night, len(top), len(nc.d)))
	}
	if judged == nil {
		return check.Skipped(check.Trail, "Not measured yet. Too few frames of the night were measured to tell.")
	}
	return check.New(check.Trail, check.Pass, fmt.Sprintf("The change shows in %d of %d frames, so it is not a passing satellite.", judged.agree, len(judged.d)))
}

// topShare is the one or two subs holding most of a night's brightening
// and their share of it.
func topShare(nc *nightChange) ([]int, float64) {
	var total float64
	best, second := -1, -1
	for j, d := range nc.d {
		if d <= 0 {
			continue
		}
		total += d
		switch {
		case best < 0 || d > nc.d[best]:
			best, second = j, best
		case second < 0 || d > nc.d[second]:
			second = j
		}
	}
	if best < 0 || !(total > 0) {
		return nil, 0
	}
	top := []int{best}
	sum := nc.d[best]
	if second >= 0 && sum/total < trailShare {
		top = append(top, second)
		sum += nc.d[second]
	}
	return top, sum / total
}

func scaledStamp(sp *spotPlan, e *ensemble, i int) []float64 {
	r := sp.recs[i]
	t := e.transp[i]
	if r == nil || r.stamp == nil || !(t > 0) {
		return nil
	}
	out := make([]float64, len(r.stamp))
	for k, v := range r.stamp {
		out[k] = float64(v) / t
	}
	return out
}

// streak looks in one sub, less the night's median, for a long thin trace
// of light through the spot and returns its length.
func streak(subs []Sub, sp *spotPlan, e *ensemble, nc *nightChange, j int) (float64, bool) {
	me := scaledStamp(sp, e, nc.subs[j])
	if me == nil {
		return 0, false
	}
	var all [][]float64
	for _, i := range nc.subs {
		if subs[i].Filter != subs[nc.subs[j]].Filter {
			continue
		}
		if s := scaledStamp(sp, e, i); s != nil {
			all = append(all, s)
		}
	}
	res := make([]float64, len(me))
	var vals []float64
	for k := range me {
		col := make([]float64, 0, len(all))
		for _, s := range all {
			if finite(s[k]) {
				col = append(col, s[k])
			}
		}
		res[k] = me[k] - median(col)
		if finite(res[k]) {
			vals = append(vals, res[k])
		}
	}
	noise := robustStd(vals)
	if !(noise > 0) {
		return 0, false
	}
	comp := component(res, stampSide, trailSigma*noise, trailCore)
	if len(comp) < 3 {
		return 0, false
	}
	var mx, my float64
	for _, k := range comp {
		mx += float64(k % stampSide)
		my += float64(k / stampSide)
	}
	mx /= float64(len(comp))
	my /= float64(len(comp))
	var xx, yy, xy float64
	for _, k := range comp {
		dx, dy := float64(k%stampSide)-mx, float64(k/stampSide)-my
		xx += dx * dx
		yy += dy * dy
		xy += dx * dy
	}
	n := float64(len(comp))
	xx, yy, xy = xx/n, yy/n, xy/n
	tr, det := xx+yy, xx*yy-xy*xy
	l1 := tr/2 + math.Sqrt(math.Max(tr*tr/4-det, 0))
	l2 := tr/2 - math.Sqrt(math.Max(tr*tr/4-det, 0))
	length := math.Sqrt(12 * l1)
	elong := math.Sqrt(l1 / math.Max(l2, 1e-3))
	return length, length >= trailLength && elong >= trailElong
}

// component is the 8-connected set of pixels above thr that touches the
// core of radius core at the stamp's centre.
func component(v []float64, n int, thr, core float64) []int {
	seen := make([]bool, len(v))
	var stack, out []int
	c := float64(n / 2)
	for k := range v {
		x, y := float64(k%n), float64(k/n)
		if math.Hypot(x-c, y-c) <= core && v[k] > thr {
			stack = append(stack, k)
			seen[k] = true
		}
	}
	for len(stack) > 0 {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, k)
		x, y := k%n, k/n
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				xx, yy := x+dx, y+dy
				if xx < 0 || yy < 0 || xx >= n || yy >= n {
					continue
				}
				q := yy*n + xx
				if !seen[q] && v[q] > thr {
					seen[q] = true
					stack = append(stack, q)
				}
			}
		}
	}
	return out
}

func edge(sp *spotPlan) check.Check {
	var n, satAt, satNear, nearEdge int
	var dists []float64
	for i := range sp.night {
		if r := sp.recs[i]; r != nil {
			n++
			if r.satAt {
				satAt++
			}
			if r.satNear <= satNearPx {
				satNear++
				dists = append(dists, r.satNear)
			}
			if r.edge < edgeNear {
				nearEdge++
			}
		}
	}
	if n == 0 {
		return check.Skipped(check.Edge, "Not measured yet. None of the night's frames could be measured at this spot.")
	}
	switch {
	case float64(satAt) >= satAtShare*float64(n):
		return check.New(check.Edge, check.Fail, fmt.Sprintf("Pixels at the spot are saturated in %d of %d frames.", satAt, n))
	case float64(satNear) >= satNearShare*float64(n):
		return check.New(check.Edge, check.Fail, fmt.Sprintf("A saturated star lies %.0f px from the spot in %d of %d frames.", median(dists), satNear, n))
	case float64(nearEdge) >= edgeShare*float64(n):
		return check.New(check.Edge, check.Fail, fmt.Sprintf("The spot is within %.0f px of the edge of the covered area in %d of %d frames.", edgeNear, nearEdge, n))
	}
	sat := fmt.Sprintf("No saturated pixels within %.0f px", satNearPx)
	if satNear > 0 || satAt > 0 {
		sat = fmt.Sprintf("Saturated pixels come within %.0f px in only %d of %d frames", satNearPx, max(satNear, satAt), n)
	}
	where := satOrEdgeNone
	if nearEdge > 0 {
		where = fmt.Sprintf("%d of %d frames", n-nearEdge, n)
	}
	return check.New(check.Edge, check.Pass, fmt.Sprintf("%s, and the spot is at least %.0f px inside the covered area in %s.", sat, edgeNear, where))
}
