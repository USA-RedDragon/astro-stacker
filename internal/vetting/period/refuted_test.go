package period

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// dipSeries is a survey band sampled about once a night for seven
// seasons, each point 0.4 mag fainter inside a 0.12-day dip every 5.3
// days when dips is set, with 0.02 mag noise.
func dipSeries(r *rand.Rand, survey, band string, dips bool, base float64) lc.Series {
	s := lc.Series{Survey: survey, Band: band, Dilution: 1}
	for night := range 7 * 200 {
		season := night / 200
		bjd := 2458300 + float64(season)*365.25 + float64(night%200) + 0.2*r.Float64()
		mag := base + 0.02*r.NormFloat64()
		ph := math.Mod(bjd-2458300.1, 5.3)
		if dips && (ph < 0.06 || ph > 5.3-0.06) {
			mag += 0.4
		}
		s.Points = append(s.Points, lc.Point{BJD: bjd, Mag: mag, Err: 0.02})
	}
	return s
}

// TestDipsOneSurveyRulesOut is dips every 5.3 days in ZTF that ATLAS,
// whose many points fall in them too, does not show: no one star's
// eclipses, so the analysis keeps no eclipse fit. With the same dips in
// ATLAS it keeps them.
func TestDipsOneSurveyRulesOut(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))
	ztf := dipSeries(r, lc.ZTF, "r", true, 15)
	res, err := Analyze([]lc.Series{ztf, dipSeries(r, lc.ATLAS, "o", true, 15)}, Options{})
	if err != nil || res.Ephemeris.Kind != KindEclipsing || math.Abs(res.Ephemeris.Period-5.3) > 0.001 {
		t.Fatalf("dips in both surveys: %v %+v", err, res.Ephemeris)
	}
	res, _ = Analyze([]lc.Series{ztf, dipSeries(r, lc.ATLAS, "o", false, 15)}, Options{})
	if res.Ephemeris != nil && res.Ephemeris.Kind == KindEclipsing {
		t.Errorf("ATLAS rules the dips out, yet the analysis kept a %.4f-day eclipse fit", res.Ephemeris.Period)
	}
}
