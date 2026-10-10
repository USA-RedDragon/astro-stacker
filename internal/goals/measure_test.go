package goals

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

const (
	synthW        = 800
	synthH        = 600
	synthExposure = 300.0
	synthSky      = 0.05
	synthNoise    = 0.01
)

type synthStar struct{ x, y, amp float64 }

type scene struct {
	nebula func(x, y float64) float64
	stars  []synthStar
}

func defaultScene(rng *rand.Rand) scene {
	s := scene{nebula: func(x, y float64) float64 {
		dx, dy := x-400, y-300
		u := (dx*dx + dy*dy) / (250 * 250)
		if u >= 1 {
			return 0
		}
		return 0.03 * (1 - u) * (1 - u)
	}}
	for range 40 {
		s.stars = append(s.stars, synthStar{x: 20 + rng.Float64()*(synthW-40), y: 20 + rng.Float64()*(synthH-40), amp: 0.2 + 0.4*rng.Float64()})
	}
	return s
}

func (s scene) model() []float32 {
	p := make([]float32, synthW*synthH)
	for y := range synthH {
		for x := range synthW {
			p[y*synthW+x] = float32(synthSky + s.nebula(float64(x), float64(y)))
		}
	}
	for _, st := range s.stars {
		for y := int(st.y) - 6; y <= int(st.y)+6; y++ {
			for x := int(st.x) - 6; x <= int(st.x)+6; x++ {
				if x < 0 || y < 0 || x >= synthW || y >= synthH {
					continue
				}
				dx, dy := float64(x)-st.x, float64(y)-st.y
				p[y*synthW+x] += float32(st.amp * math.Exp(-(dx*dx+dy*dy)/(2*1.5*1.5)))
			}
		}
	}
	return p
}

func (s scene) sub(rng *rand.Rand, model []float32, extra []float32) []float32 {
	off := float32(0.002 * rng.NormFloat64())
	p := make([]float32, len(model))
	for i, v := range model {
		p[i] = v + off + float32(synthNoise*rng.NormFloat64())
		if extra != nil {
			p[i] += extra[i]
		}
	}
	return p
}

func runScene(t *testing.T, s scene, n int, opts Options) (Result, []float32) {
	t.Helper()
	rng := rand.New(rand.NewPCG(1, 2))
	model := s.model()
	subs := make([]Sub, n)
	for i := range subs {
		subs[i] = Sub{Exposure: synthExposure, Weight: synthExposure, Effective: synthExposure}
	}
	m, err := NewMeasurer(synthW, synthH, subs, opts)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		b, err := Bin(s.sub(rng, model, nil), synthW, synthH, synthExposure, DefaultSaturation)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Add(i, b); err != nil {
			t.Fatal(err)
		}
	}
	res, err := m.Finish(float64(n) * synthExposure / 3600)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := Bin(model, synthW, synthH, synthExposure, DefaultSaturation)
	if err != nil {
		t.Fatal(err)
	}
	return res, clean.Val
}

func maskedMedian(v []float32, mask []bool) float64 {
	var s []float64
	for i, m := range mask {
		if m {
			s = append(s, float64(v[i]))
		}
	}
	return median(s)
}

