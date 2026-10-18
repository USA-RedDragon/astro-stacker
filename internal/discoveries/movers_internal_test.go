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
	if !onBrightStars(tr, stars, starResidualPx) {
		t.Error("a track whose detections all sit within 3 px of one bright star is its residual")
	}
	moving := Track{Points: []TrackPoint{{X: 465, Y: 1050}, {X: 470, Y: 1050}, {X: 475, Y: 1050}, {X: 480, Y: 1050}}}
	if onBrightStars(moving, stars, starResidualPx) {
		t.Error("an asteroid passing a star is not the star's residual")
	}
	pair := NewGrid([][2]float64{{100, 100}, {104, 102.7}}, 8)
	glow := Track{Points: []TrackPoint{{X: 97.2, Y: 98.1}, {X: 97.4, Y: 98.3}, {X: 102, Y: 101.3}, {X: 102.2, Y: 101.4}, {X: 106.8, Y: 104.6}}}
	if !onBrightStars(glow, pair, starResidualPx) {
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

func TestStarResidualsNearTheNightsDepthAreNotMovers(t *testing.T) {
	t.Parallel()
	// Unknown mover 58459 of M 78's 2025-11-24: five detections 2.8 to 3.3 px
	// from a G 15.02 star, the first 8.6 px from it. 67212 of California
	// Nebula Panel 1's 2026-10-06 sat within 0.4 px of a G 17.47 star, then
	// of a G 17.3 one, under the G 17 limit but 1 mag above its subs' depth
	// of 18.49.
	m78Depth, m78 := 18.19, &Result{HFRNight: 1.672}
	m78.SubDepth = &m78Depth
	maxG, r := residualStars(m78)
	if math.Abs(maxG-17.19) > 1e-9 || math.Abs(r-6.688) > 1e-9 {
		t.Fatalf("M 78's residual stars: G %.2f within %.2f px", maxG, r)
	}
	star := NewGrid([][2]float64{{903.88, 1163.92}}, 8)
	var tr58459 Track
	for _, p := range [][2]float64{{895.88, 1167.0}, {903.70, 1166.70}, {906.37, 1165.37}, {906.09, 1165.80}, {906.62, 1165.78}, {906.19, 1165.67}} {
		tr58459.Points = append(tr58459.Points, TrackPoint{X: p[0], Y: p[1]})
	}
	if i, _ := star.Nearest(895.88, 1167.0, starResidualPx); i >= 0 {
		t.Fatal("58459's first detection is 8.6 px from the star, out of the old rule's 5 px")
	}
	if !onBrightStars(tr58459, star, r) {
		t.Error("58459, all but one detection within 3.3 px of a G 15 star, is its residual")
	}
	calDepth, cal := 18.49, &Result{HFRNight: 1.543}
	cal.SubDepth = &calDepth
	if maxG, _ := residualStars(cal); 17.465 > maxG {
		t.Errorf("the G 17.47 star 67212 sat on is %.2f mag above the subs' depth, under the limit G %.2f", calDepth-17.465, maxG)
	}
	if maxG, r := residualStars(&Result{}); maxG != starResidualG || r != starResidualPx {
		t.Errorf("a night with no depth or star width: G %.1f within %.1f px", maxG, r)
	}
	var passing Track
	for k := range 8 {
		passing.Points = append(passing.Points, TrackPoint{X: 890 + 4*float64(k), Y: 1160 + 0.5*float64(k)})
	}
	if onBrightStars(passing, star, r) {
		t.Error("an asteroid passing the star at 4 px a sub is not its residual")
	}
}

func TestStarResidualInAShortBlockIsStatic(t *testing.T) {
	t.Parallel()
	// M 78's last four subs of 2025-11-24 spanned 24 minutes, less than the
	// 25.6 minutes apart the static test needs at 1.92"/px, so a residual
	// 3.2 px off a G 15 star in each of them stayed transient.
	const scale = 1.923
	start := time.Date(2025, 11, 25, 11, 6, 6, 0, time.UTC)
	offsets := []time.Duration{0, 7 * time.Minute, 12 * time.Minute, 24 * time.Minute}
	times := make([]time.Time, len(offsets))
	dets := make([][]moverDet, len(offsets))
	rng := rand.New(rand.NewPCG(2, 2))
	for j, o := range offsets {
		times[j] = start.Add(o)
		dets[j] = []moverDet{{X: 1944.9 + 0.3*rng.NormFloat64(), Y: 2675.1 + 0.3*rng.NormFloat64(), Flux: 1, SNR: 7}}
	}
	if _, _, transient := transients(times, dets, scale); !transient[0] || !transient[3] {
		t.Fatalf("without the star the short block's residual should stay transient: %v", transient)
	}
	for j := range dets {
		dets[j][0].OnStar = true
	}
	_, _, transient := transients(times, dets, scale)
	for i, tr := range transient {
		if tr {
			t.Errorf("detection %d on the star's glow taken for transient", i)
		}
	}
}

func TestStarGlowsCoverTheResidualsOfStarsWellAboveASubsDepth(t *testing.T) {
	t.Parallel()
	const w, h, hfr, zp, subDepth = 300, 300, 1.672, 25.0, 18.19
	ref := NewPlane(w, h)
	rng := rand.New(rand.NewPCG(8, 8))
	for i := range ref.Data {
		ref.Data[i] = float32(rng.NormFloat64())
	}
	sigma := 2 * hfr / 2.3548
	stars := []struct{ x, y, mag float64 }{{80, 80, 15.0}, {220, 80, 17.5}, {80, 220, 17.0}}
	for _, s := range stars {
		addGauss(ref.Data, w, h, s.x, s.y, math.Pow(10, -0.4*(s.mag-zp)), sigma)
	}
	one := NewPlane(64, 64)
	addGauss(one.Data, 64, 64, 32, 32, 1, sigma)
	z, d := zp+2.5*math.Log10(Aperture(one, 32, 32, apertureR)), subDepth
	res := &Result{HFRNight: hfr, ZeroPoint: &z, SubDepth: &d}
	g, radii := starGlows(ref, make([]bool, w*h), res)
	if g == nil {
		t.Fatal("no glows")
	}
	if i, _ := g.Nearest(80, 80, 2); i < 0 || math.Abs(radii[i]-7.0) > 0.4 {
		t.Errorf("the G 15 star's glow: %v", radii)
	}
	if i, _ := g.Nearest(220, 80, 2); i >= 0 {
		t.Errorf("a star 0.7 mag above a sub's depth leaves no residual a sub detects, glow %.1f px", radii[i])
	}
	if i, _ := g.Nearest(80, 220, 2); i < 0 || math.Abs(radii[i]-4.4) > 0.3 {
		t.Errorf("a star 1.2 mag above a sub's depth glows 1.3 star widths")
	}
	dets := [][]moverDet{{{X: 82.7, Y: 81.8}, {X: 85.5, Y: 83.2}, {X: 222.7, Y: 81.8}, {X: 150, Y: 150}}}
	markGlows(dets, g, radii)
	for k, want := range []bool{true, true, false, false} {
		if dets[0][k].OnStar != want {
			t.Errorf("detection %d at (%.1f, %.1f): on a star %v, want %v", k, dets[0][k].X, dets[0][k].Y, dets[0][k].OnStar, want)
		}
	}
	if g, _ := starGlows(ref, make([]bool, w*h), &Result{HFRNight: hfr}); g != nil {
		t.Error("glows without a zero point or a sub depth")
	}
}

func TestMoversMustMoveClearly(t *testing.T) {
	t.Parallel()
	// Tracks linked on M 78's 2025-11-24, California Nebula Panel 1's
	// 2026-10-06 and Sombrero Galaxy's 2025-12-29, each detection's minutes
	// after the first and pixel, with the night's HFR and plate solution
	// error in pixels.
	type pt struct{ min, x, y float64 }
	cases := []struct {
		name         string
		hfr, astroPx float64
		real         bool
		pts          []pt
	}{
		{"M 78 33324, a G 15 star's residual", 1.672, 0.117, false, []pt{{0.00, 1942.23, 2676.84}, {5.13, 1941.50, 2676.39}, {31.33, 1944.43, 2675.95}, {38.37, 1945.26, 2674.95}, {43.50, 1944.96, 2674.79}, {55.63, 1945.32, 2674.73}}},
		{"M 78 33327, a G 15 star's residual", 1.672, 0.117, false, []pt{{0.00, 4733.37, 1777.22}, {5.20, 4734.16, 1776.57}, {10.34, 4734.48, 1776.73}, {36.53, 4737.42, 1775.45}, {43.57, 4737.48, 1775.30}, {48.70, 4736.76, 1776.02}}},
		{"M 78 58457, a G 15 star's residual", 1.672, 0.117, false, []pt{{0.00, 801.54, 3586.37}, {10.34, 803.03, 3587.00}, {36.53, 806.18, 3585.81}, {43.57, 806.43, 3586.02}, {48.70, 805.54, 3586.48}, {60.83, 806.42, 3585.86}}},
		{"California 67212, on G 17 stars", 1.543, 0.060, false, []pt{{0.00, 246.69, 415.93}, {10.37, 246.68, 415.35}, {15.87, 246.32, 415.60}, {21.03, 246.79, 415.31}, {36.93, 243.70, 410.33}, {42.13, 244.07, 410.55}}},
		{"Sombrero 30622", 1.715, 0.056, false, []pt{{0.00, 197.28, 3214.45}, {5.20, 194.78, 3215.35}, {15.83, 195.56, 3215.88}, {21.03, 193.95, 3215.26}, {34.27, 192.86, 3213.59}, {39.47, 191.71, 3213.32}, {55.20, 191.69, 3213.26}}},
		{"California 636, a known asteroid", 1.543, 0.060, true, []pt{{0.00, 5176.67, 1565.30}, {5.50, 5176.01, 1565.07}, {15.87, 5174.83, 1566.13}, {26.57, 5173.28, 1565.69}, {31.77, 5173.38, 1565.38}, {37.27, 5173.12, 1566.20}, {58.20, 5170.94, 1565.93}}},
		{"California 635, a known asteroid", 1.543, 0.060, true, []pt{{0.00, 4621.53, 268.62}, {21.07, 4619.73, 269.88}, {36.97, 4617.83, 270.09}, {42.47, 4618.43, 270.53}, {47.63, 4617.25, 271.24}, {58.23, 4615.48, 270.54}, {63.40, 4615.44, 270.41}}},
		{"California 633, a known asteroid", 1.543, 0.060, true, []pt{{0.00, 473.68, 1641.90}, {5.17, 473.86, 1642.02}, {15.90, 472.37, 1642.23}, {21.07, 471.48, 1641.94}, {31.77, 470.87, 1642.21}, {42.13, 468.58, 1642.84}, {52.70, 468.21, 1642.42}, {57.90, 466.71, 1641.43}}},
	}
	start := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	for _, c := range cases {
		var tr Track
		for _, p := range c.pts {
			tr.Points = append(tr.Points, TrackPoint{Time: start.Add(time.Duration(p.min * float64(time.Minute))), X: p.x, Y: p.y})
		}
		d, sig := motion(tr, c.astroPx)
		if got := movedClearly(tr, 2*c.hfr, c.astroPx); got != c.real {
			t.Errorf("%s: moved %.1f px, %.1f times its error: taken for a mover %v", c.name, d, sig, got)
		}
	}
	still := Track{Points: []TrackPoint{{X: 10, Y: 10}, {X: 10, Y: 10}, {X: 10, Y: 10}}}
	if movedClearly(still, 3, 0.1) {
		t.Error("detections all at one time moved")
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
