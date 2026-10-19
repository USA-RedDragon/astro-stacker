package ownimage

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

// bareSpot is a spot with no star at (x, y), flagged on flaggedDay.
func bareSpot(f *synthField) Spot {
	s := f.spot(1, -1)
	s.GaiaG = nil
	s.RA, s.Dec = f.w.SkyOf(newX, newY)
	s.Nights[0].X, s.Nights[0].Y = newX, newY
	return s
}

const nextDay = "2025-06-04"

func measureBareSpot(t *testing.T, f *synthField, s Spot) *FluxCurve {
	t.Helper()
	return measureWithMaster(t, f, s, synthSub{sigma: 1.4, tr: 1, seed: 999, quiet: true})
}

func measureWithMaster(t *testing.T, f *synthField, s Spot, master synthSub) *FluxCurve {
	t.Helper()
	res, err := Measure(context.Background(), &trackField{synthField: f, master: master}, synthObj, []Spot{s}, Options{Threads: 4})
	if err != nil {
		t.Fatal(err)
	}
	c := res[0].Flux
	if c == nil {
		t.Fatal("no flux curve")
	}
	return c
}

func nightOf(t *testing.T, c *FluxCurve, night string) FluxNight {
	t.Helper()
	for _, n := range c.Nights {
		if n.Night == night && n.Filter == synthLum {
			return n
		}
	}
	t.Fatalf("no night %s", night)
	return FluxNight{}
}

// blobs scatters night-to-night residual light around (x, y), none
// within inner px of it, as a galaxy's bulge leaves in a difference.
func blobs(seed uint64, x, y, inner, amp float64) [][3]float64 {
	r := rand.New(rand.NewPCG(seed, 3))
	var out [][3]float64
	for len(out) < 220 {
		dx, dy := 120*r.Float64()-60, 120*r.Float64()-60
		if d := math.Hypot(dx, dy); d < inner || d > 60 {
			continue
		}
		out = append(out, [3]float64{x + dx, y + dy, amp * r.NormFloat64()})
	}
	return out
}

func TestLocalErrorComesFromThePlacesAroundTheSpot(t *testing.T) {
	t.Parallel()
	// In M 31's bulge the scatter of empty places around a spot was 1.5 to
	// 3.4 times the image-wide noise; spots of the 2025 bulge cluster that
	// the detection put at signal to noise 7 to 12.8 stood at 0.7 to 6.8
	// against it.
	f := newSynth()
	f.addNights(synthNights(), 6)
	for i := range f.subs {
		n := f.subs[i].Night
		f.subs[i].extra = blobs(math.Float64bits(float64(n.YearDay())), newX, newY, 4, 0.06)
		if nightKey(n) == flaggedDay {
			f.subs[i].transient = 0.06
		}
	}
	c := measureBareSpot(t, f, bareSpot(f))
	n := nightOf(t, c, flaggedDay)
	t.Logf("flagged night: flux %.4f err %.4f local %.4f snr %.1f", n.Flux, n.Err, n.Local, n.SNR())
	if !(n.Local > 0) || n.Err < n.Local {
		t.Fatalf("no local error on the flagged night: %+v", n)
	}
	if n.SNR() >= 5 {
		t.Errorf("light as faint as the residuals around it stood at %.1f", n.SNR())
	}
	quiet := nightOf(t, c, nextDay)
	if !(quiet.Local > 0) {
		t.Errorf("the night after was not measured around the spot: %+v", quiet)
	}
	if early := nightOf(t, c, "2025-01-02"); early.Local != 0 {
		t.Errorf("a night far from the flagged one was measured around the spot: %+v", early)
	}

	g := newSynth()
	g.addNights(synthNights(), 6)
	for i := range g.subs {
		n := g.subs[i].Night
		g.subs[i].extra = blobs(math.Float64bits(float64(n.YearDay())), newX, newY, 4, 0.06)
		if nightKey(n) == flaggedDay {
			g.subs[i].transient = 1.2
		}
	}
	c = measureBareSpot(t, g, bareSpot(g))
	if n := nightOf(t, c, flaggedDay); n.SNR() < 5 || n.Mag == 0 {
		t.Errorf("bright new light against the same residuals: %+v", n)
	}
}

