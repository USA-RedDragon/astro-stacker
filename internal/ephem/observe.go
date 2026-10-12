package ephem

import (
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const (
	deg        = math.Pi / 180
	arcsec     = deg / 3600
	lightKmS   = 299792.458
	auKm       = 149597870.7
	wgs84A     = 6378.137
	wgs84F     = 1 / 298.257223563
	ringPoleRA = 40.589
	ringPoleDE = 83.537
)

type Vec3 [3]float64

func (a Vec3) add(b Vec3) Vec3      { return Vec3{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func (a Vec3) sub(b Vec3) Vec3      { return Vec3{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func (a Vec3) dot(b Vec3) float64   { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func (a Vec3) norm() float64        { return math.Sqrt(a.dot(a)) }
func (a Vec3) scale(k float64) Vec3 { return Vec3{a[0] * k, a[1] * k, a[2] * k} }

type mat3 [3][3]float64

func (m mat3) apply(v Vec3) Vec3 {
	return Vec3{m[0][0]*v[0] + m[0][1]*v[1] + m[0][2]*v[2], m[1][0]*v[0] + m[1][1]*v[1] + m[1][2]*v[2], m[2][0]*v[0] + m[2][1]*v[1] + m[2][2]*v[2]}
}

func (m mat3) transpose() mat3 {
	return mat3{{m[0][0], m[1][0], m[2][0]}, {m[0][1], m[1][1], m[2][1]}, {m[0][2], m[1][2], m[2][2]}}
}

func (m mat3) mul(n mat3) mat3 {
	var out mat3
	for i := range 3 {
		for j := range 3 {
			out[i][j] = m[i][0]*n[0][j] + m[i][1]*n[1][j] + m[i][2]*n[2][j]
		}
	}
	return out
}

func rotZ(a float64) mat3 {
	s, c := math.Sincos(a)
	return mat3{{c, s, 0}, {-s, c, 0}, {0, 0, 1}}
}

func rotY(a float64) mat3 {
	s, c := math.Sincos(a)
	return mat3{{c, 0, -s}, {0, 1, 0}, {s, 0, c}}
}

// precession is the IAU 1976 matrix from the J2000 mean equator to the mean
// equator of date.
func precession(et float64) mat3 {
	t := et / 86400 / 36525
	zeta := (2306.2181*t + 0.30188*t*t + 0.017998*t*t*t) * arcsec
	z := (2306.2181*t + 1.09468*t*t + 0.018203*t*t*t) * arcsec
	theta := (2004.3109*t - 0.42665*t*t - 0.041833*t*t*t) * arcsec
	return rotZ(-z).mul(rotY(theta)).mul(rotZ(-zeta))
}

func unitVec(raDeg, decDeg float64) Vec3 {
	sr, cr := math.Sincos(raDeg * deg)
	sd, cd := math.Sincos(decDeg * deg)
	return Vec3{cd * cr, cd * sr, sd}
}

func radec(v Vec3) (float64, float64) {
	ra := math.Atan2(v[1], v[0]) / deg
	if ra < 0 {
		ra += 360
	}
	return ra, math.Atan2(v[2], math.Hypot(v[0], v[1])) / deg
}

// Observer is a site's position at one time: its geocentric vector in the
// ICRF, in km, and what is needed for altitudes.
type Observer struct {
	Site sky.Site
	At   time.Time
	et   float64
	geo  Vec3
	prec mat3
	lst  float64
}

func NewObserver(site sky.Site, t time.Time) Observer {
	et := ET(t)
	lst := sky.LST(t, site.Longitude)
	h := 0.0
	if site.Elevation != nil && !math.IsNaN(*site.Elevation) {
		h = *site.Elevation / 1000
	}
	sl, cl := math.Sincos(site.Latitude * deg)
	c := 1 / math.Sqrt(cl*cl+(1-wgs84F)*(1-wgs84F)*sl*sl)
	s := (1 - wgs84F) * (1 - wgs84F) * c
	ss, cs := math.Sincos(lst * deg)
	date := Vec3{(wgs84A*c + h) * cl * cs, (wgs84A*c + h) * cl * ss, (wgs84A*s + h) * sl}
	p := precession(et)
	return Observer{Site: site, At: t, et: et, geo: p.transpose().apply(date), prec: p, lst: lst}
}

// Altitude is the geometric altitude, in degrees, of an ICRF direction.
func (o Observer) Altitude(raDeg, decDeg float64) float64 {
	ra, dec := radec(o.prec.apply(unitVec(raDeg, decDeg)))
	return sky.AltitudeAtLST(ra, dec, o.Site.Latitude, o.lst)
}

// Position is a body as seen from a site.
type Position struct {
	Body Body
	At   time.Time
	// RA and Dec are astrometric ICRF, degrees.
	RA, Dec float64
	// Altitude is geometric, degrees, without refraction.
	Altitude float64
	// Distance is the topocentric range in au; SunDistance the body's
	// distance from the Sun in au.
	Distance, SunDistance float64
	// Phase is the Sun-body-observer angle, degrees.
	Phase float64
	// Diameter is the apparent equatorial diameter, degrees.
	Diameter float64
	// Magnitude is the visual magnitude from the Astronomical Almanac
	// formulae (Meeus, Astronomical Algorithms ch. 41) and, for the Moon,
	// Allen's phase law; NaN for the Sun.
	Magnitude float64
}

func radiusKm(b Body) float64 {
	switch b {
	case Mercury:
		return 2440.5
	case Venus:
		return 6051.8
	case Mars:
		return 3396.2
	case Jupiter:
		return 71492
	case Saturn:
		return 60268
	case Uranus:
		return 25559
	case Neptune:
		return 24764
	case Moon:
		return 1737.4
	case Sun:
		return 695700
	case EMB, Earth:
		return 6378.137
	}
	return 0
}

// Observe computes the body's topocentric astrometric position: the body at
// the time light left it, seen from the site's ICRF position at t.
func (e *Ephemeris) Observe(b Body, o Observer) (Position, error) {
	earth, err := e.Barycentric(Earth, o.et)
	if err != nil {
		return Position{}, err
	}
	obs := earth.add(o.geo)
	var body, rel Vec3
	tau := 0.0
	for range 3 {
		body, err = e.Barycentric(b, o.et-tau)
		if err != nil {
			return Position{}, err
		}
		rel = body.sub(obs)
		tau = rel.norm() / lightKmS
	}
	ra, dec := radec(rel)
	dist := rel.norm()
	p := Position{Body: b, At: o.At, RA: ra, Dec: dec, Altitude: o.Altitude(ra, dec), Distance: dist / auKm, Magnitude: math.NaN()}
	p.Diameter = 2 * math.Asin(math.Min(1, radiusKm(b)/dist)) / deg
	if b == Sun {
		return p, nil
	}
	sun, err := e.Barycentric(Sun, o.et-tau)
	if err != nil {
		return Position{}, err
	}
	toSun := sun.sub(body)
	toObs := rel.scale(-1)
	p.SunDistance = toSun.norm() / auKm
	p.Phase = math.Acos(math.Max(-1, math.Min(1, toSun.dot(toObs)/(toSun.norm()*toObs.norm())))) / deg
	p.Magnitude = magnitude(b, p.SunDistance, p.Distance, p.Phase, rel)
	return p, nil
}

func magnitude(b Body, r, delta, i float64, rel Vec3) float64 {
	d := 5 * math.Log10(r*delta)
	switch b {
	case Mercury:
		return -0.42 + d + 0.0380*i - 0.000273*i*i + 0.000002*i*i*i
	case Venus:
		return -4.40 + d + 0.0009*i + 0.000239*i*i - 0.00000065*i*i*i
	case Mars:
		return -1.52 + d + 0.016*i
	case Jupiter:
		return -9.40 + d + 0.005*i
	case Saturn:
		sinB := math.Abs(rel.scale(1 / rel.norm()).dot(unitVec(ringPoleRA, ringPoleDE)))
		return -8.88 + d + 0.044*i - 2.60*sinB + 1.25*sinB*sinB
	case Uranus:
		return -7.19 + d
	case Neptune:
		return -6.87 + d
	case Moon:
		return -12.73 + 0.026*i + 4e-9*i*i*i*i
	case EMB, Earth, Sun:
		return math.NaN()
	}
	return math.NaN()
}

// SunAltitude is the Sun's geometric altitude in degrees.
func (e *Ephemeris) SunAltitude(o Observer) (float64, error) {
	p, err := e.Observe(Sun, o)
	return p.Altitude, err
}
