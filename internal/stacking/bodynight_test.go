package stacking

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

type movingBody struct {
	subs   [][]float32
	shifts [][2]float64
	stored []storedSub
	w, h   int
}

func synthBody(n int, bodyFlux, bodySigma float64) movingBody {
	const w, h = 240, 160
	r := rand.New(rand.NewPCG(7, 11))
	type star struct{ x, y, f float64 }
	stars := make([]star, 0, 80)
	for range 80 {
		stars = append(stars, star{10 + r.Float64()*220, 10 + r.Float64()*140, 0.002 + 0.1*math.Pow(r.Float64(), 4)})
	}
	blob := func(img []float32, cx, cy, f, s float64) {
		for y := int(cy) - 10; y <= int(cy)+10; y++ {
			for x := int(cx) - 10; x <= int(cx)+10; x++ {
				if x >= 0 && y >= 0 && x < w && y < h {
					dx, dy := float64(x)-cx, float64(y)-cy
					img[y*w+x] += float32(f / (2 * math.Pi * s * s) * math.Exp(-(dx*dx+dy*dy)/(2*s*s)))
				}
			}
		}
	}
	b := movingBody{subs: make([][]float32, n), shifts: make([][2]float64, n), stored: make([]storedSub, n), w: w, h: h}
	for i := range n {
		img := make([]float32, w*h)
		for j := range img {
			img[j] = float32(0.01 + 0.0005*r.NormFloat64())
		}
		for _, s := range stars {
			blob(img, s.x, s.y, s.f, 1.5)
		}
		drift := float64(i)
		blob(img, 80+drift, 80, bodyFlux, bodySigma)
		b.subs[i] = img
		b.shifts[i] = [2]float64{-drift, 0}
		b.stored[i] = storedSub{exposure: 10, weight: 1}
	}
	return b
}

