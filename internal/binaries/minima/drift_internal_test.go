package minima

import (
	"math"
	"strings"
	"testing"
)

// curvedNight is a night on the test dip whose light drifts along a
// parabola of curve mag per ephemeris half width squared about tc, with
// a straight line slope mag a day, frames from start to end half widths
// about the prediction every five minutes, with noise mag of white noise.
func curvedNight(r *rng, d Dip, truth, tc, curve, slope, start, end, noise float64) []Point {
	var out []Point
	for t := d.Mid + start*d.Half; t <= d.Mid+end*d.Half; t += 300.0 / 86400 {
		x := (t - tc) / d.Half
		y := 0.3 + 0.4*d.Shape(t-truth) + curve*x*x + slope*(t-d.Mid) + noise*r.normal()
		out = append(out, Point{T: t, Y: y, E: noise})
	}
	return out
}

// tally is how a set of nights came out: timed, refused for the drift,
// and timed wrong (over 3 errors from the truth) yet marked good.
type tally struct {
	n, timed, good, drift, wrongGood int
	errSum                           float64
}

func (c *tally) add(m Minimum, why string, ok bool, truth float64) {
	c.n++
	if !ok {
		if strings.HasPrefix(why, ReasonCurve) {
			c.drift++
		}
		return
	}
	c.timed++
	c.errSum += m.Err
	if m.Agree {
		c.good++
		if math.Abs(m.T-truth) > 3*m.Err {
			c.wrongGood++
		}
	}
}

func (c *tally) meanErrMin() float64 {
	if c.timed == 0 {
		return 0
	}
	return c.errSum / float64(c.timed) * 1440
}

// TestTimeCurvedDriftOnPartNights times nights whose light drifts along
// a parabola the size of an airmass curve, the frames covering one side
// of the dip only part way and the noise low: at most 1 of 60 may come
// out wrong yet marked good.
func TestTimeCurvedDriftOnPartNights(t *testing.T) {
	t.Parallel()
	r := &rng{s: 41}
	var c tally
	for i := range 60 {
		pred := 2461700.5 + float64(i)*testPeriod
		d := testDip(pred)
		truth := pred + (r.uniform()-0.5)*0.2*d.Half
		tc := truth + (r.uniform()-0.5)*2*d.Half
		curve := driftHalfCurve * (0.75 + r.uniform()/4)
		if i%2 == 0 {
			curve = -curve
		}
		start, end := -0.4-0.55*r.uniform(), 1.8
		if i%4 >= 2 {
			start, end = -1.8, 0.4+0.55*r.uniform()
		}
		m, why, ok := Time(curvedNight(r, d, truth, tc, curve, 0, start, end, 0.003), d)
		c.add(m, why, ok, truth)
	}
	t.Logf("curved part nights: %d timed, %d good, %d refused for the drift, %d wrong yet good, mean error %.2f min", c.timed, c.good, c.drift, c.wrongGood, c.meanErrMin())
	if c.wrongGood > 1 {
		t.Errorf("%d of %d curved part nights wrong yet marked good", c.wrongGood, c.n)
	}
}

// TestTimeEvenNightsKeepTheirErrors times flat and sloping nights
// covering the dip evenly: the drift systematic must refuse none of
// them and add little to their errors.
func TestTimeEvenNightsKeepTheirErrors(t *testing.T) {
	t.Parallel()
	r := &rng{s: 43}
	var c tally
	var grow []float64
	ns := make([]nightCase, 0, 60)
	for i := range 60 {
		pred := 2461900.5 + float64(i)*testPeriod
		d := testDip(pred)
		truth := pred + (r.uniform()-0.5)*0.2*d.Half
		slope := 0.0
		if i%2 == 1 {
			slope = 0.3 * (r.uniform() - 0.5)
		}
		ns = append(ns, nightCase{curvedNight(r, d, truth, truth, 0, slope, -1.8, 1.8, 0.008), d, truth})
	}
	for i, res := range timeNights(ns) {
		m, why, ok, truth := res.m, res.why, res.ok, ns[i].truth
		c.add(m, why, ok, truth)
		if ok {
			stat := math.Max(m.Formal*math.Max(1, m.Red), m.Bead)
			grow = append(grow, m.Err/stat-1)
			if m.Drift > stat {
				t.Errorf("night %d: drift systematic %.2f min over the statistical error %.2f min", i, m.Drift*1440, stat*1440)
			}
		}
	}
	t.Logf("even nights: %d of %d timed, %d good, %d refused for the drift, mean error %.2f min, mean error growth %.1f%%", c.timed, c.n, c.good, c.drift, c.meanErrMin(), 100*mean(grow))
	if c.drift > 0 || c.timed < c.n-1 {
		t.Errorf("even nights: %d timed of %d, %d refused for the drift", c.timed, c.n, c.drift)
	}
	if g := mean(grow); g > 0.05 {
		t.Errorf("even nights' errors grew %.1f%% from the drift systematic", 100*g)
	}
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

const driftHalfCurve = 0.004
