package minima

import (
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

const testPeriod = 3.7

func testDip(mid float64) Dip {
	w, a := 0.03, 1.6
	return Dip{Mid: mid, Half: w * testPeriod, Shape: func(dt float64) float64 { return period.Shape(dt/testPeriod, w, a) }}
}

type night struct {
	from, to, step float64
	noise, red     float64
	depth          float64
	slope          float64
}

func synth(r *rng, truth float64, d Dip, n night) []Point {
	var out []Point
	walk := 0.0
	for t := n.from; t <= n.to; t += n.step {
		walk = 0.8*walk + n.red*r.normal()
		y := 0.3 + n.depth*d.Shape(t-truth) + n.slope*(t-d.Mid) + n.noise*r.normal() + walk
		out = append(out, Point{T: t, Y: y, E: n.noise})
	}
	return out
}

func TestTimeRecoversKnownMinima(t *testing.T) {
	t.Parallel()
	r := &rng{s: 7}
	pulls := make([]float64, 0, 120)
	for i := range 120 {
		pred := 2461000.5 + float64(i)*testPeriod
		d := testDip(pred)
		truth := pred + (r.uniform()-0.5)*0.4*d.Half
		pts := synth(r, truth, d, night{from: pred - 1.6*d.Half, to: pred + 1.6*d.Half, step: 180.0 / 86400, noise: 0.012, depth: 0.4})
		m, why, ok := Time(pts, d)
		if !ok {
			t.Fatalf("trial %d: no minimum: %s", i, why)
		}
		pulls = append(pulls, (m.T-truth)/m.Err)
		if m.Check == nil {
			t.Fatalf("trial %d: no second method", i)
		}
	}
	within2, ss := 0, 0.0
	for _, p := range pulls {
		if math.Abs(p) < 2 {
			within2++
		}
		ss += p * p
	}
	rms := math.Sqrt(ss / float64(len(pulls)))
	if share := float64(within2) / float64(len(pulls)); share < 0.92 {
		t.Errorf("only %.0f%% of minima within 2 errors of the truth", 100*share)
	}
	if rms > 1.25 || rms < 0.5 {
		t.Errorf("pull rms %.2f, want near 1 (errors neither too small nor far too big)", rms)
	}
}

func TestTimeErrorGrowsWithRedNoise(t *testing.T) {
	t.Parallel()
	r := &rng{s: 11}
	var pulls []float64
	for i := range 100 {
		pred := 2461000.5 + float64(i)*testPeriod
		d := testDip(pred)
		truth := pred + (r.uniform()-0.5)*0.2*d.Half
		pts := synth(r, truth, d, night{from: pred - 1.6*d.Half, to: pred + 1.6*d.Half, step: 120.0 / 86400, noise: 0.008, red: 0.006, depth: 0.35})
		m, _, ok := Time(pts, d)
		if !ok {
			continue
		}
		if m.Bead <= 0 {
			t.Fatalf("trial %d: no red noise error", i)
		}
		pulls = append(pulls, (m.T-truth)/m.Err)
	}
	if len(pulls) < 85 {
		t.Fatalf("only %d of 100 timed", len(pulls))
	}
	within2 := 0
	for _, p := range pulls {
		if math.Abs(p) < 2 {
			within2++
		}
	}
	if share := float64(within2) / float64(len(pulls)); share < 0.88 {
		t.Errorf("with drifting noise only %.0f%% within 2 errors", 100*share)
	}
}

func TestTimeWithNightTrend(t *testing.T) {
	t.Parallel()
	r := &rng{s: 3}
	pred := 2461100.25
	d := testDip(pred)
	truth := pred + 0.004
	pts := synth(r, truth, d, night{from: pred - 1.2*d.Half, to: pred + 1.9*d.Half, step: 150.0 / 86400, noise: 0.01, depth: 0.3, slope: 0.6})
	m, why, ok := Time(pts, d)
	if !ok {
		t.Fatal(why)
	}
	if !m.Slope {
		t.Error("the night's trend was not fitted")
	}
	if math.Abs(m.T-truth) > 3*m.Err {
		t.Errorf("minimum %.5f, truth %.5f, error %.5f", m.T, truth, m.Err)
	}
}

func TestTimeNeedsBothSides(t *testing.T) {
	t.Parallel()
	r := &rng{s: 5}
	pred := 2461200.5
	d := testDip(pred)
	pts := synth(r, pred, d, night{from: pred - 1.6*d.Half, to: pred - 0.05*d.Half, step: 120.0 / 86400, noise: 0.01, depth: 0.4})
	if _, why, ok := Time(pts, d); ok || !strings.HasPrefix(why, ReasonSides) {
		t.Errorf("one side only gave ok=%v, %q", ok, why)
	}
}

func TestTimeRejectsFlatNight(t *testing.T) {
	t.Parallel()
	r := &rng{s: 9}
	pred := 2461300.5
	d := testDip(pred)
	pts := synth(r, pred, d, night{from: pred - 1.6*d.Half, to: pred + 1.6*d.Half, step: 120.0 / 86400, noise: 0.02, depth: 0})
	if _, _, ok := Time(pts, d); ok {
		t.Error("a night with no dip was timed")
	}
}

func TestKvWMatchesSymmetricMinimum(t *testing.T) {
	t.Parallel()
	r := &rng{s: 13}
	pulls := make([]float64, 0, 150)
	for i := range 150 {
		pred := 2461000.5 + float64(i)*testPeriod
		d := testDip(pred)
		truth := pred + (r.uniform()-0.5)*0.2*d.Half
		pts := synth(r, truth, d, night{from: pred - 1.5*d.Half, to: pred + 1.5*d.Half, step: 120.0 / 86400, noise: 0.01, depth: 0.4})
		c, ok := KvW(pts, pred, d.Half)
		if !ok {
			t.Fatalf("trial %d: no KvW minimum", i)
		}
		pulls = append(pulls, (c.T-truth)/c.Err)
	}
	within3 := 0
	for _, p := range pulls {
		if math.Abs(p) < 3 {
			within3++
		}
	}
	if share := float64(within3) / float64(len(pulls)); share < 0.9 {
		t.Errorf("KvW: only %.0f%% within 3 errors", 100*share)
	}
}

// TestTimeWrongShapeOnPartNight times nights whose dip is wider,
// narrower or rounder than the ephemeris's, the frames starting part way
// into it. Nights a timing can be trusted on must come out within 3
// errors; any other night must be refused with the reason that the
// frames missed a side or that a drift could not be told from the dip.
// Where 0.95 of the half width before the middle is covered, at least
// mostTimed of 40 nights must be timed: all of the wider dip's, but a
// rounder dip fits the ephemeris's shape best at the widest width tried,
// where its sides come back up further out, so nights whose frames fall
// short of where that fit has come a quarter of the way back up are
// refused.
func TestTimeWrongShapeOnPartNight(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		w, a      float64
		start     float64
		mostTimed int
	}{
		{"dip 30% narrower than the template", 0.03 / 1.3, 1.6, -0.6, 0},
		{"dip 30% wider than the template", 0.03 * 1.3, 1.6, -0.6, 0},
		{"rounder dip than the template", 0.03, 0.8, -0.6, 0},
		{"dip 30% wider, more of it covered", 0.03 * 1.3, 1.6, -0.95, 30},
		{"rounder dip, more of it covered", 0.03, 0.8, -0.95, 15},
	} {
		r := &rng{s: 21}
		var sum float64
		within3, doubtful, n := 0, 0, 0
		for i := range 40 {
			pred := 2461500.5 + float64(i)*testPeriod
			d := testDip(pred)
			truth := pred + (r.uniform()-0.5)*0.2*d.Half
			trueShape := func(dt float64) float64 { return period.Shape(dt/testPeriod, c.w, c.a) }
			var pts []Point
			for tt := pred + c.start*d.Half; tt <= pred+1.6*d.Half; tt += 300.0 / 86400 {
				pts = append(pts, Point{T: tt, Y: 0.4*trueShape(tt-truth) + 0.008*r.normal(), E: 0.008})
			}
			m, why, ok := Time(pts, d)
			if !ok {
				if !strings.HasPrefix(why, ReasonSides) && !strings.HasPrefix(why, ReasonDrift) && !strings.HasPrefix(why, ReasonCurve) {
					t.Errorf("%s, night %d: refused with %q", c.name, i, why)
				}
				continue
			}
			n++
			p := (m.T - truth) / m.Err
			sum += p
			if math.Abs(p) < 3 {
				within3++
			}
			if !m.Agree {
				doubtful++
			}
		}
		if n < c.mostTimed {
			t.Errorf("%s: only %d of 40 timed", c.name, n)
		}
		t.Logf("%s: %d of 40 timed, %d within 3 errors, %d doubtful", c.name, n, within3, doubtful)
		if n < 10 {
			continue
		}
		if mean := sum / float64(n); math.Abs(mean) > 1 || float64(within3)/float64(n) < 0.9 {
			t.Errorf("%s: mean pull %.2f, %d of %d within 3 errors, %d doubtful", c.name, mean, within3, n, doubtful)
		}
	}
}

