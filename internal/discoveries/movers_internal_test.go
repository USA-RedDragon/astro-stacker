package discoveries

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

func TestLinkMoversOnAnInterleavedNight(t *testing.T) {
	t.Parallel()
	const scale = 1.915
	rng := rand.New(rand.NewPCG(3, 5))
	start := time.Date(2025, 3, 1, 6, 0, 0, 0, time.UTC)
	stars := make([][2]float64, 0, 300)
	for range 300 {
		stars = append(stars, [2]float64{100 + rng.Float64()*3000, 100 + rng.Float64()*2000})
	}
	const n = 24
	times := make([]time.Time, n)
	dets := make([][]moverDet, n)
	rate := 0.5 / scale
	for j := range n {
		times[j] = start.Add(time.Duration(j) * 5 * time.Minute)
		for _, s := range stars {
			dets[j] = append(dets[j], moverDet{X: s[0] + rng.NormFloat64()*0.2, Y: s[1] + rng.NormFloat64()*0.2, Flux: 1, SNR: 10})
		}
		m := float64(j * 5)
		dets[j] = append(dets[j], moverDet{X: 1500 + rate*m + rng.NormFloat64()*0.2, Y: 900 + rng.NormFloat64()*0.2, Flux: 1, SNR: 8})
		if j%4 == 0 {
			dets[j] = append(dets[j], moverDet{X: 2500 - rate*m + rng.NormFloat64()*0.2, Y: 1400 + rate*m + rng.NormFloat64()*0.2, Flux: 0.5, SNR: 6})
		}
	}
	tracks, transient := linkMovers(times, dets, scale, 3200*2200)
	if len(tracks) != 2 {
		t.Fatalf("want the asteroid moving 1.3 px between subs 5 minutes apart and the faint one seen in every 4th sub, have %d tracks", len(tracks))
	}
	if len(tracks[0]) < n-2 || len(tracks[1]) != n/4 {
		t.Errorf("tracks have %d and %d detections", len(tracks[0]), len(tracks[1]))
	}
	for j := range n {
		static := 0
		for k := range stars {
			if !transient[j][k] {
				static++
			}
		}
		if static < len(stars)-5 {
			t.Errorf("sub %d: only %d of %d fixed sources left out as static", j, static, len(stars))
		}
	}
}

func TestCutPassesAChangeSeenInOneFilter(t *testing.T) {
	t.Parallel()
	var c cand
	c.Sign, c.SNR, c.Dipole, c.Frac = 1, 40, 0.1, 0.2
	for k := range 16 {
		f, v := "Luminance", 5.0
		if k%4 != 0 {
			f, v = []string{"Red", "Green", "Blue"}[k%4-1], -0.5
		}
		if k%4 == 0 && k == 8 {
			v = 4
		}
		c.fluxes = append(c.fluxes, v)
		c.errs = append(c.errs, 1)
		c.Light = append(c.Light, LightPoint{Filter: f})
	}
	for range 4 {
		c.fluxes = append(c.fluxes, 5)
		c.errs = append(c.errs, 1)
		c.Light = append(c.Light, LightPoint{Filter: "Luminance"})
	}
	res := &Result{Funnel: map[string]Funnel{}, ApSigma: 1}
	p := NewPlane(4, 4)
	in := &Input{}
	if all := perSub(c.fluxes, c.errs); all.reason == "" {
		t.Fatalf("the mixed subs should fail together: %+v", all)
	}
	out := in.cut([]cand{c}, p, p, localFrac{}, res)
	if len(out) != 1 {
		t.Fatalf("a change every L sub shows should pass on the L subs alone; rejected %+v", res.Rejected)
	}
	if out[0].SubsTested != 8 || out[0].PresentIn != 8 {
		t.Errorf("tested %d, present %d", out[0].SubsTested, out[0].PresentIn)
	}
}

