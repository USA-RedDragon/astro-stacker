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
	LiveFaint     = "faint"
	LiveNoDip     = "no-dip"
	LiveDip       = "dip"
)

const (
	// liveMinSubs is the fewest measured subs before the card judges.
	liveMinSubs = 6
	// compRecent is how many of the latest subs judge whether the nearby
	// stars agree and the star can be read; compAgree is the least share
	// of the nearby stars that must agree in a sub, and faintErr the
	// largest error a sub may typically have.
	compRecent = 5
	compAgree  = 0.6
	faintErr   = 0.25
	// shiftSigma and depthSigma are how many of its own errors a timing
	// or depth must clear the expected one by to count as off.
	shiftSigma = 2.0
	depthSigma = 3.0
	// depthShare is the least depth difference, as a share of the
	// expected depth, that counts as off.
	depthShare = 0.2
	// fitSteps is how many trial timings the fit tries across the dip.
	fitSteps = 400
	// dipRun is the fewest subs in a row a dip watch averages, dipFloor
	// the least fade it calls a dip and dipSigma how far beyond the
	// scatter of that average it must fall. runSpan is the longest a run
	// of subs may last, and runSpanFloor the least.
	dipRun       = 4
	dipFloor     = 0.05
	dipSigma     = 4.0
	runSpan      = time.Hour
	runSpanFloor = 12 * time.Minute
	// errFloor is the least error given one sub.
	errFloor = 0.003
)

// LivePoint is one measured sub: the star in magnitudes fainter than its
// normal level against the nearby stars, so a dimming they share drops
// out, and the nearby stars together in magnitudes fainter than their
// typical level. Agree of the Present nearby stars agreed with each
// other, scattering by Spread magnitudes.
type LivePoint struct {
	FrameID int       `json:"frame_id"`
	Time    time.Time `json:"time"`
	Filter  string    `json:"filter"`
	Mag     float64   `json:"mag"`
	Err     float64   `json:"err"`
	Comp    float64   `json:"comp"`
	Spread  float64   `json:"spread"`
	Agree   int       `json:"agree"`
	Present int       `json:"present"`
}

