package discoveries

import (
	"context"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/klauspost/compress/zstd"
)

const m22Side = 41

// readM22Blue reads 41x41 px around candidate 230822 (M 22, Blue,
// 2021-06-29) in six Blue subs, aligned on the spot at (20, 20),
// background subtracted and scaled to the first: the reference's subs
// ID224298 (2021-05-23 08:56) and ID224303 (09:09, a cosmic ray on the
// spot), then the night's four.
func readM22Blue(t *testing.T) []*Plane {
	t.Helper()
	f, err := os.Open("testdata/m22-blue-2021-06-29.f32.zst")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := make([]*Plane, 6)
	for i := range out {
		out[i] = NewPlane(m22Side, m22Side)
		if err := binary.Read(zr, binary.LittleEndian, out[i].Data); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func planeNoise(p *Plane) float64 {
	v := make([]float64, 0, len(p.Data))
	for _, x := range p.Data {
		v = append(v, float64(x))
	}
	return robustStd(v)
}

func stackOf(t *testing.T, ps []*Plane) *Plane {
	t.Helper()
	load := func(_ context.Context, i int) (*Plane, float64, error) {
		n := planeNoise(ps[i])
		c := &Plane{W: ps[i].W, H: ps[i].H, Data: append([]float32(nil), ps[i].Data...)}
		return c, 1 / (n * n), nil
	}
	st, err := ClippedMean(context.Background(), ps[0].W, ps[0].H, len(ps), load, 1)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestTwoSubReferenceDropsTheCosmicRay is candidate 230822 (M 22, Blue,
// 2021-06-29): the reference of two subs held a cosmic ray of the 09:09 sub
// on the spot, and the night read a fade of 0.97 mag; a plain mean of the
// two reads 1.5 mag here. The reference must hold the light of the clean sub
// there within 15%, and the night fade by no more than 0.4 mag, as the
// clean sub alone gives 0.2 with the night's seeing.
func TestTwoSubReferenceDropsTheCosmicRay(t *testing.T) {
	t.Parallel()
	ps := readM22Blue(t)
	ref, night := stackOf(t, ps[:2]), stackOf(t, ps[2:])
	at := func(p *Plane) float64 { return Aperture(p, 20, 20, 2.5) }
	clean, ray, mean := at(ps[0]), at(ps[1]), 0.5*(at(ps[0])+at(ps[1]))
	t.Logf("spot: clean sub %.0f, cosmic ray sub %.0f, night %.0f, reference %.0f (plain mean %.0f)", clean, ray, at(night), at(ref), mean)
	if ray < 2.5*clean {
		t.Fatalf("the fixture lost the cosmic ray: %.0f against %.0f", ray, clean)
	}
	if math.Abs(at(ref)/clean-1) > 0.15 {
		t.Errorf("reference %.0f at the spot, clean sub %.0f", at(ref), clean)
	}
	if dm := -2.5 * math.Log10(at(night)/at(ref)); dm > 0.4 {
		t.Errorf("night fades by %.2f mag from the reference", dm)
	}
}

// TestTwoSubReferenceKeepsStars: two subs that differ only by their noise
// and seeing stack to their mean, a 3x2 px cosmic ray in one is left out,
// and a clean third sub shows no fade against them.
func TestTwoSubReferenceKeepsStars(t *testing.T) {
	t.Parallel()
	const n, sky = 64, 1.0
	rng := rand.New(rand.NewPCG(2, 3))
	sub := func(sigma float64) *Plane {
		p := NewPlane(n, n)
		for y := range n {
			for x := range n {
				dx, dy := float64(x)-32, float64(y)-32
				star := 400 / (2 * math.Pi * sigma * sigma) * math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma))
				p.Data[y*n+x] = float32(star + sky*rng.NormFloat64())
			}
		}
		return p
	}
	a, b, c := sub(1.6), sub(2.2), sub(1.9)
	for _, k := range [][2]int{{40, 20}, {41, 20}, {42, 20}, {40, 21}, {41, 21}, {42, 21}} {
		b.Data[k[1]*n+k[0]] += 60
	}
	want := 0.5 * (Aperture(a, 32, 32, 6) + Aperture(b, 32, 32, 6))
	ref := stackOf(t, []*Plane{a, b})
	if got := Aperture(ref, 32, 32, 6); math.Abs(got/want-1) > 0.02 {
		t.Errorf("star %.1f in the reference, %.1f in the subs' mean", got, want)
	}
	if v := Aperture(ref, 41, 20.5, 1.5); v > 5*sky {
		t.Errorf("cosmic ray %.1f in the reference", v)
	}
	var dropped int
	for _, v := range ref.Data {
		if math.IsNaN(float64(v)) {
			dropped++
		}
	}
	if dropped > 0 {
		t.Errorf("%d pixels lost their coverage", dropped)
	}
	if d := Aperture(c, 41, 20.5, 1.5) - Aperture(ref, 41, 20.5, 1.5); d < -5*sky {
		t.Errorf("a clean sub fades by %.1f at the cosmic ray", d)
	}
}