func TestLinkMoversIgnoresChanceAlignmentsInACrowdedField(t *testing.T) {
	t.Parallel()
	const scale, n = 1.915, 15
	rng := rand.New(rand.NewPCG(9, 1))
	start := time.Date(2026, 10, 14, 2, 48, 0, 0, time.UTC)
	times := make([]time.Time, n)
	dets := make([][]moverDet, n)
	for j := range n {
		times[j] = start.Add(time.Duration(j) * 12 * time.Minute)
		for range 6000 {
			dets[j] = append(dets[j], moverDet{X: 100 + rng.Float64()*3000, Y: 100 + rng.Float64()*2000, Flux: 1, SNR: 5})
		}
		dets[j] = append(dets[j], moverDet{X: 800 + 0.3*float64(j*12), Y: 700 + 0.1*float64(j*12), Flux: 2, SNR: 9})
	}
	tracks, _ := linkMovers(times, dets, scale, 3000*2000)
	if len(tracks) != 1 {
		t.Fatalf("want the one mover among 6000 random detections a sub, have %d tracks", len(tracks))
	}
}

// TestLinkMoversChecksTheFittedRate: residuals that stay put, scattered by
// 2 px of noise, give seed pairs above the slowest rate linked and other
// subs' detections within 2.5 px of the predictions, so tracks of them fit
// rates under 0.15"/min (51 such tracks over 100 random 10-sub nights); an
// asteroid at 0.2"/min still links.
func TestLinkMoversChecksTheFittedRate(t *testing.T) {
	t.Parallel()
	const scale, n = 1.923, 10
	vmin := moverMinArcMin / scale * 1440
	start := time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)
	slow, linked := 0, 0
	for seed := uint64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewPCG(seed, 3))
		times := make([]time.Time, n)
		dets := make([][]moverDet, n)
		rate := 0.2 / scale
		for j := range n {
			times[j] = start.Add(time.Duration(j) * 6 * time.Minute)
			for k := range 30 {
				dets[j] = append(dets[j], moverDet{X: 500 + float64(k%6)*300 + rng.NormFloat64()*2, Y: 500 + float64(k/6)*300 + rng.NormFloat64()*2, SNR: 5})
			}
			dets[j] = append(dets[j], moverDet{X: 2300 + rate*float64(j*6), Y: 2300 + rate*float64(j*6), SNR: 9})
		}
		links, _ := linkMovers(times, dets, scale, 2500*2500)
		for _, hs := range links {
			var ts []time.Time
			var xs, ys []float64
			for _, h := range hs {
				ts = append(ts, times[h.sub])
				xs = append(xs, dets[h.sub][h.idx].X)
				ys = append(ys, dets[h.sub][h.idx].Y)
			}
			if _, _, _, vx, vy, _ := fitTrack(ts, xs, ys); math.Hypot(vx, vy) < vmin {
				slow++
			}
			if hs[0].idx == 30 {
				linked++
			}
		}
	}
	if slow > 0 {
		t.Errorf("%d tracks fitted a rate under 0.15\"/min", slow)
	}
	if linked != 40 {
		t.Errorf("the 0.2\"/min asteroid linked on %d of 40 nights", linked)
	}
}

