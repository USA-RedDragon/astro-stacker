package binaries

import (
	"fmt"
	"math"
	"slices"
	"time"
)

// The live card's states.
const (
	LiveEarly     = "early"
	LiveFollowing = "following"
	LiveOff       = "off"
	LiveComps     = "comps"
	LiveNoDip     = "no-dip"
	LiveDip       = "dip"
)

const (
	// liveMinSubs is the fewest measured subs before the card judges.
	liveMinSubs = 6
	// compRecent is how many of the latest subs judge whether the nearby
	// stars are moving; compShift is how far their median may sit from
	// the typical level and compSpread how much they may scatter.
	compRecent = 5
	compShift  = 0.1
	compSpread = 0.05
	// shiftSigma and depthSigma are how many of its own errors a timing
	// or depth must clear the expected one by to count as off.
	shiftSigma = 2.0
	depthSigma = 3.0
	// depthShare is the least depth difference, as a share of the
	// expected depth, that counts as off.
	depthShare = 0.2
	// fitSteps is how many trial timings the fit tries across the dip.
	fitSteps = 400
	// dipRun is how many subs in a row a dip watch averages, dipFloor the
	// least fade it calls a dip and dipSigma how far beyond the single
	// sub scatter of that average it must fall.
	dipRun   = 4
	dipFloor = 0.05
	dipSigma = 4.0
	// errFloor is the least error given one sub.
	errFloor = 0.003
)

// LivePoint is one measured sub: the star in magnitudes fainter than its
// normal level and the nearby stars together in magnitudes fainter than
// their typical level.
type LivePoint struct {
	FrameID int       `json:"frame_id"`
	Time    time.Time `json:"time"`
	Filter  string    `json:"filter"`
	Mag     float64   `json:"mag"`
	Err     float64   `json:"err"`
	Comp    float64   `json:"comp"`
}

// eclipseModel is the expected eclipse: its middle and timing error, half
// its length and its depth in the sub's band, and its shape, 1 at the
// middle and 0 outside, by time from the middle.
type eclipseModel struct {
	mid   time.Time
	err   time.Duration
	half  time.Duration
	depth float64
	shape func(dt time.Duration) float64
}

func (m eclipseModel) at(t time.Time, shift time.Duration) float64 {
	return m.shape(t.Sub(m.mid) - shift)
}

// compsCheck is how the nearby stars behave: their typical scatter, and
// whether the latest subs show them moving.
type compsCheck struct {
	scatter float64
	moving  bool
	why     string
}

func checkComps(pts []LivePoint) compsCheck {
	var all []float64
	for _, p := range pts {
		all = append(all, p.Comp)
	}
	c := compsCheck{scatter: spreadOf(all)}
	recent := all[max(0, len(all)-compRecent):]
	if len(recent) < 3 {
		return c
	}
	if m := medianOf(recent); math.Abs(m) > compShift {
		dir := "fainter"
		if m < 0 {
			dir = "brighter"
		}
		c.moving, c.why = true, fmt.Sprintf("the nearby stars read %.2f mag %s than usual over the last %d subs", math.Abs(m), dir, len(recent))
	} else if s := spreadOf(recent); s > compSpread {
		c.moving, c.why = true, fmt.Sprintf("the nearby stars jump by %.2f mag from sub to sub", s)
	}
	return c
}

// eclipseFit is the eclipse as the measured subs show it so far.
type eclipseFit struct {
	state    string
	why      string
	zero     float64
	shift    time.Duration
	shiftErr time.Duration
	depth    float64
	depthErr float64
	scatter  float64
	ok       bool
	comps    compsCheck
}

