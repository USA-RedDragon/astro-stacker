package goals

import (
	"math"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
)

func testWCS(w, h int) WCS {
	s := DefaultPixelScale / 3600
	return WCS{RA0: 180, Dec0: 45, PX0: float64(w+1) / 2, PY0: float64(h+1) / 2, CD: [2][2]float64{{-s, 0}, {0, s}}}
}

func TestParseWCS(t *testing.T) {
	t.Parallel()
	kw := frameheader.Keywords{"CTYPE1": "RA---TAN", "CTYPE2": "DEC--TAN", "CRVAL1": "10", "CRVAL2": "41", "CRPIX1": "100", "CRPIX2": "50",
		"CD1_1": strconv.FormatFloat(-1.915/3600, 'g', -1, 64), "CD1_2": "0", "CD2_1": "0", "CD2_2": strconv.FormatFloat(1.915/3600, 'g', -1, 64)}
	g, ok := ParseWCS(kw)
	if !ok || math.Abs(g.PixelScaleArcsec()-1.915) > 1e-9 {
		t.Fatalf("wcs %+v %v", g, ok)
	}
	x, y, ok := g.ToPixel(g.ToSky(321, 123))
	if !ok || math.Abs(x-321) > 1e-6 || math.Abs(y-123) > 1e-6 {
		t.Fatalf("round trip %v %v", x, y)
	}
	if _, ok := ParseWCS(frameheader.Keywords{"CRVAL1": "1"}); ok {
		t.Fatal("parsed a header without a solution")
	}
}

func TestZeroPoint(t *testing.T) {
	t.Parallel()
	const w, h = 1600, 1200
	const zp = 21.3
	g := testWCS(w, h)
	bw, bh := w/NoiseBin, h/NoiseBin
	res := Result{BW: bw, BH: bh, Mean: make([]float64, bw*bh), Coverage: make([]float32, bw*bh)}
	rng := rand.New(rand.NewPCG(3, 4))
	for i := range res.Mean {
		res.Mean[i] = 1e-7 * rng.NormFloat64()
		res.Coverage[i] = 1
	}
	var stars []CatalogStar
	for gy := 30; gy < bh-30; gy += 40 {
		for gx := 30; gx < bw-30; gx += 40 {
			bx, by := float64(gx)+rng.Float64()-0.5, float64(gy)+rng.Float64()-0.5
			mag := 9 + 5*rng.Float64()
			flux := math.Pow(10, -0.4*(mag-zp))
			col := (bx+0.5)*NoiseBin - 0.5
			row := (by+0.5)*NoiseBin - 0.5
			ra, dec := g.ToSky(col+1, float64(h)-row)
			stars = append(stars, CatalogStar{RA: ra, Dec: dec, G: mag})
			const sig = 0.7
			norm := flux / (NoiseBin * NoiseBin) / (2 * math.Pi * sig * sig)
			for y := gy - 5; y <= gy+5; y++ {
				for x := gx - 5; x <= gx+5; x++ {
					dx, dy := float64(x)-bx, float64(y)-by
					res.Mean[y*bw+x] += norm * math.Exp(-(dx*dx+dy*dy)/(2*sig*sig))
				}
			}
		}
	}
	stars = append(stars, CatalogStar{RA: 0, Dec: -45, G: 10})
	got, n, ok := ZeroPoint(res, h, g, stars)
	if !ok || n < MinZeroPointStars || math.Abs(got-zp) > 0.03 {
		t.Fatalf("zero point %.3f from %d stars (%v), want %.2f", got, n, ok, zp)
	}
	for i := range res.Coverage {
		res.Coverage[i] = 0.5
	}
	if _, _, ok := ZeroPoint(res, h, g, stars); ok {
		t.Fatal("zero point from saturated or partly covered stars")
	}
}

func TestDepth(t *testing.T) {
	t.Parallel()
	d, ok := Depth(20, 1e-6, 2)
	want := 20 - 2.5*math.Log10(3e-6/4)
	if !ok || math.Abs(d-want) > 1e-9 {
		t.Fatalf("depth %v, want %v", d, want)
	}
	if _, ok := Depth(20, 0, 2); ok {
		t.Fatal("depth with no noise")
	}
}
