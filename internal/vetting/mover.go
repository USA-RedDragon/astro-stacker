package vetting

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

// track is a moving object's detections on its night, from the stored
// track of its best candidate.
type track struct {
	cand   app.DiscoveryCandidate
	rec    discoveries.TrackRecord
	points []discoveries.TrackPoint
	// rate in arcsec per minute and pa in degrees east of north, from a
	// straight line fitted to the positions.
	rate, pa float64
	fit      skyLine
}

// skyLine is a straight, steady path on the sky about (ra0, dec0): xi
// and eta in arcsec, east and north, at t0 plus vxi and veta arcsec per
// minute.
type skyLine struct {
	ra0, dec0  float64
	t0         time.Time
	xi, eta    float64
	vxi, veta  float64
	rms, chi   float64
	curvedChi  float64
	n          int
	typicalErr float64
}

func (l skyLine) at(t time.Time) (ra, dec float64) {
	m := t.Sub(l.t0).Minutes()
	xi, eta := l.xi+l.vxi*m, l.eta+l.veta*m
	dec = l.dec0 + eta/3600
	ra = l.ra0 + xi/3600/math.Cos(l.dec0*math.Pi/180)
	return math.Mod(ra+360, 360), dec
}

func wrap180(d float64) float64 {
	d = math.Mod(d+540, 360) - 180
	return d
}

func tangent(ra0, dec0, ra, dec float64) (xi, eta float64) {
	return wrap180(ra-ra0) * math.Cos(dec0*math.Pi/180) * 3600, (dec - dec0) * 3600
}

// trackOf picks the source's candidate with the most detections and
// reads its stored track.
func trackOf(members []app.DiscoveryCandidate) (track, bool) {
	var best track
	found := false
	for _, c := range members {
		if c.Track == "" {
			continue
		}
		var rec discoveries.TrackRecord
		if err := json.Unmarshal([]byte(c.Track), &rec); err != nil || len(rec.Points) < 2 {
			continue
		}
		if !found || len(rec.Points) > len(best.points) || (len(rec.Points) == len(best.points) && c.RunID > best.cand.RunID) {
			best, found = track{cand: c, rec: rec, points: rec.Points}, true
		}
	}
	if !found {
		return best, false
	}
	sort.SliceStable(best.points, func(i, j int) bool { return best.points[i].Time.Before(best.points[j].Time) })
	errs := make([]float64, len(best.points))
	for i, p := range best.points {
		errs[i] = 1
		for _, a := range best.rec.ADES {
			if a.FrameID == p.FrameID && a.RMSRA > 0 {
				errs[i] = a.RMSRA
				break
			}
		}
	}
	best.fit = fitSky(best.points, errs)
	best.rate = math.Hypot(best.fit.vxi, best.fit.veta)
	best.pa = math.Mod(math.Atan2(best.fit.vxi, best.fit.veta)*180/math.Pi+360, 360)
	return best, true
}

// fitSky fits a straight, steady path to the positions, and a curved one
// to compare, each weighted by its position error in arcsec.
func fitSky(pts []discoveries.TrackPoint, errs []float64) skyLine {
	mid := pts[len(pts)/2]
	l := skyLine{ra0: mid.RA, dec0: mid.Dec, t0: mid.Time, n: len(pts)}
	ts := make([]float64, len(pts))
	xs := make([]float64, len(pts))
	ys := make([]float64, len(pts))
	ws := make([]float64, len(pts))
	for i, p := range pts {
		ts[i] = p.Time.Sub(l.t0).Minutes()
		xs[i], ys[i] = tangent(l.ra0, l.dec0, p.RA, p.Dec)
		ws[i] = 1 / (errs[i] * errs[i])
	}
	l.typicalErr = median(errs)
	cx := polyFit(ts, xs, ws, 1)
	cy := polyFit(ts, ys, ws, 1)
	l.xi, l.vxi, l.eta, l.veta = cx[0], cx[1], cy[0], cy[1]
	var chi2, ss float64
	for i, t := range ts {
		dx, dy := xs[i]-cx[0]-cx[1]*t, ys[i]-cy[0]-cy[1]*t
		ss += dx*dx + dy*dy
		chi2 += (dx*dx + dy*dy) * ws[i]
	}
	l.rms = math.Sqrt(ss / float64(len(ts)))
	if dof := 2*len(ts) - 4; dof > 0 {
		l.chi = math.Sqrt(chi2 / float64(dof))
	}
	if len(ts) >= 4 {
		qx, qy := polyFit(ts, xs, ws, 2), polyFit(ts, ys, ws, 2)
		var q2 float64
		for i, t := range ts {
			dx := xs[i] - qx[0] - qx[1]*t - qx[2]*t*t
			dy := ys[i] - qy[0] - qy[1]*t - qy[2]*t*t
			q2 += (dx*dx + dy*dy) * ws[i]
		}
		if dof := 2*len(ts) - 6; dof > 0 {
			l.curvedChi = math.Sqrt(q2 / float64(dof))
		}
	}
	return l
}