func TestLevels(t *testing.T) {
	t.Parallel()
	cases := map[int][]int{7: nil, 8: {4}, 16: {4, 8}, 20: {4, 8, 10}, 200: {4, 8, 16, 32, 64, 100}}
	for n, want := range cases {
		if got := Levels(n); !slices.Equal(got, want) {
			t.Errorf("Levels(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestSubsetDeterministic(t *testing.T) {
	t.Parallel()
	a := Subset(300, 200, Seed("M31", "Red"))
	b := Subset(300, 200, Seed("M31", "Red"))
	c := Subset(300, 200, Seed("M31", "Green"))
	if len(a) != 200 || !slices.Equal(a, b) || slices.Equal(a, c) || !slices.IsSorted(a) {
		t.Fatalf("subset not deterministic or sorted")
	}
	if got := Subset(50, 200, 1); len(got) != 50 {
		t.Fatalf("small stack subset %d", len(got))
	}
}

func TestInsufficientData(t *testing.T) {
	t.Parallel()
	_, err := NewMeasurer(synthW, synthH, make([]Sub, MinSubs-1), Options{})
	if !errors.Is(err, ErrInsufficientData) {
		t.Fatalf("err = %v", err)
	}
}

func TestBinDropsInvalid(t *testing.T) {
	t.Parallel()
	p := make([]float32, 8*8)
	for i := range p {
		p[i] = 0.1
	}
	p[0] = 0
	p[1] = 0.95
	p[2] = 0.3
	b, err := Bin(p, 8, 8, 10, DefaultSaturation)
	if err != nil {
		t.Fatal(err)
	}
	if b.Count[0] != 14 || b.Count[1] != 16 {
		t.Fatalf("counts %v", b.Count)
	}
	want := float32((0.1*13 + 0.3) / 14 / 10)
	if math.Abs(float64(b.Val[0]+0.1/10-want)) > 1e-6 {
		t.Fatalf("value %v", b.Val[0])
	}
}

func TestMeasureSyntheticSNR(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(7, 7))
	s := defaultScene(rng)
	n := 64
	res, clean := runScene(t, s, n, Options{Seed: Seed("Synth", "H-a")})
	sigmaBin := synthNoise / NoiseBin / synthExposure
	wantNoise := sigmaBin / math.Sqrt(float64(n))
	if r := res.NoiseNow / wantNoise; r < 0.9 || r > 1.1 {
		t.Errorf("noise now %g, want %g (ratio %.3f)", res.NoiseNow, wantNoise, r)
	}
	wantSignal := maskedMedian(clean, res.Masks.Band) - maskedMedian(clean, res.Masks.Background)
	wantSNR := wantSignal / wantNoise
	if r := res.SNR / wantSNR; r < 0.9 || r > 1.1 {
		t.Errorf("SNR %.2f, want %.2f", res.SNR, wantSNR)
	}
	if res.SkyStep > 2 || res.NebFraction > FrameFillingNebula {
		t.Errorf("sky step %.2f, nebula fraction %.2f", res.SkyStep, res.NebFraction)
	}
	if res.NoiseB > 0.35*res.NoiseNow {
		t.Errorf("pure noise floor b = %g against sigma now %g", res.NoiseB, res.NoiseNow)
	}
	if res.Levels != 4 || len(res.Points) != 4*DrawsPerLevel {
		t.Errorf("levels %d points %d", res.Levels, len(res.Points))
	}
	if res.HeldOutErrPct == nil || math.Abs(*res.HeldOutErrPct) > 10 {
		t.Errorf("held out %v", res.HeldOutErrPct)
	}
	if res.LowConfidence {
		t.Errorf("low confidence: %s", res.LowReason)
	}
	if res.NoiseMask != NoiseMaskFaint {
		t.Errorf("noise mask %s", res.NoiseMask)
	}
	pure := 100 * (1 - math.Sqrt(5.3333/6.3333))
	if math.Abs(res.GainPerHourPct-pure) > 1.5 {
		t.Errorf("gain per hour %.2f, pure %.2f", res.GainPerHourPct, pure)
	}
	for _, st := range s.stars {
		bx, by := int(st.x)/NoiseBin, int(st.y)/NoiseBin
		i := by*res.BW + bx
		if !res.Masks.Stars[i] || res.Masks.Band[i] || res.Masks.Background[i] {
			t.Errorf("star at %.0f,%.0f not masked", st.x, st.y)
		}
	}
}

func TestMeasureRegion(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(7, 7))
	s := defaultScene(rng)
	region := []Point{{0.55, 0.3}, {0.85, 0.3}, {0.85, 0.75}, {0.55, 0.75}}
	res, clean := runScene(t, s, 16, Options{Seed: 3, Region: region})
	if res.NoiseMask != NoiseMaskRegion {
		t.Fatalf("noise mask %s", res.NoiseMask)
	}
	inside := rasterize(region, res.BW, res.BH, synthW, synthH)
	for i, b := range res.Masks.Band {
		if b && (!inside[i] || res.Masks.Stars[i]) {
			t.Fatalf("band bin %d outside the region or on a star", i)
		}
	}
	if c := count(res.Masks.Band); c < 3000 {
		t.Fatalf("region band has %d bins", c)
	}
	want := maskedMedian(clean, res.Masks.Band) - maskedMedian(clean, res.Masks.Background)
	if math.Abs(res.Signal-want) > 0.1*want {
		t.Fatalf("region signal %g, want %g", res.Signal, want)
	}
}

func TestLowConfidenceFrameFilling(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(9, 9))
	s := defaultScene(rng)
	s.nebula = func(x, y float64) float64 {
		if x+y < 300 {
			return 0
		}
		return 0.003 * math.Min(1, (x+y-300)/100)
	}
	res, _ := runScene(t, s, 8, Options{Seed: 4})
	if !res.LowConfidence || res.SkyStep <= SkyStepLimit {
		t.Fatalf("low confidence %v (%s), sky step %.2f", res.LowConfidence, res.LowReason, res.SkyStep)
	}
}

