package period

import (
	"math"
	"path/filepath"
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

// TestParityShrugsOffBadTESSCycle is source 618 with its real ZTF, TESS
// and NEOWISE light curves: sector 59 caught its shallow dip once with
// points from -2.5 to +3.6 mag around a dip of 0.1 to 0.2 mag. Summed raw,
// that cycle made the dip -0.19 mag deep on odd cycles in TESS, which
// failed the double check with 4.512794 days until TESS was dropped for
// brightening in the dip, losing its good cycles too. Per cycle depths
// combined robustly must show the dip in TESS on odd and even cycles
// alike, and the double check must pass.
func TestParityShrugsOffBadTESSCycle(t *testing.T) {
	t.Parallel()
	series, comps := caseSeries(t, filepath.Join(confirmedDir, "618.csv.zst"))
	e := &Ephemeris{Period: 2.2563969876833516, PeriodErr: 7.7e-7, T0: 2458841.0738453465, T0Err: 0.00035, Kind: KindEclipsing,
		Eclipses: []Eclipse{{Phase: 0, Width: 0.11962900330880054, Shape: 8}, {Phase: 0.5012543429930786, Width: 0.11328334067605096, Shape: 5.241445786017781}}}
	worst := 0.0
	for _, s := range series {
		if s.Survey != lc.TESS || s.Band != "S59" {
			continue
		}
		for c := 470.0; c < 485; c++ {
			mid := e.T0 + (c+e.Eclipses[1].Phase)*e.Period
			lo, hi := math.Inf(1), math.Inf(-1)
			for _, p := range s.Points {
				if math.Abs(p.BJD-mid) < e.Eclipses[1].Width*e.Period {
					lo, hi = math.Min(lo, p.Mag), math.Max(hi, p.Mag)
				}
			}
			worst = math.Max(worst, hi-lo)
		}
	}
	if worst < 5 {
		t.Fatalf("sector 59's worst shallow dip spans %.2f mag; the fixture lacks the bad cycle", worst)
	}
	a := &analysis{d: prepare(series), opts: Options{}}
	a.f = flatten(a.d)
	a.flatOff, a.flatScore = flatFit(a.f)
	a.fitAt(e)
	a.settle()
	a.alignSrc()
	ws := a.parityWindows()
	for g, gr := range a.d.groups {
		if gr.survey != lc.TESS {
			continue
		}
		ev, od := ws[1][0].gy[g], ws[1][1].gy[g]
		se, so := ev[0]/ev[1], od[0]/od[1]
		ee, eo := 1/math.Sqrt(ev[1]), 1/math.Sqrt(od[1])
		t.Logf("TESS shallow dip: even %.2f +- %.2f, odd %.2f +- %.2f of its depth", se, ee, so, eo)
		if se < 0.5 || so < 0.5 || math.Abs(se-so) > 4*math.Hypot(ee, eo) {
			t.Errorf("TESS shallow dip even %.2f +- %.2f, odd %.2f +- %.2f; want alike and real", se, ee, so, eo)
		}
		if !a.realDip(ws, 1, g) {
			t.Error("TESS is dropped from the shallow dip's odd and even test")
		}
	}
	res, err := Analyze(series, Options{Comparisons: func() [][]lc.Series { return comps }})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ephemeris.Eclipses) != 2 || math.Abs(res.Ephemeris.Period-2.256397) > 2e-5 {
		t.Fatalf("got %d dips at %.6f", len(res.Ephemeris.Eclipses), res.Ephemeris.Period)
	}
	if d := rows(res)[check.Double]; d.Status != check.Pass {
		t.Errorf("double check %s: %s", d.Status, d.Evidence)
	}
}
