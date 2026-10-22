package binaries

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/klauspost/compress/zstd"
)

func liveMid() time.Time { return time.Date(2026, 10, 21, 3, 41, 0, 0, time.UTC) }

func liveModel() eclipseModel {
	half := 105 * time.Minute
	return eclipseModel{mid: liveMid(), err: 4 * time.Minute, half: half, depth: 0.45, shape: func(dt time.Duration) float64 {
		u := dt.Seconds() / half.Seconds()
		if math.Abs(u) >= 1 {
			return 0
		}
		return math.Pow(1-u*u, 0.7)
	}}
}

// synthRun is subs every 5 minutes from 130 minutes before the expected
// middle to last minutes from it, the dip shift late and depth deep, on a
// level of 12.3 with noise of 0.02 mag, and the nearby stars steady to
// 0.008 mag.
func synthRun(last int, shift time.Duration, depth float64) []LivePoint {
	const sigma = 0.02
	m := liveModel()
	r := rand.New(rand.NewPCG(3, 5))
	var out []LivePoint
	for k := -130; k <= last; k += 5 {
		t := liveMid().Add(time.Duration(k) * time.Minute)
		y := 12.3 + depth*m.at(t, shift) + sigma*r.NormFloat64()
		out = append(out, LivePoint{FrameID: len(out) + 1, Time: t, Filter: "R", Mag: y, Err: sigma, Comp: 0.008 * r.NormFloat64()})
	}
	return out
}

func TestEclipseFollowingTheCurve(t *testing.T) {
	t.Parallel()
	j := judgeEclipse(synthRun(60, 3*time.Minute, 0.45), liveModel())
	if j.state != LiveFollowing {
		t.Fatalf("state %s (%s), want following", j.state, j.why)
	}
	if math.Abs(j.shift.Minutes()-3) > 3 || j.shiftErr > 4*time.Minute {
		t.Errorf("timing %.1f ± %.1f min, truth 3", j.shift.Minutes(), j.shiftErr.Minutes())
	}
	if math.Abs(j.depth-0.45) > 0.04 || math.Abs(j.zero-12.3) > 0.02 {
		t.Errorf("depth %.3f level %.3f, truth 0.45 and 12.3", j.depth, j.zero)
	}
}

func TestEclipseLateIsOff(t *testing.T) {
	t.Parallel()
	j := judgeEclipse(synthRun(80, 25*time.Minute, 0.45), liveModel())
	if j.state != LiveOff || !strings.Contains(j.why, "min late") {
		t.Fatalf("state %s (%s), want off and late", j.state, j.why)
	}
	if math.Abs(j.shift.Minutes()-25) > 4 {
		t.Errorf("timing %.1f min, truth 25", j.shift.Minutes())
	}
}

func TestEclipseFarLateIsOff(t *testing.T) {
	t.Parallel()
	j := judgeEclipse(synthRun(330, 150*time.Minute, 0.45), liveModel())
	if j.state != LiveOff || !strings.Contains(j.why, "min late") {
		t.Fatalf("state %s (%s), want off and late", j.state, j.why)
	}
	if math.Abs(j.shift.Minutes()-150) > 4 {
		t.Errorf("timing %.1f min, truth 150", j.shift.Minutes())
	}
	if before := judgeEclipse(synthRun(330, 150*time.Minute, 0.45)[:30], liveModel()); before.state == LiveFollowing {
		t.Errorf("before the late dip ends: state %s (%s)", before.state, before.why)
	}
}

func TestEclipseShallowIsOff(t *testing.T) {
	t.Parallel()
	j := judgeEclipse(synthRun(60, 0, 0.25), liveModel())
	if j.state != LiveOff || !strings.Contains(j.why, "shallower") || strings.Contains(j.why, "late") || strings.Contains(j.why, "early") {
		t.Fatalf("state %s (%s), want off and shallower only", j.state, j.why)
	}
}

func TestEclipseTooEarly(t *testing.T) {
	t.Parallel()
	if j := judgeEclipse(synthRun(-110, 0, 0.45), liveModel()); j.state != LiveEarly || !strings.Contains(j.why, "6 subs needed") {
		t.Errorf("five subs: state %s (%s), want early", j.state, j.why)
	}
	if j := judgeEclipse(synthRun(-60, 0, 0.45), liveModel()); j.state != LiveEarly || !strings.Contains(j.why, "middle") {
		t.Errorf("none near the middle: state %s (%s), want early", j.state, j.why)
	}
}