// polyFit is the weighted least squares polynomial of degree deg.
func polyFit(ts, vs, ws []float64, deg int) []float64 {
	n := deg + 1
	a := make([][]float64, n)
	for i := range a {
		a[i] = make([]float64, n+1)
	}
	for k, t := range ts {
		pw := make([]float64, 2*n)
		pw[0] = 1
		for j := 1; j < 2*n; j++ {
			pw[j] = pw[j-1] * t
		}
		for i := range n {
			for j := range n {
				a[i][j] += ws[k] * pw[i+j]
			}
			a[i][n] += ws[k] * pw[i] * vs[k]
		}
	}
	for c := range n {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		a[c], a[p] = a[p], a[c]
		if math.Abs(a[c][c]) < 1e-300 {
			return make([]float64, n)
		}
		for r := range n {
			if r == c {
				continue
			}
			f := a[r][c] / a[c][c]
			for k := c; k <= n; k++ {
				a[r][k] -= f * a[c][k]
			}
		}
	}
	out := make([]float64, n)
	for i := range n {
		out[i] = a[i][n] / a[i][i]
	}
	return out
}

func compass(pa float64) string {
	names := []string{"north", "north-northeast", "northeast", "east-northeast", "east", "east-southeast", "southeast", "south-southeast",
		"south", "south-southwest", "southwest", "west-southwest", "west", "west-northwest", "northwest", "north-northwest"}
	return names[int(math.Mod(pa+11.25+360, 360)/22.5)%16]
}

func motionWords(rate, pa float64) string {
	return fmt.Sprintf("%.3f arcsec a minute toward %s (%.0f degrees)", rate, compass(pa), pa)
}

const (
	lineChi     = 3.0
	curvedGain  = 2.0
	minLinePts  = 3
	mpcAdvice   = "report its positions to the Minor Planet Center"
	notMeasured = "Not measured yet."
	notSeen     = "Not seen."
)

func lineCheck(tr track) check.Check {
	l := tr.fit
	if l.n < minLinePts {
		return check.Skipped(check.MoverLine, fmt.Sprintf("Not measured. Only %d positions, too few to test a straight path.", l.n))
	}
	span := tr.points[len(tr.points)-1].Time.Sub(tr.points[0].Time).Minutes()
	lead := fmt.Sprintf("Its %d positions over %.0f minutes fit a straight path at a steady %s", l.n, span, motionWords(tr.rate, tr.pa))
	if l.chi <= lineChi {
		return check.New(check.MoverLine, check.Pass, fmt.Sprintf("%s. They stray from it by %.2f arcsec on average, within their measuring error of about %.2f arcsec.", lead, l.rms, l.typicalErr))
	}
	ev := fmt.Sprintf("Its %d positions stray from a straight, steady path by %.2f arcsec on average, %.1f times their measuring error of about %.2f arcsec.", l.n, l.rms, l.chi, l.typicalErr)
	if l.curvedChi > 0 && l.curvedChi <= lineChi && l.chi/l.curvedChi >= curvedGain {
		ev += " A curved or speeding-up path fits them; a very close asteroid can move that way, a fixed spot cannot."
	}
	return check.New(check.MoverLine, check.Fail, ev)
}

// knownBody is a known asteroid or comet compared with the track.
type knownBody struct {
	body   discoveries.Body
	offset float64
	allow  float64
	rate   float64
	pa     float64
	same   bool
	match  bool
}

