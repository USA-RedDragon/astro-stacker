package discoveries

import (
	"context"
	"math"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// bulgeField is a 512x512 difference of unit noise but for two cells: one
// 3.4 times as noisy, as M 31's nucleus cell was on 2025-10-26 (60465),
// and one 1.7 times, about as the spot of AT 2026acsi was on 2026-10-11.
func bulgeField() (diff, ref *Plane, edge []bool) {
	const n = 512
	rng := rand.New(rand.NewPCG(7, 3))
	diff, ref = NewPlane(n, n), NewPlane(n, n)
	for y := range n {
		for x := range n {
			s := 1.0
			switch {
			case x >= 256 && x < 320 && y >= 256 && y < 320:
				s = 3.4
			case x >= 128 && x < 192 && y >= 320 && y < 384:
				s = 1.7
			}
			diff.Data[y*n+x] = float32(s * rng.NormFloat64())
			ref.Data[y*n+x] = float32(0.01 * rng.NormFloat64())
		}
	}
	return diff, ref, make([]bool, n*n)
}

// addStar adds a Gaussian of sigma 1.3 px holding total at (x, y).
func addStar(p *Plane, x, y, total float64) {
	const s = 1.3
	for yy := int(y) - 8; yy <= int(y)+8; yy++ {
		for xx := int(x) - 8; xx <= int(x)+8; xx++ {
			dx, dy := float64(xx)-x, float64(yy)-y
			p.Data[yy*p.W+xx] += float32(total / (2 * math.Pi * s * s) * math.Exp(-(dx*dx+dy*dy)/(2*s*s)))
		}
	}
}

func TestNoiseMapReadsTheBulgeCell(t *testing.T) {
	t.Parallel()
	diff, ref, edge := bulgeField()
	global := emptyApertureSigma(diff, edge, apertureR, emptyApertures)
	m := newNoiseMap(diff, ref, edge, global)
	for _, tc := range []struct {
		name string
		x, y float64
		want float64
	}{
		{"calm sky", 96, 96, 1},
		{"nucleus cell", 288, 288, 3.4},
		{"bulge cell", 160, 352, 1.7},
	} {
		if r := m.at(tc.x, tc.y) / global; math.Abs(r/tc.want-1) > 0.15 {
			t.Errorf("%s: noise %.2f times the picture's, want %.1f", tc.name, r, tc.want)
		}
	}
	if r := m.at(288, 256) / global; r < 1.8 || r > 2.6 {
		t.Errorf("between the nucleus cell and calm sky the noise is both cells': %.2f times the picture's", r)
	}
	if m.at(2, 2) < global {
		t.Error("the local noise is never under the picture's")
	}
}

// TestLocallyFaintBrighteningIsHeld replays the bulge: a brightening at 10
// times the picture's noise in the nucleus cell stands at about 3 against
// its own (60465: 12.8 against 1.7) and is held; one at 11 in a cell 1.7
// times as noisy stands at about 6.5 (AT 2026acsi: 6.8 to 7.4) and is not.
func TestLocallyFaintBrighteningIsHeld(t *testing.T) {
	t.Parallel()
	diff, ref, edge := bulgeField()
	global := emptyApertureSigma(diff, edge, apertureR, emptyApertures)
	m := newNoiseMap(diff, ref, edge, global)
	k := Aperture(func() *Plane { p := NewPlane(32, 32); addStar(p, 16, 16, 1); return p }(), 16, 16, apertureR)
	res := &Result{Funnel: map[string]Funnel{}, ApSigma: global, NightApSigma: global, HFRNight: 1.5, HFRRef: 1.5}
	cands := make([]cand, 0, 2)
	for _, s := range []struct {
		x, y, snr float64
	}{{288, 288, 10}, {160, 352, 11}} {
		addStar(diff, s.x, s.y, s.snr*global/k)
		flux := Aperture(diff, s.x, s.y, apertureR)
		local := m.at(s.x, s.y)
		c := cand{noiseScale: local / global}
		c.X, c.Y, c.Sign, c.SNR, c.LocalSNR, c.Frac = s.x, s.y, 1, flux/global, flux/local, 1
		for range 8 {
			c.fluxes, c.errs = append(c.fluxes, 1), append(c.errs, 0.2)
			c.Light = append(c.Light, LightPoint{Filter: broadbandClear})
		}
		cands = append(cands, c)
	}
	out := (&Input{}).cut(cands, NewPlane(512, 512), diff, localFrac{}, res)
	if len(out) != 2 {
		t.Fatalf("%d of 2 kept: %+v", len(out), res.Rejected)
	}
	for _, c := range out {
		if c.SNR < PromoteSigma {
			t.Fatalf("at %.0f,%.0f the picture's SNR is %.1f, under the promotion bar", c.X, c.Y, c.SNR)
		}
	}
	if c := out[0]; !c.LowConf || c.LocalSNR >= 4 {
		t.Errorf("nucleus cell: SNR %.1f, local %.1f, low confidence %v", c.SNR, c.LocalSNR, c.LowConf)
	}
	if c := out[1]; c.LowConf || c.LocalSNR < 5.5 || c.LocalSNR > 7.5 {
		t.Errorf("bulge cell: SNR %.1f, local %.1f, low confidence %v", c.SNR, c.LocalSNR, c.LowConf)
	}
	ls := classifyChanges(context.Background(), t, &Result{Candidates: out}, Known{})
	if ls[0].Class != ClassLowConfidence || ls[0].Matches[len(ls[0].Matches)-1].Catalog != catalogNoise {
		t.Errorf("nucleus cell: %s %+v", ls[0].Class, ls[0].Matches)
	}
	for _, mt := range ls[1].Matches {
		if mt.Catalog == catalogNoise {
			t.Errorf("bulge cell held for noise: %+v", mt)
		}
	}
}

// TestPerSubErrorsTakeTheLocalNoise: a change in every sub at twice each
// sub's picture-wide noise is in none of them at 3.4 times that noise, and
// the errors the light curve keeps are the local ones.
func TestPerSubErrorsTakeTheLocalNoise(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scale   float64
		present int
	}{{0, 8}, {0.8, 8}, {3.4, 0}} {
		c := &cand{noiseScale: tc.scale}
		c.Sign = 1
		for range 8 {
			c.fluxes, c.errs = append(c.fluxes, 1), append(c.errs, 0.5*c.localScale())
			c.Light = append(c.Light, LightPoint{Filter: broadbandClear, FluxErr: 0.5 * c.localScale()})
		}
		if _, st := c.subTest(); st.present != tc.present {
			t.Errorf("noise %.1f times the picture's: %+v", tc.scale, st)
		}
	}
}

