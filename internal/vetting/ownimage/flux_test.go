package ownimage

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"
)

func TestPSFFluxTakesAwayTheLocalBackground(t *testing.T) {
	t.Parallel()
	// 67045 on Sombrero Galaxy's 2025-12-29 was diffuse light near a corner
	// of the frame: measured without the annulus it gave signal to noise 27
	// over the night, with it 3, as random places nearby did.
	const n, sigma = stampSide, 1.4
	c := float64(stampHalf)
	rng := rand.New(rand.NewPCG(6, 7))
	glow := make([]float64, n*n)
	star := make([]float64, n*n)
	for k := range glow {
		x, y := float64(k%n), float64(k/n)
		d := math.Exp(-((x-c-9)*(x-c-9)+(y-c+7)*(y-c+7))/(2*12*12)) + 0.02*(x-c)
		noise := 0.05 * rng.NormFloat64()
		glow[k] = 0.6*d + noise
		r2 := (x-c)*(x-c) + (y-c)*(y-c)
		star[k] = glow[k] + 20*math.Exp(-r2/(2*sigma*sigma))/(2*math.Pi*sigma*sigma)
	}
	f, e, ok := psfFlux(glow, c, c, sigma)
	if !ok {
		t.Fatal("no flux")
	}
	if math.Abs(f) > 3*e {
		t.Errorf("diffuse light measured as %.2f ± %.2f", f, e)
	}
	fs, es, ok := psfFlux(star, c, c, sigma)
	if !ok || math.Abs(fs-f-20) > 3*es {
		t.Errorf("a flux 20 star on the same light measured as %.2f ± %.2f", fs-f, es)
	}
}

func TestNewSourceFluxCurveGivesItsMagnitude(t *testing.T) {
	t.Parallel()
	f := newSynth()
	f.addNights(synthNights(), 6)
	for i := range f.subs {
		if nightKey(f.subs[i].Night) == flaggedDay {
			f.subs[i].transient = 0.5
		}
	}
	fresh := f.spot(1, -1)
	fresh.GaiaG = nil
	fresh.RA, fresh.Dec = f.w.SkyOf(newX, newY)
	res, err := Measure(context.Background(), &trackField{synthField: f, master: synthSub{sigma: 1.4, tr: 1, seed: 999, quiet: true}}, synthObj, []Spot{fresh}, Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	c := res[0].Flux
	if c == nil {
		t.Fatal("no flux curve for a spot with no star")
	}
	want := 15 - 2.5*math.Log10(0.5)
	quiet := 0
	for _, n := range c.Nights {
		t.Logf("%s %s n=%d flux %.4f ± %.4f mag %.2f limit %.2f", n.Night, n.Filter, n.N, n.Flux, n.Err, n.Mag, n.Limit)
		if n.Night == flaggedDay {
			if !n.Flagged || math.Abs(n.Mag-want) > 0.05 || n.SNR() < 20 {
				t.Errorf("flagged night %+v, want magnitude %.2f", n, want)
			}
			continue
		}
		if n.Mag != 0 || n.Limit < want+2 {
			t.Errorf("quiet night %+v shows light or a shallow limit", n)
		}
		quiet++
	}
	if quiet != len(synthNights())-1 {
		t.Errorf("%d quiet nights", quiet)
	}
}
