package period

import (
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

// oneDip3635 is the star:15 vet's ephemeris of source 3635: one dip
// every 3.919735 days.
var oneDip3635 = Ephemeris{Period: 3.919735367653492, PeriodErr: 1.2857035930280572e-05, T0: 2458317.3639296764, T0Err: 0.005783115775778132, Kind: KindEclipsing,
	Eclipses: []Eclipse{{Phase: 0, Width: 0.03166251527803854, Shape: 1.39416297165816, Depth: map[string]float64{
		"ATLAS c": 0.14438179898922565, "ATLAS o": 0.19014528947232395, "NEOWISE W1": 0.30880236827873486,
		"NEOWISE W2": 0.2355212102749828, "TESS": 0.11856824085436138, "ZTF r": 0.19577203821877576}}}}

// TestOddEvenDepths3635 is source 3635, one dip every 3.919735 days in
// the star:15 vet, which said the dip has the same depth on odd and even
// cycles without measuring them. ATLAS o has the best measured odd and
// even depths, a few sigma apart; the double check must give them.
func TestOddEvenDepths3635(t *testing.T) {
	t.Parallel()
	series, comps := vetDumpSeries(t, vet3635)
	res, err := AnalyzeAt(series, &oneDip3635, opts3635(comps))
	if err != nil {
		t.Fatal(err)
	}
	d := rows(res)[check.Double]
	t.Logf("%s: %s", d.Status, d.Evidence)
	if d.Status == check.Pass || !strings.HasPrefix(d.Evidence, SameDepthOddEven) || !strings.Contains(d.Evidence, "in ATLAS o") || !strings.Contains(d.Evidence, "sigma apart") {
		t.Errorf("double check does not give the measured odd and even depths: %s", d.Evidence)
	}
}