func TestThinNightsAndFreshFrames(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	var used []discoveries.LightPoint
	for i := range f.subs {
		switch nightKey(f.subs[i].Night) {
		case flaggedDay:
			f.subs[i].transient = 0.5
			used = append(used, discoveries.LightPoint{FrameID: f.subs[i].FrameID, Filter: synthLum})
		case nextDay:
			f.subs[i].transient = 0.4
		}
	}
	f.subs = append(f.subs[:0:0], f.subs...)
	kept := f.subs[:0]
	thin := 0
	for _, s := range f.subs {
		if nightKey(s.Night) == "2025-06-02" {
			if thin == 2 {
				continue
			}
			thin++
		}
		kept = append(kept, s)
	}
	f.subs = kept
	s := bareSpot(f)
	s.Nights[0].Light = used
	c := measureBareSpot(t, f, s)
	if n := nightOf(t, c, "2025-06-02"); !n.Thin || n.N != 2 {
		t.Errorf("a two-frame night is not thin: %+v", n)
	}
	if n := nightOf(t, c, flaggedDay); n.FreshN != 0 || n.FreshSNR() != 0 {
		t.Errorf("the detection's own frames counted as fresh: %+v", n)
	}
	if n := nightOf(t, c, nextDay); n.FreshN != 6 || n.FreshSNR() < 5 {
		t.Errorf("the next night's frames are fresh and show the light: %+v", n)
	}
}

func TestWingOfABrightStarIsFittedWithEachFramesOwnShape(t *testing.T) {
	t.Parallel()
	// The ten spots of Andromeda's 2025-01-21 lay 4 to 8 px from G 13.7 to
	// 15.8 stars on nights whose star shape had wider wings than the
	// reference's; fitted with each sub's own shape, 66053 showed -0.4 to
	// -0.1 sigma. AT 2026uti, 2.8 px from a G 15.77 star, showed +4.9 to
	// +18.3.
	const sx, sy = newX + 5, newY
	nights := []time.Time{day("2024-12-01"), day("2024-12-10"), day("2024-12-20"), day("2025-01-15"), day("2025-06-01"), day("2025-06-02"), day(flaggedDay)}
	build := func(light float64) *FluxCurve {
		f := newSynth()
		f.stars = append(f.stars, synthStar{x: sx, y: sy, g: 13.8, bprp: 1})
		f.addNights(nights, 6)
		for i := range f.subs {
			if nightKey(f.subs[i].Night) == flaggedDay {
				f.subs[i].halo = 0.3
				f.subs[i].transient = light
			}
		}
		return measureBareSpot(t, f, bareSpot(f))
	}
	c := build(0)
	w := c.Wing
	if w == nil || !w.Measured {
		t.Fatalf("wing not measured: %+v", w)
	}
	plain := nightOf(t, c, flaggedDay)
	t.Logf("wing only: %+v; plain fit %.1f sigma", *w, plain.SNR())
	if w.G != 13.8 || math.Abs(w.SepPx-5) > 0.5 {
		t.Errorf("wrong star: %+v", *w)
	}
	if w.Sigma >= WingSigma {
		t.Errorf("the star's wing alone stood at %.1f sigma", w.Sigma)
	}
	c = build(0.3)
	if w := c.Wing; w == nil || !w.Measured || w.Sigma < WingSigma {
		t.Errorf("new light beside the star: %+v", w)
	}
}