func TestTimeAcrossFilters(t *testing.T) {
	t.Parallel()
	r := &rng{s: 17}
	hits, trials := 0, 60
	for i := range trials {
		pred := 2461400.5 + float64(i)*testPeriod
		d := testDip(pred)
		truth := pred + (r.uniform()-0.5)*0.3*d.Half
		var pts []Point
		for j, t := 0, pred-1.6*d.Half; t <= pred+1.6*d.Half; j, t = j+1, t+200.0/86400 {
			g := j % 3
			depth := []float64{0.45, 0.38, 0.3}[g]
			pts = append(pts, Point{T: t, Y: 0.2*float64(g) + depth*d.Shape(t-truth) + 0.012*r.normal(), E: 0.012, G: g})
		}
		m, why, ok := Time(pts, d)
		if !ok {
			t.Fatalf("trial %d: %s", i, why)
		}
		if math.Abs(m.T-truth) < 2*m.Err {
			hits++
		}
	}
	if share := float64(hits) / float64(trials); share < 0.88 {
		t.Errorf("three filters together: only %.0f%% within 2 errors", 100*share)
	}
}

func TestProbabilities(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got, want float64
	}{
		{ChiProb(3.841, 1), 0.05},
		{ChiProb(10.828, 1), 0.001},
		{ChiProb(13.816, 2), 0.001},
		{FProb(4.965, 1, 10), 0.05},
		{FProb(21.04, 1, 10), 0.001},
		{FProb(7.559, 2, 10), 0.01},
	}
	for i, c := range cases {
		if math.Abs(c.got-c.want) > 0.03*c.want {
			t.Errorf("case %d: %.5f, want %.5f", i, c.got, c.want)
		}
	}
}