func TestEclipseNotHeldWhenTheNearbyStarsDimTogether(t *testing.T) {
	t.Parallel()
	pts := synthRun(60, 0, 0.45)
	for i := len(pts) - 5; i < len(pts); i++ {
		pts[i].Comp += 0.3
		pts[i].Agree, pts[i].Present = 12, 12
	}
	if j := judgeEclipse(pts, liveModel()); j.state != LiveFollowing || math.Abs(j.comps.shift-0.3) > 0.03 {
		t.Errorf("state %s (%s) shift %.3f, want following with the 0.3 mag shared dimming noted", j.state, j.why, j.comps.shift)
	}
}

func TestEclipseHeldBackWhenNearbyStarsDisagree(t *testing.T) {
	t.Parallel()
	pts := synthRun(60, 0, 0.45)
	for i := len(pts) - 5; i < len(pts); i++ {
		pts[i].Agree, pts[i].Present = 5, 12
	}
	if j := judgeEclipse(pts, liveModel()); j.state != LiveComps || !strings.Contains(j.why, "only 5 of the 12 nearby stars agree") {
		t.Errorf("state %s (%s), want nearby stars disagreeing", j.state, j.why)
	}
}

// synthWatch is 30 subs 3 minutes apart on a level of 16.9 with noise of
// 0.03 mag, faded by fade for subs from to to.
func synthWatch(fade float64, from, to int) []LivePoint {
	r := rand.New(rand.NewPCG(9, 1))
	const n, sigma = 30, 0.03
	out := make([]LivePoint, 0, n)
	for i := range n {
		y := 16.9 + sigma*r.NormFloat64()
		if i >= from && i <= to {
			y += fade
		}
		out = append(out, LivePoint{FrameID: i + 1, Time: liveMid().Add(time.Duration(3*i) * time.Minute), Filter: "L", Mag: y, Err: sigma, Comp: 0.008 * r.NormFloat64()})
	}
	return out
}

func TestDipWatchQuiet(t *testing.T) {
	t.Parallel()
	j := judgeDip(synthWatch(0, -1, -1), goalOf(0.15, 2))
	if j.state != LiveNoDip || j.found {
		t.Fatalf("state %s (%s), want no dip", j.state, j.why)
	}
	if j.smallest < 0.05 || j.smallest > 0.09 || j.over != 12*time.Minute {
		t.Errorf("smallest dip %.3f over %s, want about 0.06 over 12m", j.smallest, j.over)
	}
	if math.Abs(j.zero-16.9) > 0.02 {
		t.Errorf("level %.3f, truth 16.9", j.zero)
	}
}

func TestDipWatchSeesADip(t *testing.T) {
	t.Parallel()
	j := judgeDip(synthWatch(0.3, 12, 25), goalOf(0.15, 2))
	if j.state != LiveDip {
		t.Fatalf("state %s (%s), want dip", j.state, j.why)
	}
	if want := liveMid().Add(36 * time.Minute); !j.start.Equal(want) {
		t.Errorf("dip starts %s, want %s", j.start, want)
	}
	if math.Abs(j.depth-0.3) > 0.04 || math.Abs(j.zero-16.9) > 0.02 {
		t.Errorf("depth %.3f level %.3f, truth 0.3 and 16.9", j.depth, j.zero)
	}
}

func TestDipWatchSharedDimmingIsNoDip(t *testing.T) {
	t.Parallel()
	pts := synthWatch(0, -1, -1)
	for i := 12; i < 30; i++ {
		pts[i].Comp += 0.4
	}
	if j := judgeDip(pts, goalOf(0.15, 2)); j.state != LiveNoDip || j.found {
		t.Errorf("state %s (%s), want no dip while the nearby stars dim together", j.state, j.why)
	}
	pts = synthWatch(0.3, 12, 18)
	for i := 12; i <= 18; i++ {
		pts[i].Agree, pts[i].Present = 4, 12
	}
	if j := judgeDip(pts, goalOf(0.15, 2)); j.found {
		t.Errorf("state %s (%s), want no dip while the nearby stars disagree", j.state, j.why)
	}
	for i := 25; i < 30; i++ {
		pts[i].Agree, pts[i].Present = 4, 12
	}
	if j := judgeDip(pts, goalOf(0.15, 2)); j.state != LiveComps {
		t.Errorf("state %s (%s), want nearby stars disagreeing", j.state, j.why)
	}
}

