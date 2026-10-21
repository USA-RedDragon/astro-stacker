package period

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

// oneDip3635 is the star:15 vet's ephemeris of source 3635: one dip
// every 3.919735 days.
func oneDip3635() Ephemeris {
	return Ephemeris{Period: 3.919735367653492, PeriodErr: 1.2857035930280572e-05, T0: 2458317.3639296764, T0Err: 0.005783115775778132, Kind: KindEclipsing,
		Eclipses: []Eclipse{{Phase: 0, Width: 0.03166251527803854, Shape: 1.39416297165816, Depth: map[string]float64{
			"ATLAS c": 0.14438179898922565, "ATLAS o": 0.19014528947232395, "NEOWISE W1": 0.30880236827873486,
			"NEOWISE W2": 0.2355212102749828, "TESS": 0.11856824085436138, ztfRBand: 0.19577203821877576}}}}
}

// TestOddEvenDepths3635 is source 3635, one dip every 3.919735 days in
// the star:15 vet: its odd and even cycles are its Min I and Min II, two
// nearly equal eclipses of a 7.839490 day orbit. ATLAS o once put them
// 2.7 sigma apart from per-cycle errors that left out the night-to-night
// offset, two camera 01a nights at PSF chi/N 14 to 262, and TESS sectors
// of different depth pooled. The double check must give the odd and even
// depths as equal within their errors in every band it can compare.
func TestOddEvenDepths3635(t *testing.T) {
	t.Parallel()
	series, comps := vetDumpSeries(t, vet3635)
	e := oneDip3635()
	res, err := AnalyzeAt(series, &e, opts3635(comps))
	if err != nil {
		t.Fatal(err)
	}
	d := rows(res)[check.Double]
	t.Logf("%s: %s", d.Status, d.Evidence)
	m := regexp.MustCompile(`([0-9.]+) sigma apart`).FindStringSubmatch(d.Evidence)
	if d.Status == check.Fail || !strings.HasPrefix(d.Evidence, SameDepthOddEven) || m == nil {
		t.Fatalf("double check does not give the measured odd and even depths: %s", d.Evidence)
	}
	if z, _ := strconv.ParseFloat(m[1], 64); z >= 2 {
		t.Errorf("odd and even depths %s sigma apart, want equal within errors", m[1])
	}
}
