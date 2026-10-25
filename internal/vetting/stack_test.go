package vetting

import (
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
)

const (
	case182     = "182, AT 2026acsi"
	fewerFrames = "fewer frames than the detection tested"
	wingNight   = "2025-01-21"
)

// auditLight is the light, at zero point 25, the detections of the
// transient audit reported, set to 1000 so each night's error makes its
// fake show as the audit measured.
const auditLight = 1000.0

// auditNight is a night of 12 subs whose own signal to noise is z where
// the reported light shows at inj; the empty places around the spot give
// under 2.6 in 99 of 100, a little over what a normal scatter gives,
// 2.3, as the audit's empty places stood in units of their scatter.
func auditNight(night string, z, inj float64, flagged bool) ownimage.FluxNight {
	e := auditLight / inj
	return ownimage.FluxNight{Night: night, Filter: lum, N: 12, Flux: z * e, Err: e, Local: e, Empty99: 2.6 * e, Flagged: flagged}
}

func auditReps(night string) map[string]reported {
	return lightAt(auditLight/10, night)
}

func TestAuditedTransientsStackToTheirKnownAnswers(t *testing.T) {
	t.Parallel()
	// audit-novas/data/analysis.json, adjacent.json and dia_known.json: the
	// flagged night's local z and the reported light's, then the nights 1 to
	// 3 days after with 3 subs or more.
	cases := []struct {
		name   string
		nights []ownimage.FluxNight
		want   ownimage.StackVerdict
	}{
		{case182, []ownimage.FluxNight{auditNight("2026-10-11", 6.2, 5.8, true)}, ownimage.StackConfirmed},
		{"AT 2026uti", []ownimage.FluxNight{auditNight("2026-07-19", 10.7, 7.1, true), auditNight("2026-07-20", 9.4, 6.5, false)}, ownimage.StackConfirmed},
		{"AT 2025abrs", []ownimage.FluxNight{auditNight("2025-10-26", 10.3, 6.4, true), auditNight("2025-10-27", 7.9, 8.2, false)}, ownimage.StackConfirmed},
		{"AT 2025adss", []ownimage.FluxNight{auditNight("2025-11-21", 30.8, 10.7, true), auditNight("2025-11-22", 18.5, 4.4, false), auditNight("2025-11-24", 9.8, 11.8, false)}, ownimage.StackConfirmed},
		{"60465, bulge", []ownimage.FluxNight{auditNight("2025-10-26", 0.8, 12.1, true), auditNight("2025-10-27", -0.3, 8.4, false)}, ownimage.StackRefuted},
		{"60670, bulge", []ownimage.FluxNight{auditNight("2025-10-15", 2.7, 6.3, true), auditNight("2025-10-17", 0.0, 5.9, false), auditNight("2025-10-18", 0.3, 3.6, false)}, ownimage.StackRefuted},
		{"35876, bulge", []ownimage.FluxNight{auditNight("2025-10-18", 1.9, 10.1, true), auditNight("2025-10-19", -0.7, 5.2, false)}, ownimage.StackRefuted},
		{"60345, bulge with no later night", []ownimage.FluxNight{auditNight("2025-10-27", 0.3, 12.7, true)}, ownimage.StackRefuted},
		{"35878, bulge", []ownimage.FluxNight{auditNight("2025-10-18", 3.3, 7.9, true), auditNight("2025-10-19", 1.0, 4.7, false)}, ownimage.StackRefuted},
		{"58090, on a star", []ownimage.FluxNight{auditNight("2025-11-21", 3.5, 5.7, true), auditNight("2025-11-22", -1.5, 7.6, false)}, ownimage.StackDoubtful},
		{"66053, a star's wing", []ownimage.FluxNight{auditNight(wingNight, 0.4, 4.3, true)}, ownimage.StackRefuted},
	}
	for _, c := range cases {
		fc := curveOf(c.nights...)
		var first string
		for _, n := range c.nights {
			if n.Flagged {
				first = n.Night
			}
		}
		ch, v := transientStack(fc, auditReps(first))
		if v != c.want {
			t.Errorf("%s: %s, want %s: %s", c.name, v, c.want, ch.Evidence)
		}
	}
}

