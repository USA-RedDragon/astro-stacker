package period

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// TestSlowFadeEcho167 is source 167 (Gaia DR3 387387289682776192), whose
// ZTF light curve faded by 0.25 mag in g over 2022 and 2023 and came back.
// Daily visits turn that slow fade into cycles of about one day: the vet
// once called it an eclipsing binary of 0.997727 days, then a 0.99965 day
// cycle next to one day. Once the fade is taken out, ZTF g and r show a
// 4.318 day smooth cycle that its five comparison stars lack, and TESS
// sectors 17, 57 and 84 show 4.27 to 4.35 days with nothing at one day.
// The vet must report 4.318 days as the period, with the daily echo it
// replaces, and the season to season fade in g, r and i.
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
	t.Logf("%s %.6f echo=%q real=%v bands=%d alias=%.5f %q", e.Kind, e.Period, r.Echo, r.Real, r.Bands, r.Alias, r.AliasEcho)
	if e.Kind != KindSinusoidal || math.Abs(e.Period-4.318) > 0.01 {
		t.Errorf("period %s %.5f days, want sinusoidal 4.318", e.Kind, e.Period)
	}
	if r.Echo != "" || !r.Real || r.Bands < 2 {
		t.Errorf("4.318 days not real in two bands: echo=%q real=%v bands=%d", r.Echo, r.Real, r.Bands)
	}
	if r.AliasEcho != "one day" || math.Abs(r.Alias-1) > 0.01 {
		t.Errorf("replaced %.5f days next to %q, want the daily echo", r.Alias, r.AliasEcho)
	}
	if !res.Trend.Proven() {
		t.Fatalf("season to season fade not found: %+v", res.Trend)
	}
	for _, b := range res.Trend.Bands {
		t.Logf("trend %s range=%.3f z=%.1f comp=%.3f n=%d", b.Band, b.Range, b.Z, b.CompRange, b.Comparisons)
	}
	if l := res.Trend.Largest(); l.Band != lc.ZTF+" g" || math.Abs(l.Range-0.27) > 0.03 {
		t.Errorf("largest fade %s %.3f mag, want ZTF g about 0.27", l.Band, l.Range)
	}
}

func TestEchoOfNamesDailyAliases(t *testing.T) {
	t.Parallel()
	for p, want := range map[float64]string{0.997727: "one sidereal day", 1.0004: "one day", 0.4999: "half a day", 4.318: "", 9.3564: "", 7.83949: ""} {
		if got := EchoOf(p); got != want {
			t.Errorf("EchoOf(%g) = %q, want %q", p, got, want)
		}
	}
}