// judgeEclipse levels the subs, fits the eclipse's timing and depth and
// says how the subs sit against the expected curve. pts hold one filter,
// in magnitudes on an arbitrary zero.
func judgeEclipse(pts []LivePoint, m eclipseModel) eclipseFit {
	j := eclipseFit{comps: checkComps(pts)}
	j.fit(pts, m)
	near := 0
	core := max(m.half/2, m.err)
	for _, p := range pts {
		if absDur(p.Time.Sub(m.mid)) <= core {
			near++
		}
	}
	switch {
	case len(pts) < liveMinSubs:
		j.state, j.why = LiveEarly, fmt.Sprintf("%d of the %d subs needed are measured", len(pts), liveMinSubs)
	case near == 0:
		j.state, j.why = LiveEarly, "no sub is near the expected middle yet"
	case j.comps.moving:
		j.state, j.why = LiveComps, j.comps.why+", so the reading is held back"
	case !j.ok:
		j.state, j.why = LiveEarly, "the subs do not pin the dip down yet"
	default:
		j.state = LiveFollowing
		off := []string{}
		band := m.err.Seconds()
		if s := math.Abs(j.shift.Seconds()); s-shiftSigma*j.shiftErr.Seconds() > band {
			word := "late"
			if j.shift < 0 {
				word = "early"
			}
			off = append(off, fmt.Sprintf("the dip comes %.0f min %s, give or take %.0f, and the timing error allows %.0f", s/60, word, j.shiftErr.Minutes(), band/60))
		}
		if d := j.depth - m.depth; math.Abs(d) > depthSigma*j.depthErr && math.Abs(d) > depthShare*m.depth {
			word := "deeper"
			if d < 0 {
				word = "shallower"
			}
			off = append(off, fmt.Sprintf("the dip is %.2f mag %s than the expected %.2f", math.Abs(d), word, m.depth))
		}
		if len(off) > 0 {
			j.state, j.why = LiveOff, joinAnd(off)
		} else {
			j.why = fmt.Sprintf("the subs sit within the timing error of %.0f min", band/60)
		}
	}
	return j
}

// fit finds the timing whose curve, scaled in depth and set on a level,
// best matches the subs, and the errors of timing and depth. Once the subs
// pass the expected middle, the timings tried reach as far as the subs do,
// so a dip far from its expected time is still found.
func (j *eclipseFit) fit(pts []LivePoint, m eclipseModel) {
	if len(pts) < 3 || m.half <= 0 {
		return
	}
	reach := m.half + 3*m.err
	if last := pts[len(pts)-1].Time; last.After(m.mid.Add(m.err)) {
		for _, p := range pts {
			reach = max(reach, absDur(p.Time.Sub(m.mid)))
		}
	}
	step := 2 * reach / fitSteps
	type trial struct {
		s       time.Duration
		chi     float64
		z, d    float64
		dVar    float64
		healthy bool
	}
	var tr []trial
	for k := 0; k <= fitSteps; k++ {
		s := -reach + time.Duration(k)*step
		z, d, dv, chi, ok := levelFit(pts, func(t time.Time) float64 { return m.at(t, s) })
		tr = append(tr, trial{s, chi, z, d, dv, ok})
	}
	best := -1
	for i, t := range tr {
		if t.healthy && (best < 0 || t.chi < tr[best].chi) {
			best = i
		}
	}
	if best < 0 {
		return
	}
	b := tr[best]
	red := math.Max(1, b.chi/float64(max(1, len(pts)-3)))
	lo, hi := b.s, b.s
	for _, t := range tr {
		if t.healthy && t.chi-b.chi <= red {
			lo, hi = min(lo, t.s), max(hi, t.s)
		}
	}
	j.zero, j.shift, j.depth = b.z, b.s, b.d
	j.shiftErr = max((hi-lo)/2, step)
	j.depthErr = math.Sqrt(b.dVar * red)
	var res []float64
	for _, p := range pts {
		res = append(res, p.Mag-b.z-b.d*m.at(p.Time, b.s))
	}
	j.scatter = spreadOf(res)
	j.ok = b.d > 0 && lo > -reach && hi < reach
}

// levelFit is the weighted least squares level z and depth d of y = z +
// d·f(t), the variance of d, and the chi-square.
func levelFit(pts []LivePoint, f func(time.Time) float64) (z, d, dVar, chi float64, ok bool) {
	var sw, sx, sy, sxx, sxy float64
	for _, p := range pts {
		w := 1 / sq(math.Max(p.Err, errFloor))
		x := f(p.Time)
		sw += w
		sx += w * x
		sy += w * p.Mag
		sxx += w * x * x
		sxy += w * x * p.Mag
	}
	det := sw*sxx - sx*sx
	if !(det > 1e-12*sw*sxx) {
		return 0, 0, 0, 0, false
	}
	d = (sw*sxy - sx*sy) / det
	z = (sy - d*sx) / sw
	dVar = sw / det
	for _, p := range pts {
		chi += sq((p.Mag - z - d*f(p.Time)) / math.Max(p.Err, errFloor))
	}
	return z, d, dVar, chi, true
}