// agrees says whether most of the sub's nearby stars agree; a sub that
// does not say is taken as agreeing.
func (p LivePoint) agrees() bool {
	return p.Present == 0 || float64(p.Agree) >= compAgree*float64(p.Present)
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

// compsCheck is how the nearby stars and the star behave: the scatter of
// the nearby stars' shared level, how far it sits from usual and how far
// the stars scatter about each other in the latest subs, and whether the
// reading is held back, with its state and why. A dimming the nearby
// stars share, haze or a low sky, drops out of the star's reading and
// holds nothing back; the reading is held back only when the nearby
// stars disagree with each other or the star is too faint to read.
type compsCheck struct {
	scatter float64
	shift   float64
	spread  float64
	held    bool
	state   string
	why     string
}

func checkComps(pts []LivePoint) compsCheck {
	all := make([]float64, 0, len(pts))
	for _, p := range pts {
		all = append(all, p.Comp)
	}
	c := compsCheck{scatter: spreadOf(all)}
	recent := pts[max(0, len(pts)-compRecent):]
	if len(recent) < 3 {
		return c
	}
	var shift, spread, share, errs []float64
	agree, present := 0, 0
	for _, p := range recent {
		shift = append(shift, p.Comp)
		errs = append(errs, p.Err)
		if p.Present > 0 {
			spread = append(spread, p.Spread)
			share = append(share, float64(p.Agree)/float64(p.Present))
			agree += p.Agree
			present += p.Present
		}
	}
	c.shift = medianOf(shift)
	if len(spread) > 0 {
		c.spread = medianOf(spread)
	}
	switch {
	case len(share) >= 3 && medianOf(share) < compAgree:
		n := float64(len(share))
		c.held, c.state = true, LiveComps
		c.why = fmt.Sprintf("only %.0f of the %.0f nearby stars agree with each other over the last %d subs", float64(agree)/n, float64(present)/n, len(recent))
	case medianOf(errs) > faintErr:
		c.held, c.state = true, LiveFaint
		c.why = fmt.Sprintf("the star is measured only to %.2f mag over the last %d subs", medianOf(errs), len(recent))
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
	case j.comps.held:
		j.state, j.why = j.comps.state, j.comps.why+", so the reading is held back"
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

// dipGoal is the dip a watch looks for: the shallowest of the past dips
// and the longest a run of subs may last, half the shortest past dip.
type dipGoal struct {
	depth float64
	span  time.Duration
}

// goalOf is the goal for past dips depth deep at the least and durHours
// long at the least, either 0 when not known.
func goalOf(depth, durHours float64) dipGoal {
	span := runSpan
	if durHours > 0 {
		span = min(span, max(runSpanFloor, time.Duration(durHours/2*float64(time.Hour))))
	}
	return dipGoal{depth: depth, span: span}
}

// dipFit is a dip watch's subs so far: their normal level and scatter,
// how many subs in a row one reading averages, the smallest dip that
// average would show, the latest average and any dip it shows.
type dipFit struct {
	state    string
	why      string
	zero     float64
	scatter  float64
	run      int
	smallest float64
	over     time.Duration
	latest   float64
	start    time.Time
	depth    float64
	found    bool
	comps    compsCheck
}

// judgeDip levels a dip watch's subs on their normal level, leaving out
// any dip, and looks for a run of subs in a row whose weighted mean fades
// beyond its noise while the nearby stars agree. The run is the fewest
// subs, at least dipRun, whose mean would show the goal's depth, and no
// longer than the goal's span.
func judgeDip(pts []LivePoint, goal dipGoal) dipFit {
	j := dipFit{comps: checkComps(pts), run: dipRun}
	if len(pts) == 0 {
		j.state, j.why = LiveEarly, "no sub is measured yet"
		return j
	}
	var gaps []float64
	for i := 1; i < len(pts); i++ {
		gaps = append(gaps, pts[i].Time.Sub(pts[i-1].Time).Seconds())
	}
	gap := time.Duration(0)
	if len(gaps) > 0 {
		gap = time.Duration(medianOf(gaps) * float64(time.Second))
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
		j.run = runFor(j.scatter, gap, goal)
		j.smallest = math.Max(dipFloor, dipSigma*j.scatter/math.Sqrt(float64(j.run)))
		in = cover(j.dipStarts(pts), j.run)
	}
	starts := j.dipStarts(pts)
	j.over = gap * time.Duration(j.run)
	if len(pts) >= j.run {
		j.latest = j.mean(pts[len(pts)-j.run:])
	}
	for i := 0; i+j.run <= len(pts); i++ {
		if !starts[i] {
			continue
		}
		if !j.found {
			j.found = true
			for _, p := range pts[i : i+j.run] {
				if p.Mag-j.zero >= j.smallest {
					j.start = p.Time
					break
				}
			}
		}
		j.depth = math.Max(j.depth, j.mean(pts[i:i+j.run]))
	}
	need := max(liveMinSubs, j.run)
	switch {
	case len(pts) < need:
		j.state, j.why = LiveEarly, fmt.Sprintf("%d of the %d subs one reading needs are measured", len(pts), need)
	case j.comps.held:
		j.state, j.why = j.comps.state, j.comps.why+", so the reading is held back"
	case j.found:
		j.state, j.why = LiveDip, fmt.Sprintf("the star faded %.2f mag from %s against nearby stars that agree", j.depth, j.start.UTC().Format(time.RFC3339))
	default:
		j.state, j.why = LiveNoDip, fmt.Sprintf("no %d subs in a row fade %.2f mag or more", j.run, j.smallest)
	}
	return j
}

// runFor is how many subs in a row, gap apart, a reading averages: the
// fewest, at least dipRun, whose mean at scatter would show the goal's
// depth, and no more than its span holds.
func runFor(scatter float64, gap time.Duration, goal dipGoal) int {
	most := dipRun
	if gap > 0 {
		most = max(dipRun, int(goal.span/gap))
	}
	n := dipRun
	for n < most && dipSigma*scatter/math.Sqrt(float64(n)) > math.Max(goal.depth, dipFloor) {
		n++
	}
	return n
}

// mean is the subs' weighted mean against the normal level.
func (j *dipFit) mean(pts []LivePoint) float64 {
	var sw, sy float64
	for _, p := range pts {
		w := 1 / sq(math.Max(p.Err, errFloor))
		sw += w
		sy += w * (p.Mag - j.zero)
	}
	return sy / sw
}

// dipStarts marks the first sub of every run whose weighted mean fades
// past the smallest dip with the nearby stars agreeing in each sub.
func (j *dipFit) dipStarts(pts []LivePoint) []bool {
	out := make([]bool, len(pts))
	for i := 0; i+j.run <= len(pts); i++ {
		run := pts[i : i+j.run]
		if slices.IndexFunc(run, func(p LivePoint) bool { return !p.agrees() }) < 0 && j.mean(run) >= j.smallest {
			out[i] = true
		}
	}
	return out
}

// cover marks every sub of the runs of n that starts begin.
func cover(starts []bool, n int) []bool {
	out := make([]bool, len(starts))
	for i, s := range starts {
		for k := i; s && k < min(len(out), i+n); k++ {
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
