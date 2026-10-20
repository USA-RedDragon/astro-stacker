package period

import (
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// tessSectors makes TESS series at the times of 28741's sectors 43 to
// 45 and 71, ten-minute points with noise sd, the star's share of the
// light dil, and a cycle of semi-amplitude amp at per seen through it.
func tessSectors(sd, dil, amp, per float64) []lc.Series {
	rng := rand.New(rand.NewPCG(28741, 71))
	var out []lc.Series
	for _, sec := range []struct {
		n  int
		t0 float64
	}{{43, 2459474.0}, {44, 2459500.5}, {45, 2459526.0}, {71, 2460207.0}} {
		s := lc.Series{Survey: lc.TESS, Note: "sector " + strconv.Itoa(sec.n), Relative: true, Dilution: dil}
		for t := sec.t0; t < sec.t0+25; t += 10.0 / 1440 {
			if t > sec.t0+12 && t < sec.t0+13.5 {
				continue
			}
			y := dilute(amp, dil)*math.Sin(2*math.Pi*t/per) + rng.NormFloat64()*sd
			s.Points = append(s.Points, lc.Point{BJD: t, Mag: y, Err: sd})
		}
		out = append(out, s)
	}
	return out
}

func tessCheck(t *testing.T, res *Result) check.Check {
	t.Helper()
	i := slices.IndexFunc(res.Checks, func(c check.Check) bool { return c.ID == check.TESS })
	if i < 0 {
		t.Fatalf("no TESS row: %+v", res.Checks)
	}
	return res.Checks[i]
}

// TestTESSRules28741 is 28741's ZTF g with TESS sectors like its own:
// half the light in the TESS aperture is the star's, and with that taken
// out TESS holds a cycle at 0.9969 days, at twice and at half of it, to a
// few mmag against ZTF g's 0.077 mag. The vet used to say the change was
// too faint for TESS to see; it must refute the period. A 0.03 mag cycle
// in the same TESS points must pass, and TESS too noisy or blended to
// recover a cycle of the ZTF size must not refute it.
func TestTESSRules28741(t *testing.T) {
	t.Parallel()
	const per = 0.996882
	cases := []struct {
		name     string
		sd, dil  float64
		amp      float64
		want     check.Status
		wantRule bool
	}{
		{"flat", 0.02, 0.5, 0, check.Fail, true},
		{"real cycle", 0.02, 0.5, 0.03, check.Pass, false},
		{"faint and blended", 0.4, 0.05, 0, check.NotRun, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			res, err := Analyze(append(load28741(t), tessSectors(c.sd, c.dil, c.amp, per)...), Options{})
			if err != nil {
				t.Fatal(err)
			}
			e, r := res.Ephemeris, res.Robust
			if e == nil || r == nil || e.Kind != KindSinusoidal || math.Abs(e.Period-per) > 0.0005 {
				t.Fatalf("want the 0.9969-day smooth cycle, got %+v", e)
			}
			tc := tessCheck(t, res)
			t.Logf("%s rule=%+v: %s", tc.Status, r.TESSRule, tc.Evidence)
			if tc.Status != c.want || (r.TESSRule != nil) != c.wantRule {
				t.Errorf("TESS row %s rule %v, want %s %v", tc.Status, r.TESSRule != nil, c.want, c.wantRule)
			}
		})
	}
}
