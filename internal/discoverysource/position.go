package discoverysource

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	numberRE = regexp.MustCompile(`[+-]?\d+(?:\.\d+)?`)
	signRE   = regexp.MustCompile(`[+-]\s*\d`)
	hmsRE    = regexp.MustCompile(`[hm:]`)
	otherRE  = regexp.MustCompile(`[0-9+\-.\s:hm]`)
)

// ParsePosition reads a position typed as decimal degrees ("314.32 28.72"),
// or as right ascension in hours, minutes and seconds and declination in
// degrees, minutes and seconds ("20h57m17s +28°43′07″", "20:57:17 +28:43:07",
// "20 57 17 28 43 07"). It answers the right ascension and declination in
// degrees.
func ParsePosition(q string) (float64, float64, bool) {
	q = strings.NewReplacer("−", "-", "′", " ", "″", " ", "'", " ", "\"", " ", "°", " ", "d", " ", "s", " ").Replace(strings.ToLower(q))
	if otherRE.ReplaceAllString(q, "") != "" {
		return 0, 0, false
	}
	nums := numberRE.FindAllString(q, -1)
	v := make([]float64, len(nums))
	for i, n := range nums {
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, 0, false
		}
		v[i] = f
	}
	var ra, dec float64
	switch {
	case len(v) == 2 && !hmsRE.MatchString(q):
		ra, dec = v[0], v[1]
	case len(v) == 6:
		ra = 15 * (v[0] + v[1]/60 + v[2]/3600)
		sign := 1.0
		if m := signRE.FindAllString(q, -1); (len(m) > 0 && strings.HasPrefix(m[len(m)-1], "-")) || v[3] < 0 {
			sign = -1
		}
		dec = sign * (math.Abs(v[3]) + math.Abs(v[4])/60 + math.Abs(v[5])/3600)
	default:
		return 0, 0, false
	}
	if ra < 0 || ra >= 360 || dec < -90 || dec > 90 {
		return 0, 0, false
	}
	return ra, dec, true
}