func TestStackReadsTheWingFitBesideABrightStar(t *testing.T) {
	t.Parallel()
	// 66233, 6.6 px from a G 13.94 star on 2025-01-21: light in the plain
	// fit, none left once the star is fitted with each sub's own shape.
	fc := curveOf(auditNight(wingNight, 9.0, 8.2, true))
	fc.Wing = &ownimage.Wing{G: 13.94, SepPx: 6.6, Night: wingNight, Filter: lum, Excess: 2.4 * 100, Sigma: 2.4, Err: 100, Measured: true}
	c, v := transientStack(fc, auditReps(wingNight))
	if v == ownimage.StackConfirmed || c.Status == check.Pass {
		t.Errorf("a wing confirmed: %s %s", v, c.Evidence)
	}
	fc.Wing.Sigma, fc.Wing.Excess = 9.5, 950
	if _, v := transientStack(fc, auditReps(wingNight)); v != ownimage.StackConfirmed {
		t.Errorf("new light beside a star, AT 2026uti-like, not confirmed: %s", v)
	}
}

func TestStackFreshFramesMustShowIt(t *testing.T) {
	t.Parallel()
	n := auditNight("2026-10-11", 12, 11, true)
	n.FreshN, n.FreshErr = 6, n.Err*math.Sqrt(2)
	n.FreshFlux = 0.2 * n.FreshErr
	if c, v := transientStack(curveOf(n), auditReps("2026-10-11")); v != ownimage.StackDoubtful {
		t.Errorf("flat fresh frames: %s %s", v, c.Evidence)
	}
	n.FreshFlux = 3.5 * n.FreshErr
	if c, v := transientStack(curveOf(n), auditReps("2026-10-11")); v != ownimage.StackConfirmed {
		t.Errorf("fresh frames showing it: %s %s", v, c.Evidence)
	}
}

func TestStackRefutesLightTooWeakToHaveBeenFound(t *testing.T) {
	t.Parallel()
	// needs-a-look-12: 60762 (Andromeda, 2025-10-17) was reported at signal
	// to noise 8.5 in 17 subs; 15 of them in Red show the spot at 0.8 where
	// a fake of its light shows at 2.6. 34178 (Horsehead Nebula,
	// 2025-11-16): 4 of 10 subs in Green, 1.7 against 2.5.
	cases := []struct {
		name          string
		obs, inj      float64
		n, subs       int
		want          ownimage.StackVerdict
		wantEvidence  string
		wantFailCheck bool
	}{
		{"60762", 0.8, 2.6, 15, 17, ownimage.StackRefuted, "too faint to have been found", true},
		{"34178", 1.7, 2.5, 4, 10, ownimage.StackRefuted, "too faint to have been found", true},
		{"faint real light at the spot", 4.8, 4.4, 12, 12, ownimage.StackUntested, "", false},
		{"reported light at the hold", 3.0, 4.6, 12, 12, ownimage.StackUntested, "", false},
		{fewerFrames, 1.0, 2.6, 4, 16, ownimage.StackUntested, "", false},
	}
	for _, c := range cases {
		e := auditLight / c.inj
		n := ownimage.FluxNight{Night: "2025-10-17", Filter: lum, N: c.n, Flux: c.obs * e, Err: e, Local: e, Empty99: 2.6 * e, Flagged: true}
		reps := auditReps("2025-10-17")
		for k, r := range reps {
			r.subs = c.subs
			reps[k] = r
		}
		ch, v := transientStack(curveOf(n), reps)
		if v != c.want {
			t.Errorf("%s: %s, want %s: %s", c.name, v, c.want, ch.Evidence)
		}
		if (ch.Status == check.Fail) != c.wantFailCheck || !strings.Contains(ch.Evidence, c.wantEvidence) {
			t.Errorf("%s: %s %q", c.name, ch.Status, ch.Evidence)
		}
	}
}

