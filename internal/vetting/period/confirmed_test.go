package period

import (
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// confirmedDir holds confirmed binaries' light curves the audit lacks or
// has too few surveys for: the ZTF points of 27822 and 1592 from their
// VSX packages, and for 45029, 17511, 15362 and 2301 the audit's ZTF and
// comparison stars with the TESS sectors the vet's TESS job measured on
// 2026-10-18 (and NEOWISE for 45029).
const confirmedDir = "testdata/confirmed"

// TestConfirmedBinaryPeriods runs the vet's own period search on every
// confirmed eclipsing binary, as the vet does, and needs the confirmed
// period, or the alternative its review left open, within the fit's
// errors. The search finds no period in the ZTF and TESS data of 2301,
// nor in the ZTF points the VSX packages of 27822 and 1592 keep; any
// period it does find for them must still match. 45029 has two equal
// dips with the second at phase 0.571 and came out at twice its period
// once TESS was added. 16040 has two equal eclipses per orbit that look
// like one 0.47 mag dip every 2.741971 days. 3635 has two nearly equal
// eclipses every 7.839490 days and runs on the vet's own input with its
// Gaia parallax, colour and reddening, which the dark companion rule needs
// to tell its one 26% dip every 3.92 days is half the orbit.
func TestConfirmedBinaryPeriods(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key  string
		path string
		want []float64
		err  float64
	}{
		{"45029", confirmedDir, []float64{21.08148}, 0.00007},
		{"43141", auditDir + "/c43141", []float64{4.129667}, 0},
		{"33629", auditDir + "/c33629", []float64{5.39172, 10.78344}, 0},
		{"33136", auditDir + "/c33136", []float64{44.5540}, 0},
		{"30141", auditDir + "/c30141", []float64{15.22282}, 0},
		{"23081", auditDir + "/c23081", []float64{11.944118}, 0},
		{"21142", auditDir + "/c21142", []float64{7.625329}, 0},
		{"49572", auditDir + "/c49572", []float64{4.111827}, 0},
		{"16658", auditDir + "/c16658", []float64{13.72664}, 0},
		{"15362", confirmedDir, []float64{52.392}, 0.003},
		{"6014", auditDir + "/c6014", []float64{6.690110}, 0},
		{"1688", auditDir + "/c1688", []float64{13.5715, 6.78575}, 0},
		{"618", auditDir + "/c618", []float64{2.256397}, 0},
		{"2301", confirmedDir, []float64{39.6865, 39.6865 / 2}, 0},
		{"4046", auditDir + "/c4046", []float64{14.62491}, 0},
		{"571", auditDir + "/c571", []float64{15.84854}, 0},
		{"16040", auditDir + "/16040", []float64{5.483943}, 0},
		{"17511", confirmedDir, []float64{38.4397, 76.8793}, 0},
		{"27822", confirmedDir, []float64{8.159494}, 0},
		{"1592", confirmedDir, []float64{49.6413}, 0},
		{"236", auditDir + "/ctlM31", []float64{9.98715}, 0},
		{"3754", auditDir + "/ctlIC1396", []float64{19.11938}, 0},
		{"31560", auditDir + "/ctlNAN", []float64{34.40645}, 0},
		{"3635", vet3635, []float64{7.839490}, 0},
	}
	searchMisses := map[string]bool{"2301": true, "27822": true, "1592": true}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			var res *Result
			var err error
			if c.path == vet3635 {
				series, comps := vetDumpSeries(t, vet3635)
				res, err = Analyze(series, opts3635(comps))
			} else {
				path := c.path + ".csv.zst"
				if c.path == confirmedDir {
					path = filepath.Join(confirmedDir, c.key+".csv.zst")
				}
				series, comps := caseSeries(t, path)
				res, err = Analyze(series, Options{Comparisons: func() [][]lc.Series { return comps }})
			}
			if errors.Is(err, ErrNoPeriod) && searchMisses[c.key] {
				t.Skip("the search finds no period in this data")
			}
			if err != nil {
				t.Fatalf("no period: %v", err)
			}
			e := res.Ephemeris
			t.Logf("%s %.6f +- %.6f", e.Kind, e.Period, e.PeriodErr)
			for _, id := range []string{check.Half, check.Double} {
				r := rows(res)[id]
				t.Logf("%s %s: %s", id, r.Status, r.Evidence)
			}
			if e.Kind != KindEclipsing {
				t.Fatalf("found a %s light curve, not an eclipsing binary", e.Kind)
			}
			for _, p := range c.want {
				if math.Abs(e.Period-p) <= math.Max(3*math.Hypot(e.PeriodErr, c.err), 2e-5*p) {
					return
				}
			}
			t.Errorf("period %.6f days, confirmed %v", e.Period, c.want)
		})
	}
}

// TestEqualEclipses16040 is source 16040 (Gaia DR3 1859916208643846656),
// whose ZTF light curve shows one 0.47 mag dip every 2.741971 days with
// nothing half a cycle later. A hand fit found its orbit at 5.483943 days
// with two eclipses alike in depth and width and the second at phase
// 0.49987 +- 0.00038: no companion dark enough to hide its own eclipse
// can block 35% of the light. The vet must take 5.483943 days with both
// eclipses alike and say the dark companion rule decided.
func TestEqualEclipses16040(t *testing.T) {
	t.Parallel()
	series, comps := auditSeries(t, "16040")
	res, err := Analyze(series, Options{Comparisons: func() [][]lc.Series { return comps }})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Ephemeris
	if e.Kind != KindEclipsing || len(e.Eclipses) != 2 || math.Abs(e.Period-5.483943) > math.Max(3*e.PeriodErr, 2e-5) {
		t.Fatalf("got %s with %d dips at %.6f +- %.6f, want two at 5.483943", e.Kind, len(e.Eclipses), e.Period, e.PeriodErr)
	}
	if ph := e.Eclipses[1].Phase; math.Abs(ph-0.5) > 0.002 {
		t.Errorf("Min II at phase %.5f, want 0.5", ph)
	}
	d0, d1 := e.Eclipses[0].Depth[lc.ZTF+" r"], e.Eclipses[1].Depth[lc.ZTF+" r"]
	if math.Abs(d0-0.47) > 0.03 || math.Abs(d0-d1) > 0.05 {
		t.Errorf("r depths %.3f and %.3f, want both about 0.47", d0, d1)
	}
	h := rows(res)[check.Half]
	t.Logf("%s: %s", h.Status, h.Evidence)
	if h.Status != check.Pass || !strings.HasPrefix(h.Evidence, NoDarkCompanion) {
		t.Errorf("half check %s: %s; want passed by the dark companion rule", h.Status, h.Evidence)
	}
	if d := rows(res)[check.Double]; d.Status == check.Fail {
		t.Errorf("double check failed: %s", d.Evidence)
	}
}
