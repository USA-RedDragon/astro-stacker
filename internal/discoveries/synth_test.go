package discoveries

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// field is a synthetic target: stars on a sky, imaged over several nights,
// with one star brighter on the last night, a new point source on a
// galaxy, and an asteroid crossing during the last night.
type field struct {
	W, H     int
	WCS      *WCS
	Stars    []synthStar
	Nights   []time.Time
	PerNight int
	Var      int
	NewX     float64
	NewY     float64
	AstX     float64
	AstY     float64
	AstVX    float64
	AstVY    float64
	// hot puts a defect pixel on the last night's subs, spread over 2x2
	// pixels and moved by up to a pixel and a half between them, as
	// registration spreads and moves one.
	hot   bool
	HotX  float64
	HotY  float64
	start time.Time
	sky   float64
	noise float64
	zp    float64
}

type synthStar struct{ X, Y, Flux float64 }

const (
	synthSubSeconds = 60
	synthCadence    = 6 * time.Minute
	synthBorder     = 12
)

func newField(t *testing.T) *field {
	t.Helper()
	rng := rand.New(rand.NewPCG(7, 11))
	f := &field{W: 1800, H: 1280, PerNight: 6, sky: 0.05, noise: 0.002, zp: 23.5}
	w, err := ParseWCS(synthWCS(), f.H)
	if err != nil {
		t.Fatal(err)
	}
	f.WCS = w
	for len(f.Stars) < 1500 {
		x := 20 + rng.Float64()*float64(f.W-40)
		y := 20 + rng.Float64()*float64(f.H-40)
		flux := 0.05 * math.Pow(10, 2.2*rng.Float64())
		f.Stars = append(f.Stars, synthStar{x, y, flux})
	}
	best, bd := -1, math.Inf(1)
	for i, s := range f.Stars {
		if s.X < 500 || s.X > 1300 || s.Y < 400 || s.Y > 880 || s.Flux < 1 || s.Flux > 3 {
			continue
		}
		near := math.Inf(1)
		for j, o := range f.Stars {
			if j != i {
				near = math.Min(near, math.Hypot(o.X-s.X, o.Y-s.Y))
			}
		}
		if near > 15 && math.Abs(s.X-900) < bd {
			best, bd = i, math.Abs(s.X-900)
		}
	}
	if best < 0 {
		t.Fatal("no star to make variable")
	}
	f.Var = best
	f.NewX, f.NewY = f.emptySpot(t, 600, 800)
	f.AstX, f.AstY = f.emptySpot(t, 1200, 440)
	f.AstVX, f.AstVY = 0.5, 0.25
	for n := range 4 {
		f.Nights = append(f.Nights, time.Date(2026, 3, 1+n, 0, 0, 0, 0, time.UTC))
	}
	f.start = time.Date(2026, 3, 4, 4, 0, 0, 0, time.UTC)
	return f
}

const testFilter = "Luminance"

func synthWCS() map[string]string { return tanWCS(150, 20, 2, 900.5, 640.5) }

// tanWCS is a north-up plate solution of scale arcseconds a pixel.
func tanWCS(ra, dec, scale, crpix1, crpix2 float64) map[string]string {
	return map[string]string{"CTYPE1": "RA---TAN", "CTYPE2": "DEC--TAN", "CRPIX1": fmt.Sprint(crpix1), "CRPIX2": fmt.Sprint(crpix2),
		"CRVAL1": fmt.Sprint(ra), "CRVAL2": fmt.Sprint(dec), "CD1_1": fmt.Sprint(-scale / 3600), "CD1_2": "0", "CD2_1": "0", "CD2_2": fmt.Sprint(scale / 3600)}
}

func (f *field) emptySpot(t *testing.T, x0, y0 float64) (float64, float64) {
	for r := 0.0; r < 200; r += 3 {
		for a := 0.0; a < 2*math.Pi; a += 0.3 {
			x, y := x0+r*math.Cos(a), y0+r*math.Sin(a)
			ok := true
			for _, s := range f.Stars {
				if math.Hypot(s.X-x, s.Y-y) < 18 {
					ok = false
					break
				}
			}
			if ok {
				return x, y
			}
		}
	}
	t.Fatal("no empty spot")
	return 0, 0
}

// subTime is the start of sub i on night n.
func (f *field) subTime(n, i int) time.Time {
	return f.start.AddDate(0, 0, n-len(f.Nights)+1).Add(time.Duration(i) * synthCadence)
}