// tonight is the dip watch on find 15523 of 2026-10-21 UTC, 02:10 to
// 06:59, as the live card measures it: 87 subs, 76 of them below the
// cut, while the nearby stars dimmed together 0.4 mag as the field sank
// toward the horizon.
func tonight(t *testing.T) []LivePoint {
	t.Helper()
	raw, err := os.ReadFile("testdata/live15523-20261021.json.zst")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var rows []struct {
		T       time.Time `json:"t"`
		Mag     float64   `json:"mag"`
		Err     float64   `json:"err"`
		Comp    float64   `json:"comp"`
		Spread  float64   `json:"spread"`
		Agree   int       `json:"agree"`
		Present int       `json:"present"`
	}
	if err := json.NewDecoder(dec).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	out := make([]LivePoint, len(rows))
	for i, r := range rows {
		out[i] = LivePoint{FrameID: i + 1, Time: r.T, Filter: redFilter, Mag: r.Mag, Err: r.Err, Comp: r.Comp, Spread: r.Spread, Agree: r.Agree, Present: r.Present}
	}
	return out
}

func TestDipWatchTonightIsNotHeldBack(t *testing.T) {
	t.Parallel()
	pts := tonight(t)
	last := make([]float64, 0, compRecent)
	for _, p := range pts[len(pts)-compRecent:] {
		last = append(last, p.Comp)
	}
	if m := medianOf(last); m < 0.2 {
		t.Fatalf("the nearby stars read %.2f mag fainter over the last subs, want tonight's 0.24", m)
	}
	j := judgeDip(pts, goalOf(0.15, 2))
	if j.state != LiveNoDip || j.found {
		t.Fatalf("state %s (%s), want no dip and no hold", j.state, j.why)
	}
	if j.run < 12 || j.run > 18 || j.smallest > 0.16 || math.Abs(j.latest) > 0.1 {
		t.Errorf("runs of %d subs, smallest dip %.3f, latest %.3f", j.run, j.smallest, j.latest)
	}
}

func TestDipWatchTonightSeesADip(t *testing.T) {
	t.Parallel()
	pts := tonight(t)
	from := len(pts) - 30
	for i := from; i < len(pts); i++ {
		pts[i].Mag += 0.3
	}
	j := judgeDip(pts, goalOf(0.15, 2))
	if j.state != LiveDip || math.Abs(j.depth-0.3) > 0.08 {
		t.Fatalf("state %s (%s) depth %.3f, want a 0.3 mag dip", j.state, j.why, j.depth)
	}
	if j.start.Before(pts[from-j.run].Time) || j.start.After(pts[from+j.run].Time) {
		t.Errorf("dip starts %s, truth %s", j.start, pts[from].Time)
	}
}

func TestDipWatchTonightTooFaintIsHeld(t *testing.T) {
	t.Parallel()
	pts := tonight(t)
	for i := len(pts) - compRecent; i < len(pts); i++ {
		pts[i].Err = 0.3
	}
	if j := judgeDip(pts, goalOf(0.15, 2)); j.state != LiveFaint || !strings.Contains(j.why, "measured only to 0.30 mag") {
		t.Errorf("state %s (%s), want too faint", j.state, j.why)
	}
}

// tonight27822 is the second eclipse of find 27822 on 2026-10-22 UTC as
// the live card measured it by 05:07: 88 Red subs from 01:35, all before
// the expected middle at 06:35, and the expected curve, whose shape the
// model reads back from its samples.
func tonight27822(t *testing.T) ([]LivePoint, eclipseModel) {
	t.Helper()
	return live27822(t, "testdata/live27822-20261022.json.zst")
}

