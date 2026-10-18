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
		if !m.OK || math.Abs(m.B-c.theta) > 2e-4 || math.Abs(-m.C-c.u) > 1 || math.Abs(-m.F-c.v) > 1 {
			t.Errorf("want %+v, have %+v", c, m)
		}
		for _, q := range [][2]float64{{100, 100}, {1100, 700}, {600, 50}} {
			sx, sy := m.At(q[0], q[1])
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

// sensorW and sensorH are the size of the synthetic sensor.
const sensorW, sensorH = 1600, 1100

// trueRegistration is a sub's registration as Siril's homography makes
// it on a night of field rotation: the registered frame turned by theta
// about the sensor's centre, scaled by sx and sy and shifted by (u, v) on
// the sensor.
func trueRegistration(theta, sx, sy, u, v float64) SensorMap {
	const cx, cy = sensorW / 2, sensorH / 2
	c, s := math.Cos(theta), math.Sin(theta)
	m := SensorMap{A: sx * c, B: sx * s, D: -sy * s, E: sy * c, OK: true, Measured: true}
	m.C = cx + u - m.A*cx - m.B*cy
	m.F = cy + v - m.D*cx - m.E*cy
	return m
}

// inverse is the registered pixel over the sensor pixel (sx, sy).
func (m SensorMap) inverse(sx, sy float64) (x, y float64) {
	det := m.A*m.E - m.B*m.D
	dx, dy := sx-m.C, sy-m.F
	return (m.E*dx - m.B*dy) / det, (m.A*dy - m.D*dx) / det
}

// registeredPair renders a star field on the sensor and the same field
// registered with m, NaN where the sensor does not reach.
func registeredPair(m SensorMap, seed uint64) (reg, cal *Plane) {
	const w, h = sensorW, sensorH
	rng := rand.New(rand.NewPCG(seed, 3))
	cal, reg = NewPlane(w, h), NewPlane(w, h)
	for range 500 {
		x, y := rng.Float64()*float64(w), rng.Float64()*float64(h)
		f := 2000 * math.Exp(3*rng.Float64())
		addGauss(cal.Data, w, h, x, y, f, 1.6)
		rx, ry := m.inverse(x, y)
		addGauss(reg.Data, w, h, rx, ry, f, 1.6)
	}
	for i := range cal.Data {
		cal.Data[i] += float32(100 + 3*rng.NormFloat64())
		reg.Data[i] += float32(100 + 3*rng.NormFloat64())
		sx, sy := m.At(float64(i%w), float64(i/w))
		if sx < 0 || sy < 0 || sx > float64(w-1) || sy > float64(h-1) {
			reg.Data[i] = float32(math.NaN())
		}
	}
	return reg, cal
}

func TestFitSensorFindsTheRegistrationsScaleAndAnyTurn(t *testing.T) {
	t.Parallel()
	const w, h, cx, cy = sensorW, sensorH, sensorW / 2, sensorH / 2
	for i, m := range []SensorMap{
		trueRegistration(0.0027, 1.0012, 1.0010, 31.9, -34.5),
		trueRegistration(-0.0011, 1.0009, 1.0013, -12, 20),
		trueRegistration(math.Pi-0.0008, 0.9991, 0.9990, 6, -4),
		trueRegistration(2.424, 1.0011, 1.0011, -40, 25),
	} {
		reg, cal := registeredPair(m, uint64(i)+1)
		fp := footprint(reg)
		fit, rms, n, ok := FitSensor(reg, cal)
		if !ok {
			t.Fatalf("case %d: no fit, %d stars at %.2f px", i, n, rms)
		}
		var worst, worstFP float64
		for _, q := range [][2]float64{{50, 50}, {w - 50, 50}, {50, h - 50}, {w - 50, h - 50}, {cx, cy}} {
			wx, wy := m.At(q[0], q[1])
			fx, fy := fit.At(q[0], q[1])
			worst = math.Max(worst, math.Hypot(fx-wx, fy-wy))
			px, py := fp.At(q[0], q[1])
			worstFP = math.Max(worstFP, math.Hypot(px-wx, py-wy))
		}
		t.Logf("case %d: %d stars, rms %.3f px, off by up to %.3f px, the footprint by %.1f px", i, n, rms, worst, worstFP)
		if worst > 0.1 {
			t.Errorf("case %d: the fitted map is off by up to %.3f px", i, worst)
		}
	}
}

func TestHotPixelWalkedByRotationAndScaleIsNotAMover(t *testing.T) {
	t.Parallel()
	// A night that turns 0.0027 rad and drifts 9 px, its subs registered
	// with scales of 1.0008 to 1.0015, walks a hot pixel at constant rate
	// across the sky. Each sub's map, fitted to its stars as it is
	// registered, keeps the hot pixel on one sensor pixel.
	const w, h, scale, n = sensorW, sensorH, 1.915, 12
	rng := rand.New(rand.NewPCG(5, 9))
	start := time.Date(2026, 2, 16, 8, 0, 0, 0, time.UTC)
	hot := [2]float64{1500, 1020}
	times := make([]time.Time, n)
	dets := make([][]moverDet, n)
	for j := range n {
		times[j] = start.Add(time.Duration(j) * 10 * time.Minute)
		m := trueRegistration(0.0027*float64(j)/(n-1), 1.0008+0.0007*rng.Float64(), 1.0008+0.0005*rng.Float64(),
			-0.8*float64(j)+0.3*rng.NormFloat64(), -0.4*float64(j)+0.3*rng.NormFloat64())
		reg, cal := registeredPair(m, uint64(j)+20)
		fit, _, _, ok := FitSensor(reg, cal)
		if !ok {
			t.Fatalf("sub %d: no fit", j)
		}
		hx, hy := m.inverse(hot[0], hot[1])
		ax, ay := 500+0.5*float64(j*10), 400-0.2*float64(j*10)
		for _, d := range [][2]float64{{hx, hy}, {ax, ay}} {
			sx, sy := fit.At(d[0], d[1])
			dets[j] = append(dets[j], moverDet{X: d[0], Y: d[1], Flux: 1, SNR: 20, SX: sx, SY: sy, Sensor: true})
		}
	}
	if d := math.Hypot(dets[n-1][0].X-dets[0][0].X, dets[n-1][0].Y-dets[0][0].Y); d < 8 {
		t.Fatalf("the hot pixel walked only %.1f px", d)
	}
	tracks, _ := linkMovers(times, dets, scale, w*h)
	if len(tracks) != 1 || tracks[0][0].idx != 1 {
		t.Fatalf("want only the asteroid linked, not the hot pixel: %v", tracks)
	}
	for j := range dets {
		dets[j][0].Sensor, dets[j][1].Sensor = false, false
	}
	if tracks, _ := linkMovers(times, dets, scale, w*h); len(tracks) != 2 {
		t.Errorf("without sensor pixels the hot pixel links as a mover: %d tracks", len(tracks))
	}
}

func TestMarkarianChainHotPixelsAreNotMovers(t *testing.T) {
	t.Parallel()
	// Unknown movers 27939 and 54782 of Markarian Chain Panel 6's
	// 2026-02-15 night were master-dark hot pixels at sensor (1399, 382) and
	// (78, 325), walked across the sky by the night's field rotation. fit is
	// each sub's map fitted to its registered and raw frames' stars, foot
	// the footprint's, which put them up to 6 px apart on the sensor.
	night := []string{"08:07:41", "08:12:49", "08:18:01", "08:33:23", "08:38:31", "08:43:43", "08:59:05", "09:04:13", "09:09:25",
		"09:14:53", "09:24:19", "09:29:31", "09:34:43", "09:39:51", "09:45:03", "09:50:15", "09:55:23", "10:00:35", "10:05:57",
		"10:11:05", "10:16:17", "10:21:29", "11:16:39", "11:21:47", "11:26:59", "11:32:27", "11:37:35", "11:42:47", "11:47:59"}
	type point struct {
		sub       int
		x, y      float64
		fit, foot [6]float64
	}
	tracks := [][]point{{
		{10, 1396.32, 374.14, [6]float64{0.9988992, 0.0004024997, 3.989904, -0.0003049149, 0.9988521, 8.748185}, [6]float64{1, 0.0002400579, 0, -0.0002400579, 1, 4.244499}},
		{13, 1395.24, 376.56, [6]float64{0.9988935, 0.001135271, 4.651114, -0.001047541, 0.9988462, 7.654742}, [6]float64{1, 0.000971077, -2.077089, -0.000971077, 1, 3.236878}},
		{14, 1395.63, 377.24, [6]float64{0.998958, 0.00137348, 4.314275, -0.001291369, 0.9989191, 7.283404}, [6]float64{1, 0.001253842, -2.244188, -0.001253842, 1, 3.242607}},
		{16, 1394.09, 378.64, [6]float64{0.9988774, 0.001835, 5.918586, -0.001765543, 0.9988353, 6.59202}, [6]float64{1, 0.0009421915, 0.809203, -0.0009421915, 1, 2.417317}},
		{18, 1393.28, 380.80, [6]float64{0.9990688, 0.002321626, 5.948672, -0.002267395, 0.999036, 4.776406}, [6]float64{1, 0.00228939, -0.264653, -0.00228939, 1, 5.349875}},
		{21, 1391.89, 382.65, [6]float64{0.9990497, 0.002982254, 7.365487, -0.002942161, 0.9990254, 3.827321}, [6]float64{1, 0.002963887, 0.9899326, -0.002963887, 1, 4.386962}},
	}, {
		{11, 74.34, 317.21, [6]float64{0.9989698, 0.0006559242, 3.377098, -0.0005563159, 0.9989271, 8.392387}, [6]float64{1, 0.0005559473, 0, -0.0005559473, 1, 4.374761}},
		{12, 76.29, 317.75, [6]float64{0.9991023, 0.0009043746, 1.400422, -0.0008210097, 0.9990604, 7.727288}, [6]float64{1, 0.0007905248, 0, -0.0007905248, 1, 4.252564}},
		{14, 73.34, 318.39, [6]float64{0.998958, 0.00137348, 4.314275, -0.001291369, 0.9989191, 7.283404}, [6]float64{1, 0.001253842, -2.244188, -0.001253842, 1, 3.242607}},
		{17, 72.22, 319.35, [6]float64{0.9989397, 0.002072772, 5.553176, -0.002002627, 0.9989038, 6.231609}, [6]float64{1, 0.001263813, 0.5333334, -0.001263813, 1, 2.776456}},
		{18, 71.28, 320.75, [6]float64{0.9990688, 0.002321626, 5.948672, -0.002267395, 0.999036, 4.776406}, [6]float64{1, 0.00228939, -0.264653, -0.00228939, 1, 5.349875}},
		{20, 67.91, 321.30, [6]float64{0.9989263, 0.002766792, 9.340803, -0.00271398, 0.9988906, 4.51814}, [6]float64{1, 0.002726704, 2.23612, -0.002726704, 1, 5.012953}},
	}}
	const scale = 1.915
	times := make([]time.Time, len(night))
	for j, s := range night {
		tm, err := time.Parse(time.RFC3339, "2026-02-16T"+s+"Z")
		if err != nil {
			t.Fatal(err)
		}
		times[j] = tm
	}
	link := func(fitted bool) [][]hit {
		dets := make([][]moverDet, len(night))
		for _, tr := range tracks {
			for _, p := range tr {
				c := p.foot
				if fitted {
					c = p.fit
				}
				m, _ := SensorMapFrom(c[:])
				sx, sy := m.At(p.x, p.y)
				dets[p.sub] = append(dets[p.sub], moverDet{X: p.x, Y: p.y, Flux: 1, SNR: 6, SX: sx, SY: sy, Sensor: true})
			}
		}
		links, _ := linkMovers(times, dets, scale, 6248*4176)
		return links
	}
	if links := link(false); len(links) != 2 {
		t.Errorf("mapped by their footprints both hot pixels link, as they did: %d tracks", len(links))
	}
	if links := link(true); len(links) != 0 {
		t.Errorf("mapped by their registrations the hot pixels link as %d movers", len(links))
	}
}