func (f *field) gaia() []Star {
	out := make([]Star, len(f.Stars))
	for i, s := range f.Stars {
		ra, dec := f.WCS.SkyOf(s.X, s.Y)
		out[i] = Star{RA: ra, Dec: dec, G: f.zp - 2.5*math.Log10(s.Flux*1000)}
	}
	return out
}

func (f *field) galaxy() Galaxy {
	ra, dec := f.WCS.SkyOf(f.NewX+4, f.NewY+3)
	return Galaxy{Name: "Test Galaxy", RA: ra, Dec: dec, Major: 1, Minor: 0.5}
}

func addGauss(d []float32, w, h int, x, y, flux, sigma float64) {
	r := int(math.Ceil(5 * sigma))
	norm := flux / (2 * math.Pi * sigma * sigma)
	for yy := max(0, int(y)-r); yy <= min(h-1, int(y)+r); yy++ {
		for xx := max(0, int(x)-r); xx <= min(w-1, int(x)+r); xx++ {
			dx, dy := float64(xx)-x, float64(yy)-y
			d[yy*w+xx] += float32(norm * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
		}
	}
}

// render draws sub i of night n, or the noiseless master with n < 0. Each
// sub's border moves with its dither, as a registered sub's does.
func (f *field) render(n, i int) []float32 {
	rng := rand.New(rand.NewChaCha8(sha256.Sum256(fmt.Appendf(nil, "%d/%d", n, i))))
	d := make([]float32, f.W*f.H)
	sigma, scale := 1.3, 1.0
	if n >= 0 {
		sigma = 1.15 + 0.25*rng.Float64()
		scale = 0.9 + 0.2*rng.Float64()
	}
	last := n == len(f.Nights)-1
	for k, s := range f.Stars {
		fl := s.Flux
		if k == f.Var && last {
			fl *= 1.6
		}
		addGauss(d, f.W, f.H, s.X, s.Y, fl*scale, sigma)
	}
	if last && f.hot {
		ox, oy := []float64{0.2, 1.1, 0.6, 1.4, 0.9, 0.3}[i%6], []float64{0.7, 0.1, 1.3, 0.8, 0.4, 1.2}[i%6]
		x0, y0 := int(f.HotX+ox), int(f.HotY+oy)
		fx, fy := f.HotX+ox-float64(x0), f.HotY+oy-float64(y0)
		for _, q := range [][3]float64{{0, 0, (1 - fx) * (1 - fy)}, {1, 0, fx * (1 - fy)}, {0, 1, (1 - fx) * fy}, {1, 1, fx * fy}} {
			d[(y0+int(q[1]))*f.W+x0+int(q[0])] += float32(0.6 * q[2])
		}
	}
	if last {
		addGauss(d, f.W, f.H, f.NewX, f.NewY, 0.6*scale, sigma)
		dt := f.subTime(n, i).Add(synthSubSeconds * time.Second / 2).Sub(f.subTime(n, 0)).Minutes()
		addGauss(d, f.W, f.H, f.AstX+f.AstVX*dt, f.AstY+f.AstVY*dt, 1.2*scale, sigma)
	}
	for k := range d {
		v := float64(d[k]) + f.sky
		if n >= 0 {
			v += rng.NormFloat64() * f.noise
		}
		d[k] = float32(v)
	}
	if n >= 0 {
		dx, dy := (n*5+i*3)%9-4, (n*3+i*7)%9-4
		for y := range f.H {
			for x := range f.W {
				if x < synthBorder+dx || y < synthBorder+dy || x >= f.W-synthBorder+dx || y >= f.H-synthBorder+dy {
					d[y*f.W+x] = 0
				}
			}
		}
	}
	return d
}

func (f *field) xisf(t *testing.T, n, i int) []byte {
	t.Helper()
	d := f.render(n, i)
	codes := make([]uint16, len(d))
	for k, v := range d {
		codes[k] = imagedata.Quantize16(v)
	}
	var b bytes.Buffer
	if err := imagedata.WriteXISF16(&b, f.W, f.H, codes, nil, imagedata.Pedestal16Properties()); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *field) masterFITS(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := imagedata.WriteFITS(&b, f.W, f.H, 1, f.render(-1, 0), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *field) subs(filter string) (night, ref []Sub) {
	id := 1
	for n, nd := range f.Nights {
		for i := range f.PerNight {
			s := Sub{FrameID: id, Filter: filter, Night: nd, Start: f.subTime(n, i), Exposure: synthSubSeconds, Weight: 1, Key: fmt.Sprintf("registered/T/%s/%d_%d.xisf", filter, n, i)}
			id++
			if n == len(f.Nights)-1 {
				night = append(night, s)
			} else {
				ref = append(ref, s)
			}
		}
	}
	return night, ref
}

func (f *field) input(t *testing.T) Input {
	t.Helper()
	files := map[string][]byte{}
	night, ref := f.subs(testFilter)
	for n := range f.Nights {
		for i := range f.PerNight {
			files[fmt.Sprintf("registered/T/%s/%d_%d.xisf", testFilter, n, i)] = f.xisf(t, n, i)
		}
	}
	m, err := imagedata.Decode(f.masterFITS(t))
	if err != nil {
		t.Fatal(err)
	}
	return Input{W: f.W, H: f.H, WCS: f.WCS, Gaia: f.gaia(), Threads: 2,
		Filters: []FilterInput{{Filter: testFilter, Anchors: Anchors(PlaneOf(m)), Night: night, Ref: ref}},
		Load: func(_ context.Context, s Sub) (*Plane, error) {
			b, ok := files[s.Key]
			if !ok {
				return nil, fmt.Errorf("no %s", s.Key)
			}
			im, err := imagedata.Decode(b)
			if err != nil {
				return nil, err
			}
			return PlaneOf(im), nil
		}}
}

func TestRunFindsSyntheticChanges(t *testing.T) {
	t.Parallel()
	f := newField(t)
	in := f.input(t)
	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("funnel %+v apsig %g hfr %.2f/%.2f conv %s tiles %d sum %.3f zp %v depth %v subdets %d astro %v", res.Funnel, res.ApSigma, res.HFRNight, res.HFRRef, res.Convolved, res.KernelTiles, res.KernelSum, res.ZeroPoint, res.Depth, res.SubDetections, res.AstroRMSArcsec)
	if res.KernelTiles == 0 || res.ZeroPoint == nil || res.Depth == nil {
		t.Fatalf("kernel tiles %d zero point %v depth %v", res.KernelTiles, res.ZeroPoint, res.Depth)
	}
	v := f.Stars[f.Var]
	foundVar, foundNew := false, false
	for _, c := range res.Candidates {
		t.Logf("candidate %+d at %.1f,%.1f snr %.1f gaia %v present %d/%d", c.Sign, c.X, c.Y, c.SNR, c.GaiaG != nil, c.PresentIn, c.SubsTested)
		if math.Hypot(c.X-v.X, c.Y-v.Y) < 1.5 && c.Sign > 0 && c.GaiaG != nil && c.DeltaMag != nil && *c.DeltaMag < -0.3 {
			foundVar = true
		}
		if math.Hypot(c.X-f.NewX, c.Y-f.NewY) < 1.5 && c.Sign > 0 && c.GaiaG == nil {
			foundNew = true
			if !c.SkyFixed || c.SubSharp > 2 || c.SubSharp <= 0 {
				t.Errorf("the new source moved with the sky %v, %.1f times a star's sharpness in its subs", c.SkyFixed, c.SubSharp)
			}
			if c.DeltaMag != nil {
				t.Errorf("the new source has a magnitude change %.2f against an empty reference", *c.DeltaMag)
			}
		}
	}
	if !foundVar {
		t.Errorf("the brightened star at %.1f,%.1f was not found", v.X, v.Y)
	}
	if !foundNew {
		t.Errorf("the new source at %.1f,%.1f was not found", f.NewX, f.NewY)
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("want 1 track, have %d", len(res.Tracks))
	}
	tr := res.Tracks[0]
	if len(tr.Points) < MoverMinHits || tr.Stamps == nil {
		t.Errorf("track has %d points, stamps %v", len(tr.Points), tr.Stamps != nil)
	}
	wantRate := math.Hypot(f.AstVX, f.AstVY) * 2
	if math.Abs(tr.RateArcmin-wantRate) > 0.05*wantRate {
		t.Errorf("rate %.3f\"/min, want %.3f", tr.RateArcmin, wantRate)
	}

	known := Known{Epoch: res.MidTime, Galaxies: []Galaxy{f.galaxy()}}
	vra, vdec := f.WCS.SkyOf(v.X, v.Y)
	known.Variables = []Variable{{Name: "V1 Test", Type: "EW", RA: vra, Dec: vdec}}
	ara, adec := f.WCS.SkyOf(tr.Position(res.MidTime))
	known.Bodies = []Body{{Name: "2000 AA", Number: "12345", Class: "MB>Inner", RA: ara, Dec: adec, V: 17,
		DRA: -f.AstVX * 2 * 60, DDec: -f.AstVY * 2 * 60}}
	labelled, tracks, _, _ := Classify(context.Background(), res, known, nil)
	classes := map[string]int{}
	for _, l := range labelled {
		classes[l.Class]++
	}
	if classes[ClassKnownVariable] != 1 || classes[ClassSupernova] != 1 {
		t.Errorf("classes %v, want one known variable and one supernova candidate", classes)
	}
	if tracks[0].Class != ClassKnownAsteroid || tracks[0].OffsetArc == nil || *tracks[0].OffsetArc > 3 {
		t.Errorf("track class %s offset %v", tracks[0].Class, tracks[0].OffsetArc)
	}
}

// TestRunComparesANightOfOneSubPerFilter replays North America Nebula
// Panel 2's 2025-12-17: two L subs and one each of R, G and B.
func TestRunComparesANightOfOneSubPerFilter(t *testing.T) {
	t.Parallel()
	f := newField(t)
	in := f.input(t)
	all := in.Filters[0]
	split := func(name string, night []Sub, ref []Sub) FilterInput {
		fi := FilterInput{Filter: name, Anchors: all.Anchors}
		for _, s := range night {
			s.Filter = name
			fi.Night = append(fi.Night, s)
		}
		for _, s := range ref {
			s.Filter = name
			fi.Ref = append(fi.Ref, s)
		}
		return fi
	}
	r, q := all.Ref, len(all.Ref)/4
	in.Filters = []FilterInput{
		split("Luminance", all.Night[0:2], r[0:q]),
		split("Red", all.Night[2:3], r[q:2*q]),
		split("Green", all.Night[3:4], r[2*q:3*q]),
		split("Blue", all.Night[4:5], r[3*q:]),
	}
	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	v := f.Stars[f.Var]
	found := false
	for _, c := range res.Candidates {
		found = found || (math.Hypot(c.X-v.X, c.Y-v.Y) < 1.5 && c.Sign > 0 && c.SubsTested == 5)
	}
	if res.NightSubs != 5 || len(res.Filters) != 4 || !found {
		t.Errorf("night subs %d, filters %v, brightened star found %v", res.NightSubs, res.Filters, found)
	}
	in.Filters = []FilterInput{
		split("Red", all.Night[0:1], r[0:q]),
		split("Green", all.Night[1:2], r[q:2*q]),
		split("Blue", all.Night[2:3], r[2*q:3*q]),
	}
	if _, err := Run(context.Background(), in); !errors.Is(err, ErrTooLittle) {
		t.Errorf("a night of one R, G and B sub has no filter to stack: %v", err)
	}
}

// TestRunHoldsADefectPixel replays Pelican Nebula Panel 2's night of
// 2026-09-14: a defect pixel moved by a pixel between subs by their
// registration is less sharp in the stack than in each sub, where it is
// one pixel, and is held at low confidence as a new source.
func TestRunHoldsADefectPixel(t *testing.T) {
	t.Parallel()
	f := newField(t)
	f.hot = true
	f.HotX, f.HotY = f.emptySpot(t, 1400, 900)
	res, err := Run(context.Background(), f.input(t))
	if err != nil {
		t.Fatal(err)
	}
	held := false
	for _, c := range res.Candidates {
		if math.Hypot(c.X-f.HotX-1.2, c.Y-f.HotY-1.2) < 2 {
			held = c.LowConf
		}
		if math.Hypot(c.X-f.NewX, c.Y-f.NewY) < 1.5 && c.LowConf {
			t.Errorf("the new star was held: %+v", c)
		}
	}
	if !held {
		for _, r := range res.Rejected {
			if math.Hypot(r.X-f.HotX, r.Y-f.HotY) < 3 {
				t.Logf("rejected %+v", r)
			}
		}
		t.Error("the defect pixel was not found and held")
	}
}