func live27822(t *testing.T, path string) ([]LivePoint, eclipseModel) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var in struct {
		Points []struct {
			T       time.Time `json:"t"`
			Mag     float64   `json:"mag"`
			Err     float64   `json:"err"`
			Comp    float64   `json:"comp"`
			Spread  float64   `json:"spread"`
			Agree   int       `json:"agree"`
			Present int       `json:"present"`
		} `json:"points"`
		Eclipse struct {
			Mid    time.Time `json:"mid"`
			ErrMin float64   `json:"err_min"`
			Start  time.Time `json:"start"`
			End    time.Time `json:"end"`
			Depth  float64   `json:"depth"`
			Curve  []struct {
				T time.Time `json:"t"`
				V float64   `json:"v"`
			} `json:"curve"`
		} `json:"eclipse"`
	}
	if err := json.NewDecoder(dec).Decode(&in); err != nil {
		t.Fatal(err)
	}
	pts := make([]LivePoint, len(in.Points))
	for i, r := range in.Points {
		pts[i] = LivePoint{FrameID: i + 1, Time: r.T, Filter: redFilter, Mag: r.Mag, Err: r.Err, Comp: r.Comp, Spread: r.Spread, Agree: r.Agree, Present: r.Present}
	}
	e := in.Eclipse
	curve := e.Curve
	shape := func(dt time.Duration) float64 {
		at := e.Mid.Add(dt)
		k := sort.Search(len(curve), func(i int) bool { return !curve[i].T.Before(at) })
		if k == 0 || k == len(curve) {
			return 0
		}
		a, b := curve[k-1], curve[k]
		f := at.Sub(a.T).Seconds() / b.T.Sub(a.T).Seconds()
		return (a.V + f*(b.V-a.V)) / e.Depth
	}
	m := eclipseModel{mid: e.Mid, err: time.Duration(e.ErrMin * float64(time.Minute)), half: e.End.Sub(e.Start) / 2, depth: e.Depth, shape: shape}
	return pts, m
}

// synth27822 is tonight's sub times and errors carried on at the same
// pace to until, with the expected eclipse shift late on a flat level and
// noise at each sub's own error.
func synth27822(pts []LivePoint, m eclipseModel, until time.Time, shift time.Duration) []LivePoint {
	r := rand.New(rand.NewPCG(27822, 1022))
	gap := pts[len(pts)-1].Time.Sub(pts[0].Time) / time.Duration(len(pts)-1)
	out := slices.Clone(pts)
	for t, i := pts[len(pts)-1].Time.Add(gap), 0; !t.After(until); t, i = t.Add(gap), i+1 {
		p := pts[i%len(pts)]
		p.FrameID, p.Time = len(out)+1, t
		out = append(out, p)
	}
	for i := range out {
		out[i].Mag = m.depth*m.at(out[i].Time, shift) + out[i].Err*r.NormFloat64()
	}
	return out
}

func liveRun27822(m eclipseModel) *liveRun {
	return &liveRun{model: &m, from: m.mid.Add(-9 * time.Hour), to: m.mid.Add(9 * time.Hour),
		view: LiveCurve{Kind: LiveEclipse, Eclipse: &LiveEclipseView{Mid: m.mid, Depth: m.depth}}}
}

func rawOf(pts []LivePoint) []ownimage.LivePoint {
	out := make([]ownimage.LivePoint, len(pts))
	for i, p := range pts {
		out[i] = ownimage.LivePoint{FrameID: p.FrameID, Time: p.Time, Filter: p.Filter, Mag: p.Mag, Err: p.Err, Comp: p.Comp, Spread: p.Spread, Agree: p.Agree, Present: p.Present}
	}
	return out
}

func TestTonight27822BeforeTheMiddleHasNoTiming(t *testing.T) {
	t.Parallel()
	pts, m := tonight27822(t)
	if last := pts[len(pts)-1].Time; len(pts) != 88 || !last.Before(m.mid) {
		t.Fatalf("%d subs to %s, want tonight's 88 all before the middle %s", len(pts), last, m.mid)
	}
	j := judgeEclipse(pts, m)
	if !j.ok || j.shift > -30*time.Minute {
		t.Fatalf("one-sided fit ok %v at %.0f min, want tonight's fit of about 48 min early", j.ok, j.shift.Minutes())
	}
	if j.timed || strings.Contains(j.why, "early") || strings.Contains(j.why, "timing error of") {
		t.Errorf("timed %v (%s), want no timing before the middle", j.timed, j.why)
	}
	v := liveView(liveRun27822(m), rawOf(pts), 15, pts[len(pts)-1].Time)
	if v.Eclipse.ShiftMin != nil || v.Eclipse.ShiftErr != nil {
		t.Errorf("the card times the minimum at %v ± %v min before the middle", *v.Eclipse.ShiftMin, *v.Eclipse.ShiftErr)
	}
}

