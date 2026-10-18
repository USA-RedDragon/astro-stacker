package period

import (
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// TestParityIgnoresBrighteningBand is source 1688: ZTF g and r show one
// 0.17 mag dip every 6.7857 days, equal on odd and even cycles, and the
// vet's ATLAS c light curve had the star 0.46 mag brighter inside the dip
// on even cycles and 0.14 fainter on odd ones, which doubled the period.
// A band that brightens in a dip says nothing about the eclipse, so the
// double check must leave twice the period open, not fail.
func TestParityIgnoresBrighteningBand(t *testing.T) {
	t.Parallel()
	ztf, _ := auditSeries(t, "c1688")
	base, err := Analyze(ztf, Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := base.Ephemeris
	if e.Kind != KindEclipsing || len(e.Eclipses) != 1 || math.Abs(e.Period-6.7857) > 0.001 {
		t.Fatalf("ZTF alone: %s at %.5f with %d dips, want one dip every 6.7857 days", e.Kind, e.Period, len(e.Eclipses))
	}
	rng := lcg(11)
	atlas := lc.Series{Survey: lc.ATLAS, Band: "c", Dilution: 1}
	w := e.Eclipses[0].Width
	for night := range 1600 {
		for k := range 3 {
			tt := 2457400 + float64(night)*1.9 + 0.2*rng.next() + float64(k)*0.012
			x := (tt - e.T0) / e.Period
			cyc := int(math.Round(x))
			sh := shapeOnly(x-float64(cyc), w, e.Eclipses[0].Shape)
			dip := 0.14 * sh
			if cyc%2 == 0 {
				dip = -0.46 * sh
			}
			atlas.Points = append(atlas.Points, lc.Point{BJD: tt, Mag: 15.9 + dip + 0.03*rng.gauss(), Err: 0.03})
		}
	}
	res, err := Analyze(append(ztf, atlas), Options{})
	if err != nil {
		t.Fatal(err)
	}
	e = res.Ephemeris
	if e.Kind != KindEclipsing || len(e.Eclipses) != 1 || math.Abs(e.Period-6.7857) > 0.001 {
		t.Fatalf("with ATLAS: %s at %.5f with %d dips, want one dip every 6.7857 days", e.Kind, e.Period, len(e.Eclipses))
	}
	d := rows(res)[check.Double]
	if d.Status == check.Fail || !strings.HasPrefix(d.Evidence, SameDepthOddEven) {
		t.Errorf("double %s: %s; want twice the period left open", d.Status, d.Evidence)
	}
}
