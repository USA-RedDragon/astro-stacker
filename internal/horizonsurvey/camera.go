package horizonsurvey

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	PierEast    = "pierEast"
	PierWest    = "pierWest"
	PierUnknown = "pierUnknown"
)

var (
	ErrNoGeometry = errors.New("frame has no plate solve and no camera calibration for its pier side")
	ErrBadWCS     = errors.New("invalid WCS")
)

type Geometry interface {
	Dir(x, y float64) Vec
	Size() (int, int)
}

type Calibration struct {
	Theta    float64 `json:"theta"`
	PierSide string  `json:"pier_side"`
	Parity   int     `json:"parity"`
	Scale    float64 `json:"scale_arcsec"`
	Frames   int     `json:"frames"`
	Spread   float64 `json:"theta_spread"`
	Source   string  `json:"source"`
}

func (c Calibration) Valid() bool {
	return (c.Parity == 1 || c.Parity == -1) && c.Scale > 0 && (c.PierSide == PierEast || c.PierSide == PierWest)
}

func (c Calibration) ThetaFor(pier string) (float64, bool) {
	switch {
	case !c.Valid():
		return 0, false
	case pier == c.PierSide:
		return c.Theta, true
	case pier == PierEast || pier == PierWest:
		return wrap360(c.Theta + 180), true
	default:
		return 0, false
	}
}

type MountGeometry struct {
	W, H     int
	Lat      float64
	HA, Dec  float64
	Theta    float64
	Parity   int
	ScaleDeg float64
	c, r, u  Vec
}

func NewMountGeometry(w, h int, lat, ha, dec, theta float64, parity int, scaleArcsec float64) *MountGeometry {
	g := &MountGeometry{W: w, H: h, Lat: lat, HA: ha, Dec: dec, Theta: theta, Parity: parity, ScaleDeg: scaleArcsec / 3600}
	north := Vec{-math.Sin(dec*rad) * math.Cos(ha*rad), math.Sin(dec*rad) * math.Sin(ha*rad), math.Cos(dec * rad)}
	east := Vec{math.Sin(ha * rad), math.Cos(ha * rad), 0}
	st, ct := math.Sin(theta*rad), math.Cos(theta*rad)
	k := g.ScaleDeg * rad
	g.c = EqVec(ha, dec)
	g.u = north.Scale(ct).Add(east.Scale(st)).Scale(k)
	g.r = north.Scale(st).Sub(east.Scale(ct)).Scale(float64(parity) * k)
	return g
}

func (g *MountGeometry) Size() (int, int) { return g.W, g.H }

func (g *MountGeometry) Dir(x, y float64) Vec {
	dx := x - float64(g.W-1)/2
	dy := float64(g.H-1)/2 - y
	return EqToHor(g.c.Add(g.r.Scale(dx)).Add(g.u.Scale(dy)).Unit(), g.Lat)
}

type WCS struct {
	CRPIX1, CRPIX2 float64
	CRVAL1, CRVAL2 float64
	CD             [2][2]float64
}

func ParseWCS(cards map[string]string) (WCS, bool) {
	num := func(k string) (float64, bool) {
		v, ok := cards[k]
		if !ok {
			return 0, false
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(v, "D", "E")), 64)
		return f, err == nil
	}
	var w WCS
	var ok [4]bool
	w.CRPIX1, ok[0] = num("CRPIX1")
	w.CRPIX2, ok[1] = num("CRPIX2")
	w.CRVAL1, ok[2] = num("CRVAL1")
	w.CRVAL2, ok[3] = num("CRVAL2")
	if !ok[0] || !ok[1] || !ok[2] || !ok[3] {
		return w, false
	}
	c11, a := num("CD1_1")
	c12, b := num("CD1_2")
	c21, c := num("CD2_1")
	c22, d := num("CD2_2")
	if a && b && c && d {
		w.CD = [2][2]float64{{c11, c12}, {c21, c22}}
		return w, w.CD[0][0]*w.CD[1][1]-w.CD[0][1]*w.CD[1][0] != 0
	}
	d1, a := num("CDELT1")
	d2, b := num("CDELT2")
	if !a || !b {
		return w, false
	}
	rot, _ := num("CROTA2")
	cr, sr := math.Cos(rot*rad), math.Sin(rot*rad)
	w.CD = [2][2]float64{{d1 * cr, -d2 * sr}, {d1 * sr, d2 * cr}}
	return w, d1 != 0 && d2 != 0
}

func (w WCS) Cards() map[string]string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'E', 12, 64) }
	return map[string]string{
		"CTYPE1": "RA---TAN", "CTYPE2": "DEC--TAN",
		"CRPIX1": f(w.CRPIX1), "CRPIX2": f(w.CRPIX2),
		"CRVAL1": f(w.CRVAL1), "CRVAL2": f(w.CRVAL2),
		"CD1_1": f(w.CD[0][0]), "CD1_2": f(w.CD[0][1]),
		"CD2_1": f(w.CD[1][0]), "CD2_2": f(w.CD[1][1]),
	}
}

