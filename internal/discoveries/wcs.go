package discoveries

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const deg = math.Pi / 180

// WCS is a TAN plate solution with optional SIP distortion, in FITS pixel
// coordinates (1-based, row 1 at the bottom). PixelOf and SkyOf convert to
// and from the top-first 0-based pixels of a Plane of height H.
type WCS struct {
	CRPIX1, CRPIX2 float64
	CRVAL1, CRVAL2 float64
	CD             [2][2]float64
	A, B           [][]float64
	AP, BP         [][]float64
	H              int
	// Fix is an affine correction in plane pixels fitted to Gaia, applied
	// after the plate solution: x' = x + Fix[0] + Fix[1]x + Fix[2]y,
	// y' = y + Fix[3] + Fix[4]x + Fix[5]y.
	Fix [6]float64
}

// ParseWCS reads a plate solution from FITS keywords.
func ParseWCS(kw map[string]string, h int) (*WCS, error) {
	f := func(k string) (float64, bool) {
		s := strings.Trim(strings.TrimSpace(kw[k]), "'")
		if s == "" {
			return 0, false
		}
		v, err := strconv.ParseFloat(strings.ReplaceAll(s, "D", "E"), 64)
		return v, err == nil
	}
	w := &WCS{H: h}
	var ok1, ok2, ok3, ok4 bool
	w.CRPIX1, ok1 = f("CRPIX1")
	w.CRPIX2, ok2 = f("CRPIX2")
	w.CRVAL1, ok3 = f("CRVAL1")
	w.CRVAL2, ok4 = f("CRVAL2")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, fmt.Errorf("no plate solution")
	}
	if c, ok := f("CD1_1"); ok {
		c12, _ := f("CD1_2")
		c21, _ := f("CD2_1")
		c22, _ := f("CD2_2")
		w.CD = [2][2]float64{{c, c12}, {c21, c22}}
	} else {
		d1, ok1 := f("CDELT1")
		d2, ok2 := f("CDELT2")
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("no CD matrix or CDELT")
		}
		p11, ok := f("PC1_1")
		if ok {
			p12, _ := f("PC1_2")
			p21, _ := f("PC2_1")
			p22, _ := f("PC2_2")
			w.CD = [2][2]float64{{d1 * p11, d1 * p12}, {d2 * p21, d2 * p22}}
		} else {
			rot, _ := f("CROTA2")
			rot *= deg
			w.CD = [2][2]float64{{d1 * math.Cos(rot), -d2 * math.Sin(rot)}, {d1 * math.Sin(rot), d2 * math.Cos(rot)}}
		}
	}
	if w.CD[0][0]*w.CD[1][1]-w.CD[0][1]*w.CD[1][0] == 0 {
		return nil, fmt.Errorf("singular CD matrix")
	}
	poly := func(name string) [][]float64 {
		n, ok := f(name + "_ORDER")
		if !ok || n < 1 || n > 9 {
			return nil
		}
		o := int(n)
		c := make([][]float64, o+1)
		for p := range c {
			c[p] = make([]float64, o+1)
			for q := range c[p] {
				if p+q <= o {
					c[p][q], _ = f(fmt.Sprintf("%s_%d_%d", name, p, q))
				}
			}
		}
		return c
	}
	if strings.Contains(strings.ToUpper(kw["CTYPE1"]), "SIP") {
		w.A, w.B, w.AP, w.BP = poly("A"), poly("B"), poly("AP"), poly("BP")
	}
	return w, nil
}

func evalPoly(c [][]float64, u, v float64) float64 {
	var s float64
	up := 1.0
	for p := range c {
		vq := 1.0
		for q := range c[p] {
			if c[p][q] != 0 {
				s += c[p][q] * up * vq
			}
			vq *= v
		}
		up *= u
	}
	return s
}

// PixelScale is arcseconds per pixel.
func (w *WCS) PixelScale() float64 {
	return math.Sqrt(math.Abs(w.CD[0][0]*w.CD[1][1]-w.CD[0][1]*w.CD[1][0])) * 3600
}

func (w *WCS) fitsToSky(fx, fy float64) (ra, dec float64) {
	u, v := fx-w.CRPIX1, fy-w.CRPIX2
	if w.A != nil && w.B != nil {
		u, v = u+evalPoly(w.A, u, v), v+evalPoly(w.B, u, v)
	}
	xi := (w.CD[0][0]*u + w.CD[0][1]*v) * deg
	eta := (w.CD[1][0]*u + w.CD[1][1]*v) * deg
	d0 := w.CRVAL2 * deg
	den := math.Cos(d0) - eta*math.Sin(d0)
	ra = math.Mod(w.CRVAL1+math.Atan2(xi, den)/deg+360, 360)
	dec = math.Atan2(math.Sin(d0)+eta*math.Cos(d0), math.Hypot(xi, den)) / deg
	return ra, dec
}