func TestSeparateCometTapsEachSubOnce(t *testing.T) {
	t.Parallel()
	for _, n := range []int{12, cometMedianMax + 4} {
		b := synthBody(n, 0.5, 1.5)
		seen := make([]int, n)
		var fluxes []float64
		read := func(i int) ([]float32, int, int, error) { return slices.Clone(b.subs[i]), b.w, b.h, nil }
		_, err := separateComet(b.stored, b.shifts, nil, nil, DefaultOptions(), read, func(int, int) {}, func(i int, sub []float32, w, h int, scale float64) {
			seen[i]++
			if f := goals.ApertureFlux(sub, w, h, 80, 80, 0.9); f.OK && scale > 0 {
				fluxes = append(fluxes, f.Flux/scale)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		for i, c := range seen {
			if c != 1 {
				t.Fatalf("%d subs: sub %d tapped %d times", n, i, c)
			}
		}
		res, err := goals.PointSNR(fluxes, 0)
		if err != nil || len(fluxes) != n || math.Abs(res.Flux*10-0.5)/0.5 > 0.1 || res.SNR < 50 {
			t.Errorf("%d subs: %d fluxes, %+v %v", n, len(fluxes), res, err)
		}
	}
}

func TestComaMeasureOnBodyAlignedSubs(t *testing.T) {
	t.Parallel()
	const n = 32
	snr := func(flux float64) (app.BodyNight, error) {
		b := synthBody(n, flux, 6)
		gs := make([]goals.Sub, n)
		for i := range gs {
			gs[i] = goals.Sub{Exposure: 10, Weight: 1, Effective: 10}
		}
		tail := [2]float64{1, 0}
		coma := goals.ComaRegions(b.w/goals.NoiseBin, b.h/goals.NoiseBin, 80, 80, &tail, 2)
		meas, measErr := goals.NewMeasurer(b.w, b.h, gs, goals.Options{Seed: 1, RegionMask: coma.Union()})
		if measErr != nil {
			return app.BodyNight{}, measErr
		}
		read := func(i int) ([]float32, int, int, error) { return slices.Clone(b.subs[i]), b.w, b.h, nil }
		var tapErr error
		_, err := separateComet(b.stored, b.shifts, nil, nil, DefaultOptions(), read, func(int, int) {}, func(i int, sub []float32, w, h int, _ float64) {
			bn, err := goals.Bin(sub, w, h, 10, 0.9)
			if err == nil {
				err = meas.Add(i, bn)
			}
			if tapErr == nil {
				tapErr = err
			}
		})
		if err != nil || tapErr != nil {
			return app.BodyNight{}, errorsJoin(err, tapErr)
		}
		row := app.BodyNight{EffectiveHours: n * 10.0 / 3600}
		applyComaMeasure(&row, meas, nil, coma, false)
		return row, nil
	}
	faint, err := snr(0.05)
	if err != nil {
		t.Fatal(err)
	}
	bright, err := snr(0.5)
	if err != nil {
		t.Fatal(err)
	}
	if faint.Error != nil || bright.Error != nil || faint.HeadSNR == nil || bright.HeadSNR == nil || faint.TailSNR == nil || bright.TailSNR == nil ||
		*faint.HeadSNR <= 0 || *bright.HeadSNR < 2**faint.HeadSNR || math.Abs(*bright.TailSNR) > 0.2**bright.HeadSNR ||
		bright.SNR <= faint.SNR || faint.Levels < goals.MinFloorLevels {
		t.Errorf("faint %+v bright %+v", faint, bright)
	}
	none := app.BodyNight{}
	applyComaMeasure(&none, nil, goals.ErrInsufficientData, goals.ComaMasks{}, true)
	if none.Error == nil || *none.Error != goals.ErrInsufficientData.Error() {
		t.Errorf("insufficient %+v", none)
	}
}

func errorsJoin(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func TestBodyNightSignature(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)
	rows := []nightSub{{RegisteredKey: "a", Exposure: 60, Weight: 1, Score: 0.8, DateObs: at}, {RegisteredKey: "b", Exposure: 60, Weight: 1, Score: 0.6, DateObs: at.Add(time.Minute)}}
	comet := bodies.Target{Designation: "161P", Kind: bodies.KindComet}
	sig := bodyNightSignature(rows, comet)
	if sig != bodyNightSignature(slices.Clone(rows), comet) {
		t.Error("unstable")
	}
	more := append(slices.Clone(rows), nightSub{RegisteredKey: "c", Exposure: 60, Weight: 1, Score: 0.7})
	rescored := slices.Clone(rows)
	rescored[0].Weight = 0.5
	for name, other := range map[string]string{
		"a new sub":   bodyNightSignature(more, comet),
		"a new score": bodyNightSignature(rescored, comet),
		"asteroid":    bodyNightSignature(rows, bodies.Target{Designation: "161P", Kind: bodies.KindAsteroid}),
	} {
		if other == sig {
			t.Errorf("%s keeps the signature", name)
		}
	}
	if h := effectiveHours(rows); math.Abs(h-(0.8*60+0.6*60)/3600) > 1e-12 {
		t.Errorf("effective hours %v", h)
	}
	if m := middleTime(rows); !m.Equal(at.Add(time.Minute + 30*time.Second)) {
		t.Errorf("middle %s", m)
	}
}

func TestOffsetSky(t *testing.T) {
	t.Parallel()
	ra, dec := offsetSky(10, 60, 90, 3600)
	if math.Abs(dec-60) > 1e-9 || math.Abs((ra-10)*math.Cos(60*math.Pi/180)-1) > 1e-9 {
		t.Errorf("east %v %v", ra, dec)
	}
	ra, dec = offsetSky(10, 60, 0, 36)
	if ra != 10 || math.Abs(dec-60.01) > 1e-9 {
		t.Errorf("north %v %v", ra, dec)
	}
}

func TestApplyPointMeasure(t *testing.T) {
	t.Parallel()
	row := app.BodyNight{}
	applyPointMeasure(&row, []float64{1, 1.1, 0.9, 1, 1.05, 0.95, 1, 1.02}, 0)
	if row.Error != nil || row.SNR <= 0 || row.Saturated {
		t.Errorf("measured %+v", row)
	}
	sat := app.BodyNight{}
	applyPointMeasure(&sat, []float64{1, 1, 1}, 7)
	if sat.Error != nil || !sat.Saturated || sat.LowReason != goals.ReasonSaturated {
		t.Errorf("saturated %+v", sat)
	}
	few := app.BodyNight{}
	applyPointMeasure(&few, []float64{1, 1}, 0)
	if few.Error == nil {
		t.Errorf("too few %+v", few)
	}
}

func TestSeparateCometKeepsACompactBody(t *testing.T) {
	t.Parallel()
	const n = 12
	b := synthBody(n, 0.05, 0.7)
	track := make([][2]float64, n)
	for i := range track {
		track[i] = [2]float64{80 + float64(i), 80}
	}
	read := func(i int) ([]float32, int, int, error) { return slices.Clone(b.subs[i]), b.w, b.h, nil }
	measured := 0
	_, err := separateComet(b.stored, b.shifts, track, nil, DefaultOptions(), read, func(int, int) {}, func(_ int, sub []float32, w, h int, _ float64) {
		if goals.ApertureFlux(sub, w, h, 80, 80, 0.9).OK {
			measured++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if measured != n {
		t.Errorf("%d of %d subs measured", measured, n)
	}
}

func TestBodyCrop(t *testing.T) {
	t.Parallel()
	r := bodyCrop([][2]float64{{1000, 800}, {1100, 820}}, 1050, 810, 2, 6000, 4000)
	if r.X > 1000-154 || r.X+r.W < 1100+154 || r.Y > 800-154 || r.Y+r.H < 820+154 {
		t.Errorf("crop %+v misses the tail reach", r)
	}
	edge := bodyCrop([][2]float64{{10, 10}}, 10, 10, 2, 600, 400)
	if edge.X != 0 || edge.Y != 0 || edge.W > 600 || edge.H > 400 {
		t.Errorf("edge crop %+v", edge)
	}
}
