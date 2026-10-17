package ownimage

import (
	"context"
	"math"
	"testing"
)

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