func (w *WCS) skyToFITS(ra, dec float64) (fx, fy float64, ok bool) {
	d0, d, da := w.CRVAL2*deg, dec*deg, (ra-w.CRVAL1)*deg
	cosc := math.Sin(d0)*math.Sin(d) + math.Cos(d0)*math.Cos(d)*math.Cos(da)
	if cosc <= 0 {
		return 0, 0, false
	}
	xi := math.Cos(d) * math.Sin(da) / cosc / deg
	eta := (math.Cos(d0)*math.Sin(d) - math.Sin(d0)*math.Cos(d)*math.Cos(da)) / cosc / deg
	det := w.CD[0][0]*w.CD[1][1] - w.CD[0][1]*w.CD[1][0]
	U := (w.CD[1][1]*xi - w.CD[0][1]*eta) / det
	V := (-w.CD[1][0]*xi + w.CD[0][0]*eta) / det
	u, v := U, V
	if w.AP != nil && w.BP != nil {
		u, v = U+evalPoly(w.AP, U, V), V+evalPoly(w.BP, U, V)
	}
	if w.A != nil && w.B != nil {
		for range 10 {
			fu := u + evalPoly(w.A, u, v) - U
			fv := v + evalPoly(w.B, u, v) - V
			if math.Abs(fu) < 1e-6 && math.Abs(fv) < 1e-6 {
				break
			}
			const e = 1e-3
			a11 := 1 + (evalPoly(w.A, u+e, v)-evalPoly(w.A, u-e, v))/(2*e)
			a12 := (evalPoly(w.A, u, v+e) - evalPoly(w.A, u, v-e)) / (2 * e)
			a21 := (evalPoly(w.B, u+e, v) - evalPoly(w.B, u-e, v)) / (2 * e)
			a22 := 1 + (evalPoly(w.B, u, v+e)-evalPoly(w.B, u, v-e))/(2*e)
			dd := a11*a22 - a12*a21
			if dd == 0 {
				break
			}
			u -= (a22*fu - a12*fv) / dd
			v -= (-a21*fu + a11*fv) / dd
		}
	}
	return u + w.CRPIX1, v + w.CRPIX2, true
}

// PixelOf returns the plane pixel (0-based, row 0 at the top) of a sky
// position.
func (w *WCS) PixelOf(ra, dec float64) (x, y float64, ok bool) {
	fx, fy, ok := w.skyToFITS(ra, dec)
	if !ok {
		return 0, 0, false
	}
	x, y = fx-1, float64(w.H)-fy
	return x + w.Fix[0] + w.Fix[1]*x + w.Fix[2]*y, y + w.Fix[3] + w.Fix[4]*x + w.Fix[5]*y, true
}

// SkyOf returns the sky position of a plane pixel.
func (w *WCS) SkyOf(x, y float64) (ra, dec float64) {
	f := w.Fix
	a, b, c, d := 1+f[1], f[2], f[4], 1+f[5]
	det := a*d - b*c
	rx, ry := x-f[0], y-f[3]
	ux, uy := (d*rx-b*ry)/det, (-c*rx+a*ry)/det
	return w.fitsToSky(ux+1, float64(w.H)-uy)
}

// Shifted is the plate solution of a cutout whose top-left plane pixel is
// (x0, y0) and which is s pixels tall, with the Gaia correction folded in
// only to first order (its offset at the cutout centre).
func (w *WCS) Shifted(x0, y0, s int) *WCS {
	c := *w
	cx, cy := float64(x0)+float64(s)/2, float64(y0)+float64(s)/2
	dx := w.Fix[0] + w.Fix[1]*cx + w.Fix[2]*cy
	dy := w.Fix[3] + w.Fix[4]*cx + w.Fix[5]*cy
	c.CRPIX1 = w.CRPIX1 - float64(x0) + dx
	c.CRPIX2 = w.CRPIX2 - float64(w.H-s-y0) - dy
	c.H = s
	c.Fix = [6]float64{}
	return &c
}

// Cards writes the plate solution as FITS keywords.
func (w *WCS) Cards() [][2]string {
	out := [][2]string{
		{"CTYPE1", "'RA---TAN" + map[bool]string{true: "-SIP'", false: "'"}[w.A != nil]},
		{"CTYPE2", "'DEC--TAN" + map[bool]string{true: "-SIP'", false: "'"}[w.A != nil]},
		{"CRPIX1", fmt.Sprintf("%.10g", w.CRPIX1)}, {"CRPIX2", fmt.Sprintf("%.10g", w.CRPIX2)},
		{"CRVAL1", fmt.Sprintf("%.12g", w.CRVAL1)}, {"CRVAL2", fmt.Sprintf("%.12g", w.CRVAL2)},
		{"CD1_1", fmt.Sprintf("%.12g", w.CD[0][0])}, {"CD1_2", fmt.Sprintf("%.12g", w.CD[0][1])},
		{"CD2_1", fmt.Sprintf("%.12g", w.CD[1][0])}, {"CD2_2", fmt.Sprintf("%.12g", w.CD[1][1])},
		{"RADESYS", "'ICRS'"},
	}
	add := func(name string, c [][]float64) {
		if c == nil {
			return
		}
		out = append(out, [2]string{name + "_ORDER", strconv.Itoa(len(c) - 1)})
		for p := range c {
			for q := range c[p] {
				if c[p][q] != 0 {
					out = append(out, [2]string{fmt.Sprintf("%s_%d_%d", name, p, q), fmt.Sprintf("%.12g", c[p][q])})
				}
			}
		}
	}
	add("A", w.A)
	add("B", w.B)
	add("AP", w.AP)
	add("BP", w.BP)
	return out
}

// Separation is the angle between two sky positions, in degrees.
func Separation(ra1, dec1, ra2, dec2 float64) float64 {
	s := math.Sin((dec2-dec1)*deg/2)*math.Sin((dec2-dec1)*deg/2) +
		math.Cos(dec1*deg)*math.Cos(dec2*deg)*math.Sin((ra2-ra1)*deg/2)*math.Sin((ra2-ra1)*deg/2)
	return 2 * math.Asin(math.Min(1, math.Sqrt(s))) / deg
}