// uncertaintyArcsec is how far a known orbit may put its object, from
// the Minor Planet Center's uncertainty parameter: the position's
// drift per decade it stands for, at least 10 arcsec.
func uncertaintyArcsec(b discoveries.Body) (float64, string) {
	if b.U != nil {
		u := *b.U
		runoff := []float64{1, 4.4, 19.6, 84, 366, 1620, 7200, 32000, 142000, 600000}
		if u < 0 {
			u = 0
		}
		if u > 9 {
			u = 9
		}
		return math.Max(10, runoff[u]), fmt.Sprintf("uncertainty %d on a scale of 0 to 9", u)
	}
	if b.ErrArc > 0 {
		return math.Max(10, 3*b.ErrArc), fmt.Sprintf("predicted position good to %.0f arcsec", b.ErrArc)
	}
	if b.Kind == kindComet {
		return 60, "comet orbits are less certain"
	}
	return 30, "no uncertainty given"
}

func bodyRate(b discoveries.Body) (rate, pa float64) {
	return math.Hypot(b.DRA, b.DDec) / 60, math.Mod(math.Atan2(b.DRA, b.DDec)*180/math.Pi+360, 360)
}

func sameMotion(r1, pa1, r2, pa2 float64) bool {
	dpa := math.Abs(wrap180(pa1 - pa2))
	return math.Abs(r1-r2) <= math.Max(0.03, 0.15*r1) && dpa <= 12
}

func bodyLabel(b discoveries.Body) string {
	name := strings.TrimSpace(b.Name)
	if b.Number != "" {
		name = fmt.Sprintf("(%s) %s", b.Number, name)
	}
	return name
}

// epochBodies are the known bodies the service listed at one of the
// track's times.
type epochBodies struct {
	at     time.Time
	ra     float64
	dec    float64
	bodies []discoveries.Body
}

// compareKnown matches the track against the known bodies listed at its
// first, middle and last times: a body must sit within its orbit's
// uncertainty at every time, and beyond 10 arcsec move the same way.
func compareKnown(tr track, lists []epochBodies) []knownBody {
	byKey := map[string]*knownBody{}
	var order []string
	for _, l := range lists {
		for _, b := range l.bodies {
			if b.Kind == kindPlanet {
				continue
			}
			k := bodyLabel(b)
			off := arcsec(l.ra, l.dec, b.RA, b.Dec)
			kb, ok := byKey[k]
			if !ok {
				r, pa := bodyRate(b)
				allow, _ := uncertaintyArcsec(b)
				allow = math.Min(allow, knownRadius*3600)
				kb = &knownBody{body: b, offset: off, allow: allow, rate: r, pa: pa}
				byKey[k] = kb
				order = append(order, k)
				continue
			}
			kb.offset = math.Max(kb.offset, off)
		}
	}
	out := make([]knownBody, 0, len(order))
	for _, k := range order {
		kb := *byKey[k]
		kb.same = sameMotion(tr.rate, tr.pa, kb.rate, kb.pa)
		kb.match = kb.offset <= kb.allow && (kb.offset <= 10 || kb.same)
		if kb.match && kb.offset > 10 && kb.body.V > 0 && tr.cand.NightMag != nil && kb.body.V-*tr.cand.NightMag > 3 {
			kb.match = false
		}
		out = append(out, kb)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].match != out[j].match {
			return out[i].match
		}
		return out[i].offset/out[i].allow < out[j].offset/out[j].allow
	})
	return out
}

func knownCheck(tr track, lists []epochBodies, radius float64, err error) (check.Check, *knownBody) {
	if err != nil {
		return check.Skipped(check.MoverKnown, "Not run: the known asteroid list could not be read."), nil
	}
	if len(lists) == 0 {
		return check.Skipped(check.MoverKnown, "Not run: no known asteroid list was available."), nil
	}
	ks := compareKnown(tr, lists)
	if len(ks) == 0 {
		return check.New(check.MoverKnown, check.Pass, fmt.Sprintf("No known asteroid or comet within %.0f arcmin at any of %d times along the track.", radius*60, len(lists))), nil
	}
	b := ks[0]
	_, why := uncertaintyArcsec(b.body)
	mag := ""
	if b.body.V > 0 {
		mag = fmt.Sprintf(", predicted at magnitude %.1f", b.body.V)
		if tr.cand.NightMag != nil {
			mag += fmt.Sprintf(" against %.1f measured", *tr.cand.NightMag)
		}
	}
	moves := fmt.Sprintf("it moves %s", motionWords(b.rate, b.pa))
	if b.match {
		ev := fmt.Sprintf("Matches %s%s: it is predicted %.1f arcsec from the detections, and %s.", bodyLabel(b.body), mag, b.offset, moves)
		if b.offset > 10 {
			ev += fmt.Sprintf(" That offset is within the %.0f arcsec its orbit allows (%s), and it moves the same way.", b.allow, why)
		}
		return check.New(check.MoverKnown, check.Fail, ev), &b
	}
	if b.offset <= b.allow && b.same {
		return check.New(check.MoverKnown, check.Pass, fmt.Sprintf("No known asteroid or comet fits. The closest, %s%s, is predicted %.0f arcsec away and moves the same way, but it is too faint to be this one.",
			bodyLabel(b.body), mag, b.offset)), nil
	}
	if b.offset <= b.allow {
		return check.New(check.MoverKnown, check.Pass, fmt.Sprintf("No known asteroid or comet fits. The closest, %s%s, is predicted %.0f arcsec away, within the %.0f arcsec its orbit allows (%s), but %s, not the way this one does.",
			bodyLabel(b.body), mag, b.offset, b.allow, why, moves)), nil
	}
	return check.New(check.MoverKnown, check.Pass, fmt.Sprintf("No known asteroid or comet fits. The closest, %s%s, is predicted %.0f arcsec away, beyond the %.0f arcsec its orbit allows (%s), and %s.",
		bodyLabel(b.body), mag, b.offset, b.allow, why, moves)), nil
}

