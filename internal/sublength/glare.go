package sublength

import (
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
)

const (
	lunarCycleDays      = 29.53059
	glareFarSeparation  = 150.0
	glareFullSeparation = 30.0
)

type Bucket string

const (
	BucketDark   Bucket = "dark"
	BucketLow    Bucket = "low"
	BucketMid    Bucket = "mid"
	BucketBright Bucket = "bright"
)

const (
	GlareLow    = 0.05
	GlareMid    = 0.25
	GlareBright = 0.5
)

func Buckets() []Bucket { return []Bucket{BucketDark, BucketLow, BucketMid, BucketBright} }

func BucketOf(glare float64) Bucket {
	switch {
	case !(glare >= GlareLow):
		return BucketDark
	case glare < GlareMid:
		return BucketLow
	case glare < GlareBright:
		return BucketMid
	}
	return BucketBright
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func sunRA(t time.Time) float64 {
	const deg = math.Pi / 180
	T := (float64(t.UTC().UnixNano())/86400e9 + 2440587.5 - 2451545) / 36525
	l0 := 280.46646 + 36000.76983*T
	m := 357.52911 + 35999.05029*T
	c := (1.914602-0.004817*T)*math.Sin(m*deg) + 0.019993*math.Sin(2*m*deg) + 0.000289*math.Sin(3*m*deg)
	omega := 125.04 - 1934.136*T
	lambda := l0 + c - 0.00569 - 0.00478*math.Sin(omega*deg)
	eps := 23.439291 - 0.0130042*T + 0.00256*math.Cos(omega*deg)
	ra := math.Atan2(math.Cos(eps*deg)*math.Sin(lambda*deg), math.Cos(lambda*deg)) / deg
	return math.Mod(ra+360, 360)
}

func moonAge(t time.Time, moonRA float64) float64 {
	return math.Mod(moonRA-sunRA(t)+720, 360) * lunarCycleDays / 360
}

func Glare(t time.Time, ra, dec, lat, lon float64) float64 {
	p := moon.At(t)
	illum := (1 - math.Cos(2*math.Pi*moonAge(t, p.RA)/lunarCycleDays)) / 2
	up := clamp01((p.Altitude(t, lat, lon) + 5) / 10)
	near := clamp01((glareFarSeparation - p.Separation(ra, dec)) / (glareFarSeparation - glareFullSeparation))
	return clamp01(illum) * up * near
}
