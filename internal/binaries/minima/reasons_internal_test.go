package minima

import (
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// deepNight is a night on a deep, narrow Algol dip like source 618's
// (P 2.256 d, 2 mag deep), two half widths either side of the
// prediction: fluxes with noise fnoise of the normal flux, frames whose
// flux comes out at or below 0 lost, every other frame in a second
// filter whose points come from second.
func deepNight(r *rng, truth float64, d Dip, fnoise float64, second func(dt float64) (float64, float64, bool)) []Point {
	var out []Point
	i := 0
	for t := d.Mid - 2*d.Half; t <= d.Mid+2*d.Half; t += 120.0 / 86400 {
		i++
		if second != nil && i%2 == 0 {
			if y, e, ok := second(t - truth); ok {
				out = append(out, Point{T: t, Y: y, E: e, G: 1})
			}
			continue
		}
		f := math.Pow(10, -0.4*2*d.Shape(t-truth)) + fnoise*r.normal()
		if f <= 0 {
			continue
		}
		out = append(out, Point{T: t, Y: -2.5 * math.Log10(f), E: 1.0857 * fnoise / f})
	}
	return out
}

func deepDip(mid float64) Dip {
	const p, w = 2.256397, 0.03
	return Dip{Mid: mid, Half: w * p, Shape: func(dt float64) float64 { return period.Shape(dt/p, w, 1) }}
}

func pullsOf(t *testing.T, name string, night func(r *rng, truth float64, d Dip) []Point) {
	t.Helper()
	r := &rng{s: 29}
	timed, within := 0, 0
	const trials = 30
	for i := range trials {
		d := deepDip(2461000.5 + float64(i)*2.256397)
		truth := d.Mid + (r.uniform()-0.5)*0.4*d.Half
		m, why, ok := Time(night(r, truth, d), d)
		if !ok {
			t.Errorf("%s, trial %d: not timed: %s", name, i, why)
			continue
		}
		timed++
		if math.Abs(m.T-truth) < 3*m.Err {
			within++
		}
	}
	if timed > 0 && float64(within)/float64(timed) < 0.9 {
		t.Errorf("%s: only %d of %d timings within 3 errors of the truth", name, within, timed)
	}
}

func TestTimeDeepDipWithLostFrames(t *testing.T) {
	t.Parallel()
	pullsOf(t, "2 mag dip, faint star", func(r *rng, truth float64, d Dip) []Point {
		return deepNight(r, truth, d, 0.3, nil)
	})
}

func TestTimeIgnoresFilterTheStarIsLostIn(t *testing.T) {
	t.Parallel()
	pullsOf(t, "second filter only noise", func(r *rng, truth float64, d Dip) []Point {
		return deepNight(r, truth, d, 0.05, func(float64) (float64, float64, bool) { return 0.3 * r.normal(), 0.3, true })
	})
}

func TestTimeFilterWithoutFramesInDip(t *testing.T) {
	t.Parallel()
	pullsOf(t, "second filter only before the dip", func(r *rng, truth float64, d Dip) []Point {
		return deepNight(r, truth, d, 0.05, func(dt float64) (float64, float64, bool) {
			return 0.02 * r.normal(), 0.02, dt < -1.2*d.Half
		})
	})
}

func TestTimeFilterWithoutBaseline(t *testing.T) {
	t.Parallel()
	pullsOf(t, "second filter only inside the dip", func(r *rng, truth float64, d Dip) []Point {
		return deepNight(r, truth, d, 0.05, func(dt float64) (float64, float64, bool) {
			return 2.3*d.Shape(dt) + 0.05*r.normal(), 0.05, math.Abs(dt) < 0.6*d.Half
		})
	})
}

func TestTimeShallowDipLikeSource1688(t *testing.T) {
	t.Parallel()
	const p, w = 6.78575, 0.02
	r := &rng{s: 31}
	timed, within := 0, 0
	for i := range 30 {
		mid := 2461000.5 + float64(i)*p
		d := Dip{Mid: mid, Half: w * p, Shape: func(dt float64) float64 { return period.Shape(dt/p, w, 1) }}
		truth := mid + (r.uniform()-0.5)*0.4*d.Half
		pts := synth(r, truth, d, night{from: mid - 2*d.Half, to: mid + 2*d.Half, step: 180.0 / 86400, noise: 0.04, depth: 0.18})
		for i := range pts {
			pts[i].Y -= 0.3
			pts[i].G = i % 2
		}
		m, why, ok := Time(pts, d)
		if !ok {
			t.Errorf("trial %d: not timed: %s", i, why)
			continue
		}
		timed++
		if math.Abs(m.T-truth) < 3*m.Err {
			within++
		}
	}
	if float64(within) < 0.9*float64(timed) {
		t.Errorf("only %d of %d within 3 errors", within, timed)
	}
}

func TestTimeSaysWhyNoDip(t *testing.T) {
	t.Parallel()
	r := &rng{s: 37}
	for i := range 40 {
		d := deepDip(2461000.5 + float64(i)*2.256397)
		var pts []Point
		for j, tt := 0, d.Mid-2*d.Half; tt <= d.Mid+2*d.Half; j, tt = j+1, tt+120.0/86400 {
			pts = append(pts, Point{T: tt, Y: 0.03 * r.normal(), E: 0.03, G: j % 2})
		}
		_, why, ok := Time(pts, d)
		if ok {
			t.Errorf("trial %d: a night with no dip was timed", i)
			continue
		}
		if !strings.HasPrefix(why, ReasonNoDip) && !strings.HasPrefix(why, ReasonShallow) {
			t.Errorf("trial %d: no dip, reason %q", i, why)
		}
		if !strings.Contains(why, "mag") {
			t.Errorf("trial %d: reason %q gives no depth", i, why)
		}
	}
}
