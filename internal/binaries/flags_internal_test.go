package binaries

import (
	"slices"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

func report(e *period.Ephemeris, half, double check.Check) *vetting.Report {
	return &vetting.Report{Ephemeris: e, Groups: []vetting.Group{{ID: check.GroupSurveys, Checks: []check.Check{half, double}}}}
}

func oneDip() *period.Ephemeris {
	return &period.Ephemeris{Period: 5.4, PeriodErr: 5e-6, T0: 2458143.4, T0Err: 0.002, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{{Phase: 0, Width: 0.018, Shape: 3, Depth: map[string]float64{keyZTFr: 0.31, "ZTF g": 0.35}}}}
}

func twoDips(phase2, d2 float64) *period.Ephemeris {
	return &period.Ephemeris{Period: 13.7, PeriodErr: 2e-5, T0: 2458149.2, T0Err: 0.002, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.018, Shape: 8, Depth: map[string]float64{keyZTFr: 0.48}},
			{Phase: phase2, Width: 0.015, Shape: 8, Depth: map[string]float64{keyZTFr: d2}},
		}}
}

func ids(fs []Flag) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}

func goodCoverage() []eclfacts.Coverage {
	return []eclfacts.Coverage{{Points: 40, Events: 20}, {Points: 30, Events: 15}}
}

func TestFlags(t *testing.T) {
	t.Parallel()
	pass := func(id, ev string) check.Check { return check.New(id, check.Pass, ev) }
	sameDepth := pass(check.Double, period.SameDepthOddEven+", so the period is not 10.78 days.")
	cases := []struct {
		name string
		r    *vetting.Report
		f    func(Facts) Facts
		want []string
	}{
		{"clean two dips", report(twoDips(0.43, 0.3), pass(check.Half, "differ"), pass(check.Double, "Ruled out")),
			func(f Facts) Facts {
				f.Survey = &eclfacts.Summary{Coverage: goodCoverage(), Twin: &eclfacts.Twin{Band: keyZTFr, DepthZ: 9, PhaseZ: 20, PhaseErr: 0.001}}
				return f
			}, []string{}},
		{"one dip, same depth on odd and even (old pass)", report(oneDip(), pass(check.Half, "normal"), sameDepth), nil, []string{FlagTwice}},
		{"one dip, double undecided", report(oneDip(), pass(check.Half, "normal"), check.New(check.Double, check.NotRun, period.SameDepthOddEven+", so two equal dips fit.")), nil, []string{FlagTwice}},
		{"one dip ruled out on both parities", report(oneDip(), pass(check.Half, "normal"), pass(check.Double, "Ruled out: ZTF only saw the dip on even cycles, but ATLAS caught it on odd cycles.")), nil, []string{}},
		{"one dip that differs odd and even is doubled, alike at 2P", report(oneDip(), pass(check.Half, "normal"), check.New(check.Double, check.Fail, "The dip is 0.3 mag deep on even cycles but 0.1 on odd cycles in r, so the true period may be 10.8 days.")),
			func(f Facts) Facts {
				f.Survey = &eclfacts.Summary{Coverage: goodCoverage(), Twin: &eclfacts.Twin{Band: keyZTFr, DepthZ: 0.1, PhaseZ: 1, PhaseErr: 0.001}}
				return f
			}, []string{FlagHalf}},
		{"doubled and the halves differ", report(oneDip(), pass(check.Half, "normal"), check.New(check.Double, check.Fail, "differ")),
			func(f Facts) Facts {
				f.Survey = &eclfacts.Summary{Coverage: goodCoverage(), Twin: &eclfacts.Twin{Band: keyZTFr, DepthZ: 8, PhaseZ: 1, PhaseErr: 0.001}}
				return f
			}, []string{}},
		{"half check failed", report(twoDips(0.5, 0.47), check.New(check.Half, check.Fail, "The two dips match in depth."), pass(check.Double, "Ruled out")), nil, []string{FlagHalf}},
		{"half passed weakly", report(twoDips(0.5007, 0.30), pass(check.Half, "The two dips have different depths."), pass(check.Double, "Ruled out")),
			func(f Facts) Facts {
				f.Survey = &eclfacts.Summary{Coverage: goodCoverage(), Twin: &eclfacts.Twin{Band: keyZTFr, DepthZ: 1.7, Depth1: eclfacts.Depth{Value: 0.4}, Depth2: eclfacts.Depth{Value: 0.3}, Phase2: 0.5007, PhaseErr: 0.002, PhaseZ: 0.35}}
				return f
			}, []string{FlagHalf}},
		{"few points in the deeper dip", report(twoDips(0.49, 0.57), pass(check.Half, "phase"), pass(check.Double, "Ruled out")),
			func(f Facts) Facts {
				f.Survey = &eclfacts.Summary{Coverage: []eclfacts.Coverage{{Points: 40, Events: 20}, {Points: 6, Events: 5}}, Twin: &eclfacts.Twin{Band: keyZTFr, DepthZ: 4, PhaseZ: 9, PhaseErr: 0.001}}
				return f
			}, []string{FlagFew}},
		{"few eclipses", report(twoDips(0.4, 0.17), pass(check.Half, "phase"), check.New(check.Double, check.Fail, "deep dip differs")),
			func(f Facts) Facts {
				f.Survey = &eclfacts.Summary{Coverage: []eclfacts.Coverage{{Points: 12, Events: 4}, {Points: 10, Events: 3}}, Twin: &eclfacts.Twin{Band: keyZTFr, DepthZ: 4, PhaseZ: 50, PhaseErr: 0.001}}
				return f
			}, []string{FlagFew}},
		{"blend", report(twoDips(0.496, 0.14), pass(check.Half, "phase"), pass(check.Double, "Ruled out")),
			func(f Facts) Facts {
				f.Survey = &eclfacts.Summary{Coverage: goodCoverage(), Twin: &eclfacts.Twin{Band: keyZTFr, DepthZ: 6, PhaseZ: 7.6, PhaseErr: 0.0005}}
				f.Gaia = &GaiaFacts{RUWE: 15.95}
				return f
			}, []string{FlagBlend}},
		{"RUWE just under the limit", report(twoDips(0.67, 0.21), pass(check.Half, "phase"), pass(check.Double, "Ruled out")),
			func(f Facts) Facts {
				f.Survey = &eclfacts.Summary{Coverage: goodCoverage(), Twin: &eclfacts.Twin{Band: keyZTFr, DepthZ: 1, PhaseZ: 90, PhaseErr: 0.001}}
				f.Gaia = &GaiaFacts{RUWE: 1.36}
				return f
			}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e, dbl := adopt(c.r)
			f := Facts{Ephemeris: e, Doubled: dbl, Deeper: deeper(e)}
			if c.f != nil {
				f = c.f(f)
			}
			got := Flags(c.r, f)
			if !slices.Equal(ids(got), c.want) {
				t.Errorf("flags %v, want %v: %+v", ids(got), c.want, got)
			}
			for _, fl := range got {
				if fl.Label == "" || fl.Why == "" {
					t.Errorf("flag %+v has no label or reason", fl)
				}
			}
		})
	}
}

