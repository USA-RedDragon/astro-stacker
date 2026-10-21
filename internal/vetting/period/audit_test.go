package period

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// auditDir holds the 2026-10-17 audit's cases: the ZTF light curves of
// each star and of up to five nearby steady stars of its brightness (DR24
// from the SNAD mirror), TESS from TGLC where the binary review read it,
// NEOWISE for the two stars the vet took a NEOWISE-only period for, and
// the period and verdict the vet gave.
const auditDir = "testdata/audit"

// The audit's outcomes, as cases.json names them.
const (
	wantReal    = "real"
	wantNotReal = "not-real"
	wantAlias   = "alias"
)

// AuditCase is one audited star: Want is "real" for the confirmed and
// newly found binaries, "drifts" for the real but quasi-periodic 34867,
// "not-real" for false periods, "real-period" for catalogued stars whose
// period is right and "alias" for catalogued stars whose period was a
// daily-sampling echo.
type AuditCase struct {
	Key       string     `json:"key"`
	Group     string     `json:"group"`
	Want      string     `json:"want"`
	Kind      string     `json:"kind"`
	Period    float64    `json:"period"`
	G         float64    `json:"g"`
	Ephemeris *Ephemeris `json:"ephemeris"`
}

func auditCases(t *testing.T) []AuditCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(auditDir, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cs []AuditCase
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatal(err)
	}
	return cs
}

// auditSeries reads a case's light curves: the star's, one series per
// survey band (per TESS sector), and each comparison star's. ZTF points
// carry their ZTF object id, one per field and CCD quadrant.
func auditSeries(t *testing.T, key string) ([]lc.Series, [][]lc.Series) {
	t.Helper()
	return caseSeries(t, filepath.Join(auditDir, key+".csv.zst"))
}

// caseSeries reads a case file in the audit's layout: one row per point
// with who ("t" for the star, "c0", "c1", ... for comparison stars),
// survey, band, bjd - 2450000, mag, err and the ZTF object id.
func caseSeries(t *testing.T, path string) ([]lc.Series, [][]lc.Series) {
	t.Helper()
	rows := readCSV(t, path)
	type sk struct{ who, survey, band string }
	by := map[sk][]lc.Point{}
	var order []sk
	for _, r := range rows {
		k := sk{r["who"], r["survey"], r["band"]}
		if _, ok := by[k]; !ok {
			order = append(order, k)
		}
		e := 0.0
		if r["err"] != "" {
			e = num(t, r["err"])
		}
		src, _ := strconv.ParseInt(r["oid"], 10, 64)
		by[k] = append(by[k], lc.Point{BJD: num(t, r["bjd"]) + 2450000, Mag: num(t, r["mag"]), Err: e, Src: src})
	}
	var target []lc.Series
	comps := map[string][]lc.Series{}
	var names []string
	for _, k := range order {
		s := lc.Series{Survey: k.survey, Band: k.band, Points: by[k], Dilution: 1}
		if k.survey == lc.TESS {
			s.Relative, s.Deblended = true, true
		}
		if k.who == "t" {
			target = append(target, s)
			continue
		}
		if _, ok := comps[k.who]; !ok {
			names = append(names, k.who)
		}
		comps[k.who] = append(comps[k.who], s)
	}
	slices.SortFunc(names, func(a, b string) int {
		x, _ := strconv.Atoi(a[1:])
		y, _ := strconv.Atoi(b[1:])
		return x - y
	})
	out := make([][]lc.Series, 0, len(names))
	for _, n := range names {
		out = append(out, comps[n])
	}
	return target, out
}

// auditResult runs the vet's checks on a case at the period the vet
// claimed, or at the one its search finds when the audit has no claim.
// It is nil when no survey but NEOWISE sees the period at all.
func auditResult(t *testing.T, c AuditCase) *Result {
	t.Helper()
	series, comps := auditSeries(t, c.Key)
	opts := Options{Comparisons: func() [][]lc.Series { return comps }}
	var res *Result
	var err error
	if c.Ephemeris != nil {
		res, err = AnalyzeAt(series, c.Ephemeris, opts)
	} else {
		res, err = Analyze(series, opts)
	}
	if errors.Is(err, ErrNoPeriod) {
		return nil
	}
	if err != nil {
		t.Fatalf("%s: %v", c.Key, err)
	}
	if res.Robust == nil {
		t.Fatalf("%s: no robustness tests", c.Key)
	}
	return res
}