// motionCheck compares the track's motion with the known asteroids
// around it on the same date.
func motionCheck(tr track, bodies []discoveries.Body, radius float64) check.Check {
	var rates, pas []float64
	like := 0
	closest := math.Inf(1)
	for _, b := range bodies {
		if b.Kind == kindPlanet || b.Kind == kindComet || (b.DRA == 0 && b.DDec == 0) {
			continue
		}
		r, pa := bodyRate(b)
		rates, pas = append(rates, r), append(pas, pa)
		if sameMotion(tr.rate, tr.pa, r, pa) {
			like++
		}
		dv := math.Hypot(tr.rate*math.Sin(tr.pa*math.Pi/180)-r*math.Sin(pa*math.Pi/180), tr.rate*math.Cos(tr.pa*math.Pi/180)-r*math.Cos(pa*math.Pi/180))
		closest = math.Min(closest, dv)
	}
	area := fmt.Sprintf("within %.0f arcmin", radius*60)
	if radius >= 1 {
		area = fmt.Sprintf("within %.1f degrees", radius)
	}
	if len(rates) < 5 {
		return check.Skipped(check.MoverMotion, fmt.Sprintf("Not measured. Only %d known asteroids %s to compare with.", len(rates), area))
	}
	sort.Float64s(rates)
	lo, hi := rates[len(rates)/10], rates[len(rates)-1-len(rates)/10]
	paLo, paHi := spanPA(pas)
	their := fmt.Sprintf("the %d known asteroids %s, which move %.2f to %.2f arcsec a minute toward %.0f to %.0f degrees", len(rates), area, lo, hi, paLo, paHi)
	switch {
	case like > 0:
		return check.New(check.MoverMotion, check.Pass, fmt.Sprintf("It moves %s, like %d of %s.", motionWords(tr.rate, tr.pa), like, their))
	case tr.rate > rates[len(rates)-1]:
		return check.New(check.MoverMotion, check.Pass, fmt.Sprintf("It moves %s, faster than all of %s. An asteroid passing near the Earth moves like this.", motionWords(tr.rate, tr.pa), their))
	}
	return check.New(check.MoverMotion, check.Fail, fmt.Sprintf("It moves %s, unlike any of %s; the nearest motion differs by %.2f arcsec a minute.", motionWords(tr.rate, tr.pa), their, closest))
}

// spanPA is the narrowest range of directions holding the middle 80% of
// pas, in degrees east of north.
func spanPA(pas []float64) (float64, float64) {
	if len(pas) == 0 {
		return 0, 0
	}
	c := append([]float64(nil), pas...)
	sort.Float64s(c)
	gap, at := -1.0, 0
	for i := range c {
		next := c[(i+1)%len(c)]
		if i == len(c)-1 {
			next += 360
		}
		if g := next - c[i]; g > gap {
			gap, at = g, i
		}
	}
	rot := make([]float64, len(c))
	for i := range c {
		rot[i] = c[(at+1+i)%len(c)]
		if i > 0 && rot[i] < rot[i-1] {
			rot[i] += 360
		}
	}
	k := len(rot) / 10
	return math.Mod(rot[k], 360), math.Mod(rot[len(rot)-1-k], 360)
}

