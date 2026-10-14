package discoveries

import (
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
	tracks, transient := linkMovers(times, dets, scale)
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