func TestStackRefutesASpotTooWeakToHaveBeenFound(t *testing.T) {
	t.Parallel()
	// not-sure-4: the frames that found them, stacked at the spot, against
	// the fake of the light the detection reported.
	cases := []struct {
		name     string
		obs, inj float64
		n, subs  int
		want     ownimage.StackVerdict
	}{
		{"43178, Orion H-a nebula", 3.3, 11.3, 16, 16, ownimage.StackRefuted},
		{"43187, Orion H-a nebula", 3.4, 14.8, 16, 16, ownimage.StackRefuted},
		{"42416, Rosette Nebula H-a", 4.0, 8.9, 10, 10, ownimage.StackRefuted},
		{case182, 6.2, 5.8, 12, 12, ownimage.StackConfirmed},
		{"light the detection could find, short of its fake", 4.6, 9.0, 12, 12, ownimage.StackDoubtful},
		{"39197, 2 of the 8 subs tested", 5.1, 12.1, 2, 8, ownimage.StackDoubtful},
	}
	for _, c := range cases {
		e := auditLight / c.inj
		n := ownimage.FluxNight{Night: "2025-01-24", Filter: lum, N: c.n, Flux: c.obs * e, Err: e, Local: e, Empty99: 2.4 * e, Flagged: true}
		reps := auditReps("2025-01-24")
		for k, r := range reps {
			r.subs = c.subs
			reps[k] = r
		}
		ch, v := transientStack(curveOf(n), reps)
		if v != c.want {
			t.Errorf("%s: %s, want %s: %s", c.name, v, c.want, ch.Evidence)
		}
		if v == ownimage.StackRefuted && !strings.Contains(ch.Evidence, "too faint to have been found") {
			t.Errorf("%s: %q", c.name, ch.Evidence)
		}
	}
}

func TestStackRunsOnAllTheFiltersThatFoundIt(t *testing.T) {
	t.Parallel()
	// The nova audit's local stacks of 2025-10-26 and 10-27 in M 31, each
	// filter and all filters against the same fake: 60353, a bulge
	// residual, stood out in Blue only; AT 2025abrs, a real nova, in all.
	filterNight := func(night, filter string, n int, z, inj float64) ownimage.FluxNight {
		e := auditLight / inj
		return ownimage.FluxNight{Night: night, Filter: filter, N: n, Flux: z * e, Err: e, Local: e, Empty99: 2.4 * e, Flagged: true}
	}
	cases := []struct {
		name   string
		night  string
		nights []ownimage.FluxNight
		subs   int
		want   ownimage.StackVerdict
	}{
		{"60353", "2025-10-27", []ownimage.FluxNight{
			filterNight("2025-10-27", "Blue", 13, 3.4, 5.2), filterNight("2025-10-27", "Red", 16, 0.4, 4.1), filterNight("2025-10-27", "Green", 16, -0.7, 4.7),
			filterNight("2025-10-27", ownimage.AllFilters, 45, 1.1, 9.0)}, 13, ownimage.StackRefuted},
		{"AT 2025abrs", "2025-10-26", []ownimage.FluxNight{
			filterNight("2025-10-26", "Blue", 9, 6.9, 10.3), filterNight("2025-10-26", "Red", 9, 5.0, 8.8), filterNight("2025-10-26", "Green", 9, 6.0, 9.8),
			filterNight("2025-10-26", ownimage.AllFilters, 27, 10.3, 16.6)}, 27, ownimage.StackConfirmed},
	}
	for _, c := range cases {
		reps := auditReps(c.night)
		for k, r := range reps {
			r.subs = c.subs
			reps[k] = r
		}
		ch, v := transientStack(curveOf(c.nights...), reps)
		if v != c.want || !strings.Contains(ch.Evidence, "in "+ownimage.AllFilters) {
			t.Errorf("%s: %s, want %s over all filters: %s", c.name, v, c.want, ch.Evidence)
		}
	}
}

func TestStackUnderTheDetectionHoldDoesNotConfirm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		obs, inj float64
		n, subs  int
		want     ownimage.StackVerdict
	}{
		{"13924, Crab Nebula", 4.4, 5.6, 5, 5, ownimage.StackDoubtful},
		{"39478, Cygnus Loop Panel 2", 4.0, 5.6, 2, 2, ownimage.StackDoubtful},
		{case182, 6.2, 5.8, 12, 12, ownimage.StackConfirmed},
		{fewerFrames, 4.0, 5.6, 2, 8, ownimage.StackConfirmed},
	}
	for _, c := range cases {
		e := auditLight / c.inj
		n := ownimage.FluxNight{Night: "2026-09-13", Filter: lum, N: c.n, Flux: c.obs * e, Err: e, Local: e, Empty99: 2.4 * e, Flagged: true}
		reps := auditReps("2026-09-13")
		for k, r := range reps {
			r.subs = c.subs
			reps[k] = r
		}
		if ch, v := transientStack(curveOf(n), reps); v != c.want {
			t.Errorf("%s: %s, want %s: %s", c.name, v, c.want, ch.Evidence)
		}
	}
}