// dipFit is a dip watch's subs so far: their normal level and scatter,
// the smallest dip they would show, and any dip they show.
type dipFit struct {
	state    string
	why      string
	zero     float64
	scatter  float64
	smallest float64
	over     time.Duration
	start    time.Time
	depth    float64
	found    bool
	comps    compsCheck
}

// judgeDip levels a dip watch's subs on their normal level, leaving out
// any dip, and looks for dipRun subs in a row that fade beyond the noise
// while the nearby stars stay steady.
func judgeDip(pts []LivePoint) dipFit {
	j := dipFit{comps: checkComps(pts)}
	if len(pts) == 0 {
		j.state, j.why = LiveEarly, "no sub is measured yet"
		return j
	}
	in := make([]bool, len(pts))
	for range 3 {
		var ys []float64
		for i, p := range pts {
			if !in[i] {
				ys = append(ys, p.Mag)
			}
		}
		if len(ys) < 3 {
			break
		}
		j.zero = medianOf(ys)
		var res []float64
		for i, p := range pts {
			if !in[i] {
				res = append(res, p.Mag-j.zero)
			}
		}
		j.scatter = spreadOf(res)
		j.smallest = math.Max(dipFloor, dipSigma*j.scatter/math.Sqrt(dipRun))
		in = cover(j.dipStarts(pts))
	}
	starts := j.dipStarts(pts)
	var gaps []float64
	for i := 1; i < len(pts); i++ {
		gaps = append(gaps, pts[i].Time.Sub(pts[i-1].Time).Seconds())
	}
	if len(gaps) > 0 {
		j.over = time.Duration(medianOf(gaps)*dipRun) * time.Second
	}
	for i := 0; i+dipRun <= len(pts); i++ {
		if !starts[i] {
			continue
		}
		if !j.found {
			j.found = true
			for _, p := range pts[i : i+dipRun] {
				if p.Mag-j.zero >= j.smallest {
					j.start = p.Time
					break
				}
			}
		}
		var sum float64
		for _, p := range pts[i : i+dipRun] {
			sum += p.Mag - j.zero
		}
		j.depth = math.Max(j.depth, sum/dipRun)
	}
	switch {
	case len(pts) < liveMinSubs:
		j.state, j.why = LiveEarly, fmt.Sprintf("%d of the %d subs needed are measured", len(pts), liveMinSubs)
	case j.comps.moving:
		j.state, j.why = LiveComps, j.comps.why+", so the reading is held back"
	case j.found:
		j.state, j.why = LiveDip, fmt.Sprintf("the star faded %.2f mag from %s while the nearby stars stayed steady", j.depth, j.start.UTC().Format(time.RFC3339))
	default:
		j.state, j.why = LiveNoDip, fmt.Sprintf("no %d subs in a row fade %.2f mag or more", dipRun, j.smallest)
	}
	return j
}

// dipStarts marks the first sub of every dipRun in a row whose mean fades
// past the smallest dip with the nearby stars steady in each.
func (j *dipFit) dipStarts(pts []LivePoint) []bool {
	out := make([]bool, len(pts))
	for i := 0; i+dipRun <= len(pts); i++ {
		var sum float64
		steady := true
		for _, p := range pts[i : i+dipRun] {
			sum += p.Mag - j.zero
			steady = steady && math.Abs(p.Comp) <= compShift
		}
		if steady && sum/dipRun >= j.smallest {
			out[i] = true
		}
	}
	return out
}

// cover marks every sub of the runs that starts begin.
func cover(starts []bool) []bool {
	out := make([]bool, len(starts))
	for i, s := range starts {
		for k := i; s && k < min(len(out), i+dipRun); k++ {
			out[k] = true
		}
	}
	return out
}

func sq(v float64) float64 { return v * v }

func medianOf(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	c := slices.Clone(v)
	slices.Sort(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

// spreadOf is the scatter of v from the median absolute deviation, as a
// normal sigma.
func spreadOf(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	m := medianOf(v)
	d := make([]float64, len(v))
	for i, x := range v {
		d[i] = math.Abs(x - m)
	}
	return 1.4826 * medianOf(d)
}
