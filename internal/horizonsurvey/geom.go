package horizonsurvey

import (
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const rad = math.Pi / 180

type Vec [3]float64

func (v Vec) Add(o Vec) Vec       { return Vec{v[0] + o[0], v[1] + o[1], v[2] + o[2]} }
func (v Vec) Sub(o Vec) Vec       { return Vec{v[0] - o[0], v[1] - o[1], v[2] - o[2]} }
func (v Vec) Scale(k float64) Vec { return Vec{v[0] * k, v[1] * k, v[2] * k} }
func (v Vec) Dot(o Vec) float64   { return v[0]*o[0] + v[1]*o[1] + v[2]*o[2] }
func (v Vec) Norm() float64       { return math.Sqrt(v.Dot(v)) }
func (v Vec) Unit() Vec           { return v.Scale(1 / v.Norm()) }
func clampUnit(x float64) float64 { return math.Max(-1, math.Min(1, x)) }

type Site struct {
	Lat float64 `json:"latitude"`
	Lon float64 `json:"longitude"`
}

func wrap360(a float64) float64 {
	a = math.Mod(a, 360)
	if a < 0 {
		a += 360
	}
	return a
}

func wrap180(a float64) float64 {
	a = wrap360(a)
	if a > 180 {
		a -= 360
	}
	return a
}

func HorVec(az, alt float64) Vec {
	return Vec{math.Cos(alt*rad) * math.Cos(az*rad), math.Cos(alt*rad) * math.Sin(az*rad), math.Sin(alt * rad)}
}

func AltAz(v Vec) (az, alt float64) {
	v = v.Unit()
	return wrap360(math.Atan2(v[1], v[0]) / rad), math.Asin(clampUnit(v[2])) / rad
}

func EqVec(ha, dec float64) Vec {
	return Vec{math.Cos(dec*rad) * math.Cos(ha*rad), -math.Cos(dec*rad) * math.Sin(ha*rad), math.Sin(dec * rad)}
}

func HADec(v Vec) (ha, dec float64) {
	v = v.Unit()
	return wrap180(math.Atan2(-v[1], v[0]) / rad), math.Asin(clampUnit(v[2])) / rad
}

func EqToHor(v Vec, lat float64) Vec {
	s, c := math.Sin(lat*rad), math.Cos(lat*rad)
	return Vec{-s*v[0] + c*v[2], v[1], c*v[0] + s*v[2]}
}

func HorToEq(v Vec, lat float64) Vec {
	s, c := math.Sin(lat*rad), math.Cos(lat*rad)
	return Vec{-s*v[0] + c*v[2], v[1], c*v[0] + s*v[2]}
}

func (s Site) AltAzToHADec(az, alt float64) (ha, dec float64) {
	return HADec(HorToEq(HorVec(az, alt), s.Lat))
}

func (s Site) HADecToAltAz(ha, dec float64) (az, alt float64) {
	return AltAz(EqToHor(EqVec(ha, dec), s.Lat))
}

func (s Site) LST(t time.Time) float64 { return sky.LST(t, s.Lon) }

func (s Site) SunHADec(t time.Time) (ha, dec float64) {
	ra, dec := sky.SunPosition(t)
	return wrap180(s.LST(t) - ra), dec
}

func (s Site) SunAltAz(t time.Time) (az, alt float64) {
	ha, dec := s.SunHADec(t)
	return s.HADecToAltAz(ha, dec)
}

func Separation(a, b Vec) float64 {
	return math.Acos(clampUnit(a.Unit().Dot(b.Unit()))) / rad
}

func (s Site) SunSeparation(t time.Time, az, alt float64) float64 {
	saz, salt := s.SunAltAz(t)
	return Separation(HorVec(az, alt), HorVec(saz, salt))
}

func julianCenturies(t time.Time) float64 {
	jd := float64(t.UTC().UnixNano())/86400e9 + 2440587.5
	return (jd - 2451545) / 36525
}

func precession(t time.Time) [3][3]float64 {
	tc := julianCenturies(t)
	as := rad / 3600
	zeta := (2306.2181*tc + 0.30188*tc*tc + 0.017998*tc*tc*tc) * as
	z := (2306.2181*tc + 1.09468*tc*tc + 0.018203*tc*tc*tc) * as
	theta := (2004.3109*tc - 0.42665*tc*tc - 0.041833*tc*tc*tc) * as
	cz, sz := math.Cos(zeta), math.Sin(zeta)
	cZ, sZ := math.Cos(z), math.Sin(z)
	ct, st := math.Cos(theta), math.Sin(theta)
	return [3][3]float64{
		{cz*ct*cZ - sz*sZ, -sz*ct*cZ - cz*sZ, -st * cZ},
		{cz*ct*sZ + sz*cZ, -sz*ct*sZ + cz*cZ, -st * sZ},
		{cz * st, -sz * st, ct},
	}
}

func raDecVec(ra, dec float64) Vec {
	return Vec{math.Cos(dec*rad) * math.Cos(ra*rad), math.Cos(dec*rad) * math.Sin(ra*rad), math.Sin(dec * rad)}
}

func vecRADec(v Vec) (ra, dec float64) {
	v = v.Unit()
	return wrap360(math.Atan2(v[1], v[0]) / rad), math.Asin(clampUnit(v[2])) / rad
}

func J2000ToDate(ra, dec float64, t time.Time) (float64, float64) {
	p := precession(t)
	v := raDecVec(ra, dec)
	var o Vec
	for i := range 3 {
		o[i] = p[i][0]*v[0] + p[i][1]*v[1] + p[i][2]*v[2]
	}
	return vecRADec(o)
}

func DateToJ2000(ra, dec float64, t time.Time) (float64, float64) {
	p := precession(t)
	v := raDecVec(ra, dec)
	var o Vec
	for i := range 3 {
		o[i] = p[0][i]*v[0] + p[1][i]*v[1] + p[2][i]*v[2]
	}
	return vecRADec(o)
}

func (s Site) AltAzToJ2000(az, alt float64, t time.Time) (ra, dec float64) {
	ha, d := s.AltAzToHADec(az, alt)
	return DateToJ2000(wrap360(s.LST(t)-ha), d, t)
}
