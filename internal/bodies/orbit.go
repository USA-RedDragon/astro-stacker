package bodies

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	gaussK    = 0.01720209895
	lightDay  = 0.0057755183
	obliquity = 23.4392911 * math.Pi / 180
	degree    = math.Pi / 180
	j2000     = 2451545.0
)

// Elements are one comet's heliocentric two-body elements from the MPC's
// CometEls.txt, angles in degrees on the J2000 ecliptic. H and G are the
// total-magnitude parameters, nil when the MPC gives none.
type Elements struct {
	Name        string
	Designation string
	OrbitType   string
	Perihelion  time.Time
	Q           float64
	E           float64
	ArgPeri     float64
	Node        float64
	Incl        float64
	H           *float64
	G           *float64
}

func julian(t time.Time) float64 {
	return float64(t.UTC().UnixNano())/86400e9 + 2440587.5
}

func field(line string, from, to int) string {
	if from > len(line) {
		return ""
	}
	return strings.TrimSpace(line[from-1 : min(to, len(line))])
}

// designationOf is the designation a comet's MPC name starts with:
// "2P/Encke" is 2P, "C/2025 R2 (SWAN)" is C/2025 R2.
func designationOf(name string) string {
	if i := strings.IndexByte(name, '/'); i > 0 && name[0] >= '0' && name[0] <= '9' {
		return name[:i]
	}
	if i := strings.Index(name, " ("); i > 0 {
		return strings.TrimSpace(name[:i])
	}
	return strings.TrimSpace(name)
}

// ParseCometLine reads one line of the MPC's comet orbit export format
// (https://www.minorplanetcenter.net/iau/info/CometOrbitFormat.html).
func ParseCometLine(line string) (Elements, error) {
	var el Elements
	el.Name = field(line, 103, 158)
	if el.Name == "" {
		return el, errors.New("no designation and name")
	}
	el.Designation = designationOf(el.Name)
	el.OrbitType = field(line, 5, 5)
	year, err1 := strconv.Atoi(field(line, 15, 18))
	month, err2 := strconv.Atoi(field(line, 20, 21))
	day, err3 := strconv.ParseFloat(field(line, 23, 29), 64)
	nums := make([]float64, 5)
	errs := []error{err1, err2, err3}
	for i, c := range [][2]int{{31, 39}, {42, 49}, {52, 59}, {62, 69}, {72, 79}} {
		v, err := strconv.ParseFloat(field(line, c[0], c[1]), 64)
		nums[i] = v
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return el, fmt.Errorf("%s: %w", el.Name, err)
	}
	el.Perihelion = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Add(time.Duration((day - 1) * 86400e9))
	el.Q, el.E, el.ArgPeri, el.Node, el.Incl = nums[0], nums[1], nums[2], nums[3], nums[4]
	if el.Q <= 0 || el.E < 0 {
		return el, fmt.Errorf("%s: q %v e %v", el.Name, el.Q, el.E)
	}
	if h, err := strconv.ParseFloat(field(line, 92, 95), 64); err == nil {
		el.H = &h
	}
	if g, err := strconv.ParseFloat(field(line, 97, 100), 64); err == nil {
		el.G = &g
	}
	return el, nil
}

// ParseComets reads CometEls.txt, skipping lines it can't read.
func ParseComets(text string) ([]Elements, int) {
	var out []Elements
	bad := 0
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		el, err := ParseCometLine(line)
		if err != nil {
			bad++
			continue
		}
		out = append(out, el)
	}
	return out, bad
}

// Vec is a position on the J2000 ecliptic, in au.
type Vec [3]float64

