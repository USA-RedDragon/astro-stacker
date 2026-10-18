package binaries

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
)

func TestSameRunIgnoresNanoseconds(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 17, 17, 22, 19, 581537734, time.UTC)
	if !sameRun(at.Truncate(time.Microsecond), at) {
		t.Error("a run time stored to the microsecond is not the same run")
	}
	if sameRun(at, at.Add(time.Millisecond)) {
		t.Error("two runs a millisecond apart are the same")
	}
}

func TestRefineDoubled(t *testing.T) {
	t.Parallel()
	twin := func(d1, d2, e, z float64) *eclfacts.Summary {
		return &eclfacts.Summary{Coverage: goodCoverage(), Twin: &eclfacts.Twin{Band: keyZTFr, Depth1: eclfacts.Depth{Value: d1, Err: e}, Depth2: eclfacts.Depth{Value: d2, Err: e}, DepthZ: z, PhaseErr: 0.001}}
	}
	cases := []struct {
		name  string
		sum   *eclfacts.Summary
		dips  int
		alike bool
		deep  int
	}{
		{"equal halves", twin(0.17, 0.17, 0.01, 0.1), 2, true, 0},
		{"second half deeper", twin(0.1, 0.3, 0.01, 14), 2, false, 1},
		{"no dip on the other half", twin(0.3, 0.005, 0.01, 21), 1, false, 0},
	}
	for _, c := range cases {
		e := eclfacts.Doubled(oneDip())
		f := Facts{Ephemeris: e, Doubled: true, Survey: c.sum}
		f.refineDoubled(vetting.StarData{})
		if len(f.Ephemeris.Eclipses) != c.dips || f.Alike != c.alike || f.Deeper != c.deep {
			t.Errorf("%s: %d dips, alike %v, deeper %d", c.name, len(f.Ephemeris.Eclipses), f.Alike, f.Deeper)
		}
		r := report(oneDip(), goodHalf(), failedDouble())
		got := ids(Flags(r, f))
		wantHalf := c.alike
		if hasID(got, FlagHalf) != wantHalf {
			t.Errorf("%s: flags %v", c.name, got)
		}
	}
}

func goodHalf() check.Check { return check.New(check.Half, check.Pass, "normal") }

func failedDouble() check.Check {
	return check.New(check.Double, check.Fail, "The dip is 0.3 mag deep on even cycles but 0.1 on odd cycles in r, so the true period may be 10.8 days.")
}

func hasID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func TestRequestAttemptsAreTheirOwnProjects(t *testing.T) {
	t.Parallel()
	mid := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	if RequestGUID(1, KindPrimary, mid, 0) == RequestGUID(1, KindPrimary, mid, 1) {
		t.Error("a second attempt reuses the first one's project")
	}
	if RequestGUID(1, KindPrimary, mid, 0) == RequestGUID(1, KindSecondary, mid, 0) {
		t.Error("the two eclipses share a project")
	}
}