func (w WCS) j2000(i, j float64) Vec {
	px, py := i-w.CRPIX1, j-w.CRPIX2
	xi := (w.CD[0][0]*px + w.CD[0][1]*py) * rad
	eta := (w.CD[1][0]*px + w.CD[1][1]*py) * rad
	a0, d0 := w.CRVAL1*rad, w.CRVAL2*rad
	c := raDecVec(w.CRVAL1, w.CRVAL2)
	east := Vec{-math.Sin(a0), math.Cos(a0), 0}
	north := Vec{-math.Sin(d0) * math.Cos(a0), -math.Sin(d0) * math.Sin(a0), math.Cos(d0)}
	return c.Add(east.Scale(xi)).Add(north.Scale(eta)).Unit()
}

type WCSGeometry struct {
	W, H int
	Lat  float64
	wcs  WCS
	lst  float64
	p    [3][3]float64
}

func NewWCSGeometry(w, h int, site Site, wcs WCS, t time.Time) *WCSGeometry {
	return &WCSGeometry{W: w, H: h, Lat: site.Lat, wcs: wcs, lst: site.LST(t), p: precession(t)}
}

func (g *WCSGeometry) Size() (int, int) { return g.W, g.H }

func (g *WCSGeometry) eq(x, y float64) Vec {
	v := g.wcs.j2000(x+1, float64(g.H)-y)
	var d Vec
	for i := range 3 {
		d[i] = g.p[i][0]*v[0] + g.p[i][1]*v[1] + g.p[i][2]*v[2]
	}
	ra, dec := vecRADec(d)
	return EqVec(wrap180(g.lst-ra), dec)
}

func (g *WCSGeometry) Dir(x, y float64) Vec { return EqToHor(g.eq(x, y), g.Lat) }

func (g *WCSGeometry) Center() (ha, dec float64) {
	return HADec(g.eq(float64(g.W-1)/2, float64(g.H-1)/2))
}

func (g *WCSGeometry) Orientation() (theta float64, parity int, scaleArcsec float64) {
	cx, cy := float64(g.W-1)/2, float64(g.H-1)/2
	c := g.eq(cx, cy)
	ha, dec := HADec(c)
	north := Vec{-math.Sin(dec*rad) * math.Cos(ha*rad), math.Sin(dec*rad) * math.Sin(ha*rad), math.Cos(dec * rad)}
	east := Vec{math.Sin(ha * rad), math.Cos(ha * rad), 0}
	up := g.eq(cx, cy-1).Sub(c)
	right := g.eq(cx+1, cy).Sub(c)
	theta = wrap360(math.Atan2(up.Dot(east), up.Dot(north)) / rad)
	model := north.Scale(math.Sin(theta * rad)).Sub(east.Scale(math.Cos(theta * rad)))
	parity = 1
	if right.Dot(model) < 0 {
		parity = -1
	}
	return theta, parity, up.Norm() / rad * 3600
}

func WCSFromGeometry(g Geometry, site Site, t time.Time) WCS {
	w, h := g.Size()
	cx, cy := float64(w-1)/2, float64(h-1)/2
	p := precession(t)
	lst := site.LST(t)
	j2000 := func(x, y float64) Vec {
		ha, dec := HADec(HorToEq(g.Dir(x, y), site.Lat))
		ra, d := wrap360(lst-ha), dec
		v := raDecVec(ra, d)
		var o Vec
		for i := range 3 {
			o[i] = p[0][i]*v[0] + p[1][i]*v[1] + p[2][i]*v[2]
		}
		return o
	}
	c := j2000(cx, cy)
	a0, d0 := vecRADec(c)
	east := Vec{-math.Sin(a0 * rad), math.Cos(a0 * rad), 0}
	north := Vec{-math.Sin(d0*rad) * math.Cos(a0*rad), -math.Sin(d0*rad) * math.Sin(a0*rad), math.Cos(d0 * rad)}
	tangent := func(v Vec) (float64, float64) {
		k := 1 / v.Dot(c)
		return v.Dot(east) * k / rad, v.Dot(north) * k / rad
	}
	xi1, eta1 := tangent(j2000(cx+1, cy))
	xi2, eta2 := tangent(j2000(cx, cy-1))
	return WCS{
		CRPIX1: cx + 1, CRPIX2: float64(h) - cy,
		CRVAL1: a0, CRVAL2: d0,
		CD: [2][2]float64{{xi1, xi2}, {eta1, eta2}},
	}
}

func FrameGeometry(f FrameRecord, w, h int, site Site, cards map[string]string, cal *Calibration) (Geometry, string, error) {
	if wcs, ok := ParseWCS(cards); ok {
		return NewWCSGeometry(w, h, site, wcs, f.MidTime()), "plate solve", nil
	}
	if cal == nil {
		return nil, "", fmt.Errorf("%s: %w", f.File, ErrNoGeometry)
	}
	theta, ok := cal.ThetaFor(f.Mount.PierSide)
	if !ok {
		return nil, "", fmt.Errorf("%s (pier side %q): %w", f.File, f.Mount.PierSide, ErrNoGeometry)
	}
	scale := f.PixelScale
	if cal.Scale > 0 {
		scale = cal.Scale
	}
	return NewMountGeometry(w, h, site.Lat, f.Mount.HourAngle(), f.Mount.Dec, theta, cal.Parity, scale), "mount position and calibration", nil
}