// otherNight is another night of the target and what was found there.
type otherNight struct {
	night    time.Time
	at       time.Time
	ra, dec  float64
	covered  bool
	compared bool
	depth    *float64
	found    *app.DiscoveryCandidate
	sep      float64
	radius   float64
}

func nightsCheck(nights []otherNight) check.Check {
	if len(nights) == 0 {
		return check.Skipped(check.MoverNights, "Not measured. You have no frames of this target within 3 nights either side.")
	}
	var seen, covered, notCompared []string
	radius := 0.0
	for _, n := range nights {
		d := n.night.Format(time.DateOnly)
		switch {
		case n.found != nil:
			seen = append(seen, fmt.Sprintf("on %s a track moving the same way was found %.0f arcsec from where this one would be", d, n.sep))
		case !n.covered:
		case !n.compared:
			notCompared = append(notCompared, d)
		default:
			lim := ""
			if n.depth != nil {
				lim = fmt.Sprintf(", single frames reaching %.1f", *n.depth)
			}
			covered = append(covered, d+lim)
			radius = math.Max(radius, n.radius)
		}
	}
	if len(seen) > 0 {
		return check.New(check.MoverNights, check.Pass, sentence(joinWords(seen)))
	}
	if len(covered) > 0 {
		return check.Skipped(check.MoverNights, fmt.Sprintf(notSeen+" Your frames of %s cover where it would have been, but no moving object was found within %.0f arcsec of there. It may have been too faint, or the straight path may not carry that far.", joinWords(covered), radius))
	}
	if len(notCompared) > 0 {
		return check.Skipped(check.MoverNights, fmt.Sprintf("Not measured yet. Your frames of %s cover where it would have been, but %s not been searched for moving objects yet.", joinWords(notCompared), hasHave(len(notCompared))))
	}
	return check.Skipped(check.MoverNights, "Not measured. Your frames of the nights around it do not cover where it would have been.")
}

func depthWords(tr track, run app.DiscoveryRun) string {
	if tr.cand.NightMag == nil || run.SubDepth == nil {
		return ""
	}
	m, d := *tr.cand.NightMag, *run.SubDepth
	switch {
	case m <= d-0.5:
		return fmt.Sprintf(" At magnitude %.1f it is %.1f mag brighter than the faintest a single frame shows (%.1f).", m, d-m, d)
	case m <= d:
		return fmt.Sprintf(" At magnitude %.1f it is close to the faintest a single frame shows (%.1f).", m, d)
	}
	return fmt.Sprintf(" At magnitude %.1f it is fainter than a single frame's usual limit (%.1f), so each detection is near the noise.", m, d)
}