func TestStackRefutesLightLostInAStarsWing(t *testing.T) {
	t.Parallel()
	wingCurve := func(sigma, err, floor, nightErr float64, n int) *ownimage.FluxCurve {
		fc := curveOf(auditNight("2025-11-02", 0.5, 2.0, true))
		fc.Wing = &ownimage.Wing{G: 16.3, SepPx: 6.0, Night: "2025-11-02", Filter: lum, Excess: sigma * err, Sigma: sigma, Err: err,
			Floor: floor, NightErr: nightErr, N: n, Far: 5, Measured: true}
		return fc
	}
	reps := func(subs int) map[string]reported {
		r := auditReps("2025-11-02")
		k := discoveries.GroupBroadband + "\x00" + "2025-11-02"
		x := r[k]
		x.subs = subs
		r[k] = x
		return r
	}
	// 35030 (Horsehead Nebula, 2025-11-02): the reported light stood 2.6
	// times the wing's own night to night wobble, the spot 2.3.
	floor := auditLight / 2.6
	c, v := transientStack(wingCurve(2.3, floor, floor, floor/3, 12), reps(12))
	if v != ownimage.StackRefuted || c.Status != check.Fail || !strings.Contains(c.Evidence, "cannot be told from the star's wing") {
		t.Errorf("35030: %s %s %s", v, c.Status, c.Evidence)
	}
	// 54894 (Crab Nebula, 2026-02-05): a fake at 2.8 in the wing fit, its
	// error from the night's own subs.
	e := auditLight / 2.8
	if _, v := transientStack(wingCurve(0.4, e, e/10, e, 12), reps(12)); v != ownimage.StackRefuted {
		t.Errorf("54894: %s", v)
	}
	// The same with four times the subs tested would stand at 5.6.
	if _, v := transientStack(wingCurve(0.4, e, e/10, e, 12), reps(48)); v != ownimage.StackUntested {
		t.Errorf("more subs could lift it clear: %s", v)
	}
	// AT 2026uti-like light standing clear of the wing is not refuted.
	if _, v := transientStack(wingCurve(9.5, e, e/10, e, 12), reps(12)); v == ownimage.StackRefuted {
		t.Errorf("new light beside a star refuted: %s", v)
	}
	st := ownOnly()
	st[check.TransStack], st[check.TransWing] = check.Fail, check.Fail
	if s := transientSuggest(transientGroups(st), transientFacts{stackV: ownimage.StackRefuted}); s.State != app.VetArtifact || s.Say != sayWing {
		t.Errorf("wing and refuted stack: %+v", s)
	}
}

func TestStackRunsOnTheFilterTheLightWasFoundIn(t *testing.T) {
	t.Parallel()
	// 17028 (Cygnus Loop Panel 2, 2026-08-10): found in H-a at signal to
	// noise 8.1 over 6 subs; the night's all filters stood higher than its
	// H-a, and the detection reported no broadband light for them.
	const night = "2026-08-10"
	all := zNight(night, ownimage.AllFilters, 3.4, 18, true)
	h := zNight(night, ha, 2.3, 6, true)
	reps := map[string]reported{discoveries.GroupOf(ha) + "\x00" + night: {mag: 25 - 2.5*math.Log10(8.1*10), ok: true}}
	c, v := transientStack(curveOf(all, h), reps)
	if v == ownimage.StackUntested || strings.Contains(c.Evidence, "reported no new light") || !strings.Contains(c.Evidence, "in "+ha) {
		t.Errorf("17028: %s %s", v, c.Evidence)
	}
	if v != ownimage.StackRefuted && v != ownimage.StackDoubtful {
		t.Errorf("17028 at 2.3 where its light shows at 8.1: %s %s", v, c.Evidence)
	}
	c, v = transientStack(curveOf(all, h), map[string]reported{})
	if v != ownimage.StackUntested || !strings.Contains(c.Evidence, "reported no new light in all filters") {
		t.Errorf("no light reported anywhere: %s %s", v, c.Evidence)
	}
}