func (a Vec) sub(b Vec) Vec { return Vec{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func (a Vec) norm() float64 { return math.Sqrt(a[0]*a[0] + a[1]*a[1] + a[2]*a[2]) }

func stumpff(z float64) (c, s float64) {
	switch {
	case z > 1e-6:
		sz := math.Sqrt(z)
		return (1 - math.Cos(sz)) / z, (sz - math.Sin(sz)) / (sz * z)
	case z < -1e-6:
		sz := math.Sqrt(-z)
		return (math.Cosh(sz) - 1) / -z, (math.Sinh(sz) - sz) / (sz * -z)
	}
	return 1.0/2 - z/24 + z*z/720, 1.0/6 - z/120 + z*z/5040
}

// perifocal propagates from perihelion by dt days with universal variables
// (any eccentricity), giving the position in the orbit plane, x toward
// perihelion.
func perifocal(q, e, dt float64) (x, y float64) {
	mu := gaussK * gaussK
	alpha := (1 - e) / q
	v0 := math.Sqrt(mu * (1 + e) / q)
	sm := math.Sqrt(mu)
	kepler := func(chi float64) (f, df, d2f float64) {
		z := alpha * chi * chi
		c, s := stumpff(z)
		return (1-alpha*q)*chi*chi*chi*s + q*chi - sm*dt, chi*chi*c + q*(1-z*c), (1 - alpha*q) * chi * (1 - z*s)
	}
	chi := sm * dt / q
	switch {
	case alpha > 0:
		chi = sm * dt * alpha
	case alpha < 0:
		// Vallado's starter for hyperbolic orbits, from perihelion.
		a := 1 / alpha
		if ratio := -2 * mu * alpha * math.Abs(dt) / (math.Sqrt(-mu*a) * (1 - q*alpha)); ratio > 1 {
			chi = math.Copysign(math.Sqrt(-a)*math.Log(ratio), dt)
		}
	}
	if math.Abs(chi) > 1e3 && alpha >= 0 {
		chi = math.Copysign(math.Cbrt(6*sm*math.Abs(dt)), dt)
	}
	// Laguerre's method (n=5) converges from poor starts at any eccentricity.
	const n = 5.0
	for range 200 {
		f, df, d2f := kepler(chi)
		if math.IsNaN(f) || math.IsInf(f, 0) || math.IsNaN(df) || math.IsInf(df, 0) {
			chi /= 2
			continue
		}
		den := df + math.Copysign(math.Sqrt(math.Abs((n-1)*(n-1)*df*df-n*(n-1)*f*d2f)), df)
		if den == 0 {
			break
		}
		step := n * f / den
		chi -= step
		if math.Abs(step) <= 1e-13*math.Max(1, math.Abs(chi)) {
			break
		}
	}
	z := alpha * chi * chi
	c, s := stumpff(z)
	ff := 1 - chi*chi*c/q
	g := dt - chi*chi*chi*s/sm
	return ff * q, g * v0
}

func rotate(x, y, argPeri, node, incl float64) Vec {
	w, o, i := argPeri*degree, node*degree, incl*degree
	cw, sw, co, so, ci, si := math.Cos(w), math.Sin(w), math.Cos(o), math.Sin(o), math.Cos(i), math.Sin(i)
	return Vec{
		(co*cw-so*sw*ci)*x + (-co*sw-so*cw*ci)*y,
		(so*cw+co*sw*ci)*x + (-so*sw+co*cw*ci)*y,
		(sw*si)*x + (cw*si)*y,
	}
}

// Heliocentric is the comet's position on the J2000 ecliptic, in au; NaN
// when the propagation fails.
func (el Elements) Heliocentric(t time.Time) Vec {
	x, y := perifocal(el.Q, el.E, julian(t)-julian(el.Perihelion))
	return rotate(x, y, el.ArgPeri, el.Node, el.Incl)
}

// earth is the Earth-Moon barycentre on the J2000 ecliptic, in au, from
// JPL's approximate planetary elements valid 1800-2050 (Standish, Table 1).
func earth(t time.Time) Vec {
	tc := (julian(t) - j2000) / 36525
	a := 1.00000261 + 0.00000562*tc
	e := 0.01671123 - 0.00004392*tc
	incl := -0.00001531 - 0.01294668*tc
	l := 100.46457166 + 35999.37244981*tc
	peri := 102.93768193 + 0.32327364*tc
	m := math.Mod(l-peri, 360) * degree
	ea := m
	for range 30 {
		d := (ea - e*math.Sin(ea) - m) / (1 - e*math.Cos(ea))
		ea -= d
		if math.Abs(d) < 1e-14 {
			break
		}
	}
	x, y := a*(math.Cos(ea)-e), a*math.Sqrt(1-e*e)*math.Sin(ea)
	return rotate(x, y, peri, 0, incl)
}

// Sighting is where a comet is seen from the Earth's centre: astrometric
// J2000 RA and Dec in degrees, light-time corrected, and its distances from
// the Sun (R) and the Earth (Delta) in au.
type Sighting struct {
	RA    float64
	Dec   float64
	R     float64
	Delta float64
}

// Finite reports whether every value of s is a number.
func (s Sighting) Finite() bool {
	for _, v := range []float64{s.RA, s.Dec, s.R, s.Delta} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

func (el Elements) Seen(t time.Time) Sighting {
	e := earth(t)
	p := el.Heliocentric(t)
	d := p.sub(e).norm()
	if math.IsNaN(d) || math.IsInf(d, 0) {
		return Sighting{RA: math.NaN(), Dec: math.NaN(), R: math.NaN(), Delta: math.NaN()}
	}
	for range 2 {
		p = el.Heliocentric(t.Add(-time.Duration(d * lightDay * 86400e9)))
		d = p.sub(e).norm()
	}
	g := p.sub(e)
	ce, se := math.Cos(obliquity), math.Sin(obliquity)
	x, y, z := g[0], ce*g[1]-se*g[2], se*g[1]+ce*g[2]
	ra := math.Mod(math.Atan2(y, x)/degree+360, 360)
	dec := math.Asin(z/d) / degree
	return Sighting{RA: ra, Dec: dec, R: p.norm(), Delta: d}
}

// Magnitude is the total visual magnitude from the MPC's H and G:
// m = H + 5 log10(Delta) + 2.5 G log10(r), the convention Stellarium uses
// for MPC comet elements.
func (el Elements) Magnitude(s Sighting) (float64, bool) {
	if el.H == nil || el.G == nil || s.R <= 0 || s.Delta <= 0 {
		return 0, false
	}
	return *el.H + 5*math.Log10(s.Delta) + 2.5**el.G*math.Log10(s.R), true
}