// TestAuditPeriods is the 2026-10-17 audit as known answers: the 19
// confirmed eclipsing binaries and the two new ones must pass every test
// for false periods, and none of the false or echoed periods may. The
// audit's stars whose cycle nearby steady stars share (failure mode A2)
// must fail the comparison test.
func TestAuditPeriods(t *testing.T) {
	t.Parallel()
	sameCycleInComparisons := map[string]bool{"4978": true, "16528": true, "26697": true, "28513": true, "49093": true}
	for _, c := range auditCases(t) {
		t.Run(c.Key, func(t *testing.T) {
			t.Parallel()
			res := auditResult(t, c)
			if res == nil {
				if c.Want != wantNotReal && c.Want != wantAlias {
					t.Fatal("the period is not seen in the survey data")
				}
				return
			}
			r := res.Robust
			slow := r.Echo == "" && r.Stable || r.Confirmed != ""
			holds := r.Real && slow && r.CompPass
			t.Logf("%s %s at %.5f: %+v", c.Key, res.Ephemeris.Kind, res.Ephemeris.Period, *r)
			switch c.Want {
			case wantReal, "real-period", "drifts":
				if !holds {
					t.Errorf("a real period fails the tests: %+v", *r)
				}
			case wantNotReal, wantAlias:
				if holds {
					t.Errorf("a false period passes the tests: %+v", *r)
				}
			default:
				t.Fatalf("unknown want %q", c.Want)
			}
			if sameCycleInComparisons[c.Key] && r.CompPass {
				t.Errorf("nearby steady stars share the cycle, but the comparison test passed: %+v", *r)
			}
		})
	}
}

// holds says a result passes every test for false periods, as the vet's
// verdict needs to call it real.
func holds(r *Robust) bool {
	slow := r.Echo == "" && r.Stable || r.Confirmed != "" || r.TESS
	return r.Real && slow && r.CompPass
}

// otherCycle is the true period of an audit case whose audited period is
// false: 167's 0.9977 day eclipses were a daily echo of its slow fade,
// and ZTF g and r and TESS sectors 17, 57 and 84 show a 4.318 day cycle.
func otherCycle() map[string]float64 { return map[string]float64{"167": 4.318} }

// TestAuditSearch runs the vet's own period search on each audit case, as
// production does: no false or echoed star may come out with a period
// that passes, and a binary whose period the search finds (at the
// period, or half or twice it) must pass. From ZTF alone the search
// misses 17511 (gain 56), 15362 (its 26.2 day candidate is sixth) and
// 2301 (two cycles); the vet had more surveys for them.
func TestAuditSearch(t *testing.T) {
	t.Parallel()
	for _, c := range auditCases(t) {
		t.Run(c.Key, func(t *testing.T) {
			t.Parallel()
			series, comps := auditSeries(t, c.Key)
			res, err := Analyze(series, Options{Comparisons: func() [][]lc.Series { return comps }})
			if errors.Is(err, ErrNoPeriod) || res.Robust == nil {
				if c.Want == wantReal && !slices.Contains([]string{"17511", "c15362", "c2301"}, c.Key) {
					t.Errorf("the search found no period")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			e, r := res.Ephemeris, res.Robust
			t.Logf("%s %s at %.5f: %+v", c.Key, e.Kind, e.Period, *r)
			switch c.Want {
			case wantNotReal, wantAlias:
				if p, ok := otherCycle()[c.Key]; ok && math.Abs(e.Period/p-1) < 0.01 {
					if !holds(r) {
						t.Errorf("the star's own %.3f day cycle fails: %+v", p, *r)
					}
					return
				}
				if holds(r) {
					t.Errorf("a false star's %s %.5f day period passes: %+v", e.Kind, e.Period, *r)
				}
			case wantReal:
				q := e.Period / c.Period
				same := false
				for _, k := range []float64{0.5, 1, 2} {
					same = same || math.Abs(q/k-1) < 0.01
				}
				if same && e.Kind == KindEclipsing && !holds(r) {
					t.Errorf("the search found the binary at %.5f days, but it fails: %+v", e.Period, *r)
				}
			}
		})
	}
}

// TestFlatTESSConfirmsNothing adds a flat TESS sector, 5 mmag noise at
// half hour cadence, to confirmed binaries: TESS shows no dips, so it must
// never confirm them.
func TestFlatTESSConfirmsNothing(t *testing.T) {
	t.Parallel()
	byKey := map[string]AuditCase{}
	for _, c := range auditCases(t) {
		byKey[c.Key] = c
	}
	for _, key := range []string{"c16658", "c45029", "c33136"} {
		c := byKey[key]
		series, _ := auditSeries(t, key)
		for seed := range 12 {
			rng := lcg(100 + seed)
			start := 2458700 + 900*rng.next()
			s := lc.Series{Survey: lc.TESS, Band: "S" + strconv.Itoa(14+seed), Relative: true, Deblended: true, Dilution: 1}
			for i := range 1300 {
				s.Points = append(s.Points, lc.Point{BJD: start + float64(i)/48, Mag: 0.005 * rng.gauss(), Err: 0.005})
			}
			res, err := AnalyzeAt(append(slices.Clone(series), s), c.Ephemeris, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Robust.TESS {
				t.Errorf("%s seed %d: a flat TESS sector confirms the dips: %+v", key, seed, *res.Robust)
			}
		}
	}
}