// TestSubErrorsMatchTheNebula is 41445 (Rosette Nebula, H-a, 2025-02-28)
// in miniature: subs of unit noise but for a nebula 1.6 times as noisy,
// as the subs there measured 1.51 to 1.66 times their picture-wide
// noise. A change at the nebula's spot must carry errors that match the
// scatter of empty apertures around it within 10%, where the picture-wide
// errors were about 1.6 times too small.
func TestSubErrorsMatchTheNebula(t *testing.T) {
	t.Parallel()
	const n, s = 512, 1.6
	rng := rand.New(rand.NewPCG(41, 445))
	field := func() *Plane {
		p := NewPlane(n, n)
		for y := range n {
			for x := range n {
				v := rng.NormFloat64()
				if x >= 192 && x < 320 && y >= 192 && y < 320 {
					v *= s
				}
				p.Data[y*n+x] = float32(v)
			}
		}
		return p
	}
	ref, edge := NewPlane(n, n), make([]bool, n*n)
	diff := field()
	global := emptyApertureSigma(diff, edge, apertureR, emptyApertures)
	m := newNoiseMap(diff, ref, edge, global)
	c := &cand{noiseScale: m.at(256, 256) / global}
	old, now, empty := make([]float64, 0, 8), make([]float64, 0, 8), make([]float64, 0, 8)
	for range 8 {
		sub := field()
		sig := emptyApertureSigma(sub, edge, apertureR, 4000)
		var v []float64
		for i := 1; i <= 300; i++ {
			v = append(v, Aperture(sub, 200+halton(i, 2)*112, 200+halton(i, 3)*112, apertureR))
		}
		old, now, empty = append(old, sig), append(now, sig*c.localScale()), append(empty, robustStd(v))
	}
	o, w, e := median64(old), median64(now), median64(empty)
	t.Logf("empty apertures at the spot %.3f, picture-wide error %.3f (%.2f times too small), local error %.3f", e, o, e/o, w)
	if e/o < 1.4 {
		t.Fatalf("the field does not show the nebula's noise: %.2f", e/o)
	}
	if math.Abs(w/e-1) > 0.1 {
		t.Errorf("sub errors %.3f against %.3f measured at the spot", w, e)
	}
}

// TestNoiseMapOnTheAppsStamps measures the app's own 49x49 difference
// stamps of 60465 (M 31's bulge, 2025-10-26, an artifact the app called a
// nova at 12.8) and of AT 2026acsi (2026-10-11): the audit's empty
// apertures read their local noise 3.40 and 1.97 times the runs' and their
// signal 1.7 and 6.8 times it.
func TestNoiseMapOnTheAppsStamps(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file          string
		apSigma       float64
		inflation     float64
		lo, hi        float64
		held          bool
		auditLocalSNR float64
	}{
		{"testdata/stamp-60465-2025-10-26.fits", 0.00018515960522993478, 3.40, 1.0, 3.0, true, 1.71},
		{"testdata/stamp-182-2026-10-11.fits", 0.0002552134819432988, 1.97, 5.5, 8.5, false, 6.79},
	} {
		b, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		im, err := imagedata.Decode(b)
		if err != nil || im.C != 3 || im.W != StampSize || im.H != StampSize {
			t.Fatalf("%s: %v", tc.file, err)
		}
		n := StampSize * StampSize
		ref := &Plane{W: StampSize, H: StampSize, Data: im.Data[n : 2*n]}
		diff := &Plane{W: StampSize, H: StampSize, Data: im.Data[2*n : 3*n]}
		m := newNoiseMap(diff, ref, make([]bool, n), tc.apSigma)
		c := float64(StampSize / 2)
		local := m.at(c, c)
		snr := Aperture(diff, c, c, apertureR) / local
		t.Logf("%s: local noise %.2f times the run's, local SNR %.2f (audit %.2f times, %.2f)", tc.file, local/tc.apSigma, snr, tc.inflation, tc.auditLocalSNR)
		if r := local / tc.apSigma / tc.inflation; r < 0.8 || r > 1.25 {
			t.Errorf("%s: local noise %.2f times the run's, the audit's %.2f", tc.file, local/tc.apSigma, tc.inflation)
		}
		cd := Candidate{Sign: 1, LocalSNR: snr}
		if snr < tc.lo || snr > tc.hi || cd.locallyFaint() != tc.held {
			t.Errorf("%s: local SNR %.2f, held %v", tc.file, snr, cd.locallyFaint())
		}
	}
}