func TestTonight27822BothSidesIsTimed(t *testing.T) {
	t.Parallel()
	pts, m := tonight27822(t)
	const truth = 6 * time.Minute
	full := synth27822(pts, m, m.mid.Add(m.half), truth)
	v := liveView(liveRun27822(m), rawOf(full), 15, full[len(full)-1].Time)
	e := v.Eclipse
	if e.ShiftMin == nil || e.ShiftErr == nil {
		t.Fatalf("state %s (%s), want a timing once both sides are covered", v.State, v.Why)
	}
	if math.Abs(*e.ShiftMin-truth.Minutes()) > math.Max(4, 2**e.ShiftErr) {
		t.Errorf("timing %.1f ± %.1f min, truth %.0f", *e.ShiftMin, *e.ShiftErr, truth.Minutes())
	}
	early := synth27822(pts, m, m.mid.Add(truth+2*time.Minute), truth)
	if j := judgeEclipse(early, m); j.timed {
		t.Errorf("timed %.1f min with only the subs to %s, before %d past the fitted middle", j.shift.Minutes(), early[len(early)-1].Time, minFramesSide)
	}
}

func TestLiveViewDropsAStaleTiming(t *testing.T) {
	t.Parallel()
	pts, m := tonight27822(t)
	run := liveRun27822(m)
	stale, se := -48.0, 24.0
	run.view.Eclipse.ShiftMin, run.view.Eclipse.ShiftErr = &stale, &se
	if v := liveView(run, rawOf(pts[:3]), 15, pts[2].Time); v.Eclipse.ShiftMin != nil || v.Eclipse.FitDepth != nil {
		t.Errorf("a timing of %v min carried over from an earlier look", *v.Eclipse.ShiftMin)
	}
}

func TestTonight27822BeforeTheMiddleHasNoDepth(t *testing.T) {
	t.Parallel()
	pts, m := tonight27822(t)
	j := judgeEclipse(pts, m)
	if !j.ok || j.depth <= 0 {
		t.Fatalf("one-sided fit ok %v at depth %.3f, want tonight's one-sided depth", j.ok, j.depth)
	}
	if strings.Contains(j.why, "deeper") || strings.Contains(j.why, "shallower") {
		t.Errorf("state %s (%s), want no depth judged before the middle", j.state, j.why)
	}
	v := liveView(liveRun27822(m), rawOf(pts), 15, pts[len(pts)-1].Time)
	if v.Eclipse.FitDepth != nil || v.Eclipse.DepthErr != nil {
		t.Errorf("the card gives a depth of %v ± %v mag from subs before the middle only", *v.Eclipse.FitDepth, *v.Eclipse.DepthErr)
	}
	half := m
	half.depth = 0.4 * m.depth
	if j := judgeEclipse(synth27822(pts, half, m.mid.Add(-10*time.Minute), 0), m); j.state == LiveOff {
		t.Errorf("state %s (%s), want no shallow dip called from subs before the middle", j.state, j.why)
	}
}

func TestTonight27822BothSidesMeasuresTheDepth(t *testing.T) {
	t.Parallel()
	pts, m := tonight27822(t)
	truth := m
	truth.depth = 0.75 * m.depth
	full := synth27822(pts, truth, m.mid.Add(m.half), 0)
	v := liveView(liveRun27822(m), rawOf(full), 15, full[len(full)-1].Time)
	e := v.Eclipse
	if e.FitDepth == nil || e.DepthErr == nil {
		t.Fatalf("state %s (%s), want a depth once both sides are covered", v.State, v.Why)
	}
	if math.Abs(*e.FitDepth-truth.depth) > math.Max(0.01, 3**e.DepthErr) {
		t.Errorf("depth %.3f ± %.3f mag, truth %.3f", *e.FitDepth, *e.DepthErr, truth.depth)
	}
}