func TestLowConfidenceHalfFrameNebula(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(9, 3))
	s := defaultScene(rng)
	s.nebula = func(x, _ float64) float64 {
		return 0.003 * math.Max(0, math.Min(1, (x-300)/40))
	}
	res, _ := runScene(t, s, 8, Options{Seed: 8})
	if !res.LowConfidence || res.NebFraction <= FrameFillingNebula || res.SkyStep > SkyStepLimit {
		t.Fatalf("low confidence %v (%s), nebula fraction %.2f, sky step %.2f", res.LowConfidence, res.LowReason, res.NebFraction, res.SkyStep)
	}
}

func TestLowConfidenceNoNebula(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(5, 5))
	s := defaultScene(rng)
	s.nebula = func(float64, float64) float64 { return 0 }
	res, _ := runScene(t, s, 8, Options{Seed: 5})
	if !res.LowConfidence || res.BandFraction >= MinBandFraction || res.NoiseMask != NoiseMaskBg {
		t.Fatalf("low confidence %v (%s), band %.3f, noise mask %s", res.LowConfidence, res.LowReason, res.BandFraction, res.NoiseMask)
	}
}

func TestFitNoise(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 1))
	for _, tc := range []struct{ a, b float64 }{{1, 0}, {1, 0.15}, {2.5, 0.4}} {
		pts := make([]DrawPoint, 0, 18)
		for _, n := range []int{4, 8, 16, 32, 64, 128} {
			for range 3 {
				h := float64(n) * 0.1
				s := NoiseAt(tc.a, tc.b, h) * (1 + 0.005*rng.NormFloat64())
				pts = append(pts, DrawPoint{N: n, Hours: h, Sigma: s})
			}
		}
		a, b, ok := FitNoise(pts)
		if !ok || math.Abs(a-tc.a) > 0.05*tc.a || math.Abs(b-tc.b) > 0.1*tc.b+0.15*NoiseAt(tc.a, tc.b, 12.8) {
			t.Errorf("fit (%g, %g) gave (%g, %g)", tc.a, tc.b, a, b)
		}
	}
}

func TestHeldOut(t *testing.T) {
	t.Parallel()
	pts := make([]DrawPoint, 0, 4)
	for _, n := range []int{4, 8, 16, 32} {
		pts = append(pts, DrawPoint{N: n, Hours: float64(n), Sigma: 1 / math.Sqrt(float64(n))})
	}
	e, ok := heldOut(pts, 64)
	if !ok || math.Abs(e) > 0.5 {
		t.Fatalf("held out %v %v", e, ok)
	}
	if _, ok := heldOut(pts[:3], 32); ok {
		t.Fatal("held out with two lower levels")
	}
}

func TestRasterize(t *testing.T) {
	t.Parallel()
	poly := []Point{{0, 0}, {0.5, 0}, {0.5, 0.5}, {0, 0.5}}
	m := rasterize(poly, 10, 10, 40, 40)
	if count(m) != 25 || !m[0] || m[5] || m[50] {
		t.Fatalf("raster %d bins", count(m))
	}
}

func TestGaussianPreservesConstant(t *testing.T) {
	t.Parallel()
	in := make([]float64, 30*20)
	for i := range in {
		in[i] = 3
	}
	out := gaussian(in, 30, 20, 4)
	for _, v := range out {
		if math.Abs(v-3) > 1e-9 {
			t.Fatalf("got %v", v)
		}
	}
}
