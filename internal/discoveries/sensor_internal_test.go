package discoveries

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

func TestHotPixelWalkedByTheRegistrationIsNotAMover(t *testing.T) {
	t.Parallel()
	// track14202 is an unknown mover of Cygnis Loop Panel 2's 2026-10-07
	// broadband night (run 24): a saturated hot pixel the registration walked
	// 17 px across the sky. The footprints of its six subs put all six points
	// on one sensor pixel, (648.2, 2113.6) up to the frame's orientation.
	track14202 := []struct {
		t    string
		x, y float64
	}{
		{"2026-10-08T03:23:45.822Z", 657.158, 2119.503},
		{"2026-10-08T03:28:55.84Z", 658.469, 2119.052},
		{"2026-10-08T03:34:07.861Z", 658.836, 2117.729},
		{"2026-10-08T04:10:03.839Z", 663.437, 2106.316},
		{"2026-10-08T04:15:13.798Z", 664.374, 2105.786},
		{"2026-10-08T04:20:25.797Z", 665.033, 2104.441},
	}
	const scale = 1.923
	times := make([]time.Time, len(track14202))
	dets := make([][]moverDet, len(track14202))
	for j, p := range track14202 {
		tm, err := time.Parse(time.RFC3339, p.t)
		if err != nil {
			t.Fatal(err)
		}
		times[j] = tm
		dx, dy := p.x-648.2, p.y-2113.6
		m := float64(j) * 1.9
		dets[j] = []moverDet{
			{X: p.x, Y: p.y, Flux: 1, SNR: 2000, SX: 648.2, SY: 2113.6, Sensor: true},
			{X: 3000 + 1.5*m, Y: 1000 - 0.4*m, Flux: 1, SNR: 9, SX: 3000 + 1.5*m - dx, SY: 1000 - 0.4*m - dy, Sensor: true},
		}
	}
	tracks, _ := linkMovers(times, dets, scale, 6000*4000)
	if len(tracks) != 1 || tracks[0][0].idx != 1 {
		t.Fatalf("want only the asteroid linked, not the hot pixel: %v", tracks)
	}
	for j := range dets {
		dets[j][0].Sensor, dets[j][1].Sensor = false, false
	}
	if tracks, _ := linkMovers(times, dets, scale, 6000*4000); len(tracks) != 2 {
		t.Errorf("without sensor pixels the hot pixel links as a mover, as it did: %d tracks", len(tracks))
	}
}

func TestFootprintFindsTheSubsShiftAndRotation(t *testing.T) {
	t.Parallel()
	const w, h = 1200, 800
	for _, c := range []struct{ theta, u, v float64 }{{0.0115, 31.9, -34.5}, {-0.004, -12, 20}, {0, 0, 0}} {
		p := NewPlane(w, h)
		for y := range h {
			for x := range w {
				fx, fy := float64(x), float64(y)
				sx, sy := fx+c.theta*fy-c.u, fy-c.theta*fx-c.v
				if sx < 0 || sy < 0 || sx > w-1 || sy > h-1 {
					p.Data[y*w+x] = float32(math.NaN())
				} else {
					p.Data[y*w+x] = 1
				}
			}
		}
		m := footprint(p)
		if !m.ok || math.Abs(m.theta-c.theta) > 2e-4 || math.Abs(m.u-c.u) > 1 || math.Abs(m.v-c.v) > 1 {
			t.Errorf("want %+v, have %+v", c, m)
		}
		for _, q := range [][2]float64{{100, 100}, {1100, 700}, {600, 50}} {
			sx, sy := m.at(q[0], q[1])
			wx, wy := q[0]+c.theta*q[1]-c.u, q[1]-c.theta*q[0]-c.v
			if math.Hypot(sx-wx, sy-wy) > 1 {
				t.Errorf("%+v: sensor (%.1f, %.1f), want (%.1f, %.1f)", c, sx, sy, wx, wy)
			}
		}
	}
}

func TestSlowAsteroidOnDitheredSubsStaysTransient(t *testing.T) {
	t.Parallel()
	const scale, n = 1.923, 40
	rng := rand.New(rand.NewPCG(7, 11))
	start := time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)
	times := make([]time.Time, n)
	dets := make([][]moverDet, n)
	for j := range n {
		times[j] = start.Add(time.Duration(j) * 5 * time.Minute)
		dx, dy := rng.NormFloat64()*4, rng.NormFloat64()*4
		ax, ay := 2000+0.1*float64(j*5), 1500.0
		hx, hy := 800+dx, 900+dy
		dets[j] = []moverDet{
			{X: ax, Y: ay, SX: ax - dx, SY: ay - dy, Sensor: true},
			{X: hx, Y: hy, SX: 800, SY: 900, Sensor: true},
		}
	}
	_, lab, transient := transients(times, dets, scale)
	static := 0
	for j := range n {
		if lab[2*j] != j {
			t.Fatalf("detection %d labelled sub %d", 2*j, lab[2*j])
		}
		if !transient[2*j] {
			static++
		}
		if transient[2*j+1] {
			t.Errorf("sub %d: the hot pixel was taken for transient", j)
		}
	}
	t.Logf("%d of %d asteroid detections static", static, n)
	if static > n/10 {
		t.Errorf("%d of %d detections of a 0.1 px/min asteroid on dithered subs left out as static", static, n)
	}
}
