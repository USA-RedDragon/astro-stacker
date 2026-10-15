// Package starcat reads and writes the local mirrors of Gaia DR3 and AAVSO
// VSX: catalogue rows partitioned by nested HEALPix pixel of their position,
// stored as zstd-compressed columnar files in S3.
package starcat

import (
	"math"
	"sort"
	"strconv"
	"strings"

	healpix "github.com/hebl/gohealpix"
)

const (
	deg       = math.Pi / 180
	masPerRad = 180 / math.Pi * 3600 * 1000
	// coverSlack widens every pixel and block test, in radians, against
	// rounding in the HEALPix and vector arithmetic.
	coverSlack = 1e-9
)

// Vec returns the unit vector of a position in degrees.
func Vec(ra, dec float64) [3]float64 {
	sa, ca := math.Sincos(ra * deg)
	sd, cd := math.Sincos(dec * deg)
	return [3]float64{cd * ca, cd * sa, sd}
}

// Angle returns the angle between two unit vectors in radians.
func Angle(a, b [3]float64) float64 {
	c := [3]float64{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
	return math.Atan2(math.Sqrt(c[0]*c[0]+c[1]*c[1]+c[2]*c[2]), a[0]*b[0]+a[1]*b[1]+a[2]*b[2])
}

// Separation returns the angle between two positions in degrees.
func Separation(ra1, dec1, ra2, dec2 float64) float64 {
	return Angle(Vec(ra1, dec1), Vec(ra2, dec2)) / deg
}

// Round returns v as VizieR reads it when printed with decimals digits.
func Round(v float64, decimals int) float64 {
	r, err := strconv.ParseFloat(strconv.FormatFloat(v, 'f', decimals, 64), 64)
	if err != nil {
		return v
	}
	return r
}

// Decimal returns a float32 column value as the float64 of its shortest
// decimal, the number Gaia's files print and VizieR holds. A value exactly
// halfway between two shortest decimals takes the one away from zero, as
// Gaia prints it.
func Decimal(v float32) float64 {
	x := float64(v)
	if x == 0 || math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	s := strconv.FormatFloat(x, 'e', -1, 32)
	m := strings.TrimPrefix(s[:strings.IndexByte(s, 'e')], "-")
	digits := len(strings.Replace(m, ".", "", 1))
	r, err := strconv.ParseFloat(strconv.FormatFloat(math.Nextafter(x, math.Copysign(math.Inf(1), x)), 'e', digits-1, 64), 64)
	if err != nil {
		return x
	}
	return r
}

// Pixel returns the nested HEALPix pixel of a position at an order.
func Pixel(order int, ra, dec float64) int64 {
	return healpix.Vec2PixNest(int64(1)<<order, Vec(ra, dec))
}

// pixelCenter returns the unit vector of a nested pixel's centre.
func pixelCenter(order int, pix int64) [3]float64 {
	return healpix.Pix2VecNest(int64(1)<<order, pix)
}

// MaxPixelRadius returns the largest angle, in radians, between a pixel's
// centre and any point of the pixel at an order (HEALPix max_pixrad).
func MaxPixelRadius(order int) float64 {
	nside := float64(int64(1) << order)
	va := zphiVec(2./3., math.Pi/(4*nside))
	t1 := 1 - 1/nside
	t1 *= t1
	vb := zphiVec(1-t1/3, 0)
	return Angle(va, vb)
}

func zphiVec(z, phi float64) [3]float64 {
	s := math.Sqrt((1 - z) * (1 + z))
	return [3]float64{s * math.Cos(phi), s * math.Sin(phi), z}
}

// Cover returns, sorted, every nested pixel at an order that may hold a
// point within radius degrees of (ra, dec). It may return a few pixels
// that hold none, never misses one.
func Cover(order int, ra, dec, radius float64) []int64 {
	v := Vec(ra, dec)
	r := radius * deg
	var out []int64
	var walk func(o int, p int64)
	walk = func(o int, p int64) {
		if Angle(pixelCenter(o, p), v) > r+MaxPixelRadius(o)+coverSlack {
			return
		}
		if o == order {
			out = append(out, p)
			return
		}
		for c := range int64(4) {
			walk(o+1, 4*p+c)
		}
	}
	for p := range int64(12) {
		walk(0, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Propagate moves a position at one epoch by its proper motion, pmRA
// (with cos dec) and pmDec in mas/yr, over years, along the great circle of
// its motion: the tangent-plane step of Gaia's epoch propagation without
// parallax or radial velocity. A NaN proper motion leaves the position.
func Propagate(ra, dec, pmRA, pmDec, years float64) (float64, float64) {
	if math.IsNaN(pmRA) || math.IsNaN(pmDec) || years == 0 {
		return ra, dec
	}
	sa, ca := math.Sincos(ra * deg)
	sd, cd := math.Sincos(dec * deg)
	u := [3]float64{cd * ca, cd * sa, sd}
	p := [3]float64{-sa, ca, 0}
	q := [3]float64{-sd * ca, -sd * sa, cd}
	a, d := pmRA*years/masPerRad, pmDec*years/masPerRad
	for i := range u {
		u[i] += a*p[i] + d*q[i]
	}
	n := math.Sqrt(u[0]*u[0] + u[1]*u[1] + u[2]*u[2])
	ra2 := math.Atan2(u[1], u[0]) / deg
	if ra2 < 0 {
		ra2 += 360
	}
	return ra2, math.Asin(u[2]/n) / deg
}

// bound is a circle around a set of points: a centre and
// the largest angle from it to any point, in radians.
type bound struct {
	C [3]float64 `json:"c"`
	R float64    `json:"r"`
}

func boundOf(vs [][3]float64) bound {
	var s [3]float64
	for _, v := range vs {
		for i := range s {
			s[i] += v[i]
		}
	}
	n := math.Sqrt(s[0]*s[0] + s[1]*s[1] + s[2]*s[2])
	if n == 0 {
		return bound{C: [3]float64{0, 0, 1}, R: math.Pi}
	}
	c := [3]float64{s[0] / n, s[1] / n, s[2] / n}
	r := 0.0
	for _, v := range vs {
		r = math.Max(r, Angle(c, v))
	}
	return bound{C: c, R: r}
}

func (c bound) reaches(v [3]float64, r float64) bool {
	return Angle(c.C, v) <= r+c.R+coverSlack
}