// TestStarResidualIsNotAMover: the alert tests' synthetic field linked a
// "mover" of 5 detections at 0.28"/min on a G 14.8 star, every detection
// within 3 px of it, one with a negative flux: the star's subtraction
// residual, its centroid moving with each sub's seeing.
func TestStarResidualIsNotAMover(t *testing.T) {
	t.Parallel()
	pts := [][2]float64{{470.09, 1050.80}, {472.31, 1049.60}, {470.03, 1050.40}, {467.54, 1050.34}, {467.53, 1049.11}}
	var tr Track
	for _, p := range pts {
		tr.Points = append(tr.Points, TrackPoint{X: p[0], Y: p[1]})
	}
	stars := NewGrid([][2]float64{{470.0, 1050.8}, {900, 900}}, 8)
	if !onBrightStars(tr, stars) {
		t.Error("a track whose detections all sit within 3 px of one bright star is its residual")
	}
	moving := Track{Points: []TrackPoint{{X: 465, Y: 1050}, {X: 470, Y: 1050}, {X: 475, Y: 1050}, {X: 480, Y: 1050}}}
	if onBrightStars(moving, stars) {
		t.Error("an asteroid passing a star is not the star's residual")
	}
	pair := NewGrid([][2]float64{{100, 100}, {104, 102.7}}, 8)
	glow := Track{Points: []TrackPoint{{X: 97.2, Y: 98.1}, {X: 97.4, Y: 98.3}, {X: 102, Y: 101.3}, {X: 102.2, Y: 101.4}, {X: 106.8, Y: 104.6}}}
	if !onBrightStars(glow, pair) {
		t.Error("a track along the glow of a close bright pair, 3.2 px off each star, is their residual")
	}
	d := NewPlane(64, 64)
	gauss := func(x0, y0, amp, sigma float64) {
		for y := range 64 {
			for x := range 64 {
				dx, dy := float64(x)-x0, float64(y)-y0
				d.Data[y*64+x] += float32(amp * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
			}
		}
	}
	gauss(20, 20, 1, 1.4)
	gauss(44, 44, 1, 2.6)
	gauss(44, 44, -2, 1.0)
	rms := make([]float32, 64*64)
	for i := range rms {
		rms[i] = 0.01
	}
	none := make([]bool, 64*64)
	ring := 0
	for _, o := range Extract(d, rms, nil, DetectSigma, 3) {
		if Aperture(d, o.X, o.Y, apertureR) <= 0 {
			ring++
		}
	}
	if ring == 0 {
		t.Fatal("the residual left no detection with a negative aperture flux to test")
	}
	dets := moverDetections(d, rms, none, none, 0.05, math.Inf(1))
	for _, m := range dets {
		if !(m.Flux > 0) {
			t.Errorf("a detection with no light in its aperture was kept: %+v", m)
		}
	}
	if len(dets) == 0 || math.Hypot(dets[0].X-20, dets[0].Y-20) > 0.5 {
		t.Errorf("the star-like source was not kept: %+v", dets)
	}
}

func TestMoverNullTestLeavesTheLinkedTracksOut(t *testing.T) {
	t.Parallel()
	const scale, n = 1.923, 20
	start := time.Date(2025, 3, 1, 6, 0, 0, 0, time.UTC)
	times := make([]time.Time, n)
	dets := make([][]moverDet, n)
	for j := range n {
		times[j] = start.Add(time.Duration(j) * 5 * time.Minute)
		dets[j] = []moverDet{{X: 1500 + 0.26*float64(j*5), Y: 900 + 0.1*float64(j*5), Flux: 1, SNR: 9}}
	}
	links, _ := linkMovers(times, dets, scale, 3000*2000)
	if len(links) != 1 {
		t.Fatalf("the asteroid did not link: %d tracks", len(links))
	}
	if null := nullMovers(times, dets, links, scale, 3000*2000); null.Tracks != 0 || null.MaxHits != 0 {
		t.Errorf("the null test counted the linked asteroid: %+v", null)
	}
	tr := Track{T0: start, X0: 1500, Y0: 900, VX: 0.26 * 1440, VY: 0.1 * 1440}
	for j := range n {
		x, y := tr.Position(times[j])
		tr.Points = append(tr.Points, TrackPoint{Time: times[j], X: x + 0.1*float64(j%3-1), Y: y, SNR: 9})
	}
	if chi := trackChi(tr, 1.4, 0.1); chi > 1 {
		t.Errorf("chi %.2f for detections 0.1 px off", chi)
	}
	tr.Points[5].X += 4
	if chi := trackChi(tr, 1.4, 0.1); chi <= maxTrackChi {
		t.Errorf("chi %.2f with a detection 4 px off a 9 sigma track", chi)
	}
}
