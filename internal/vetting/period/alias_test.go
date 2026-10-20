package period

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// TestSlowFadeEcho167 is source 167 (Gaia DR3 387387289682776192), whose
// ZTF light curve faded by 0.25 mag in g over 2022 and 2023 and came back.
// Daily visits turn that slow fade into cycles of about one day: the vet
// once called it an eclipsing binary of 0.997727 days. Once the fade is
// taken out, ZTF g and r show a 4.318 day smooth cycle that its five
// comparison stars lack, and TESS sectors 17, 57 and 84 show 4.27 to 4.35
// days with nothing at one day. The vet must flag the period as a daily
// echo that nothing confirms, and name 4.318 days, not its 0.810 day
// echo, as the cycle left once the slow changes are taken out.
func TestSlowFadeEcho167(t *testing.T) {
	t.Parallel()
	series, comps := caseSeries(t, "testdata/alias/167.csv.zst")
	res, err := Analyze(series, Options{Comparisons: func() [][]lc.Series { return comps }})
	if err != nil {
		t.Fatal(err)
	}
	e, r := res.Ephemeris, res.Robust
	if e == nil || r == nil {
		t.Fatalf("no period tests: %+v", res)
	}
	t.Logf("%s %.6f echo=%q confirmed=%q real=%v partner=%.5f from %s", e.Kind, e.Period, r.Echo, r.Confirmed, r.Real, r.Partner, r.PartnerFrom)
	if r.Echo == "" {
		t.Errorf("%.6f days not flagged as a daily echo", e.Period)
	}
	if r.Confirmed != "" {
		t.Errorf("echo confirmed by %s", r.Confirmed)
	}
	if math.Abs(r.Partner-4.318) > 0.01 {
		t.Errorf("partner %.5f days, want 4.318", r.Partner)
	}
}
