package goals

import (
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
)

const (
	DefaultPixelScale   = 1.915
	MinZeroPointStars   = 10
	CatalogBrightG      = 9.0
	CatalogFaintG       = 14.0
	apertureBins        = 3.0
	annulusInnerBins    = 5.0
	annulusOuterBins    = 8.0
	apertureFullCover   = 0.95
	apertureMinSNR      = 20.0
	apertureCentreSlack = 1.5
)

const degree = math.Pi / 180

type WCS struct {
	RA0, Dec0 float64
	PX0, PY0  float64
	CD        [2][2]float64
}

func ParseWCS(kw frameheader.Keywords) (WCS, bool) {
	c1, c2 := strings.ToUpper(kw.String("CTYPE1")), strings.ToUpper(kw.String("CTYPE2"))
	if c1 != "" && (!strings.HasPrefix(c1, "RA--") || !strings.HasPrefix(c2, "DEC-")) {
		return WCS{}, false
	}
	g := WCS{RA0: kw.Float("CRVAL1"), Dec0: kw.Float("CRVAL2"), PX0: kw.Float("CRPIX1"), PY0: kw.Float("CRPIX2")}
	get := func(name string, def float64) float64 {
		if v := kw.Float(name); !math.IsNaN(v) {
			return v
		}
		return def
	}
	switch {
	case !math.IsNaN(kw.Float("CD1_1")) || !math.IsNaN(kw.Float("CD2_2")):
		g.CD = [2][2]float64{{get("CD1_1", 0), get("CD1_2", 0)}, {get("CD2_1", 0), get("CD2_2", 0)}}
	case !math.IsNaN(kw.Float("CDELT1")) && !math.IsNaN(kw.Float("CDELT2")):
		d1, d2 := kw.Float("CDELT1"), kw.Float("CDELT2")
		if _, ok := kw["PC1_1"]; ok {
			g.CD = [2][2]float64{{d1 * get("PC1_1", 1), d1 * get("PC1_2", 0)}, {d2 * get("PC2_1", 0), d2 * get("PC2_2", 1)}}
		} else {
			rot := get("CROTA2", 0) * degree
			g.CD = [2][2]float64{{d1 * math.Cos(rot), -d2 * math.Sin(rot)}, {d1 * math.Sin(rot), d2 * math.Cos(rot)}}
		}
	default:
		return WCS{}, false
	}
	for _, v := range []float64{g.RA0, g.Dec0, g.PX0, g.PY0} {
		if math.IsNaN(v) {
			return WCS{}, false
		}
	}
	if g.det() == 0 {
		return WCS{}, false
	}
	return g, true
}

func (g WCS) det() float64 { return g.CD[0][0]*g.CD[1][1] - g.CD[0][1]*g.CD[1][0] }

func (g WCS) PixelScaleArcsec() float64 {
	return math.Sqrt(math.Abs(g.det())) * 3600
}

func (g WCS) ToPixel(ra, dec float64) (x, y float64, ok bool) {
	d0, d, da := g.Dec0*degree, dec*degree, (ra-g.RA0)*degree
	cosc := math.Sin(d0)*math.Sin(d) + math.Cos(d0)*math.Cos(d)*math.Cos(da)
	if cosc <= 0 {
		return 0, 0, false
	}
	xi := math.Cos(d) * math.Sin(da) / cosc / degree
	eta := (math.Cos(d0)*math.Sin(d) - math.Sin(d0)*math.Cos(d)*math.Cos(da)) / cosc / degree
	det := g.det()
	dx := (g.CD[1][1]*xi - g.CD[0][1]*eta) / det
	dy := (-g.CD[1][0]*xi + g.CD[0][0]*eta) / det
	return dx + g.PX0, dy + g.PY0, true
}

func (g WCS) ToSky(x, y float64) (ra, dec float64) {
	dx, dy := x-g.PX0, y-g.PY0
	xi := (g.CD[0][0]*dx + g.CD[0][1]*dy) * degree
	eta := (g.CD[1][0]*dx + g.CD[1][1]*dy) * degree
	d0 := g.Dec0 * degree
	den := math.Cos(d0) - eta*math.Sin(d0)
	ra = math.Mod(g.RA0+math.Atan2(xi, den)/degree+360, 360)
	dec = math.Atan2(math.Sin(d0)+eta*math.Cos(d0), math.Hypot(xi, den)) / degree
	return ra, dec
}

func (g WCS) Field(w, h int) (ra, dec, radius float64) {
	ra, dec = g.ToSky(float64(w+1)/2, float64(h+1)/2)
	radius = math.Hypot(float64(w), float64(h)) / 2 * g.PixelScaleArcsec() / 3600
	return ra, dec, radius
}

type CatalogStar struct {
	RA  float64 `json:"ra"`
	Dec float64 `json:"dec"`
	G   float64 `json:"g"`
}

