package frameheader

import (
	"math"
	"strconv"
	"strings"
)

const deg = math.Pi / 180

// pointing is where a frame was pointed, in degrees: RA and DEC as NINA
// writes them, or else the centre of the plate solution remote telescopes
// (Telescope.live) write instead, or else the target's position some of
// theirs carry alone (OBJCTRA, OBJCTDEC). NaN when the header has none.
func pointing(k Keywords) (ra, dec float64) {
	ra, dec = k.Float("RA"), k.Float("DEC")
	if !math.IsNaN(ra) && !math.IsNaN(dec) {
		return ra, dec
	}
	if ra, dec, ok := WCSCentre(k); ok {
		return ra, dec
	}
	if ra, dec, ok := ObjectPosition(k); ok {
		return ra, dec
	}
	return math.NaN(), math.NaN()
}

// ObjectPosition is the target's position from OBJCTRA and OBJCTDEC,
// sexagesimal hours and degrees ("14 39 29.71", "-60 49 55.99"), in
// degrees.
func ObjectPosition(k Keywords) (ra, dec float64, ok bool) {
	h, ok1 := sexagesimal(k.String("OBJCTRA"))
	d, ok2 := sexagesimal(k.String("OBJCTDEC"))
	if !ok1 || !ok2 || h < 0 || h >= 24 || d < -90 || d > 90 {
		return 0, 0, false
	}
	return h * 15, d, true
}

// sexagesimal reads "d m s", "d:m:s" or "d" into a number.
func sexagesimal(s string) (float64, bool) {
	f := strings.FieldsFunc(strings.TrimSpace(s), func(r rune) bool { return r == ' ' || r == ':' })
	if len(f) == 0 || len(f) > 3 {
		return 0, false
	}
	neg := strings.HasPrefix(f[0], "-")
	var v float64
	for i, part := range f {
		x, err := strconv.ParseFloat(strings.TrimPrefix(strings.TrimPrefix(part, "-"), "+"), 64)
		if err != nil || x < 0 {
			return 0, false
		}
		v += x / math.Pow(60, float64(i))
	}
	if neg {
		v = -v
	}
	return v, true
}

// WCSCentre is the sky position of the middle of the image by its plate
// solution: CRVAL moved from CRPIX to the centre pixel through the linear
// part of a gnomonic (TAN) solution. The distortion terms (SIP) are left
// out; at the centre they move it by a pixel or so. ok is false without a
// solution in RA and Dec, in degrees.
func WCSCentre(k Keywords) (ra, dec float64, ok bool) {
	c1, c2 := strings.ToUpper(k.String("CTYPE1")), strings.ToUpper(k.String("CTYPE2"))
	if !strings.HasPrefix(c1, "RA--") || !strings.HasPrefix(c2, "DEC-") {
		return 0, 0, false
	}
	for _, u := range []string{"CUNIT1", "CUNIT2"} {
		if unit := strings.ToLower(k.String(u)); unit != "" && unit != "deg" {
			return 0, 0, false
		}
	}
	ra0, dec0 := k.Float("CRVAL1"), k.Float("CRVAL2")
	if math.IsNaN(ra0) || math.IsNaN(dec0) {
		return 0, 0, false
	}
	cd, haveCD := linearTransform(k)
	w, h := k.Float("NAXIS1"), k.Float("NAXIS2")
	px, py := k.Float("CRPIX1"), k.Float("CRPIX2")
	if !haveCD || !(w > 0) || !(h > 0) || math.IsNaN(px) || math.IsNaN(py) || !strings.HasSuffix(strings.TrimSuffix(c1, "-SIP"), "TAN") {
		// Not a TAN solution we can move across: its reference point is
		// still near the centre, where solvers put it.
		return ra0, dec0, true
	}
	dx, dy := (w+1)/2-px, (h+1)/2-py
	xi := (cd[0]*dx + cd[1]*dy) * deg
	eta := (cd[2]*dx + cd[3]*dy) * deg
	d0 := dec0 * deg
	den := math.Cos(d0) - eta*math.Sin(d0)
	ra = math.Mod(ra0+math.Atan2(xi, den)/deg+360, 360)
	dec = math.Atan2(math.Sin(d0)+eta*math.Cos(d0), math.Hypot(xi, den)) / deg
	return ra, dec, true
}

// linearTransform is a solution's CD matrix, from CD keywords or CDELT with
// PC or CROTA2.
func linearTransform(k Keywords) ([4]float64, bool) {
	get := func(name string, def float64) float64 {
		if v := k.Float(name); !math.IsNaN(v) {
			return v
		}
		return def
	}
	if !math.IsNaN(k.Float("CD1_1")) || !math.IsNaN(k.Float("CD2_2")) {
		return [4]float64{get("CD1_1", 0), get("CD1_2", 0), get("CD2_1", 0), get("CD2_2", 0)}, true
	}
	c1, c2 := k.Float("CDELT1"), k.Float("CDELT2")
	if math.IsNaN(c1) || math.IsNaN(c2) {
		return [4]float64{}, false
	}
	if _, ok := k["PC1_1"]; ok {
		return [4]float64{c1 * get("PC1_1", 1), c1 * get("PC1_2", 0), c2 * get("PC2_1", 0), c2 * get("PC2_2", 1)}, true
	}
	rot := get("CROTA2", 0) * deg
	return [4]float64{c1 * math.Cos(rot), -c2 * math.Sin(rot), c1 * math.Sin(rot), c2 * math.Cos(rot)}, true
}

// PixelScale is the size of a pixel by the plate solution, in degrees.
func PixelScale(k Keywords) (float64, bool) {
	cd, ok := linearTransform(k)
	if !ok {
		return 0, false
	}
	s := math.Sqrt(math.Abs(cd[0]*cd[3] - cd[1]*cd[2]))
	return s, s > 0
}