func TestStackRefutesAtTheDetectionHold(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		obs, inj float64
		n, subs  int
		want     ownimage.StackVerdict
	}{
		{"fake between the hold and 5, spot no higher than empty places", 1.0, 4.7, 7, 7, ownimage.StackRefuted},
		{"fake between the hold and 5, spot above empty places", 3.0, 4.7, 7, 7, ownimage.StackUntested},
		{"faint real light near the hold is not confirmed", 4.6, 4.7, 7, 7, ownimage.StackUntested},
		{fewerFrames, 1.0, 4.7, 4, 16, ownimage.StackUntested},
		{"fake under the hold, spot under it too", 1.0, 4.4, 7, 7, ownimage.StackRefuted},
	}
	for _, c := range cases {
		e := auditLight / c.inj
		n := ownimage.FluxNight{Night: wingNight, Filter: lum, N: c.n, Flux: c.obs * e, Err: e, Local: e, Empty99: 2.6 * e, Flagged: true}
		reps := auditReps(wingNight)
		for k, r := range reps {
			r.subs = c.subs
			reps[k] = r
		}
		if ch, v := transientStack(curveOf(n), reps); v != c.want {
			t.Errorf("%s: %s, want %s: %s", c.name, v, c.want, ch.Evidence)
		}
	}
}

func TestStackWingFitIgnoresPlainUnusedFrames(t *testing.T) {
	t.Parallel()
	// 56176 (Caldwell 14, 2026-01-10), 9.8 px from a G 15.8 star: 0.6 sigma
	// in the wing fit where a fake shows at 7.1, and 2.9 on 2026-01-13 in a
	// plain fit that holds the star's wing.
	fc := curveOf(auditNight("2026-01-10", 2.6, 6.0, true), auditNight("2026-01-13", 2.9, 6.6, false))
	e := auditLight / 7.1
	fc.Wing = &ownimage.Wing{G: 15.8, SepPx: 9.8, Night: "2026-01-10", Filter: lum, Excess: 0.6 * e, Sigma: 0.6, Err: e, Measured: true}
	c, v := transientStack(fc, auditReps("2026-01-10"))
	if v != ownimage.StackRefuted {
		t.Errorf("56176: %s %s", v, c.Evidence)
	}
	st := ownOnly()
	st[check.TransStack], st[check.TransWing] = check.Fail, check.Fail
	if s := transientSuggest(transientGroups(st), transientFacts{stackV: v}); s.Say != sayWing {
		t.Errorf("56176: %+v", s)
	}
	// 54896 (Crab Nebula, 2026-02-05): 1.2 sigma in the wing fit where a
	// fake shows at 4.3, and the night's 7 unused frames flat at -0.2.
	crab := curveOf(auditNight("2026-02-05", 1.0, 4.3, true))
	crab.Nights[0].FreshN, crab.Nights[0].FreshFlux, crab.Nights[0].FreshErr = 7, -0.2*auditLight/7.8, auditLight/7.8
	ce := auditLight / 4.3
	crab.Wing = &ownimage.Wing{G: 13.8, SepPx: 6.6, Night: "2026-02-05", Filter: lum, Excess: 1.2 * ce, Sigma: 1.2, Err: ce, Measured: true}
	if _, v := transientStack(crab, auditReps("2026-02-05")); v != ownimage.StackRefuted {
		t.Errorf("54896: %s", v)
	}
	fc.Wing.Sigma, fc.Wing.Excess = 9.5, 9.5*e
	if _, v := transientStack(fc, auditReps("2026-01-10")); v != ownimage.StackConfirmed {
		t.Errorf("new light beside a star not confirmed: %s", v)
	}
}