// moverSuggest turns the moving object checks into a suggestion. A known
// object that fits where and how it moves settles it first: nothing else
// lands on a known orbit by chance. With stack nil it suggests from the
// cheaper checks alone; with it, a stack that finds the object absent
// makes it an artifact, and only a stack that confirms it lets it be
// real: the ten unknown movers of 2025 and 2026 audited on 2026-10-17
// were all artifacts.
func moverSuggest(groups []Group, flags ownimage.TrackFlags, known *knownBody, stack *ownimage.StackVerdict) Suggestion {
	ev := func(id string) string { return evidence(groups, id) }
	st := func(id string) check.Status { return status(groups, id) }
	if known != nil {
		conf := High
		if known.offset > 10 {
			conf = Medium
		}
		return Suggestion{State: app.VetKnown, Say: "Known asteroid or comet: " + bodyLabel(known.body), Confidence: conf, Why: ev(check.MoverKnown)}
	}
	switch {
	case flags.HotPixel:
		conf := Medium
		if flags.NotStar || strings.Contains(ev(check.MoverSensor), "lined up by chance") {
			conf = High
		}
		return Suggestion{State: app.VetArtifact, Say: "Artifact: hot pixel carried along by the frames", Confidence: conf, Why: joinSentences(ev(check.MoverSensor), shapeWhy(groups))}
	case flags.Flash:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: a flash or noise, not a moving object", Confidence: Medium, Why: ev(check.MoverSteady)}
	case flags.NotStar:
		return Suggestion{State: app.VetArtifact, Say: sayNotStar, Confidence: Medium, Why: ev(check.MoverShape)}
	case flags.Faint:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: not there when measured again", Confidence: Medium, Why: ev(check.MoverFrames)}
	case stack != nil && *stack == ownimage.StackRefuted:
		return Suggestion{State: app.VetArtifact, Say: "Artifact: not there when its frames are stacked along its motion", Confidence: Medium, Why: ev(check.MoverStack)}
	}
	if st(check.MoverLine) == check.Fail {
		return Suggestion{State: app.VetUnsure, Say: "Not sure: it does not keep to a straight, steady path", Confidence: Low, Why: ev(check.MoverLine)}
	}
	core := []string{check.MoverFrames, check.MoverLine, check.MoverSensor, check.MoverShape, check.MoverSteady, check.MoverKnown}
	if stack != nil && *stack == ownimage.StackDoubtful {
		return Suggestion{State: app.VetUnsure, Say: "Not sure: its frames stacked along its motion do not confirm it", Confidence: Low, Why: ev(check.MoverStack)}
	}
	var missing []string
	for _, id := range core {
		if st(id) != check.Pass {
			missing = append(missing, lowerFirst(check.Names()[id]))
		}
	}
	if len(missing) > 0 {
		return Suggestion{State: app.VetUnsure, Say: "Not sure: some checks could not be made", Confidence: Low,
			Why: fmt.Sprintf("Nothing found against it, but these could not be checked: %s.", joinWords(missing))}
	}
	if stack != nil && *stack != ownimage.StackConfirmed {
		return Suggestion{State: app.VetUnsure, Say: "Not sure: its frames have not been stacked to confirm it", Confidence: Low,
			Why: joinSentences(ev(check.MoverStack), "Every other check passed, but an object found in your frames alone needs its stack to confirm it.")}
	}
	if st(check.MoverMotion) == check.Fail {
		return Suggestion{State: app.VetUnsure, Say: "Not sure: it moves unlike the asteroids around it", Confidence: Low,
			Why: joinSentences(ev(check.MoverMotion), "Every other check passed, so look at its frames before reporting it.")}
	}
	conf := Medium
	if st(check.MoverMotion) == check.Pass && st(check.MoverNights) == check.Pass {
		conf = High
	}
	why := joinSentences(ev(check.MoverLine), "It is star-shaped, steady from frame to frame and moves across the sensor, and no known asteroid or comet fits.")
	if strings.HasPrefix(ev(check.MoverNights), notSeen) {
		conf = Low
		why = joinSentences(why, ev(check.MoverNights))
	}
	return Suggestion{State: app.VetReal, Say: "Looks like an unknown asteroid; " + mpcAdvice, Confidence: conf, Why: why}
}

func shapeWhy(groups []Group) string {
	if status(groups, check.MoverShape) == check.Fail {
		return evidence(groups, check.MoverShape)
	}
	return ""
}

// trackChart is the object's brightness along its track.
func trackChart(tr track) Chart {
	ch := Chart{ID: "track", Title: "Along the track, " + dateWords(tr.cand.Night.UTC()), XUnit: unitHours, YUnit: unitMag, YLabel: labelMag,
		XLabel: "Hours from its first detection", Sub: "Its brightness in each frame it was found in. Lower is fainter."}
	var pts []discoveries.TrackPoint
	for _, p := range tr.points {
		if p.Mag != nil {
			pts = append(pts, p)
		}
	}
	if len(pts) == 0 {
		ch.Empty = notMeasured
		return ch
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		e := 0.0
		if p.MagErr != nil {
			e = *p.MagErr
		}
		x := p.Time.Sub(pts[0].Time).Hours()
		ch.Points = append(ch.Points, ChartPoint{X: round(x, 4), Y: round(*p.Mag, 4), Err: round(e, 4)})
		lo, hi = math.Min(lo, *p.Mag-e), math.Max(hi, *p.Mag+e)
	}
	x1 := ch.Points[len(ch.Points)-1].X
	pad := math.Max(0.05, x1*0.08)
	ch.X0, ch.X1 = -pad, x1+pad
	ch.Y0, ch.Y1 = padRange(lo, hi)
	ch.XTicks, ch.YTicks = ticks(ch.X0, ch.X1, 5), ticks(ch.Y0, ch.Y1, 5)
	ch.Legend = []LegendItem{{Style: styleDot, Label: "Its detections"}}
	return ch
}

func hasHave(n int) string {
	if n == 1 {
		return "that night has"
	}
	return "those nights have"
}

// lowerFirst lowers a name's first letter for the middle of a sentence,
// unless it starts a word in capitals, as in ATLAS or ZTF.
func lowerFirst(s string) string {
	if len(s) < 2 || strings.ToUpper(s[1:2]) == s[1:2] {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
