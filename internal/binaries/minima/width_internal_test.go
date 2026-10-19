package minima

import (
	"fmt"
	"math"
	"sync"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// verdicts counts how a set of nights with a known middle came out.
type verdicts struct {
	mu sync.Mutex

	good, wrong, doubt, doubtWrong, no int
}

func (v *verdicts) add(m Minimum, ok bool, truth float64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !ok {
		v.no++
		return
	}
	off := math.Abs(m.T-truth) > 3*m.Err
	switch {
	case m.Agree && off:
		v.wrong++
	case m.Agree:
		v.good++
	case off:
		v.doubtWrong++
	default:
		v.doubt++
	}
}

func (v *verdicts) timed() int { return v.good + v.wrong + v.doubt + v.doubtWrong }

func (v *verdicts) String() string {
	return fmt.Sprintf("%d good, %d wrong but good, %d doubtful, %d wrong and doubtful, %d refused", v.good, v.wrong, v.doubt, v.doubtWrong, v.no)
}

// widthNight is a night on a dip of shape a and ratio times the
// ephemeris's width, its frames every 5 minutes from start half widths
// of the ephemeris's dip before the prediction to 1.6 after.
func widthNight(r *rng, d Dip, truth, a, ratio, start, noise float64) []Point {
	shape := func(dt float64) float64 { return period.Shape(dt/testPeriod, 0.03*ratio, a) }
	var pts []Point
	for t := d.Mid + start*d.Half; t <= d.Mid+1.6*d.Half; t += 300.0 / 86400 {
		pts = append(pts, Point{T: t, Y: 0.4*shape(t-truth) + noise*r.normal(), E: noise})
	}
	return pts
}

func shapedDip(mid, a float64) Dip {
	return Dip{Mid: mid, Half: 0.03 * testPeriod, Shape: func(dt float64) float64 { return period.Shape(dt/testPeriod, 0.03, a) }}
}

// TestTimeAcrossDipWidths times nights whose dip is 0.5 to 2.5 times as
// wide as the ephemeris's, round, mean and sharp, against an ephemeris
// of the same shape or of the mean one, with the frames over the whole
// dip or starting part way into it. A timing more than 3 errors off the
// truth must not pass as good: by chance alone about 0.3% of right
// timings land that far off, so 1% of all the timings are allowed, and
// no more than one night of any one kind. Where the frames cover a dip
// no wider than 1.5 times the ephemeris's of its own shape whole, at
// least 4 of 6 nights must be timed.
func TestTimeAcrossDipWidths(t *testing.T) {
	t.Parallel()
	var all verdicts
	var wg sync.WaitGroup
	for _, sh := range [][2]float64{{0.8, 0.8}, {1.6, 1.6}, {8, 8}, {0.8, 1.6}, {8, 1.6}} {
		a, tmpl := sh[0], sh[1]
		for _, ratio := range []float64{0.5, 0.75, 1, 1.5, 2, 2.5} {
			for _, start := range []float64{-1.6, -0.95, -0.6} {
				wg.Go(func() {
					var v verdicts
					r := &rng{s: uint64(1000*a + 300*tmpl + 100*ratio - 10*start)}
					for i := range 6 {
						d := shapedDip(2461500.5+float64(i)*testPeriod, tmpl)
						truth := d.Mid + (r.uniform()-0.5)*0.2*d.Half
						m, _, ok := Time(widthNight(r, d, truth, a, ratio, start, 0.01+0.02*r.uniform()), d)
						v.add(m, ok, truth)
						all.add(m, ok, truth)
					}
					name := fmt.Sprintf("shape %.1f against %.1f, %.2f times as wide, frames from %.2f", a, tmpl, ratio, start)
					if v.wrong > 1 {
						t.Errorf("%s: %s", name, &v)
					}
					if a == tmpl && start < -1.5 && ratio <= 1.5 && v.timed() < 4 {
						t.Errorf("%s: only %d of 6 timed: %s", name, v.timed(), &v)
					}
				})
			}
		}
	}
	wg.Wait()
	if float64(all.wrong) > 0.01*float64(all.timed()) {
		t.Errorf("all nights: %s", &all)
	}
	t.Logf("all nights: %s", &all)
}