func TestDoubtfulStackInABrightStarsHaloIsAnArtifact(t *testing.T) {
	t.Parallel()
	// 33729 (Horsehead Nebula, 2025-11-17), 6.2' from Alnilam: 4.0 where a
	// fake shows at 10.0, above the empty places' 2.5.
	st := ownOnly()
	st[check.TransStack], st[check.TransClear] = check.Fail, check.Fail
	halo := transientFacts{stackV: ownimage.StackDoubtful, flux: &ownimage.FluxCurve{Halo: &ownimage.Halo{V: 1.69, SepArcsec: 372, Reach: 600}}}
	if s := transientSuggest(transientGroups(st), halo); s.State != app.VetArtifact || !strings.Contains(s.Say, "halo") {
		t.Errorf("33729: %+v", s)
	}
	if s := transientSuggest(transientGroups(st), transientFacts{stackV: ownimage.StackDoubtful, flux: &ownimage.FluxCurve{}}); s.State != app.VetUnsure {
		t.Errorf("doubtful by the edge: %+v", s)
	}
	st[check.TransStack] = check.Pass
	if s := transientSuggest(transientGroups(st), transientFacts{stackV: ownimage.StackConfirmed, flux: halo.flux}); s.State == app.VetArtifact {
		t.Errorf("confirmed light in a halo refuted: %+v", s)
	}
}

// shareNight is a night of n Blue frames that shows z where the reported
// light, before its master's share, shows at inj, with empty places
// under 2.5 in 99 of 100, and its master's share and count of subs.
func shareNight(night string, n int, z, inj, shared float64, subs int) ownimage.FluxNight {
	e := auditLight / inj
	return ownimage.FluxNight{Night: night, Filter: "Blue", N: n, Flux: z * e, Err: e, Local: e, Empty99: 2.5 * e, Flagged: true, Shared: shared, MasterSubs: subs}
}

func TestStackPutsTheFakeInAtTheShareTheMasterLeaves(t *testing.T) {
	t.Parallel()
	// 67098 (Horsehead Nebula, 2025-11-21): 4.1 against a fake of 7.7 over
	// 7 Blue frames, 53%. Its Blue master takes 20.3% of its weight from
	// that night, but 40 subs of 11 other nights give Blue a level that
	// takes the master's share away again, so the curve carries no share,
	// and 4.1 over the 7 subs the detection tested is too faint to have
	// been found.
	reps := auditReps("2025-11-21")
	_, v := transientStack(curveOf(shareNight("2025-11-21", 7, 4.1, 7.7, 0, 0)), reps)
	if v != ownimage.StackRefuted {
		t.Errorf("67098 with its Blue level: %s, want refuted", v)
	}
	// Were there no such level, the master's 20.3% would stay taken away:
	// the fake then shows at 7.7 times 0.797, 6.1, and 4.1 is 67% of it,
	// over the 60% that confirms.
	c, v := transientStack(curveOf(shareNight("2025-11-21", 7, 4.1, 7.7, 0.203, 45)), reps)
	if v != ownimage.StackConfirmed || !strings.Contains(c.Evidence, "shows at 6.1") || !strings.Contains(c.Evidence, "takes 20% of its weight") {
		t.Errorf("67098 with no Blue level: %s %s", v, c.Evidence)
	}
	// M 15's 2026-10-18 Green master of 2 subs, both that night: all the
	// night's light cancels itself, and the stack is not judged.
	m15 := auditReps("2026-10-18")
	c, v = transientStack(curveOf(shareNight("2026-10-18", 2, 0.0, 6.0, 1, 2)), m15)
	if v != ownimage.StackUntested || c.Status != check.NotRun || !strings.Contains(c.Evidence, "Not judged") {
		t.Errorf("M 15's 2-sub Green master: %s %s %s", v, c.Status, c.Evidence)
	}
	// M 15's Blue master: 2 subs of 2026-10-16 and 5 of 2026-10-18, too
	// few unflagged ones for a level, 62% of its weight from the flagged
	// night.
	if _, v := transientStack(curveOf(shareNight("2026-10-18", 5, 1.0, 6.0, 0.618, 7)), m15); v != ownimage.StackUntested {
		t.Errorf("M 15's Blue master: %s, want untested", v)
	}
	if c := fluxCheck(curveOf(shareNight("2026-10-18", 2, 0.0, 6.0, 1, 2)), m15); c.Status != check.NotRun {
		t.Errorf("flux check on a master of the night's own subs: %s %s", c.Status, c.Evidence)
	}
}