func ZeroPoint(res Result, h int, g WCS, stars []CatalogStar) (float64, int, bool) {
	bw, bh := res.BW, res.BH
	type pos struct{ x, y, g float64 }
	var ps []pos
	for _, s := range stars {
		if s.G < CatalogBrightG || s.G > CatalogFaintG {
			continue
		}
		fx, fy, ok := g.ToPixel(s.RA, s.Dec)
		if !ok {
			continue
		}
		col, row := fx-1, float64(h)-fy
		bx := (col+0.5)/NoiseBin - 0.5
		by := (row+0.5)/NoiseBin - 0.5
		if bx < annulusOuterBins+1 || by < annulusOuterBins+1 || bx > float64(bw)-annulusOuterBins-2 || by > float64(bh)-annulusOuterBins-2 {
			continue
		}
		ps = append(ps, pos{bx, by, s.G})
	}
	var zps []float64
	for i, p := range ps {
		crowded := false
		for j, q := range ps {
			if i != j && math.Hypot(p.x-q.x, p.y-q.y) < annulusOuterBins {
				crowded = true
				break
			}
		}
		if crowded {
			continue
		}
		flux, ok := apertureFlux(res, p.x, p.y)
		if !ok {
			continue
		}
		zps = append(zps, p.g+2.5*math.Log10(flux))
	}
	if len(zps) < MinZeroPointStars {
		return 0, len(zps), false
	}
	return median(zps), len(zps), true
}

func apertureFlux(res Result, cx, cy float64) (float64, bool) {
	bw := res.BW
	var ring []float64
	type px struct {
		i    int
		x, y int
	}
	var ap []px
	r := int(annulusOuterBins) + 1
	ix, iy := int(math.Round(cx)), int(math.Round(cy))
	for y := iy - r; y <= iy+r; y++ {
		for x := ix - r; x <= ix+r; x++ {
			i := y*bw + x
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			switch {
			case d <= apertureBins:
				if res.Coverage[i] < apertureFullCover {
					return 0, false
				}
				ap = append(ap, px{i, x, y})
			case d >= annulusInnerBins && d <= annulusOuterBins && float64(res.Coverage[i]) >= CoverageMin:
				ring = append(ring, res.Mean[i])
			}
		}
	}
	if len(ring) < 20 || len(ap) == 0 {
		return 0, false
	}
	bg := median(ring)
	noise := mad(ring)
	var flux float64
	peak := ap[0]
	for _, p := range ap {
		flux += res.Mean[p.i] - bg
		if res.Mean[p.i] > res.Mean[peak.i] {
			peak = p
		}
	}
	if math.Hypot(float64(peak.x)-cx, float64(peak.y)-cy) > apertureCentreSlack {
		return 0, false
	}
	flux *= NoiseBin * NoiseBin
	sigma := noise * NoiseBin * NoiseBin * math.Sqrt(float64(len(ap)))
	if !(flux > 0) || (sigma > 0 && flux < apertureMinSNR*sigma) {
		return 0, false
	}
	return flux, true
}

func Depth(zeroPoint, noiseNow, pixelScale float64) (float64, bool) {
	if !(noiseNow > 0) || !(pixelScale > 0) {
		return 0, false
	}
	f := DepthSNR * noiseNow / (pixelScale * pixelScale)
	return zeroPoint - 2.5*math.Log10(f), true
}

const (
	SystemGaiaG  = "gaia-g"
	SystemXPAB   = "xp-ab"
	BandGaiaG    = "Gaia G"
	speedOfLight = 299792458.0
	abOffset     = 56.10
)

type LineBand struct {
	Filter string
	Lambda float64
	Label  string
}

func lineBands() []LineBand {
	return []LineBand{
		{Filter: "H-a", Lambda: 656.28, Label: "H-α 656.3 nm AB (Gaia XP)"},
		{Filter: "O-III", Lambda: 500.69, Label: "O-III 500.7 nm AB (Gaia XP)"},
		{Filter: "S-II", Lambda: 671.65, Label: "S-II 671.6 nm AB (Gaia XP)"},
	}
}

func NarrowbandLine(filter string) (LineBand, bool) {
	for _, b := range lineBands() {
		if b.Filter == filter {
			return b, true
		}
	}
	return LineBand{}, false
}

func IsNarrowband(filter string) bool {
	_, ok := NarrowbandLine(filter)
	return ok
}

type XPStar struct {
	RA  float64 `json:"ra"`
	Dec float64 `json:"dec"`
	FB  float64 `json:"b"`
	FV  float64 `json:"v"`
	FR  float64 `json:"r"`
	FI  float64 `json:"i"`
}

func xpPivots() [4]float64 { return [4]float64{438, 545, 641, 798} }

func (s XPStar) ABAt(lambda float64) (float64, bool) {
	pivots := xpPivots()
	flux := [4]float64{s.FB, s.FV, s.FR, s.FI}
	for k := range 3 {
		lo, hi := pivots[k], pivots[k+1]
		if lambda < lo || lambda > hi {
			continue
		}
		a, b := flux[k], flux[k+1]
		if !(a > 0) || !(b > 0) {
			return 0, false
		}
		t := (lambda - lo) / (hi - lo)
		fl := math.Exp(math.Log(a) + t*(math.Log(b)-math.Log(a)))
		lm := lambda * 1e-9
		fnu := fl * 1e9 * lm * lm / speedOfLight
		return -2.5*math.Log10(fnu) - abOffset, true
	}
	return 0, false
}

func LineStars(stars []XPStar, lambda float64) []CatalogStar {
	out := make([]CatalogStar, 0, len(stars))
	for _, s := range stars {
		if m, ok := s.ABAt(lambda); ok {
			out = append(out, CatalogStar{RA: s.RA, Dec: s.Dec, G: m})
		}
	}
	return out
}

func SkyBrightness(zeroPoint, skyRate, pixelScale float64) (float64, bool) {
	if !(skyRate > 0) || !(pixelScale > 0) {
		return 0, false
	}
	return zeroPoint - 2.5*math.Log10(skyRate/(pixelScale*pixelScale)), true
}