// TestTonight27822AtTwoThirtySevenIsNotOff is the card at 02:37 CDT, 151
// Red subs to 07:35 UTC, an hour past the expected middle. A curve held
// at the surveys' width put the middle 38 ± 12 min early and called the
// eclipse off its 5 min timing error, yet 14 TESS second eclipses of
// 2019 to 2025 sit 1 ± 2 min from the ephemeris. With one side of the dip
// still open, a wider dip fits as well with its middle near the expected
// one, so the timing error must take in the width.
func TestTonight27822AtTwoThirtySevenIsNotOff(t *testing.T) {
	t.Parallel()
	pts, m := live27822(t, "testdata/live27822-20261022-0237.json.zst")
	if len(pts) != 151 || m.err.Minutes() < 5.1 || m.err.Minutes() > 5.2 {
		t.Fatalf("%d subs and a %.2f min timing error, want 151 and 5.14", len(pts), m.err.Minutes())
	}
	j := judgeEclipse(pts, m)
	if j.state == LiveOff {
		t.Errorf("state off (%s), want the subs within reach of the expected curve", j.why)
	}
	if !j.ok || j.shiftErr < 15*time.Minute || j.shift < -45*time.Minute || j.shift > 0 {
		t.Errorf("timing %.1f ± %.1f min (ok %v), want about 27 ± 20 min early", j.shift.Minutes(), j.shiftErr.Minutes(), j.ok)
	}
}

// TestWiderDipIsNotTakenForAnEarlyOne draws tonight's 02:37 subs again
// from a dip on time but 1.25 times as wide as the surveys': a curve held
// at their width put its middle a median 18 min early, with half the
// draws beyond twice their error.
func TestWiderDipIsNotTakenForAnEarlyOne(t *testing.T) {
	t.Parallel()
	pts, m := live27822(t, "testdata/live27822-20261022-0237.json.zst")
	at := m.widened(1.25)
	var shifts []float64
	within := 0
	for seed := range uint64(40) {
		r := rand.New(rand.NewPCG(27822, seed))
		for i := range pts {
			pts[i].Mag = m.depth*at(pts[i].Time, 0) + pts[i].Err*r.NormFloat64()
		}
		j := judgeEclipse(pts, m)
		if !j.ok {
			continue
		}
		shifts = append(shifts, j.shift.Minutes())
		if absDur(j.shift) <= 2*j.shiftErr {
			within++
		}
	}
	slices.Sort(shifts)
	if n := len(shifts); n < 30 || math.Abs(shifts[n/2]) > 5 || float64(within) < 0.85*float64(n) {
		t.Errorf("%d of 40 draws fitted, median %.1f min, %d within twice their error", n, shifts[n/2], within)
	}
}

func TestNoDipIsNotTimed(t *testing.T) {
	t.Parallel()
	pts, m := live27822(t, "testdata/live27822-20261022-0237.json.zst")
	timed := 0
	for seed := range uint64(40) {
		r := rand.New(rand.NewPCG(seed, 99))
		for i := range pts {
			pts[i].Mag = pts[i].Err * r.NormFloat64()
		}
		if j := judgeEclipse(pts, m); j.ok {
			timed++
		}
	}
	if timed > 4 {
		t.Errorf("%d of 40 nights with no dip at all gave a timing", timed)
	}
}

// TestTonight27822AllSubs is the whole night, 167 Red subs from 01:35 to
// 08:14 UTC, 99 min past the expected middle with the dip's last 91 min
// unseen. The curve held at the surveys' width could not bound the
// middle (19.5 min early, give or take 137); with the width fitted too
// it lands 4.5 min early, give or take 21, on the expected time.
func TestTonight27822AllSubs(t *testing.T) {
	t.Parallel()
	pts, m := live27822(t, "testdata/live27822-20261022-0318.json.zst")
	if len(pts) != 167 {
		t.Fatalf("%d subs, want 167", len(pts))
	}
	j := judgeEclipse(pts, m)
	if !j.ok || !j.timed || j.state == LiveOff || math.Abs(j.shift.Minutes()+4.5) > 3 || j.shiftErr < 15*time.Minute || j.shiftErr > 30*time.Minute {
		t.Errorf("timing %.1f ± %.1f min, ok %v, timed %v, state %s (%s); want about 4.5 ± 21 min early, on time", j.shift.Minutes(), j.shiftErr.Minutes(), j.ok, j.timed, j.state, j.why)
	}
}
