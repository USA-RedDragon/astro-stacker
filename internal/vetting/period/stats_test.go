package period

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// TestStatsMatchAstropy checks the periodogram power, Baluev false alarm
// probability, sine amplitude and box search against astropy 8.0 on the
// same ZTF r points (LombScargle with errors floored at 5 mmag,
// BoxLeastSquares with objective snr and oversample 20).
func TestStatsMatchAstropy(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		key                string
		period             float64
		power, logFAP, amp float64
		box                float64
	}{
		{"4272", 0.9997318452395556, 0.48747, -291.62, 0.069585, 112.20},
		{"16040", 2.741972, 0.039656, -4.321, 0.025282, 154.04},
	} {
		series, _ := auditSeries(t, c.key)
		var r []lc.Series
		for _, s := range series {
			if s.Survey == lc.ZTF && s.Band == "r" {
				r = append(r, s)
			}
		}
		d := prepare(r)
		p := groupPts(d, 0, nil)
		pow := powerNear(p, c.period)
		if math.Abs(pow/c.power-1) > 0.01 {
			t.Errorf("%s: power %.5f, astropy %.5f", c.key, pow, c.power)
		}
		if lf := math.Log10(fap(pow, p)); math.Abs(lf-c.logFAP) > 0.3 {
			t.Errorf("%s: log10 FAP %.2f, astropy %.2f", c.key, lf, c.logFAP)
		}
		if f, ok := fitSineAt(p, c.period); !ok || math.Abs(f.amp/c.amp-1) > 0.01 {
			t.Errorf("%s: amplitude %.5f, astropy %.5f", c.key, f.amp, c.amp)
		}
		if b := boxSearch(p, c.period, nil, nil); math.Abs(b.snr/c.box-1) > 0.03 {
			t.Errorf("%s: box SNR %.2f, astropy %.2f", c.key, b.snr, c.box)
		}
	}
}

func TestEchoOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		p      float64
		smooth bool
		want   string
	}{
		{0.9997318, true, "one day"},
		{0.9977268, false, "one sidereal day"},
		{0.4997585, true, "half a day"},
		{2.99779, false, "three days"},
		{1.03, true, ""},
		{0.4648, true, ""},
		{2.256397, false, ""},
		{14.62491, false, ""},
		{14.62491, true, "the Moon's cycle"},
		{29.6, true, "the Moon's cycle"},
		{50.187, true, ""},
	} {
		if got := echoOf(c.p, 2700, c.smooth); got != c.want {
			t.Errorf("echoOf(%g, smooth %v) = %q, want %q", c.p, c.smooth, got, c.want)
		}
	}
}

// TestNoPeriodFromNEOWISEAlone is sources 17073 and 49921: with no star
// matched at the spot the vet read only NEOWISE, whose few visits a year
// gave 60.1 and 19.9 day periods that ZTF of the star does not show.
func TestNoPeriodFromNEOWISEAlone(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"17073", "49921"} {
		series, _ := auditSeries(t, key)
		var neo []lc.Series
		for _, s := range series {
			if s.Survey == lc.NEOWISE {
				neo = append(neo, s)
			}
		}
		if len(neo) == 0 {
			t.Fatalf("%s: no NEOWISE points", key)
		}
		res, err := Analyze(neo, Options{})
		if err == nil && res.Ephemeris != nil && res.Ephemeris.Period > 0 {
			t.Errorf("%s: NEOWISE alone gave a %.3f day %s period", key, res.Ephemeris.Period, res.Ephemeris.Kind)
		}
		res, err = Analyze(series, Options{})
		if err == nil && res.Robust != nil && res.Robust.Real {
			t.Errorf("%s: ZTF and NEOWISE gave a %.3f day period that passes: %+v", key, res.Ephemeris.Period, *res.Robust)
		}
	}
}
