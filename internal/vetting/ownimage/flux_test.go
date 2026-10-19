package ownimage

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
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

func TestDegenerateSubIsLeftOut(t *testing.T) {
	t.Parallel()
	// M 15, 2026-10-18 (68912): the one Green sub measured, at 3.9e-9 with
	// an error of 9.9e-10, beside Blue, Luminance and Red subs of the usual
	// errors, from the night's zero points.
	zero := map[string]float64{"Blue": 12.12, "Green": 12.28, "Luminance": 13.28, "Red": 12.41}
	var pts []FluxPoint
	for i, f := range []string{"Blue", "Luminance", "Red", "Blue", "Luminance", "Red", "Blue", "Red"} {
		e := map[string]float64{"Blue": 5.1e-4, "Luminance": 1.2e-3, "Red": 8.7e-4}[f]
		pts = append(pts, FluxPoint{FrameID: i, Night: "2026-10-18", Filter: f, Flux: e, Err: e * math.Sqrt(5)})
	}
	pts = append(pts, FluxPoint{FrameID: 99, Night: "2026-10-18", Filter: "Green", Flux: 3.9e-9, Err: 9.9e-10},
		FluxPoint{FrameID: 100, Night: "2026-10-18", Filter: "H-a", Flux: 1e-3, Err: 2e-3})
	got := dropDegenerate(pts, zero)
	if len(got) != 9 {
		t.Fatalf("kept %d of 10", len(got))
	}
	for _, p := range got {
		if p.FrameID == 99 {
			t.Errorf("the master's own stamp kept: %+v", p)
		}
	}
}

// meanMaster is a synthetic target whose Luminance master is the weighted
// mean of its own subs, as the stacker builds it, with their new light
// in it unless clean.
type meanMaster struct {
	*synthField
	clean bool
}

func (m *meanMaster) Masters(context.Context, string) (map[string]string, error) {
	return map[string]string{synthLum: masterKey}, nil
}

func (m *meanMaster) Load(ctx context.Context, key string) (*discoveries.Plane, error) {
	if key != masterKey {
		return m.synthField.Load(ctx, key)
	}
	out := discoveries.NewPlane(synthW, synthH)
	acc := make([]float64, len(out.Data))
	sw := make([]float64, len(out.Data))
	for _, s := range m.subs {
		if m.clean {
			s.transient = 0
		}
		for i, v := range m.render(s).Data {
			if !math.IsNaN(float64(v)) {
				acc[i] += s.Weight * float64(v) / s.tr
				sw[i] += s.Weight
			}
		}
	}
	for i := range out.Data {
		out.Data[i] = float32(acc[i] / sw[i])
	}
	return out, nil
}

// shareField is a target with a new source of light 0.5 on flaggedDay,
// on the nights given with subs each, the flagged night's subs weighted
// on and the others off times the usual.
func shareField(nights []string, subs int, on, off float64) *synthField {
	f := newSynth()
	ts := make([]time.Time, len(nights))
	for i, n := range nights {
		ts[i] = day(n)
	}
	f.addNights(ts, subs)
	for i := range f.subs {
		if nightKey(f.subs[i].Night) == flaggedDay {
			f.subs[i].transient = 0.5
			f.subs[i].Weight *= on
		} else {
			f.subs[i].Weight *= off
		}
	}
	return f
}

func flaggedLight(t *testing.T, f *synthField, clean bool) (FluxNight, *FluxCurve) {
	t.Helper()
	s := f.spot(1, -1)
	s.GaiaG = nil
	s.RA, s.Dec = f.w.SkyOf(newX, newY)
	res, err := Measure(context.Background(), &meanMaster{synthField: f, clean: clean}, synthObj, []Spot{s}, Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	c := res[0].Flux
	if c == nil {
		t.Fatal("no flux curve")
	}
	for _, n := range c.Nights {
		if n.Night == flaggedDay && n.Filter == synthLum {
			return n, c
		}
	}
	t.Fatal("no flagged night")
	return FluxNight{}, nil
}

func TestMasterShareOfTheNightsLight(t *testing.T) {
	t.Parallel()
	// The flagged night's 6 subs weighted 4 times the usual hold 40% of a
	// master that also holds 7 other nights: the level of those nights
	// shows the master's share as light missing, and taking it away gives
	// the night all its light, as a master without it does.
	nights := []string{"2025-01-02", "2025-01-05", "2025-03-01", "2025-03-04", "2025-06-01", "2025-06-02", flaggedDay, nextDay}
	got, c := flaggedLight(t, shareField(nights, 6, 4, 1), false)
	want, _ := flaggedLight(t, shareField(nights, 6, 4, 1), true)
	t.Logf("with level: %.4f ± %.4f, clean master %.4f, base %v", got.Flux, got.Err, want.Flux, c.Base)
	if math.Abs(got.Flux-want.Flux) > 2*got.Err || got.Shared != 0 || !(c.Base[synthLum] < -0.1) {
		t.Errorf("with a level the night shows %.4f ± %.4f, a clean master %.4f; share %.3f, base %.4f", got.Flux, got.Err, want.Flux, got.Shared, c.Base[synthLum])
	}
	// With 2 subs of another night, weighted 5 times, too few for a level,
	// the master keeps its 6 × 1.5 / (6 × 1.5 + 2 × 7.5) = 37.5% share, near
	// enough for the subs' random weights, and the night shows the rest.
	f := shareField([]string{"2025-06-01", flaggedDay}, 6, 1, 5)
	f.subs = f.subs[4:]
	got, c = flaggedLight(t, f, false)
	g := shareField([]string{"2025-06-01", flaggedDay}, 6, 1, 5)
	g.subs = g.subs[4:]
	want, _ = flaggedLight(t, g, true)
	t.Logf("no level: %.4f ± %.4f, clean master %.4f, share %.3f of %d subs", got.Flux, got.Err, want.Flux, got.Shared, got.MasterSubs)
	if _, ok := c.Base[synthLum]; ok || got.Shared < 0.3 || got.Shared > 0.45 || got.MasterSubs != 8 || !got.Judgeable() {
		t.Fatalf("no level: base %v, share %.3f of %d subs", c.Base, got.Shared, got.MasterSubs)
	}
	if r := got.Flux / want.Flux; math.Abs(r-got.Kept()) > 0.03 {
		t.Errorf("the night shows %.3f of its light where the master leaves %.3f", r, got.Kept())
	}
	// M 15's 2026-10-18: a master of the night's own 2 subs holds all its
	// light.
	f = shareField([]string{flaggedDay}, 2, 1, 1)
	got, _ = flaggedLight(t, f, false)
	if got.Shared != 1 || got.MasterSubs != 2 || got.Judgeable() {
		t.Errorf("a master of the night's own 2 subs: share %.3f of %d subs, judgeable %v", got.Shared, got.MasterSubs, got.Judgeable())
	}
}