func TestDoubledStarShowsTwiceThePeriod(t *testing.T) {
	t.Parallel()
	r := report(oneDip(), check.New(check.Half, check.Pass, "normal"), check.New(check.Double, check.Fail, "differs"))
	r.Suggestion.Confidence = "high"
	b := Star{Report: r}.View(nil)
	if b.PeriodText != "10.80000" || b.AltPeriodText != "or 5.40000 (half that)" || b.PeriodSettled {
		t.Errorf("period %q alt %q settled %v", b.PeriodText, b.AltPeriodText, b.PeriodSettled)
	}
	if len(b.Eclipses) != 2 || b.Type != "EA" {
		t.Errorf("eclipses %+v type %s", b.Eclipses, b.Type)
	}
}

func TestTypeOf(t *testing.T) {
	t.Parallel()
	if ty, w := TypeOf(twoDips(0.43, 0.3)); ty != "EA" || w != "EA, oval orbit" {
		t.Errorf("eccentric detached: %s %q", ty, w)
	}
	if ty, w := TypeOf(twoDips(0.502, 0.3)); ty != "EA" || w != "EA" {
		t.Errorf("circular detached: %s %q", ty, w)
	}
	wide := &period.Ephemeris{Period: 0.4, Kind: period.KindEclipsing, Eclipses: []period.Eclipse{
		{Phase: 0, Width: 0.25, Shape: 0.5, Depth: map[string]float64{keyZTFr: 0.5}},
		{Phase: 0.5, Width: 0.25, Shape: 0.5, Depth: map[string]float64{keyZTFr: 0.45}},
	}}
	if ty, _ := TypeOf(wide); ty != "EW" {
		t.Errorf("short period with wide equal dips: %s, want EW", ty)
	}
	wide.Period = 3
	if ty, _ := TypeOf(wide); ty != "EB" {
		t.Errorf("long period with wide dips: %s, want EB", ty)
	}
}