func TestRingAroundAGalaxysNucleus(t *testing.T) {
	t.Parallel()
	// 54004 on Markarian Chain Panel 9's 2026-02-23 stood at H-a 7.6, 6.4 px
	// from NGC 4313's nucleus, while places on the same circle around the
	// nucleus ran from -3.1 to +7.0.
	const nx, ny = newX - 6.4, newY
	build := func(light float64) *FluxCurve {
		f := newSynth()
		f.addNights(synthNights(), 6)
		for i := range f.subs {
			w := 4.0
			if nightKey(f.subs[i].Night) == flaggedDay {
				w = 3.2
				f.subs[i].transient = light
			}
			f.subs[i].galaxy = [4]float64{nx, ny, w, 40}
		}
		return measureWithMaster(t, f, bareSpot(f), synthSub{sigma: 1.4, tr: 1, seed: 999, quiet: true, galaxy: [4]float64{nx, ny, 4, 40}})
	}
	c := build(0)
	if c.Ring == nil || len(c.Ring.Nights) == 0 {
		t.Fatalf("no ring measured: %+v", c.Ring)
	}
	r := c.Ring.Nights[0]
	t.Logf("residual only: sep %.1f px, %+v", c.Ring.SepPx, r)
	if math.Abs(c.Ring.SepPx-6.4) > 1 {
		t.Errorf("nucleus found %.1f px away", c.Ring.SepPx)
	}
	if r.Spot >= r.Max+3 {
		t.Errorf("a ring residual stood out at the spot: %+v", r)
	}
	c = build(1.5)
	if r := c.Ring.Nights[0]; r.Spot < r.Max+3 {
		t.Errorf("new light by the nucleus did not stand out of the ring: %+v", r)
	}
}

func TestHaloReach(t *testing.T) {
	t.Parallel()
	if r := haloReach(1.7); math.Abs(r/60-6.6) > 0.1 {
		t.Errorf("V 1.7 reaches %.2f arcmin, want 6.6", r/60)
	}
	if r := haloReach(8); r != 60 {
		t.Errorf("V 8 reaches %.0f arcsec", r)
	}
	// Alnilam (V 1.69) lay 6.2' from 33729 and 5.9' from 67098; Alnitak
	// (V 1.74) 2.4' from 64804.
	for _, sep := range []float64{6.2, 5.9, 2.4} {
		if sep*60 > haloReach(1.74) {
			t.Errorf("a spot %.1f arcmin from Alnitak is outside its halo", sep)
		}
	}
	alnitak := [2]float64{85.18969, -1.94257}
	f := &field{}
	sp := &spotPlan{spot: Spot{RA: alnitak[0] + 2.4/60, Dec: alnitak[1]}}
	if h := f.haloAt(sp); h == nil || h.V > 2 {
		t.Errorf("no bright star halo 2.4 arcmin from Alnitak: %+v", h)
	}
	sp.spot.Dec++
	if h := f.haloAt(sp); h != nil {
		t.Errorf("a halo a degree from Alnitak: %+v", h)
	}
}

func TestFakesAndEmptyPlacesAreMeasuredLikeTheSpot(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	for i := range f.subs {
		if nightKey(f.subs[i].Night) == flaggedDay {
			f.subs[i].transient = 0.5
		}
	}
	c := measureBareSpot(t, f, bareSpot(f))
	if r := c.RespOf(synthLum); r < 0.9 || r > 1.02 {
		t.Errorf("a star of unit light put in measures %.3f", r)
	}
	n := nightOf(t, c, flaggedDay)
	t.Logf("response %.3f, empty places 99th percentile %.2f of the error, err %.4f local %.4f", c.RespOf(synthLum), n.Empty99/n.Err, n.Err, n.Local)
	if z := n.Empty99 / n.Err; !(z > 1.5) || z > 4 {
		t.Errorf("the empty places' 99th percentile stands at %.2f of the night's error", z)
	}
	if early := nightOf(t, c, "2025-01-02"); early.Empty99 != 0 {
		t.Errorf("a night far from the flagged one has empty places measured: %+v", early)
	}
}
