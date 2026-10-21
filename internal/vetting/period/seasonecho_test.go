package period

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// season28741 is the ZTF g light curve of source 28741 (Gaia DR3
// 3402151557757159552, Crab, 2026-02-05), its 412 good points from ZTF
// DR in two fields (oid). Its season means change by 0.20 mag from 2018
// to 2025, and nightly visits turn those changes into a 0.9969-day cycle.
const season28741 = "testdata/season28741/ztf_g.csv.zst"

func load28741(t *testing.T) []lc.Series {
	t.Helper()
	by := map[string]*lc.Series{}
	var order []string
	for _, r := range readCSV(t, season28741) {
		s, ok := by[r["oid"]]
		if !ok {
			s = &lc.Series{Survey: lc.ZTF, Band: "g", Dilution: 1}
			by[r["oid"]] = s
			order = append(order, r["oid"])
		}
		s.Points = append(s.Points, lc.Point{BJD: num(t, r["bjd"]) + 2450000, Mag: num(t, r["mag"]), Err: num(t, r["err"])})
	}
	out := make([]lc.Series, 0, len(order))
	for _, o := range order {
		out = append(out, *by[o])
	}
	return out
}

func echoCheck(t *testing.T, res *Result) check.Check {
	t.Helper()
	i := slices.IndexFunc(res.Checks, func(c check.Check) bool { return c.ID == check.Echo })
	if i < 0 {
		t.Fatalf("no echo row: %+v", res.Checks)
	}
	return res.Checks[i]
}

// TestSeasonEcho28741 needs the vet to say that the 0.9969-day cycle of
// 28741, next to one sidereal day, goes away once each season's level is
// taken out: its Lomb-Scargle power in ZTF g falls from 0.52 to under
// 0.1. The vet used to say only that TESS was needed. It must not refute
// the cycle on that alone; TestSeasonEchoKeepsRealCycle shows why.
func TestSeasonEcho28741(t *testing.T) {
	t.Parallel()
	res, err := Analyze(load28741(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	e, r := res.Ephemeris, res.Robust
	if e == nil || r == nil || e.Kind != KindSinusoidal || math.Abs(e.Period-0.99688) > 0.0005 {
		t.Fatalf("want the 0.9969-day smooth cycle, got %+v %+v", e, r)
	}
	if r.Echo != echoSidereal {
		t.Fatalf("echo %q, want one sidereal day", r.Echo)
	}
	c := echoCheck(t, res)
	t.Logf("%s raw=%.3f season=%.3f: %s", c.Status, r.RawPower, r.SeasonPower, c.Evidence)
	if !r.SeasonEcho || c.Status != check.NotRun || !strings.Contains(c.Evidence, "each season's level is taken out") {
		t.Errorf("echo row %s, want skipped and naming the season levels: %s", c.Status, c.Evidence)
	}
	if r.RawPower < 0.4 || r.SeasonPower > 0.1 {
		t.Errorf("power %.3f to %.3f, want about 0.5 to under 0.1", r.RawPower, r.SeasonPower)
	}
}

// TestSeasonEchoKeepsRealCycle puts a 0.03 mag cycle at the same period
// into 28741's points. A real cycle 1/2565 days per day off the sidereal
// rate drifts by under a tenth of a cycle per season at nightly visits,
// so it also goes away with the season levels: the row must not fail.
func TestSeasonEchoKeepsRealCycle(t *testing.T) {
	t.Parallel()
	const per = 0.99688
	in := load28741(t)
	for i := range in {
		for j := range in[i].Points {
			p := &in[i].Points[j]
			p.Mag += 0.03 * math.Sin(2*math.Pi*p.BJD/per)
		}
	}
	res, err := Analyze(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	e, r := res.Ephemeris, res.Robust
	if e == nil || r == nil || math.Abs(e.Period-per) > 0.0005 {
		t.Fatalf("want the %.5f-day cycle, got %+v", per, e)
	}
	c := echoCheck(t, res)
	t.Logf("%s raw=%.3f season=%.3f: %s", c.Status, r.RawPower, r.SeasonPower, c.Evidence)
	if c.Status == check.Fail {
		t.Errorf("real cycle refuted: %s", c.Evidence)
	}
}
